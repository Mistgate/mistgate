package fleet

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/health"
)

// The Cloudflare edition has two isolates: the cron tick's evaluator writes the node-health row, and the admin isolate,
// a health.Service of its own with no evaluator pass behind it, shows a node's status from that row alone.
func TestEdgeAdminShowsDoctorFailFromTheRowTheEvaluatorWrote(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	connectFull(a, "inst1")
	lease := func(ctx context.Context, name string, ttl time.Duration) bool {
		taken, err := e.st.TakeLease(ctx, name, "holder", ttl, time.Now())
		return err == nil && taken
	}
	jobs := health.New(e.st, e.v, e.f.reg, e.f, health.Config{Edge: true, RoundLease: lease})
	admin := health.New(e.st, e.v, e.f.reg, e.f, health.Config{Edge: true})
	e.f.SetHealth(admin)
	nodes := nodeService{e.f}

	status := func() *adminv1.Node {
		t.Helper()
		resp, err := nodes.GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Node
	}
	listed := func() *adminv1.Node {
		t.Helper()
		resp, err := nodes.ListNodes(e.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range resp.Msg.Nodes {
			if n.Id == a.nodeID {
				return n
			}
		}
		t.Fatal("node missing from ListNodes")
		return nil
	}
	overviewCard := func() *adminv1.NodeCard {
		t.Helper()
		resp, err := fleetService{e.f}.Overview(e.ctx, connect.NewRequest(&adminv1.OverviewRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range resp.Msg.Nodes {
			if c.Id == a.nodeID {
				return c
			}
		}
		t.Fatal("node missing from Overview")
		return nil
	}
	evaluate := func() {
		t.Helper()
		e.exec(`UPDATE job_lease SET expires_at = 0 WHERE name = 'health-eval'`)
		ctx, cancel := context.WithTimeout(e.ctx, 30*time.Second)
		defer cancel()
		jobs.EvaluateOnce(ctx)
	}
	failing := func(status agentv1.DoctorStatus) *agentv1.DoctorReport {
		return &agentv1.DoctorReport{Results: []*agentv1.DoctorResult{{Id: "disk_space", Status: status, Detail: "disk_space detail"}}}
	}

	if n := status(); n.Status != adminv1.NodeStatus_NODE_STATUS_ONLINE || n.Reason != nil {
		t.Fatalf("before any report: %v %v", n.Status, n.Reason)
	}
	jobs.DoctorReport(e.ctx, a.nodeID, failing(agentv1.DoctorStatus_DOCTOR_STATUS_FAIL))
	if n := status(); n.Reason != nil {
		t.Fatalf("a report alone is not the evaluator's row: %v", n.Reason)
	}
	evaluate()
	for name, reason := range map[string]*adminv1.StatusReason{"get": status().Reason, "list": listed().Reason, "overview": overviewCard().Reason} {
		if reason == nil || reason.Code != "doctor_fail" || reason.Params["check"] != "disk_space" {
			t.Fatalf("%s after the evaluator pass: %v, want doctor_fail disk_space", name, reason)
		}
	}
	jobs.DoctorReport(e.ctx, a.nodeID, failing(agentv1.DoctorStatus_DOCTOR_STATUS_OK))
	evaluate()
	if n := status(); n.Reason != nil {
		t.Fatalf("after the check passed: %v", n.Reason)
	}
}
