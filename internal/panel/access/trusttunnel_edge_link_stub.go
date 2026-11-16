//go:build edge_link

package access

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The Link Worker serves no admin RPC (the admin Worker does), but the generated handler interface still names these three.
var errTrustTunnelNotLinked = connect.NewError(connect.CodeUnimplemented, errors.New("TrustTunnel keys are not linked in the Link Worker"))

func (*Service) GetTrustTunnelLinks(context.Context, *connect.Request[adminv1.GetTrustTunnelLinksRequest]) (*connect.Response[adminv1.GetTrustTunnelLinksResponse], error) {
	return nil, errTrustTunnelNotLinked
}

func (*Service) IssueTrustTunnelKey(context.Context, *connect.Request[adminv1.IssueTrustTunnelKeyRequest]) (*connect.Response[adminv1.IssueTrustTunnelKeyResponse], error) {
	return nil, errTrustTunnelNotLinked
}

func (*Service) RevokeTrustTunnelKey(context.Context, *connect.Request[adminv1.RevokeTrustTunnelKeyRequest]) (*connect.Response[adminv1.RevokeTrustTunnelKeyResponse], error) {
	return nil, errTrustTunnelNotLinked
}

// The Link Worker only serves agent sessions and desired state. It does not issue or render a person's TrustTunnel key.
func (*Service) trustTunnelKeyForNewUser(string, time.Time) (store.AccessKeyDevice, error) {
	return store.AccessKeyDevice{}, errors.New("TrustTunnel key management is not linked in the Link Worker")
}

func (*Service) issueTrustTunnelKey(context.Context, string, store.AccessUser) (bool, error) {
	return false, errors.New("TrustTunnel key management is not linked in the Link Worker")
}

func (*Service) revokeTrustTunnelKey(context.Context, string, string) error {
	return errors.New("TrustTunnel key management is not linked in the Link Worker")
}

func (*Service) rollbackTrustTunnelKey(context.Context, string, string, bool) {}

func (*Service) trustTunnelServers(context.Context, store.AccessUser, store.AccessGroup, []store.AccessInboundFull,
	[]store.RelayLink, string, SubOptions) ([]SubServer, error) {
	return nil, nil
}
