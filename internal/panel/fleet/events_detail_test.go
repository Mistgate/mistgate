package fleet

import (
	"math"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func healthBatch(h ...*agentv1.InboundHealth) *agentv1.ConnectRequest {
	now := time.Now().Unix()
	return &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now - 10, IntervalEndUnix: now, Health: h}}}
}

// An ACME certificate shows up after the ApplyResult: the stats health is what puts it on the inbound row, and an
// ApplyResult that carries none must not leave it blank for good.
func TestStatsHealthStoresTheServedCertificate(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	pin := strings.Repeat("ab", 32)
	notAfter := time.Now().Add(60 * 24 * time.Hour).Unix()
	cert := func() (string, int64) {
		in, err := e.st.Access().Inbound(e.ctx, ids.i1)
		if err != nil {
			t.Fatal(err)
		}
		return in.CertPinSHA256, in.CertNotAfter.Unix()
	}
	running := agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING

	// malformed pins and a missing expiry are not taken
	c.send(1, healthBatch(&agentv1.InboundHealth{InboundId: ids.i1, State: running, CertPinSha256: "zz", CertNotAfterUnix: notAfter}))
	c.send(2, healthBatch(&agentv1.InboundHealth{InboundId: ids.i1, State: running, CertPinSha256: pin}))
	c.sync(3)
	if p, n := cert(); p != "" || n > 0 {
		t.Fatalf("stored %q %d from unusable reports", p, n)
	}

	c.send(4, healthBatch(&agentv1.InboundHealth{InboundId: ids.i1, State: running, CertPinSha256: strings.ToUpper(pin), CertNotAfterUnix: notAfter}))
	c.sync(5)
	if p, n := cert(); p != pin || n != notAfter {
		t.Fatalf("health certificate: %q %d, want %q %d", p, n, pin, notAfter)
	}

	// an expiry far beyond what the panel's own certificates live would hide the real one from the health check
	c.send(6, healthBatch(&agentv1.InboundHealth{InboundId: ids.i1, State: running, CertPinSha256: pin, CertNotAfterUnix: math.MaxInt64}))
	c.sync(7)
	if p, n := cert(); p != pin || n != notAfter {
		t.Fatalf("an absurd expiry replaced the stored one: %q %d, want %q %d", p, n, pin, notAfter)
	}

	// a node cannot write another node's inbound
	c.send(8, healthBatch(&agentv1.InboundHealth{InboundId: ids.i3, State: running, CertPinSha256: pin, CertNotAfterUnix: notAfter}))
	c.sync(9)
	if in3, _ := e.st.Access().Inbound(e.ctx, ids.i3); in3.CertPinSHA256 != "" {
		t.Errorf("foreign inbound got a pin: %q", in3.CertPinSHA256)
	}
}

// An ApplyResult without a certificate blanks the row (that is what the agent said); the next stats batch restores it.
func TestApplyResultWithoutCertificateIsHealedByTheNextStatsBatch(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, ds := connectFull(a, "inst1")
	pin := strings.Repeat("cd", 32)
	notAfter := time.Now().Add(30 * 24 * time.Hour).Unix()
	running := agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING
	h := &agentv1.InboundHealth{InboundId: ids.i1, State: running, CertPinSha256: pin, CertNotAfterUnix: notAfter}

	c.send(1, healthBatch(h))
	c.sync(2)
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: ds.Revision, Status: agentv1.ApplyStatus_APPLY_STATUS_APPLIED, StateHash: ds.StateHash,
		Inbounds: []*agentv1.InboundResult{{InboundId: ids.i1, State: running}}}}})
	c.sync(3)
	if in, _ := e.st.Access().Inbound(e.ctx, ids.i1); in.CertPinSHA256 != "" {
		t.Fatalf("the apply result said no certificate, the row says %q", in.CertPinSHA256)
	}
	c.send(4, healthBatch(h)) // the same report as before: the memo must not hide that the row changed
	c.sync(5)
	if in, _ := e.st.Access().Inbound(e.ctx, ids.i1); in.CertPinSHA256 != pin || in.CertNotAfter.Unix() != notAfter {
		t.Fatalf("after the next batch: %q %v", in.CertPinSHA256, in.CertNotAfter)
	}
}

// The events list joins the profile of an inbound at read time; once the inbound is gone the names the event recorded
// itself are used. Nothing is migrated: the rows of the live database have inbound_id and empty params.
func TestEventListNamesTheProfile(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	c.send(1, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Severity: agentv1.Severity_SEVERITY_INFO, Code: "engine_started", InboundId: ids.i1, TimeUnix: time.Now().Unix()}}})
	c.sync(2)
	for _, row := range []store.EventRow{
		{Time: time.Now(), Severity: 1, Code: "profile_removed", Source: "admin", NodeID: a.nodeID, InboundID: "inb_gone",
			Params: map[string]string{"profile": "old profile", "protocol": "fakewg", "actor": "ops"}},
		{Time: time.Now(), Severity: 1, Code: "update_step_passed", Source: "panel", NodeID: a.nodeID,
			Params: map[string]string{"from_version": "0.1.0-a", "to_version": "0.1.0-b"}},
	} {
		if err := e.st.InsertEvent(e.ctx, row); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := fleetService{e.f}.ListEvents(e.ctx, connect.NewRequest(&adminv1.ListEventsRequest{NodeId: a.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]*adminv1.Event{}
	for _, ev := range resp.Msg.Events {
		by[ev.Code] = ev
	}
	if ev := by["engine_started"]; ev == nil || ev.InboundId != ids.i1 || ev.ProfileName != "p1" || ev.Protocol != "fakehy" || ev.Source != "agent" {
		t.Errorf("engine_started: %+v", ev)
	}
	if ev := by["profile_removed"]; ev == nil || ev.ProfileName != "old profile" || ev.Protocol != "fakewg" || ev.Source != "admin" || ev.Params["actor"] != "ops" {
		t.Errorf("profile_removed: %+v", ev)
	}
	if ev := by["update_step_passed"]; ev == nil || ev.ProfileName != "" || ev.Source != "panel" || ev.Params["to_version"] != "0.1.0-b" {
		t.Errorf("update_step_passed: %+v", ev)
	}
}

// An event of node A that names an inbound of node B must not show B's profile in A's log.
func TestEventListIgnoresAnInboundOfAnotherNode(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	err := e.st.InsertEvent(e.ctx, store.EventRow{Time: time.Now(), Severity: 1, Code: "engine_started", Source: "agent",
		NodeID: a.nodeID, InboundID: ids.i3})
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := e.st.Events(e.ctx, store.EventFilter{NodeID: a.nodeID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Code == "engine_started" {
			if r.InboundID != ids.i3 || r.ProfileName != "" || r.Protocol != "" {
				t.Fatalf("a foreign inbound was resolved: %+v", r)
			}
			return
		}
	}
	t.Fatalf("no engine_started event in %+v", rows)
}

func TestRestartInboundsLeavesAnEventForTheAdmin(t *testing.T) {
	e := newEnv(t)
	e.f.unit = 20 * time.Millisecond
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	e.exec(`UPDATE node SET liveness_timeout_s = 3600`)
	c, _, _ := connectFull(a, "inst1")
	go func() {
		m := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetRestartInbound() != nil })
		c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
			RequestId: m.GetRestartInbound().RequestId, Ok: true, Affected: 1}}})
	}()
	if _, err := (nodeService{e.f}).RestartInbounds(e.ctx, connect.NewRequest(&adminv1.RestartInboundsRequest{NodeId: a.nodeID, InboundId: ids.i1})); err != nil {
		t.Fatal(err)
	}
	rows, _, err := e.st.Events(e.ctx, store.EventFilter{NodeID: a.nodeID, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Code == "profiles_restarted" {
			if r.Source != "admin" || r.InboundID != ids.i1 || r.ProfileName != "p1" || r.Params["count"] != "1" {
				t.Errorf("profiles_restarted: %+v", r)
			}
			return
		}
	}
	t.Fatalf("no profiles_restarted event in %+v", rows)
}
