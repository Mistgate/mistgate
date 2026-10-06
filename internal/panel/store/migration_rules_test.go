package store

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

var (
	transactionDirective = regexp.MustCompile(`(?i)^\s*--\s*\+goose\s+NO\s+TRANSACTION\b`)
	explicitTransaction  = regexp.MustCompile(`(?i)^\s*(?:BEGIN(?:\s+(?:IMMEDIATE|DEFERRED|EXCLUSIVE|TRANSACTION))?|COMMIT(?:\s+TRANSACTION)?|END\s+TRANSACTION|SAVEPOINT\s+\S+|RELEASE(?:\s+SAVEPOINT)?(?:\s+\S+)?)\s*;\s*$`)
	foreignKeysPragma    = regexp.MustCompile(`(?i)^\s*PRAGMA\s+foreign_keys\b`)
)

func TestMigrationTransactionRules(t *testing.T) {
	// 00033 predates ADR 0002 and is the single grandfathered conversion exception.
	// Its explicit transaction and foreign_keys lines are stripped by the D1 runner; see
	// design/adr/0002-database-sql-seam.md.
	exceptions := map[string]bool{
		"00033_reusable_retired_node_names.sql": false,
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		data, err := fs.ReadFile(migrationsFS, "migrations/"+entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, exceptional := exceptions[entry.Name()]; exceptional {
			exceptions[entry.Name()] = true
			continue
		}
		for lineNumber, line := range strings.Split(string(data), "\n") {
			if transactionDirective.MatchString(line) || explicitTransaction.MatchString(line) || foreignKeysPragma.MatchString(line) {
				t.Errorf("%s:%d violates the cross-edition migration rule: %s", entry.Name(), lineNumber+1, strings.TrimSpace(line))
			}
		}
	}
	for name, found := range exceptions {
		if !found {
			t.Errorf("listed migration exception %s was not found", name)
		}
	}
}
