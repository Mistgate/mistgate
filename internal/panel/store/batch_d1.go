//go:build js && wasm

package store

import (
	"context"

	"github.com/mistgate/mistgate/edge/d1driver"
)

func (s *Store) batchStore(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	batch := make([]d1driver.Statement, len(stmts))
	for i, stmt := range stmts {
		batch[i] = d1driver.Statement{Query: stmt.Query, Args: stmt.Args, Returning: stmt.Returning}
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
