package access

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type accessPortCall struct {
	node  string
	ports []uint16
}

type fakeAccessPortChecker struct {
	mu      sync.Mutex
	calls   []accessPortCall
	active  int
	maximum int
	reason  string
	reply   func(string, uint16) (store.PortCheck, bool)
	started chan struct{}
	release <-chan struct{}
}

func (f *fakeAccessPortChecker) check(ctx context.Context, nodeID string, ports []uint16) ([]store.PortCheck, string, string) {
	f.mu.Lock()
	f.calls = append(f.calls, accessPortCall{node: nodeID, ports: slices.Clone(ports)})
	f.active++
	if f.active > f.maximum {
		f.maximum = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, "", "failed"
		}
	}
	var rows []store.PortCheck
	for _, port := range ports {
		if f.reply == nil {
			continue
		}
		row, ok := f.reply(nodeID, port)
		if !ok {
			continue
		}
		if row.NodeID == "" {
			row.NodeID = nodeID
		}
		if row.Port == 0 {
			row.Port = port
		}
		if row.Sender == "" {
			row.Sender = "panel"
		}
		if row.CheckedAt.IsZero() {
			row.CheckedAt = time.Unix(1790779200, 0).UTC()
		}
		rows = append(rows, row)
	}
	return rows, "panel", f.reason
}

func (f *fakeAccessPortChecker) snapshot() ([]accessPortCall, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := make([]accessPortCall, len(f.calls))
	for i, call := range f.calls {
		calls[i] = accessPortCall{node: call.node, ports: slices.Clone(call.ports)}
	}
	return calls, f.maximum
}

func usePortChecker(e *env, reply func(string, uint16) (store.PortCheck, bool), reason string) *fakeAccessPortChecker {
	f := &fakeAccessPortChecker{reply: reply, reason: reason}
	e.s.cfg.CheckPorts = f.check
	return f
}

func portResult(e *env, node string, port uint16, verdict string, sent, got uint32) store.PortCheck {
	n := must(e.st.Access().Node(e.ctx, node))
	return store.PortCheck{NodeID: node, Address: n.Address, Port: port, Sent: sent, Got: got, Verdict: verdict,
		Sender: "panel", CheckedAt: e.clock}
}

func savePortResult(t *testing.T, e *env, row store.PortCheck) {
	t.Helper()
	if err := e.st.PutPortChecks(e.ctx, []store.PortCheck{row}); err != nil {
		t.Fatal(err)
	}
}

func warningWithCode(warnings []*adminv1.StatusReason, code string) *adminv1.StatusReason {
	for _, warning := range warnings {
		if warning.Code == code {
			return warning
		}
	}
	return nil
}

func TestPortPickerSkipsBadAndPrefersFresh(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	main := e.profile("main", "")
	e.inbound(main.Id, "nod_de1")
	savePortResult(t, e, portResult(e, "nod_de1", 8443, "broken", 300, 150))
	savePortResult(t, e, portResult(e, "nod_de1", 4443, "ok", 300, 300))
	checker := usePortChecker(e, nil, "")

	second := e.profile("second", `{"port":25000}`)
	checked := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{
		ProfileId: second.Id, NodeId: "nod_de1", ValidateOnly: true,
	}))).Msg
	if checked.FreePort != 4443 {
		t.Fatalf("free_port = %d, want fresh 4443", checked.FreePort)
	}

	third := e.profile("third", "")
	_, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{
		ProfileId: third.Id, NodeId: "nod_de1", ValidateOnly: true,
	}))
	wantCoded(t, err, connect.CodeAlreadyExists, "port_taken", map[string]string{"free": "4443"})
	if calls, _ := checker.snapshot(); len(calls) != 0 {
		t.Fatalf("cache-only picker called CheckPorts: %+v", calls)
	}
}

func TestPortValidationUsesOnlyCachedChecks(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	f := usePortChecker(e, nil, "")
	portReads := 0
	e.s.portChecksReadHookForTest = func() { portReads++ }
	bad := portResult(e, "nod_de1", 8443, "broken", 300, 100)
	savePortResult(t, e, bad)
	main := e.profile("main", `{"tls_mode":"self_signed"}`)
	response, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{
		ProfileId: main.Id, NodeId: "nod_de1", PortOverride: 8443, ValidateOnly: true,
	}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "port_lossy", map[string]string{"port": "8443", "node": "de1"})
	if response != nil {
		t.Fatalf("lossy validate_only returned a response: %+v", response.Msg)
	}
	if portReads != 1 {
		t.Fatalf("CreateInbound validate_only read port checks %d times, want 1", portReads)
	}
	allowed := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{
		ProfileId: main.Id, NodeId: "nod_de1", PortOverride: 8443, ValidateOnly: true, AllowLossyPort: true,
	}))).Msg
	if warningWithCode(allowed.Warnings, "port_lossy") == nil || allowed.Inbound.Port != 8443 {
		t.Fatalf("allow-lossy validate_only = %+v", allowed)
	}
	if portReads != 2 {
		t.Fatalf("second CreateInbound validate_only made %d total port-check reads, want 2", portReads)
	}

	fixture := newFixture(t)
	fixtureChecker := usePortChecker(fixture.e, nil, "")
	fixtureReads := 0
	fixture.e.s.portChecksReadHookForTest = func() { fixtureReads++ }
	fixtureBad := portResult(fixture.e, fixture.nodeID, 8443, "broken", 300, 100)
	savePortResult(t, fixture.e, fixtureBad)
	_, err = fixture.e.s.UpdateInbound(fixture.e.ctx, req(&adminv1.UpdateInboundRequest{
		InboundId: fixture.inbound, PortOverride: new(uint32(8443)), ValidateOnly: true,
	}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "port_lossy", map[string]string{"port": "8443"})
	if fixtureReads != 1 {
		t.Fatalf("UpdateInbound validate_only read port checks %d times, want 1", fixtureReads)
	}
	version := must(fixture.e.st.Access().Profile(fixture.e.ctx, fixture.profile)).Version
	settings := `{"port":8443}`
	_, err = fixture.e.s.UpdateProfile(fixture.e.ctx, req(&adminv1.UpdateProfileRequest{
		ProfileId: fixture.profile, ExpectedVersion: version, SettingsJson: &settings, DryRun: true,
	}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "port_lossy", map[string]string{"port": "8443"})
	if fixtureReads != 2 {
		t.Fatalf("UpdateProfile dry_run made %d total port-check reads, want 2", fixtureReads)
	}
	if calls, _ := f.snapshot(); len(calls) != 0 {
		t.Fatalf("validate_only or dry_run called CheckPorts: %+v", calls)
	}
	if calls, _ := fixtureChecker.snapshot(); len(calls) != 0 {
		t.Fatalf("UpdateInbound validate_only called CheckPorts: %+v", calls)
	}
}

func TestCreateInboundChecksChangedPort(t *testing.T) {
	t.Run("fresh result skips check", func(t *testing.T) {
		e := newEnv(t)
		e.node("nod_de1", "de1", "de1.example.com", "active")
		checker := usePortChecker(e, nil, "")
		savePortResult(t, e, portResult(e, "nod_de1", 8443, "ok", 300, 300))
		p := e.profile("main", `{"tls_mode":"self_signed"}`)
		created := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_de1", PortOverride: 8443}))).Msg
		if created.Inbound.Port != 8443 || len(created.Warnings) != 0 {
			t.Fatalf("created inbound = %+v", created)
		}
		if calls, _ := checker.snapshot(); len(calls) != 0 {
			t.Fatalf("fresh result ran CheckPorts: %+v", calls)
		}
	})

	t.Run("unchecked saves with warning", func(t *testing.T) {
		e := newEnv(t)
		e.node("nod_de1", "de1", "de1.example.com", "active")
		checker := usePortChecker(e, nil, "node_offline")
		p := e.profile("main", `{"tls_mode":"self_signed"}`)
		created := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_de1", PortOverride: 8443}))).Msg
		warning := warningWithCode(created.Warnings, "port_unchecked")
		if warning == nil || warning.Params["reason"] != "node_offline" || created.Inbound.Id == "" {
			t.Fatalf("unchecked inbound = %+v", created)
		}
		if calls, _ := checker.snapshot(); len(calls) != 1 {
			t.Fatalf("CheckPorts calls = %d, want 1", len(calls))
		} else if !slices.Equal(calls[0].ports[:2], []uint16{8443, 443}) {
			t.Fatalf("checked ports = %v", calls[0].ports)
		}
	})

	t.Run("bad result refuses with proven alternative", func(t *testing.T) {
		e := newEnv(t)
		e.node("nod_de1", "de1", "de1.example.com", "active")
		checker := usePortChecker(e, func(node string, port uint16) (store.PortCheck, bool) {
			if port == 8443 {
				return portResult(e, node, port, "broken", 300, 100), true
			}
			if port == 443 {
				return portResult(e, node, port, "ok", 300, 300), true
			}
			return store.PortCheck{}, false
		}, "")
		p := e.profile("main", `{"tls_mode":"self_signed"}`)
		_, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: "nod_de1", PortOverride: 8443}))
		wantCoded(t, err, connect.CodeFailedPrecondition, "port_lossy", map[string]string{
			"port": "8443", "node": "de1", "sent": "300", "got": "100", "free": "443",
		})
		if calls, _ := checker.snapshot(); len(calls) != 1 {
			t.Fatalf("CheckPorts calls = %d, want 1", len(calls))
		}
	})

	t.Run("add anyway saves and audits the choice", func(t *testing.T) {
		e := newEnv(t)
		e.node("nod_de1", "de1", "de1.example.com", "active")
		usePortChecker(e, func(node string, port uint16) (store.PortCheck, bool) {
			if port == 8443 {
				return portResult(e, node, port, "lossy", 300, 240), true
			}
			if port == 443 {
				return portResult(e, node, port, "ok", 300, 300), true
			}
			return store.PortCheck{}, false
		}, "")
		p := e.profile("main", `{"tls_mode":"self_signed"}`)
		created := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{
			ProfileId: p.Id, NodeId: "nod_de1", PortOverride: 8443, AllowLossyPort: true,
		}))).Msg
		if created.Inbound.Id == "" || warningWithCode(created.Warnings, "port_lossy") == nil {
			t.Fatalf("allow-lossy create = %+v", created)
		}
		assertLossyOverrideAudit(t, e)
	})
}

func TestUpdateInboundUnchangedLossyPortWarnsWithoutRefusal(t *testing.T) {
	f := newFixture(t)
	savePortResult(t, f.e, portResult(f.e, f.nodeID, 443, "broken", 300, 100))
	checker := usePortChecker(f.e, nil, "")
	sni := "vpn.example.com"
	updated := must(f.e.s.UpdateInbound(f.e.ctx, req(&adminv1.UpdateInboundRequest{
		InboundId: f.inbound, TlsServerNameOverride: &sni,
	}))).Msg
	if warningWithCode(updated.Warnings, "port_lossy") == nil || updated.Inbound.TlsServerName != sni {
		t.Fatalf("SNI update = %+v", updated)
	}
	if calls, _ := checker.snapshot(); len(calls) != 0 {
		t.Fatalf("unchanged port ran CheckPorts: %+v", calls)
	}
}

func TestUpdateInboundSwitchOnChecksLossyPort(t *testing.T) {
	f := newFixture(t)
	turnedOff := false
	must(f.e.s.UpdateInbound(f.e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: f.inbound, Enabled: &turnedOff})))
	savePortResult(t, f.e, portResult(f.e, f.nodeID, 443, "broken", 300, 100))
	checker := usePortChecker(f.e, func(node string, port uint16) (store.PortCheck, bool) {
		if port == 443 {
			return portResult(f.e, node, port, "broken", 300, 100), true
		}
		return portResult(f.e, node, port, "ok", 300, 300), true
	}, "")
	turnedOn := true
	_, err := f.e.s.UpdateInbound(f.e.ctx, req(&adminv1.UpdateInboundRequest{
		InboundId: f.inbound, Enabled: &turnedOn, ValidateOnly: true,
	}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "port_lossy", map[string]string{"port": "443"})
	if calls, _ := checker.snapshot(); len(calls) != 0 {
		t.Fatalf("validate_only switch-on called CheckPorts: %+v", calls)
	}
	_, err = f.e.s.UpdateInbound(f.e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: f.inbound, Enabled: &turnedOn}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "port_lossy", map[string]string{"port": "443"})
	updated := must(f.e.s.UpdateInbound(f.e.ctx, req(&adminv1.UpdateInboundRequest{
		InboundId: f.inbound, Enabled: &turnedOn, AllowLossyPort: true,
	}))).Msg
	if updated.Inbound.State != adminv1.InboundState_INBOUND_STATE_PENDING || warningWithCode(updated.Warnings, "port_lossy") == nil {
		t.Fatalf("allow-lossy switch-on = %+v", updated)
	}
	assertLossyOverrideAudit(t, f.e)
	if calls, _ := checker.snapshot(); len(calls) != 2 {
		t.Fatalf("CheckPorts calls = %d, want 2 real attempts", len(calls))
	}
}

func TestUpdateProfileChecksChangedPortPerNode(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	p := e.profile("main", `{"tls_mode":"self_signed"}`)
	e.inbound(p.Id, "nod_de1")
	e.inbound(p.Id, "nod_nl1")
	checker := usePortChecker(e, func(node string, port uint16) (store.PortCheck, bool) {
		if port == 8443 && node == "nod_de1" {
			return portResult(e, node, port, "broken", 300, 100), true
		}
		return portResult(e, node, port, "ok", 300, 300), true
	}, "")
	settings := `{"port":8443}`
	_, err := e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{
		ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings,
	}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "port_lossy", map[string]string{"node": "de1", "port": "8443"})
	if got := must(e.st.Access().Profile(e.ctx, p.Id)); got.Version != p.Version {
		t.Fatalf("refused update changed profile version to %d", got.Version)
	}

	updated := must(e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{
		ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings, AllowLossyPort: true,
	}))).Msg
	if warningWithCode(updated.Profile.Warnings, "port_lossy") == nil {
		t.Fatalf("allow-lossy profile update warnings = %+v", updated.Profile.Warnings)
	}
	if got := must(e.st.Access().Profile(e.ctx, p.Id)); got.Version != p.Version+1 {
		t.Fatalf("profile version = %d, want %d", got.Version, p.Version+1)
	}
	assertLossyOverrideAudit(t, e)
	if calls, _ := checker.snapshot(); len(calls) != 3 {
		t.Fatalf("CheckPorts calls = %d, want 3 (one before refusal and one per node when allowed)", len(calls))
	}
}

func TestTwinPortChecksCandidatesInParallelAndSkipsBad(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	p := e.profile("main", `{"tls_mode":"self_signed"}`)
	e.inbound(p.Id, "nod_de1")
	e.inbound(p.Id, "nod_nl1")
	savePortResult(t, e, portResult(e, "nod_de1", 8443, "broken", 300, 100))
	release := make(chan struct{})
	checker := usePortChecker(e, func(node string, port uint16) (store.PortCheck, bool) {
		if port == 4443 && node == "nod_de1" {
			return portResult(e, node, port, "broken", 300, 100), true
		}
		return portResult(e, node, port, "ok", 300, 300), true
	}, "")
	checker.started, checker.release = make(chan struct{}, 4), release
	portReads := 0
	e.s.portChecksReadHookForTest = func() { portReads++ }
	result := make(chan struct {
		response *adminv1.TwinProfileResponse
		err      error
	}, 1)
	go func() {
		response, err := e.twin(p.Id, "warp", true, 0)
		result <- struct {
			response *adminv1.TwinProfileResponse
			err      error
		}{response, err}
	}()
	<-checker.started
	<-checker.started
	_, maximum := checker.snapshot()
	close(release)
	got := <-result
	if got.err != nil {
		t.Fatal(got.err)
	}
	if maximum != 2 {
		t.Fatalf("parallel checks observed max concurrency %d, want 2", maximum)
	}
	if portReads != 1 {
		t.Fatalf("TwinProfile read port checks %d times, want 1", portReads)
	}
	if got.response.Port != 2053 {
		t.Fatalf("twin port = %d, want 2053 after skipping bad candidates", got.response.Port)
	}
	if len(got.response.PortChecks) != 4 {
		t.Fatalf("port_checks = %+v, want chosen port on two nodes plus two bad candidates", got.response.PortChecks)
	}
	chosen := 0
	for _, check := range got.response.PortChecks {
		if check.Port == 2053 && check.Verdict == "ok" {
			chosen++
		}
	}
	if chosen != 2 {
		t.Fatalf("chosen port results = %d, want 2: %+v", chosen, got.response.PortChecks)
	}
	calls, _ := checker.snapshot()
	if len(calls) != 2 || !slices.Equal(calls[0].ports, []uint16{4443, 2053, 2083, 2087}) ||
		!slices.Equal(calls[1].ports, calls[0].ports) {
		t.Fatalf("twin checks = %+v", calls)
	}
}

func TestTwinPortSkipsNodeWithFourFreshResults(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	p := e.profile("main", `{"tls_mode":"self_signed"}`)
	e.inbound(p.Id, "nod_de1")
	e.inbound(p.Id, "nod_nl1")
	for _, port := range []uint16{8443, 4443, 2053, 2083} {
		savePortResult(t, e, portResult(e, "nod_de1", port, "ok", 300, 300))
	}
	checker := usePortChecker(e, func(node string, port uint16) (store.PortCheck, bool) {
		return portResult(e, node, port, "ok", 300, 300), true
	}, "")
	plan, err := e.twin(p.Id, "warp", true, 0)
	if err != nil || plan.Port != 8443 {
		t.Fatalf("twin plan = %+v, %v", plan, err)
	}
	if calls, _ := checker.snapshot(); len(calls) != 1 || calls[0].node != "nod_nl1" {
		t.Fatalf("checks = %+v, want only nod_nl1", calls)
	}
}

func TestTwinPortNoCleanPortAfterTwoBatches(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.profile("main", `{"tls_mode":"self_signed"}`)
	e.inbound(p.Id, "nod_de1")
	checker := usePortChecker(e, func(node string, port uint16) (store.PortCheck, bool) {
		return portResult(e, node, port, "broken", 300, 100), true
	}, "")
	_, err := e.twin(p.Id, "warp", true, 0)
	wantCoded(t, err, connect.CodeFailedPrecondition, "no_clean_port", nil)
	if calls, _ := checker.snapshot(); len(calls) != 2 {
		t.Fatalf("CheckPorts calls = %d, want two candidate batches", len(calls))
	}
}

func TestRealTwinRefusesPortThatTurnedBad(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.profile("main", `{"tls_mode":"self_signed"}`)
	e.inbound(p.Id, "nod_de1")
	usePortChecker(e, func(node string, port uint16) (store.PortCheck, bool) {
		return portResult(e, node, port, "ok", 300, 300), true
	}, "")
	plan, err := e.twin(p.Id, "warp", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	savePortResult(t, e, portResult(e, "nod_de1", uint16(plan.Port), "broken", 300, 100))
	_, err = e.twin(p.Id, "warp", false, plan.Port)
	wantCoded(t, err, connect.CodeFailedPrecondition, "port_lossy", map[string]string{"node": "de1", "port": strconv.Itoa(int(plan.Port))})
}

func assertLossyOverrideAudit(t *testing.T, e *env) {
	t.Helper()
	for _, row := range must(e.st.ListAudit(e.ctx, "", 0, 100)) {
		if row.Action != "port_lossy_override" {
			continue
		}
		var params map[string]any
		if err := json.Unmarshal([]byte(row.Params), &params); err != nil {
			t.Fatal(err)
		}
		decision, _ := params["decision"].(string)
		if row.Result == "ok" && strings.Contains(decision, "chose to save a lossy port") {
			return
		}
	}
	t.Fatal("no audit row records that the admin chose to save a lossy port")
}
