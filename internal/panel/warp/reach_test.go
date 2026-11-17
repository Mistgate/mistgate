package warp

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// reachNode is a node with a registered, UP WARP, connected, whose agent lists warp-reach/1.
func (e *env) reachNode(name string) string {
	e.t.Helper()
	node := e.addNode(name, "doctor/1", CapWarp, CapReach)
	if _, err := e.register(node); err != nil {
		e.t.Fatal(err)
	}
	e.live[node] = true
	e.warpUp(node)
	return node
}

func (e *env) warpUp(node string) {
	e.t.Helper()
	if err := e.s.StoreHealth(e.ctx, node, &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_UP, Colo: "FRA", WarpFlag: "on"}); err != nil {
		e.t.Fatal(err)
	}
}

// answers makes the fake node say verdicts[i] (status 403 for blocked, 200 for ok) for the i-th site asked, from colo.
func (e *env) answers(colo string, verdicts ...string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reach = func(_ string, domains []string) (string, []store.WarpReachItem, error) {
		items := make([]store.WarpReachItem, len(domains))
		for i := range items {
			v := verdicts[i%len(verdicts)]
			items[i] = store.WarpReachItem{Verdict: v, Millis: 100 + uint32(i)}
			switch v {
			case "ok":
				items[i].HTTPStatus = 200
			case "blocked":
				items[i].HTTPStatus = 403
			}
		}
		return colo, items, nil
	}
}

func (e *env) asked() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]string(nil), e.reachAsked...)
}

func (e *env) getWarp(node string) *adminv1.GetWarpResponse {
	e.t.Helper()
	g, err := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node}))
	if err != nil {
		e.t.Fatal(err)
	}
	return g.Msg
}

func (e *env) setReachDomains(domains ...string) {
	e.t.Helper()
	if _, err := e.rpc().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&adminv1.UpdateWarpReachSettingsRequest{Settings: &adminv1.WarpReachSettings{Domains: domains}})); err != nil {
		e.t.Fatal(err)
	}
}

func TestReachSettingsDefaultUpdateAndReset(t *testing.T) {
	e := newEnv(t)
	get := func() *adminv1.GetWarpReachSettingsResponse {
		r, err := e.rpc().GetWarpReachSettings(e.ctx, connect.NewRequest(&adminv1.GetWarpReachSettingsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	g := get()
	if g.Customised || !slices.Equal(g.Settings.Domains, DefaultReachDomains) || !slices.Equal(g.Defaults.Domains, DefaultReachDomains) || len(DefaultReachDomains) > 8 {
		t.Fatalf("defaults = %+v", g)
	}
	for _, d := range DefaultReachDomains {
		if strings.ToLower(d) != d || !strings.Contains(d, ".") {
			t.Errorf("default %q is not a host name", d)
		}
	}

	e.setReachDomains("notes.example.com", "chat.example.com")
	if g = get(); !g.Customised || !slices.Equal(g.Settings.Domains, []string{"notes.example.com", "chat.example.com"}) {
		t.Fatalf("after an update = %+v", g)
	}
	// an empty list is the owner's own: the check is off, and the settings say so (not the built-in list)
	e.setReachDomains()
	if g = get(); !g.Customised || len(g.Settings.Domains) != 0 {
		t.Fatalf("after an empty list = %+v", g)
	}
	r, err := e.rpc().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&adminv1.UpdateWarpReachSettingsRequest{UseDefaults: true}))
	if err != nil || r.Msg.Customised || !slices.Equal(r.Msg.Settings.Domains, DefaultReachDomains) {
		t.Fatalf("use defaults = %+v %v", r, err)
	}
	if g = get(); g.Customised || !slices.Equal(g.Settings.Domains, DefaultReachDomains) {
		t.Fatalf("after use_defaults = %+v", g)
	}
	if n := strings.Count(strings.Join(e.auditActions(), ","), "warp_reach_settings"); n != 3 {
		t.Errorf("audit rows %v, want 3 warp_reach_settings", e.auditActions())
	}
}

func TestReachSettingsRefuseWhatIsNotAHostName(t *testing.T) {
	e := newEnv(t)
	nine := []string{"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com", "f.example.com", "g.example.com", "h.example.com", "i.example.com"}
	for name, list := range map[string][]string{
		"upper case": {"Notes.example.com"}, "an address": {"192.0.2.10"}, "a port": {"notes.example.com:8443"}, "a scheme": {"https://notes.example.com"},
		"a path": {"notes.example.com/x"}, "one label": {"localhost"}, "empty": {""}, "a space": {"a b.example.com"}, "a dash first": {"-a.example.com"},
	} {
		_, err := e.rpc().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&adminv1.UpdateWarpReachSettingsRequest{Settings: &adminv1.WarpReachSettings{Domains: list}}))
		if c, m := code(err); c != connect.CodeInvalidArgument || m != "reach_domain_invalid" {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := e.rpc().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&adminv1.UpdateWarpReachSettingsRequest{Settings: &adminv1.WarpReachSettings{Domains: nine}}))
	e.wantErr(err, connect.CodeInvalidArgument, "reach_domains_too_many")
	_, err = e.rpc().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&adminv1.UpdateWarpReachSettingsRequest{Settings: &adminv1.WarpReachSettings{Domains: []string{"a.example.com", "a.example.com"}}}))
	e.wantErr(err, connect.CodeInvalidArgument, "reach_domain_duplicate")
	if raw, err := e.st.Setting(e.ctx, SettingReach); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a refused list was stored: %q %v", raw, err)
	}
}

func TestCheckReachStoresTheRunAndCountsTheStreak(t *testing.T) {
	e := newEnv(t)
	node := e.reachNode("de1")
	e.setReachDomains("notes.example.com", "chat.example.com", "gone.example.com")

	if g := e.getWarp(node); g.Reach != nil || g.ReachState != "waiting" {
		t.Fatalf("before a run: %+v %q", g.Reach, g.ReachState)
	}
	e.answers("FRA", "blocked", "ok", "unreachable")
	e.s.CheckReach(e.ctx)
	if asked := e.asked(); len(asked) != 1 || !slices.Equal(asked[0], []string{"notes.example.com", "chat.example.com", "gone.example.com"}) {
		t.Fatalf("the node was asked %v", asked)
	}
	g := e.getWarp(node)
	if g.ReachState != "ready" || g.Reach == nil || g.Reach.Colo != "FRA" || g.Reach.CheckedUnix != e.now.Unix() || len(g.Reach.Domains) != 3 {
		t.Fatalf("after one run: %+v %q", g.Reach, g.ReachState)
	}
	d := g.Reach.Domains
	if d[0].Domain != "notes.example.com" || d[0].Verdict != "blocked" || d[0].HttpStatus != 403 || d[0].BlockedStreak != 1 || d[0].LatencyMs != 100 ||
		d[1].Verdict != "ok" || d[1].HttpStatus != 200 || d[1].BlockedStreak != 0 || d[2].Verdict != "unreachable" || d[2].HttpStatus != 0 {
		t.Fatalf("domains after one run: %+v", d)
	}
	if got := e.s.Conditions(e.ctx); len(got) != 0 {
		t.Fatalf("one blocked run opened an alert: %+v", got)
	}

	// the second run in a row opens it, with the colo of the run that said so
	e.now = e.now.Add(15 * time.Minute)
	e.warpUp(node)
	e.answers("AMS", "blocked", "ok", "unreachable")
	e.s.CheckReach(e.ctx)
	conds := e.s.Conditions(e.ctx)
	if len(conds) != 1 || conds[0].Kind != "warp_reach" || conds[0].NodeID != node || conds[0].Subject != "notes.example.com" || conds[0].Severity != 2 ||
		conds[0].Why != "health.alert.warp_reach.why" || conds[0].Params["domain"] != "notes.example.com" || conds[0].Params["colo"] != "AMS" ||
		conds[0].Params["http_status"] != "403" || conds[0].Params["streak"] != "2" {
		t.Fatalf("conditions after two runs: %+v", conds)
	}
	if g = e.getWarp(node); g.Reach.Domains[0].BlockedStreak != 2 || g.Reach.Colo != "AMS" {
		t.Fatalf("GetWarp after two runs: %+v", g.Reach)
	}

	// a run in which the site does not answer says nothing either way: the alert stays and the streak is kept
	e.now = e.now.Add(15 * time.Minute)
	e.warpUp(node)
	e.answers("AMS", "unreachable", "ok", "unreachable")
	e.s.CheckReach(e.ctx)
	if got := e.s.Conditions(e.ctx); len(got) != 1 || got[0].Params["streak"] != "2" || got[0].Params["http_status"] != "0" {
		t.Fatalf("a timeout closed or changed the alert: %+v", got)
	}

	// the next run says ok: the streak and the alert are gone
	e.now = e.now.Add(15 * time.Minute)
	e.warpUp(node)
	e.answers("AMS", "ok")
	e.s.CheckReach(e.ctx)
	if got := e.s.Conditions(e.ctx); len(got) != 0 {
		t.Fatalf("an ok run left %+v", got)
	}
	if g = e.getWarp(node); g.Reach.Domains[0].BlockedStreak != 0 || g.Reach.Domains[0].Verdict != "ok" {
		t.Fatalf("GetWarp after the ok run: %+v", g.Reach)
	}
	// and a block again starts from one
	e.now = e.now.Add(15 * time.Minute)
	e.warpUp(node)
	e.answers("AMS", "blocked", "ok")
	e.s.CheckReach(e.ctx)
	if g = e.getWarp(node); g.Reach.Domains[0].BlockedStreak != 1 || len(e.s.Conditions(e.ctx)) != 0 {
		t.Fatalf("a block after an ok run: %+v", g.Reach)
	}
}

// What stops a run.
func TestCheckReachSkipsWhatCannotAnswer(t *testing.T) {
	e := newEnv(t)
	node := e.reachNode("de1")
	e.setReachDomains("notes.example.com")
	e.answers("FRA", "ok")

	// the node is offline
	e.live[node] = false
	e.s.CheckReach(e.ctx)
	// its WARP is not up (the last report says down)
	e.live[node] = true
	if err := e.s.StoreHealth(e.ctx, node, &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_DOWN}); err != nil {
		t.Fatal(err)
	}
	e.s.CheckReach(e.ctx)
	// its report is old
	e.warpUp(node)
	e.now = e.now.Add(10 * time.Minute)
	e.s.CheckReach(e.ctx)
	e.warpUp(node)
	// WARP is paused
	if _, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: false})); err != nil {
		t.Fatal(err)
	}
	e.s.CheckReach(e.ctx)
	if _, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: true})); err != nil {
		t.Fatal(err)
	}
	e.warpUp(node)
	// the list is empty: the check is off
	e.setReachDomains()
	e.s.CheckReach(e.ctx)
	if asked := e.asked(); len(asked) != 0 {
		t.Fatalf("asked %v although it could not be answered", asked)
	}
	if g := e.getWarp(node); g.ReachState != "disabled" || g.Reach != nil {
		t.Fatalf("with an empty list: %q %+v", g.ReachState, g.Reach)
	}

	// an agent without the capability is told nothing, and GetWarp says why there is no reach
	old := e.addNode("de2", "doctor/1", CapWarp)
	if _, err := e.register(old); err != nil {
		t.Fatal(err)
	}
	e.live[old] = true
	e.warpUp(old)
	e.setReachDomains("notes.example.com")
	e.s.CheckReach(e.ctx)
	if n := len(e.asked()); n != 1 { // only de1
		t.Fatalf("%d runs, want only the node with the capability: %v", n, e.asked())
	}
	g := e.getWarp(old)
	if g.ReachState != "agent_too_old" || g.Reach != nil {
		t.Fatalf("an old agent: %q %+v", g.ReachState, g.Reach)
	}
	// the rest of GetWarp is as it was
	if g.Account == nil || g.Health == nil || !g.AgentSupports {
		t.Fatalf("GetWarp of the old agent lost its other parts: %+v", g)
	}
}

func TestCheckReachKeepsThePreviousResultWhenTheNodeDoesNotAnswer(t *testing.T) {
	e := newEnv(t)
	node := e.reachNode("de1")
	e.setReachDomains("notes.example.com")
	e.answers("FRA", "blocked")
	e.s.CheckReach(e.ctx)
	firstRun := e.now.Unix()

	e.mu.Lock()
	e.reach = nil // the next runs fail
	e.mu.Unlock()
	e.now = e.now.Add(15 * time.Minute)
	e.warpUp(node)
	e.s.CheckReach(e.ctx)
	g := e.getWarp(node)
	if g.Reach == nil || g.Reach.Domains[0].BlockedStreak != 1 || g.Reach.CheckedUnix != firstRun {
		t.Fatalf("a failed run changed the result: %+v", g.Reach)
	}
	// a wrong number of answers is no answer either
	e.mu.Lock()
	e.reach = func(string, []string) (string, []store.WarpReachItem, error) { return "FRA", nil, nil }
	e.mu.Unlock()
	e.s.CheckReach(e.ctx)
	if g = e.getWarp(node); g.Reach.Domains[0].BlockedStreak != 1 || g.Reach.CheckedUnix != firstRun {
		t.Fatalf("a short answer changed the result: %+v", g.Reach)
	}
}

// The alert follows the owner's list and the age of the result: a site taken off the list, an off switch, a paused account or a
// result nobody renews end it.
func TestReachConditionEndsWithItsReasons(t *testing.T) {
	e := newEnv(t)
	node := e.reachNode("de1")
	e.setReachDomains("notes.example.com", "chat.example.com")
	e.answers("FRA", "blocked")
	e.s.CheckReach(e.ctx)
	e.now = e.now.Add(15 * time.Minute)
	e.warpUp(node)
	e.s.CheckReach(e.ctx)
	if got := e.s.Conditions(e.ctx); len(got) != 2 {
		t.Fatalf("two sites blocked twice: %+v", got)
	}
	e.setReachDomains("chat.example.com")
	if got := e.s.Conditions(e.ctx); len(got) != 1 || got[0].Subject != "chat.example.com" {
		t.Fatalf("a site taken off the list: %+v", got)
	}
	e.setReachDomains()
	if got := e.s.Conditions(e.ctx); len(got) != 0 {
		t.Fatalf("the check switched off: %+v", got)
	}
	e.setReachDomains("chat.example.com")
	if len(e.s.Conditions(e.ctx)) != 1 {
		t.Fatal("the condition did not come back with the list")
	}
	// a paused account holds no alert
	if _, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: false})); err != nil {
		t.Fatal(err)
	}
	if got := e.s.Conditions(e.ctx); len(got) != 0 {
		t.Fatalf("a paused account: %+v", got)
	}
	if _, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: true})); err != nil {
		t.Fatal(err)
	}
	// nobody renews the result
	e.now = e.now.Add(reachFresh + time.Minute)
	if got := e.s.Conditions(e.ctx); len(got) != 0 {
		t.Fatalf("a result of %s ago: %+v", reachFresh, got)
	}
}
