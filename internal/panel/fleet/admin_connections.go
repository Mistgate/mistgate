package fleet

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// GetNodeConnections: the connection counters of the node's inbounds (agent.proto LinkCounters). Not built yet.
func (s nodeService) GetNodeConnections(context.Context, *connect.Request[adminv1.GetNodeConnectionsRequest]) (*connect.Response[adminv1.GetNodeConnectionsResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented yet"))
}
