package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

const pUpdateSubs = adminv1connect.SubscriptionServiceUpdateSubscriptionSettingsProcedure

// A custom desktop client, the way an owner adds one: its own scheme in the add link.
var myClient = map[string]any{
	"platform": "macos", "name": "My Client", "kind": "happ",
	"download_url":      "https://github.com/example/myclient/releases/latest/download/myclient-macos.pkg",
	"add_link_template": "myclient://add?url={url_enc}&name={name_enc}",
	"description":       "Hysteria2 in one tap", "recommended": true,
}

func factIn(t *testing.T, facts []Fact, key string) Fact {
	t.Helper()
	for _, f := range facts {
		if f.Key == key {
			return f
		}
	}
	t.Fatalf("no fact %q in %+v", key, facts)
	return Fact{}
}

func storedFacts(t *testing.T, e *testEnv) []Fact {
	t.Helper()
	var facts []Fact
	if err := json.Unmarshal([]byte(e.plans.only().FactsJSON), &facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestSubscriptionSettingsGet(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	out := mustOK(t, e.session(secret), "subscription_settings_get", map[string]any{})
	v := decode[SubsSettingsV](t, out)
	if v.EffectiveTitle != "Example VPN" || v.ServerNameTemplate != subsettings.DefaultNameTemplate || !v.UserPage.PagePassword || !v.UserPage.DeviceSelfService {
		t.Errorf("page settings: %+v", v)
	}
	ios := v.Apps["ios"]
	if len(ios) != 2 || ios[0].Name != "Happ" || ios[0].Kind != "happ" || ios[0].AddLinkTemplate != "happ://add/{url}" || !ios[0].Recommended || ios[1].Kind != "amnezia" {
		t.Errorf("iOS apps: %+v", ios)
	}
	if len(v.Apps["linux"]) != 1 || len(v.Apps) != 5 {
		t.Errorf("apps by platform: %+v", v.Apps)
	}
	// the last wall still applies: a query string reads [redacted]
	if !strings.Contains(out, "play.google.com/store/apps/details?[redacted]") {
		t.Errorf("the Play link was not scrubbed: %s", out)
	}
}

func TestSubscriptionAppAddWaitsForTheOwner(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	before := proto.Clone(e.w.subs).(*adminv1.SubscriptionSettings)

	p := decode[PlanOut](t, mustOK(t, s, "subscription_app_upsert_plan", myClient))
	if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != "user_page" {
		t.Fatalf("plan: %+v", p)
	}
	// the owner's card: every field in full, the agent's values marked as data
	facts := storedFacts(t, e)
	for key, want := range map[string]string{
		"platform": "macOS", "app": "My Client", "download_url": myClient["download_url"].(string),
		"add_link_template": "myclient://add?url={url_enc}&name={name_enc}", "description": "Hysteria2 in one tap", "recommended": "true",
	} {
		if f := factIn(t, facts, key); f.Value != want {
			t.Errorf("fact %s = %q, want %q", key, f.Value, want)
		}
	}
	for _, key := range []string{"app", "download_url", "add_link_template", "description"} {
		if !factIn(t, facts, key).Untrusted {
			t.Errorf("%s is not marked untrusted", key)
		}
	}
	if f := factIn(t, facts, "effect"); f.Code != "app_add" {
		t.Errorf("effect: %+v", f)
	}
	if f := factIn(t, facts, "kind"); f.Code != "happ" {
		t.Errorf("kind: %+v", f)
	}

	if out := mustFail(t, s, "subscription_app_upsert_apply", map[string]any{"confirm_token": p.ConfirmToken}); !strings.Contains(out, "waiting for the owner") {
		t.Errorf("apply before approval: %s", out)
	}
	if len(e.w.subsReq) != 0 {
		t.Fatal("saved before the owner approved")
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	ap := decode[ApplyOut](t, mustOK(t, s, "subscription_app_upsert_apply", map[string]any{"confirm_token": p.ConfirmToken}))
	if ap.Status != StatusApplied || e.plans.only().OutcomeCode != "subscription_app_added" {
		t.Errorf("apply: %+v %s", ap, e.plans.only().OutcomeCode)
	}
	if c := e.w.calls(pUpdateSubs); len(c) != 1 || c[0].Approved != p.PlanID {
		t.Fatalf("the save did not run under the plan's grant: %+v", c)
	}
	// exactly one app more, at the end; nothing else touched
	got := e.w.subs
	added := got.Apps[len(got.Apps)-1]
	want := &adminv1.PlatformApp{Platform: adminv1.Platform_PLATFORM_MACOS, Kind: adminv1.App_APP_HAPP, Name: "My Client",
		DownloadUrl: myClient["download_url"].(string), AddLinkTemplate: "myclient://add?url={url_enc}&name={name_enc}", Description: "Hysteria2 in one tap", Recommended: true}
	if !proto.Equal(added, want) {
		t.Errorf("added %v", added)
	}
	got = proto.Clone(got).(*adminv1.SubscriptionSettings)
	got.Apps = got.Apps[:len(got.Apps)-1]
	if !proto.Equal(got, before) {
		t.Errorf("the rest of the settings changed:\n%v\n%v", got, before)
	}
}

func TestSubscriptionAppUpdateShowsBeforeAndAfter(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	before := proto.Clone(e.w.subs).(*adminv1.SubscriptionSettings)

	// an existing app (ignoring case), only the fields given change
	p := decode[PlanOut](t, mustOK(t, s, "subscription_app_upsert_plan", map[string]any{
		"platform": "ios", "name": "happ", "description": "  Fast and simple  ", "recommended": false,
	}))
	facts := storedFacts(t, e)
	if f := factIn(t, facts, "effect"); f.Code != "app_update" {
		t.Errorf("effect: %+v", f)
	}
	if f := factIn(t, facts, "description"); f.Code != "change" || f.Params["from"] != "" || f.Params["to"] != "Fast and simple" ||
		strings.Join(f.UntrustedParams, ",") != "from,to" || f.Untrusted {
		t.Errorf("description: %+v", f)
	}
	if f := factIn(t, facts, "recommended"); f.Code != "change" || f.Params["from"] != "true" || f.Params["to"] != "false" {
		t.Errorf("recommended: %+v", f)
	}
	for _, f := range facts {
		if f.Key == "download_url" || f.Key == "add_link_template" || f.Key == "kind" {
			t.Errorf("an unchanged field is listed: %+v", f)
		}
	}
	e.plans.decide(p.PlanID, true)
	mustOK(t, s, "subscription_app_upsert_apply", map[string]any{"confirm_token": p.ConfirmToken})
	if e.plans.only().OutcomeCode != "subscription_app_updated" {
		t.Errorf("outcome %s", e.plans.only().OutcomeCode)
	}
	before.Apps[0].Description, before.Apps[0].Recommended = "Fast and simple", false
	if !proto.Equal(e.w.subs, before) {
		t.Errorf("saved:\n%v\nwant:\n%v", e.w.subs, before)
	}
}

func TestSubscriptionAppRemove(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	n := len(e.w.subs.Apps)

	p := decode[PlanOut](t, mustOK(t, s, "subscription_app_remove_plan", map[string]any{"platform": "linux", "name": "AmneziaVPN"}))
	facts := storedFacts(t, e)
	if f := factIn(t, facts, "effect"); f.Code != "app_remove" || factIn(t, facts, "download_url").Value != "https://amnezia.org/downloads" ||
		factIn(t, facts, "kind").Code != "amnezia" || factIn(t, facts, "platform").Value != "Linux" || !p.NeedsApproval {
		t.Errorf("remove plan: %+v %+v", p, facts)
	}
	e.plans.decide(p.PlanID, true)
	mustOK(t, s, "subscription_app_remove_apply", map[string]any{"confirm_token": p.ConfirmToken})
	if len(e.w.subs.Apps) != n-1 {
		t.Fatalf("%d apps", len(e.w.subs.Apps))
	}
	for _, a := range e.w.subs.Apps {
		if a.Platform == adminv1.Platform_PLATFORM_LINUX {
			t.Errorf("still there: %v", a)
		}
	}
	if e.plans.only().OutcomeCode != "subscription_app_removed" {
		t.Errorf("outcome %s", e.plans.only().OutcomeCode)
	}
}

// An edit the owner saved after the plan is never overwritten by the plan's stale copy: the apply refuses and asks for a new plan.
func TestSubscriptionAppApplyRefusesChangedSettings(t *testing.T) {
	for _, tool := range []string{"subscription_app_upsert", "subscription_app_remove"} {
		t.Run(tool, func(t *testing.T) {
			e := newTestEnv(t)
			_, secret := e.token(ProfileOperator)
			s := e.session(secret)
			args := myClient
			if tool == "subscription_app_remove" {
				args = map[string]any{"platform": "ios", "name": "Happ"}
			}
			p := decode[PlanOut](t, mustOK(t, s, tool+"_plan", args))
			e.plans.decide(p.PlanID, true)
			e.w.mu.Lock()
			e.w.subs.Title = "Saved by the owner meanwhile"
			e.w.mu.Unlock()
			if out := mustFail(t, s, tool+"_apply", map[string]any{"confirm_token": p.ConfirmToken}); !strings.Contains(out, "changed after the plan") {
				t.Errorf("apply over a changed document: %s", out)
			}
			if len(e.w.subsReq) != 0 || e.w.subs.Title != "Saved by the owner meanwhile" {
				t.Error("the owner's edit was overwritten")
			}
			if pl := e.plans.only(); pl.Status != StatusFailed || pl.OutcomeCode != "changed_since_plan" {
				t.Errorf("plan after the refusal: %s %s", pl.Status, pl.OutcomeCode)
			}
		})
	}
}

// The plan checks what the admin's save checks, and the agent's text may not hide characters from the owner's card.
func TestSubscriptionAppValidation(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	with := func(kv ...any) map[string]any {
		m := map[string]any{}
		for k, v := range myClient {
			m[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i+1] == nil {
				delete(m, kv[i].(string))
			} else {
				m[kv[i].(string)] = kv[i+1]
			}
		}
		return m
	}
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"platform":         {with("platform", "beos"), "platform: ios, android"},
		"unspecified":      {with("platform", "unspecified"), "platform: ios, android"},
		"kind":             {with("kind", "vless"), "kind: happ"},
		"no kind for new":  {with("kind", nil), "kind is required"},
		"no name":          {with("name", "  "), "name is required"},
		"ftp download":     {with("download_url", "ftp://example.com/x"), "must be an https:// link"},
		"javascript link":  {with("add_link_template", "javascript:alert(1)"), "scheme is not allowed"},
		"data link":        {with("add_link_template", "data:text/html,x"), "scheme is not allowed"},
		"no scheme":        {with("add_link_template", "{url}"), "must start with a scheme"},
		"placeholder":      {with("add_link_template", "myclient://add?u={token}"), "unknown placeholder {token}"},
		"space":            {with("add_link_template", "myclient://add {url}"), "not a valid link template"},
		"long description": {with("description", strings.Repeat("я", 81)), "too long (at most 80"},
		"newline":          {with("description", "two\nlines"), "control characters"},
		"bidi":             {with("download_url", "https://example.com/"+string(rune(0x202E))+"gpj.exe"), "invisible formatting"},
		"zero width":       {with("name", "My"+string(rune(0x200B))+"Client"), "invisible formatting"},
		"unchanged":        {map[string]any{"platform": "ios", "name": "Happ", "recommended": true}, "nothing to change"},
	} {
		if out := mustFail(t, s, "subscription_app_upsert_plan", c.args); !strings.Contains(out, c.want) {
			t.Errorf("%s: %s, want %q", name, out, c.want)
		}
	}
	if out := mustFail(t, s, "subscription_app_remove_plan", map[string]any{"platform": "linux", "name": "Happ"}); !strings.Contains(out, "no app with this name") {
		t.Errorf("remove of a missing app: %s", out)
	}
	// MaxApps, as in the admin
	e.w.mu.Lock()
	for len(e.w.subs.Apps) < subsettings.MaxApps {
		e.w.subs.Apps = append(e.w.subs.Apps, &adminv1.PlatformApp{Platform: adminv1.Platform_PLATFORM_LINUX, Kind: adminv1.App_APP_HAPP, Name: "x"})
	}
	e.w.mu.Unlock()
	if out := mustFail(t, s, "subscription_app_upsert_plan", myClient); !strings.Contains(out, "at most 30") {
		t.Errorf("31st app: %s", out)
	}
	// two apps of one name on one platform: the agent cannot tell them apart
	if out := mustFail(t, s, "subscription_app_remove_plan", map[string]any{"platform": "linux", "name": "X"}); !strings.Contains(out, "several apps") {
		t.Errorf("ambiguous name: %s", out)
	}
	if len(e.plans.byID) != 0 || len(e.w.subsReq) != 0 {
		t.Error("a refused plan was stored or saved")
	}
}

// The choice of DNS per server is a switch of the page like the others: the view shows it (off when the stored document has
// none), an app change carries it through untouched, and the plan's hash covers it, so a switch the owner flips after the
// plan is not overwritten by the plan's stale copy.
func TestSubscriptionDNSChoiceRoundTrips(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	get := func() SubsSettingsV {
		return decode[SubsSettingsV](t, mustOK(t, s, "subscription_settings_get", map[string]any{}))
	}
	if get().UserPage.DNSChoice {
		t.Fatal("a document without the key reads as on")
	}
	e.w.mu.Lock()
	e.w.subs.UserPage.AllowDnsChoice = proto.Bool(true)
	e.w.mu.Unlock()
	if !get().UserPage.DNSChoice {
		t.Fatal("the switch is not shown")
	}

	p := decode[PlanOut](t, mustOK(t, s, "subscription_app_upsert_plan", myClient))
	e.plans.decide(p.PlanID, true)
	mustOK(t, s, "subscription_app_upsert_apply", map[string]any{"confirm_token": p.ConfirmToken})
	if !e.w.subs.UserPage.GetAllowDnsChoice() || !get().UserPage.DNSChoice || len(e.w.subsReq) != 1 || !e.w.subsReq[0].Settings.UserPage.GetAllowDnsChoice() {
		t.Fatalf("the switch did not survive the app change: %v", e.w.subs.UserPage)
	}

	p = decode[PlanOut](t, mustOK(t, s, "subscription_app_remove_plan", map[string]any{"platform": "linux", "name": "AmneziaVPN"}))
	e.plans.decide(p.PlanID, true)
	e.w.mu.Lock()
	e.w.subs.UserPage.AllowDnsChoice = proto.Bool(false) // the owner switches it off meanwhile
	e.w.mu.Unlock()
	if out := mustFail(t, s, "subscription_app_remove_apply", map[string]any{"confirm_token": p.ConfirmToken}); !strings.Contains(out, "changed after the plan") {
		t.Errorf("apply over a flipped switch: %s", out)
	}
	if e.w.subs.UserPage.GetAllowDnsChoice() {
		t.Error("the owner's switch was overwritten")
	}
}
