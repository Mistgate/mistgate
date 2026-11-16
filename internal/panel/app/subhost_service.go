package app

import (
	"context"
	"net"
	"net/http"

	"connectrpc.com/connect"

	nodeprovision "github.com/mistgate/mistgate/internal/panel/provision"
)

// subHostService is the subscription host (subhost): the owner's RPCs, the public connections its tunnels carry, the
// subdomain the public listener gets a certificate for, and the SSH install of the first host. nil on the Cloudflare edition.
type subHostService interface {
	nodeprovision.SubHostAPI
	Handler(opts ...connect.HandlerOption) (string, http.Handler)
	PublicListener() net.Listener
	ACMEHost() string
	Load(ctx context.Context) error
	Run(ctx context.Context)
}
