//go:build !js

package store

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateSchemaGolden = flag.Bool("update", false, "rewrite the normalized SQLite schema golden")

func TestMigrationSchemaGolden(t *testing.T) {
	st := openTemp(t)
	got, err := normalizedSchema(context.Background(), st.R)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "schema.golden")
	if *updateSchemaGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read schema golden (run go test ./internal/panel/store -run '^TestMigrationSchemaGolden$' -update): %v", err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		t.Fatalf("migration schema differs from %s; run go test ./internal/panel/store -run '^TestMigrationSchemaGolden$' -update to review and rewrite it", path)
	}
}
