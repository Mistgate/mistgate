//go:build !js

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
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

func TestStoreReadBatchUsesReadPool(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if err := s.W.Close(); err != nil {
		t.Fatal(err)
	}
	var value string
	r := reads{}
	r.add(func(rows [][]any) error {
		if len(rows) != 1 {
			return errors.New("read returned no row")
		}
		return batchRow(rows[0]).Scan(&value)
	}, `SELECT 'reader' AS value`)
	if err := r.run(ctx, s); err != nil {
		t.Fatal(err)
	}
	if value != "reader" {
		t.Fatalf("read batch value = %q", value)
	}

	if _, err := s.read(ctx, Stmt{Query: `INSERT INTO setting (k, v) VALUES ('read.only', 'no') RETURNING k`, Returning: true}); err == nil {
		t.Fatal("read batch accepted a write")
	}
	if _, err := s.read(ctx, Stmt{Query: `SELECT 1`}); err == nil {
		t.Fatal("read batch accepted a SELECT without Returning")
	}
	if _, err := s.Setting(ctx, "read.only"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read-only batch left a setting: %v", err)
	}
}

func TestHasSQLPrefix(t *testing.T) {
	for _, query := range []string{" \t\nselect 1", "\u00a0ſelect 1", "SELECTOR"} {
		if !hasSQLPrefix(query, "SELECT") {
			t.Errorf("hasSQLPrefix(%q) = false, want true", query)
		}
	}
	for _, query := range []string{"INSERT INTO setting VALUES (1, 2)", "  ", string([]byte{0xff, 'S', 'E', 'L', 'E', 'C', 'T'})} {
		if hasSQLPrefix(query, "SELECT") {
			t.Errorf("hasSQLPrefix(%q) = true, want false", query)
		}
	}
}

func TestBatchStmtCacheBoundedAndClosed(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	var cache batchStmtCache
	t.Cleanup(func() { _ = cache.close() })
	first, err := cache.prepare(ctx, s.R, `SELECT 0`)
	if err != nil || first == nil {
		t.Fatalf("prepare first statement = %v, %v", first, err)
	}
	got, err := cache.prepare(ctx, s.R, `SELECT 0`)
	if err != nil || got != first {
		t.Fatalf("cached statement = %p, %v; want %p", got, err, first)
	}
	for i := 1; i < maxCachedBatchStatements; i++ {
		if stmt, err := cache.prepare(ctx, s.R, fmt.Sprintf("SELECT %d", i)); err != nil || stmt == nil {
			t.Fatalf("prepare statement %d = %v, %v", i, stmt, err)
		}
	}
	if stmt, err := cache.prepare(ctx, s.R, `SELECT beyond_cache_limit`); err != nil || stmt != nil {
		t.Fatalf("statement beyond cache limit = %v, %v; want uncached", stmt, err)
	}
	if len(cache.statements) != maxCachedBatchStatements {
		t.Fatalf("cached statements = %d, want %d", len(cache.statements), maxCachedBatchStatements)
	}
	if err := cache.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.prepare(ctx, s.R, `SELECT 1`); !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("prepare after cache close = %v, want sql.ErrConnDone", err)
	}
}

func TestConcurrentReadStatementCache(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	const workers = 50
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := range workers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results, err := s.read(ctx, Stmt{Query: `SELECT ? AS value`, Args: []any{i}, Returning: true})
			if err != nil {
				errs <- err
				return
			}
			if len(results) != 1 || len(results[0].Rows) != 1 || results[0].Rows[0][0] != int64(i) {
				errs <- fmt.Errorf("read result for %d = %#v", i, results)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

func TestReadGateHonorsCanceledContext(t *testing.T) {
	s := openTemp(t)
	for range cap(s.readGate) {
		s.readGate <- struct{}{}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.read(ctx, Stmt{Query: `SELECT 1`, Returning: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("read with canceled context = %v, want context.Canceled", err)
	}
	if len(s.readGate) != cap(s.readGate) {
		t.Fatalf("canceled read acquired a gate slot: %d of %d", len(s.readGate), cap(s.readGate))
	}
}

func TestStoreCloseClosesBatchStatementCaches(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	if _, err := s.read(ctx, Stmt{Query: `SELECT 1`, Returning: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.batch(ctx, Stmt{Query: `UPDATE setting SET v = v WHERE k = ?`, Args: []any{"missing"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for name, cache := range map[string]*batchStmtCache{"reader": &s.readStmtCache, "writer": &s.writeStmtCache} {
		cache.mu.Lock()
		closed, count := cache.closed, len(cache.statements)
		cache.mu.Unlock()
		if !closed || count != 0 {
			t.Errorf("%s cache closed=%v statements=%d, want closed and empty", name, closed, count)
		}
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
