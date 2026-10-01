package health

import (
	"context"
	"sync"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The updates module raises UPDATE_FAILED through a condition source: it opens while the source returns it and is
// resolved as cleared when the source stops, like every other alert ("a function of the current state"). It does not
// wait for the node to be reachable, and a kind this module does not know is dropped.
func TestExtConditionSourceOpensAndClearsAnAlert(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	var mu sync.Mutex
	var conds []ExtCond
	e.s.AddConditionSource(func(context.Context) []ExtCond {
		mu.Lock()
		defer mu.Unlock()
		return append([]ExtCond(nil), conds...)
	})
	set := func(c ...ExtCond) {
		mu.Lock()
		conds = c
		mu.Unlock()
		e.evaluate()
	}
	failed := ExtCond{Kind: "update_failed", NodeID: "de1", Subject: "rol_1", Severity: sevWarning,
		Why: "health.alert.update_failed.why.gate_failed", Params: map[string]string{"rollout_id": "rol_1", "node": "de1", "reason": "probe_failed"}}

	set()
	want(t, e.active())
	set(failed)
	got := e.active()
	want(t, got, "update_failed/de1/rol_1")
	a := got["update_failed/de1/rol_1"]
	if a.Severity != sevWarning || a.WhyKey != failed.Why || a.TitleKey != "health.alert.update_failed.title" || a.Params["reason"] != "probe_failed" {
		t.Fatalf("alert %+v", a)
	}
	if kindProto["update_failed"] != adminv1.AlertKind_ALERT_KIND_UPDATE_FAILED {
		t.Fatal("kind is not mapped")
	}
	if alertMsg(a, map[string]string{"de1": "de1"}, func(store.HealthAlert) string { return "" }).Kind != adminv1.AlertKind_ALERT_KIND_UPDATE_FAILED {
		t.Fatal("the admin message has another kind")
	}

	// the node drops off while the rollout is still paused: the alert stays, it is about the rollout
	e.fl.set("de1", liveState{up: false})
	e.evaluate()
	want(t, e.active(), "update_failed/de1/rol_1")

	// the owner resumes while the node is still away: the alert resolves all the same
	set()
	want(t, e.active())
	if h := e.history(); len(h) != 1 || h[0].Kind != "update_failed" || h[0].Resolution != "cleared" {
		t.Fatalf("history %+v", h)
	}

	// kinds that are not the module's to raise are dropped
	set(ExtCond{Kind: "quota", NodeID: "de1", Subject: "x", Severity: sevCritical, Why: "w"}, ExtCond{Kind: "update_failed", NodeID: "de1", Subject: "rol_2", Severity: 9, Why: "w"})
	want(t, e.active())
}

// NodeChecksSince counts what the rollout gate needs: the probeable inbounds and the rounds that ended after the reconnect.
func TestNodeChecksSince(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(a) // a round from before the update
	e.pass(b)

	since := e.clock.Now()
	if p, ok, f := e.s.NodeChecksSince(e.ctx, "de1", since); p != 2 || ok != 0 || f != 0 {
		t.Fatalf("before any new round: probeable %d ok %d failed %d", p, ok, f)
	}
	e.pass(a)
	if p, ok, f := e.s.NodeChecksSince(e.ctx, "de1", since); p != 2 || ok != 1 || f != 0 {
		t.Fatalf("one round ok: %d %d %d", p, ok, f)
	}
	e.fail(b, "timeout")
	if p, ok, f := e.s.NodeChecksSince(e.ctx, "de1", since); p != 2 || ok != 1 || f != 1 {
		t.Fatalf("one failed: %d %d %d", p, ok, f)
	}
	e.round(b, cDeg, "http_status") // a degraded round is not an ok round
	if p, ok, f := e.s.NodeChecksSince(e.ctx, "de1", since); p != 2 || ok != 1 || f != 1 {
		t.Fatalf("one degraded: %d %d %d", p, ok, f)
	}
	e.pass(b)
	if p, ok, f := e.s.NodeChecksSince(e.ctx, "de1", since); p != 2 || ok != 2 || f != 0 {
		t.Fatalf("both ok: %d %d %d", p, ok, f)
	}
	// an inbound that is not active is not probeable; a node that is away has nothing to probe
	e.exec(`UPDATE inbound SET state = 'pending' WHERE id = ?`, b)
	e.s.invalidateSnapshot()
	if p, ok, _ := e.s.NodeChecksSince(e.ctx, "de1", since); p != 1 || ok != 1 {
		t.Fatalf("one inbound pending: %d %d", p, ok)
	}
	e.fl.set("de1", liveState{up: false})
	e.s.invalidateSnapshot()
	if p, _, _ := e.s.NodeChecksSince(e.ctx, "de1", since.Add(-time.Hour)); p != 0 {
		t.Fatalf("an offline node is probeable: %d", p)
	}
	if p, _, _ := e.s.NodeChecksSince(e.ctx, "nod_nope", since); p != 0 {
		t.Fatal("unknown node")
	}
}
