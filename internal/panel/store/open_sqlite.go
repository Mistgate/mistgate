//go:build !js

package store

import (
	"context"
	"database/sql"
	_ "embed" // schemaSnapshotSQL
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

const sqliteReaderPoolSize = 4

// schemaSnapshotSQL builds the schema and the data of a new, fully migrated database in one transaction. The migration chain
// is slow on an empty file (about 180 ADD COLUMN, each making SQLite read the whole schema again), the script is not.
// Regenerate it with the command in its first lines whenever a migration is added; TestSchemaSnapshot fails until you do.
// The chain stays: goose still runs after the snapshot, and brings a database the snapshot does not cover up to date.
//
//go:embed schema_snapshot.sql
var schemaSnapshotSQL string

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
	// The read batches keep sqliteReaderPoolSize connections of the pool for themselves; two more serve the
	// single-statement reads on s.R.
	r, err := openDB(path, sqliteReaderPoolSize+2, true)
	if err != nil {
		w.Close()
		return nil, err
	}
	s := &Store{W: w, R: r, readGate: make(chan struct{}, sqliteReaderPoolSize)}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.BackfillDecoyVersions(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("read the decoy versions the migration made: %w", err)
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
	db.SetMaxIdleConns(maxConns)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return db, nil
}

// applySnapshot builds a database that has no schema yet from the snapshot text and reports whether it did. The check and
// the script share one BEGIN IMMEDIATE transaction, so of two processes opening the same new file one builds it and the
// other finds it built.
func (s *Store) applySnapshot(ctx context.Context, snapshot string) (bool, error) {
	conn, err := s.W.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return false, err
	}
	rollback := func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), `ROLLBACK`) }
	var objects int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master`).Scan(&objects); err != nil {
		rollback()
		return false, err
	}
	if objects > 0 {
		rollback()
		return false, nil
	}
	if _, err := conn.ExecContext(ctx, snapshot); err != nil {
		rollback()
		return false, fmt.Errorf("apply the schema snapshot: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		rollback()
		return false, fmt.Errorf("commit the schema snapshot: %w", err)
	}
	return true, nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.applySnapshot(ctx, schemaSnapshotSQL); err != nil {
		return err
	}
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
