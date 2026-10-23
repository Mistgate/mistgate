package access

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// GetUserConnection: what the nodes saw of a person's connections (agent.proto LinkCounters). Not built yet.
func (s *Service) GetUserConnection(context.Context, *connect.Request[adminv1.GetUserConnectionRequest]) (*connect.Response[adminv1.GetUserConnectionResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented yet"))
}
