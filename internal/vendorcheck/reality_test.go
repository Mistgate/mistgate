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

// The patch is a list of hunks in tls.go: the key-share rule, then the listener's accept loop (the three scattered edits of that
// one are three entries). Each is the upstream text and the text the copy holds instead; both must occur exactly once.
var tlsHunks = []struct{ upstream, patched string }{
	{
		upstream: `			if peerPub2 == nil {
				break // reject outdated/strange Client Hello that doesn't have X25519MLKEM768 before optional X25519
			}
`,
		patched: `			if peerPub2 == nil {
				if peerPub = legacyPeerPub(config, hs.clientHello.keyShares); peerPub == nil { // Mistgate: the pre-2026-11 rule, mistgate_compat.go
					break // reject outdated/strange Client Hello that doesn't have X25519MLKEM768 before optional X25519
				}
			}
`,
	},
	{
		upstream: `	err   error
}
`,
		patched: `	err   error
	done  chan struct{} // Mistgate: closed by the accept goroutine after it set err; see MISTGATE.md
}
`,
	},
	{
		upstream: `	if c, ok := <-l.conns; ok {
		return c, nil
	}
	return nil, l.err
`,
		patched: `	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, l.err // Mistgate: err is written before done is closed
	}
`,
	},
	{
		upstream: `		l.conns = make(chan net.Conn)
		go func() {
			for {
				c, err := l.Listener.Accept()
				if err != nil {
					l.err = err
					close(l.conns)
					return
				}
				go func() {
					defer func() { recover() }()
					c, err = Server(context.Background(), c, l.config)
					if err == nil {
						l.conns <- c
					}
				}()
`,
		patched: `		l.conns = make(chan net.Conn)
		l.done = make(chan struct{})
		go func() {
			for {
				c, err := l.Listener.Accept()
				if err != nil {
					l.err = err
					close(l.done) // Mistgate: conns is never closed, so a finished handshake cannot send on a closed channel
					return
				}
				go func() {
					defer func() { recover() }()
					c, err = Server(context.Background(), c, l.config)
					if err == nil {
						select {
						case l.conns <- c:
						case <-l.done:
							c.Close() // Mistgate: nobody is going to Accept it
						}
					}
				}()
`,
	},
}

// The copy differs from the upstream module in the Go module cache by the hunks in tls.go and by our own files, nothing else.
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
			for i, h := range tlsHunks {
				if bytes.Count(got, []byte(h.patched)) != 1 {
					t.Errorf("tls.go does not hold patch hunk %d exactly once", i+1)
				}
				got = bytes.Replace(got, []byte(h.patched), []byte(h.upstream), 1)
			}
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
