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
