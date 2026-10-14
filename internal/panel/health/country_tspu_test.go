package health

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type tport = countryreach.TSPUPort

// tspuOn switches the TSPU probe source on with an invented probe node, probe1, whose rows count for six hours.
func (c *countryEnv) tspuOn() {
	c.t.Helper()
	var n int
	if err := c.st.R.QueryRowContext(c.ctx, `SELECT count(*) FROM node WHERE id = 'probe1'`).Scan(&n); err != nil {
		c.t.Fatal(err)
	}
	if n == 0 {
		c.node("probe1", "hoster-probe1", true)
	}
	*c.tspu = TSPUState{On: true, Vantage: "probe1", MaxAge: 6 * time.Hour,
		Run: &adminv1.TspuProbeRun{Status: "ok", AtUnix: c.clock.Now().Unix(), NodeId: "probe1", DpiHop: 4, BlockedRef: "31.13.72.36", NodesChecked: 1}}
}

// putTSPU stores what the probe node saw of a node now (what the countryprobe package does after a batch).
func (c *countryEnv) putTSPU(node, verdict string, ports ...tport) {
	c.t.Helper()
	detail, _ := json.Marshal(ports)
	row := store.TSPUCheck{NodeID: node, Vantage: "probe1", At: c.clock.Now(), Verdict: verdict, DPIHop: 4, ControlOK: true, DetailJSON: string(detail)}
	if err := c.st.InsertTSPUChecks(c.ctx, []store.TSPUCheck{row}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *countryEnv) noView() {
	c.t.Helper()
	rows, err := c.s.countryChecks(c.ctx, "")
	if err != nil || len(rows) != 0 {
		c.t.Fatalf("country_checks = %v, %v, want none", rows, err)
	}
}

var (
	cvOK      = adminv1.CountryVerdict_COUNTRY_VERDICT_OK
	cvTCP     = adminv1.CountryVerdict_COUNTRY_VERDICT_TCP_BLOCKED
	cvBlocked = adminv1.CountryVerdict_COUNTRY_VERDICT_BLOCKED
	cvUnknown = adminv1.CountryVerdict_COUNTRY_VERDICT_UNKNOWN
	cvListed  = adminv1.CountryVerdict_COUNTRY_VERDICT_LISTED
)

// With only the probe node on, a node shows its verdict: blocked ports are tcp_blocked, an open port keeps it ok; always the
// data-centre confidence, with the detail of the run.
func TestCountryChecksShowTheTSPUProbeAlone(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.cfg.Enabled = false // Globalping off
	c.noView()            // nothing is on yet
	c.tspuOn()
	c.noView() // on, but no run yet

	c.putTSPU("de1", "partial", tport{Port: 443, Verdict: "blocked"}, tport{Port: 8443, Verdict: "refused"})
	at := c.clock.Now().Unix()
	v := c.view("de1")
	if v.Verdict != cvTCP || v.Confidence != "dc_only" || !slices.Equal(v.Sources, []string{"tspu"}) || v.Country != "RU" || v.AtUnix != at ||
		v.TspuVerdict != cvTCP || v.DpiHop != 4 || v.TspuVantageId != "probe1" || v.TspuVantageName != "probe1" || v.TspuAtUnix != at ||
		len(v.TspuPorts) != 2 || v.TspuPorts[0].Port != 443 || v.TspuPorts[0].Verdict != "blocked" || v.TspuPorts[1].Verdict != "refused" ||
		v.GlobalpingVerdict != adminv1.CountryVerdict_COUNTRY_VERDICT_UNSPECIFIED || len(v.Probes) != 0 || v.Eyeball {
		t.Fatalf("partial without an open port = %v", v)
	}

	c.clock.Advance(time.Minute)
	c.putTSPU("de1", "partial", tport{Port: 443, Verdict: "blocked"}, tport{Port: 8443, Verdict: "open"})
	if v := c.view("de1"); v.Verdict != cvOK || v.Confidence != "dc_only" || v.TspuVerdict != cvOK || len(v.TspuPorts) != 2 {
		t.Fatalf("partial with an open port = %v", v)
	}
	c.clock.Advance(time.Minute)
	c.putTSPU("de1", "blocked", tport{Port: 443, Verdict: "blocked"})
	if v := c.view("de1"); v.Verdict != cvTCP || v.TspuVerdict != cvTCP {
		t.Fatalf("blocked = %v", v)
	}
	c.clock.Advance(time.Minute)
	c.putTSPU("de1", "unknown", tport{Port: 443, Verdict: "timeout"})
	if v := c.view("de1"); v.Verdict != cvUnknown || v.Confidence != "" || len(v.Sources) != 0 || v.TspuVerdict != cvUnknown {
		t.Fatalf("unknown = %v", v)
	}

	// a run so old that the probe node must have been off says nothing now
	c.putTSPU("de1", "blocked", tport{Port: 443, Verdict: "blocked"})
	c.clock.Advance(7 * time.Hour)
	c.noView()

	// off (or Disconnect) hides everything, the rows stay
	c.clock.Advance(-7 * time.Hour)
	c.view("de1")
	c.tspu.On = false
	c.noView()

	// a row of another probe node does not count: the vantage is the chosen one
	c.tspuOn()
	c.tspu.Vantage = "probe2"
	c.noView()
}

// The probe node and Globalping together: agreement keeps the better confidence, a disagreement shows the worse verdict as a
// conflict, and a Globalping round that could not say leaves the probe node's verdict alone.
func TestCountryChecksCombineTheTSPUProbeWithGlobalping(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.tspuOn()
	for _, tc := range []struct {
		name, gp, tspu string
		verdict        adminv1.CountryVerdict
		confidence     string
		sources        []string
		gpVerdict      adminv1.CountryVerdict
	}{
		{"both blocked", "tcp_blocked", "blocked", cvTCP, "eyeball", []string{"globalping", "tspu"}, cvTCP},
		{"both ok", "ok", "open", cvOK, "eyeball", []string{"globalping", "tspu"}, cvOK},
		{"eyeball ok, probe node blocked", "ok", "blocked", cvTCP, "conflict", []string{"globalping", "tspu"}, cvOK},
		{"eyeball blocked, probe node ok", "tcp_blocked", "open", cvTCP, "conflict", []string{"globalping", "tspu"}, cvTCP},
		{"eyeball blocked hard, probe node ok", "blocked", "open", cvBlocked, "conflict", []string{"globalping", "tspu"}, cvBlocked},
		{"globalping cannot say", "few", "blocked", cvTCP, "dc_only", []string{"tspu"}, cvUnknown},
	} {
		c.src.set("de1", tc.gp)
		c.tick() // a round, then +16 minutes
		stored := "blocked"
		ports := []tport{{Port: 443, Verdict: "blocked"}}
		if tc.tspu == "open" {
			stored, ports = "open", []tport{{Port: 443, Verdict: "open"}}
		}
		c.putTSPU("de1", stored, ports...)
		v := c.view("de1")
		if v.Verdict != tc.verdict || v.Confidence != tc.confidence || !slices.Equal(v.Sources, tc.sources) || v.GlobalpingVerdict != tc.gpVerdict {
			t.Errorf("%s: got %v %s %v (globalping %v), want %v %s %v (globalping %v)", tc.name, v.Verdict, v.Confidence, v.Sources, v.GlobalpingVerdict,
				tc.verdict, tc.confidence, tc.sources, tc.gpVerdict)
		}
		if v.TspuVerdict == adminv1.CountryVerdict_COUNTRY_VERDICT_UNSPECIFIED || len(v.TspuPorts) != 1 {
			t.Errorf("%s: the probe node's own view is gone: %v", tc.name, v)
		}
		if tc.name != "globalping cannot say" && len(v.Probes) == 0 {
			t.Errorf("%s: the Globalping probes are gone", tc.name)
		}
		c.clock.Advance(time.Minute)
	}

	// the block lists add detail to the combined verdict and say "listed" only when no live source has a verdict
	c.lists.Store(true)
	c.putList("de1", true, "ExampleHost")
	c.src.set("de1", "few")
	c.tick()
	c.putTSPU("de1", "unknown", tport{Port: 443, Verdict: "timeout"})
	if v := c.view("de1"); v.Verdict != cvListed || v.Confidence != "listed" || !slices.Equal(v.Sources, []string{"lists"}) || len(v.ListedBy) == 0 {
		t.Fatalf("lists with no live verdict = %v", v)
	}
	c.putTSPU("de1", "open", tport{Port: 443, Verdict: "open"})
	if v := c.view("de1"); v.Verdict != cvOK || v.Confidence != "dc_only" || !slices.Equal(v.Sources, []string{"tspu"}) || len(v.ListedBy) == 0 {
		t.Fatalf("a live verdict with a list hit = %v", v)
	}
}

// GetChecks carries the probe node's run next to the per-node rows, while the source is on.
func TestGetChecksCarriesTheTSPURun(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	r, err := rpc{c.s}.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{}))
	if err != nil || r.Msg.TspuRun != nil {
		t.Fatalf("while off: %v, %v", r.Msg.TspuRun, err)
	}
	c.tspuOn()
	c.putTSPU("de1", "open", tport{Port: 443, Verdict: "open"})
	r, err = rpc{c.s}.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{}))
	if err != nil || r.Msg.TspuRun == nil || r.Msg.TspuRun.Status != "ok" || r.Msg.TspuRun.DpiHop != 4 || r.Msg.TspuRun.BlockedRef != "31.13.72.36" {
		t.Fatalf("run = %v, %v", r.Msg.TspuRun, err)
	}
}

// The alert counts a run of the probe node as a round of the node: two bad runs open it with dc_only, an unknown run is
// neutral, two OK runs close it, a single bad run opens nothing.
func TestCountryAlertFromTheTSPUProbeAlone(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.cfg.Enabled = false // Globalping off: only the probe node speaks
	c.tspuOn()
	const k = "country_blocked/de1/"
	run := func(stored string, ports ...tport) {
		c.clock.Advance(2 * time.Hour)
		c.putTSPU("de1", stored, ports...)
		c.evaluate()
	}
	blocked := tport{Port: 443, Verdict: "blocked"}

	run("blocked", blocked)
	mustHave(t, c, k, false) // one bad run
	run("unknown", tport{Port: 443, Verdict: "timeout"})
	mustHave(t, c, k, false) // unknown is neutral
	run("partial", blocked, tport{Port: 8443, Verdict: "refused"})
	mustHave(t, c, k, true) // the second bad run, with an unknown between them
	a := c.active()[k]
	if a.Severity != sevWarning || a.WhyKey != "health.alert.country_blocked.why.tcp_blocked" {
		t.Fatalf("alert = %+v", a)
	}
	for key, val := range map[string]string{"node": "de1", "country": "RU", "verdict": "tcp_blocked", "confidence": "dc_only", "sources": "tspu", "networks": "", "ports": "443"} {
		if a.Params[key] != val {
			t.Errorf("param %s = %q, want %q", key, a.Params[key], val)
		}
	}
	run("open", tport{Port: 443, Verdict: "open"})
	mustHave(t, c, k, true) // one good run is not enough
	run("unknown", tport{Port: 443, Verdict: "timeout"})
	mustHave(t, c, k, true)
	run("open", tport{Port: 443, Verdict: "open"})
	mustHave(t, c, k, false)

	// a node with an open port among blocked ones is reachable: partial is not bad then
	run("partial", blocked, tport{Port: 8443, Verdict: "open"})
	run("partial", blocked, tport{Port: 8443, Verdict: "open"})
	mustHave(t, c, k, false)

	// the rows of a probe node that was switched off, or a run so old that it says nothing, do not keep an alert
	run("blocked", blocked)
	run("blocked", blocked)
	mustHave(t, c, k, true)
	c.tspu.On = false
	c.evaluate()
	mustHave(t, c, k, false) // let go when no live source is on
}

// A run of the probe node that is older than its validity (the probe node was off, or the source was off for a while) is not
// held against the rounds of Globalping that come later.
func TestCountryAlertIgnoresATSPURunThatIsTooOld(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.tspuOn() // a run is good for six hours
	const k = "country_blocked/de1/"
	c.tick()
	c.tick()
	c.putTSPU("de1", "blocked", tport{Port: 443, Verdict: "blocked"})
	c.clock.Advance(7 * time.Hour)
	c.tick()
	c.tick()
	mustHave(t, c, k, false)
	if v := c.view("de1"); v.Verdict != cvOK || v.Confidence != "eyeball" || !slices.Equal(v.Sources, []string{"globalping"}) || v.TspuVerdict != adminv1.CountryVerdict_COUNTRY_VERDICT_UNSPECIFIED {
		t.Fatalf("a stale run is shown: %v", v)
	}
}

// Globalping says ok, the probe node says blocked: the worse verdict is shown as a conflict, and a conflict that two rounds in a
// row show opens the alert (better a false alarm than a miss); it closes after two rounds in which they agree on ok.
func TestCountryAlertOnAConflict(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.tspuOn()
	const k = "country_blocked/de1/"
	c.tick() // Globalping: ok
	c.tick() // Globalping: ok
	mustHave(t, c, k, false)
	c.putTSPU("de1", "blocked", tport{Port: 443, Verdict: "blocked"})
	c.evaluate()
	mustHave(t, c, k, false) // one round in conflict, after an ok one
	c.tick()                 // a Globalping round in the same second as the run is the same round
	mustHave(t, c, k, false)
	c.tick() // the next Globalping round: ok against the run's blocked
	mustHave(t, c, k, true)
	a := c.active()[k]
	for key, val := range map[string]string{"verdict": "tcp_blocked", "confidence": "conflict", "sources": "globalping,tspu", "networks": "Beeline, ER-Telecom, Rostelecom", "ports": "443"} {
		if a.Params[key] != val {
			t.Errorf("param %s = %q, want %q", key, a.Params[key], val)
		}
	}
	// the probe node's next run is ok as well: two rounds in which both agree close it
	c.putTSPU("de1", "open", tport{Port: 443, Verdict: "open"})
	c.tick()
	mustHave(t, c, k, true) // one ok round after the conflicts: held
	c.tick()
	mustHave(t, c, k, false)
}

// One measurement of the probe node is one round, however many Globalping rounds without a verdict follow it.
func TestCountryAlertDoesNotCountOneTSPURunTwice(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.tspuOn()
	c.src.set("de1", "few")
	c.putTSPU("de1", "blocked", tport{Port: 443, Verdict: "blocked"})
	for range 6 {
		c.tick()
	}
	mustHave(t, c, "country_blocked/de1/", false)
}

// A list hit never opens the alert, with the probe node on as well.
func TestCountryAlertNeverOpensFromAListHitWithTheTSPUOn(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.lists.Store(true)
	c.tspuOn()
	c.cfg.Enabled = false
	for range 4 {
		c.putList("de1", true, "ExampleHost")
		c.putTSPU("de1", "unknown", tport{Port: 443, Verdict: "timeout"})
		c.evaluate()
		c.clock.Advance(2 * time.Hour)
	}
	mustHave(t, c, "country_blocked/de1/", false)
	if v := c.view("de1"); v.Verdict != cvListed {
		t.Fatalf("verdict = %v, want LISTED", v.Verdict)
	}
}

// A node that is not connected keeps its alert (held) and gets no new one from probe-node rows.
func TestCountryAlertHeldWhileTheNodeIsOffline(t *testing.T) {
	c := newCountryEnv(t)
	c.onlineNode("de1", "203.0.113.10")
	c.cfg.Enabled = false
	c.tspuOn()
	blocked := tport{Port: 443, Verdict: "blocked"}
	c.putTSPU("de1", "blocked", blocked)
	c.clock.Advance(2 * time.Hour)
	c.putTSPU("de1", "blocked", blocked)
	c.evaluate()
	mustHave(t, c, "country_blocked/de1/", true)
	c.fl.set("de1", liveState{up: false, caps: []string{capDoctor}, lastSeen: c.clock.Now()})
	c.clock.Advance(2 * time.Hour)
	c.putTSPU("de1", "open", tport{Port: 443, Verdict: "open"})
	c.clock.Advance(2 * time.Hour)
	c.putTSPU("de1", "open", tport{Port: 443, Verdict: "open"})
	c.evaluate()
	mustHave(t, c, "country_blocked/de1/", true)
}

// The pure part: the rounds of a node, newest first, each combined with what the other source said at that moment.
func TestCountryEvents(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	round := func(ago time.Duration, verdict string) store.CountryCheck {
		return store.CountryCheck{NodeID: "de1", At: t0.Add(-ago), Country: "RU", Verdict: verdict, Detail: mustJSON(countryDetail{Eyeball: true})}
	}
	run := func(ago time.Duration, stored string, port tport) tspuRound {
		return tspuRounds([]store.TSPUCheck{{NodeID: "de1", At: t0.Add(-ago), Verdict: stored, DetailJSON: mustJSON([]tport{port})}})[0]
	}
	rounds := []store.CountryCheck{round(10*time.Minute, "ok"), round(25*time.Minute, "unknown"), round(40*time.Minute, "tcp_blocked")}
	tspu := []tspuRound{run(30*time.Minute, "blocked", tport{Port: 443, Verdict: "blocked"}), run(5*time.Hour, "open", tport{Port: 443, Verdict: "open"})}
	type ev struct {
		ago                 time.Duration
		verdict, confidence string
		round, tspu         bool
	}
	var have []ev
	for _, e := range countryEvents(rounds, tspu, 4*time.Hour) {
		have = append(have, ev{t0.Sub(e.at), e.combined.Verdict, e.combined.Confidence, e.round != nil, e.tspu != nil})
	}
	// The unknown Globalping round is not a round. Each other measurement is, combined with the newest of the other source at
	// that moment, the probe node's run only while it is no older than maxAge (4 h).
	want := []ev{
		{10 * time.Minute, "tcp_blocked", "conflict", true, true}, // Globalping ok against the probe node's blocked run of 30 minutes ago
		{30 * time.Minute, "tcp_blocked", "eyeball", true, true},  // the run, with the Globalping round then in force (40 minutes ago): they agree
		{40 * time.Minute, "tcp_blocked", "eyeball", true, false}, // the round alone: the only run before it is 4 h 20 min old
		{5 * time.Hour, "ok", "dc_only", false, true},             // the oldest run alone
	}
	if !slices.Equal(have, want) {
		t.Fatalf("events: got %+v, want %+v", have, want)
	}
	// a change of country starts the Globalping history over
	other := round(50*time.Minute, "blocked")
	other.Country = "BY"
	if got := countryEvents(append(slices.Clone(rounds), other), nil, time.Hour); len(got) != 2 {
		t.Errorf("rounds of another country counted: %d events, want 2", len(got))
	}
	if got := countryEvents(nil, nil, time.Hour); len(got) != 0 {
		t.Errorf("events from nothing: %v", got)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
