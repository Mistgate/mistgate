//go:build !js

package store

import (
	"context"
	"database/sql"
	"strings"
)

func (s *Store) batchStore(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	db := s.W
	if len(stmts) > 0 {
		readOnly := true
		for _, stmt := range stmts {
			query := strings.ToUpper(strings.TrimSpace(stmt.Query))
			if !stmt.Returning || !strings.HasPrefix(query, "SELECT") {
				readOnly = false
				break
			}
		}
		if readOnly {
			db = s.R
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
			rows, err := tx.QueryContext(ctx, stmt.Query, stmt.Args...)
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
			if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt.Query)), "SELECT") {
				results[i].RowsAffected = int64(len(values))
			}
			continue
		}
		res, err := tx.ExecContext(ctx, stmt.Query, stmt.Args...)
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
