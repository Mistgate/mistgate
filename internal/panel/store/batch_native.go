//go:build !js

package store

import (
	"context"
	"database/sql"
)

func (s *Store) batchStore(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	return runSQLBatch(ctx, s.W, &s.writeStmtCache, stmts...)
}

// readStore runs a read batch on a reader admitted in arrival order, as BEGIN ... COMMIT on the reader's own
// connection with a context that cannot be cancelled. A database/sql transaction starts a goroutine per transaction
// and per query, and the driver one per statement, to watch a cancellable context: about 25 per subscription fetch.
// On one CPU that overflowed the scheduler's local run queue into the global one, which a busy P drains once in 61
// rounds, and a connection whose net/http read goroutine landed there stalled for up to a second. A read batch is
// short, so it is not worth interrupting; waiting for a reader and for its connection still honours ctx.
func (s *Store) readStore(ctx context.Context, stmts ...Stmt) ([]StmtResult, error) {
	if s.readGate == nil { // a Store not made by Open (tests)
		return runSQLBatch(ctx, s.R, nil, stmts...)
	}
	if err := s.acquireReader(ctx); err != nil {
		return nil, err
	}
	defer func() { <-s.readGate }()
	rd := s.popReader()
	defer s.pushReader(rd)
	if rd.conn == nil {
		var err error
		if rd.conn, err = s.R.Conn(ctx); err != nil {
			return nil, err
		}
		rd.stmts = map[string]*sql.Stmt{}
	}
	results, err := rd.run(context.WithoutCancel(ctx), stmts)
	if err != nil {
		rd.release(true) // the connection may still be inside the transaction: never hand it back to the pool
	}
	return results, err
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

// popReader takes the reader used last, so that sequential reads keep one connection open (the gate bounds the
// readers to cap(readGate)).
func (s *Store) popReader() *reader {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	n := len(s.readers)
	if n == 0 {
		return &reader{}
	}
	rd := s.readers[n-1]
	s.readers = s.readers[:n-1]
	return rd
}

func (s *Store) pushReader(rd *reader) {
	s.readMu.Lock()
	s.readers = append(s.readers, rd)
	s.readMu.Unlock()
}

func (rd *reader) run(ctx context.Context, stmts []Stmt) ([]StmtResult, error) {
	if err := rd.exec(ctx, "BEGIN"); err != nil {
		return nil, err
	}
	results := make([]StmtResult, len(stmts))
	for i, stmt := range stmts {
		st, err := rd.stmt(ctx, stmt.Query)
		if err != nil {
			return nil, err
		}
		var rows *sql.Rows
		if st != nil {
			rows, err = st.QueryContext(ctx, stmt.Args...)
		} else {
			rows, err = rd.conn.QueryContext(ctx, stmt.Query, stmt.Args...)
		}
		if err != nil {
			return nil, err
		}
		values, err := collectRows(rows)
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return nil, err
		}
		results[i].Rows = values
	}
	return results, rd.exec(ctx, "COMMIT")
}

// stmt is query prepared on the reader's connection; nil (run it unprepared) once the reader holds
// maxCachedBatchStatements statements.
func (rd *reader) stmt(ctx context.Context, query string) (*sql.Stmt, error) {
	if st := rd.stmts[query]; st != nil || len(rd.stmts) >= maxCachedBatchStatements {
		return st, nil
	}
	st, err := rd.conn.PrepareContext(ctx, query)
	if err != nil {
		return nil, err
	}
	rd.stmts[query] = st
	return st, nil
}

func (rd *reader) exec(ctx context.Context, query string) error {
	st, err := rd.stmt(ctx, query)
	if err != nil {
		return err
	}
	if st == nil {
		_, err = rd.conn.ExecContext(ctx, query)
	} else {
		_, err = st.ExecContext(ctx)
	}
	return err
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
