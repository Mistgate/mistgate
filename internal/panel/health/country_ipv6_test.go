package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The IPv6 side of the country check (step 3 of the IPv6 plan): which address a node is measured on, the budget of two
// families, the alternation of the rounds and what an IPv6 round stores. IPv4 is left exactly as it was.

func doctorIPv6(params ...string) *store.DoctorRow {
	row := &store.DoctorRow{NodeID: "n", CheckID: "ipv6", Params: map[string]string{}}
	for i := 0; i+1 < len(params); i += 2 {
		row.Params[params[i]] = params[i+1]
	}
	return row
}

func TestNodeIPv6AddressAndState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		address   string
		row       *store.DoctorRow
		wantAddr  string
		wantState string
	}{
		{"the uplink's src", "de1.example.com", doctorIPv6("ipv6", "yes", "connect", "ok", "src", "2001:db8::10", "addrs", "2001:db8::10/64"), "2001:db8::10", ipv6OK},
		{"no src: the first stable address of the uplink", "de1.example.com", doctorIPv6("ipv6", "yes", "connect", "ok", "addrs", "2001:db8:1::5/64,2001:db8:1::6/64"), "2001:db8:1::5", ipv6OK},
		{"a unique-local src is no address to test", "de1.example.com", doctorIPv6("ipv6", "yes", "connect", "ok", "src", "fd00::5", "addrs", "fe80::1/64"), "", ipv6AgentOld},
		{"an agent that predates the IPv6 facts names none", "de1.example.com", doctorIPv6("ipv6", "yes", "connect", "ok"), "", ipv6AgentOld},
		{"an IPv6 literal Address is the address", "2001:db8::77", doctorIPv6("ipv6", "yes", "connect", "ok", "src", "2001:db8::10"), "2001:db8::77", ipv6OK},
		{"an IPv4 literal Address leaves it to the doctor", "203.0.113.10", doctorIPv6("ipv6", "yes", "connect", "ok", "src", "2001:db8::10"), "2001:db8::10", ipv6OK},
		{"an old agent with an IPv6 literal Address", "2001:db8::77", doctorIPv6("ipv6", "yes", "connect", "ok"), "2001:db8::77", ipv6OK},
		{"no global IPv6", "de1.example.com", doctorIPv6("ipv6", "no"), "", ipv6None},
		{"an address at the hoster that does not work", "de1.example.com", doctorIPv6("ipv6", "yes", "connect", "failed", "suggest", "client_ipv6_off", "src", "2001:db8::10"), "", ipv6Broken},
		{"broken, with IPv6 for clients off", "de1.example.com", doctorIPv6("ipv6", "yes", "connect", "failed", "client_ipv6", "off"), "", ipv6Broken},
		{"an IPv6 literal Address is not tested when IPv6 is broken", "2001:db8::77", doctorIPv6("ipv6", "yes", "connect", "failed"), "", ipv6Broken},
		{"the doctor has not reported", "de1.example.com", nil, "", ipv6Unknown},
		{"a report without the facts", "de1.example.com", doctorIPv6(), "", ipv6Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := countryNode{node: store.NodeRow{ID: "de1", Address: tc.address}, ipv6: ipv6FactsOf(tc.row)}
			addr, state := nodeIPv6(n)
			if addr != tc.wantAddr || state != tc.wantState {
				t.Fatalf("nodeIPv6 = %q, %q; want %q, %q", addr, state, tc.wantAddr, tc.wantState)
			}
		})
	}
	// a node that was never looked at (the IPv6 checks are off) is unknown
	if addr, state := nodeIPv6(countryNode{}); addr != "" || state != ipv6Unknown {
		t.Fatalf("nodeIPv6 of a fresh node = %q, %q", addr, state)
	}
}

func TestCountryPlanBudgetWithIPv6(t *testing.T) {
	// six nodes with two TCP ports each; the IPv6 round has the control and the nodes whose IPv6 works
	mk := func(withIPv6 int) []countryNode {
		out := make([]countryNode, 6)
		for i := range out {
			out[i] = countryNode{ports: make([]uint16, 2)}
			if i < withIPv6 {
				out[i].ipv6.state = ipv6OK
			}
		}
		return out
	}
	// IPv4 alone is 1 + 6*3 = 19 tests a probe. Without a token (200 of 250 an hour) three probes would be 228: two, 152.
	if k, every, perHour := countryPlan(mk(0), 15*time.Minute, 250); k != 2 || every != 15*time.Minute || perHour != 152 {
		t.Errorf("IPv4 only, no token: probes %d every %v, %d an hour; want 2, 15m, 152", k, every, perHour)
	}
	// Two nodes with IPv6 add 1 + 2*3 = 7: 26 a probe, 52 with two probes, 208 an hour at 15 minutes - over, so the interval
	// stretches to 16 minutes (195 an hour).
	k, every, perHour := countryPlan(mk(2), 15*time.Minute, 250)
	if k != 2 || every != 16*time.Minute || perHour != 195 {
		t.Errorf("two IPv6 nodes, no token: probes %d every %v, %d an hour; want 2, 16m, 195", k, every, perHour)
	}
	// With a token (400 of 500) three probes fit: 78 a pair of rounds, 312 an hour, at the chosen interval.
	if k, every, perHour := countryPlan(mk(2), 15*time.Minute, 500); k != 3 || every != 15*time.Minute || perHour != 312 {
		t.Errorf("two IPv6 nodes, token: probes %d every %v, %d an hour; want 3, 15m, 312", k, every, perHour)
	}
	if k, every, perHour := countryPlan(mk(0), 15*time.Minute, 500); k != 3 || every != 15*time.Minute || perHour != 228 {
		t.Errorf("IPv4 only, token: probes %d every %v, %d an hour; want 3, 15m, 228", k, every, perHour)
	}
	// nodes whose IPv6 is broken or absent cost nothing, and a fleet with no IPv6 node has no IPv6 round
	nodes := mk(0)
	nodes[0].ipv6.state = ipv6Broken
	nodes[1].ipv6.state = ipv6None
	if got, want := countryCost(nodes, 3), countryCost(mk(0), 3); got != want {
		t.Errorf("cost with broken IPv6 nodes = %d, want the IPv4 cost %d", got, want)
	}
	if got, want := countryCost(mk(1), 2), (19+1+3)*2; got != want {
		t.Errorf("cost with one IPv6 node = %d, want %d", got, want)
	}
}

// ipv6Env is a country environment with the IPv6 checks on: de1 has working IPv6, nl1 has an address that does not work at the
// hoster, fi1 has none, and plain has not reported yet.
func newIPv6Env(t *testing.T) *countryEnv {
	t.Helper()
	c := newCountryEnv(t)
	c.cfg.IPv6 = true
	for _, id := range []string{"de1", "nl1", "fi1", "plain"} {
		c.onlineNode(id, "203.0.113.10")
	}
	c.report("de1", true, res("ipv6", dOK, "", "ipv6", "yes", "connect", "ok", "iface", "eth0", "addrs", "2001:db8::10/64", "src", "2001:db8::10"))
	c.report("nl1", true, res("ipv6", dWarn, "", "ipv6", "yes", "connect", "failed", "suggest", "client_ipv6_off", "src", "2001:db8::20"))
	c.report("fi1", true, res("ipv6", dOK, "", "ipv6", "no"))
	return c
}

// tickOnce is one scheduler poll without moving the clock.
func (c *countryEnv) tickOnce() bool {
	c.t.Helper()
	c.s.invalidateSnapshot()
	return c.s.CountryTick(c.ctx)
}

func (c *countryEnv) rows(family int, node string) []store.CountryCheck {
	c.t.Helper()
	rows, err := c.st.RecentCountryChecks(c.ctx, family, 20, node)
	if err != nil {
		c.t.Fatal(err)
	}
	return rows
}

func TestCountryTickAlternatesIPv4AndIPv6Rounds(t *testing.T) {
	c := newIPv6Env(t)
	if !c.tickOnce() {
		t.Fatal("no first round")
	}
	rounds := c.src.roundsSnapshot()
	if len(rounds) != 1 || rounds[0].Family != familyV4 || rounds[0].ControlHost != "ya.ru" {
		t.Fatalf("the IPv4 round goes first: %+v", rounds)
	}
	if got := c.rows(familyV6, ""); len(got) != 0 {
		t.Fatalf("IPv6 rows came with the IPv4 round: %+v", got)
	}
	// the very next poll runs the IPv6 round, and no poll runs two rounds
	if !c.tickOnce() {
		t.Fatal("the IPv6 round did not follow")
	}
	if c.tickOnce() {
		t.Fatal("a third round ran inside the interval")
	}
	rounds = c.src.roundsSnapshot()
	if len(rounds) != 2 || rounds[1].Family != familyV6 {
		t.Fatalf("rounds = %+v, want IPv4 then IPv6", rounds)
	}
	r6 := rounds[1]
	if r6.Country != "RU" || r6.ControlHost != "ya.ru" || r6.ControlPort != 443 || len(r6.Want) != 0 {
		t.Errorf("IPv6 round = %+v, want the Russian IPv6 control and no pinned operators", r6)
	}
	if len(r6.Targets) != 1 || r6.Targets[0].Key != "de1" || r6.Targets[0].IP != "2001:db8::10" || len(r6.Targets[0].Ports) != 1 || r6.Targets[0].Ports[0] != 443 {
		t.Errorf("IPv6 targets = %+v, want de1 on its IPv6 address and TCP 443; nl1, fi1 and plain are skipped", r6.Targets)
	}
	for _, tg := range rounds[0].Targets {
		if tg.IP != "203.0.113.10" {
			t.Errorf("an IPv4 target = %+v", tg)
		}
	}

	// what was stored: IPv6 rows under family 6, each skipped node with its reason, and every IPv4 row as before
	reasons := map[string]string{"nl1": "ipv6_broken", "fi1": "no_ipv6", "plain": "not_checked"}
	for node, reason := range reasons {
		rows := c.rows(familyV6, node)
		if len(rows) != 1 || rows[0].Verdict != verdictUnknown || rows[0].Family != familyV6 {
			t.Fatalf("%s IPv6 rows = %+v", node, rows)
		}
		if d := detailOf(t, rows[0]); d.Reason != reason {
			t.Errorf("%s: reason %q, want %q", node, d.Reason, reason)
		}
	}
	if rows := c.rows(familyV6, "de1"); len(rows) != 1 || rows[0].Verdict != verdictOK || rows[0].Family != familyV6 {
		t.Fatalf("de1 IPv6 rows = %+v", rows)
	}
	for _, id := range []string{"de1", "nl1", "fi1", "plain"} {
		if v := c.verdicts(id); len(v) != 1 || v[0] != verdictOK {
			t.Errorf("%s IPv4 verdicts = %v, want the one ok round", id, v)
		}
	}

	// the next interval: IPv4 first again, then IPv6
	c.clock.Advance(16 * time.Minute)
	for _, want := range []int{familyV4, familyV6} {
		if !c.tickOnce() {
			t.Fatalf("no round for family %d", want)
		}
		rounds = c.src.roundsSnapshot()
		if got := rounds[len(rounds)-1].Family; got != want {
			t.Fatalf("round family = %d, want %d", got, want)
		}
	}
	if c.tickOnce() {
		t.Fatal("a third round ran inside the interval")
	}
}

func detailOf(t *testing.T, r store.CountryCheck) countryDetail {
	t.Helper()
	var d countryDetail
	if err := json.Unmarshal([]byte(r.Detail), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestCountryIPv6SwitchOffMeansNoIPv6Rounds(t *testing.T) {
	c := newIPv6Env(t)
	c.cfg.IPv6 = false
	if !c.tickOnce() {
		t.Fatal("no IPv4 round")
	}
	if c.tickOnce() {
		t.Fatal("a second round ran with the IPv6 checks off")
	}
	c.clock.Advance(16 * time.Minute)
	if !c.tickOnce() || c.tickOnce() {
		t.Fatal("the IPv4 rounds changed with the IPv6 checks off")
	}
	for _, r := range c.src.roundsSnapshot() {
		if r.Family != familyV4 {
			t.Fatalf("an IPv6 round was measured with the switch off: %+v", r)
		}
	}
	if rows := c.rows(familyV6, ""); len(rows) != 0 {
		t.Fatalf("IPv6 rows with the switch off: %+v", rows)
	}
}

func TestCountryIPv6RoundWithoutProbesAndOtherFailures(t *testing.T) {
	c := newIPv6Env(t)
	if !c.tickOnce() {
		t.Fatal("no IPv4 round")
	}
	c.src.err = ErrCountryNoProbes
	if !c.tickOnce() {
		t.Fatal("no IPv6 round")
	}
	rows := c.rows(familyV6, "de1")
	if len(rows) != 1 || rows[0].Verdict != verdictUnknown || detailOf(t, rows[0]).Reason != "no_probes" {
		t.Fatalf("de1 after a country without IPv6 probes: %+v", rows)
	}
	// a node that was skipped keeps its own reason
	if rows := c.rows(familyV6, "nl1"); len(rows) != 1 || detailOf(t, rows[0]).Reason != "ipv6_broken" {
		t.Fatalf("nl1: %+v", rows)
	}
	// and IPv4 was not touched
	if v := c.verdicts("de1"); len(v) != 1 || v[0] != verdictOK {
		t.Fatalf("de1 IPv4 verdicts = %v", v)
	}

	// a source that fails an IPv6 round for another reason is a source error as usual
	c.clock.Advance(16 * time.Minute)
	c.src.err = nil
	if !c.tickOnce() {
		t.Fatal("no IPv4 round")
	}
	c.src.err = errors.New("boom")
	if !c.tickOnce() {
		t.Fatal("no IPv6 round")
	}
	if rows := c.rows(familyV6, "de1"); len(rows) != 2 || detailOf(t, rows[0]).Reason != "source_error" {
		t.Fatalf("de1 after a source error: %+v", rows)
	}
}

// The IPv6 rounds run next to the IPv4 ones and change nothing of them: the same source answers, the same IPv4 rows and alerts
// come out, with the IPv6 checks on and off.
func TestIPv6RoundsLeaveTheIPv4OutcomeAlone(t *testing.T) {
	run := func(ipv6 bool) string {
		c := newCountryEnv(t)
		c.cfg.IPv6 = ipv6
		c.onlineNode("de1", "203.0.113.10")
		c.onlineNode("nl1", "203.0.113.20")
		c.report("de1", true, res("ipv6", dOK, "", "ipv6", "yes", "connect", "ok", "src", "2001:db8::10"))
		c.report("nl1", true, res("ipv6", dOK, "", "ipv6", "yes", "connect", "ok", "src", "2001:db8::20"))
		var out string
		for i, mode := range []string{"ok", "tcp_blocked", "tcp_blocked", "tcp_blocked", "ok", "ok"} {
			c.src.set("de1", mode)
			c.tickOnce()
			c.tickOnce() // the IPv6 round, when it is on
			c.evaluate()
			c.clock.Advance(16 * time.Minute)
			out += fmt.Sprintf("step %d: de1 %v nl1 %v\n", i, c.verdicts("de1"), c.verdicts("nl1"))
			for _, a := range c.active() {
				out += fmt.Sprintf("  active %s/%s/%s sev %d params %v opened %v\n", a.Kind, a.NodeID, a.Subject, a.Severity, withoutIPv6(a.Params), a.OpenedAt.Unix())
			}
		}
		for _, a := range c.env.history() {
			out += fmt.Sprintf("  history %s/%s/%s sev %d params %v opened %v resolved %v\n", a.Kind, a.NodeID, a.Subject, a.Severity, withoutIPv6(a.Params), a.OpenedAt.Unix(), a.ResolvedAt.Unix())
		}
		if ipv6 {
			if rows := c.rows(familyV6, "de1"); len(rows) == 0 {
				t.Fatal("the IPv6 checks were on, but no IPv6 row came")
			}
		}
		return out
	}
	off, on := run(false), run(true)
	if !reflect.DeepEqual(off, on) {
		t.Fatalf("IPv6 rounds changed the IPv4 outcome.\noff:\n%s\non:\n%s", off, on)
	}
	if off == "" {
		t.Fatal("empty golden")
	}
}

func TestCountryChecksCarryTheIPv6ResultNested(t *testing.T) {
	c := newIPv6Env(t)
	byNode := func() map[string]*adminv1.CountryCheck {
		t.Helper()
		got, err := c.s.countryChecks(c.ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]*adminv1.CountryCheck{}
		for _, m := range got {
			out[m.NodeId] = m
		}
		return out
	}
	c.tickOnce() // IPv4 only so far
	before := byNode()
	if m := before["de1"]; m == nil || m.Ipv6 == nil || m.Ipv6.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN || m.Ipv6.Reason != "not_checked" {
		t.Fatalf("before the IPv6 round: %+v", before["de1"])
	}
	c.tickOnce() // IPv6
	got := byNode()
	de := got["de1"]
	if de.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_OK || de.Ipv6 == nil || de.Ipv6.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_OK ||
		len(de.Ipv6.Probes) != 3 || len(de.Ipv6.Sources) != 1 || de.Ipv6.Sources[0] != "globalping" || de.Ipv6.Ipv6 != nil ||
		len(de.Ipv6.SourceVerdicts) != 1 || len(de.Ipv6.Vantages) != 3 {
		t.Fatalf("de1 = %+v", de)
	}
	for node, reason := range map[string]string{"nl1": "ipv6_broken", "fi1": "no_ipv6", "plain": "not_checked"} {
		m := got[node].Ipv6
		if m == nil || m.Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN || m.Reason != reason || m.NodeId != node || m.Country != "RU" {
			t.Errorf("%s ipv6 = %+v, want unknown %s", node, m, reason)
		}
	}

	// the IPv4 fields are the same message with the nested one taken off, and the switch off takes it away
	c.cfg.IPv6 = false
	off := byNode()
	for node, m := range got {
		m = proto.Clone(m).(*adminv1.CountryCheck)
		m.Ipv6 = nil
		if !proto.Equal(m, off[node]) {
			t.Errorf("%s: the IPv4 view changed with the IPv6 result.\nwith: %+v\nwithout: %+v", node, m, off[node])
		}
		if off[node].Ipv6 != nil {
			t.Errorf("%s: ipv6 shown with the switch off", node)
		}
	}
}
