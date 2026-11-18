//go:build !js

package mcp

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The own routing profiles for Happ and INCY over MCP: the list, the plan that shows the owner what matters (never the rules), and the
// refusals of what the save refuses.

const familyRouting = `{"Name":"family","GlobalProxy":"true","RemoteDNSType":"DoU","RemoteDNSIP":"192.0.2.1","DirectSites":["domain:example.ru","geosite:category-ru"],` +
	`"DirectIp":["geoip:ru"],"ProxySites":["domain:example.org"],"BlockSites":["geosite:category-ads"],"Geoipurl":"https://geo.example.com/geoip.dat","DomainStrategy":"IPIfNonMatch"}`

func routingEnv(t *testing.T, list ...*adminv1.RoutingProfile) *testEnv {
	t.Helper()
	e := newTestEnv(t)
	e.w.groups["grp_router"] = &adminv1.Group{Id: "grp_router", Name: "ROUTER"}
	e.w.groups["grp_pc"] = &adminv1.Group{Id: "grp_pc", Name: "PC"}
	e.w.subs.RoutingProfiles = list
	return e
}

func routingEntry(profile string, onlyAdd bool, groups ...string) *adminv1.RoutingProfile {
	return &adminv1.RoutingProfile{Profile: profile, OnlyAdd: onlyAdd, OnlyGroups: len(groups) > 0, GroupIds: groups}
}

func factsOfPlan(t *testing.T, e *testEnv, id string) []Fact {
	t.Helper()
	var facts []Fact
	if err := json.Unmarshal([]byte(e.plans.byID[id].FactsJSON), &facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func canonicalRouting(t *testing.T, in string) string {
	t.Helper()
	p, err := subsettings.ParseRoutingInput(in)
	if err != nil {
		t.Fatal(err)
	}
	return p.Doc.Canonical()
}

func TestSettingsGetListsRoutingProfiles(t *testing.T) {
	e := routingEnv(t)
	e.w.subs.RoutingProfiles = []*adminv1.RoutingProfile{routingEntry(canonicalRouting(t, familyRouting), true, "grp_router")}
	_, secret := e.token(ProfileReadonly)
	v := decode[SubsSettingsV](t, mustOK(t, e.session(secret), "subscription_settings_get", map[string]any{}))
	if len(v.RoutingProfiles) != 1 {
		t.Fatalf("profiles: %+v", v.RoutingProfiles)
	}
	p := v.RoutingProfiles[0]
	if p.Name != "family" || p.Mode != "add_only" || p.AllGroups || len(p.GroupIDs) != 1 || p.DirectSites != 2 || p.DirectIP != 1 || p.ProxySites != 1 || p.BlockSites != 1 ||
		!p.GeoFiles || p.InBody || p.RemoteDNS != "DoU 192.0.2.1" || len(p.SHA256) != 12 || !strings.Contains(p.Profile, "domain:example.ru") {
		t.Errorf("profile: %+v", p)
	}
	e.w.subs.RoutingProfiles = nil
	if out := mustOK(t, e.session(secret), "subscription_settings_get", map[string]any{}); !strings.Contains(out, `"routing_profiles":[]`) {
		t.Errorf("no profiles: %s", out)
	}
}

func TestSubscriptionPreviewNamesTheRoutingProfile(t *testing.T) {
	e := routingEnv(t, routingEntry(canonicalRouting(t, familyRouting), false, "grp_router"))
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	alice := e.w.users["usr_alice"].User
	use := func() *routingUseV {
		return decode[SubscriptionPreviewV](t, mustOK(t, s, "subscription_preview", map[string]any{"user_id": "usr_alice"})).Routing
	}
	if u := use(); u == nil || u.Profile != "" || u.Reason != "no_profile_for_group" {
		t.Errorf("a group without a profile: %+v", u)
	}
	alice.GroupId = "grp_router"
	if u := use(); u == nil || u.Profile != "family" || u.Reason != "" {
		t.Errorf("a group with a profile: %+v", u)
	}
	alice.Status = adminv1.UserStatus_USER_STATUS_EXPIRED
	if u := use(); u.Profile != "" || u.Reason != "no_access" {
		t.Errorf("an expired person: %+v", u)
	}
}

func TestRoutingSetCreatesUpdatesAndRemoves(t *testing.T) {
	e := routingEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	apply := func(args map[string]any) PlanOut {
		t.Helper()
		p := decode[PlanOut](t, mustOK(t, s, "subscription_routing_set_plan", args))
		if err := e.plans.decide(p.PlanID, true); err != nil {
			t.Fatal(err)
		}
		mustOK(t, s, "subscription_routing_set_apply", map[string]any{"confirm_token": p.ConfirmToken})
		return p
	}
	link := "incy://routing/add/" + base64.StdEncoding.EncodeToString([]byte(familyRouting))
	p := decode[PlanOut](t, mustOK(t, s, "subscription_routing_set_plan", map[string]any{"name": "Kids", "profile": link, "group_ids": []string{"grp_pc"}}))
	facts := factsOfPlan(t, e, p.PlanID)
	if f := factIn(t, facts, "routing_profile"); f.Value != "Kids" {
		t.Errorf("name: %+v", f) // the name of the call is the name of the profile, whatever the pasted JSON says
	}
	if f := factIn(t, facts, "mode"); f.Code != "routing_add" {
		t.Errorf("a link that says add: %+v", f)
	}
	if f := factIn(t, facts, "rules"); f.Params["direct_sites"] != "2" || f.Params["proxy_sites"] != "1" || f.Params["block_ip"] != "0" {
		t.Errorf("rules: %+v", f)
	}
	if f := factIn(t, facts, "download_hosts"); f.Value != "geo.example.com" {
		t.Errorf("hosts: %+v", f)
	}
	if f := factIn(t, facts, "delivery"); f.Code != "routing_in_header" {
		t.Errorf("delivery: %+v", f)
	}
	for _, f := range facts { // the rules themselves are never in the card
		if strings.Contains(f.Value, "example.ru") || strings.Contains(f.Value, "category-ads") {
			t.Errorf("the card carries a rule: %+v", f)
		}
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s, "subscription_routing_set_apply", map[string]any{"confirm_token": p.ConfirmToken})
	got := e.w.subs.RoutingProfiles
	if len(got) != 1 || !got[0].OnlyAdd || !got[0].OnlyGroups || got[0].GroupIds[0] != "grp_pc" || !strings.HasPrefix(got[0].Profile, `{"Name":"Kids"`) {
		t.Fatalf("after create: %+v", got)
	}

	// only the mode and all groups: the rules stay; the card shows the change
	p = decode[PlanOut](t, mustOK(t, s, "subscription_routing_set_plan", map[string]any{"name": "kids", "mode": "add_and_turn_on", "all_groups": true}))
	if f := factIn(t, factsOfPlan(t, e, p.PlanID), "mode"); f.Code != "change" {
		t.Errorf("mode change: %+v", f)
	}
	if f := factIn(t, factsOfPlan(t, e, p.PlanID), "groups"); f.Code != "all_groups" {
		t.Errorf("groups: %+v", f)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s, "subscription_routing_set_apply", map[string]any{"confirm_token": p.ConfirmToken})
	got = e.w.subs.RoutingProfiles
	if got[0].OnlyAdd || got[0].OnlyGroups || len(got[0].GroupIds) != 0 || !strings.Contains(got[0].Profile, "example.ru") {
		t.Fatalf("after the change: %+v", got[0])
	}

	// new rules, found by the name ignoring case
	apply(map[string]any{"name": "KIDS", "profile": strings.Replace(familyRouting, "example.ru", "example.su", 1)})
	if len(e.w.subs.RoutingProfiles) != 1 || !strings.Contains(e.w.subs.RoutingProfiles[0].Profile, "example.su") || !strings.HasPrefix(e.w.subs.RoutingProfiles[0].Profile, `{"Name":"Kids"`) {
		t.Fatalf("after the rules change: %+v", e.w.subs.RoutingProfiles)
	}
	apply(map[string]any{"name": "kids", "remove": true})
	if len(e.w.subs.RoutingProfiles) != 0 {
		t.Fatalf("after the removal: %+v", e.w.subs.RoutingProfiles)
	}
}

func TestRoutingSetRefusals(t *testing.T) {
	e := routingEnv(t, routingEntry(canonicalRouting(t, familyRouting), false, "grp_router"))
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"nothing to change":     {map[string]any{"name": "family", "profile": familyRouting, "group_ids": []string{"grp_router"}}, "nothing to change"},
		"no field":              {map[string]any{"name": "family"}, "give profile"},
		"remove with a field":   {map[string]any{"name": "family", "remove": true, "mode": "add_only"}, "stands alone"},
		"remove what is not":    {map[string]any{"name": "ghost", "remove": true}, "no routing profile"},
		"a new one w/o profile": {map[string]any{"name": "ghost", "group_ids": []string{"grp_pc"}}, "needs profile"},
		"no name":               {map[string]any{"name": " ", "profile": familyRouting}, "name is required"},
		"garbage":               {map[string]any{"name": "x", "profile": "hello, world!"}, "not a link"},
		"empty object":          {map[string]any{"name": "x", "profile": "{}"}, "no routing field"},
		"wrong type":            {map[string]any{"name": "x", "profile": `{"DirectSites":"a"}`}, "DirectSites"},
		"http geo file":         {map[string]any{"name": "x", "profile": `{"Geoipurl":"http://geo.example.com/g.dat"}`}, "https"},
		"off link":              {map[string]any{"name": "x", "profile": "happ://routing/off"}, "turns routing off"},
		"too big":               {map[string]any{"name": "x", "profile": `{"DirectSites":["` + strings.Repeat("a", 250) + `"` + strings.Repeat(`,"`+strings.Repeat("b", 250)+`"`, 140) + `]}`}, "at most"},
		"unknown group":         {map[string]any{"name": "x", "profile": familyRouting, "group_ids": []string{"grp_nosuch"}}, "no group with id"},
		"a group of another":    {map[string]any{"name": "x", "profile": familyRouting, "group_ids": []string{"grp_router"}}, "already in the profile"},
		"both group switches":   {map[string]any{"name": "x", "profile": familyRouting, "group_ids": []string{"grp_pc"}, "all_groups": true}, "give one of them"},
		"false without groups":  {map[string]any{"name": "x", "profile": familyRouting, "all_groups": false}, "needs group_ids"},
		"a bad mode":            {map[string]any{"name": "x", "profile": familyRouting, "mode": "maybe"}, "add_and_turn_on or add_only"},
		"an invisible char":     {map[string]any{"name": "x", "profile": familyRouting + " ‮"}, "invisible"},
		"an invisible name":     {map[string]any{"name": "a​b", "profile": familyRouting}, "invisible"},
	} {
		if out := mustFail(t, s, "subscription_routing_set_plan", c.args); !strings.Contains(out, c.want) {
			t.Errorf("%s: %s", name, out)
		}
		if len(e.plans.byID) != 0 {
			t.Fatalf("%s created a plan", name)
		}
	}
}

// A profile read through the scrubber and written back would carry its markers.
func TestRoutingSetRefusesTheScrubbersMarkers(t *testing.T) {
	e := routingEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	out := mustFail(t, s, "subscription_routing_set_plan", map[string]any{"name": "x", "profile": strings.Replace(familyRouting, "domain:example.ru", "[redacted]", 1)})
	if !strings.Contains(out, "template_has_redaction_markers") {
		t.Errorf("%s", out)
	}
}

func TestRoutingSetFollowsTheSettingsHash(t *testing.T) {
	e := routingEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	p := decode[PlanOut](t, mustOK(t, s, "subscription_routing_set_plan", map[string]any{"name": "family", "profile": familyRouting, "all_groups": true}))
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	e.w.subs.Title = "changed meanwhile"
	if out := mustFail(t, s, "subscription_routing_set_apply", map[string]any{"confirm_token": p.ConfirmToken}); !strings.Contains(out, "changed_since_plan") && !strings.Contains(out, "changed after the plan") {
		t.Errorf("a stale plan: %s", out)
	}
	if len(e.w.subs.RoutingProfiles) != 0 {
		t.Error("the stale plan was applied")
	}
}
