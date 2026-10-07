//go:build js && wasm

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall/js"

	"github.com/mistgate/mistgate/edge/d1driver"
)

var errCloudflareUnavailable = errors.New("store: not available in the Cloudflare edition")

// Open is unavailable in the Cloudflare edition; use OpenD1 with the Worker binding.
func Open(context.Context, string) (*Store, error) { return nil, errCloudflareUnavailable }

// SchemaVersions is unavailable in the Cloudflare edition because the database is a D1 binding.
func SchemaVersions(context.Context, string) (db, binary int64, err error) {
	return 0, 0, errCloudflareUnavailable
}

// SnapshotDatabase is unavailable in the Cloudflare edition; edge backups are not part of this step.
func (s *Store) SnapshotDatabase(context.Context, string) error { return errCloudflareUnavailable }

// OpenD1 wraps a Cloudflare D1 binding and applies the shared migrations.
func OpenD1(ctx context.Context, binding js.Value) (*Store, error) {
	w := sql.OpenDB(d1driver.NewConnector(binding))
	r := sql.OpenDB(d1driver.NewConnector(binding))
	s := &Store{W: w, R: r}
	if err := migrateD1(ctx, binding, w); err != nil {
		s.Close()
		return nil, fmt.Errorf("migrate D1: %w", err)
	}
	return s, nil
}

func migrateD1(ctx context.Context, binding js.Value, db *sql.DB) error {
	var hasVersionTable int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'goose_db_version'`).Scan(&hasVersionTable)
	if err != nil {
		return fmt.Errorf("check goose version table: %w", err)
	}
	if hasVersionTable == 0 {
		_, err = db.ExecContext(ctx, `CREATE TABLE goose_db_version (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		version_id INTEGER NOT NULL,
		is_applied INTEGER NOT NULL,
		tstamp TIMESTAMP DEFAULT (datetime('now'))
	)`)
		if err != nil {
			return fmt.Errorf("create goose version table: %w", err)
		}
	}
	_, err = db.ExecContext(ctx, `INSERT INTO goose_db_version (version_id, is_applied)
		SELECT 0, TRUE WHERE NOT EXISTS (
			SELECT 1 FROM goose_db_version WHERE version_id = 0 AND is_applied = TRUE
		)`)
	if err != nil {
		return fmt.Errorf("initialize goose version table: %w", err)
	}

	applied := make(map[int64]bool)
	rows, err := db.QueryContext(ctx, `SELECT version_id FROM goose_db_version WHERE is_applied = TRUE`)
	if err != nil {
		return fmt.Errorf("read goose versions: %w", err)
	}
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			rows.Close()
			return fmt.Errorf("read goose versions: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read goose versions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("read goose versions: %w", err)
	}

	migrations, err := d1Migrations()
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if applied[migration.version] {
			continue
		}
		statements, err := migrationStatements(migration.name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", migration.name, err)
		}
		statements = append(statements, d1driver.Statement{
			Query: `INSERT INTO goose_db_version (version_id, is_applied) VALUES (?, TRUE)`,
			Args:  []any{migration.version},
		})
		if err := d1driver.Batch(ctx, binding, statements); err != nil {
			return fmt.Errorf("apply migration %s: %w", migration.name, err)
		}
		applied[migration.version] = true
	}
	return nil
}

type d1Migration struct {
	name    string
	version int64
}

func d1Migrations() ([]d1Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	var migrations []d1Migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		component := strings.SplitN(entry.Name(), "_", 2)[0]
		version, err := strconv.ParseInt(component, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid migration name %q", entry.Name())
		}
		migrations = append(migrations, d1Migration{name: entry.Name(), version: version})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].version < migrations[j].version })
	return migrations, nil
}

var (
	d1TransactionLine = regexp.MustCompile(`(?i)^\s*(?:BEGIN\s+(?:IMMEDIATE|DEFERRED|EXCLUSIVE|TRANSACTION)|BEGIN\s+TRANSACTION|COMMIT|END\s+TRANSACTION)\s*;\s*$`)
	d1ForeignKeysLine = regexp.MustCompile(`(?i)^\s*PRAGMA\s+foreign_keys\s*=\s*(?:OFF|ON|0|1)\s*;?\s*$`)
)

// migrationStatements reads a migration's Up section and splits it the way goose does, so D1 runs exactly the
// statements SQLite runs: a block between "-- +goose StatementBegin" and "-- +goose StatementEnd" is one statement,
// anywhere else a statement ends on a line that ends in ";". The grandfathered 00033 loses its explicit transaction and
// foreign_keys switch (D1 has neither); turning foreign keys off becomes PRAGMA defer_foreign_keys (ADR 0002).
func migrationStatements(name string) ([]d1driver.Statement, error) {
	data, err := fs.ReadFile(migrationsFS, "migrations/"+name)
	if err != nil {
		return nil, err
	}
	var out []d1driver.Statement
	var cur strings.Builder
	flush := func() {
		if q := trimTrailingComments(cur.String()); hasSQL(q) {
			out = append(out, d1driver.Statement{Query: q})
		}
		cur.Reset()
	}
	inUp, inBlock, deferFK := false, false, false
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch directive := strings.ToLower(trimmed); {
		case directive == "-- +goose up":
			inUp = true
			continue
		case directive == "-- +goose down":
			inUp = false
		case directive == "-- +goose statementbegin":
			inBlock = true
			continue
		case directive == "-- +goose statementend":
			inBlock = false
			flush()
			continue
		case strings.HasPrefix(directive, "-- +goose "):
			continue
		}
		if !inUp {
			if len(out) > 0 || cur.Len() > 0 {
				break // the Down section starts: nothing after it belongs to Up
			}
			continue
		}
		if name == "00033_reusable_retired_node_names.sql" {
			if d1ForeignKeysLine.MatchString(line) {
				deferFK = deferFK || strings.Contains(strings.ToLower(line), "off") || strings.Contains(line, "0")
				continue
			}
			if d1TransactionLine.MatchString(line) {
				continue
			}
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
		if !inBlock && strings.HasSuffix(strings.TrimSpace(stripLineComment(line)), ";") {
			flush()
		}
	}
	flush()
	if len(out) == 0 {
		return nil, errors.New("migration has no statements in its goose Up section")
	}
	if deferFK {
		out = append([]d1driver.Statement{{Query: "PRAGMA defer_foreign_keys = on"}}, out...)
	}
	return out, nil
}

// stripLineComment drops a trailing "-- comment" outside quotes, so "...; -- why" still ends a statement as in goose.
func stripLineComment(line string) string {
	quote := byte(0)
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '-' && i+1 < len(line) && line[i+1] == '-':
			return line[:i]
		}
	}
	return line
}

// trimTrailingComments drops the comment that follows a statement's last ";" ("...; -- why", or comment lines before the
// next statement). SQLite ignores it; D1 prepares it as a second, empty statement and rejects the whole batch with
// "SQL code did not contain a statement".
func trimTrailingComments(q string) string {
	lines := strings.Split(q, "\n")
	for len(lines) > 0 {
		last := stripLineComment(lines[len(lines)-1])
		if strings.TrimSpace(last) == "" {
			lines = lines[:len(lines)-1]
			continue
		}
		lines[len(lines)-1] = last
		break
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// hasSQL reports whether a statement holds more than comments and blank lines.
func hasSQL(q string) bool {
	for _, l := range strings.Split(q, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "--") {
			return true
		}
	}
	return false
}
