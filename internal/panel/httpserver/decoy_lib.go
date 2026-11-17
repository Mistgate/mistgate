//go:build !js

package httpserver

import (
	"context"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// libraryTTL is how long the choice of the library is reused: the owner's change reaches the public listener within this time,
// and a request costs no database read in between.
const libraryTTL = 2 * time.Second

// libraryDecoy reads the panel's own site from the store: the setting decoy_panel_source ("builtin", "dir" or "site:<id>") and, for
// a site, the files of its current version (read again when the version changes).
type libraryDecoy struct {
	st  *store.Store
	ttl time.Duration

	mu     sync.Mutex
	at     time.Time
	kind   string
	siteID string
	hash   string
	fsys   fs.FS
}

func newLibrary(st *store.Store) librarySource { return &libraryDecoy{st: st, ttl: libraryTTL} }

func (l *libraryDecoy) resolve() (string, fs.FS) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at.IsZero() || time.Since(l.at) >= l.ttl {
		l.refresh()
	}
	return l.kind, l.fsys
}

// refresh reads the setting. A site that is gone, is a draft or cannot be read leaves the panel on its directory (or the
// built-in page): the listener never serves nothing.
func (l *libraryDecoy) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	source, err := l.st.PanelDecoySource(ctx)
	if err != nil {
		l.at = time.Now() // keep the last choice, try again after the ttl
		return
	}
	l.at = time.Now()
	id, isSite := strings.CutPrefix(source, "site:")
	if !isSite {
		l.kind, l.siteID, l.hash, l.fsys = source, "", "", nil
		return
	}
	site, err := l.st.DecoySite(ctx, id)
	if err != nil || site.State != store.DecoyStateReady {
		l.kind, l.siteID, l.hash, l.fsys = "dir", "", "", nil
		return
	}
	if l.kind == "site" && l.siteID == id && l.hash == site.BundleHash && l.fsys != nil {
		return
	}
	b, _, err := l.st.DecoySiteBundle(ctx, id, 0)
	if err != nil {
		l.kind, l.siteID, l.hash, l.fsys = "dir", "", "", nil
		return
	}
	l.kind, l.siteID, l.hash, l.fsys = "site", id, b.Hash(), b.FS()
}
