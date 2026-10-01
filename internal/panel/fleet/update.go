package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// This file is the seam to the updates module (internal/panel/update): the stream hands
// over nothing but asks two things of it (is a node mid-update, serve a bundle file), and offers it the two
// commands that need a stream (UpdateAgent, RollbackAgent) plus the live numbers the canary choice needs. The
// updates module imports nothing from here; cmd/mistgate connects them.

// capUpdate is the Hello.capabilities entry of an agent that can replace its own binary (agent.proto "UPDATE").
const capUpdate = "update/1"

// Updates is implemented by the updates module. Both methods must be cheap; Serve blocks for the length of a download.
type Updates interface {
	// Updating reports that a rollout step of the node is in flight (command sent, reconnect or gate pending): its
	// stream drops when the agent re-executes, and that is neither a blip nor an outage.
	Updating(nodeID string) bool
	// Serve streams the named file of the current trusted bundle from offset: send gets at most 256 KiB at a time
	// and the size of the whole file. Errors are Connect errors (NOT_FOUND, RESOURCE_EXHAUSTED, FAILED_PRECONDITION).
	Serve(ctx context.Context, nodeID, name string, offset uint64, send func(chunk []byte, total uint64) error) error
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
	return u.Serve(ctx, id, req.Msg.Name, req.Msg.Offset, func(chunk []byte, total uint64) error {
		return stream.Send(&agentv1.FetchUpdateResponse{Data: chunk, TotalSize: total})
	})
}

// updateSession returns the node's stream if it can update itself, else the error the admin API answers. Nothing is
// ever sent to a node that did not list "update/1".
func (f *Fleet) updateSession(nodeID string) (*session, error) {
	s := f.session(nodeID)
	if s == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node is offline"))
	}
	if !s.can(capUpdate) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node cannot update itself"))
	}
	return s, nil
}

// UpdateAgent sends a signed manifest to a connected node with the update capability and waits up to wait for the
// CommandResult (the download is part of it). The agent answers before it re-executes; a stream that drops right
// after the answer still delivers it.
func (f *Fleet) UpdateAgent(ctx context.Context, nodeID string, manifest, signature []byte, wait time.Duration) (*agentv1.CommandResult, error) {
	s, err := f.updateSession(nodeID)
	if err != nil {
		return nil, err
	}
	return s.roundtrip(ctx, wait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{
			UpdateAgent: &agentv1.UpdateAgent{RequestId: reqID, Manifest: manifest, Signature: signature}}}
	})
}

// RollbackAgent asks a connected node with the update capability to put its previous binary back.
func (f *Fleet) RollbackAgent(ctx context.Context, nodeID string, wait time.Duration) (*agentv1.CommandResult, error) {
	s, err := f.updateSession(nodeID)
	if err != nil {
		return nil, err
	}
	return s.roundtrip(ctx, wait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RollbackAgent{
			RollbackAgent: &agentv1.RollbackAgent{RequestId: reqID}}}
	})
}

// OnlineUsersByNode counts the distinct users with an open session on each connected node (the canary is the node
// with the fewest).
func (f *Fleet) OnlineUsersByNode() map[string]int {
	seen := map[string]map[string]bool{}
	for _, o := range f.Online() {
		if seen[o.NodeID] == nil {
			seen[o.NodeID] = map[string]bool{}
		}
		seen[o.NodeID][o.UserID] = true
	}
	out := make(map[string]int, len(seen))
	for n, u := range seen {
		out[n] = len(u)
	}
	return out
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
