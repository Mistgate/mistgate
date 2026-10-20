package fleet

import (
	"context"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/unit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// This file is the seam to the health module (internal/panel/health): what the stream
// hands over (doctor reports, returning nodes), what the admin views ask for (status, alert counts), and the two
// commands that need a stream (RunDoctor, ApplyFix). The health module imports nothing from here; cmd/mistgate
// connects them.

// capDoctor is the Hello.capabilities entry of an agent that implements the doctor (agent.proto "DOCTOR").
const capDoctor = "doctor/1"
const capUnitRefresh = unit.CapabilityRefresh

// maxCapabilities bounds the Hello list a node can make the panel store. It must stay well above what an agent
// lists: v0.1.36 lists 17, and the 16 of an earlier bound cut off the last one (update-guard/1).
const maxCapabilities = 64

// Health is implemented by the health module. All methods must be cheap and must not call back into the
// stream they run on.
type Health interface {
	// DoctorReport hands over every DoctorReport of a node (periodic, requested or the re-check after a fix).
	DoctorReport(ctx context.Context, nodeID string, r *agentv1.DoctorReport)
	// NodeReturned says that an active node came back after a silence of less than the blip window with an
	// unchanged boot time: health writes the history record.
	NodeReturned(ctx context.Context, nodeID string, silentSince, now time.Time)
	// NodeHealthSummaries is what the node status and card show, per node: the synthetic checks of the node all failing
	// (NoTraffic, with how many of how many) and the check of the worst FAIL of its last doctor report. A request reads
	// it once: on the edge it is one D1 read.
	NodeHealthSummaries(ctx context.Context) map[string]store.NodeHealthSummary
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

// healthSummaries reads Health.NodeHealthSummaries (nil without a health module); the node's entry is the zero value
// when it has none.
func (f *Fleet) healthSummaries(ctx context.Context) map[string]store.NodeHealthSummary {
	if h := f.hooks(); h != nil {
		return h.NodeHealthSummaries(ctx)
	}
	return nil
}

// NodeStatusWithLive derives status from a projection and the health summaries already read for the surrounding request.
func (f *Fleet) NodeStatusWithLive(ctx context.Context, n store.NodeRow, live store.NodeLiveRow, summaries map[string]store.NodeHealthSummary) adminv1.NodeStatus {
	return f.statusOfLive(ctx, n, live, nil, f.now().UTC(), summaries[n.ID]).status
}

// RunDoctor asks a connected node with the doctor capability to run checks (nil = all) and waits for the report
// that echoes the request. The report has been handed to Health by then. A node without the capability is
// never sent anything (FAILED_PRECONDITION "agent too old").
func (f *Fleet) RunDoctor(ctx context.Context, nodeID string, checks []string, wait time.Duration) (*agentv1.DoctorReport, error) {
	reply, err := f.ask(ctx, nodeID, wait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{
			RunDoctor: &agentv1.RunDoctor{RequestId: reqID, Checks: checks}}}
	})
	if err != nil {
		return nil, err
	}
	if report := reply.GetDoctorReport(); report != nil {
		return report, nil
	}
	return nil, errLinkLost
}

// ApplyFix sends one fix (or its dry run) to a connected node with the doctor capability and waits up to the
// node's apply timeout for the CommandResult.
func (f *Fleet) ApplyFix(ctx context.Context, nodeID, fixID string, dryRun bool, params map[string]string) (*agentv1.CommandResult, error) {
	n, err := f.st.Node(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return f.command(ctx, nodeID, time.Duration(n.ApplyTimeoutS)*f.unit, func(reqID string) *agentv1.ConnectResponse {
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
