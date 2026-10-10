//go:build !js

package store

import (
	"context"
	"database/sql"
)

func (s *Store) batchStore(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	return runSQLBatch(ctx, s.W, &s.writeStmtCache, stmts...)
}

func (s *Store) readStore(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	if s.readGate != nil {
		if err := s.acquireReader(ctx); err != nil {
			return nil, err
		}
		defer func() { <-s.readGate }()
	}
	return runSQLBatch(ctx, s.R, &s.readStmtCache, stmts...)
}

func (s *Store) acquireReader(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case s.readGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-s.readGate
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func runSQLBatch(ctx context.Context, db *sql.DB, cache *batchStmtCache, stmts ...Stmt) ([]StmtResult, error) {
	prepared := make([]*sql.Stmt, len(stmts))
	if cache != nil {
		for i, stmt := range stmts {
			var err error
			prepared[i], err = cache.prepare(ctx, db, stmt.Query)
			if err != nil {
				return nil, err
			}
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	results := make([]StmtResult, len(stmts))
	for i, stmt := range stmts {
		if stmt.Returning {
			var rows *sql.Rows
			if prepared[i] != nil {
				rows, err = tx.StmtContext(ctx, prepared[i]).QueryContext(ctx, stmt.Args...)
			} else {
				rows, err = tx.QueryContext(ctx, stmt.Query, stmt.Args...)
			}
			if err != nil {
				return nil, err
			}
			values, err := collectRows(rows)
			closeErr := rows.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
			results[i].Rows = values
			if !hasSQLPrefix(stmt.Query, "SELECT") {
				results[i].RowsAffected = int64(len(values))
			}
			continue
		}
		var res sql.Result
		if prepared[i] != nil {
			res, err = tx.StmtContext(ctx, prepared[i]).ExecContext(ctx, stmt.Args...)
		} else {
			res, err = tx.ExecContext(ctx, stmt.Query, stmt.Args...)
		}
		if err != nil {
			return nil, err
		}
		results[i].RowsAffected, err = res.RowsAffected()
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return results, nil
}

func collectRows(rows *sql.Rows) ([][]any, error) {
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, values)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
