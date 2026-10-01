package update

import (
	"os"
	"path/filepath"
	"testing"
)

// The add-node window offers scp of the trusted bundle's binary: only a verified file, by its absolute path.
func TestNodeBinary(t *testing.T) {
	e := newEnv(t)
	if p := e.s.NodeBinary("linux", "amd64"); p != "" {
		t.Fatalf("no bundle: %q", p)
	}
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": []byte("binary one")})
	want, _ := filepath.Abs(filepath.Join(e.dir, "dist", "mistgate-node-linux-amd64"))
	if p := e.s.NodeBinary("linux", "amd64"); p != filepath.ToSlash(want) {
		t.Fatalf("trusted bundle: %q, want %q", p, filepath.ToSlash(want))
	}
	if p := e.s.NodeBinary("linux", "arm64"); p != "" {
		t.Fatalf("a platform the bundle lacks: %q", p)
	}
	os.WriteFile(filepath.Join(e.dir, "dist", "mistgate-node-linux-amd64"), []byte("binary two"), 0o755) // same size, other bytes
	e.s.rescan()
	if p := e.s.NodeBinary("linux", "amd64"); p != "" {
		t.Fatalf("a bundle that no longer verifies: %q", p)
	}
}
