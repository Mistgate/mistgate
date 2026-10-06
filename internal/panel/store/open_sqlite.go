//go:build !js

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Open opens (creating if needed) the database at path and applies migrations. The
// database file is created with mode 0600 (SQLite gives the -wal and -shm files the
// same mode); an existing file with wider permissions is tightened.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := makePrivate(path); err != nil {
		return nil, err
	}
	w, err := openDB(path, 1, false)
	if err != nil {
		return nil, err
	}
	r, err := openDB(path, 4, true)
	if err != nil {
		w.Close()
		return nil, err
	}
	s := &Store{W: w, R: r, awgRetry: make(chan struct{}, 1)}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	for _, suffix := range []string{"-wal", "-shm"} { // belt and braces next to the umask and SQLite's own rule
		if err := chmodIfExists(path + suffix); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

// makePrivate creates path (empty) with mode 0600 if it does not exist and strips
// group/other permissions from it if it does.
func makePrivate(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	return chmodIfExists(path)
}

func chmodIfExists(path string) error {
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(path, 0o600)
	}
	return nil
}

func openDB(path string, maxConns int, readOnly bool) (*sql.DB, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	}
	u := url.URL{Scheme: "file", Opaque: filepath.ToSlash(path), RawQuery: q.Encode()}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(maxConns)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return db, nil
}

func (s *Store) migrate(ctx context.Context) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, s.W, sub)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// SchemaVersions are the newest migration applied to the database file at path, read without migrating it, and the
// newest one this binary carries. goose accepts a database newer than the binary, whose code would not know its tables:
// a restore compares the two first.
func SchemaVersions(ctx context.Context, path string) (db, binary int64, err error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if n, err := goose.NumericComponent(e.Name()); err == nil {
			binary = max(binary, n)
		}
	}
	r, err := openDB(path, 1, true)
	if err != nil {
		return 0, 0, err
	}
	defer r.Close()
	err = r.QueryRowContext(ctx, `SELECT coalesce(max(version_id), 0) FROM goose_db_version`).Scan(&db)
	return db, binary, err
}

// SnapshotDatabase creates a consistent SQLite snapshot at a new path. VACUUM INTO
// includes committed WAL data without copying live -wal/-shm files.
func (s *Store) SnapshotDatabase(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("store: snapshot path is empty")
	}
	if _, err := os.Lstat(path); err == nil {
		return ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := s.W.ExecContext(ctx, `VACUUM INTO ?`, path); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure database snapshot: %w", err)
	}
	return nil
}
