package health

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type countryRelayFixture struct {
	vless, hysteria2 string
}

func newCountryRelayFixture(t *testing.T, c *countryEnv) countryRelayFixture {
	t.Helper()
	c.onlineNode("de1", "203.0.113.10")
	c.node("msk1", "hoster-msk1", true)
	c.exec(`UPDATE node SET agent_caps = 'relay-forward/1' WHERE id = 'msk1'`)
	c.exec(`UPDATE inbound SET direct_in_subscriptions = 1 WHERE node_id = 'de1'`)

	vless, hysteria2 := "", ""
	rows, err := c.st.Access().InboundsOfNode(c.ctx, "de1")
	if err != nil {
		t.Fatal(err)
	}
	var profiles []string
	for _, row := range rows {
		profiles = append(profiles, row.ProfileID)
		if row.PortOverride == 443 {
			vless = row.ID
		} else {
			hysteria2 = row.ID
		}
	}
	if vless == "" || hysteria2 == "" {
		t.Fatalf("country fixture inbounds = %+v", rows)
	}
	group, err := c.acc.CreateGroup(c.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "relay-audience", ProfileIds: profiles}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.acc.CreateUser(c.ctx, connect.NewRequest(&adminv1.CreateUserRequest{Name: "alice", GroupId: group.Msg.Group.Id})); err != nil {
		t.Fatal(err)
	}
	return countryRelayFixture{vless: vless, hysteria2: hysteria2}
}

func addHealthyCountryRelay(t *testing.T, c *countryEnv, id, inboundID, network string, port uint16) {
	t.Helper()
	now := c.clock.Now()
	if err := c.st.CreateRelay(c.ctx, store.Relay{ID: id, InboundID: inboundID, ViaNodeID: "msk1", Network: network, Port: port}, now); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if err := c.st.UpdateRelay(c.ctx, id, store.RelayPatch{Enabled: &enabled}, now); err != nil {
		t.Fatal(err)
	}
	c.exec(`UPDATE relay SET state = 'active' WHERE id = ?`, id)
	check := store.RelayCheck{At: now.Unix(), Network: network, Port: port, To: "203.0.113.10:443",
		Leg: store.RelayStage{Status: "ok"}, Path: store.RelayStage{Status: "ok"}}
	check.Settle()
	if err := c.st.SetRelayCheck(c.ctx, id, check); err != nil {
		t.Fatal(err)
	}
	setCountryRelayMonitor(t, c, id, true, 0)
}

func setCountryRelayMonitor(t *testing.T, c *countryEnv, id string, ok bool, streak uint32) {
	t.Helper()
	if err := c.st.SetRelayMonitorCheck(c.ctx, id, store.RelayMonitorCheck{AtUnix: c.clock.Now().Unix(), OK: ok, FailStreak: streak}); err != nil {
		t.Fatal(err)
	}
}

func openCountryRelayAlert(t *testing.T, c *countryEnv) store.HealthAlert {
	t.Helper()
	base := c.clock.Now().Truncate(time.Second)
	probes := reviewBlockedProbes()
	insertCountryAlertRound(t, c, "de1", verdictBlocked, base.Add(-time.Minute), probes...)
	insertCountryAlertRound(t, c, "de1", verdictBlocked, base, probes...)
	c.evaluate()
	alert, ok := c.active()["country_blocked/de1/"]
	if !ok {
		t.Fatalf("country alert did not open: %+v", c.active())
	}
	return alert
}

func TestCountryBlockedAlertIsInfoWhenEveryServerUsesHealthyRelays(t *testing.T) {
	c := newCountryEnv(t)
	fixture := newCountryRelayFixture(t, c)
	addHealthyCountryRelay(t, c, "rly_vless", fixture.vless, "tcp", 8443)
	addHealthyCountryRelay(t, c, "rly_hysteria2", fixture.hysteria2, "udp", 8443)
	var transitions []Transition
	c.s.cfg.OnTransition = func(tr Transition) { transitions = append(transitions, tr) }

	a := openCountryRelayAlert(t, c)
	if a.Severity != sevInfo || a.WhyKey != "health.alert.country_blocked.why.reached_through_relays" ||
		a.Params["why"] != "reached_through_relays" || a.Params["verdict"] != verdictBlocked {
		t.Fatalf("country alert = %+v", a)
	}
	if len(transitions) != 1 || transitions[0].Resolved || transitions[0].Alert.Severity != sevInfo {
		t.Fatalf("transitions = %+v, want one info open", transitions)
	}
}

func TestCountryBlockedAlertIsInfoWhenEveryServerUsesHealthyReverseRelay(t *testing.T) {
	c := newCountryEnv(t)
	fixture := newCountryRelayFixture(t, c)
	addHealthyCountryRelay(t, c, "rly_vless", fixture.vless, "tcp", 8443)
	addHealthyCountryRelay(t, c, "rly_hysteria2", fixture.hysteria2, "udp", 8444)
	now := c.clock.Now()
	c.exec(`UPDATE node SET agent_caps = 'relay-forward/1 relay-reverse/1' WHERE id = 'msk1'`)
	c.exec(`UPDATE node SET agent_caps = 'relay-forward/1 relay-reverse/1' WHERE id = 'de1'`)
	link, err := c.st.RelayLink(c.ctx, "rly_vless")
	if err != nil {
		t.Fatal(err)
	}
	check, _ := link.Check()
	check.Path = store.RelayStage{Status: "ok", At: now.Unix()}
	if err := c.st.SetRelayCheck(c.ctx, "rly_vless", check); err != nil {
		t.Fatal(err)
	}
	// the exit's agent holds the bridge (reported: the stage is derived from it)
	if err := c.st.SetRelayBridgeSide(c.ctx, "rly_vless", "exit", &store.RelayBridgeSideCheck{Connected: true, Conns: 2,
		SinceUnix: now.Unix() - 60, AtUnix: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := c.st.SetRelayActiveMode(c.ctx, "rly_vless", store.RelayModeReverse, "leg_failed", now); err != nil {
		t.Fatal(err)
	}
	a := openCountryRelayAlert(t, c)
	if a.Severity != sevInfo || a.WhyKey != "health.alert.country_blocked.why.reached_through_relays" {
		t.Fatalf("country alert for a healthy reverse relay = %+v", a)
	}
	if err := c.st.SetRelayBridgeSide(c.ctx, "rly_vless", "exit", &store.RelayBridgeSideCheck{AtUnix: now.Unix()}); err != nil {
		t.Fatal(err)
	}
	c.evaluate()
	if a := c.active()["country_blocked/de1/"]; a.Severity != sevWarning || a.Params["why"] != "" {
		t.Fatalf("country alert did not return to warning after reverse bridge failed: %+v", a)
	}
}

func TestCountryBlockedAlertStaysWarningWhenOneServerHasNoRelay(t *testing.T) {
	c := newCountryEnv(t)
	fixture := newCountryRelayFixture(t, c)
	addHealthyCountryRelay(t, c, "rly_vless", fixture.vless, "tcp", 8443)

	a := openCountryRelayAlert(t, c)
	if a.Severity != sevWarning || a.WhyKey != "health.alert.country_blocked.why.blocked" || a.Params["why"] != "" {
		t.Fatalf("country alert = %+v, want the normal block warning", a)
	}
}

func TestCountryBlockedAlertReturnsToWarningAfterRelayFailsTwice(t *testing.T) {
	c := newCountryEnv(t)
	fixture := newCountryRelayFixture(t, c)
	addHealthyCountryRelay(t, c, "rly_vless", fixture.vless, "tcp", 8443)
	addHealthyCountryRelay(t, c, "rly_hysteria2", fixture.hysteria2, "udp", 8443)
	var transitions []Transition
	c.s.cfg.OnTransition = func(tr Transition) { transitions = append(transitions, tr) }
	opened := openCountryRelayAlert(t, c)
	setCountryRelayMonitor(t, c, "rly_vless", false, 1) // one failed round is not a failed relay
	c.evaluate()
	if a := c.active()["country_blocked/de1/"]; a.Severity != sevInfo {
		t.Fatalf("one failed relay round raised the alert: %+v", a)
	}
	setCountryRelayMonitor(t, c, "rly_vless", false, 2)
	c.evaluate()

	active := c.active()
	a := active["country_blocked/de1/"]
	if len(active) != 1 || a.ID != opened.ID || a.Severity != sevWarning || a.WhyKey != "health.alert.country_blocked.why.blocked" {
		t.Fatalf("country alert after relay failures = %+v; alerts = %+v", a, active)
	}
	// raised in place out of info: told as an open (the one that was quiet), not as a second alert
	if len(transitions) != 2 || transitions[1].Resolved || transitions[1].Alert.ID != opened.ID || transitions[1].Alert.Severity != sevWarning {
		t.Fatalf("transitions = %+v, want the quiet open and the raise", transitions)
	}
	c.evaluate()
	if len(transitions) != 2 {
		t.Fatalf("a settled alert was announced again: %+v", transitions)
	}
}

func TestCountryBlockedAlertStaysWarningWhenNobodyIsGivenTheNode(t *testing.T) {
	c := newCountryEnv(t)
	fixture := newCountryRelayFixture(t, c)
	addHealthyCountryRelay(t, c, "rly_vless", fixture.vless, "tcp", 8443)
	addHealthyCountryRelay(t, c, "rly_hysteria2", fixture.hysteria2, "udp", 8443)
	c.exec(`UPDATE user SET disabled = 1`) // alice is the only person: no server of de1 is given to anyone

	a := openCountryRelayAlert(t, c)
	if a.Severity != sevWarning || a.Params["why"] != "" {
		t.Fatalf("country alert = %+v, want the normal block warning", a)
	}
}

func TestCountryBlockedOpenAlertIsLoweredInPlaceWhenRelaysBecomeHealthy(t *testing.T) {
	c := newCountryEnv(t)
	fixture := newCountryRelayFixture(t, c)
	var transitions []Transition
	c.s.cfg.OnTransition = func(tr Transition) { transitions = append(transitions, tr) }
	opened := openCountryRelayAlert(t, c)
	if opened.Severity != sevWarning {
		t.Fatalf("initial country alert severity = %d, want warning", opened.Severity)
	}
	addHealthyCountryRelay(t, c, "rly_vless", fixture.vless, "tcp", 8443)
	addHealthyCountryRelay(t, c, "rly_hysteria2", fixture.hysteria2, "udp", 8443)
	c.evaluate()

	active := c.active()
	a := active["country_blocked/de1/"]
	if len(active) != 1 || a.ID != opened.ID || a.Severity != sevInfo || a.WhyKey != "health.alert.country_blocked.why.reached_through_relays" {
		t.Fatalf("country alert after relays become healthy = %+v; alerts = %+v", a, active)
	}
	if len(transitions) != 1 || transitions[0].Resolved {
		t.Fatalf("lowering emitted another transition: %+v", transitions)
	}
}
