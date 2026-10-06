//go:build js && wasm

package d1driver

import (
	"context"
	"database/sql"
	"errors"
	"syscall/js"
	"testing"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db := Open(js.Global().Get("__d1"))
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestD1ValueRoundTripAndExecResults(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE d1driver_values (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		integer_value INTEGER,
		float_value REAL,
		text_value TEXT,
		blob_value BLOB,
		null_value TEXT,
		bool_value INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `INSERT INTO d1driver_values
		(integer_value, float_value, text_value, blob_value, null_value, bool_value)
		VALUES (?, ?, ?, ?, ?, ?)`, int64(42), 1.25, "hello", []byte{0, 1, 255}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		t.Fatalf("RowsAffected() = %d, %v; want 1", affected, err)
	}
	if id, err := result.LastInsertId(); err != nil || id != 1 {
		t.Fatalf("LastInsertId() = %d, %v; want 1", id, err)
	}
	var integer int64
	var decimal float64
	var text string
	var blob []byte
	var nullable sql.NullString
	var boolean int64
	if err := db.QueryRowContext(ctx, `SELECT integer_value, float_value, text_value, blob_value, null_value, bool_value
		FROM d1driver_values`).Scan(&integer, &decimal, &text, &blob, &nullable, &boolean); err != nil {
		t.Fatal(err)
	}
	if integer != 42 || decimal != 1.25 || text != "hello" || string(blob) != string([]byte{0, 1, 255}) || nullable.Valid || boolean != 1 {
		t.Fatalf("round trip: integer=%d float=%v text=%q blob=%v null=%+v bool=%d", integer, decimal, text, blob, nullable, boolean)
	}
}

func TestD1RejectsIntegersOutsideSafeRange(t *testing.T) {
	db := testDB(t)
	if _, err := db.ExecContext(context.Background(), `SELECT ?`, int64((1<<53)+1)); !errors.Is(err, ErrUnsafeInteger) {
		t.Fatalf("binding unsafe integer: %v", err)
	}
	var got int64
	err := db.QueryRowContext(context.Background(), `SELECT CAST(9007199254740993 AS INTEGER)`).Scan(&got)
	if !errors.Is(err, ErrUnsafeInteger) {
		t.Fatalf("reading unsafe integer: %v", err)
	}
}

func TestD1BufferedBatchIsAtomic(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE d1driver_batch_values (value INTEGER UNIQUE)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO d1driver_batch_values (value) VALUES (?)`, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO d1driver_batch_values (value) VALUES (?)`, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("duplicate batch unexpectedly committed")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM d1driver_batch_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed batch left %d rows", count)
	}
}

func TestD1BatchResultsAndRollback(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE d1driver_batch_results (id INTEGER PRIMARY KEY, value TEXT UNIQUE)`); err != nil {
		t.Fatal(err)
	}
	results, err := BatchResultsDB(ctx, db, []Statement{
		{Query: `INSERT INTO d1driver_batch_results (id, value) VALUES (1, 'first')`},
		{Query: `UPDATE d1driver_batch_results SET value = 'changed' WHERE id = 1 RETURNING id AS row_id, value AS updated_value`, Returning: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].RowsAffected != 1 || results[1].RowsAffected != 1 || len(results[1].Rows) != 1 {
		t.Fatalf("batch results = %+v", results)
	}
	if results[1].Rows[0][0] != int64(1) || results[1].Rows[0][1] != "changed" {
		t.Fatalf("RETURNING row = %#v", results[1].Rows[0])
	}
	if _, err := BatchResultsDB(ctx, db, []Statement{{Query: `SELECT 42 AS "3", 17 AS "1"`, Returning: true}}); err == nil || err.Error() != `d1driver: batch column "1" has an integer-like name; give it an alias` {
		t.Fatalf("integer-like column error = %v", err)
	}
	ordered, err := BatchResultsDB(ctx, db, []Statement{{Query: `SELECT 42 AS first_value, 17 AS second_value`, Returning: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 1 || len(ordered[0].Rows) != 1 || ordered[0].Rows[0][0] != int64(42) || ordered[0].Rows[0][1] != int64(17) {
		t.Fatalf("ordered aliased columns = %+v", ordered)
	}
	if _, err := BatchResultsDB(ctx, db, []Statement{
		{Query: `INSERT INTO d1driver_batch_results (id, value) VALUES (2, 'second')`},
		{Query: `INSERT INTO d1driver_batch_results (id, value) VALUES (3, 'changed')`},
	}); err == nil {
		t.Fatal("batch with a constraint violation succeeded")
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM d1driver_batch_results`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed batch left %d rows, want only the earlier committed row", count)
	}
}

func TestD1RollbackDropsBufferedWrites(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE d1driver_rollback_values (value INTEGER)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO d1driver_rollback_values (value) VALUES (?)`, 1); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM d1driver_rollback_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rollback left %d rows", count)
	}
}

func TestD1TransactionReadAndDeferredResultErrors(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE d1driver_tx_values (value INTEGER)`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.QueryContext(ctx, `SELECT 1`); !errors.Is(err, ErrReadInTx) {
		t.Fatalf("QueryContext in transaction: %v", err)
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO d1driver_tx_values (value) VALUES (?)`, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := result.RowsAffected(); !errors.Is(err, ErrDeferredResult) {
		t.Fatalf("RowsAffected before commit: %v", err)
	}
	if _, err := result.LastInsertId(); !errors.Is(err, ErrDeferredResult) {
		t.Fatalf("LastInsertId before commit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestD1ContextCancellationWhileAwaiting(t *testing.T) {
	db := testDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := db.ExecContext(context.Background(), `CREATE TABLE d1driver_cancel_values (value INTEGER)`); err != nil {
		t.Fatal(err)
	}
	binding := js.Global().Get("__d1")
	awaiting := make(chan struct{}, 1)
	callback := js.FuncOf(func(js.Value, []js.Value) any {
		awaiting <- struct{}{}
		return nil
	})
	defer callback.Release()
	binding.Call("__onNextAwait", callback)
	binding.Call("__holdNextRun")
	finished := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, `INSERT INTO d1driver_cancel_values (value) VALUES (1)`)
		finished <- err
	}()
	<-awaiting
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecContext after cancellation: %v", err)
	}
}

func TestD1DuplicateColumnNamesPreserveValuesInOrder(t *testing.T) {
	db := testDB(t)
	rows, err := db.QueryContext(context.Background(), `SELECT 'left' AS name, 'right' AS name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 || columns[0] != "name" || columns[1] != "name" {
		t.Fatalf("Columns() = %v, want [name name]", columns)
	}
	if !rows.Next() {
		t.Fatalf("Next() = false, err = %v", rows.Err())
	}
	var first, second string
	if err := rows.Scan(&first, &second); err != nil {
		t.Fatal(err)
	}
	if first != "left" || second != "right" {
		t.Fatalf("row = (%q, %q), want (left, right)", first, second)
	}
}

func TestD1IntegerLikeColumnNamePreservesOrder(t *testing.T) {
	db := testDB(t)
	rows, err := db.QueryContext(context.Background(), `SELECT 'x' AS name, 1 AS "1"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 || columns[0] != "name" || columns[1] != "1" {
		t.Fatalf("Columns() = %v, want [name 1]", columns)
	}
	if !rows.Next() {
		t.Fatalf("Next() = false, err = %v", rows.Err())
	}
	var name string
	var number int64
	if err := rows.Scan(&name, &number); err != nil {
		t.Fatal(err)
	}
	if name != "x" || number != 1 {
		t.Fatalf("row = (%q, %d), want (x, 1)", name, number)
	}
}

func TestD1EmptyResultRetainsColumns(t *testing.T) {
	db := testDB(t)
	rows, err := db.QueryContext(context.Background(), `SELECT 1 AS number_value, 'x' AS text_value WHERE 0`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 || columns[0] != "number_value" || columns[1] != "text_value" {
		t.Fatalf("Columns() = %v, want [number_value text_value]", columns)
	}
	if rows.Next() {
		t.Fatal("empty result unexpectedly returned a row")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestD1RawRowsPreserveNullAndBlobValues(t *testing.T) {
	db := testDB(t)
	rows, err := db.QueryContext(context.Background(), `SELECT NULL AS null_value, ? AS blob_value`, []byte{0, 1, 255})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatalf("Next() = false, err = %v", rows.Err())
	}
	var nullValue sql.NullString
	var blob []byte
	if err := rows.Scan(&nullValue, &blob); err != nil {
		t.Fatal(err)
	}
	if nullValue.Valid || string(blob) != string([]byte{0, 1, 255}) {
		t.Fatalf("row = (null=%+v, blob=%v), want (NULL, [0 1 255])", nullValue, blob)
	}
}
