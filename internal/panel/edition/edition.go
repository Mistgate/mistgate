// Package edition names the two builds of the panel (the VPS binary and the Cloudflare edge Worker) and the features
// the edge does not run yet. The admin reads the list from MeResponse and shows those features as "not available in
// this edition yet"; an RPC that would start such work refuses with Refuse instead of queuing it silently.
package edition

import (
	"errors"

	"connectrpc.com/connect"
)

// Names of the editions, as MeResponse.edition carries them.
const (
	VPS  = "vps"
	Edge = "edge"
)

// Stable feature ids, as MeResponse.unavailable carries them. The admin words each as "err.unavailable_in_edition"
// plus its own name; never rename one.
const (
	Backups      = "backups"
	Telegram     = "telegram"
	MCP          = "mcp"
	Provision    = "provision"
	HealthChecks = "health_checks"
	AgentUpdates = "agent_updates"
	AgentLogs    = "agent_logs"
	PanelUpdate  = "panel_update"
)

// Code is the refusal code of Refuse; the admin has a sentence for it ("err.unavailable_in_edition").
const Code = "unavailable_in_edition"

// edgeUnavailable is the source of truth for the edge. internal/panel/app keeps a test that every deferred edge job
// maps onto ids here and every id here has a job.
var edgeUnavailable = []string{Backups, Telegram, MCP, Provision, HealthChecks, AgentUpdates, AgentLogs, PanelUpdate}

// Unavailable is the feature ids the edition lacks: none on the VPS. The result is a copy.
func Unavailable(isEdge bool) []string {
	if !isEdge {
		return nil
	}
	return append([]string(nil), edgeUnavailable...)
}

// Name is the edition's name for MeResponse.
func Name(isEdge bool) string {
	if isEdge {
		return Edge
	}
	return VPS
}

// Refuse is the refusal of an RPC that would start work the edge does not run: FAILED_PRECONDITION whose message is the
// code and the feature ("unavailable_in_edition: feature=backups"), the same shape as the admin's other coded refusals.
func Refuse(feature string) error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(Code+": feature="+feature))
}

// Check is Refuse on the edge and nil on the VPS: the one line an RPC needs.
func Check(isEdge bool, feature string) error {
	if isEdge {
		return Refuse(feature)
	}
	return nil
}
