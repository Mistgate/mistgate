package health

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The IPv6 side of the alerts and the history (step 4 of the IPv6 plan): the verdict word of a node over IPv6, the ipv6
// parameter of its IPv4 alert, the IPv6 alert of a node with the IPv6 entry for clients, and the history per family.

// withoutIPv6 is an alert's parameters without the ipv6 one (the golden test of step 3 compares the rest).
func withoutIPv6(params map[string]string) map[string]string {
	out := maps.Clone(params)
	delete(out, "ipv6")
	return out
}

// pair runs one IPv4 round with the node in mode4, then one IPv6 round with it in mode6 (the fake answers by node key, whatever
// the family), evaluates the alerts and moves the clock past the interval.
func (c *countryEnv) pair(node, mode4, mode6 string) {
	c.t.Helper()
	c.src.set(node, mode4)
	if !c.tickOnce() {
		c.t.Fatal("no IPv4 round was due")
	}
	c.src.set(node, mode6)
	if !c.tickOnce() {
		c.t.Fatal("no IPv6 round was due")
	}
	if rounds := c.src.roundsSnapshot(); rounds[len(rounds)-1].Family != familyV6 {
		c.t.Fatalf("the second round was of family %d", rounds[len(rounds)-1].Family)
	}
	c.evaluate()
	c.clock.Advance(16 * time.Minute)
}

func (c *countryEnv) transitions() *[]Transition {
	var seen []Transition
	c.s.cfg.OnTransition = func(tr Transition) {
		if tr.Alert.Kind == kCountryBlocked {
			seen = append(seen, tr)
		}
	}
	return &seen
}

func TestCountryIPv6WordFollowsTheRow(t *testing.T) {
	row := func(verdict string) *store.CountryCheck { return &store.CountryCheck{Verdict: verdict} }
	for _, tc := range []struct {
		name  string
		row   *store.CountryCheck
		fresh bool
		want  string
	}{
		{"ok", row(verdictOK), true, "open"},
		{"partial", row(verdictPartial), true, "partial"},
		{"tcp cut", row(verdictTCPBlocked), true, "blocked"},
		{"blocked", row(verdictBlocked), true, "blocked"},
		{"unknown (no IPv6, broken, no probes)", row(verdictUnknown), true, "unknown"},
		{"a stale row says nothing", row(verdictOK), false, "unknown"},
		{"no row", nil, true, "unknown"},
	} {
		if got := countryIPv6Word(tc.row, tc.fresh); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// and the verdict the nested CountryCheck.ipv6 shows is the same row's
	for verdict, proto := range map[string]adminv1.CountryVerdict{verdictOK: adminv1.CountryVerdict_COUNTRY_VERDICT_OK,
		verdictPartial: adminv1.CountryVerdict_COUNTRY_VERDICT_PARTIAL, verdictTCPBlocked: adminv1.CountryVerdict_COUNTRY_VERDICT_TCP_BLOCKED,
		verdictBlocked: adminv1.CountryVerdict_COUNTRY_VERDICT_BLOCKED, verdictUnknown: adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN} {
		r := &store.CountryCheck{NodeID: "de1", Country: "RU", Verdict: verdict, At: time.Unix(1_790_000_000, 0)}
		if got := countryIPv6Msg("de1", "RU", r, true).Verdict; got != proto {
			t.Errorf("%s: nested verdict %v, want %v", verdict, got, proto)
		}
	}
}

// The IPv4 alert of a node without the IPv6 entry carries what the IPv6 rounds say. Updating it is a TouchAlert: the alert is
// the same row and nobody is notified again.
func TestIPv4AlertCarriesTheIPv6WordWithoutANewNotification(t *testing.T) {
	c := newIPv6Env(t)
	c.onlineNode("se1", "203.0.113.30") // a second node over IPv6: a dark probe is partial only if it reaches another node
	c.report("se1", true, res("ipv6", dOK, "", "ipv6", "yes", "connect", "ok", "src", "2001:db8::30"))
	seen := c.transitions()
	const k = "country_blocked/de1/"

	c.pair("de1", "ok", "ok")
	c.pair("de1", "tcp_blocked", "blocked")
	mustHave(t, c, k, false)
	c.pair("de1", "tcp_blocked", "blocked")
	mustHave(t, c, k, true)
	if got := c.active()[k].Params["ipv6"]; got != "blocked" {
		t.Fatalf("ipv6 = %q, want blocked", got)
	}
	if len(*seen) != 1 || (*seen)[0].Resolved {
		t.Fatalf("transitions %+v, want a single open", *seen)
	}
	opened := c.active()[k]

	for _, step := range []struct{ mode6, want string }{{"ok", "open"}, {"dark:0", "partial"}, {"few", "unknown"}, {"ok", "open"}} {
		c.pair("de1", "tcp_blocked", step.mode6)
		a, ok := c.active()[k]
		if !ok || a.Params["ipv6"] != step.want {
			t.Fatalf("v6 %s: alert %+v, want ipv6=%s", step.mode6, a, step.want)
		}
		if a.ID != opened.ID || !a.OpenedAt.Equal(opened.OpenedAt) || a.Params["verdict"] != "tcp_blocked" {
			t.Errorf("v6 %s: the alert is not the one that was opened: %+v", step.mode6, a)
		}
		if len(*seen) != 1 {
			t.Fatalf("v6 %s: %d notifications, want the one of the opening", step.mode6, len(*seen))
		}
	}
	// the Telegram text of the open alert is built from these parameters: the opening one said nothing of IPv6, the next does
	if (*seen)[0].Alert.Params["ipv6"] != "blocked" {
		t.Errorf("the opening transition carried %v", (*seen)[0].Alert.Params)
	}

	// the IPv6 checks switched off take the parameter away, again without a notification
	c.src.set("de1", "tcp_blocked")
	c.cfg.IPv6 = false
	c.tickOnce()
	c.evaluate()
	if a := c.active()[k]; a.ID != opened.ID || a.Params["ipv6"] != "" {
		t.Errorf("with the IPv6 checks off: %+v", a)
	}
	if len(*seen) != 1 {
		t.Fatalf("%d notifications after the switch", len(*seen))
	}
}

func TestIPv4AlertOfANodeWithoutWorkingIPv6SaysUnknown(t *testing.T) {
	c := newIPv6Env(t)
	for i := 0; i < 2; i++ {
		c.pair("nl1", "blocked", "blocked") // the node's IPv6 is broken at the hoster: its IPv6 row is unknown whatever the fake does
	}
	a, ok := c.active()["country_blocked/nl1/"]
	if !ok || a.Params["ipv6"] != "unknown" {
		t.Fatalf("alert %+v", a)
	}
}

// With the IPv6 checks off the alert is the one it always was.
func TestIPv4AlertHasNoIPv6ParameterWithTheChecksOff(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.src.set("de1", "blocked")
	c.tick()
	c.tick()
	a, ok := c.active()["country_blocked/de1/"]
	if !ok {
		t.Fatal("no alert")
	}
	if _, has := a.Params["ipv6"]; has || a.Params["family"] != "" {
		t.Errorf("params %v", a.Params)
	}
}

// The IPv6 alert exists only for a node with the IPv6 entry for clients. Until that entry exists (step 7) nothing sets it, so
// no IPv6 alert opens whatever the IPv6 rounds say; once a node has it, the alert has its own keys and the IPv4 ones are not
// touched.
func TestIPv6AlertOnlyForANodeWithTheEntry(t *testing.T) {
	c := newIPv6Env(t)
	seen := c.transitions()
	if c.s.ipv6Entry(store.NodeRow{ID: "de1"}) {
		t.Fatal("a node has an IPv6 entry before the feature exists")
	}
	noCountryAlert := func() {
		t.Helper()
		for key := range c.active() {
			if strings.HasPrefix(key, "country_blocked/") {
				t.Fatalf("a country alert without an entry: %v", c.active())
			}
		}
	}
	for i := 0; i < 4; i++ {
		c.pair("de1", "ok", "blocked") // IPv4 fine, IPv6 cut
		noCountryAlert()
	}
	if got := c.verdicts("de1"); got[0] != verdictOK {
		t.Fatalf("v4 verdicts %v", got)
	}
	c.pair("de1", "ok", "ok")
	c.pair("de1", "ok", "ok")

	// the entry is turned on for de1 only
	c.s.entry6 = func(n store.NodeRow) bool { return n.ID == "de1" }
	c.pair("de1", "ok", "blocked")
	mustHave(t, c, "country_blocked/de1/ipv6", false) // one round is not an alert, here too
	c.pair("de1", "ok", "blocked")
	a, ok := c.active()["country_blocked/de1/ipv6"]
	if !ok {
		t.Fatalf("no IPv6 alert after two cut rounds: %v", c.active())
	}
	if a.Params["family"] != "6" || a.Params["verdict"] != "blocked" || a.Params["country"] != "RU" || a.Params["node"] != "de1" || a.Params["ipv6"] != "" ||
		a.WhyKey != "health.alert.country_blocked.why.blocked" || a.Severity != sevWarning {
		t.Fatalf("IPv6 alert = %+v", a)
	}
	mustHave(t, c, "country_blocked/de1/", false)
	if len(*seen) != 1 || (*seen)[0].Alert.Subject != "ipv6" {
		t.Fatalf("transitions %+v", *seen)
	}

	// a v4 block of the same node is its own alert, with no ipv6 parameter (the IPv6 one speaks for IPv6)
	c.pair("de1", "blocked", "blocked")
	c.pair("de1", "blocked", "blocked")
	v4, ok := c.active()["country_blocked/de1/"]
	if !ok || v4.Params["ipv6"] != "" || v4.Params["family"] != "" {
		t.Fatalf("IPv4 alert = %+v", v4)
	}
	mustHave(t, c, "country_blocked/de1/ipv6", true)

	// IPv6 back: two reaching rounds close the IPv6 alert only
	c.pair("de1", "blocked", "ok")
	c.pair("de1", "blocked", "ok")
	mustHave(t, c, "country_blocked/de1/ipv6", false)
	mustHave(t, c, "country_blocked/de1/", true)

	// the entry turned off lets the key go: a new IPv6 block opens nothing, and one that was open is resolved
	c.pair("de1", "blocked", "blocked")
	c.pair("de1", "blocked", "blocked")
	mustHave(t, c, "country_blocked/de1/ipv6", true)
	c.s.entry6 = nil
	c.pair("de1", "blocked", "blocked")
	mustHave(t, c, "country_blocked/de1/ipv6", false)
	if v4 := c.active()["country_blocked/de1/"]; v4.Params["ipv6"] != "blocked" {
		t.Errorf("without an entry the IPv4 alert speaks of IPv6 again: %v", v4.Params)
	}
}

// The IPv6 alert is held while the node is offline, and with the IPv6 checks off it is let go.
func TestIPv6AlertIsHeldOfflineAndReleasedWithTheSwitch(t *testing.T) {
	c := newIPv6Env(t)
	c.s.entry6 = func(n store.NodeRow) bool { return n.ID == "de1" }
	c.pair("de1", "ok", "blocked")
	c.pair("de1", "ok", "blocked")
	const k = "country_blocked/de1/ipv6"
	mustHave(t, c, k, true)

	c.fl.set("de1", liveState{up: false, caps: []string{capDoctor}, lastSeen: c.clock.Now()})
	c.evaluate()
	c.clock.Advance(16 * time.Minute)
	c.evaluate()
	mustHave(t, c, k, true)

	c.fl.set("de1", liveState{up: true, caps: []string{capDoctor}, lastSeen: c.clock.Now()})
	c.cfg.IPv6 = false
	c.evaluate()
	mustHave(t, c, k, false)
}

// An IPv6 rounds' outage (no probes with IPv6) is unknown, not a block: it opens nothing and closes nothing.
func TestIPv6AlertIgnoresUnknownRounds(t *testing.T) {
	c := newIPv6Env(t)
	c.s.entry6 = func(n store.NodeRow) bool { return n.ID == "de1" }
	c.pair("de1", "ok", "blocked")
	c.pair("de1", "ok", "few")
	c.pair("de1", "ok", "few")
	mustHave(t, c, "country_blocked/de1/ipv6", false)
	c.pair("de1", "ok", "blocked")
	mustHave(t, c, "country_blocked/de1/ipv6", true)
	c.pair("de1", "ok", "few")
	c.pair("de1", "ok", "few")
	mustHave(t, c, "country_blocked/de1/ipv6", true)
}

// decorate gives the partial alert of the family its subject; the IPv4 subjects are the old ones.
func TestCountryFamilyAlertsSubjects(t *testing.T) {
	full4, partial4 := countryFamilyAlerts{family: familyV4}.subjects()
	full6, partial6 := countryFamilyAlerts{family: familyV6}.subjects()
	if full4 != "" || partial4 != "partial" || full6 != "ipv6" || partial6 != "ipv6/partial" {
		t.Fatalf("subjects %q %q %q %q", full4, partial4, full6, partial6)
	}
	base := cond{key: key{kCountryBlocked, "n1", "partial"}, params: map[string]string{"node": "de1", "country": "RU"}}
	got := countryFamilyAlerts{family: familyV6}.decorate(base)
	if got.key.subject != "ipv6/partial" || got.params["family"] != "6" || base.params["family"] != "" || base.key.subject != "partial" {
		t.Errorf("IPv6 partial = %+v (base %+v)", got, base)
	}
	got = countryFamilyAlerts{family: familyV4, ipv6: "open"}.decorate(base)
	if got.key.subject != "partial" || got.params["ipv6"] != "open" || len(got.params) != 3 {
		t.Errorf("IPv4 partial = %+v", got)
	}
	if got = (countryFamilyAlerts{family: familyV4}).decorate(base); !maps.Equal(got.params, base.params) || got.key != base.key {
		t.Errorf("IPv4 alert without the word changed: %+v", got)
	}
}

// history6 is the IPv6 history of the week, one line per lane like countryEnv.history.
func (c *countryEnv) history6() (sources []string, lanes []string) {
	c.t.Helper()
	now := c.clock.Now()
	sources, nodes, err := c.s.CountryHistory(c.ctx, now.Add(-7*24*time.Hour), familyV6)
	if err != nil {
		c.t.Fatal(err)
	}
	ago := func(unix int64) int64 { return (now.Unix() - unix) / 3600 }
	for _, n := range nodes {
		for _, l := range n.Lanes {
			var pts []string
			for _, p := range l.Points {
				pts = append(pts, fmt.Sprintf("%s@%d", strings.ToLower(strings.TrimPrefix(p.Verdict.String(), "COUNTRY_VERDICT_")), ago(p.AtUnix)))
			}
			lanes = append(lanes, fmt.Sprintf("%s %s %s until=%d", n.NodeId, l.Source, strings.Join(pts, ","), ago(l.UntilUnix)))
		}
	}
	return sources, lanes
}

// The history is read per family: IPv6 is the Globalping lane of the IPv6 rows, nothing else; the IPv4 history does not see
// the IPv6 rows, and the IPv6 one is empty while the IPv6 checks are off.
func TestCountryHistoryPerFamily(t *testing.T) {
	c := newCountryEnv(t)
	c.cfg.IPv6 = true
	c.storeWeek()
	c.lists.Store(true)
	c.tspuOn()
	now := c.clock.Now()
	h := func(n int) time.Time { return now.Add(-time.Duration(n) * time.Hour) }
	var rows []store.CountryCheck
	for _, r := range []struct {
		node, verdict string
		ago           int
	}{{"nl1", "ok", 100}, {"nl1", "ok", 90}, {"nl1", "blocked", 60}, {"nl1", "unknown", 30}, {"nl1", "ok", 5}, {"de1", "tcp_blocked", 2}} {
		rows = append(rows, store.CountryCheck{NodeID: r.node, Country: "RU", Family: familyV6, Verdict: r.verdict, At: h(r.ago)})
	}
	rows = append(rows, store.CountryCheck{NodeID: "de1", Country: "RU", Family: familyV6, Verdict: "ok", At: h(1)})
	if err := c.st.InsertCountryChecks(c.ctx, rows); err != nil {
		t.Fatal(err)
	}

	sources4, lanes4 := c.history()
	if !slices.Equal(sources4, []string{"globalping", "lists", "tspu"}) {
		t.Fatalf("IPv4 sources %v", sources4)
	}
	// the IPv4 lanes are what storeWeek alone makes: the IPv6 rows are in none
	c2 := newCountryEnv(t)
	c2.cfg.IPv6 = true
	c2.storeWeek()
	c2.lists.Store(true)
	c2.tspuOn()
	_, lanes4Alone := c2.history()
	if !slices.Equal(lanes4, lanes4Alone) {
		t.Fatalf("the IPv6 rows changed the IPv4 history.\nwith: %q\nalone: %q", lanes4, lanes4Alone)
	}

	sources6, lanes6 := c.history6()
	want := []string{"de1 globalping tcp_blocked@2,ok@1 until=0", "nl1 globalping ok@100,blocked@60,unknown@30,ok@5 until=0"}
	if !slices.Equal(sources6, []string{"globalping"}) || !slices.Equal(lanes6, want) {
		t.Fatalf("IPv6 history: %v\n%q\nwant %q", sources6, lanes6, want)
	}

	c.cfg.IPv6 = false
	if sources, lanes := c.history6(); len(sources) != 0 || len(lanes) != 0 {
		t.Fatalf("IPv6 history with the IPv6 checks off: %v %v", sources, lanes)
	}
	c.cfg.IPv6, c.cfg.Enabled = true, false
	if sources, lanes := c.history6(); len(sources) != 0 || len(lanes) != 0 {
		t.Fatalf("IPv6 history with Globalping off: %v %v", sources, lanes)
	}
}
