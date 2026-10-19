package update

import (
	"context"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// RecordAnswer stores the first agent result for a recorded update or rollback request. The step store rejects stale,
// duplicate, or no-longer-active requests; detail is bounded before the proto is persisted.
func (s *Service) RecordAnswer(ctx context.Context, nodeID, requestID string, result *agentv1.CommandResult) error {
	if nodeID == "" || requestID == "" || result == nil {
		return nil
	}
	answer := proto.Clone(result).(*agentv1.CommandResult)
	answer.RequestId = requestID
	answer.Detail = store.Clip(answer.Detail, 200)
	answer.Error = store.Clip(answer.Error, 200)
	data, err := protojson.Marshal(answer)
	if err != nil {
		return err
	}
	_, err = s.st.SetStepAnswer(ctx, nodeID, requestID, string(data))
	return err
}
