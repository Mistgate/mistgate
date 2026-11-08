//go:build !js

// Package storetest gives tests a migrated panel database quickly. store.Open on a new file runs every migration, which
// takes about 0.15 s, and 4.3 s under -race: in a package whose tests each open a store (internal/panel/fleet opens one per
// test, four hundred of them) the migrations were most of the race job's time. Path runs them once per test binary, keeps the
// result in memory, and gives each test its own copy of it, which store.Open then opens in about 75 ms under -race.
//
// The copy is the same database a fresh store.Open would make: the template is a store.Open on an empty file, checkpointed and
// closed, and opening the copy runs store.Open's start-up work again (it finds every migration applied).
package storetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

var (
	once     sync.Once
	template []byte
	failed   error
)

func build() ([]byte, error) {
	dir, err := os.MkdirTemp("", "mistgate-storetest-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "template.db")
	ctx := context.Background()
	st, err := store.Open(ctx, p)
	if err != nil {
		return nil, err
	}
	// Everything into the main file, so the file alone is the database.
	if _, err := st.W.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		st.Close()
		return nil, fmt.Errorf("checkpoint: %w", err)
	}
	if err := st.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// Path writes a copy of the migrated template database into a new temporary directory of t and returns the file's path, for
// store.Open. Each call is a new database.
func Path(t testing.TB) string {
	t.Helper()
	once.Do(func() { template, failed = build() })
	if failed != nil {
		t.Fatalf("storetest: build the template database: %v", failed)
	}
	p := filepath.Join(t.TempDir(), "panel.db")
	if err := os.WriteFile(p, template, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
