package health

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func newRelayMonitorEnv(t *testing.T, mut ...func(*Config)) (*env, string, string) {
	t.Helper()
	e := newEnv(t, mut...)
	e.node("de1", "provider-a", true)
	e.node("fi1", "provider-b", true)
	exitInbound := e.inbound("de1", 443)
	now := e.clock.Now()
	if err := e.st.CreateRelay(e.ctx, store.Relay{ID: "rly_test", InboundID: exitInbound, ViaNodeID: "fi1", Network: "udp", Port: 8443}, now); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err := e.st.UpdateRelay(e.ctx, "rly_test", store.RelayPatch{Enabled: &enabled}, now); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE relay SET state = 'active' WHERE id = 'rly_test'`)
	e.s.invalidateSnapshot()
	return e, exitInbound, "rly_test"
}

func setRelayMonitor(t *testing.T, e *env, id string, ok bool, streak uint32, stage string) {
	t.Helper()
	e.s.invalidateSnapshot()
	sn, err := e.s.snapshot(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := sn.relayTargets[relayTargetID(id)]
	if target == nil {
		t.Fatalf("no client-eye target for %s", id)
	}
	monitor := store.RelayMonitorCheck{AtUnix: e.clock.Now().Unix(), OK: ok, Stage: stage, Code: "timeout", FailStreak: streak,
		TargetKey: relayTargetKey(target)}
	if ok {
		monitor.Code, monitor.Stage = "", ""
	}
	if err := e.st.SetRelayMonitorCheck(e.ctx, id, monitor); err != nil {
		t.Fatal(err)
	}
}

func TestRelayDownAlertStreakAndRecovery(t *testing.T) {
	var transitions []Transition
	e, _, id := newRelayMonitorEnv(t, func(c *Config) {
		c.Interval = time.Minute
		c.OnTransition = func(tr Transition) { transitions = append(transitions, tr) }
	})
	setRelayMonitor(t, e, id, false, 1, "freeze")
	e.evaluate()
	want(t, e.active())

	e.clock.Advance(time.Second)
	setRelayMonitor(t, e, id, false, 2, "freeze")
	e.evaluate()
	key := "relay_down/fi1/" + id
	alerts := e.active()
	want(t, alerts, key)
	alert := alerts[key]
	if alert.NodeID != "fi1" || alert.Subject != id || alert.WhyKey != "health.alert.relay_down.why.freeze" ||
		alert.Params["relay"] != "fi1" || alert.Params["exit"] != "de1" || alert.Params["exit_profile"] == "" ||
		alert.Params["port"] != "8443" || alert.Params["stage"] != "freeze" {
		t.Fatalf("relay alert = %+v", alert)
	}
	if len(transitions) != 1 || transitions[0].Resolved {
		t.Fatalf("open transition = %+v", transitions)
	}

	e.clock.Advance(time.Second)
	setRelayMonitor(t, e, id, true, 0, "")
	e.evaluate()
	want(t, e.active())
	history := e.history()
	var resolved *store.HealthAlert
	for i := range history {
		if history[i].ID == alert.ID {
			resolved = &history[i]
			break
		}
	}
	if resolved == nil || resolved.ResolvedAt.IsZero() || resolved.Resolution != "cleared" {
		t.Fatalf("recovered relay alert = %+v", resolved)
	}
	if len(transitions) != 2 || !transitions[1].Resolved {
		t.Fatalf("recovery transition = %+v", transitions)
	}
}

func TestReverseRelayDownAlertUsesBridgeStage(t *testing.T) {
	e, _, id := newRelayMonitorEnv(t)
	now := e.clock.Now()
	e.exec(`UPDATE relay SET network = 'tcp' WHERE id = ?`, id)
	if err := e.st.SetRelayActiveMode(e.ctx, id, store.RelayModeReverse, "leg_failed", now); err != nil {
		t.Fatal(err)
	}
	check := store.RelayCheck{At: now.Unix(), Network: "tcp", Port: 8443, Leg: store.RelayStage{Status: "fail"},
		Path: store.RelayStage{Status: "fail", Code: "timeout"}}
	if err := e.st.SetRelayCheck(e.ctx, id, check); err != nil {
		t.Fatal(err)
	}
	setRelayMonitor(t, e, id, false, 2, "bridge")
	e.evaluate()
	alert := e.active()["relay_down/fi1/"+id]
	if alert.WhyKey != "health.alert.relay_down.why.bridge" || alert.Params["stage"] != "bridge" {
		t.Fatalf("reverse relay alert = %+v, want bridge stage", alert)
	}
}

func TestRelayDownIsSuperseded(t *testing.T) {
	t.Run("exit check alert", func(t *testing.T) {
		e, exitInbound, id := newRelayMonitorEnv(t)
		setRelayMonitor(t, e, id, false, 2, "path")
		e.evaluate()
		if _, ok := e.active()["relay_down/fi1/"+id]; !ok {
			t.Fatal("relay_down did not open before the exit check alert")
		}
		other := e.inbound("de1", 5443)
		e.pass(other)
		e.fail(exitInbound, "timeout")
		e.fail(exitInbound, "timeout")
		want(t, e.active(), "check_failed/de1/"+exitInbound)
		assertRelayResolution(t, e, id, "superseded")
	})

	t.Run("entry node down", func(t *testing.T) {
		e, _, id := newRelayMonitorEnv(t)
		setRelayMonitor(t, e, id, false, 2, "leg")
		e.evaluate()
		if _, ok := e.active()["relay_down/fi1/"+id]; !ok {
			t.Fatal("relay_down did not open before the entry node went down")
		}
		old := e.clock.Now().Add(-20 * time.Minute)
		e.exec(`UPDATE node SET last_seen_at = ?, last_connected_at = ? WHERE id = 'fi1'`, old.Unix(), old.Unix())
		e.fl.set("fi1", liveState{up: false, lastSeen: old})
		e.evaluate()
		want(t, e.active(), "node_down/fi1/")
		assertRelayResolution(t, e, id, "superseded")
	})
}

func assertRelayResolution(t *testing.T, e *env, id, resolution string) {
	t.Helper()
	for _, alert := range e.history() {
		if alert.Kind == kRelayDown && alert.Subject == id {
			if alert.ResolvedAt.IsZero() || alert.Resolution != resolution {
				t.Fatalf("relay alert resolution = %+v, want %q", alert, resolution)
			}
			return
		}
	}
	t.Fatalf("relay alert %q is absent from history", id)
}

func TestEnabledRelayJoinsPeriodicClientEyeRounds(t *testing.T) {
	e, exitInbound, id := newRelayMonitorEnv(t)
	sn, err := e.s.snapshot(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	target := sn.relayTargets[relayTargetID(id)]
	if target == nil || target.node.ID != "de1" || target.relay.entry.ID != "fi1" ||
		target.spec.Listen.Port != 8443 || target.relay.exitPort != 443 {
		t.Fatalf("relay target = %+v", target)
	}
	e.s.sched[exitInbound] = &schedule{next: e.clock.Now().Add(time.Hour)}
	e.s.sched[relayTargetID(id)] = &schedule{next: e.clock.Now().Add(-time.Second)}
	e.s.scheduleDue(e.ctx)
	select {
	case got := <-e.s.work:
		if got != relayTargetID(id) {
			t.Fatalf("scheduled %q, want relay target %q", got, relayTargetID(id))
		}
	default:
		t.Fatal("enabled relay was not queued for its periodic client-eye round")
	}
}

func TestRelayAutomaticDiagnosisRepeatLimit(t *testing.T) {
	e, _, id := newRelayMonitorEnv(t, func(c *Config) {
		c.Dialers = map[string]Dialer{"hysteria2": func(context.Context, Target, DialOptions) (Tunnel, error) {
			return nil, errors.New("probe dial failed")
		}}
	})
	e.fl.mu.Lock()
	e.fl.relayCheck = func(context.Context, string, ...string) (store.RelayCheck, error) {
		return store.RelayCheck{Leg: store.RelayStage{Status: "fail", Code: "refused"}}, nil
	}
	e.fl.mu.Unlock()

	run := func() {
		t.Helper()
		if _, ok := e.s.roundRelay(e.ctx, relayTargetID(id)); !ok {
			t.Fatal("relay client-eye round was skipped")
		}
	}
	count := func() int {
		e.fl.mu.Lock()
		defer e.fl.mu.Unlock()
		return len(e.fl.relayCalls)
	}
	run()
	if count() != 1 {
		t.Fatalf("automatic diagnosis calls = %d, want 1", count())
	}
	run()
	if count() != 1 {
		t.Fatalf("a repeated failed round bypassed the diagnosis limit: %d calls", count())
	}
	e.clock.Advance(relayAutoCheckInterval - time.Minute)
	run()
	if count() != 1 {
		t.Fatalf("diagnosis ran before the limit expired: %d calls", count())
	}
	e.clock.Advance(time.Minute)
	run()
	if count() != 2 {
		t.Fatalf("diagnosis did not become due at the limit: %d calls", count())
	}
}

// A failed round of a relay that can use the bridge is diagnosed with the exit's probe of the entry's bridge endpoint in the same
// pass: the controller needs it fresh to move the relay (a flow that froze leaves the leg alive, so the probe would not run by itself),
// and the next diagnosis is half an hour away. A relay that cannot (UDP, an agent that is too old, the owner's dnat) gets the pair it
// always got.
func TestRelayDiagnosisAsksForReverseReachWhereTheBridgeIsPossible(t *testing.T) {
	for _, tc := range []struct {
		name    string
		network string
		caps    string
		mode    string
		want    []string
	}{
		{"tcp, both agents, auto", "tcp", "relay-forward/1 relay-reverse/1", store.RelayModeAuto, []string{store.RelayStageLeg, store.RelayStagePath, store.RelayStageReverseReach}},
		{"udp", "udp", "relay-forward/1 relay-reverse/1", store.RelayModeAuto, nil},
		{"an agent without the capability", "tcp", "relay-forward/1", store.RelayModeAuto, nil},
		{"the owner's dnat", "tcp", "relay-forward/1 relay-reverse/1", store.RelayModeDNAT, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _, id := newRelayMonitorEnv(t, func(c *Config) {
				c.Dialers = map[string]Dialer{"hysteria2": func(context.Context, Target, DialOptions) (Tunnel, error) {
					return nil, errors.New("probe dial failed")
				}}
			})
			e.exec(`UPDATE relay SET network = ?, mode = ? WHERE id = ?`, tc.network, tc.mode, id)
			e.exec(`UPDATE node SET agent_caps = ? WHERE id IN (SELECT via_node_id FROM relay WHERE id = ?)`, tc.caps, id)
			e.exec(`UPDATE node SET agent_caps = ? WHERE id IN (SELECT node_id FROM inbound WHERE id IN (SELECT inbound_id FROM relay WHERE id = ?))`, "relay-reverse/1 tcpcheck/1", id)
			if _, ok := e.s.roundRelay(e.ctx, relayTargetID(id)); !ok {
				t.Fatal("relay client-eye round was skipped")
			}
			e.fl.mu.Lock()
			defer e.fl.mu.Unlock()
			if len(e.fl.relayStages) != 1 || !slices.Equal(e.fl.relayStages[0], tc.want) {
				t.Fatalf("stages asked for = %v, want one call with %v", e.fl.relayStages, tc.want)
			}
		})
	}
}

func TestRelayDownRequiresFreshMatchingMonitorTarget(t *testing.T) {
	e, _, id := newRelayMonitorEnv(t)
	setRelayMonitor(t, e, id, false, 2, "path")
	e.exec(`UPDATE relay SET port = 8444 WHERE id = ?`, id)
	e.evaluate()
	want(t, e.active())
	if _, ok := e.active()["relay_down/fi1/"+id]; ok {
		t.Fatal("an alert opened for a monitor result measured on an old forwarded port")
	}
}

func TestUnverifiableRelayCheckDoesNotOpenRelayDown(t *testing.T) {
	e, _, id := newRelayMonitorEnv(t)
	check := store.RelayCheck{At: e.clock.Now().Unix(), Network: "udp", Port: 8443,
		Leg:  store.RelayStage{Status: "skipped", Code: "unverifiable"},
		Path: store.RelayStage{Status: "skipped", Code: "unverifiable"}}
	check.Settle()
	if !check.OK || check.Verified() {
		t.Fatalf("check = %+v", check)
	}
	if err := e.st.SetRelayCheck(e.ctx, id, check); err != nil {
		t.Fatal(err)
	}
	e.evaluate()
	if _, ok := e.active()["relay_down/fi1/"+id]; ok {
		t.Fatal("an unverifiable setup check opened relay_down without a failed client-eye round")
	}
}

func TestDoctorReportCopiesRelayCounters(t *testing.T) {
	e, _, id := newRelayMonitorEnv(t)
	report := &agentv1.DoctorReport{Results: []*agentv1.DoctorResult{{
		Id: "relay", Status: agentv1.DoctorStatus_DOCTOR_STATUS_OK,
		Params: map[string]string{"relay": id, "network": "udp", "port": "8443", "bytes": "65536", "packets": "42"},
	}}}
	e.s.DoctorReport(e.ctx, "fi1", report)
	link, err := e.st.RelayLink(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	check, ok := link.Check()
	if !ok || check.Counters == nil || check.Counters.Bytes != 65536 || check.Counters.Packets != 42 ||
		check.Counters.CountedUnix != e.clock.Now().Unix() {
		t.Fatalf("stored relay counters = %+v, check ok=%v", check.Counters, ok)
	}
}
