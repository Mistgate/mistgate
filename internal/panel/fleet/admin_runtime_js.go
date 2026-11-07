//go:build js

package fleet

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The reads behind the MCP tools node_runtime, node_metrics with hours and path_loss_matrix have no reader on the
// Cloudflare edition (it has no MCP and no assistant), so their code stays out of the Workers' wasm.

var errNotOnEdge = connect.NewError(connect.CodeUnimplemented, errors.New("not available on this edition"))

func (nodeService) GetNodeRuntime(context.Context, *connect.Request[adminv1.GetNodeRuntimeRequest]) (*connect.Response[adminv1.GetNodeRuntimeResponse], error) {
	return nil, errNotOnEdge
}

func (nodeService) GetNodeHostHistory(context.Context, *connect.Request[adminv1.GetNodeHostHistoryRequest]) (*connect.Response[adminv1.GetNodeHostHistoryResponse], error) {
	return nil, errNotOnEdge
}

func (nodeService) GetPathLoss(context.Context, *connect.Request[adminv1.GetPathLossRequest]) (*connect.Response[adminv1.GetPathLossResponse], error) {
	return nil, errNotOnEdge
}
