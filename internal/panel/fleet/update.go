package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"github.com/mistgate/mistgate/internal/panel/store"
	panelupdate "github.com/mistgate/mistgate/internal/panel/update"
)

// This file is the seam to the updates module (internal/panel/update): the stream hands
// over nothing but asks two things of it (is a node mid-update, serve a bundle file), and offers it the two
// commands that need a stream (UpdateAgent, RollbackAgent) plus the live numbers the canary choice needs. The
// updates module imports nothing from here; cmd/mistgate connects them.

// capUpdate is the Hello.capabilities entry of an agent that can replace its own binary (agent.proto "UPDATE").
const capUpdate = "update/1"

// capUpdateLink is the edge link-only agent update capability; VPS updates continue to require capUpdate.
const capUpdateLink = "update-link/1"

// capUpdateSources gates UpdateAgent.sources and UpdateProgress.source.
const capUpdateSources = "update-sources/1"

const capOSStatus = osupdate.Capability

// Updates is implemented by the updates module. Its three metadata methods are cheap; Serve blocks for a download.
type Updates interface {
	// LinkDownload authorizes one file of the signed manifest pinned to a current SENT step.
	LinkDownload(ctx context.Context, nodeID, name, token string) (string, error)
	// RecordAnswer stores the first result of an edge update command against its sent step. Other command results never call it.
	RecordAnswer(ctx context.Context, nodeID, requestID string, result *agentv1.CommandResult) error
	// RecordProgress stores the latest best-effort progress for an in-flight update command.
	RecordProgress(ctx context.Context, nodeID string, progress *agentv1.UpdateProgress) error
	// Updating reports that a rollout step of the node is in flight (command sent, reconnect or gate pending): its
	// stream drops when the agent re-executes, and that is neither a blip nor an outage.
	Updating(nodeID string) bool
	// ServeWithWriteDeadline streams the file and arms the HTTP response's write deadline for each chunk.
	ServeWithWriteDeadline(ctx context.Context, nodeID, name string, offset uint64, setWriteDeadline func(time.Time) error,
		send func(chunk []byte, total uint64) error) error
	// NodeBinary is the absolute path, on the panel's server, of the agent binary for the platform in the current
	// trusted bundle; "" when there is none. The add-node window offers it as a ready scp command.
	NodeBinary(goos, goarch string) string
}

// SetUpdates connects the updates module. Call it before serving; nil disconnects (FetchUpdate answers UNIMPLEMENTED).
func (f *Fleet) SetUpdates(u Updates) {
	f.mu.Lock()
	f.upd = u
	f.mu.Unlock()
}

func (f *Fleet) updates() Updates {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upd
}

// osStatusAllowed reports whether this panel asks agents for their operating-system status: a VPS panel whose update
// service reads it. The edge edition never does.
func (f *Fleet) osStatusAllowed() bool {
	if f.cfg.Remote != nil {
		return false
	}
	u, ok := f.updates().(interface{ OSUpdatesEnabled() bool })
	return ok && u.OSUpdatesEnabled()
}

func (f *Fleet) updating(nodeID string) bool {
	u := f.updates()
	return u != nil && u.Updating(nodeID)
}

// FetchUpdate is AgentService.FetchUpdate: the agent middleware has already verified the node certificate (the node id
// is in ctx), so only a valid, non-revoked agent gets bytes.
func (a agentService) FetchUpdate(ctx context.Context, req *connect.Request[agentv1.FetchUpdateRequest], stream *connect.ServerStream[agentv1.FetchUpdateResponse]) error {
	id, ok := nodeID(ctx)
	if !ok {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("no node certificate"))
	}
	u := a.f.updates()
	if u == nil {
		return connect.NewError(connect.CodeUnimplemented, errors.New("this panel does not serve updates"))
	}
	return u.ServeWithWriteDeadline(ctx, id, req.Msg.Name, req.Msg.Offset, panelupdate.WriteDeadlineFromContext(ctx), func(chunk []byte, total uint64) error {
		return stream.Send(&agentv1.FetchUpdateResponse{Data: chunk, TotalSize: total})
	})
}

// UpdateAgent sends a signed manifest to a connected node with the update capability and waits up to wait for the
// CommandResult (the download is part of it). The agent answers before it re-executes; a stream that drops right
// after the answer still delivers it.
func (f *Fleet) UpdateAgent(ctx context.Context, nodeID string, manifest, signature []byte, wait, downloadTimeout time.Duration) (*agentv1.CommandResult, error) {
	return f.command(ctx, nodeID, wait, func(reqID string) *agentv1.ConnectResponse {
		return updateAgentFrame(reqID, manifest, signature, downloadTimeout)
	})
}

// UpdateAgentWithRequestID is the rollout path. The caller persists the request ID with the step so progress and
// command replies from a previous attempt cannot be mistaken for the current update after a reconnect or restart.
func (f *Fleet) UpdateAgentWithRequestID(ctx context.Context, nodeID, requestID string, manifest, signature []byte, wait, downloadTimeout time.Duration, options panelupdate.UpdateAgentOptions) (*agentv1.CommandResult, error) {
	frame := updateAgentFrame(requestID, manifest, signature, downloadTimeout)
	frame.GetUpdateAgent().Sources = append([]string(nil), options.Sources...)
	return f.commandWithRequestID(ctx, nodeID, requestID, wait, frame)
}

func updateAgentFrame(requestID string, manifest, signature []byte, downloadTimeout time.Duration) *agentv1.ConnectResponse {
	r := &agentv1.UpdateAgent{RequestId: requestID, Manifest: manifest, Signature: signature}
	if downloadTimeout > 0 {
		r.DownloadTimeoutSeconds = uint32(downloadTimeout / time.Second)
	}
	return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: r}}
}

// RollbackAgent asks a connected node with the update capability to put its previous binary back.
func (f *Fleet) RollbackAgent(ctx context.Context, nodeID string, wait time.Duration) (*agentv1.CommandResult, error) {
	return f.command(ctx, nodeID, wait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RollbackAgent{
			RollbackAgent: &agentv1.RollbackAgent{RequestId: reqID}}}
	})
}

// SendUpdateAgent sends an update command through the edge link without waiting for CommandResult.
func (f *Fleet) SendUpdateAgent(ctx context.Context, nodeID, requestID string, manifest, signature []byte, downloadToken string, deadline time.Time, downloadTimeout time.Duration) error {
	r := updateAgentFrame(requestID, manifest, signature, downloadTimeout).GetUpdateAgent()
	r.DownloadToken = downloadToken
	frame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: r}}
	return f.sendRecorded(ctx, nodeID, requestID, frame, deadline)
}

// SendRollbackAgent sends a rollback command through the edge link without waiting for CommandResult.
func (f *Fleet) SendRollbackAgent(ctx context.Context, nodeID, requestID string, deadline time.Time) error {
	frame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RollbackAgent{RollbackAgent: &agentv1.RollbackAgent{RequestId: requestID}}}
	return f.sendRecorded(ctx, nodeID, requestID, frame, deadline)
}

func (f *Fleet) sendRecorded(ctx context.Context, nodeID, requestID string, frame *agentv1.ConnectResponse, deadline time.Time) error {
	remote := f.cfg.Remote
	if remote == nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("node is offline"))
	}
	if requestID == "" || frame == nil {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid update request"))
	}
	if _, err := f.readyForRequest(ctx, nodeID, frame, nil); err != nil {
		return err
	}
	if err := remote.Send(ctx, nodeID, requestID, frame, deadline); err != nil {
		return requestOfflineError(frame)
	}
	return nil
}

// lastUpdateJSON is Hello.last_update as stored on the node row ("" = none). Strings are bounded: they are stored and shown.
func lastUpdateJSON(l *agentv1.LastUpdate) string {
	if l == nil {
		return ""
	}
	var outcome string
	switch l.Outcome {
	case agentv1.UpdateOutcome_UPDATE_OUTCOME_OK:
		outcome = "ok"
	case agentv1.UpdateOutcome_UPDATE_OUTCOME_ROLLED_BACK:
		outcome = "rolled_back"
	case agentv1.UpdateOutcome_UPDATE_OUTCOME_FAILED:
		outcome = "failed"
	default:
		return ""
	}
	b, err := json.Marshal(store.LastUpdateRow{Outcome: outcome, FromVersion: clip(l.FromVersion, 64), FromBuilt: l.FromBuilt,
		ToVersion: clip(l.ToVersion, 64), ToBuilt: l.ToBuilt, Reason: clip(l.Reason, 128), AtUnix: l.AtUnix})
	if err != nil {
		return ""
	}
	return string(b)
}

// recordCommit keeps node.last_update in step with the reliable event update_committed: the outcome travels in Hello, but
// a commit happens minutes after the reconnect, so "ok" would reach the panel only with the next reconnect. An event
// without to_built (an older agent) is not recorded. A newer stored outcome is kept.
func (f *Fleet) recordCommit(ctx context.Context, nodeID string, row store.EventRow) {
	toBuilt, _ := strconv.ParseInt(row.Params["to_built"], 10, 64)
	if toBuilt <= 0 {
		return
	}
	fromBuilt, _ := strconv.ParseInt(row.Params["from_built"], 10, 64)
	err := f.st.SetLastUpdateIfNewer(ctx, nodeID, store.LastUpdateRow{Outcome: "ok", FromVersion: clip(row.Params["from_version"], 64), FromBuilt: fromBuilt,
		ToVersion: clip(row.Params["to_version"], 64), ToBuilt: toBuilt, AtUnix: row.Time.Unix()})
	if err != nil {
		f.log.Warn("record last update", "node", nodeID, "err", err)
	}
}
