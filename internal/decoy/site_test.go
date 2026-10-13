package decoy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func get(h http.Handler) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	return w
}

// A damaged state file must never keep a node from starting: OpenSite says so and the caller serves the built-in page.
func TestOpenSiteRefusesDamagedState(t *testing.T) {
	files := []File{{Path: "index.html", Content: []byte("own site")}}
	b, err := FromFiles(files)
	if err != nil {
		t.Fatal(err)
	}
	for name, damage := range map[string]func(s *Site){
		"state is not JSON":     func(s *Site) { _ = os.WriteFile(s.statePath, []byte("{"), 0o600) },
		"bundle file is gone":   func(s *Site) { _ = os.Remove(s.bundlePath(b.Hash())) },
		"bundle file is edited": func(s *Site) { _ = os.WriteFile(s.bundlePath(b.Hash()), []byte("PK"), 0o600) },
		"unknown mode":          func(s *Site) { _ = os.WriteFile(s.statePath, []byte(`{"mode":"x","sha256":"`+b.Hash()+`"}`), 0o600) },
		"builtin with a hash": func(s *Site) {
			_ = os.WriteFile(s.statePath, []byte(`{"mode":"builtin","sha256":"`+b.Hash()+`"}`), 0o600)
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenSite(dir)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Apply(ModeOwn, b.Hash(), files); err != nil {
				t.Fatal(err)
			}
			damage(s)
			if _, err := OpenSite(dir); err == nil {
				t.Fatal("OpenSite accepted a damaged state")
			}
			fallback := BuiltinSite(dir)
			if mode, hash := fallback.State(); mode != ModeBuiltin || hash != "" {
				t.Fatalf("fallback state = %q %q", mode, hash)
			}
		})
	}
}

func TestApplyRefusals(t *testing.T) {
	s := BuiltinSite(t.TempDir())
	b, _ := FromFiles([]File{{Path: "index.html", Content: []byte("x")}})
	for name, tc := range map[string]struct {
		mode, hash string
		files      []File
	}{
		"hash does not match files": {ModeOwn, sha256Of("other"), []File{{Path: "index.html", Content: []byte("x")}}},
		"no files for an unknown":   {ModeOwn, b.Hash(), nil},
		"builtin with files":        {ModeBuiltin, "", []File{{Path: "index.html", Content: []byte("x")}}},
		"builtin with a hash":       {ModeBuiltin, b.Hash(), nil},
		"unknown mode":              {"proxy", "", nil},
		"parent path":               {ModeOwn, b.Hash(), []File{{Path: "../index.html", Content: []byte("x")}}},
	} {
		if _, err := s.Apply(tc.mode, tc.hash, tc.files); err == nil {
			t.Errorf("%s: Apply accepted", name)
		}
	}
	if mode, hash := s.State(); mode != ModeBuiltin || hash != "" {
		t.Fatalf("refused updates changed the state to %q %q", mode, hash)
	}
}

func sha256Of(s string) string {
	b, _ := FromFiles([]File{{Path: "index.html", Content: []byte(s)}})
	return b.Hash()
}

// Switching while requests are in flight changes what is served without an error or a torn answer; the stable handler is
// what the listeners hold, so no listener is restarted.
func TestSwitchUnderLiveTraffic(t *testing.T) {
	one := []File{{Path: "index.html", Content: []byte("site one")}}
	two := []File{{Path: "index.html", Content: []byte("site two")}}
	b1, _ := FromFiles(one)
	b2, _ := FromFiles(two)
	s := BuiltinSite(t.TempDir())
	h := s.Handler()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	bad := make(chan string, 1)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				w := get(h)
				body := w.Body.String()
				if w.Code != 200 || (body != "site one" && body != "site two" && !containsComingSoon(body)) {
					select {
					case bad <- body:
					default:
					}
					return
				}
			}
		}()
	}
	for i := 0; i < 40; i++ {
		var err error
		switch i % 4 {
		case 0:
			_, err = s.Apply(ModePanel, b1.Hash(), one)
		case 1:
			_, err = s.Apply(ModeOwn, b2.Hash(), two)
		case 2:
			_, err = s.Apply(ModeBuiltin, "", nil)
		case 3:
			_, err = s.Apply(ModeOwn, b1.Hash(), one)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case body := <-bad:
		t.Fatalf("a request got %q while the site was switched", body)
	default:
	}
	// No bundle outlives its use: only the active one is on disk.
	entries, _ := filepath.Glob(filepath.Join(s.dir, "site-*.zip"))
	if len(entries) != 1 {
		t.Fatalf("bundles on disk after switching = %v, want only the active one", entries)
	}
}

func containsComingSoon(body string) bool {
	for i := 0; i+11 <= len(body); i++ {
		if body[i:i+11] == "Coming soon" {
			return true
		}
	}
	return false
}
