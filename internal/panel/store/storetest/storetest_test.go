//go:build !js

package storetest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// A copy is a fully migrated database of its own: the same schema as a fresh store.Open, and nothing written to one copy shows
// in another.
func TestCopiesAreMigratedAndSeparate(t *testing.T) {
	ctx := context.Background()
	open := func(p string) *store.Store {
		t.Helper()
		st, err := store.Open(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		return st
	}
	schema := func(st *store.Store) string {
		t.Helper()
		var s string
		if err := st.R.QueryRowContext(ctx, `SELECT group_concat(name || ':' || coalesce(sql, ''), ';') FROM (SELECT name, sql FROM sqlite_schema ORDER BY name)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	fresh := open(filepath.Join(t.TempDir(), "fresh.db"))
	a, b := open(Path(t)), open(Path(t))
	if schema(a) != schema(fresh) || schema(b) != schema(fresh) {
		t.Fatal("a copy's schema differs from a freshly migrated store's")
	}
	if _, err := a.W.ExecContext(ctx, `INSERT INTO setting (k, v) VALUES ('storetest', 'a')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := b.R.QueryRowContext(ctx, `SELECT count(*) FROM setting WHERE k = 'storetest'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a row written to one copy shows in another: %d, %v", n, err)
	}
}
