//go:build js

package access

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The Cloudflare edition does not offer TrustTunnel in its first version: the plugin is not registered there, so a person never
// has a key, and these are the places the shared code reaches for key work. Switching the app on, which would make a key, is
// refused like any other feature the edition lacks.
var errTrustTunnelEdition = edition.Refuse(edition.TrustTunnel)

func (*Service) trustTunnelKeyForNewUser(string, time.Time) (store.AccessKeyDevice, error) {
	return store.AccessKeyDevice{}, errTrustTunnelEdition
}

func (*Service) issueTrustTunnelKey(context.Context, string, store.AccessUser) (bool, error) {
	return false, errTrustTunnelEdition
}

func (*Service) revokeTrustTunnelKey(context.Context, string, string) error { return nil }

func (*Service) trustTunnelServers(context.Context, store.AccessUser, store.AccessGroups, []store.AccessInboundFull,
	[]store.RelayLink, string, SubOptions) ([]SubServer, error) {
	return nil, nil
}
