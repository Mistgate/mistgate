//go:build !js

package warp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	warpreachv1 "github.com/mistgate/mistgate/gen/mistgate/warpreach/v1"
	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// reachNode is a node with a registered, UP WARP, connected, whose agent lists warp-reach/1.
func (e *env) reachNode(name string) string {
	e.t.Helper()
	node := e.addNode(name, "doctor/1", agentcap.Warp, agentcap.WarpReach)
	if _, err := e.register(node); err != nil {
		e.t.Fatal(err)
	}
	e.live[node] = true
	e.warpUp(node)
	return node
}

func (e *env) reachRPC() reachRPC { return reachRPC{e.s} }

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
	if _, err := e.reachRPC().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&warpreachv1.UpdateWarpReachSettingsRequest{Settings: &warpreachv1.WarpReachSettings{Domains: domains}})); err != nil {
		e.t.Fatal(err)
	}
}

func TestReachSettingsDefaultUpdateAndReset(t *testing.T) {
	e := newEnv(t)
	get := func() *warpreachv1.GetWarpReachSettingsResponse {
		r, err := e.reachRPC().GetWarpReachSettings(e.ctx, connect.NewRequest(&warpreachv1.GetWarpReachSettingsRequest{}))
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
	r, err := e.reachRPC().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&warpreachv1.UpdateWarpReachSettingsRequest{UseDefaults: true}))
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
		_, err := e.reachRPC().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&warpreachv1.UpdateWarpReachSettingsRequest{Settings: &warpreachv1.WarpReachSettings{Domains: list}}))
		if c, m := code(err); c != connect.CodeInvalidArgument || m != "reach_domain_invalid" {
			t.Errorf("%s: %v", name, err)
		}
	}
	_, err := e.reachRPC().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&warpreachv1.UpdateWarpReachSettingsRequest{Settings: &warpreachv1.WarpReachSettings{Domains: nine}}))
	e.wantErr(err, connect.CodeInvalidArgument, "reach_domains_too_many")
	_, err = e.reachRPC().UpdateWarpReachSettings(e.ctx, connect.NewRequest(&warpreachv1.UpdateWarpReachSettingsRequest{Settings: &warpreachv1.WarpReachSettings{Domains: []string{"a.example.com", "a.example.com"}}}))
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
	old := e.addNode("de2", "doctor/1", agentcap.Warp)
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

// reachFn is what the fake node answers to WarpReach; the Workers build has no such check and no such type.
type reachFn = func(nodeID string, domains []string) (string, []store.WarpReachItem, error)

func (f *envFleet) WarpReach(_ context.Context, nodeID string, domains []string) (string, []store.WarpReachItem, error) {
	f.mu.Lock()
	f.reachAsked = append(f.reachAsked, append([]string(nil), domains...))
	reach := f.reach
	f.mu.Unlock()
	if reach == nil {
		return "", nil, errors.New("no answer")
	}
	return reach(nodeID, domains)
}

// ---------------------------------------------------------------------------------------------------
// "Check now" of the sites card: GetWarpRequest.check_reach_now

func (e *env) checkNow(node string) *adminv1.WarpReachCheck {
	e.t.Helper()
	g, err := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node, CheckReachNow: true}))
	if err != nil {
		e.t.Fatal(err)
	}
	return g.Msg.ReachCheck
}

func TestCheckReachNowAsksTheNodeAndTheCardReadsTheResult(t *testing.T) {
	e := newEnv(t)
	node := e.reachNode("de1")
	e.setReachDomains("notes.example.com", "chat.example.com")
	if g := e.getWarp(node); g.ReachCheck != nil {
		t.Fatalf("a node nobody has asked has a check: %+v", g.ReachCheck)
	}
	release := make(chan struct{})
	e.mu.Lock()
	e.reach = func(_ string, domains []string) (string, []store.WarpReachItem, error) {
		<-release // the node is opening the sites
		items := make([]store.WarpReachItem, len(domains))
		for i := range items {
			items[i] = store.WarpReachItem{Verdict: "blocked", HTTPStatus: 403, Millis: 90}
		}
		return "AMS", items, nil
	}
	e.mu.Unlock()

	v := e.checkNow(node)
	if v == nil || v.Asked != "started" || !v.Running || v.RetryAfterS != 0 {
		t.Fatalf("the press: %+v", v)
	}
	// the call did not wait: the old result (none) is still there and the check is on, for every read
	if g := e.getWarp(node); g.Reach != nil || g.ReachCheck == nil || !g.ReachCheck.Running || g.ReachCheck.Asked != "" {
		t.Fatalf("while it runs: %+v %+v", g.Reach, g.ReachCheck)
	}
	// a second press while it runs does not ask again
	if v := e.checkNow(node); v.Asked != "running" || !v.Running {
		t.Fatalf("a press during the run: %+v", v)
	}
	close(release)
	e.s.reachWG.Wait()

	g := e.getWarp(node)
	if g.ReachState != "ready" || g.Reach == nil || g.Reach.Colo != "AMS" || g.Reach.Domains[0].Verdict != "blocked" || g.Reach.Domains[0].BlockedStreak != 1 {
		t.Fatalf("after the run: %q %+v", g.ReachState, g.Reach)
	}
	if g.ReachCheck == nil || g.ReachCheck.Running || g.ReachCheck.Last != "ok" || g.ReachCheck.LastUnix != e.now.Unix() {
		t.Fatalf("the check after the run: %+v", g.ReachCheck)
	}
	if asked := e.asked(); len(asked) != 1 {
		t.Fatalf("the node was asked %d times: %v", len(asked), asked)
	}
}

func TestCheckReachNowIsLimitedToOneInTwoMinutes(t *testing.T) {
	e := newEnv(t)
	node := e.reachNode("de1")
	e.setReachDomains("notes.example.com")
	e.answers("FRA", "ok")
	if v := e.checkNow(node); v.Asked != "started" {
		t.Fatalf("first press: %+v", v)
	}
	e.s.reachWG.Wait()

	e.now = e.now.Add(45 * time.Second)
	e.warpUp(node)
	v := e.checkNow(node)
	if v.Asked != "too_soon" || v.RetryAfterS != 75 || v.Running {
		t.Fatalf("a press 45 s later: %+v", v)
	}
	e.s.reachWG.Wait()
	if n := len(e.asked()); n != 1 {
		t.Fatalf("a press that came too soon asked the node (%d runs)", n)
	}
	// the early press does not push the time back
	e.now = e.now.Add(76 * time.Second)
	e.warpUp(node)
	if v := e.checkNow(node); v.Asked != "started" {
		t.Fatalf("a press after two minutes: %+v", v)
	}
	e.s.reachWG.Wait()
	if n := len(e.asked()); n != 2 {
		t.Fatalf("%d runs, want 2", n)
	}
	// another node has its own time
	other := e.reachNode("de2")
	if v := e.checkNow(other); v.Asked != "started" {
		t.Fatalf("another node: %+v", v)
	}
	e.s.reachWG.Wait()
}

func TestCheckReachNowSaysWhyTheNodeCannotBeAsked(t *testing.T) {
	e := newEnv(t)
	node := e.reachNode("de1")
	e.setReachDomains("notes.example.com")
	e.answers("FRA", "ok")
	expect := func(want string) {
		t.Helper()
		e.now = e.now.Add(3 * time.Minute)
		v := e.checkNow(node)
		e.s.reachWG.Wait()
		if v == nil || v.Asked != want || v.Running {
			t.Fatalf("asked = %+v, want %q", v, want)
		}
		if n := len(e.asked()); n != 0 {
			t.Fatalf("the node was asked (%d) although the answer was %q", n, want)
		}
	}
	e.live[node] = false
	expect("node_offline")
	e.live[node] = true
	e.warpUp(node)
	if err := e.s.StoreHealth(e.ctx, node, &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_DOWN}); err != nil {
		t.Fatal(err)
	}
	expect("warp_down")
	e.warpUp(node)
	if _, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: false})); err != nil {
		t.Fatal(err)
	}
	expect("warp_down") // paused
	if _, err := e.rpc().SetWarpEnabled(e.ctx, connect.NewRequest(&adminv1.SetWarpEnabledRequest{NodeId: node, Enabled: true})); err != nil {
		t.Fatal(err)
	}
	e.warpUp(node)
	e.setReachDomains()
	expect("disabled")
	// an agent without the capability is asked nothing
	old := e.addNode("de2", "doctor/1", agentcap.Warp)
	if _, err := e.register(old); err != nil {
		t.Fatal(err)
	}
	e.live[old] = true
	e.warpUp(old)
	e.setReachDomains("notes.example.com")
	if v := e.checkNow(old); v.Asked != "agent_too_old" {
		t.Fatalf("an old agent: %+v", v)
	}
	// none of these started the clock of the two minutes
	e.warpUp(node)
	if v := e.checkNow(node); v.Asked != "started" {
		t.Fatalf("after the refusals: %+v", v)
	}
	e.s.reachWG.Wait()
}

func TestCheckReachNowKeepsHowTheNodeAnswered(t *testing.T) {
	for _, tc := range []struct {
		err  string
		want string
	}{{"warp reach: busy", "busy"}, {"warp reach: too_soon", "too_soon"}, {"warp reach: warp_down", "warp_down"}, {"warp reach: failed", "failed"}} {
		e := newEnv(t)
		node := e.reachNode("de1")
		e.setReachDomains("notes.example.com")
		e.answers("FRA", "ok")
		e.s.CheckReach(e.ctx)
		before := e.getWarp(node).Reach.CheckedUnix
		e.now = e.now.Add(5 * time.Minute)
		e.warpUp(node)
		e.mu.Lock()
		e.reach = func(string, []string) (string, []store.WarpReachItem, error) { return "", nil, errors.New(tc.err) }
		e.mu.Unlock()
		if v := e.checkNow(node); v.Asked != "started" {
			t.Fatalf("%s: %+v", tc.err, v)
		}
		e.s.reachWG.Wait()
		g := e.getWarp(node)
		if g.ReachCheck == nil || g.ReachCheck.Running || g.ReachCheck.Last != tc.want || g.ReachCheck.LastUnix != e.now.Unix() {
			t.Fatalf("%s: the check = %+v", tc.err, g.ReachCheck)
		}
		if g.Reach == nil || g.Reach.CheckedUnix != before {
			t.Fatalf("%s: a node that did not answer changed the result: %+v", tc.err, g.Reach)
		}
	}
}

// The schedule and the button never ask one node at once: a check by hand that is on holds the schedule off the node, and the
// other way round.
func TestCheckReachNowAndTheScheduleDoNotAskTheSameNodeTogether(t *testing.T) {
	e := newEnv(t)
	node := e.reachNode("de1")
	e.setReachDomains("notes.example.com")
	release := make(chan struct{})
	e.mu.Lock()
	e.reach = func(_ string, domains []string) (string, []store.WarpReachItem, error) {
		<-release
		return "FRA", []store.WarpReachItem{{Verdict: "ok", HTTPStatus: 200, Millis: 10}}, nil
	}
	e.mu.Unlock()
	if v := e.checkNow(node); v.Asked != "started" {
		t.Fatalf("press: %+v", v)
	}
	e.s.CheckReach(e.ctx) // the schedule finds the node busy and leaves it
	close(release)
	e.s.reachWG.Wait()
	if n := len(e.asked()); n != 1 {
		t.Fatalf("the node was asked %d times", n)
	}
	// and a check of the schedule that is on is told so
	if !e.s.claimReach(node) {
		t.Fatal("the node is still held after the run")
	}
	e.now = e.now.Add(3 * time.Minute)
	e.warpUp(node)
	if v := e.checkNow(node); v.Asked != "running" || !v.Running {
		t.Fatalf("a press during the schedule check: %+v", v)
	}
	e.s.releaseReach(node, "")
}

func TestCheckReachNowIsIgnoredWithoutAnAccount(t *testing.T) {
	e := newEnv(t)
	node := e.addNode("de1", "doctor/1", agentcap.Warp, agentcap.WarpReach)
	g, err := e.rpc().GetWarp(e.ctx, connect.NewRequest(&adminv1.GetWarpRequest{NodeId: node, CheckReachNow: true}))
	if err != nil || g.Msg.Account != nil || g.Msg.ReachCheck != nil {
		t.Fatalf("a node without WARP: %+v, %v", g, err)
	}
}
