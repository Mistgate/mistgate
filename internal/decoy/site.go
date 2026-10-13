package decoy

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"sync/atomic"
)

const (
	ModeBuiltin = "builtin"
	ModePanel   = "panel"
	ModeOwn     = "own"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type siteState struct {
	mode    string
	hash    string
	bundle  *Bundle
	handler http.Handler
}

type siteRecord struct {
	Mode string `json:"mode"`
	Hash string `json:"sha256,omitempty"`
}

// Site atomically swaps the handler used by every node masquerade listener and persists its active bundle.
type Site struct {
	dir       string
	statePath string
	mu        sync.Mutex
	state     atomic.Pointer[siteState]
	handler   http.Handler
}

// OpenSite loads the site's persisted mode and bundle from stateDir. Missing state means the historical built-in page.
func OpenSite(stateDir string) (*Site, error) {
	dir := filepath.Join(stateDir, "decoy")
	s := newSite(dir)

	data, err := os.ReadFile(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil || len(data) > 4096 {
		return nil, ErrInvalidBundle
	}
	var rec siteRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, ErrInvalidBundle
	}
	if rec.Mode == ModeBuiltin {
		if rec.Hash != "" {
			return nil, ErrInvalidBundle
		}
		return s, nil
	}
	if (rec.Mode != ModePanel && rec.Mode != ModeOwn) || !sha256Pattern.MatchString(rec.Hash) {
		return nil, ErrInvalidBundle
	}
	archive, err := os.ReadFile(s.bundlePath(rec.Hash))
	if err != nil {
		return nil, ErrInvalidBundle
	}
	b, err := ParseStoredArchive(archive)
	if err != nil || b.Hash() != rec.Hash {
		return nil, ErrInvalidBundle
	}
	s.state.Store(stateFor(rec.Mode, b))
	return s, nil
}

// BuiltinSite returns an in-memory builtin site. It is the safe fallback when persisted node state is unreadable.
func BuiltinSite(stateDir string) *Site { return newSite(filepath.Join(stateDir, "decoy")) }

func newSite(dir string) *Site {
	s := &Site{dir: dir, statePath: filepath.Join(dir, "site.json")}
	s.state.Store(&siteState{mode: ModeBuiltin, handler: Handler()})
	s.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := s.state.Load()
		if current == nil || current.handler == nil {
			Handler().ServeHTTP(w, r)
			return
		}
		current.handler.ServeHTTP(w, r)
	})
	return s
}

// Handler returns a stable handler whose active site can be changed while listeners are running.
func (s *Site) Handler() http.Handler { return s.handler }

// State reports the selected mode and the hash of its active bundle. Builtin has an empty hash.
func (s *Site) State() (mode, hash string) {
	if s == nil {
		return ModeBuiltin, ""
	}
	current := s.state.Load()
	if current == nil {
		return ModeBuiltin, ""
	}
	return current.mode, current.hash
}

// Apply validates a panel update, stores it atomically, and switches all current requests to the new site.
// An empty files slice reuses the currently installed bundle only when its hash already matches.
func (s *Site) Apply(mode, hash string, files []File) (bool, error) {
	if s == nil {
		return false, ErrInvalidBundle
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Load()
	var nextBundle *Bundle
	switch mode {
	case ModeBuiltin:
		if hash != "" || len(files) != 0 {
			return false, ErrInvalidBundle
		}
	case ModePanel, ModeOwn:
		if !sha256Pattern.MatchString(hash) {
			return false, ErrInvalidBundle
		}
		if len(files) > 0 {
			var err error
			nextBundle, err = FromFiles(files)
			if err != nil || nextBundle.Hash() != hash {
				return false, ErrInvalidBundle
			}
		} else if current != nil && current.bundle != nil && current.hash == hash {
			nextBundle = current.bundle
		} else {
			return false, ErrInvalidBundle
		}
	default:
		return false, ErrInvalidBundle
	}
	if current != nil && current.mode == mode && current.hash == hash {
		return false, nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return false, err
	}
	if nextBundle != nil && (current == nil || current.hash != hash || current.bundle == nil) {
		if err := writeAtomic(s.bundlePath(hash), nextBundle.Archive(), 0o600); err != nil {
			return false, err
		}
	}
	data, err := json.Marshal(siteRecord{Mode: mode, Hash: hash})
	if err != nil {
		return false, err
	}
	if err := writeAtomic(s.statePath, data, 0o600); err != nil {
		return false, err
	}
	s.state.Store(stateFor(mode, nextBundle))
	if current != nil && current.hash != "" && current.hash != hash {
		_ = os.Remove(s.bundlePath(current.hash))
	}
	return true, nil
}

func (s *Site) bundlePath(hash string) string { return filepath.Join(s.dir, "site-"+hash+".zip") }

func stateFor(mode string, b *Bundle) *siteState {
	if mode == ModeBuiltin {
		return &siteState{mode: ModeBuiltin, handler: Handler()}
	}
	return &siteState{mode: mode, hash: b.Hash(), bundle: b, handler: BundleHandler(b)}
}

func writeAtomic(name string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".decoy-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
