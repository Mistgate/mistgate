package access

import (
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The leg family of a relay (relay.proto Relay.leg): an auto relay whose IPv4 leg is dead moves to the exit's IPv6 address on
// the next check, keeps the move only when the IPv6 leg passes, and does not probe again for half an hour.

var badLeg = store.RelayStage{Status: "fail", Code: "timeout", Sender: "nod_fi1"}

func withIPv6Exit(f *relayFix) {
	f.t.Helper()
	f.sql(`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_de1'`)
	f.sql(`UPDATE node SET agent_caps = 'relay-forward/1 relay-forward/2' WHERE id = 'nod_fi1'`)
}

func TestAutoLegMovesToIPv6WhenTheIPv4LegDies(t *testing.T) {
	f, c, r := newCheckedFix(t)
	withIPv6Exit(f)
	c.leg, c.legByActive = badLeg, map[string]store.RelayStage{store.RelayFamily6: okLeg}
	must(f.s.CheckRelay(f.ctx, req(&adminv1.CheckRelayRequest{Id: r.Id})))
	if l := f.relayOf(r.Id); l.ActiveFamily() != store.RelayFamily6 || l.LegTriedAt.IsZero() {
		t.Fatalf("after the check: active %q, tried %v; want the IPv6 leg proved", l.ActiveFamily(), l.LegTriedAt)
	}
	reportsForward(f, r.Id, true, "")
	on := true
	got := must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	if !got.Enabled || got.ActiveLeg != store.RelayFamily6 || got.Leg != store.RelayLegAuto {
		t.Fatalf("after the move: enabled %v, active %q, leg %q", got.Enabled, got.ActiveLeg, got.Leg)
	}
	l := f.relayOf(r.Id)
	if l.LegTriedAt.IsZero() {
		t.Fatalf("the move left no trace: %+v", l)
	}
	if chk, ok := l.Check(); !ok || chk.Leg.Status != "ok" || chk.To != "[2001:db8::5]:443" {
		t.Fatalf("the check of the moved leg: %+v, %v", chk, ok)
	}
}

func TestAutoLegStaysWhenNeitherFamilyWorks(t *testing.T) {
	f, c, r := newCheckedFix(t)
	withIPv6Exit(f)
	c.leg = badLeg // both families time out
	must(f.s.CheckRelay(f.ctx, req(&adminv1.CheckRelayRequest{Id: r.Id})))
	on := true
	wantMsg(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on})), "failed_precondition: relay_check_failed: code=timeout&stage=leg")
	if l := f.relayOf(r.Id); l.ActiveFamily() != store.RelayFamily4 || l.LegTriedAt.IsZero() {
		t.Fatalf("after a failed move: active %q, tried %v", l.ActiveFamily(), l.LegTriedAt)
	}
	c.mu.Lock()
	probes := len(c.calls)
	c.mu.Unlock()
	// The half hour is not over: the gate says the same thing without asking the other family again.
	errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	c.mu.Lock()
	again := len(c.calls)
	c.mu.Unlock()
	if again != probes {
		t.Fatalf("the same move was probed twice: %d then %d calls", probes, again)
	}
}

// A family the owner pins never moves by itself, even when its leg is dead.
func TestPinnedLegDoesNotMove(t *testing.T) {
	f, c, r := newCheckedFix(t)
	withIPv6Exit(f)
	c.leg, c.legByActive = badLeg, map[string]store.RelayStage{store.RelayFamily6: okLeg}
	leg := store.RelayLegIPv4
	must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Leg: &leg}))
	must(f.s.CheckRelay(f.ctx, req(&adminv1.CheckRelayRequest{Id: r.Id})))
	on := true
	errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	if l := f.relayOf(r.Id); l.Leg != store.RelayLegIPv4 || l.ActiveFamily() != store.RelayFamily4 {
		t.Fatalf("a pinned IPv4 leg moved: leg %q, active %q", l.Leg, l.ActiveFamily())
	}
}

// An entry whose agent cannot carry an IPv6 leg is refused the pin, and auto never tries it.
func TestIPv6LegNeedsTheAgentsCapability(t *testing.T) {
	f, c, r := newCheckedFix(t)
	leg := store.RelayLegIPv6
	wantMsg(t, errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Leg: &leg})), "failed_precondition: agent_old: node=fi1")
	c.leg = badLeg
	must(f.s.CheckRelay(f.ctx, req(&adminv1.CheckRelayRequest{Id: r.Id})))
	on := true
	errOf2(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Enabled: &on}))
	if l := f.relayOf(r.Id); l.ActiveFamily() != store.RelayFamily4 {
		t.Fatalf("an agent without relay-forward/2 carried an IPv6 leg: %q", l.ActiveFamily())
	}
}

// The bridge's hop has a family too: an auto reverse relay whose v4 reach failed moves to the entry's IPv6 address when the
// exit's probe of that address passes.
func TestReverseAutoLegMovesToIPv6WhenTheExitReachesTheEntryThere(t *testing.T) {
	f, c, r := newCheckedFix(t)
	f.sql(`UPDATE node SET ipv6_entry = '2001:db8::5' WHERE id = 'nod_fi1'`) // the entry's address for the bridge
	mode := store.RelayModeReverse
	must(f.update(&adminv1.UpdateRelayRequest{Id: r.Id, Mode: &mode}))
	c.reverse, c.reverseByFamily = badLeg, map[string]store.RelayStage{store.RelayFamily6: okLeg}
	must(f.s.CheckRelay(f.ctx, req(&adminv1.CheckRelayRequest{Id: r.Id, ReverseReach: true})))
	l := f.relayOf(r.Id)
	if l.ActiveFamily() != store.RelayFamily6 || l.LegTriedAt.IsZero() {
		t.Fatalf("after the reverse check: active %q, tried %v; want the IPv6 bridge leg proved", l.ActiveFamily(), l.LegTriedAt)
	}
	chk, ok := l.Check()
	if !ok || chk.ReverseReach.Status != "ok" || chk.ReverseReach.Family != store.RelayFamily6 {
		t.Fatalf("the check of the moved bridge leg: %+v, %v", chk.ReverseReach, ok)
	}
}
