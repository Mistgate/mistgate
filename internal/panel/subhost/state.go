//go:build !js

package subhost

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The state lives in the setting table (no migration). state.PublicURL is the panel's effective public address: the one it had
// until the host is ready, https://<domain> once it is, the address the owner returns to while it is leaving. While a host is
// set it is kept apart (sub_host_public_url): public_url stays the panel's own address, which the CLI reads at the next start
// (the agents' mTLS address, the admin URL a restore prints), so neither ever names the subdomain, where the admin is
// refused. A host turned off writes the address it leaves the panel on to public_url. The bridge path is a secret of the
// panel and the host: it is never shown, logged or audited.
const (
	keyState        = "sub_host_state" // "" (off) | on | moving | leaving
	keyDomain       = "sub_host_domain"
	keyNode         = "sub_host_node"
	keyNext         = "sub_host_next_node"
	keyPrevURL      = "sub_host_prev_public_url"
	keyPath         = "sub_host_path"
	keySiteName     = "sub_host_site_name"
	keyNextSiteName = "sub_host_next_site_name"
	keyNextPath     = "sub_host_next_path"
	keyBootstrapPin = "sub_host_bootstrap_pin"
	keyReadyAt      = "sub_host_ready_at"
	keyPublicURL    = "public_url"          // the panel's own address (state off)
	keyHostURL      = "sub_host_public_url" // the effective address while a host is set
)

const (
	stateOff     = ""
	stateOn      = "on"
	stateMoving  = "moving"
	stateLeaving = "leaving"
)

// state is the stored state of the subscription host.
type state struct {
	State, Domain, Node, Next, PrevURL string
	Path, SiteName, NextSiteName       string
	NextPath                           string // the next node's own bridge path during a move: a host never learns another's
	BootstrapPin                       string // the host site's pin from the SSH install, until its agent reports one
	ReadyAt                            string // unix seconds: when the host was first ready ("" = not yet)
	PublicURL                          string
}

func loadState(ctx context.Context, st *store.Store) (state, error) {
	var s state
	fields := (&s).ptrs()
	var hostURL string
	fields[keyHostURL] = &hostURL
	for k, dst := range fields {
		v, err := st.Setting(ctx, k)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return state{}, err
		}
		*dst = v
	}
	if s.State != stateOff {
		s.PublicURL = hostURL
	}
	return s, nil
}

// ptrs are the fields of s by their setting key.
func (s *state) ptrs() map[string]*string {
	return map[string]*string{
		keyState: &s.State, keyDomain: &s.Domain, keyNode: &s.Node, keyNext: &s.Next, keyPrevURL: &s.PrevURL,
		keyPath: &s.Path, keySiteName: &s.SiteName, keyNextSiteName: &s.NextSiteName, keyNextPath: &s.NextPath, keyBootstrapPin: &s.BootstrapPin,
		keyReadyAt: &s.ReadyAt, keyPublicURL: &s.PublicURL,
	}
}

// saveState writes the whole state in one batch: the keys of an empty field are deleted. The effective address goes to
// sub_host_public_url while a host is set; turned off, the address the panel is left on goes to public_url.
func saveState(ctx context.Context, st *store.Store, s state) error {
	set := map[string]string{}
	var del []string
	for k, v := range (&s).ptrs() {
		if k == keyPublicURL {
			continue
		}
		if *v == "" {
			del = append(del, k)
		} else {
			set[k] = *v
		}
	}
	if s.State == stateOff {
		set[keyPublicURL] = s.PublicURL
		del = append(del, keyHostURL)
	} else {
		set[keyHostURL] = s.PublicURL
	}
	return st.SetAndDeleteSettings(ctx, set, del...)
}

// newPath is a fresh bridge path: 24 random URL-safe characters.
func newPath() string {
	var b [18]byte
	_, _ = rand.Read(b[:])
	return "/" + base64.RawURLEncoding.EncodeToString(b[:])
}
