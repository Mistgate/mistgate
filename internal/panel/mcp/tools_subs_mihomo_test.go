//go:build !js

package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The own Mihomo templates over MCP: the list without text, the reading in windows, the plan that shows the owner what matters
// and never the text, and the refusal of a text that was cut by the scrubber.

const routerTemplate = `# Router: the panel puts the servers into proxies and adds Auto.
mixed-port: 7890
proxy-groups:
  - name: Proxy
    type: select
    include-all-proxies: true
    proxies: [Auto, DIRECT]
rule-providers:
  ads:
    type: http
    behavior: domain
    format: mrs
    url: https://rules.example.com/ads.mrs
    interval: 86400
rules:
  - RULE-SET,ads,REJECT
  - MATCH,Proxy
`

// pcTemplate has an own dns section (a warning) and a rule that names nothing the template defines (another).
const pcTemplate = `mixed-port: 7890
dns:
  enable: true
  nameserver: [1.1.1.1]
proxy-groups:
  - name: Proxy
    type: select
    include-all-proxies: true
rules:
  - DOMAIN-SUFFIX,example.com,NoSuchGroup
  - MATCH,Proxy
`

// bigTemplate is a template of nearly 64 KiB whose JSON text is longer than the text: comment lines of quotes and angle brackets.
func bigTemplate() string {
	var b strings.Builder
	b.WriteString("mixed-port: 7890\nproxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\nrules:\n")
	for i := 0; b.Len() < 58<<10; i++ {
		fmt.Fprintf(&b, "  - DOMAIN-SUFFIX,host%d.example.com,DIRECT\n", i)
	}
	for i := 0; b.Len() < subsettings.MaxMihomoTemplateBytes-200; i++ {
		b.WriteString("# " + strings.Repeat(`"`, 60) + "\n")
	}
	b.WriteString("  - MATCH,Proxy\n")
	if b.Len() > subsettings.MaxMihomoTemplateBytes {
		panic("the test template is over the limit")
	}
	return b.String()
}

func mihomoEnv(t *testing.T, templates ...*adminv1.MihomoTemplate) *testEnv {
	t.Helper()
	e := newTestEnv(t)
	e.w.groups["grp_router"] = &adminv1.Group{Id: "grp_router", Name: "ROUTER"}
	e.w.groups["grp_pc"] = &adminv1.Group{Id: "grp_pc", Name: "PC"}
	e.w.subs.MihomoTemplates = templates
	return e
}

func tpl(name, text string, groups ...string) *adminv1.MihomoTemplate {
	return &adminv1.MihomoTemplate{Name: name, Yaml: text, GroupIds: groups}
}

func TestSettingsGetListsTemplatesWithoutText(t *testing.T) {
	e := mihomoEnv(t, tpl("ROUTER", routerTemplate, "grp_router"), tpl("PC", pcTemplate, "grp_pc"), tpl("spare", routerTemplate))
	_, secret := e.token(ProfileReadonly)
	out := mustOK(t, e.session(secret), "subscription_settings_get", map[string]any{})
	for _, secretText := range []string{"include-all-proxies", "rules.example.com", "DOMAIN-SUFFIX", "mixed-port"} {
		if strings.Contains(out, secretText) {
			t.Fatalf("the settings read carries template text (%q): %s", secretText, out)
		}
	}
	v := decode[SubsSettingsV](t, out)
	if len(v.MihomoTemplates) != 3 {
		t.Fatalf("templates: %+v", v.MihomoTemplates)
	}
	router, pc, spare := v.MihomoTemplates[0], v.MihomoTemplates[1], v.MihomoTemplates[2]
	if router.Name != "ROUTER" || router.Bytes != len(routerTemplate) || len(router.SHA256) != 12 || router.SHA256 != hashText(routerTemplate)[:12] ||
		len(router.GroupIDs) != 1 || router.GroupIDs[0] != "grp_router" || router.Warnings != 0 {
		t.Errorf("router: %+v", router)
	}
	if pc.Warnings != 2 || !strings.Contains(strings.Join(pc.WarningCodes, " "), "own_dns") || !strings.Contains(strings.Join(pc.WarningCodes, " "), "unknown_rule_target:NoSuchGroup") {
		t.Errorf("pc warnings: %+v", pc)
	}
	if spare.Warnings != 1 || spare.WarningCodes[0] != "unused" {
		t.Errorf("a template of no group warns unused: %+v", spare)
	}
	// no template: an empty list, not null
	e.w.subs.MihomoTemplates = nil
	if out := mustOK(t, e.session(secret), "subscription_settings_get", map[string]any{}); !strings.Contains(out, `"mihomo_templates":[]`) {
		t.Errorf("no templates: %s", out)
	}
}

func TestSubscriptionPreviewNamesTheTemplate(t *testing.T) {
	e := mihomoEnv(t, tpl("ROUTER", routerTemplate, "grp_router"))
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	alice := e.w.users["usr_alice"].User
	use := func() *mihomoUseV {
		return decode[SubscriptionPreviewV](t, mustOK(t, s, "subscription_preview", map[string]any{"user_id": "usr_alice"})).Mihomo
	}
	if u := use(); u == nil || u.Template != "" || u.Reason != "no_template_for_group" {
		t.Errorf("a group without a template: %+v", u)
	}
	alice.GroupId = "grp_router"
	if u := use(); u == nil || u.Template != "ROUTER" || u.Reason != "" {
		t.Errorf("a group with a template: %+v", u)
	}
	alice.Status = adminv1.UserStatus_USER_STATUS_EXPIRED
	if u := use(); u.Template != "" || u.Reason != "no_access" {
		t.Errorf("an expired person: %+v", u)
	}
	alice.Status = adminv1.UserStatus_USER_STATUS_ACTIVE
	e.w.users["usr_alice"].NodeAccess = nil
	if u := use(); u.Template != "" || u.Reason != "no_servers" {
		t.Errorf("a person with no server: %+v", u)
	}
}

// The windows of a read, put together, are the template; each result is far under the cap even when the text has to be escaped.
func TestTemplateReadInWindows(t *testing.T) {
	big := bigTemplate()
	e := mihomoEnv(t, tpl("Big", big, "grp_pc"))
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	var got strings.Builder
	offset, windows := 0, 0
	for {
		out := mustOK(t, s, "subscription_mihomo_template_get", map[string]any{"name": "big", "offset": offset})
		if len(out) > 16<<10 { // the assistant clips a tool result at 16384 characters
			t.Fatalf("a window is %d bytes", len(out))
		}
		w := decode[MihomoTemplateTextV](t, out)
		if w.Name != "Big" || w.Bytes != len(big) || w.SHA256 != hashText(big)[:12] || w.Offset != offset || w.Lossy {
			t.Fatalf("window %+v", w)
		}
		got.WriteString(w.Text)
		windows++
		if w.NextOffset == 0 {
			break
		}
		if w.NextOffset <= offset {
			t.Fatalf("next_offset %d does not move on from %d", w.NextOffset, offset)
		}
		offset = w.NextOffset
	}
	if got.String() != big {
		t.Errorf("the windows (%d) do not make the template: %d bytes of %d", windows, got.Len(), len(big))
	}
	if windows < 3 {
		t.Errorf("a 64 KiB template came in %d windows", windows)
	}
	for _, args := range []map[string]any{{"name": "nope"}, {"name": "Big", "offset": -1}, {"name": "Big", "offset": len(big)}, {"name": "Big", "offset": 1 << 40}} {
		mustFail(t, s, "subscription_mihomo_template_get", args)
	}
}

func TestTemplateReadMarksWhatTheScrubberCut(t *testing.T) {
	text := strings.Replace(routerTemplate, "https://rules.example.com/ads.mrs", "https://rules.example.com/ads.mrs?token=abc123", 1)
	e := mihomoEnv(t, tpl("ROUTER", text, "grp_router"))
	_, secret := e.token(ProfileReadonly)
	w := decode[MihomoTemplateTextV](t, mustOK(t, e.session(secret), "subscription_mihomo_template_get", map[string]any{"name": "router"}))
	if !w.Lossy || !strings.Contains(w.Text, "[redacted]") || strings.Contains(w.Text, "abc123") || !strings.Contains(w.Note, "do not send") {
		t.Errorf("window: %+v", w)
	}
}

func TestWindowBudgetCountsTheEscaping(t *testing.T) {
	// one line of 100000 quotes, no line end: cut at a character, every window inside the budget
	text := strings.Repeat(`"`, 100000)
	offset := 0
	for n := 0; offset < len(text); n++ {
		w, next := mihomoWindow(text, offset)
		if len(w) > mihomoWindowBytes/2 || (next != 0 && len(w) != mihomoWindowBytes/2) || n > 20 {
			t.Fatalf("window %d of %d bytes", n, len(w))
		}
		if next == 0 {
			break
		}
		offset = next
	}
	if w, next := mihomoWindow("a\nb\n", 0); w != "a\nb\n" || next != 0 {
		t.Errorf("a short text is one window: %q %d", w, next)
	}
}

// The plan shows the owner the name, groups, size, warnings and hosts, and not the text; the apply saves what the plan said.
func TestTemplateSetPlanShowsHostsAndNotTheText(t *testing.T) {
	text := routerTemplate + "# lists: https://user:pw@Lists.Example.org/geo/ads.mrs and http://cdn.example.net:8443/x.mrs and https://rules.example.com/again\n"
	logs := captureLogs(t)
	e := mihomoEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	p := decode[PlanOut](t, mustOK(t, s, "subscription_mihomo_template_set_plan", map[string]any{"name": "ROUTER", "yaml": text, "group_ids": []string{"grp_router"}, "reason": "split the router out"}))
	if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != "user_page" {
		t.Fatalf("plan: %+v", p)
	}
	facts := storedFacts(t, e)
	for key, want := range map[string]string{
		"template": "ROUTER", "action": "create", "groups": "ROUTER", "bytes": fmt.Sprint(len(text)), "sha256_12": hashText(text)[:12],
		"hosts": "cdn.example.net:8443, lists.example.org, rules.example.com",
	} {
		if got := factIn(t, facts, key).Value; got != want {
			t.Errorf("fact %s = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{"template", "groups", "hosts"} {
		if !factIn(t, facts, key).Untrusted {
			t.Errorf("%s is not marked untrusted", key)
		}
	}
	// nothing of the text, nor the credentials in a URL, in what the agent, the owner or the audit gets
	planJSON, _ := json.Marshal(p)
	factsJSON, _ := json.Marshal(facts)
	for what, doc := range map[string]string{"plan result": string(planJSON), "stored facts": string(factsJSON), "summary": e.plans.only().Summary} {
		for _, leak := range []string{"include-all-proxies", "mixed-port", "RULE-SET", "user:pw"} {
			if strings.Contains(doc, leak) {
				t.Errorf("%s carries %q: %s", what, leak, doc)
			}
		}
	}
	if len(planJSON) > maxResultBytes {
		t.Errorf("the plan is %d bytes", len(planJSON))
	}
	if len(e.w.subsReq) != 0 {
		t.Fatal("the plan saved something")
	}
	if out := mustFail(t, s, "subscription_mihomo_template_set_apply", map[string]any{"confirm_token": p.ConfirmToken}); !strings.Contains(out, "waiting for the owner") {
		t.Errorf("apply before approval: %s", out)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	a := decode[ApplyOut](t, mustOK(t, s, "subscription_mihomo_template_set_apply", map[string]any{"confirm_token": p.ConfirmToken}))
	if a.Status != StatusApplied || len(e.w.subs.MihomoTemplates) != 1 || e.w.subs.MihomoTemplates[0].Yaml != text || e.w.subs.MihomoTemplates[0].GroupIds[0] != "grp_router" {
		t.Fatalf("apply %+v, stored %+v", a, e.w.subs.MihomoTemplates)
	}
	if e.plans.only().OutcomeCode != "subscription_mihomo_template_updated" {
		t.Errorf("outcome %q", e.plans.only().OutcomeCode)
	}
	// the text is in the stored plan only (the apply needs it), never in the audit log or the panel's log
	for _, entry := range e.audits("mcp_plan") {
		b, _ := json.Marshal(entry)
		for _, leak := range []string{"include-all-proxies", "RULE-SET", "rules.example.com"} {
			if strings.Contains(string(b), leak) {
				t.Errorf("the audit entry carries the text (%s): %s", leak, b)
			}
		}
	}
	for _, entry := range append(e.audits("mcp_plan"), e.audits("mcp_apply")...) {
		b, _ := json.Marshal(entry)
		if strings.Contains(string(b), "mixed-port") {
			t.Errorf("an audit entry carries the text: %s", b)
		}
	}
	if strings.Contains(logs.String(), "mixed-port") || strings.Contains(logs.String(), "include-all-proxies") {
		t.Errorf("the log carries the text: %s", logs.String())
	}
}

func TestMihomoTemplateConditionalWarningsAndHosts(t *testing.T) {
	text := strings.Replace(routerTemplate, "  - MATCH,Proxy\n", `# @mistgate-if ["grp_router","grp_pc"]
  - DOMAIN-SUFFIX,assigned.example,AssignedMissing
# @mistgate-end
# @mistgate-if ["grp_router"]
  - DOMAIN-SUFFIX,router.example,RouterMissing
# @mistgate-end
  - DOMAIN-SUFFIX,common.example,CommonMissing
  - MATCH,Proxy
`, 1) + `# @mistgate-if ["grp_router"]
dns:
  enable: true
  nameserver: ["https://router.example/dns-query"]
# @mistgate-end
# @mistgate-if ["grp_unassigned"]
dns:
  enable: true
  nameserver: ["https://unused.example/dns-query"]
rules:
  - MATCH,UnassignedMissing
# @mistgate-end
`
	e := mihomoEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	p := decode[PlanOut](t, mustOK(t, s, "subscription_mihomo_template_set_plan", map[string]any{
		"name": "Shared", "yaml": text, "group_ids": []string{"grp_router", "grp_pc"}, "reason": "add group variants",
	}))
	facts := storedFacts(t, e)
	if got := factIn(t, facts, "hosts").Value; got != "router.example, rules.example.com, unused.example" {
		t.Errorf("plan hosts = %q", got)
	}
	if got := factIn(t, facts, "sha256_12").Value; got != hashText(text)[:12] {
		t.Errorf("plan hash = %q", got)
	}
	checkWarnings := func(got []string) {
		t.Helper()
		counts := map[string]int{}
		for _, w := range got {
			counts[w]++
		}
		if len(got) != 4 || counts["own_dns"] != 1 || counts["unknown_rule_target:CommonMissing"] != 1 ||
			counts["unknown_rule_target:AssignedMissing"] != 1 || counts["unknown_rule_target:RouterMissing"] != 1 {
			t.Errorf("warnings: %v", got)
		}
	}
	checkWarnings(strings.Split(factIn(t, facts, "warnings").Value, ", "))
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s, "subscription_mihomo_template_set_apply", map[string]any{"confirm_token": p.ConfirmToken})
	list := decode[SubsSettingsV](t, mustOK(t, s, "subscription_settings_get", map[string]any{}))
	if len(list.MihomoTemplates) != 1 || list.MihomoTemplates[0].Warnings != 4 {
		t.Fatalf("settings list: %+v", list.MihomoTemplates)
	}
	checkWarnings(list.MihomoTemplates[0].WarningCodes)
	read := decode[MihomoTemplateTextV](t, mustOK(t, s, "subscription_mihomo_template_get", map[string]any{"name": "Shared"}))
	if read.Text != text || read.SHA256 != hashText(text)[:12] || read.Lossy {
		t.Fatal("the template read did not return the complete canonical source")
	}
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

// A template of 64 KiB goes through the plan: the owner's card stays small, and the arguments fit the stored plan.
func TestTemplateSetPlanOfALargeTemplate(t *testing.T) {
	big := bigTemplate()
	e := mihomoEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	out := mustOK(t, s, "subscription_mihomo_template_set_plan", map[string]any{"name": "Big", "yaml": big, "group_ids": []string{"grp_pc"}})
	if len(out) > maxResultBytes/4 {
		t.Errorf("the plan of a 64 KiB template is %d bytes", len(out))
	}
	p := decode[PlanOut](t, out)
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s, "subscription_mihomo_template_set_apply", map[string]any{"confirm_token": p.ConfirmToken})
	if len(e.w.subs.MihomoTemplates) != 1 || e.w.subs.MihomoTemplates[0].Yaml != big {
		t.Fatal("the large template was not stored whole")
	}
}

func TestTemplateSetUpdatesAndRemoves(t *testing.T) {
	e := mihomoEnv(t, tpl("ROUTER", routerTemplate, "grp_router"), tpl("PC", pcTemplate))
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	apply := func(args map[string]any) {
		t.Helper()
		p := decode[PlanOut](t, mustOK(t, s, "subscription_mihomo_template_set_plan", args))
		if err := e.plans.decide(p.PlanID, true); err != nil {
			t.Fatal(err)
		}
		mustOK(t, s, "subscription_mihomo_template_set_apply", map[string]any{"confirm_token": p.ConfirmToken})
	}
	// only the groups: the text stays, the card says so
	p := decode[PlanOut](t, mustOK(t, s, "subscription_mihomo_template_set_plan", map[string]any{"name": "pc", "group_ids": []string{"grp_pc"}}))
	if f := factIn(t, storedFacts(t, e), "groups_before"); f.Value != "none: nobody gets the template" {
		t.Errorf("groups_before: %+v", f)
	}
	if f := factIn(t, storedFacts(t, e), "text"); !strings.HasPrefix(f.Value, "unchanged") {
		t.Errorf("text: %+v", f)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	mustOK(t, s, "subscription_mihomo_template_set_apply", map[string]any{"confirm_token": p.ConfirmToken})
	if got := e.w.subs.MihomoTemplates[1]; got.Name != "PC" || got.Yaml != pcTemplate || len(got.GroupIds) != 1 {
		t.Fatalf("after the group change: %+v", got)
	}
	// a new text for one, found by its name ignoring case; the other stays
	apply(map[string]any{"name": "router", "yaml": strings.Replace(routerTemplate, "7890", "7891", 1)})
	if len(e.w.subs.MihomoTemplates) != 2 || !strings.Contains(e.w.subs.MihomoTemplates[0].Yaml, "7891") || e.w.subs.MihomoTemplates[0].Name != "ROUTER" || len(e.w.subs.MihomoTemplates[0].GroupIds) != 1 {
		t.Fatalf("after the text change: %+v", e.w.subs.MihomoTemplates)
	}
	apply(map[string]any{"name": "PC", "remove": true})
	if len(e.w.subs.MihomoTemplates) != 1 || e.w.subs.MihomoTemplates[0].Name != "ROUTER" {
		t.Fatalf("after the removal: %+v", e.w.subs.MihomoTemplates)
	}
}

func TestTemplateSetRefusals(t *testing.T) {
	e := mihomoEnv(t, tpl("ROUTER", routerTemplate, "grp_router"))
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	for name, c := range map[string]struct {
		args map[string]any
		want string
	}{
		"nothing to change":      {map[string]any{"name": "ROUTER", "yaml": routerTemplate}, "nothing to change"},
		"no field":               {map[string]any{"name": "ROUTER"}, "give yaml"},
		"remove with a field":    {map[string]any{"name": "ROUTER", "remove": true, "yaml": routerTemplate}, "stands alone"},
		"remove what is not":     {map[string]any{"name": "ghost", "remove": true}, "no Mihomo template"},
		"a new one without yaml": {map[string]any{"name": "ghost", "group_ids": []string{"grp_pc"}}, "needs yaml"},
		"no name":                {map[string]any{"name": " ", "yaml": routerTemplate}, "name is required"},
		"a bad text":             {map[string]any{"name": "PC", "yaml": "a: [1,\nb: 2"}, "mihomo_templates"},
		"a second document":      {map[string]any{"name": "PC", "yaml": routerTemplate + "---\na: 1\n"}, "more than one"},
		"an unknown group":       {map[string]any{"name": "PC", "yaml": routerTemplate, "group_ids": []string{"grp_nosuch"}}, "no group with id"},
		"a group of another":     {map[string]any{"name": "PC", "yaml": routerTemplate, "group_ids": []string{"grp_router"}}, "already in the template"},
		"a reference to nobody":  {map[string]any{"name": "PC", "yaml": "proxy-groups:\n  - {name: G, type: select, proxies: [some-server-of-bob]}\n"}, "some-server-of-bob"},
		"an invisible char":      {map[string]any{"name": "PC", "yaml": routerTemplate + "# a‮b\n"}, "invisible"},
		"an invisible name":      {map[string]any{"name": "P​C", "yaml": routerTemplate}, "invisible"},
		"a too long text":        {map[string]any{"name": "PC", "yaml": routerTemplate + "# " + strings.Repeat("x", 65536) + "\n"}, "at most"},
	} {
		if out := mustFail(t, s, "subscription_mihomo_template_set_plan", c.args); !strings.Contains(out, c.want) {
			t.Errorf("%s: %s", name, out)
		}
		if len(e.plans.byID) != 0 {
			t.Fatalf("%s created a plan", name)
		}
	}
}

// A text that was read through the scrubber holds its markers; written back it would put them in the place of the real values.
func TestTemplateSetRefusesTheScrubbersMarkers(t *testing.T) {
	e := mihomoEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	for _, marker := range []string{"[redacted]", "[token]", "[key]", "[link]", "[uuid]", "[confirm]", "[private key]", "[config]"} {
		text := routerTemplate + "# was " + marker + "\n"
		out := mustFail(t, s, "subscription_mihomo_template_set_plan", map[string]any{"name": "ROUTER", "yaml": text})
		if !strings.Contains(out, "template_has_redaction_markers") || !strings.Contains(out, marker) {
			t.Errorf("%s: %s", marker, out)
		}
	}
	if len(e.plans.byID) != 0 {
		t.Fatal("a refused text created a plan")
	}
}

// The real round trip: read a template whose URL the scrubber cuts, and send it back as it was read.
func TestTemplateReadThenWriteBackIsRefused(t *testing.T) {
	text := strings.Replace(routerTemplate, "https://rules.example.com/ads.mrs", "https://rules.example.com/path/with/a-secret-looking-name-0123456789.mrs?k=v", 1)
	e := mihomoEnv(t, tpl("ROUTER", text, "grp_router"))
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	w := decode[MihomoTemplateTextV](t, mustOK(t, s, "subscription_mihomo_template_get", map[string]any{"name": "ROUTER"}))
	if !w.Lossy {
		t.Fatalf("the read was not marked lossy: %q", w.Text)
	}
	out := mustFail(t, s, "subscription_mihomo_template_set_plan", map[string]any{"name": "ROUTER", "yaml": strings.Replace(w.Text, "7890", "7891", 1)})
	if !strings.Contains(out, "template_has_redaction_markers") {
		t.Errorf("the write-back: %s", out)
	}
}

func TestTemplateSetFollowsTheSettingsHash(t *testing.T) {
	e := mihomoEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	p := decode[PlanOut](t, mustOK(t, s, "subscription_mihomo_template_set_plan", map[string]any{"name": "ROUTER", "yaml": routerTemplate}))
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	e.w.mu.Lock()
	e.w.subs.Title = "Saved meanwhile in the admin panel"
	e.w.mu.Unlock()
	if out := mustFail(t, s, "subscription_mihomo_template_set_apply", map[string]any{"confirm_token": p.ConfirmToken}); !strings.Contains(out, "changed_since_plan") && !strings.Contains(out, "changed after the plan") {
		t.Errorf("apply after a parallel save: %s", out)
	}
	if len(e.w.subsReq) != 0 {
		t.Fatal("a stale plan saved")
	}
}

// A group deleted between the plan and the apply is not a stored plan that cannot be read: the apply says what no longer applies.
func TestTemplateApplyAfterTheGroupWasDeleted(t *testing.T) {
	e := mihomoEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	p := decode[PlanOut](t, mustOK(t, s, "subscription_mihomo_template_set_plan", map[string]any{"name": "PC", "yaml": pcTemplate, "group_ids": []string{"grp_pc"}}))
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	e.w.mu.Lock()
	delete(e.w.groups, "grp_pc")
	e.w.mu.Unlock()
	if out := mustFail(t, s, "subscription_mihomo_template_set_apply", map[string]any{"confirm_token": p.ConfirmToken}); !strings.Contains(out, "no group with id") || !strings.Contains(out, "make a new plan") {
		t.Errorf("apply after the group was deleted: %s", out)
	}
	if len(e.w.subsReq) != 0 {
		t.Fatal("the plan saved a template of a group that is gone")
	}
}

func TestTemplateToolsProfiles(t *testing.T) {
	e := newTestEnv(t)
	_, ro := e.token(ProfileReadonly)
	_, op := e.token(ProfileOperator)
	names := toolNames(t, e.session(ro))
	if !contains(names, "subscription_mihomo_template_get") || contains(names, "subscription_mihomo_template_set_plan") {
		t.Errorf("readonly tools: %v", names)
	}
	names = toolNames(t, e.session(op))
	for _, n := range []string{"subscription_mihomo_template_get", "subscription_mihomo_template_set_plan", "subscription_mihomo_template_set_apply"} {
		if !contains(names, n) {
			t.Errorf("operator lacks %s", n)
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Every marker the scrubbers can leave is one the write refuses: a new replacement in scrub.go that is not in redactionMarker fails here.
func TestRedactionMarkersCoverTheScrubbers(t *testing.T) {
	markers := regexp.MustCompile(`\[[a-z ]+\]`)
	found := 0
	for _, sc := range textScrubbers {
		for _, m := range markers.FindAllString(sc.repl, -1) {
			found++
			if !redactionMarker.MatchString(m) {
				t.Errorf("the scrubber leaves %s, which the template write does not refuse", m)
			}
		}
	}
	for _, m := range []string{"[uuid]", "[key]", "[token]"} { // the passes outside the list
		if !redactionMarker.MatchString(m) {
			t.Errorf("%s is not refused", m)
		}
	}
	if found < 5 {
		t.Errorf("only %d scrubber markers found: the check reads the wrong field", found)
	}
	// and the real thing: what scrub makes of secrets always holds a marker
	for _, secret := range []string{canaryTK, canaryKey, canaryVless, canaryPrivLine, "https://a.example.com/x?y=1", "11111111-2222-3333-4444-555555555555", "https://a.example.com/aaaaaaaaaaaaaaaaaaaaaaaa"} {
		if cut := scrub(secret); cut != secret && !redactionMarker.MatchString(cut) {
			t.Errorf("scrub(%q) = %q has no marker the write refuses", secret, cut)
		}
	}
}
