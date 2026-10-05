package agent

import (
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/hostctl"
)

func TestInboundUDPFirewallTracksOnlyRunningEnabledSpecs(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.eng.mu.Lock()
	h.eng.applyErr["inb_failed"] = errBoom
	h.eng.mu.Unlock()
	disabled := inb("inb_disabled", 0, 0, cred("crd_disabled"))
	disabled.Spec.Enabled = false
	h.panel.push(fullState(1,
		inb("inb_hysteria", 20000, 20010, cred("crd_hysteria")),
		withPort(inb("inb_awg", 0, 0, cred("crd_awg")), 52717),
		withPort(disabled, 53000),
		withPort(inb("inb_failed", 0, 0, cred("crd_failed")), 54000),
	))
	r := h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL {
		t.Fatalf("apply status = %v, want PARTIAL due to failed inbound", r.Status)
	}
	// The periodic credential sweep deliberately re-runs reconcile, which reasserts the firewall desired
	// state. Stop the worker before inspecting the call history, then verify every replay is still exact.
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	calls := h.host.udpPorts()
	want := []hostctl.UDPInboundPort{{From: 20000, To: 20010}, {Port: 443}, {Port: 52717}}
	if len(calls) == 0 {
		t.Fatal("UDP firewall desired set was never synchronized")
	}
	for i, got := range calls {
		if !equalUDPPorts(got, want) {
			t.Fatalf("UDP firewall sync %d = %+v, want exact enabled/running set %+v", i, got, want)
		}
	}

	// A hop range is opened only if the nft redirect was installed successfully.
	h2 := newHarness(t, harnessOpts{noStart: true})
	h2.host.hopErr = errors.New("nft unavailable")
	res := h2.a.applyDesired(t.Context(), fullState(1, inb("inb_hysteria", 20000, 20010, cred("crd_hysteria"))))
	if res.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL || !strings.Contains(res.Inbounds[0].Error, "port hop") {
		t.Fatalf("hop failure was not surfaced: %v", res)
	}
	if got := h2.host.udpPorts(); len(got) != 1 || !equalUDPPorts(got[0], []hostctl.UDPInboundPort{{Port: 443}}) {
		t.Fatalf("firewall opened an uninstalled hop range: %+v", got)
	}
}

func TestInboundUDPFirewallRemovesStaleRulesAndReportsSyncErrors(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_a", 0, 0, cred("crd_a"))))
	h.panel.nextApply()
	h.panel.push(fullState(2))
	r := h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || len(r.Inbounds) != 0 {
		t.Fatalf("remove: %v", r)
	}
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	calls := h.host.udpPorts()
	wantBeforeRemoval := []hostctl.UDPInboundPort{{Port: 443}}
	if len(calls) < 2 || !equalUDPPorts(calls[0], wantBeforeRemoval) {
		t.Fatalf("UDP firewall sync calls = %+v, want initial listener and a removal sync", calls)
	}
	removed := false
	for i, got := range calls {
		switch {
		case equalUDPPorts(got, wantBeforeRemoval) && !removed:
			// Replays before the removal may reassert the same exact set.
		case len(got) == 0:
			removed = true
		case equalUDPPorts(got, wantBeforeRemoval):
			t.Fatalf("stale UDP listener was reopened after removal at sync %d: %+v", i, calls)
		default:
			t.Fatalf("UDP firewall sync %d opened a stale or unwanted rule: %+v", i, calls)
		}
	}
	if !removed {
		t.Fatalf("removed inbound left UDP desired rules: %+v", calls)
	}

	h2 := newHarness(t, harnessOpts{})
	h2.waitConnected()
	h2.host.udpErr = errors.New("firewalld is active; add 443/udp manually")
	h2.panel.push(fullState(1, inb("inb_a", 0, 0, cred("crd_a"))))
	r = h2.panel.nextApply()
	// The listener runs: a host firewall the agent could not reconcile is a node warning, not an inbound failure.
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || r.Inbounds[0].Error != "" || r.Inbounds[0].State != pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
		t.Fatalf("firewall sync error was attached to the inbound: %v", r)
	}
	eventually(t, func() bool {
		evs := h2.panel.eventsByCode("host_firewall_sync_failed")
		return len(evs) == 1 && evs[0].Severity == pb.Severity_SEVERITY_WARNING && strings.Contains(evs[0].Params["error"], "firewalld is active")
	}, "one host_firewall_sync_failed warning")
}

func TestHostFirewallSyncEventsTrackFailureTransitions(t *testing.T) {
	const (
		failedCode    = "host_firewall_sync_failed"
		recoveredCode = "host_firewall_sync_recovered"
	)
	failure := errors.New("firewalld is active; add 443/udp manually")
	h := newHarness(t, harnessOpts{tune: func(a *Agent) { a.sweepEvery = time.Hour }})
	h.waitConnected()
	setFirewallErr := func(err error) {
		h.host.mu.Lock()
		h.host.udpErr = err
		h.host.mu.Unlock()
	}
	setFirewallErr(failure)
	h.panel.push(fullState(1, inb("inb_a", 0, 0, cred("crd_a"))))
	result := h.panel.nextApply()
	if result.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || len(result.Inbounds) != 1 || result.Inbounds[0].Error != "" {
		t.Fatalf("firewall failure became an inbound error: %v", result)
	}

	// These are the same reconcile pass the periodic sweep runs; repeated identical failures must not enqueue more events.
	m := h.a.snapshotModel()
	h.a.reconcile(t.Context(), m, nil)
	h.a.reconcile(t.Context(), m, nil)
	setFirewallErr(nil)
	h.a.reconcile(t.Context(), m, nil)
	eventually(t, func() bool { return len(h.panel.eventsByCode(recoveredCode)) == 1 }, "one host-firewall recovery event")
	if got := len(h.panel.eventsByCode(failedCode)); got != 1 {
		t.Fatalf("same failure emitted %d failure events before recovery, want 1", got)
	}

	setFirewallErr(failure)
	h.a.reconcile(t.Context(), m, nil)
	h.a.reconcile(t.Context(), m, nil)
	eventually(t, func() bool { return len(h.panel.eventsByCode(failedCode)) == 2 }, "later host-firewall failure event")

	var transitions []string
	for _, code := range h.panel.eventCodes() {
		if code == failedCode || code == recoveredCode {
			transitions = append(transitions, code)
		}
	}
	want := []string{failedCode, recoveredCode, failedCode}
	if len(transitions) != len(want) {
		t.Fatalf("host-firewall transition events = %v, want %v", transitions, want)
	}
	for i := range want {
		if transitions[i] != want[i] {
			t.Fatalf("host-firewall transition events = %v, want %v", transitions, want)
		}
	}
}

func equalUDPPorts(a, b []hostctl.UDPInboundPort) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
