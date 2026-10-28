package access

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The gate of a relay and what follows the switch (relay_check.go), against a fake of the fleet's CheckRelay: the probes are
// the fleet module's tests (fleet/relay_check_test.go).

// fakeChecker answers CheckRelay as the fleet does: stages that run take the configured answer, the others keep the last
// check of the same target; the result is stored in the relay.
type fakeChecker struct {
	f     *relayFix
	mu    sync.Mutex
	leg   store.RelayStage
	path  store.RelayStage
	err   error
	calls [][]string
	// provenAtPath is what the relay's Proven said when the path was asked for, in the order of the asks.
	provenAtPath []bool
}

func (c *fakeChecker) check(ctx context.Context, id string, stages ...string) (store.RelayCheck, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, stages)
	if c.err != nil {
		return store.RelayCheck{}, c.err
	}
	link, err := c.f.st.RelayLink(ctx, id)
	if err != nil {
		return store.RelayCheck{}, err
	}
	runLeg, runPath := len(stages) == 0 || slices.Contains(stages, store.RelayStageLeg), len(stages) == 0 || slices.Contains(stages, store.RelayStagePath)
	now := c.f.clock.Unix()
	if slices.Contains(stages, store.RelayStagePath) {
		c.provenAtPath = append(c.provenAtPath, link.Proven())
	}
	out := store.RelayCheck{At: now, Network: link.Network, Port: link.Port, To: c.f.s.relayTo(ctx, link)}
	if prev, ok := link.Check(); ok && prev.To == out.To && !runLeg {
		out.Leg = prev.Leg
	}
	if runLeg {
		out.Leg = c.leg
		out.Leg.At = now
	}
	if runPath {
		out.Path = c.path
		out.Path.At = now
	}
	out.Settle()
	return out, c.f.st.SetRelayCheck(ctx, id, out)
}

var okLeg = store.RelayStage{Status: "ok", Sender: "nod_fi1", Verdict: "ok", Sent: 300, Got: 300}

func newCheckedFix(t *testing.T) (*relayFix, *fakeChecker, *adminv1.Relay) {
	t.Helper()
	f := newRelayFix(t)
	c := &fakeChecker{f: f, leg: okLeg, path: store.RelayStage{Status: "ok", Sender: "nod_nl1", Verdict: "ok", Sent: 300, Got: 300}}
	f.s.cfg.CheckRelay = c.check
	f.s.SetRelayWaitForTest(3*time.Second, 5*time.Millisecond)
	r := must(f.create(f.server.Id, "nod_fi1", 0))
	return f, c, r
}

// reportsForward plays the entry's agent: once the relay is switched on, it reports the forward (ok, or the error).
func reportsForward(f *relayFix, id string, ok bool, errText string) {
	go func() {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
			if l, err := f.st.RelayLink(f.ctx, id); err == nil && l.Enabled {
				_ = f.st.RelaysApplied(f.ctx, "nod_fi1", []store.RelayApplied{{ID: id, OK: ok, Error: errText, ToIP: "192.0.2.10"}}, f.clock)
				return
			}
		}
	}()
}

func (f *relayFix) relayOf(id string) store.RelayLink {
	f.t.Helper()
	return must(f.st.RelayLink(f.ctx, id))
}

func TestRelayGateNeedsAFreshPassingLegForThisTarget(t *testing.T) {
	f, c, r := newCheckedFix(t)
	gate := func() error { return f.s.relayCheckGate(f.ctx, f.relayOf(r.Id)) }
	wantMsg(t, gate(), "failed_precondition: relay_unchecked")

	must(c.check(f.ctx, r.Id))
	if err := gate(); err != nil {
		t.Fatalf("a leg that passed just now: %v", err)
	}
	// The path stage of a relay that is off was skipped; the leg alone opens the gate.
	if got := f.relayOf(r.Id).Relay; got.CheckJSON == "{}" {
		t.Fatal("the check was not stored")
	}

	f.clock = f.clock.Add(relayCheckTTL - time.Second)
	if err := gate(); err != nil {
		t.Errorf("just inside the limit: %v", err)
	}
	f.clock = f.clock.Add(2 * time.Second)
	wantMsg(t, gate(), "failed_precondition: relay_check_stale")

	// A check made for another target (the exit's server moved to another port) does not count.
	f.clock = ts("2026-09-30T12:00:00Z")
	must(c.check(f.ctx, r.Id))
	f.sql(`UPDATE node SET address = 'de1-new.example.com' WHERE id = 'nod_de1'`)
	wantMsg(t, gate(), "failed_precondition: relay_check_stale")
	f.sql(`UPDATE node SET address = 'de1.example.com' WHERE id = 'nod_de1'`)
	if err := gate(); err != nil {
		t.Errorf("back at the target that was checked: %v", err)
	}

	// A leg that failed names the stage and the reason.
	c.leg = store.RelayStage{Status: "fail", Code: "refused", Sender: "nod_fi1", Verdict: "refused"}
	must(c.check(f.ctx, r.Id))
	wantMsg(t, gate(), "failed_precondition: relay_check_failed: code=refused&stage=leg")

	// Without a checker nothing can have passed.
	c.leg = okLeg
	must(c.check(f.ctx, r.Id))
	f.s.cfg.CheckRelay = nil
	wantMsg(t, gate(), "failed_precondition: relay_unchecked")
}

func TestCheckRelayRPCStoresAndAuditsAndSwitchesNothing(t *testing.T) {
	f, c, r := newCheckedFix(t)
	c.path = store.RelayStage{Status: "skipped", Code: "off"}
	f.resetNotify()
	got := must(f.s.CheckRelay(f.ctx, req(&adminv1.CheckRelayRequest{Id: r.Id}))).Msg.Relay
	chk := got.Check
	if chk == nil || !chk.Ok || chk.Verified || chk.Stage != "" || chk.Leg.Status != "ok" || chk.Leg.SenderName != "fi1" || chk.Leg.Got != 300 ||
		chk.Path.Status != "skipped" || chk.Path.Code != "off" || chk.Network != "udp" || chk.To != "de1.example.com:443" || chk.AtUnix != f.clock.Unix() {
		t.Fatalf("check = %+v", chk)
	}
	if got.Enabled || f.notify.n.Load() != 0 {
		t.Error("a check switched something or told the nodes")
	}
	if len(c.calls) != 1 || len(c.calls[0]) != 0 {
		t.Errorf("both stages are asked for: %v", c.calls)
	}
	// The latest check comes back with the list.
	if l := must(f.s.ListRelays(f.ctx, req(&adminv1.ListRelaysRequest{}))).Msg.Relays[0]; l.Check == nil || l.Check.Leg.Status != "ok" {
		t.Errorf("list: %+v", l.Check)
	}
	rows := f.audited("relay.check")
	if len(rows) != 1 || !strings.Contains(rows[0].Params, `"leg":"ok"`) || !strings.Contains(rows[0].Params, `"path_code":"off"`) || strings.Contains(rows[0].Params, "192.0.2") {
		t.Fatalf("audit: %+v", rows)
	}

	// A failed leg is a result, not an error, and it still switches nothing.
	c.leg = store.RelayStage{Status: "fail", Code: "timeout", Sender: "nod_fi1", Verdict: "timeout"}
	chk = must(f.s.CheckRelay(f.ctx, req(&adminv1.CheckRelayRequest{Id: r.Id}))).Msg.Relay.Check
	if chk.Ok || chk.Stage != "leg" || chk.Leg.Code != "timeout" {
		t.Fatalf("failed check = %+v", chk)
	}
}

func TestCheckRelayRPCRefusals(t *testing.T) {
	f, _, r := newCheckedFix(t)
	_, err := f.s.CheckRelay(f.ctx, req(&adminv1.CheckRelayRequest{Id: "rly_nope"}))
	wantMsg(t, err, "not_found: relay not found")
	f.s.cfg.CheckRelay = nil
	_, err = f.s.CheckRelay(f.ctx, req(&adminv1.CheckRelayRequest{Id: r.Id}))
	wantMsg(t, err, "failed_precondition: check_unavailable")
}

// Switching on: the gate, then the entry's report, then the path from outside; a relay that is on has passed all three.
func TestSwitchingOnChecksThePathAndKeepsOnlyAWorkingRelay(t *testing.T) {
	f, c, r := newCheckedFix(t)
	on := true
	must(c.check(f.ctx, r.Id, store.RelayStageLeg))
	c.calls = nil
	f.resetNotify()

	reportsForward(f, r.Id, true, "")
	got := must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	if !got.Enabled || got.State != adminv1.InboundState_INBOUND_STATE_ACTIVE || got.Check == nil || !got.Check.Verified || got.Check.Path.Status != "ok" {
		t.Fatalf("switched on: %+v / %+v", got, got.Check)
	}
	if len(c.calls) != 1 || !slices.Equal(c.calls[0], []string{store.RelayStagePath}) {
		t.Errorf("only the path is checked after the switch (the leg was just proved): %v", c.calls)
	}
	if rows := f.audited("relay.update"); len(rows) != 1 || !strings.Contains(rows[0].Params, `"enabled":true`) {
		t.Fatalf("audit: %+v", rows)
	}
	if !f.relayOf(r.Id).Proven() || !slices.Equal(c.provenAtPath, []bool{false}) {
		t.Errorf("proven after = %v, while the path was being asked = %v", f.relayOf(r.Id).Proven(), c.provenAtPath)
	}

	// Off and on again: what the old forward proved does not carry over to the new one.
	off := false
	must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &off}))
	must(c.check(f.ctx, r.Id, store.RelayStageLeg))
	reportsForward(f, r.Id, true, "")
	must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	if !slices.Equal(c.provenAtPath, []bool{false, false}) || !f.relayOf(r.Id).Proven() {
		t.Errorf("second switch: proven while asked = %v, after = %v", c.provenAtPath, f.relayOf(r.Id).Proven())
	}
}

func TestAPathThatFailsSwitchesTheRelayOffAgain(t *testing.T) {
	f, c, r := newCheckedFix(t)
	on := true
	must(c.check(f.ctx, r.Id, store.RelayStageLeg))
	c.path = store.RelayStage{Status: "fail", Code: "broken", Sender: "nod_nl1", Verdict: "broken", Sent: 300, Got: 0}
	f.resetNotify()

	reportsForward(f, r.Id, true, "")
	wantMsg(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on})), "failed_precondition: relay_check_failed: code=broken&stage=path")
	l := f.relayOf(r.Id)
	if l.Enabled || l.Proven() {
		t.Fatal("the path failed, but the relay stays on")
	}
	chk, _ := l.Check()
	if chk.OK || chk.Stage != "path" || chk.Path.Code != "broken" || chk.Verified() {
		t.Errorf("stored check = %+v", chk)
	}
	if f.notify.n.Load() < 2 {
		t.Errorf("the nodes were told %d times, want the forward in and out again", f.notify.n.Load())
	}
	rows := f.audited("relay.update")
	if len(rows) != 2 || !strings.Contains(rows[0].Params, `"reason":"check_failed"`) || !strings.Contains(rows[0].Params, `"stage":"path"`) || !strings.Contains(rows[0].Params, `"enabled":false`) {
		t.Fatalf("audit: %+v", rows)
	}

	// The leg is still good, so the owner may try again once the cause is gone.
	c.path = store.RelayStage{Status: "ok", Sender: "nod_nl1", Verdict: "ok", Sent: 300, Got: 300}
	f.clock = f.clock.Add(time.Minute)
	reportsForward(f, r.Id, true, "")
	if got := must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on})); !got.Enabled || !got.Check.Verified {
		t.Fatalf("second try: %+v", got)
	}
}

func TestAnEntryThatDoesNotInstallTheForwardLeavesTheRelayOff(t *testing.T) {
	for _, tc := range []struct {
		name   string
		report func(*relayFix, string)
		code   string
	}{
		{"never reports", func(*relayFix, string) {}, "apply_timeout"},
		{"reports a failure", func(f *relayFix, id string) { reportsForward(f, id, false, "forward_drop") }, "apply_failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, c, r := newCheckedFix(t)
			f.s.SetRelayWaitForTest(150*time.Millisecond, 5*time.Millisecond)
			on := true
			must(c.check(f.ctx, r.Id, store.RelayStageLeg))
			c.calls = nil
			tc.report(f, r.Id)
			wantMsg(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on})), "failed_precondition: relay_check_failed: code="+tc.code+"&stage=path")
			l := f.relayOf(r.Id)
			chk, _ := l.Check()
			if l.Enabled || len(c.calls) != 0 || chk.Path.Status != "fail" || chk.Path.Code != tc.code || chk.Leg.Status != "ok" || chk.Verified() {
				t.Errorf("enabled=%v calls=%v check=%+v", l.Enabled, c.calls, chk)
			}
		})
	}
}

func TestAFleetThatCannotCheckThePathFailsClosed(t *testing.T) {
	f, c, r := newCheckedFix(t)
	on := true
	must(c.check(f.ctx, r.Id, store.RelayStageLeg))
	c.err = context.DeadlineExceeded
	reportsForward(f, r.Id, true, "")
	wantMsg(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on})), "failed_precondition: relay_check_failed: code=failed&stage=path")
	if f.relayOf(r.Id).Enabled {
		t.Fatal("no verdict is no pass")
	}
}

// Moving a relay that is on to another port is a new forward: the path is checked again, and a failure switches the relay off.
func TestMovingAnEnabledRelayChecksThePathAgain(t *testing.T) {
	f, c, r := newCheckedFix(t)
	on := true
	must(c.check(f.ctx, r.Id, store.RelayStageLeg))
	reportsForward(f, r.Id, true, "")
	must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	c.calls = nil

	port := uint32(9443)
	reportsForwardAgain := func() {
		go func() {
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
				if l, err := f.st.RelayLink(f.ctx, r.Id); err == nil && l.Port == 9443 {
					_ = f.st.RelaysApplied(f.ctx, "nod_fi1", []store.RelayApplied{{ID: r.Id, OK: true}}, f.clock)
					return
				}
			}
		}()
	}
	reportsForwardAgain()
	got := must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Port: &port}))
	if !got.Enabled || got.Port != 9443 || !got.Check.Verified || got.Check.Port != 9443 || len(c.calls) != 1 || !slices.Equal(c.calls[0], []string{store.RelayStagePath}) {
		t.Fatalf("moved: %+v %+v calls=%v", got, got.Check, c.calls)
	}

	// A port that is moved on a relay that is off needs no check: it is not on.
	off := false
	must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &off}))
	c.calls = nil
	other := uint32(9444)
	must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Port: &other}))
	if len(c.calls) != 0 {
		t.Errorf("a relay that is off was checked after a port change: %v", c.calls)
	}
}

// Enabling together with a port change is gated like the new relay: the check stays good (the leg does not depend on the port).
func TestSwitchingOnWithoutAFreshCheckIsRefusedWithTheRealGate(t *testing.T) {
	f, _, r := newCheckedFix(t)
	on := true
	wantMsg(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on})), "failed_precondition: relay_unchecked")
	if f.relayOf(r.Id).Enabled || len(f.audited("relay.update")) != 0 {
		t.Error("a refused switch left a trace")
	}
}

func TestCreateRelayRefusesAServerARelayCannotPassOn(t *testing.T) {
	f := newRelayFix(t)
	awg := f.awgProfile("awg-files", "")
	srv := f.inbound(awg.Id, "nod_de1")
	wantMsg(t, errOf2(f.create(srv.Id, "nod_fi1", 0)), "failed_precondition: relay_unsupported: reason=awg")
}

func TestRelayUnsupportedReasons(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, settings, want string
		tls                            plugin.TLSMode
	}{
		{"plain hysteria2", "hysteria2", `{}`, "", plugin.TLSSelfSigned},
		{"vless reality", "vless", `{"reality":{"target":"www.example.org:443"}}`, "", 0},
		{"vless behind a CDN", "vless", `{"cdn":{"host":"cdn.example.org","port":443}}`, "cdn", plugin.TLSAcmeDomain},
		{"vless with an empty CDN host", "vless", `{"cdn":{"host":""}}`, "", plugin.TLSAcmeDomain},
		{"certificate on an IP", "hysteria2", `{}`, "acme_ip", plugin.TLSAcmeIP},
		{"awg", "awg", `{}`, "awg", 0},
	} {
		spec := plugin.InboundSpec{Settings: []byte(tc.settings), TLS: plugin.TLS{Mode: tc.tls}}
		if got := relayUnsupported(tc.protocol, spec); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The candidates of an entry: other nodes' servers, those that cannot be passed on named, the hints only from what the sources
// said, "no data" for the rest, and the exits the country check says are cut off first.
func TestListRelayCandidates(t *testing.T) {
	f := newRelayFix(t)
	f.sql(`UPDATE node SET state = 'pending' WHERE id = 'nod_nl1'`)
	f.node("nod_fr1", "fr1", "fr1.example.com", "active")
	f.node("nod_old", "old1", "old1.example.com", "retired")
	hy := f.inbound(f.base.Id, "nod_fr1")
	awg := f.inbound(f.awgProfile("awg-files", "").Id, "nod_fr1")
	vless := f.inbound(f.vlessProfile("reality").Id, "nod_de1")
	cdnProfile := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "via-cdn",
		SettingsJson: `{"security":"tls","transport":"xhttp","tls_mode":"acme_domain","sni":"origin.example.com","cdn":{"host":"cdn.example.org","port":443}}`}))).Msg.Profile
	cdn := must(f.s.CreateInbound(f.ctx, req(&adminv1.CreateInboundRequest{ProfileId: cdnProfile.Id, NodeId: "nod_de1", PortOverride: 8443}))).Msg.Inbound
	f.inbound(f.base.Id, "nod_fi1") // the entry's own server is no candidate
	r := must(f.create(f.server.Id, "nod_fi1", 0))

	list := func() *adminv1.ListRelayCandidatesResponse {
		return must(f.s.ListRelayCandidates(f.ctx, req(&adminv1.ListRelayCandidatesRequest{ViaNodeId: "nod_fi1"}))).Msg
	}
	byID := func(resp *adminv1.ListRelayCandidatesResponse) map[string]*adminv1.RelayCandidate {
		out := map[string]*adminv1.RelayCandidate{}
		for _, c := range resp.Candidates {
			out[c.InboundId] = c
		}
		return out
	}

	// No hints at all: no guess.
	resp := list()
	if len(resp.Candidates) != 5 || !resp.AgentSupported || resp.EntryCountry.Verdict != "no_data" {
		t.Fatalf("candidates = %d agent=%v entry=%+v", len(resp.Candidates), resp.AgentSupported, resp.EntryCountry)
	}
	got := byID(resp)
	for id, c := range got {
		if c.Country.Verdict != "no_data" || c.Reach.Verdict != "no_data" || len(c.Country.Dark) != 0 || c.Reach.Source != "" {
			t.Errorf("%s: hints without data: %+v %+v", id, c.Country, c.Reach)
		}
	}
	if got[f.server.Id].RelayId != r.Id || got[f.server.Id].Network != "udp" || got[f.server.Id].Port != 443 || got[f.server.Id].NodeName != "de1" || got[f.server.Id].Unsupported != "" {
		t.Errorf("the server that has a relay: %+v", got[f.server.Id])
	}
	for id, want := range map[string]string{awg.Id: "awg", cdn.Id: "cdn", vless.Id: "", hy.Id: ""} {
		if got[id] == nil || got[id].Unsupported != want {
			t.Errorf("%s: %+v, want unsupported %q", id, got[id], want)
		}
	}
	if got[hy.Id].RelayId != "" {
		t.Error("a server without a relay names one")
	}
	// Those that can be passed on come first; the unsupported ones are last, whatever the hints say.
	for i, c := range resp.Candidates {
		if c.Unsupported != "" && i < 3 {
			t.Errorf("unsupported %s at position %d", c.InboundId, i)
		}
	}

	// With the sources' answers: fr1 is dark for two networks and the entry reaches it; de1 is fine; the entry is seen.
	f.s.SetRelayHints(func(_ context.Context, entry string, ids []string) (map[string]*adminv1.RelayCountryHint, map[string]*adminv1.RelayReachHint) {
		if entry != "nod_fi1" || !slices.Contains(ids, "nod_fr1") || !slices.Contains(ids, "nod_de1") || slices.Contains(ids, "nod_fi1") || slices.Contains(ids, "nod_nl1") {
			t.Errorf("hints asked for %q %v", entry, ids)
		}
		return map[string]*adminv1.RelayCountryHint{
				"nod_fr1": {Verdict: "partial", Dark: []string{"Mobile A", "Mobile B"}, AtUnix: 100},
				"nod_de1": {Verdict: "ok", AtUnix: 90},
				"nod_fi1": {Verdict: "ok", AtUnix: 95},
			}, map[string]*adminv1.RelayReachHint{
				"nod_fr1": {Verdict: "reach", Source: "refs_pass", AtUnix: 80},
			}
	})
	resp = list()
	got = byID(resp)
	if c := got[hy.Id]; c.Country.Verdict != "partial" || !slices.Equal(c.Country.Dark, []string{"Mobile A", "Mobile B"}) || c.Reach.Verdict != "reach" || c.Reach.Source != "refs_pass" {
		t.Errorf("fr1: %+v %+v", c.Country, c.Reach)
	}
	if c := got[f.server.Id]; c.Country.Verdict != "ok" || c.Reach.Verdict != "no_data" {
		t.Errorf("de1: %+v %+v", c.Country, c.Reach)
	}
	if resp.EntryCountry.Verdict != "ok" || resp.EntryCountry.AtUnix != 95 {
		t.Errorf("entry: %+v", resp.EntryCountry)
	}
	if first := resp.Candidates[0]; first.InboundId != hy.Id {
		t.Errorf("the exit that is dark for some networks goes first: %+v", first)
	}

	_, err := f.s.ListRelayCandidates(f.ctx, req(&adminv1.ListRelayCandidatesRequest{ViaNodeId: "nod_nope"}))
	wantCode(t, err, connect.CodeNotFound)
}
