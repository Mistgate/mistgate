package access

import (
	"slices"

	"connectrpc.com/connect"
	"github.com/mistgate/mistgate/internal/agentcap"
)

// realityTargetAgentRefusal refuses a target set on a node whose agent is known not to take it (agent_too_old); an agent whose
// capabilities are not known yet (it never connected) is not refused, the inbound waits for it.
func realityTargetAgentRefusal(agentCaps []string, target string) error {
	if target == "" || len(agentCaps) == 0 || slices.Contains(agentCaps, agentcap.VLESSTarget) {
		return nil
	}
	return coded(connect.CodeFailedPrecondition, "agent_too_old")
}
