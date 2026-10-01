package protocols

import (
	"fmt"
	"sort"

	"github.com/mistgate/mistgate/internal/plugin"
)

// Registry holds the protocol plugins of this panel, by plugin id. It is built once at startup and never
// changes afterwards, so it needs no lock.
type Registry struct {
	byID map[string]Protocol
	ids  []string
}

// NewRegistry registers the given protocols. A duplicate or empty id is a programming error.
func NewRegistry(ps ...Protocol) (*Registry, error) {
	r := &Registry{byID: make(map[string]Protocol, len(ps))}
	for _, p := range ps {
		id := p.ID()
		if id == "" {
			return nil, fmt.Errorf("protocols: empty protocol id")
		}
		if _, dup := r.byID[id]; dup {
			return nil, fmt.Errorf("protocols: duplicate protocol %q", id)
		}
		r.byID[id] = p
		r.ids = append(r.ids, id)
	}
	sort.Strings(r.ids)
	return r, nil
}

// Get returns the protocol with the given plugin id.
func (r *Registry) Get(id string) (Protocol, bool) {
	p, ok := r.byID[id]
	return p, ok
}

// List returns all protocols ordered by id.
func (r *Registry) List() []Protocol {
	out := make([]Protocol, 0, len(r.ids))
	for _, id := range r.ids {
		out = append(out, r.byID[id])
	}
	return out
}

// AllowedForApps reports whether a user with the given enabled apps gets the protocol: one of the
// enabled apps must appear in the protocol's Clients().
func AllowedForApps(p Protocol, apps ...plugin.ClientID) bool {
	for _, cs := range p.Clients() {
		for _, a := range apps {
			if cs.Client == a {
				return true
			}
		}
	}
	return false
}
