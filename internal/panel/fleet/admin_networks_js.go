//go:build js

package fleet

import (
	"context"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func (nodeService) GetNetworks(context.Context, *connect.Request[adminv1.GetNetworksRequest]) (*connect.Response[adminv1.GetNetworksResponse], error) {
	return nil, errNotOnEdge
}
