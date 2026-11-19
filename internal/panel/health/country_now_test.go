//go:build !js

package health

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
)

// budgetSource is a country source that says what is left of its window.
type budgetSource struct {
	*fakeCountrySource
	mu    sync.Mutex
	left  int
	reset time.Duration
	err   error
	asked int
}

func (b *budgetSource) Remaining(context.Context) (int, time.Duration, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked++
	return b.left, b.reset, b.err
}

func (b *budgetSource) setLeft(n int) {
	b.mu.Lock()
	b.left = n
	b.mu.Unlock()
}

// nowEnv is a country environment with one online node n1, a source that has a budget and the sources besides Globalping.
type nowEnv struct {
	*countryEnv
	src *budgetSource
}

func newNowEnv(t *testing.T) *nowEnv {
	t.Helper()
	c := newCountryEnv(t)
	c.onlineNode("n1", "198.51.100.10")
	c.s.invalidateSnapshot()
	b := &budgetSource{fakeCountrySource: c.src, left: 200, reset: time.Hour}
	c.s.cfg.Country = func(context.Context) (CountryConfig, CountrySource) { return *c.cfg, b }
	return &nowEnv{countryEnv: c, src: b}
}

// wait lets the background work of the round end.
func (n *nowEnv) wait() { n.s.nowWG.Wait() }

func stateOf(v *adminv1.CountryNow, source string) (state, reason string) {
	for _, s := range v.GetSources() {
		if s.Source == source {
			return s.State, s.Reason
		}
	}
	return "", "missing"
}

func TestCountryNowMeasuresTheNodeAndTheScheduleDoesNotMove(t *testing.T) {
	n := newNowEnv(t)
	if !n.s.CountryTick(n.ctx) {
		t.Fatal("the planned round did not run")
	}
	planned, _ := n.st.LastCountryCheckAt(n.ctx, familyV4)
	n.clock.Advance(3 * time.Minute)

	v := n.s.RunCountryNow(n.ctx, "n1")
	if st, why := stateOf(v, countryreach.SourceGlobalping); st != "running" || why != "" {
		t.Fatalf("globalping = %q %q, want running", st, why)
	}
	if !v.Running || v.StartedUnix != n.clock.Now().Unix() || v.NextUnix != n.clock.Now().Add(10*time.Minute).Unix() {
		t.Fatalf("view = %+v", v)
	}
	if v.GlobalpingLeft == nil || v.GlobalpingCost == 0 {
		t.Fatalf("the budget is not told: %+v", v)
	}
	n.wait()
	rounds := n.src.roundsSnapshot()
	if len(rounds) != 2 || len(rounds[1].Targets) != 1 || rounds[1].Targets[0].Key != "n1" {
		t.Fatalf("rounds = %+v", rounds)
	}
	rows := n.rows(familyV4, "n1")
	if len(rows) != 2 || !strings.Contains(rows[0].Detail, `"manual":true`) || strings.Contains(rows[1].Detail, "manual") {
		t.Fatalf("the manual row is not marked: %+v", rows)
	}
	if at, _ := n.st.LastCountryCheckAt(n.ctx, familyV4); !at.Equal(planned) {
		t.Fatalf("the schedule moved: %v, was %v", at, planned)
	}
	got := n.s.countryNowViews(n.ctx, "n1")
	if len(got) != 1 || got[0].Running {
		t.Fatalf("views = %+v", got)
	}
	if st, _ := stateOf(got[0], countryreach.SourceGlobalping); st != "done" {
		t.Fatalf("globalping ended %q", st)
	}
}

func TestCountryNowAsksAtMostOnceInTenMinutes(t *testing.T) {
	n := newNowEnv(t)
	n.s.RunCountryNow(n.ctx, "n1")
	n.wait()
	calls := n.src.calls()

	n.clock.Advance(4 * time.Minute)
	v := n.s.RunCountryNow(n.ctx, "n1")
	if v.RetryAfterS != 6*60 || v.Running {
		t.Fatalf("retry = %d running=%v, want 360 s", v.RetryAfterS, v.Running)
	}
	if len(v.Sources) != len(countryreach.NowSources()) {
		t.Fatalf("sources = %+v", v.Sources)
	}
	for _, s := range v.Sources {
		if s.State != "skipped" || s.Reason != "too_early" {
			t.Fatalf("%s = %s %s", s.Source, s.State, s.Reason)
		}
	}
	n.wait()
	if n.src.calls() != calls {
		t.Fatal("a press that came too early measured again")
	}
	if n.src.asked != 1 {
		t.Fatalf("a press that came too early read the limits of Globalping (%d reads)", n.src.asked)
	}
	// the early press does not push the time of the round back
	n.clock.Advance(6*time.Minute + time.Second)
	if v := n.s.RunCountryNow(n.ctx, "n1"); v.RetryAfterS != 0 || !v.Running {
		t.Fatalf("after ten minutes: %+v", v)
	}
	n.wait()
	// another node has its own time
	n.onlineNode("n2", "198.51.100.11")
	n.s.invalidateSnapshot()
	if v := n.s.RunCountryNow(n.ctx, "n2"); v.RetryAfterS != 0 {
		t.Fatalf("n2 was held by n1: %+v", v)
	}
	n.wait()
}

func TestCountryNowKeepsTheTimeAcrossARestart(t *testing.T) {
	n := newNowEnv(t)
	n.s.RunCountryNow(n.ctx, "n1")
	n.wait()
	// a new process on the same store
	s2 := New(n.st, n.v, n.s.reg, n.fl, n.s.cfg)
	if v := s2.RunCountryNow(n.ctx, "n1"); v.RetryAfterS == 0 {
		t.Fatalf("a restart forgot the last round: %+v", v)
	}
}

func TestCountryNowLeavesTheScheduledBudgetAlone(t *testing.T) {
	n := newNowEnv(t)
	// a plan of one node, three probes: 9 tests a round, a round due now and three more in the hour (every 15 minutes); a round by hand costs 9
	// and a fallback control of 3. 45 are the schedule's.
	n.src.setLeft(45 + 11)
	v := n.s.RunCountryNow(n.ctx, "n1")
	if st, why := stateOf(v, countryreach.SourceGlobalping); st != "skipped" || why != "no_budget" {
		t.Fatalf("globalping = %q %q, want skipped no_budget", st, why)
	}
	if v.GlobalpingLeft == nil || *v.GlobalpingLeft != 11 || v.GlobalpingCost != 12 || v.Running {
		t.Fatalf("view = %+v", v)
	}
	n.wait()
	if n.src.calls() != 0 {
		t.Fatal("Globalping measured without the budget")
	}
	// what the press found is kept for the page, with no time of a round
	if got := n.s.countryNowViews(n.ctx, "n1"); len(got) != 1 || got[0].StartedUnix != 0 || got[0].NextUnix != 0 || got[0].Running {
		t.Fatalf("views = %+v", got)
	} else if st, why := stateOf(got[0], countryreach.SourceGlobalping); st != "skipped" || why != "no_budget" || got[0].GlobalpingLeft == nil {
		t.Fatalf("the skipped source is not kept: %+v", got[0])
	}
	// nothing was asked, so the owner may ask again at once: the guard is for rounds that ran
	n.src.setLeft(45 + 12)
	v = n.s.RunCountryNow(n.ctx, "n1")
	if st, _ := stateOf(v, countryreach.SourceGlobalping); st != "running" {
		t.Fatalf("with the surplus Globalping did not run: %+v", v.Sources)
	}
	n.wait()
	if n.src.calls() != 1 {
		t.Fatalf("calls = %d", n.src.calls())
	}
}

func TestCountryNowReserveShrinksWhenThePlannedRoundIsFarOff(t *testing.T) {
	n := newNowEnv(t)
	if !n.s.CountryTick(n.ctx) {
		t.Fatal("the planned round did not run")
	}
	// the next planned round is 15 minutes off; with 40 minutes to the reset it falls due at 15 and 30: two rounds of 9
	n.src.reset = 40 * time.Minute
	n.src.setLeft(18 + 12)
	v := n.s.RunCountryNow(n.ctx, "n1")
	if st, why := stateOf(v, countryreach.SourceGlobalping); st != "running" {
		t.Fatalf("globalping = %q %q: %+v", st, why, v)
	}
	n.wait()
	// and with 10 minutes to the reset none falls due
	n2 := newNowEnv(t)
	n2.s.CountryTick(n2.ctx)
	n2.src.reset = 10 * time.Minute
	n2.src.setLeft(12)
	if st, why := stateOf(n2.s.RunCountryNow(n2.ctx, "n1"), countryreach.SourceGlobalping); st != "running" {
		t.Fatalf("globalping = %q %q", st, why)
	}
	n2.wait()
}

func TestCountryNowGlobalpingReasons(t *testing.T) {
	n := newNowEnv(t)
	check := func(want string) {
		t.Helper()
		n.clock.Advance(11 * time.Minute)
		v := n.s.RunCountryNow(n.ctx, "n1")
		n.wait()
		if st, why := stateOf(v, countryreach.SourceGlobalping); st != "skipped" || why != want {
			t.Fatalf("globalping = %q %q, want skipped %s", st, why, want)
		}
	}
	n.src.err = errors.New("unreachable")
	check("budget_unknown")
	n.src.err = nil
	n.cfg.Enabled = false
	check("off")
	n.cfg.Enabled = true
	n.fl.set("n1", liveState{up: false})
	check("node_offline")
	n.fl.set("n1", liveState{up: true, caps: []string{capDoctor}, lastSeen: n.clock.Now()})
	n.names["n1.example.com"] = nil
	n.s.invalidateSnapshot()
	check("no_address")
}

func TestCountryNowAskedSourcesAndTheOnesThatAreNot(t *testing.T) {
	n := newNowEnv(t)
	var mu sync.Mutex
	ran := map[string]int{}
	plan := func(source, skip, state, reason string) CountryNowSource {
		return CountryNowSource{Source: source, Plan: func(_ context.Context, nodeID string) (CountryNowRun, string) {
			if skip != "" {
				return nil, skip
			}
			return func(context.Context) (string, string) {
				mu.Lock()
				ran[source+"/"+nodeID]++
				mu.Unlock()
				return state, reason
			}, ""
		}}
	}
	n.s.cfg.CountryNow = []CountryNowSource{
		plan(countryreach.SourceTSPU, "probe_node", "", ""),
		plan(countryreach.SourceCheburcheck, "", "done", ""),
		plan(countryreach.SourceCheburcheckOwn, "held", "", ""),
		plan(countryreach.SourceProbes, "", "queued", ""),
	}
	n.lists.Store(true)
	n.s.cfg.External = func(context.Context) []ExternalState {
		return []ExternalState{{Source: countryreach.SourceBsbord, On: true, MaxAge: time.Hour}}
	}
	v := n.s.RunCountryNow(n.ctx, "n1")
	n.wait()
	if got := len(v.Sources); got != len(countryreach.NowSources()) {
		t.Fatalf("sources = %+v", v.Sources)
	}
	for i, src := range countryreach.NowSources() {
		if v.Sources[i].Source != src.ID {
			t.Fatalf("source %d = %s, want %s", i, v.Sources[i].Source, src.ID)
		}
	}
	want := map[string][2]string{
		countryreach.SourceGlobalping:     {"running", ""},
		countryreach.SourceTSPU:           {"skipped", "probe_node"},
		countryreach.SourceCheburcheck:    {"running", ""},
		countryreach.SourceCheburcheckOwn: {"skipped", "held"},
		countryreach.SourceProbes:         {"running", ""},
		countryreach.SourceBsbord:         {"skipped", "schedule_only"},
		countryreach.SourceLists:          {"reread", ""},
		countryreach.SourceKlick:          {"reread", ""},
	}
	for id, w := range want {
		if st, why := stateOf(v, id); st != w[0] || why != w[1] {
			t.Errorf("%s answered %q %q, want %v", id, st, why, w)
		}
	}
	// what each source ended in, read from GetChecks
	end := n.s.countryNowViews(n.ctx, "n1")[0]
	for id, w := range map[string][2]string{
		countryreach.SourceGlobalping:  {"done", ""},
		countryreach.SourceCheburcheck: {"done", ""},
		countryreach.SourceProbes:      {"queued", ""},
		countryreach.SourceBsbord:      {"skipped", "schedule_only"},
	} {
		if st, why := stateOf(end, id); st != w[0] || why != w[1] {
			t.Errorf("%s ended %q %q, want %v", id, st, why, w)
		}
	}
	if end.Running {
		t.Fatal("the round still runs")
	}
	if ran["cheburcheck/n1"] != 1 || ran["probes/n1"] != 1 || len(ran) != 2 {
		t.Fatalf("ran = %v", ran)
	}
	// everything off: the sources say so and nothing runs
	n2 := newNowEnv(t)
	n2.cfg.Enabled = false
	v = n2.s.RunCountryNow(n2.ctx, "n1")
	n2.wait()
	if v.Running {
		t.Fatalf("running with every source off: %+v", v)
	}
	for _, id := range []string{countryreach.SourceGlobalping, countryreach.SourceTSPU, countryreach.SourceCheburcheck, countryreach.SourceCheburcheckOwn, countryreach.SourceProbes, countryreach.SourceBsbord, countryreach.SourceLists} {
		if st, why := stateOf(v, id); st != "skipped" || why != "off" {
			t.Errorf("%s = %q %q, want skipped off", id, st, why)
		}
	}
}

func TestCountryNowFailureOfASourceIsTold(t *testing.T) {
	n := newNowEnv(t)
	n.cfg.Enabled = false
	n.s.cfg.CountryNow = []CountryNowSource{{Source: countryreach.SourceTSPU, Plan: func(context.Context, string) (CountryNowRun, string) {
		return func(context.Context) (string, string) { return "failed", "control_failed" }, ""
	}}, {Source: countryreach.SourceCheburcheck, Plan: func(context.Context, string) (CountryNowRun, string) {
		return func(context.Context) (string, string) { panic("a source broke") }, ""
	}}}
	n.s.RunCountryNow(n.ctx, "n1")
	n.wait()
	end := n.s.countryNowViews(n.ctx, "n1")[0]
	if st, why := stateOf(end, countryreach.SourceTSPU); st != "failed" || why != "control_failed" {
		t.Fatalf("tspu = %q %q", st, why)
	}
	if st, why := stateOf(end, countryreach.SourceCheburcheck); st != "failed" || why != "error" {
		t.Fatalf("a source that panicked = %q %q", st, why)
	}
}

func TestCountryNowALostRoundReadsAsInterrupted(t *testing.T) {
	n := newNowEnv(t)
	n.s.RunCountryNow(n.ctx, "n1")
	n.wait()
	// the record says running, the process that ran it is gone
	recs := n.s.nowLoad(n.ctx)
	rec := recs["n1"]
	rec.Sources[0].State = "running"
	recs["n1"] = rec
	n.s.nowSave(n.ctx, recs)
	got := n.s.countryNowViews(n.ctx, "n1")[0]
	if got.Running {
		t.Fatal("a round of a process that is gone runs")
	}
	if st, why := stateOf(got, countryreach.SourceGlobalping); st != "failed" || why != "interrupted" {
		t.Fatalf("globalping = %q %q", st, why)
	}
}

func TestRunChecksNowAnswersWithTheCountryRound(t *testing.T) {
	n := newNowEnv(t)
	n.s.cfg.Interval = time.Hour
	r := rpc{n.s}
	res, err := r.RunChecksNow(n.ctx, connect.NewRequest(&adminv1.RunChecksNowRequest{NodeId: "n1"}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.CountryNow == nil || !res.Msg.CountryNow.Running || res.Msg.CountryNow.NodeId != "n1" {
		t.Fatalf("answer = %+v", res.Msg)
	}
	n.wait()
	// the profiles' own limit (30 s) does not refuse the round of the country, and a press that is too early for both is refused as before
	res, err = r.RunChecksNow(n.ctx, connect.NewRequest(&adminv1.RunChecksNowRequest{NodeId: "n1"}))
	if err == nil {
		if res.Msg.CountryNow.RetryAfterS == 0 {
			t.Fatalf("a second press was neither refused nor told to wait: %+v", res.Msg)
		}
	} else if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatal(err)
	}
	// the whole fleet asks for no round of the country
	fleet := newNowEnv(t)
	res, err = rpc{fleet.s}.RunChecksNow(fleet.ctx, connect.NewRequest(&adminv1.RunChecksNowRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Msg.CountryNow != nil || fleet.src.calls() != 0 {
		t.Fatalf("the fleet-wide press asked a country round: %+v", res.Msg.CountryNow)
	}
	// GetChecks tells of the round
	got, err := r.GetChecks(n.ctx, connect.NewRequest(&adminv1.GetChecksRequest{NodeId: "n1"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Msg.CountryNow) != 1 || got.Msg.CountryNow[0].NodeId != "n1" || !slices.ContainsFunc(got.Msg.CountryNow[0].Sources, func(s *adminv1.CountryNowSource) bool { return s.Source == "globalping" && s.State == "done" }) {
		t.Fatalf("GetChecks = %+v", got.Msg.CountryNow)
	}
	if len(n.src.roundsSnapshot()) != 1 {
		t.Fatalf("rounds = %d", len(n.src.roundsSnapshot()))
	}
}

func TestCountryNowNeverCallsASourceWithoutTheRound(t *testing.T) {
	// the schedule alone must not have the panel read Globalping's limits
	n := newNowEnv(t)
	n.s.CountryTick(n.ctx)
	if n.src.asked != 0 {
		t.Fatalf("the planned round read the limits %d times", n.src.asked)
	}
}
