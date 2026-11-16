package access

import (
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Why the reverse way cannot carry a relay, for every reason, and the order when several hold.
func TestReverseBlockedReasons(t *testing.T) {
	both := []string{plugin.CapRelayForward, plugin.CapRelayReverse}
	old := []string{plugin.CapRelayForward}
	for name, tc := range map[string]struct {
		network         string
		entry, exit     []string
		want            string
		wantDescription string
	}{
		"tcp, both new":              {"tcp", both, both, "", "possible"},
		"tcp, old entry":             {"tcp", old, both, "entry_agent_old", "entry"},
		"tcp, old exit":              {"tcp", both, old, "exit_agent_old", "exit"},
		"tcp, both old":              {"tcp", old, nil, "entry_agent_old", "the entry is named first"},
		"tcp, exit never connected":  {"tcp", both, nil, "exit_agent_old", "no capabilities on record"},
		"udp, both new":              {"udp", both, both, "udp", "udp"},
		"udp wins over old agents":   {"udp", old, old, "udp", "an update does not help udp"},
		"tcp, the reverse-only exit": {"tcp", both, []string{plugin.CapRelayReverse}, "", "relay-forward is not needed at the exit"},
	} {
		l := store.RelayLink{Relay: store.Relay{Network: tc.network}, ViaNodeCaps: tc.entry, ExitNodeCaps: tc.exit}
		if got := reverseBlocked(l); got != tc.want {
			t.Errorf("%s: reverse_blocked = %q, want %q (%s)", name, got, tc.want, tc.wantDescription)
		}
	}
}

// The admin message carries the way as the store has it: an old relay is auto on dnat, and what the controller and the agents will
// write comes through as it is.
func TestRelayMessageCarriesTheWay(t *testing.T) {
	f := newRelayFix(t)
	r := must(f.create(f.server.Id, "nod_fi1", 0))
	if r.Mode != "auto" || r.ActiveMode != "dnat" || r.ModeReason != "" || r.ModeChangedUnix != 0 || r.DirectStreak != 0 || r.Bridge != nil ||
		r.ReverseBlocked != "udp" {
		t.Fatalf("a new relay: mode %q active %q reason %q changed %d streak %d bridge %v blocked %q",
			r.Mode, r.ActiveMode, r.ModeReason, r.ModeChangedUnix, r.DirectStreak, r.Bridge, r.ReverseBlocked)
	}

	changed := time.Unix(1_700_000_000, 0)
	if err := f.st.SetRelayActiveMode(f.ctx, r.Id, "reverse", "leg_failed", changed); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetRelayDirectStreak(f.ctx, r.Id, 2); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetRelayBridge(f.ctx, r.Id, &store.RelayBridgeCheck{Connected: true, Conns: 2, RTTMS: 38, SinceUnix: 1_700_000_100, Error: ""}); err != nil {
		t.Fatal(err)
	}
	f.sql(`UPDATE relay SET mode = 'reverse' WHERE id = ?`, r.Id)
	got := must(f.s.ListRelays(f.ctx, req(&adminv1.ListRelaysRequest{}))).Msg.Relays[0]
	if got.Mode != "reverse" || got.ActiveMode != "reverse" || got.ModeReason != "leg_failed" || got.ModeChangedUnix != changed.Unix() || got.DirectStreak != 2 ||
		got.Bridge == nil || !got.Bridge.Connected || got.Bridge.Conns != 2 || got.Bridge.RttMs != 38 || got.Bridge.SinceUnix != 1_700_000_100 || got.Bridge.Error != "" {
		t.Fatalf("a relay in the reverse way: %+v", got)
	}
	// The members of check_json the other writers own are still read.
	if got.Check != nil || got.Monitor != nil {
		t.Fatalf("the bridge report made a check out of nothing: %+v %+v", got.Check, got.Monitor)
	}
}
