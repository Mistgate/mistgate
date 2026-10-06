//go:build js && wasm

// Package d1driver implements database/sql over a Cloudflare D1 binding.
// D1 exposes SQLite integers as JavaScript numbers. Values outside the safe
// integer range (-(2^53-1) through 2^53-1) are rejected to avoid rounding.
package d1driver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"math"
	"sync"
	"syscall/js"
)

var (
	// ErrReadInTx marks a query in a buffered D1 transaction. D1 cannot provide
	// an interactive read-then-write transaction.
	ErrReadInTx = errors.New("d1driver: query inside a buffered transaction is not supported")
	// ErrDeferredResult marks result fields that are unavailable before a batch commits.
	ErrDeferredResult = errors.New("d1driver: result is deferred until the transaction commits")
	// ErrUnsafeInteger marks an integer that JavaScript cannot represent exactly.
	ErrUnsafeInteger = errors.New("d1driver: integer exceeds JavaScript's safe integer range")
)

const maxSafeInteger = int64(1<<53 - 1)

// Statement is one SQL statement sent as part of an atomic D1 batch.
type Statement struct {
	Query string
	Args  []any
}

// NewConnector creates a database/sql connector around a D1 binding.
func NewConnector(db js.Value) driver.Connector { return &connector{db: db} }

// Open creates a database/sql pool around a D1 binding.
func Open(db js.Value) *sql.DB { return sql.OpenDB(NewConnector(db)) }

type connector struct{ db js.Value }

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &conn{db: c.db}, nil
}

func (c *connector) Driver() driver.Driver { return drv{} }

type drv struct{}

func (drv) Open(string) (driver.Conn, error) {
	return nil, errors.New("d1driver: use a connector created from a D1 binding")
}

// await returns as soon as the context is canceled, even if the JavaScript
// promise remains pending. Its callbacks stay alive until that promise settles.
func await(ctx context.Context, p js.Value) (js.Value, error) {
	if err := ctx.Err(); err != nil {
		return js.Undefined(), err
	}
	type outcome struct {
		value js.Value
		err   error
	}
	settled := make(chan struct{})
	var once sync.Once
	var result outcome
	finish := func(outcome outcome) {
		once.Do(func() {
			result = outcome
			close(settled)
		})
	}
	resolve := js.FuncOf(func(_ js.Value, args []js.Value) any {
		value := js.Undefined()
		if len(args) > 0 {
			value = args[0]
		}
		finish(outcome{value: value})
		return nil
	})
	reject := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		finish(outcome{err: errors.New("d1driver: D1 operation failed")})
		return nil
	})
	_, err := call(p, "then", resolve, reject)
	if err != nil {
		resolve.Release()
		reject.Release()
		return js.Undefined(), err
	}
	go func() {
		<-settled
		resolve.Release()
		reject.Release()
	}()
	select {
	case <-settled:
		return result.value, result.err
	case <-ctx.Done():
		return js.Undefined(), ctx.Err()
	}
}

// Batch sends statements in one D1 batch. D1 guarantees the batch is atomic.
func Batch(ctx context.Context, db js.Value, statements []Statement) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(statements) == 0 {
		return nil
	}
	array := js.Global().Get("Array").New()
	for _, statement := range statements {
		prepared, err := prepare(db, statement.Query, statement.Args)
		if err != nil {
			return err
		}
		array.Call("push", prepared)
	}
	result, err := call(db, "batch", array)
	if err != nil {
		return err
	}
	result, err = await(ctx, result)
	if err != nil {
		return err
	}
	arrayCheck, err := call(js.Global().Get("Array"), "isArray", result)
	if err != nil {
		return err
	}
	if arrayCheck.Bool() {
		for i := 0; i < result.Length(); i++ {
			if err := checkSuccess(result.Index(i)); err != nil {
				return err
			}
		}
		return nil
	}
	return checkSuccess(result)
}

type conn struct {
	db     js.Value
	active *tx
}

func (c *conn) Prepare(query string) (driver.Stmt, error) { return &stmt{conn: c, query: query}, nil }
func (c *conn) Close() error                              { return nil }
func (c *conn) Begin() (driver.Tx, error)                 { return c.BeginTx(context.Background(), driver.TxOptions{}) }

func (c *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.Isolation != driver.IsolationLevel(0) || opts.ReadOnly {
		return nil, errors.New("d1driver: transaction options are not supported")
	}
	if c.active != nil {
		return nil, errors.New("d1driver: nested transaction is not supported")
	}
	t := &tx{conn: c, ctx: ctx}
	c.active = t
	return t, nil
}

func (c *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := positional(args)
	if err != nil {
		return nil, err
	}
	prepared, err := prepare(c.db, query, values)
	if err != nil {
		return nil, err
	}
	if c.active != nil {
		if c.active.closed {
			return nil, errors.New("d1driver: transaction is closed")
		}
		c.active.statements = append(c.active.statements, Statement{Query: query, Args: values})
		return deferredResult{}, nil
	}
	response, err := call(prepared, "run")
	if err != nil {
		return nil, err
	}
	response, err = await(ctx, response)
	if err != nil {
		return nil, err
	}
	if err := checkSuccess(response); err != nil {
		return nil, err
	}
	meta := response.Get("meta")
	if meta.Type() != js.TypeObject || meta.IsNull() {
		return nil, errors.New("d1driver: D1 exec result has no metadata")
	}
	changes, err := metadataInteger(meta.Get("changes"))
	if err != nil {
		return nil, err
	}
	lastID, err := metadataInteger(meta.Get("last_row_id"))
	if err != nil {
		return nil, err
	}
	return result{id: lastID, affected: changes}, nil
}

func (c *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.active != nil {
		return nil, ErrReadInTx
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := positional(args)
	if err != nil {
		return nil, err
	}
	prepared, err := prepare(c.db, query, values)
	if err != nil {
		return nil, err
	}
	options := js.Global().Get("Object").New()
	options.Set("columnNames", true)
	response, err := call(prepared, "raw", options)
	if err != nil {
		return nil, err
	}
	response, err = await(ctx, response)
	if err != nil {
		return nil, err
	}
	data := response
	isArray, err := call(js.Global().Get("Array"), "isArray", data)
	if err != nil {
		return nil, err
	}
	if !isArray.Bool() || data.Length() == 0 {
		return nil, errors.New("d1driver: D1 raw query returned no column names")
	}
	header := data.Index(0)
	isArray, err = call(js.Global().Get("Array"), "isArray", header)
	if err != nil {
		return nil, err
	}
	if !isArray.Bool() {
		return nil, errors.New("d1driver: D1 raw query returned invalid column names")
	}
	columns := make([]string, header.Length())
	for i := range columns {
		columns[i] = header.Index(i).String()
	}
	return &rows{columns: columns, data: data, index: 1}, nil
}

func positional(args []driver.NamedValue) ([]any, error) {
	values := make([]any, len(args))
	for i, arg := range args {
		if arg.Name != "" || arg.Ordinal != i+1 {
			return nil, errors.New("d1driver: only positional parameters are supported")
		}
		if _, err := toJS(arg.Value); err != nil {
			return nil, err
		}
		values[i] = arg.Value
	}
	return values, nil
}

func prepare(db js.Value, query string, args []any) (js.Value, error) {
	prepared, err := call(db, "prepare", query)
	if err != nil {
		return js.Undefined(), err
	}
	if len(args) == 0 {
		return prepared, nil
	}
	jsArgs := make([]any, len(args))
	for i, arg := range args {
		value, err := toJS(arg)
		if err != nil {
			return js.Undefined(), err
		}
		jsArgs[i] = value
	}
	return call(prepared, "bind", jsArgs...)
}

func toJS(value any) (any, error) {
	switch value := value.(type) {
	case nil, string, float64:
		return value, nil
	case bool:
		if value {
			return 1, nil
		}
		return 0, nil
	case int64:
		if value < -maxSafeInteger || value > maxSafeInteger {
			return nil, ErrUnsafeInteger
		}
		return value, nil
	case []byte:
		bytes := js.Global().Get("Uint8Array").New(len(value))
		js.CopyBytesToJS(bytes, value)
		return bytes, nil
	default:
		return nil, errors.New("d1driver: unsupported parameter type")
	}
}

func checkSuccess(result js.Value) error {
	if result.Type() != js.TypeObject || result.IsNull() {
		return errors.New("d1driver: invalid D1 result")
	}
	success := result.Get("success")
	if success.Type() == js.TypeBoolean && !success.Bool() {
		return errors.New("d1driver: D1 operation failed")
	}
	return nil
}

func metadataInteger(value js.Value) (int64, error) {
	if value.Type() == js.TypeUndefined || value.Type() == js.TypeNull {
		return 0, nil
	}
	if value.Type() != js.TypeNumber {
		return 0, errors.New("d1driver: invalid integer metadata")
	}
	n := value.Float()
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || math.Abs(n) > float64(maxSafeInteger) {
		return 0, ErrUnsafeInteger
	}
	return int64(n), nil
}

type result struct {
	id       int64
	affected int64
}

func (r result) LastInsertId() (int64, error) { return r.id, nil }
func (r result) RowsAffected() (int64, error) { return r.affected, nil }

type deferredResult struct{}

func (deferredResult) LastInsertId() (int64, error) { return 0, ErrDeferredResult }
func (deferredResult) RowsAffected() (int64, error) { return 0, ErrDeferredResult }

type tx struct {
	conn       *conn
	ctx        context.Context
	statements []Statement
	closed     bool
}

func (t *tx) Commit() error {
	if t.closed {
		return errors.New("d1driver: transaction is closed")
	}
	t.closed = true
	if t.conn.active == t {
		t.conn.active = nil
	}
	return Batch(t.ctx, t.conn.db, t.statements)
}

func (t *tx) Rollback() error {
	if t.closed {
		return nil
	}
	t.closed = true
	t.statements = nil
	if t.conn.active == t {
		t.conn.active = nil
	}
	return nil
}

type stmt struct {
	conn  *conn
	query string
}

func (s *stmt) Close() error  { return nil }
func (s *stmt) NumInput() int { return -1 }
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(args))
}
func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(args))
}
func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.conn.ExecContext(ctx, s.query, args)
}
func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.conn.QueryContext(ctx, s.query, args)
}

func named(values []driver.Value) []driver.NamedValue {
	args := make([]driver.NamedValue, len(values))
	for i, value := range values {
		args[i] = driver.NamedValue{Ordinal: i + 1, Value: value}
	}
	return args
}

type rows struct {
	columns []string
	data    js.Value
	index   int
}

func (r *rows) Columns() []string { return r.columns }
func (r *rows) Close() error      { return nil }
func (r *rows) Next(dest []driver.Value) error {
	if r.data.Type() == js.TypeUndefined || r.index >= r.data.Length() {
		return io.EOF
	}
	row := r.data.Index(r.index)
	r.index++
	if row.Type() != js.TypeObject || row.IsNull() || row.Length() != len(r.columns) {
		return errors.New("d1driver: D1 raw query returned invalid row")
	}
	for i := range r.columns {
		value, err := fromJS(row.Index(i))
		if err != nil {
			return err
		}
		dest[i] = value
	}
	return nil
}

func fromJS(value js.Value) (driver.Value, error) {
	switch value.Type() {
	case js.TypeNull, js.TypeUndefined:
		return nil, nil
	case js.TypeNumber:
		n := value.Float()
		if math.Trunc(n) == n {
			if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > float64(maxSafeInteger) {
				return nil, ErrUnsafeInteger
			}
			return int64(n), nil
		}
		return n, nil
	case js.TypeString:
		return value.String(), nil
	case js.TypeBoolean:
		if value.Bool() {
			return int64(1), nil
		}
		return int64(0), nil
	case js.TypeObject:
		return blobFromJS(value)
	default:
		return nil, errors.New("d1driver: unsupported result type")
	}
}

func blobFromJS(value js.Value) (driver.Value, error) {
	uint8Array := js.Global().Get("Uint8Array")
	if uint8Array.Type() != js.TypeFunction {
		return nil, errors.New("d1driver: Uint8Array is unavailable")
	}
	var bytes js.Value
	if value.InstanceOf(uint8Array) {
		bytes = value
	} else if value.InstanceOf(js.Global().Get("ArrayBuffer")) {
		bytes = uint8Array.New(value)
	} else {
		return nil, errors.New("d1driver: unsupported object result")
	}
	out := make([]byte, bytes.Length())
	js.CopyBytesToGo(out, bytes)
	return out, nil
}

func call(value js.Value, method string, args ...any) (result js.Value, err error) {
	defer func() {
		if recover() != nil {
			result = js.Undefined()
			err = errors.New("d1driver: D1 binding call failed")
		}
	}()
	return value.Call(method, args...), nil
}
