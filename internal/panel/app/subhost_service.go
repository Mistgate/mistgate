package app

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
)

// subHostService is the subscription host (internal/panel/subhost, design/adr/0018-panel-behind-nat.md): the owner's RPCs and
// the routing of a panel whose public address is a node's subdomain. nil on the Cloudflare edition.
type subHostService interface {
	Handler(opts ...connect.HandlerOption) (string, http.Handler)
	Load(ctx context.Context) error
	Run(ctx context.Context)
}
