//go:build js

package storetest

import (
	"path/filepath"
	"testing"
)

// Path returns a new database file under t's temporary directory; on js the store is not SQLite, so there is no template.
func Path(t testing.TB) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "panel.db")
}
