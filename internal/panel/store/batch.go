package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// errGuard: a guard statement of the batch found its condition false; nothing in the batch was written.
var errGuard = errors.New("store: batch guard failed")

// Stmt is one statement in an atomic store batch.
type Stmt struct {
	Query string
	Args  []any
	// Returning collects rows after the statement runs. Every returned column must
	// have a plain unique name: a table column or an expression with AS name.
	Returning bool
}

// StmtResult is the outcome of one statement in an atomic store batch.
type StmtResult struct {
	RowsAffected int64
	Rows         [][]any
}

// reads keeps each batch statement paired with the scanner that consumes only its rows.
type reads struct {
	stmts []Stmt
	scans []func(rows [][]any) error
}

func (r *reads) add(scan func(rows [][]any) error, query string, args ...any) {
	r.stmts = append(r.stmts, Stmt{Query: query, Args: args, Returning: true})
	r.scans = append(r.scans, scan)
}

func appendRows[T any](dst *[]T, scan func(rowScanner) (T, error)) func([][]any) error {
	return func(rows [][]any) error {
		for _, row := range rows {
			value, err := scan(batchRow(row))
			if err != nil {
				return err
			}
			*dst = append(*dst, value)
		}
		return nil
	}
}

func oneRow[T any](dst *T, scan func(rowScanner) (T, error)) func([][]any) error {
	return func(rows [][]any) error {
		if len(rows) != 1 {
			return ErrNotFound
		}
		value, err := scan(batchRow(rows[0]))
		if err != nil {
			return err
		}
		*dst = value
		return nil
	}
}

func maybeOneRow[T any](dst *T, scan func(rowScanner) (T, error)) func([][]any) error {
	return func(rows [][]any) error {
		if len(rows) == 0 {
			return nil
		}
		if len(rows) != 1 {
			return errors.New("store: read batch returned multiple rows")
		}
		value, err := scan(batchRow(rows[0]))
		if err != nil {
			return err
		}
		*dst = value
		return nil
	}
}

func scanString(r rowScanner) (string, error) {
	var value sql.NullString
	if err := r.Scan(&value); err != nil {
		return "", err
	}
	return value.String, nil
}

func (r *reads) run(ctx context.Context, s *Store) error {
	if len(r.stmts) == 0 {
		return nil
	}
	results, err := s.read(ctx, r.stmts...)
	if err != nil {
		return err
	}
	if len(results) != len(r.scans) {
		return errors.New("store: unexpected read batch result count")
	}
	for i, result := range results {
		if err := r.scans[i](result.Rows); err != nil {
			return err
		}
	}
	return nil
}

func scanRowValues(r rowScanner, n int) ([]any, error) {
	if row, ok := r.(batchRow); ok {
		if len(row) != n {
			return nil, fmt.Errorf("store: batch row has %d values, want %d", len(row), n)
		}
		return []any(row), nil
	}
	values := make([]any, n)
	dest := make([]any, n)
	for i := range values {
		dest[i] = &values[i]
	}
	if err := r.Scan(dest...); err != nil {
		return nil, err
	}
	return values, nil
}

type batchRow []any

func (r batchRow) Scan(dest ...any) error {
	if len(dest) != len(r) {
		return fmt.Errorf("store: batch row has %d values, want %d", len(r), len(dest))
	}
	for i, value := range r {
		switch target := dest[i].(type) {
		case *sql.NullString:
			if value == nil {
				target.Valid = false
				continue
			}
			v, ok := value.(string)
			if !ok {
				return fmt.Errorf("store: batch column %d is %T, want nullable string", i, value)
			}
			target.String, target.Valid = v, true
		case *sql.NullInt64:
			if value == nil {
				target.Valid = false
				continue
			}
			v, ok := value.(int64)
			if !ok {
				return fmt.Errorf("store: batch column %d is %T, want nullable integer", i, value)
			}
			target.Int64, target.Valid = v, true
		case *string:
			if value == nil {
				*target = ""
				continue
			}
			v, ok := value.(string)
			if !ok {
				return fmt.Errorf("store: batch column %d is %T, want string", i, value)
			}
			*target = v
		case *[]byte:
			if value == nil {
				*target = nil
				continue
			}
			v, ok := value.([]byte)
			if !ok {
				return fmt.Errorf("store: batch column %d is %T, want bytes", i, value)
			}
			*target = append((*target)[:0], v...)
		case *int:
			v, ok := value.(int64)
			if !ok {
				return fmt.Errorf("store: batch column %d is %T, want integer", i, value)
			}
			*target = int(v)
		case *int64:
			v, ok := value.(int64)
			if !ok {
				return fmt.Errorf("store: batch column %d is %T, want integer", i, value)
			}
			*target = v
		case *bool:
			switch v := value.(type) {
			case bool:
				*target = v
			case int64:
				if v != 0 && v != 1 {
					return fmt.Errorf("store: batch column %d is %d, want boolean", i, v)
				}
				*target = v != 0
			default:
				return fmt.Errorf("store: batch column %d is %T, want boolean", i, value)
			}
		case *uint64:
			v, ok := value.(int64)
			if !ok || v < 0 {
				return fmt.Errorf("store: batch column %d is %T, want non-negative integer", i, value)
			}
			*target = uint64(v)
		case *uint32:
			v, ok := value.(int64)
			if !ok || v < 0 || uint64(v) > uint64(^uint32(0)) {
				return fmt.Errorf("store: batch column %d is %T, want non-negative uint32", i, value)
			}
			*target = uint32(v)
		default:
			return fmt.Errorf("store: unsupported batch scan target %T", dest[i])
		}
	}
	return nil
}

// guard is a batch statement that aborts the whole batch with errGuard unless
// cond holds when the batch runs. A NULL cond counts as false.
func guard(cond string, args ...any) Stmt {
	return Stmt{Query: `INSERT INTO batch_guard (failed) SELECT 1 WHERE coalesce((` + cond + `), 0) = 0`, Args: args}
}

// batch runs a fixed list of statements atomically and returns each statement's
// outcome after the batch has completed. A one-write batch uses a guarded write
// (WHERE <cond>) and RowsAffected, with an earlier diagnostic SELECT in the same
// batch choosing the failure reason. Several writes that must all happen or none
// start with guard(cond), followed by plain writes; on errGuard, a plain read
// after the batch chooses the error message while the guard supplies atomicity.
func (s *Store) batch(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	results, err := s.batchStore(ctx, stmts...)
	if err != nil && strings.Contains(err.Error(), "CHECK constraint failed: batch_guard") {
		return nil, errGuard
	}
	return results, err
}

// read runs a read-only batch on the reader pool. D1 uses the same binding as writes.
func (s *Store) read(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	for _, stmt := range stmts {
		query := strings.ToUpper(strings.TrimSpace(stmt.Query))
		if !stmt.Returning || !strings.HasPrefix(query, "SELECT") {
			return nil, errors.New("store: read batch requires SELECT statements")
		}
	}
	return s.readStore(ctx, stmts...)
}

// retryGuarded rebuilds a guarded batch after a concurrent writer makes its snapshot stale. Once a guard fails,
// local retries serialize so they reread the state left by the winning writer instead of colliding repeatedly.
func (s *Store) retryGuarded(ctx context.Context, attempt func() ([]Stmt, error)) ([]StmtResult, error) {
	locked := false
	defer func() {
		if locked {
			s.unlockBatchRetry()
		}
	}()
	for range 8 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		stmts, err := attempt()
		if err != nil {
			return nil, err
		}
		if len(stmts) == 0 {
			return nil, nil
		}
		results, err := s.batch(ctx, stmts...)
		if errors.Is(err, errGuard) {
			if !locked {
				if err := s.lockBatchRetry(ctx); err != nil {
					return nil, err
				}
				locked = true
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		return results, nil
	}
	return nil, ErrConflict
}
