//go:build !js

package subhost

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The state lives in the setting table (no migration). public_url is always the panel's effective public address: https://<domain>
// while the host is on or moving, the address the owner returns to while it is leaving.
const (
	keyState     = "sub_host_state" // "" (off) | on | moving | leaving
	keyDomain    = "sub_host_domain"
	keyNode      = "sub_host_node"
	keySNI       = "sub_host_tunnel_sni"
	keyNext      = "sub_host_next_node"
	keyNextSNI   = "sub_host_next_tunnel_sni"
	keyPrevURL   = "sub_host_prev_public_url"
	keyPublicURL = "public_url"
)

const (
	stateOff     = ""
	stateOn      = "on"
	stateMoving  = "moving"
	stateLeaving = "leaving"
)

// state is the stored state of the subscription host.
type state struct {
	State, Domain, Node, SNI, Next, NextSNI, PrevURL string
	PublicURL                                        string
}

func loadState(ctx context.Context, st *store.Store) (state, error) {
	var s state
	for k, dst := range map[string]*string{
		keyState: &s.State, keyDomain: &s.Domain, keyNode: &s.Node, keySNI: &s.SNI, keyNext: &s.Next, keyNextSNI: &s.NextSNI,
		keyPrevURL: &s.PrevURL, keyPublicURL: &s.PublicURL,
	} {
		v, err := st.Setting(ctx, k)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return state{}, err
		}
		*dst = v
	}
	return s, nil
}

// save writes the whole state in one batch: the keys of an empty field are deleted.
func saveState(ctx context.Context, st *store.Store, s state) error {
	set := map[string]string{keyPublicURL: s.PublicURL}
	var del []string
	for k, v := range map[string]string{
		keyState: s.State, keyDomain: s.Domain, keyNode: s.Node, keySNI: s.SNI, keyNext: s.Next, keyNextSNI: s.NextSNI, keyPrevURL: s.PrevURL,
	} {
		if v == "" {
			del = append(del, k)
		} else {
			set[k] = v
		}
	}
	return st.SetAndDeleteSettings(ctx, set, del...)
}

// tunnelSNI is a fresh secret name under the domain: 16 base32 characters, like the agent SNI.
func tunnelSNI(domain string) string {
	var raw [10]byte
	rand.Read(raw[:])
	return strings.ToLower(base32.StdEncoding.EncodeToString(raw[:])) + "." + domain
}
