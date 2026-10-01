package fleet

import (
	"context"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The automatic preparation of the AmneziaWG kernel module (agent.proto "AWG AND WARP", PREPARE THE KERNEL MODULE).
//
// What keeps AmneziaWG from ever going down because of it: the stored awg_backend of a node is NOT changed when the admin
// asks for the module. The node builds it in the background and keeps running userspace; only when it reports
// "awg_kernel_prepare_done" (the module was loaded and verified there) does the panel set awg_backend to "kernel", in one
// transaction with the end of the record (store.AwgPrepareFinish), and then pushes the new setting. A failed, timed out
// or interrupted build changes nothing but the record the UI shows.

// capAwgPrepare is the Hello.capabilities entry of an agent that can prepare the module itself.
const capAwgPrepare = "awg-prepare/1"

// staleRunning is how long a "running" record is believed without news from the node (the job's hard timeout is 15 min).
const staleRunning = 40 * time.Minute

const (
	evPrepStarted = "awg_kernel_prepare_started"
	evPrepDone    = "awg_kernel_prepare_done"
	evPrepFailed  = "awg_kernel_prepare_failed"
)

// awgPrepareMsg is Node.awg_prepare: whether the agent can do it, and what the panel last learned.
func awgPrepareMsg(n store.NodeRow, caps []string, now time.Time) *adminv1.AwgPrepare {
	r := n.AwgPrepare()
	// A run is over after at most 15 minutes (its hard timeout) and its end is an event the node queues until we have it.
	// A record that still says "running" long after that lost its end (the node was wiped or re-enrolled): show it as what
	// it is, not as a spinner that never stops. The stored record is left alone, a late "done" still counts.
	if r.State == store.AwgPrepareRunning && now.Unix()-r.Since > int64(staleRunning/time.Second) {
		r = store.AwgPrepareRow{State: store.AwgPrepareFailed, Since: r.Since, Code: "interrupted", Reason: "the node did not report how the build ended"}
	}
	out := &adminv1.AwgPrepare{Supported: slices.Contains(caps, capAwgPrepare), SinceUnix: r.Since}
	switch r.State {
	case store.AwgPrepareRunning:
		out.State = adminv1.AwgPrepareState_AWG_PREPARE_STATE_RUNNING
	case store.AwgPrepareDone:
		out.State = adminv1.AwgPrepareState_AWG_PREPARE_STATE_DONE
	case store.AwgPrepareFailed:
		out.State = adminv1.AwgPrepareState_AWG_PREPARE_STATE_FAILED
		out.ReasonCode, out.Reason = clip(r.Code, 32), clip(r.Reason, 200)
	}
	return out
}

func prepareOutcome(state string) (adminv1.PrepareAwgOutcome, bool) {
	switch state {
	case "ready":
		return adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_READY, true
	case "needs_prepare":
		return adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_NEEDS_PREPARE, true
	case "started":
		return adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_STARTED, true
	case "running":
		return adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_RUNNING, true
	case "unsupported":
		return adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_UNSUPPORTED, true
	}
	return 0, false
}

func (s nodeService) PrepareAwgKernel(ctx context.Context, req *connect.Request[adminv1.PrepareAwgKernelRequest]) (*connect.Response[adminv1.PrepareAwgKernelResponse], error) {
	f := s.f
	n, err := f.st.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, internalErr(f.log.Error, "prepare awg kernel", err)
	}
	sess := f.session(n.ID)
	if sess == nil {
		return nil, errNodeOffline
	}
	if !sess.can(capAwgPrepare) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("agent too old"))
	}
	confirm := req.Msg.Confirm
	prev := n.AwgPrepare()
	now := f.now().UTC().Unix()
	restore := func() {
		if err := f.st.SetAwgPrepare(ctx, n.ID, prev); err != nil {
			f.log.Warn("restore awg prepare state", "node", n.ID, "err", err)
		}
	}
	if confirm { // the wish is recorded BEFORE the node is asked: a "done" cannot overtake it
		want := prev
		if prev.State != store.AwgPrepareRunning {
			want = store.AwgPrepareRow{State: store.AwgPrepareRunning, Since: now}
		}
		want.Want = true
		if err := f.st.SetAwgPrepare(ctx, n.ID, want); err != nil {
			return nil, internalErr(f.log.Error, "prepare awg kernel", err)
		}
	}
	res, err := sess.roundtrip(ctx, time.Duration(n.ApplyTimeoutS)*f.unit, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_PrepareAwgKernel{
			PrepareAwgKernel: &agentv1.PrepareAwgKernel{RequestId: reqID, DryRun: !confirm}}}
	})
	if err == nil && !res.Ok {
		f.log.Warn("agent failed to prepare the awg kernel module", "node", n.ID, "error", res.Error)
		err = connect.NewError(connect.CodeInternal, errors.New("the agent could not prepare: "+clip(res.Error, 200)))
	}
	var outcome adminv1.PrepareAwgOutcome
	if err == nil {
		var known bool
		if outcome, known = prepareOutcome(res.Params["state"]); !known {
			err = connect.NewError(connect.CodeInternal, errors.New("the agent gave an answer this panel does not know"))
		}
	}
	if err != nil {
		if confirm {
			restore()
		}
		return nil, err
	}
	running := outcome == adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_STARTED || outcome == adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_RUNNING
	switch {
	case confirm && !running:
		restore() // nothing was started (ready, or it cannot be done here): the wish is not kept
	case running && prev.State != store.AwgPrepareRunning:
		if err := f.st.AwgPrepareStarted(ctx, n.ID, now); err != nil { // the dry run found one the panel did not know
			f.log.Warn("record awg prepare", "node", n.ID, "err", err)
		}
	}
	if confirm && outcome == adminv1.PrepareAwgOutcome_PREPARE_AWG_OUTCOME_STARTED {
		f.audit(ctx, "node.awg_prepare", map[string]string{"node_id": n.ID, "node": n.Name})
	}
	n, err = f.st.Node(ctx, n.ID)
	if err != nil {
		return nil, internalErr(f.log.Error, "prepare awg kernel", err)
	}
	t := f.now().UTC()
	protos, _ := f.st.FleetNodeProtocols(ctx)
	today, _ := f.todayBytesByNode(ctx, t)
	enabled, _ := f.st.FleetEnabledInbounds(ctx)
	return connect.NewResponse(&adminv1.PrepareAwgKernelResponse{
		Outcome: outcome, ReasonCode: clip(res.Params["code"], 32), Reason: clip(res.Params["reason"], 200),
		Node: f.nodeMsg(ctx, n, protos[n.ID], today[n.ID], inboundsOf(enabled, n.ID), t),
	}), nil
}

// onAwgPrepareEvent keeps the record of a build in step with what the node reports. Replays are harmless: a start keeps its
// time, an end after the end changes nothing the second time (the switch only happens while the wish stands).
func (f *Fleet) onAwgPrepareEvent(ctx context.Context, nodeID string, row store.EventRow) {
	at := row.Time.Unix()
	var err error
	switch row.Code {
	case evPrepStarted:
		err = f.st.AwgPrepareStarted(ctx, nodeID, at)
	case evPrepDone, evPrepFailed:
		var switched bool
		switched, err = f.st.AwgPrepareFinish(ctx, nodeID, row.Code == evPrepDone, at, clip(row.Params["code"], 32), clip(row.Params["reason"], 200))
		if err == nil && switched {
			f.event(ctx, 1, "awg_kernel_switched", nodeID, map[string]string{"backend": "kernel", "minutes": row.Params["minutes"]})
			f.StateChanged() // pushes NodeSettings.awg_backend = "kernel" to the node
		}
	default:
		return
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		f.log.Warn("record awg prepare event", "node", nodeID, "code", row.Code, "err", err)
	}
}
