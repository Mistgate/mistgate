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

// This file is the seam to the health module (internal/panel/health): what the stream
// hands over (doctor reports, returning nodes), what the admin views ask for (status, alert counts), and the two
// commands that need a stream (RunDoctor, ApplyFix). The health module imports nothing from here; cmd/mistgate
// connects them.

// capDoctor is the Hello.capabilities entry of an agent that implements the doctor (agent.proto "DOCTOR").
const capDoctor = "doctor/1"

const maxCapabilities = 16

// Health is implemented by the health module. All methods must be cheap and must not call back into the
// stream they run on.
type Health interface {
	// DoctorReport hands over every DoctorReport of a node (periodic, requested or the re-check after a fix).
	DoctorReport(ctx context.Context, nodeID string, r *agentv1.DoctorReport)
	// NodeReturned says that an active node came back after a silence of less than the blip window with an
	// unchanged boot time: health writes the history record.
	NodeReturned(ctx context.Context, nodeID string, silentSince, now time.Time)
	// NodeHealth is what the node status and card show: the synthetic checks of the node all failing
	// (noTraffic, with how many of how many) and the check of the worst FAIL of its last doctor report.
	NodeHealth(nodeID string) (noTraffic bool, failed, total int, doctorFail string)
	// AlertCounts feeds the badge of the Overview: active alerts not muted and not INFO, and the critical ones.
	AlertCounts(ctx context.Context) (active, critical uint32)
}

// SetHealth connects the health module. Call it before serving; nil disconnects.
func (f *Fleet) SetHealth(h Health) {
	f.mu.Lock()
	f.health = h
	f.mu.Unlock()
}

func (f *Fleet) hooks() Health {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health
}

// OnlineByInbound counts the open sessions of every inbound of the connected nodes (the health and WARP dialogs
// say how many connections a restart or a pause drops).
func (f *Fleet) OnlineByInbound(ctx context.Context) map[string]int {
	out := map[string]int{}
	for _, o := range f.Online(ctx) {
		out[o.InboundID]++
	}
	return out
}

// NodeStatusWithLive derives status from a projection already read for the surrounding request.
func (f *Fleet) NodeStatusWithLive(ctx context.Context, n store.NodeRow, live store.NodeLiveRow) adminv1.NodeStatus {
	return f.statusOfLive(ctx, n, live, nil, f.now().UTC()).status
}

// doctorSession returns the node's stream if it can run the doctor, else the error the admin API answers.
func (f *Fleet) doctorSession(ctx context.Context, nodeID string) (*session, error) {
	live, err := f.liveRowsForNode(ctx, nodeID, false)
	if err != nil {
		return nil, internalErr(f.log.Error, "read node live projection", err)
	}
	if !live.Connected {
		return nil, errNodeOffline
	}
	if !slices.Contains(live.AgentCaps, capDoctor) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("agent too old"))
	}
	s := f.session(nodeID)
	if s == nil {
		return nil, errNodeOffline
	}
	return s, nil
}

// RunDoctor asks a connected node with the doctor capability to run checks (nil = all) and waits for the report
// that echoes the request. The report has been handed to Health by then. A node without the capability is
// never sent anything (FAILED_PRECONDITION "agent too old").
func (f *Fleet) RunDoctor(ctx context.Context, nodeID string, checks []string, wait time.Duration) (*agentv1.DoctorReport, error) {
	s, err := f.doctorSession(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	id := store.NewID("req_")
	ch := make(chan *agentv1.DoctorReport, 1)
	s.waitMu.Lock()
	s.docs[id] = ch
	s.waitMu.Unlock()
	defer func() {
		s.waitMu.Lock()
		delete(s.docs, id)
		s.waitMu.Unlock()
	}()
	requestAt := f.now()
	tr, err := s.stepCore(ctx, SessionEvent{Kind: EventAdminCommand, At: requestAt, Request: &AdminRequest{
		RequestID: id, Deadline: requestAt.Add(wait), Kind: PendingDoctor,
		Frame: &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{
			RunDoctor: &agentv1.RunDoctor{RequestId: id, Checks: checks}}},
	}})
	if err != nil {
		return nil, errLinkLost
	}
	if tr.Close != nil {
		return nil, sessionCloseError(tr.Close)
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case r := <-ch:
		return r, nil
	case <-t.C:
		return nil, errNoAnswer
	case <-s.done:
		return nil, errLinkLost
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// ApplyFix sends one fix (or its dry run) to a connected node with the doctor capability and waits up to the
// node's apply timeout for the CommandResult.
func (f *Fleet) ApplyFix(ctx context.Context, nodeID, fixID string, dryRun bool, params map[string]string) (*agentv1.CommandResult, error) {
	s, err := f.doctorSession(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	n, err := f.st.Node(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return s.roundtrip(ctx, time.Duration(n.ApplyTimeoutS)*f.unit, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_ApplyFix{
			ApplyFix: &agentv1.ApplyFix{RequestId: reqID, FixId: fixID, DryRun: dryRun, Params: params}}}
	})
}

// recordDoctorReport stores the snapshot through Health before a waiting admin call is woken.
func (f *Fleet) recordDoctorReport(ctx context.Context, nodeID string, r *agentv1.DoctorReport) {
	if h := f.hooks(); h != nil {
		h.DoctorReport(ctx, nodeID, r)
	}
}

func (s *session) deliverDoctor(r *agentv1.DoctorReport) {
	if r == nil || r.RequestId == "" {
		return
	}
	s.waitMu.Lock()
	ch := s.docs[r.RequestId]
	s.waitMu.Unlock()
	if ch != nil {
		select {
		case ch <- r:
		default:
		}
	}
}

// capabilities bounds what a node lists in Hello: the strings are kept and compared.
func capabilities(h *agentv1.Hello) []string {
	var out []string
	for _, c := range h.Capabilities {
		if len(out) == maxCapabilities {
			break
		}
		out = append(out, clip(c, 32))
	}
	return out
}
