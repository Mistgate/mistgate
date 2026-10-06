//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
)

func TestStoreBatchResults(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.W.ExecContext(ctx, `CREATE TABLE batch_values (id INTEGER PRIMARY KEY, value TEXT UNIQUE)`); err != nil {
		t.Fatal(err)
	}

	results, err := s.batch(ctx,
		Stmt{Query: `INSERT INTO batch_values (id, value) VALUES (1, 'first')`},
		Stmt{Query: `UPDATE batch_values SET value = 'changed' WHERE id = 1 RETURNING id, value`, Returning: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].RowsAffected != 1 || results[1].RowsAffected != 1 || len(results[1].Rows) != 1 {
		t.Fatalf("batch results = %+v", results)
	}
	if results[1].Rows[0][0] != int64(1) || results[1].Rows[0][1] != "changed" {
		t.Fatalf("RETURNING row = %#v", results[1].Rows[0])
	}

	if _, err := s.batch(ctx,
		Stmt{Query: `INSERT INTO batch_values (id, value) VALUES (2, 'second')`},
		Stmt{Query: `INSERT INTO batch_values (id, value) VALUES (3, 'changed')`},
	); err == nil {
		t.Fatal("batch with a constraint violation succeeded")
	}
	var count int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM batch_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed batch left %d rows, want only the earlier committed row", count)
	}
}

func TestStoreBatchGuard(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.W.ExecContext(ctx, `CREATE TABLE batch_guard_values (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.batch(ctx,
		Stmt{Query: `INSERT INTO batch_guard_values (id) VALUES (1)`},
		guard(`0`),
	); !errors.Is(err, errGuard) {
		t.Fatalf("false guard error = %v, want errGuard", err)
	}
	var count int
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM batch_guard_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed guard left %d earlier writes", count)
	}

	if _, err := s.batch(ctx, guard(`NULL`)); !errors.Is(err, errGuard) {
		t.Fatalf("NULL guard error = %v, want errGuard", err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM batch_guard_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("NULL guard left %d writes", count)
	}

	if _, err := s.batch(ctx,
		guard(`1`),
		Stmt{Query: `INSERT INTO batch_guard_values (id) VALUES (2)`},
	); err != nil {
		t.Fatalf("true guard failed: %v", err)
	}
	if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM batch_guard_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("true guard left %d writes, want one", count)
	}
}
