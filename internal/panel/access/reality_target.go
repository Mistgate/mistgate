package access

import (
	"slices"

	"connectrpc.com/connect"
)

// capVLESSTarget is what an agent lists when it takes a REALITY target set on its node: the other site's name in the TLS block with no
// certificate (agent.proto Hello.capabilities "vless-target/1"). An older agent rejects the block, so the panel withholds the inbound.
const capVLESSTarget = "vless-target/1"

// realityTargetAgentRefusal refuses a target set on a node whose agent is known not to take it (agent_too_old); an agent whose
// capabilities are not known yet (it never connected) is not refused, the inbound waits for it.
func realityTargetAgentRefusal(agentCaps []string, target string) error {
	if target == "" || len(agentCaps) == 0 || slices.Contains(agentCaps, capVLESSTarget) {
		return nil
	}
	return coded(connect.CodeFailedPrecondition, "agent_too_old")
}
