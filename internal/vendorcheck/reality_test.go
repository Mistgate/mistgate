package vendorcheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/mod/modfile"
)

const (
	realityPath = "github.com/xtls/reality"
	realityDir  = "../../third_party/reality"
)

// The go.mod `replace` sends xtls/reality to third_party/reality whatever version is required, so a bump of xray-core would go on
// building the old copy without a word. The version the copy says it is (MISTGATE.md) must be the one go.mod requires.
func realityRequired(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	mod, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range mod.Require {
		if r.Mod.Path == realityPath {
			return r.Mod.Version
		}
	}
	t.Fatal(realityPath + " is not required by go.mod")
	return ""
}

func TestVendoredRealityVersionMatchesGoMod(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(realityDir, "MISTGATE.md"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile("module version `([^`]+)`").FindSubmatch(doc)
	if m == nil {
		t.Fatal("third_party/reality/MISTGATE.md does not name the module version it is a copy of (\"module version `...`\")")
	}
	if got, want := string(m[1]), realityRequired(t); got != want {
		t.Fatalf("go.mod requires %s %s, but third_party/reality is a copy of %s: re-vendor it (third_party/reality/MISTGATE.md says how)", realityPath, want, got)
	}
}

const (
	upstreamHunk = `			if peerPub2 == nil {
				break // reject outdated/strange Client Hello that doesn't have X25519MLKEM768 before optional X25519
			}
`
	patchedHunk = `			if peerPub2 == nil {
				if peerPub = legacyPeerPub(config, hs.clientHello.keyShares); peerPub == nil { // Mistgate: the pre-2026-11 rule, mistgate_compat.go
					break // reject outdated/strange Client Hello that doesn't have X25519MLKEM768 before optional X25519
				}
			}
`
)

// The copy differs from the upstream module in the Go module cache by the one hunk in tls.go and by our own files, nothing else.
func TestVendoredRealityIsUpstreamPlusTheHunk(t *testing.T) {
	version := realityRequired(t)
	out, err := exec.Command("go", "mod", "download", "-json", realityPath+"@"+version).Output()
	if err != nil {
		t.Skipf("the upstream module %s@%s is not in the module cache and could not be downloaded: %v", realityPath, version, err)
	}
	var info struct{ Dir string }
	if err := json.Unmarshal(out, &info); err != nil || info.Dir == "" {
		t.Skipf("go mod download printed no directory for %s@%s", realityPath, version)
	}
	ours := map[string]bool{"mistgate_compat.go": true, "MISTGATE.md": true}
	seen := map[string]bool{}
	err = filepath.WalkDir(realityDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(realityDir, path)
		rel = filepath.ToSlash(rel)
		if ours[rel] {
			return nil
		}
		seen[rel] = true
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		want, err := os.ReadFile(filepath.Join(info.Dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s is not in upstream %s: %v", rel, version, err)
			return nil
		}
		if rel == "tls.go" {
			if bytes.Count(got, []byte(patchedHunk)) != 1 {
				t.Errorf("tls.go does not hold the patch hunk exactly once")
			}
			got = bytes.Replace(got, []byte(patchedHunk), []byte(upstreamHunk), 1)
		}
		if sha256.Sum256(got) != sha256.Sum256(want) {
			t.Errorf("%s differs from upstream %s beyond the declared patch", rel, version)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(info.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(info.Dir, path)
		if rel = filepath.ToSlash(rel); !seen[rel] && !strings.HasSuffix(rel, "_test.go") {
			t.Errorf("upstream file %s is missing from third_party/reality", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
