//go:build !js

package fleet

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// On-demand debug capture of one node (agent.proto "DEBUG CAPTURE"). The OpsService starts and stops it through these methods; the
// node answers the start with a CommandResult and ships its records, scrubbed, when the time is up (recordDebugReport, called by the
// session for a DebugCaptureReport). The Cloudflare edition has none of this (debug_capture_js.go): it cannot start a capture.

const (
	debugMaxSeconds = 900
	debugMaxSources = 8
	debugCmdWait    = 20 * time.Second
)

// DebugStart is a request to capture one node.
type DebugStart struct {
	NodeID    string
	Level     string // "debug" or "info"
	Sources   []string
	DurationS uint32
}

func debugSourceOK(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// StartDebugCapture asks the node to capture. The capture row is written before the frame is sent, so a report can never
// arrive for a capture the panel does not know. The audit entry names the actor, the node, the sources and the duration.
func (f *Fleet) StartDebugCapture(ctx context.Context, in DebugStart) (store.DebugCaptureRow, error) {
	if in.Level == "" {
		in.Level = "debug"
	}
	if in.DurationS == 0 {
		in.DurationS = 300
	}
	if in.Level != "debug" && in.Level != "info" || in.DurationS > debugMaxSeconds || len(in.Sources) > debugMaxSources {
		return store.DebugCaptureRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("level must be debug or info, duration 1 to 900 seconds, at most 8 sources"))
	}
	seen := map[string]bool{}
	var sources []string
	for _, s := range in.Sources {
		s = strings.TrimSpace(s)
		if !debugSourceOK(s) {
			return store.DebugCaptureRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("a source is 1 to 32 letters, digits, - or _"))
		}
		if !seen[s] {
			seen[s] = true
			sources = append(sources, s)
		}
	}
	n, err := f.st.Node(ctx, in.NodeID)
	if errors.Is(err, store.ErrNotFound) {
		return store.DebugCaptureRow{}, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return store.DebugCaptureRow{}, internalErr(f.log.Error, "debug capture: node", err)
	}
	if n.State == "retired" {
		return store.DebugCaptureRow{}, errNodeRetired
	}
	var row store.DebugCaptureRow
	var beginErr error
	res, err := f.command(ctx, n.ID, debugCmdWait, func(id string) *agentv1.ConnectResponse {
		now := f.now()
		row = store.DebugCaptureRow{ID: id, NodeID: n.ID, NodeName: n.Name, Actor: f.cfg.Actor(ctx), Level: in.Level, Sources: sources,
			DurationS: int(in.DurationS), StartedAt: now, EndsAt: now.Add(time.Duration(in.DurationS) * time.Second), State: "running"}
		if beginErr = f.st.BeginDebugCapture(ctx, row, now); beginErr != nil {
			return nil
		}
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_DebugCapture{DebugCapture: &agentv1.DebugCapture{
			RequestId: id, Level: in.Level, Sources: sources, DurationS: in.DurationS}}}
	})
	switch {
	case errors.Is(beginErr, store.ErrDebugBusy):
		return store.DebugCaptureRow{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("debug_busy"))
	case beginErr != nil:
		return store.DebugCaptureRow{}, internalErr(f.log.Error, "debug capture: store", beginErr)
	}
	if err != nil {
		f.dropDebug(ctx, row.ID)
		return store.DebugCaptureRow{}, err
	}
	if !res.Ok {
		f.dropDebug(ctx, row.ID)
		switch res.Error {
		case "busy":
			return store.DebugCaptureRow{}, connect.NewError(connect.CodeFailedPrecondition, errors.New("debug_busy"))
		case "invalid":
			return store.DebugCaptureRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("the node refused these settings"))
		}
		return store.DebugCaptureRow{}, connect.NewError(connect.CodeUnavailable, errors.New("the node could not start the capture"))
	}
	f.audit(ctx, "node.debug_capture", map[string]string{"node_id": n.ID, "node": n.Name, "capture_id": row.ID, "level": in.Level,
		"sources": strings.Join(sources, ","), "duration_s": strconv.Itoa(int(in.DurationS))})
	f.log.Info("fleet: debug capture started", "node", n.Name, "level", in.Level, "duration_s", in.DurationS)
	return row, nil
}

// dropDebug forgets a capture the node never started.
func (f *Fleet) dropDebug(ctx context.Context, id string) {
	if id == "" {
		return
	}
	if err := f.st.DropDebugCapture(context.WithoutCancel(ctx), id); err != nil {
		f.log.Warn("fleet: debug capture not dropped", "code", "debug_capture_unmarked", "err", err)
	}
}

// StopDebugCapture ends a running capture now: the node ships what it holds before it answers. A capture that is over is
// returned as it is.
func (f *Fleet) StopDebugCapture(ctx context.Context, captureID string) (store.DebugCaptureRow, error) {
	row, err := f.st.DebugCapture(ctx, captureID)
	if errors.Is(err, store.ErrNotFound) {
		return row, connect.NewError(connect.CodeNotFound, errors.New("capture not found"))
	}
	if err != nil {
		return row, internalErr(f.log.Error, "debug capture: read", err)
	}
	if !row.Running(f.now()) {
		return f.refreshDebug(ctx, row), nil
	}
	res, err := f.command(ctx, row.NodeID, debugCmdWait, func(id string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_DebugCapture{DebugCapture: &agentv1.DebugCapture{
			RequestId: id, Stop: true, CaptureId: row.ID}}}
	})
	if err != nil {
		return row, err
	}
	if !res.Ok {
		return row, connect.NewError(connect.CodeUnavailable, errors.New("the node could not stop the capture"))
	}
	f.audit(ctx, "node.debug_capture_stop", map[string]string{"node_id": row.NodeID, "node": row.NodeName, "capture_id": row.ID})
	return f.refreshDebug(ctx, row), nil
}

func (f *Fleet) refreshDebug(ctx context.Context, row store.DebugCaptureRow) store.DebugCaptureRow {
	if fresh, err := f.st.DebugCapture(ctx, row.ID); err == nil {
		return fresh
	}
	return row
}

// recordDebugReport stores a chunk of a capture and, on the last one, ends it. A report for a capture that is not running (unknown,
// of another node, over its time) is dropped. The chunk is cleaned again here: the node's pass is not trusted.
func (f *Fleet) recordDebugReport(ctx context.Context, nodeID string, rep *agentv1.DebugCaptureReport) {
	if rep.GetRequestId() == "" {
		return
	}
	lines := rep.GetLines()
	if len(lines) > 256 { // the agent sends 64; more is not this protocol
		lines = lines[:256]
	}
	in := make([]store.DebugLineIn, len(lines))
	for i, l := range lines {
		in[i] = store.DebugLineIn{AtMs: l.GetTimeUnixMs(), Level: int32(l.GetLevel()), Source: l.GetSource(), Message: l.GetMessage(), Attrs: l.GetAttrs()}
	}
	_, ok, err := f.st.AppendDebugLines(ctx, nodeID, rep.RequestId, in, f.cfg.JournalSecrets, f.now())
	if err != nil {
		f.log.Warn("fleet: debug capture lines not stored", "code", "debug_capture_store", "err", err)
		return
	}
	if !ok {
		return
	}
	if rep.GetEof() {
		if err := f.st.FinishDebugCapture(ctx, nodeID, rep.RequestId, rep.GetEndReason(), rep.GetDropped()); err != nil {
			f.log.Warn("fleet: debug capture not finished", "code", "debug_capture_store", "err", err)
		}
	}
}
