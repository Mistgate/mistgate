package fleet

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// GetNodeJournal: the nodes' saved journal (agent.proto JournalEntry). A read for every role and API tokens.
func (s nodeService) GetNodeJournal(context.Context, *connect.Request[adminv1.GetNodeJournalRequest]) (*connect.Response[adminv1.GetNodeJournalResponse], error) {
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("the node journal is not implemented yet"))
}
