package health

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// fakeCountrySource answers a round from a table: per node key a mode (ok, tcp_blocked, blocked, few) and nothing else.
type fakeCountrySource struct {
	mu      sync.Mutex
	limit   int
	mode    map[string]string
	err     error
	noProbe []string
	asked   []string
	rounds  []CountryRound
}

func (f *fakeCountrySource) Name() string     { return "fake" }
func (f *fakeCountrySource) HourlyLimit() int { return f.limit }

func (f *fakeCountrySource) Measure(_ context.Context, r CountryRound) (CountryObserved, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rounds = append(f.rounds, r)
	if f.err != nil {
		return CountryObserved{}, f.err
	}
	obs := CountryObserved{Eyeball: true, NoProbe: slices.Clone(f.noProbe), Asked: f.asked}
	for i, probe := range []struct {
		name string
		asn  uint32
	}{{"Rostelecom", 12389}, {"ER-Telecom", 51570}, {"Beeline", 3216}} {
		p := ObservedProbe{Network: probe.name, ASN: probe.asn, City: "Moscow", Eyeball: true, Online: true, ControlOK: true, Nodes: map[string]ObservedNode{}}
		for _, t := range r.Targets {
			n := ObservedNode{Answered: true}
			switch f.mode[t.Key] {
			case "", "ok":
				n.ICMPOK, n.ICMPMs = true, 30
				for _, port := range t.Ports {
					n.TCP = append(n.TCP, ObservedPort{Port: port, OK: true, MS: 31})
				}
			case "tcp_blocked":
				n.ICMPOK = true
				for _, port := range t.Ports {
					n.TCP = append(n.TCP, ObservedPort{Port: port})
				}
			case "blocked":
				for _, port := range t.Ports {
					n.TCP = append(n.TCP, ObservedPort{Port: port})
				}
			case "few": // only one probe is usable
				p.ControlOK = i == 0
				n.ICMPOK = true
			}
			n.Complete = true
			p.Nodes[t.Key] = n
		}
		obs.Probes = append(obs.Probes, p)
	}
	return obs, nil
}

func (f *fakeCountrySource) set(node, mode string) {
	f.mu.Lock()
	f.mode[node] = mode
	f.mu.Unlock()
}

func (f *fakeCountrySource) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rounds)
}

func (f *fakeCountrySource) roundsSnapshot() []CountryRound {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CountryRound(nil), f.rounds...)
}

// countryEnv is an environment with the country check on, a fake source and the Resolve of a name table.
type countryEnv struct {
	*env
	src   *fakeCountrySource
	cfg   *CountryConfig
	names map[string][]netip.Addr
	lists *atomic.Bool // the block-list source (Config.Lists)
	tspu  *TSPUState   // the TSPU probe source (Config.TSPU)
}

func newCountryEnv(t *testing.T) *countryEnv {
	t.Helper()
	cfg := &CountryConfig{Enabled: true, Country: "RU", Interval: 15 * time.Minute}
	src := &fakeCountrySource{limit: 250, mode: map[string]string{}}
	names := map[string][]netip.Addr{}
	lists := &atomic.Bool{}
	tspu := &TSPUState{}
	e := newEnv(t, func(c *Config) {
		c.Lists = func(context.Context) bool { return lists.Load() }
		c.TSPU = func(context.Context) TSPUState { return *tspu }
		c.Country = func(context.Context) (CountryConfig, CountrySource) {
			if !cfg.Enabled {
				return *cfg, nil
			}
			return *cfg, src
		}
		c.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
			if a, ok := names[host]; ok {
				return a, nil
			}
			return nil, errors.New("no such host")
		}
	})
	return &countryEnv{env: e, src: src, cfg: cfg, names: names, lists: lists, tspu: tspu}
}

// onlineNode adds an online node known by the name <id>.example.com, which resolves to ip, with a VLESS inbound on 443 and a
// Hysteria2 one on 8443 (REALITY needs a name).
func (c *countryEnv) onlineNode(id, ip string) {
	c.t.Helper()
	c.node(id, "hoster-"+id, true)
	c.vlessInbound(id, 443)
	c.inbound(id, 8443)
	c.names[id+".example.com"] = []netip.Addr{netip.MustParseAddr(ip), netip.MustParseAddr("2001:db8::7")}
}

// tick runs a due round and then the evaluator, like the scheduler does, and moves the clock past the interval.
func (c *countryEnv) tick() bool {
	c.t.Helper()
	c.s.invalidateSnapshot()
	ran := c.s.CountryTick(c.ctx)
	c.evaluate()
	c.clock.Advance(16 * time.Minute)
	return ran
}

func (c *countryEnv) verdicts(node string) []string {
	c.t.Helper()
	rows, err := c.st.RecentCountryChecks(c.ctx, 20, node)
	if err != nil {
		c.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, r.Verdict)
	}
	return out
}

func TestCountryJudgeVerdicts(t *testing.T) {
	probe := func(valid bool, icmp bool, tcp ...bool) ObservedProbe {
		n := ObservedNode{Answered: true, ICMPOK: icmp}
		for i, ok := range tcp {
			n.TCP = append(n.TCP, ObservedPort{Port: uint16(443 + i), OK: ok})
		}
		return ObservedProbe{Network: "net", Online: true, ControlOK: valid, Nodes: map[string]ObservedNode{"n": n}}
	}
	tcpTarget := CountryTarget{Key: "n", Ports: []uint16{443}}
	icmpTarget := CountryTarget{Key: "n"}
	for _, tc := range []struct {
		name   string
		target CountryTarget
		probes []ObservedProbe
		want   string
		reason string
	}{
		{"tcp connects everywhere", tcpTarget, []ObservedProbe{probe(true, true, true), probe(true, true, true), probe(true, true, true)}, "ok", ""},
		{"tcp connects on half", tcpTarget, []ObservedProbe{probe(true, true, true), probe(true, true, false)}, "ok", ""},
		{"tcp connects on one of three: a split picture", tcpTarget, []ObservedProbe{probe(true, true, true), probe(true, true, false), probe(true, true, false)}, "unknown", "mixed"},
		{"icmp passes, tcp fails everywhere", tcpTarget, []ObservedProbe{probe(true, true, false), probe(true, true, false), probe(true, true, false)}, "tcp_blocked", ""},
		{"icmp on half, tcp nowhere", tcpTarget, []ObservedProbe{probe(true, true, false), probe(true, false, false)}, "tcp_blocked", ""},
		{"nothing passes", tcpTarget, []ObservedProbe{probe(true, false, false), probe(true, false, false), probe(true, false, false)}, "blocked", ""},
		{"a probe that fails the control is discarded", tcpTarget, []ObservedProbe{probe(true, true, true), probe(true, true, true), probe(false, false, false)}, "ok", ""},
		{"the discarded probe would have flipped it", tcpTarget, []ObservedProbe{probe(true, false, false), probe(true, false, false), probe(false, true, true), probe(false, true, true)}, "blocked", ""},
		{"fewer than two valid probes", tcpTarget, []ObservedProbe{probe(true, true, true), probe(false, false, false), probe(false, false, false)}, "unknown", "too_few_probes"},
		{"no probes at all", tcpTarget, nil, "unknown", "too_few_probes"},
		{"icmp-only node answers", icmpTarget, []ObservedProbe{probe(true, true), probe(true, true), probe(true, false)}, "ok", ""},
		{"icmp-only node is silent", icmpTarget, []ObservedProbe{probe(true, false), probe(true, false)}, "blocked", ""},
		{"icmp-only node answers on a third", icmpTarget, []ObservedProbe{probe(true, true), probe(true, false), probe(true, false)}, "unknown", "mixed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verdict, d := countryJudge(tc.target, CountryObserved{Eyeball: true, Probes: tc.probes})
			if verdict != tc.want || d.Reason != tc.reason {
				t.Fatalf("verdict %q reason %q, want %q %q", verdict, d.Reason, tc.want, tc.reason)
			}
			if len(d.Probes) != len(tc.probes) {
				t.Fatalf("the detail lists %d probes, want all %d (also the discarded ones)", len(d.Probes), len(tc.probes))
			}
		})
	}

	// a probe that went offline in the middle of the round, and one whose test broke, do not count
	offline := ObservedProbe{Network: "net", Online: false, ControlOK: true, Nodes: map[string]ObservedNode{"n": {Answered: true}}}
	broken := ObservedProbe{Network: "net", Online: true, ControlOK: true, Nodes: map[string]ObservedNode{"n": {}}}
	if v, _ := countryJudge(tcpTarget, CountryObserved{Probes: []ObservedProbe{probe(true, true, true), offline, broken}}); v != "unknown" {
		t.Errorf("an offline and a broken probe counted: %q", v)
	}
}

func TestCountryPlanBudget(t *testing.T) {
	mk := func(nodes, ports int) []countryNode {
		out := make([]countryNode, nodes)
		for i := range out {
			out[i] = countryNode{ports: make([]uint16, ports)}
		}
		return out
	}
	// 5 nodes, 2 ports each: (1 + 5*3) * 3 = 48 tests a round; 4 rounds an hour = 192, under 80 % of 250
	if k, every, perHour := countryPlan(mk(5, 2), 15*time.Minute, 250); k != 3 || every != 15*time.Minute || perHour != 192 {
		t.Errorf("small fleet: probes %d every %v, %d an hour; want 3, 15m, 192", k, every, perHour)
	}
	// 7 nodes, 2 ports: 22 * 3 = 66 a round = 264 an hour > 200; with 2 probes 44 * 4 = 176 fits
	if k, every, perHour := countryPlan(mk(7, 2), 15*time.Minute, 250); k != 2 || every != 15*time.Minute || perHour != 176 {
		t.Errorf("probes cut: %d every %v, %d an hour; want 2, 15m, 176", k, every, perHour)
	}
	// 12 nodes, 2 ports: 37 * 2 = 74 a round; 4 an hour = 296 > 200: stretched until 74 * 60/m <= 200, i.e. 23 minutes
	k, every, perHour := countryPlan(mk(12, 2), 15*time.Minute, 250)
	if k != 2 || every != 23*time.Minute || perHour > 200 {
		t.Errorf("interval stretched: probes %d every %v, %d an hour; want 2, 23m, <= 200", k, every, perHour)
	}
	// the token doubles the limit: the same fleet runs at the chosen interval with 3 probes
	if k, every, _ := countryPlan(mk(7, 2), 15*time.Minute, 500); k != 3 || every != 15*time.Minute {
		t.Errorf("with a token: probes %d every %v, want 3, 15m", k, every)
	}
	// a longer chosen interval is never shortened, and no limit means no cuts
	if _, every, _ := countryPlan(mk(12, 2), 60*time.Minute, 250); every != 60*time.Minute {
		t.Errorf("chosen 60m became %v", every)
	}
	if k, every, _ := countryPlan(mk(50, 4), 10*time.Minute, 0); k != 3 || every != 10*time.Minute {
		t.Errorf("unlimited: %d every %v", k, every)
	}
}

func TestCountryTickTargetsAndSchedule(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.node("nl1", "hoster-nl1", false) // offline: not a target
	c.inbound("nl1", 9443)
	c.node("plain", "hoster-plain", true) // an IP address and no inbound: ICMP only
	c.exec(`UPDATE node SET address = '203.0.113.30' WHERE id = 'plain'`)
	c.node("v6", "hoster-v6", true)
	c.exec(`UPDATE node SET address = '2001:db8::1' WHERE id = 'v6'`)
	c.node("lost", "hoster-lost", true) // a name nobody resolves

	if !c.tick() {
		t.Fatal("the first round did not run")
	}
	if c.src.calls() != 1 {
		t.Fatalf("source calls = %d", c.src.calls())
	}
	r := c.src.rounds[0]
	if r.Country != "RU" || r.ControlHost != "ya.ru" || r.ControlPort != 443 || r.Probes != 3 {
		t.Fatalf("round = %+v", r)
	}
	byKey := map[string]CountryTarget{}
	for _, tg := range r.Targets {
		byKey[tg.Key] = tg
	}
	if len(byKey) != 2 {
		t.Fatalf("targets = %+v, want de1 and plain only (nl1 is offline, v6 has no IPv4, lost does not resolve)", r.Targets)
	}
	if de := byKey["de1"]; de.IP != "203.0.113.10" || len(de.Ports) != 1 || de.Ports[0] != 443 {
		t.Errorf("de1 = %+v, want its name resolved to the IPv4 and TCP 443 once (the VLESS port and the Hysteria2 decoy are the same)", de)
	}
	if pl := byKey["plain"]; pl.IP != "203.0.113.30" || len(pl.Ports) != 0 {
		t.Errorf("plain = %+v, want ICMP only", pl)
	}
	for _, id := range []string{"v6", "lost"} {
		if v := c.verdicts(id); len(v) != 1 || v[0] != "unknown" {
			t.Errorf("%s should leave an unknown row: %v", id, v)
		}
	}
	if v := c.verdicts("nl1"); len(v) != 0 {
		t.Errorf("an offline node was checked: %v", v)
	}

	// not due again until the interval has passed (tick moved the clock 16 minutes, the interval is 15)
	if !c.tick() {
		t.Fatal("the second round did not run after 16 minutes")
	}
	c.clock.Advance(-10 * time.Minute)
	if c.tick() {
		t.Fatal("a round ran before its interval")
	}
}

func TestCountryTickRecordsOperatorWantsAndSkipsUnlistedCountries(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	if !c.tick() {
		t.Fatal("the RU round did not run")
	}
	rounds := c.src.roundsSnapshot()
	if len(rounds) != 1 {
		t.Fatalf("rounds = %d, want 1", len(rounds))
	}
	var got []string
	for _, want := range rounds[0].Want {
		got = append(got, want.Name)
	}
	want := []string{"MTS", "Beeline", "MegaFon", "Rostelecom", "T2", "ER-Telecom"}
	if !slices.Equal(got, want) {
		t.Fatalf("first round wants = %v, want table order %v", got, want)
	}

	c.cfg.Country = "DE"
	if !c.tick() {
		t.Fatal("the DE round did not run")
	}
	rounds = c.src.roundsSnapshot()
	if len(rounds) != 2 || rounds[1].Country != "DE" || len(rounds[1].Want) != 0 {
		t.Fatalf("unlisted country round = %+v, want no operator wants", rounds[1])
	}
}

func TestAskedOperatorWithoutAnAnswerDoesNotStayFirst(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.src.asked = []string{"MTS"}
	for i := range 4 {
		if !c.tick() {
			t.Fatal("no round")
		}
		if i == 0 {
			c.src.mu.Lock()
			c.src.asked = nil
			c.src.mu.Unlock()
		}
	}
	for i, r := range c.src.roundsSnapshot() {
		if len(r.Want) == 0 {
			t.Fatalf("round %d has no operator wants", i+1)
		}
		if i > 0 && r.Want[0].Name == "MTS" {
			t.Fatalf("round %d starts with MTS after its pin was asked but unanswered", i+1)
		}
	}
}

func TestCountryOperatorRotationFallsBackToTableOrderOnStoreError(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.src.asked = []string{"MTS"}
	c.src.mu.Lock()
	c.src.mode = map[string]string{}
	c.src.mu.Unlock()
	if !c.tick() {
		t.Fatal("no round")
	}
	ok := c.s.countryRoundWants(c.ctx, "RU")
	ctx, cancel := context.WithCancel(c.ctx)
	cancel()
	failed := c.s.countryRoundWants(ctx, "RU")
	if ok[0].Name == failed[0].Name {
		t.Fatalf("a working read and a failed read give the same order (%s first)", ok[0].Name)
	}
	if ok[0].Name == "MTS" {
		t.Fatal("a successful read did not count the unanswered MTS pin")
	}
	if failed[0].Name != "MTS" {
		t.Fatalf("failed read order starts with %s, want table order", failed[0].Name)
	}
}

func TestCountryRateLimitHoldsTheNextRound(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.src.err = &CountryLimitError{Reset: 40 * time.Minute}
	if !c.tick() {
		t.Fatal("the round did not run")
	}
	v := c.verdicts("de1")
	if len(v) != 1 || v[0] != "unknown" {
		t.Fatalf("rate-limited round stored %v, want unknown", v)
	}
	rows, _ := c.st.RecentCountryChecks(c.ctx, 1, "de1")
	if m := countryCheckMsg(rows[0]); m.Reason != "rate_limited" {
		t.Fatalf("reason = %q", m.Reason)
	}
	// 16 minutes later the interval is up but the hold (40 minutes) is not
	c.src.err = nil
	if c.tick() {
		t.Fatal("a round ran inside the hold")
	}
	if hold := c.s.countryHold(c.ctx); !hold.After(c.clock.Now()) {
		t.Fatalf("hold = %v, want a time in the future", hold)
	}
	c.clock.Advance(30 * time.Minute)
	if !c.tick() {
		t.Fatal("no round after the hold ended")
	}
}

func TestCountrySourceFailureAndTimeoutAreUnknown(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.src.err = errors.New("globalping: HTTP 500")
	c.tick()
	c.src.err = context.DeadlineExceeded
	c.tick()
	rows, _ := c.st.RecentCountryChecks(c.ctx, 5, "de1")
	if len(rows) != 2 || countryCheckMsg(rows[0]).Reason != "timeout" || countryCheckMsg(rows[1]).Reason != "source_error" {
		t.Fatalf("rows = %+v", rows)
	}
	if len(c.active()) != 0 {
		t.Fatalf("unknown rounds raised %v", c.active())
	}
}

// The alert opens after two bad rounds in a row, ignores unknown rounds, and closes after two OK rounds.
func mustHave(t *testing.T, c *countryEnv, key string, open bool) {
	t.Helper()
	if _, ok := c.active()[key]; ok != open {
		t.Fatalf("alert %s open = %v, want %v (active: %v)", key, ok, open, c.active())
	}
}

func TestCountryAlertLifecycle(t *testing.T) {
	var seen []Transition
	c := newCountryEnv(t)
	c.s.cfg.OnTransition = func(tr Transition) {
		if tr.Alert.Kind == kCountryBlocked {
			seen = append(seen, tr)
		}
	}
	c.onlineNode("de1", "203.0.113.10")
	const k = "country_blocked/de1/"

	c.tick() // ok
	c.src.set("de1", "tcp_blocked")
	c.tick() // one bad round
	mustHave(t, c, k, false)
	c.src.set("de1", "few")
	c.tick() // unknown: neutral
	c.tick()
	mustHave(t, c, k, false)
	c.src.set("de1", "blocked")
	c.tick() // second bad round, with an unknown between them
	mustHave(t, c, k, true)
	a := c.active()[k]
	if a.Severity != sevWarning || a.WhyKey != "health.alert.country_blocked.why.blocked" || a.TitleKey != "health.alert.country_blocked.title" {
		t.Fatalf("alert = %+v", a)
	}
	for key, val := range map[string]string{"node": "de1", "country": "RU", "verdict": "blocked", "networks": "Beeline, ER-Telecom, Rostelecom", "ports": "443"} {
		if a.Params[key] != val {
			t.Errorf("param %s = %q, want %q", key, a.Params[key], val)
		}
	}
	if len(seen) != 1 || seen[0].Resolved {
		t.Fatalf("transitions = %+v, want a single open", seen)
	}

	// the node goes offline: no round, the alert is held
	c.fl.set("de1", liveState{up: false, caps: []string{capDoctor}, lastSeen: c.clock.Now()})
	before := c.src.calls()
	c.tick()
	c.tick()
	if c.src.calls() != before {
		t.Fatal("an offline node was measured")
	}
	mustHave(t, c, k, true)

	// back online; one OK round is not enough, an unknown does not help, two close it
	c.fl.set("de1", liveState{up: true, caps: []string{capDoctor}, lastSeen: c.clock.Now()})
	c.src.set("de1", "ok")
	c.tick()
	mustHave(t, c, k, true)
	c.src.set("de1", "few")
	c.tick()
	mustHave(t, c, k, true)
	c.src.set("de1", "ok")
	c.tick()
	mustHave(t, c, k, false)
	if len(seen) != 2 || !seen[1].Resolved || seen[1].Alert.Resolution != "cleared" {
		t.Fatalf("transitions = %+v", seen)
	}
	// and it can be muted like any other
	c.src.set("de1", "tcp_blocked")
	c.tick()
	c.tick()
	a = c.active()[k]
	if a.Params["verdict"] != "tcp_blocked" || a.WhyKey != "health.alert.country_blocked.why.tcp_blocked" {
		t.Fatalf("second alert = %+v", a)
	}
	if _, err := c.st.MuteAlert(c.ctx, a.ID, c.clock.Now().Add(time.Hour)); err != nil {
		t.Fatalf("mute: %v", err)
	}
}

// Switching the integration off stops the scheduler and lets the alert go; the stored rounds stay but the API hides them.
func TestCountryOffStopsEverything(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.src.set("de1", "blocked")
	c.tick()
	c.tick()
	mustHave(t, c, "country_blocked/de1/", true)
	calls := c.src.calls()
	if rows, _ := c.s.countryChecks(c.ctx, ""); len(rows) != 1 {
		t.Fatalf("country_checks while on = %v", rows)
	}

	c.cfg.Enabled = false
	for range 5 {
		if c.tick() {
			t.Fatal("a round ran while the check is off")
		}
	}
	if c.src.calls() != calls {
		t.Fatalf("the source was asked %d times after it was switched off", c.src.calls()-calls)
	}
	if len(c.active()) != 0 {
		t.Fatalf("the alert stayed after the check was switched off: %v", c.active())
	}
	if len(c.verdicts("de1")) != 2 {
		t.Fatalf("stored rounds = %v, they stay until the retention removes them", c.verdicts("de1"))
	}
	if rows, err := c.s.countryChecks(c.ctx, ""); err != nil || len(rows) != 0 {
		t.Errorf("country_checks while off = %v, %v; the API must report it off", rows, err)
	}

	// and on again: rounds resume
	c.cfg.Enabled = true
	if !c.tick() {
		t.Fatal("no round after switching it on again")
	}
}

func TestCountryOffIsTheDefault(t *testing.T) {
	e := newEnv(t) // no Country configured at all
	e.node("de1", "hoster", true)
	e.exec(`UPDATE node SET address = '203.0.113.10' WHERE id = 'de1'`)
	if e.s.CountryTick(e.ctx) {
		t.Fatal("a round ran without any configuration")
	}
	e.s.ReconcileCountry(e.ctx)
	if rows, _ := e.s.countryChecks(e.ctx, ""); len(rows) != 0 {
		t.Error("rows without configuration")
	}
}

func TestCountryRetention(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	now := c.clock.Now()
	if err := c.st.InsertCountryChecks(c.ctx, []store.CountryCheck{
		{NodeID: "de1", At: now.Add(-8 * 24 * time.Hour), Country: "RU", Verdict: "ok"},
		{NodeID: "de1", At: now.Add(-6 * 24 * time.Hour), Country: "RU", Verdict: "ok"},
	}); err != nil {
		t.Fatal(err)
	}
	c.s.Retention(c.ctx)
	if rows, _ := c.st.RecentCountryChecks(c.ctx, 10); len(rows) != 1 || !rows[0].At.Equal(now.Add(-6*24*time.Hour).Truncate(time.Second)) {
		t.Fatalf("after the sweep: %+v, want only the 6-day-old round", rows)
	}
}

func TestCountryControlTable(t *testing.T) {
	if h, p := countryControl("RU"); h != "ya.ru" || p != 443 {
		t.Errorf("RU control = %s:%d", h, p)
	}
	if h, _ := countryControl("DE"); h != "www.cloudflare.com" {
		t.Errorf("default control = %s", h)
	}
}

func TestGetChecksCarriesTheCountryCheck(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.onlineNode("nl1", "203.0.113.20")
	c.src.noProbe = []string{"MegaFon", "T2"}
	c.src.set("nl1", "tcp_blocked")
	c.tick()
	r, err := rpc{c.s}.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*adminv1.CountryCheck{}
	for _, cc := range r.Msg.CountryChecks {
		got[cc.NodeId] = cc
	}
	if len(got) != 2 || got["de1"].Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_OK || got["nl1"].Verdict != adminv1.CountryVerdict_COUNTRY_VERDICT_TCP_BLOCKED {
		t.Fatalf("country checks = %+v", r.Msg.CountryChecks)
	}
	nl := got["nl1"]
	if nl.Country != "RU" || !nl.Eyeball || !slices.Equal(nl.NoProbe, []string{"MegaFon", "T2"}) || len(nl.Probes) != 3 || !nl.Probes[0].Eyeball || !nl.Probes[0].Valid || !nl.Probes[0].IcmpOk || nl.Probes[0].Network != "Rostelecom" ||
		len(nl.Probes[0].Tcp) != 1 || nl.Probes[0].Tcp[0].Port != 443 || nl.Probes[0].Tcp[0].Ok {
		t.Fatalf("nl1 = %+v", nl)
	}
	// one node's own
	one, err := rpc{c.s}.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{NodeId: "de1"}))
	if err != nil || len(one.Msg.CountryChecks) != 1 || one.Msg.CountryChecks[0].NodeId != "de1" {
		t.Fatalf("for one node: %+v, %v", one, err)
	}
	// off: nothing, though the rounds are still stored
	c.cfg.Enabled = false
	off, err := rpc{c.s}.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{}))
	if err != nil || len(off.Msg.CountryChecks) != 0 {
		t.Fatalf("while off: %+v, %v", off, err)
	}
	// the alert kind has its proto name
	if kindProto[kCountryBlocked] != adminv1.AlertKind_ALERT_KIND_COUNTRY_BLOCKED {
		t.Fatal("kindProto")
	}
}
