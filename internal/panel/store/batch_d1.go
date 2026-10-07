//go:build js && wasm

package store

import (
	"context"
	"database/sql/driver"

	"github.com/mistgate/mistgate/edge/d1driver"
)

func (s *Store) batchStore(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	batch := make([]d1driver.Statement, len(stmts))
	for i, stmt := range stmts {
		// the same conversion database/sql applies on SQLite (int -> int64, Valuers), so a batch argument behaves alike
		args := make([]any, len(stmt.Args))
		for j, arg := range stmt.Args {
			v, err := driver.DefaultParameterConverter.ConvertValue(arg)
			if err != nil {
				return nil, err
			}
			args[j] = v
		}
		batch[i] = d1driver.Statement{Query: stmt.Query, Args: args, Returning: stmt.Returning}
	}
	results, err := d1driver.BatchResultsDB(ctx, s.W, batch)
	if err != nil {
		return nil, err
	}
	out := make([]StmtResult, len(results))
	for i, result := range results {
		out[i] = StmtResult{RowsAffected: result.RowsAffected, Rows: result.Rows}
	}
	return out, nil
}

func (s *Store) readStore(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	return s.batchStore(ctx, stmts...)
}
