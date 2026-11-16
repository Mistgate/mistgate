package subs_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The admin's preview of a Mihomo template (SubscriptionService.PreviewMihomoTemplate): the profile of one person from the draft,
// without keys, written nowhere, with the warnings.

// previewService is the admin service over the rig's database with the DNS module, whose instance default preset is the built-in one
// named (the people of the rig have no other).
func (r *templateRig) previewService(t *testing.T, preset string) *subs.Service {
	t.Helper()
	d := dns.New(r.st)
	if err := d.SetDefaultPresetID(r.ctx, preset); err != nil {
		t.Fatal(err)
	}
	svc := subs.NewService(r.st, subsettings.NewCache(r.st, nil), builtin.Registry(),
		func(ctx context.Context) (instance.Settings, error) { return instance.Load(ctx, r.st) }, d, nil)
	svc.PreviewWith(r.svc)
	return svc
}

const plainPreset, splitPreset = "dns_builtin_standard", "dns_builtin_ru_split"

func (r *templateRig) preview(t *testing.T, svc *subs.Service, user, text string, groups ...string) (*adminv1.PreviewMihomoTemplateResponse, error) {
	t.Helper()
	resp, err := svc.PreviewMihomoTemplate(r.ctx, connect.NewRequest(&adminv1.PreviewMihomoTemplateRequest{UserId: user, Yaml: text, GroupIds: groups}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func warningCodes(r *adminv1.PreviewMihomoTemplateResponse) string {
	var out []string
	for _, w := range r.Warnings {
		if w.Arg != "" {
			out = append(out, w.Code+":"+w.Arg)
		} else {
			out = append(out, w.Code)
		}
	}
	return strings.Join(out, " ")
}

func TestPreviewMihomoTemplate(t *testing.T) {
	r := newTemplateRig(t, nil)
	svc := r.previewService(t, plainPreset)
	alice, aliceToken := r.newUser("alice", nil)
	// The person has fetched once: their AmneziaWG key exists (a person who never did has no such server to show yet).
	real := r.body(t, aliceToken)

	resp, err := r.preview(t, svc, alice, r.router, r.group2)
	if err != nil {
		t.Fatal(err)
	}
	a := answerOf(t, resp.Yaml)
	if resp.NotServed != "" || resp.GroupName != "g2" || a.RedirPrt != 7892 || len(a.Proxies) != 2 || a.Groups[len(a.Groups)-1].Name != "Auto" || a.DNS["respect-rules"] != true {
		t.Fatalf("%+v\n%s", resp, resp.Yaml)
	}
	if warningCodes(resp) != "" {
		t.Errorf("warnings of a clean template: %s", warningCodes(resp))
	}
	// The servers carry their name, type, address and port; the template's own text is as written.
	for _, p := range a.Proxies {
		if len(p) != 4 || p["name"] == nil || p["type"] == nil || p["server"] == nil || p["port"] == nil {
			t.Errorf("a server in the preview: %v", p)
		}
	}
	if !strings.Contains(resp.Yaml, "# Router: transparent proxy") || !strings.Contains(resp.Yaml, "<<: *check") {
		t.Errorf("the template's text is not kept:\n%s", resp.Yaml)
	}

	// None of what the person's real answer holds as a secret is in the preview: take it from the answer itself.
	var secrets []string
	for _, p := range answerOf(t, real).Proxies {
		for _, k := range []string{"password", "obfs-password", "private-key", "pre-shared-key", "public-key", "header-protection-key", "ip"} {
			if v, ok := p[k].(string); ok && v != "" {
				secrets = append(secrets, v)
			}
		}
	}
	if len(secrets) < 5 {
		t.Fatalf("the test found too few secrets (%d) in the real answer", len(secrets))
	}
	for _, s := range secrets {
		if strings.Contains(resp.Yaml, s) {
			t.Errorf("the preview holds %q", s)
		}
	}
	if strings.Contains(resp.Yaml, aliceToken) {
		t.Error("the token is in the preview")
	}
	for _, word := range []string{"password", "private-key", "obfs", "amnezia-wg-option", "skip-cert-verify"} {
		if strings.Contains(resp.Yaml, word) {
			t.Errorf("a field of a server's credentials: %s", word)
		}
	}
}

// A preview writes nothing: not a device, not a credential, not the mark that the link was fetched.
func TestPreviewMihomoTemplateWritesNothing(t *testing.T) {
	r := newTemplateRig(t, nil)
	svc := r.previewService(t, plainPreset)
	alice, _ := r.newUser("alice", nil) // never fetched: no device, no credential yet
	count := func() (devices, creds, awg int, seen int64) {
		r.st.R.QueryRow(`SELECT count(*) FROM device`).Scan(&devices)
		r.st.R.QueryRow(`SELECT count(*) FROM device_credential`).Scan(&creds)
		r.st.R.QueryRow(`SELECT count(*) FROM device_credential WHERE protocol = 'awg'`).Scan(&awg)
		r.st.R.QueryRow(`SELECT coalesce(sum(last_seen_at), 0) FROM device`).Scan(&seen)
		return
	}
	d0, c0, a0, s0 := count()
	for i := 0; i < 3; i++ {
		if _, err := r.preview(t, svc, alice, r.router, r.group2); err != nil {
			t.Fatal(err)
		}
	}
	if d, c, a, s := count(); d != d0 || c != c0 || a != a0 || s != s0 {
		t.Errorf("the preview wrote: devices %d->%d, credentials %d->%d, awg %d->%d, seen %d->%d", d0, d, c0, c, a0, a, s0, s)
	}
	// and the real fetch still makes them (the control: the counts above can change)
	_, tok := r.newUser("bob", nil)
	r.body(t, tok)
	if _, c, _, _ := count(); c == c0 {
		t.Error("a real fetch made no credential: the test does not count what it should")
	}
}

func TestPreviewMihomoTemplateErrors(t *testing.T) {
	r := newTemplateRig(t, nil)
	svc := r.previewService(t, plainPreset)
	alice, _ := r.newUser("alice", nil)
	for name, text := range map[string]string{
		"a syntax error": "proxy-groups: [\n",
		"two documents":  r.router + "---\na: 1\n",
		"a repeated key": "mixed-port: 1\nmixed-port: 2\n",
		"an alias bomb":  "x: &a [x, x, x, x, x, x, x, x, x]\ny: &b [*a, *a, *a, *a, *a, *a, *a, *a, *a]\nz: &c [*b, *b, *b, *b, *b, *b, *b, *b, *b]\nw: &d [*c, *c, *c, *c, *c, *c, *c, *c, *c]\nv: &e [*d, *d, *d, *d, *d, *d, *d, *d, *d]\nu: &f [*e, *e, *e, *e, *e, *e, *e, *e, *e]\nt: &g [*f, *f, *f, *f, *f, *f, *f, *f, *f]\ns: &h [*g, *g, *g, *g, *g, *g, *g, *g, *g]\nr: &i [*h, *h, *h, *h, *h, *h, *h, *h, *h]\n",
		"a server named": "proxy-groups:\n  - name: P\n    type: select\n    proxies: [de1]\n",
		"empty":          "",
		"too big":        "#" + strings.Repeat("x", subsettings.MaxMihomoTemplateBytes) + "\n",
	} {
		if _, err := r.preview(t, svc, alice, text, r.group2); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := r.preview(t, svc, "usr_nobody", r.router); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("an unknown person: %v", err)
	}
	// without the access module wired the service says so instead of failing in the middle
	if _, err := headerService(r.m3rig).PreviewMihomoTemplate(r.ctx, connect.NewRequest(&adminv1.PreviewMihomoTemplateRequest{UserId: alice, Yaml: r.router})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("an unwired service: %v", err)
	}
	// the draft is checked against the saved switch of Auto
	set := subsettings.Defaults()
	set.LinkAutoSelect = new(bool)
	if _, err := r.cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	if _, err := r.preview(t, svc, alice, "proxy-groups:\n  - name: P\n    type: select\n    proxies: [Auto]\n"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("Auto with the switch off: %v", err)
	}
}

func TestPreviewMihomoTemplateNotServed(t *testing.T) {
	r := newTemplateRig(t, nil)
	svc := r.previewService(t, plainPreset)
	expired, _ := r.newUser("alice", nil)
	r.st.W.Exec(`UPDATE user SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).Unix(), expired)
	resp, err := r.preview(t, svc, expired, r.router, r.group2)
	if err != nil || resp.NotServed != "no_access" || strings.Contains(resp.Yaml, "redir-port") || !strings.Contains(resp.Yaml, "0.0.0.0") {
		t.Fatalf("no access: %+v, %v", resp, err)
	}
	// no server in their group
	empty := must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "empty"}))).Msg.Group.Id
	none, _ := r.user("bob", func(c *adminv1.CreateUserRequest) { c.GroupId = empty })
	if resp, err = r.preview(t, svc, none, r.router, empty); err != nil || resp.NotServed != "no_servers" || strings.Contains(resp.Yaml, "redir-port") || resp.GroupName != "empty" {
		t.Fatalf("no servers: %+v, %v", resp, err)
	}
	// AmneziaWG only, left out of the link
	only := must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "awg only", ProfileIds: []string{r.awg}}))).Msg.Group.Id
	awgOnly, _ := r.user("eve", func(c *adminv1.CreateUserRequest) { c.GroupId = only })
	set := subsettings.Defaults()
	set.LinkAwg = new(bool)
	if _, err := r.cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	if resp, err = r.preview(t, svc, awgOnly, r.router, only); err != nil || resp.NotServed != "no_servers" || strings.Contains(resp.Yaml, "redir-port") {
		t.Fatalf("AWG only: %+v, %v", resp, err)
	}
}

// Each code of a warning, and only for what it is.
func TestPreviewMihomoTemplateWarnings(t *testing.T) {
	r := newTemplateRig(t, nil)
	svc := r.previewService(t, plainPreset)
	alice, _ := r.newUser("alice", nil)
	const group = "proxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n"
	for _, tc := range []struct {
		name, text string
		groups     []string
		want       string
	}{
		{"clean", group + "rules:\n  - MATCH,Proxy\n", []string{r.group2}, ""},
		{"no server group", "proxy-groups:\n  - name: Proxy\n    type: select\n    proxies: [DIRECT]\n", []string{r.group2}, "no_server_group"},
		{"own dns", group + "dns:\n  enable: true\n", []string{r.group2}, "own_dns"},
		{"own Auto", "proxy-groups:\n  - name: Auto\n    type: url-test\n    include-all-proxies: true\n", []string{r.group2}, "own_auto"},
		{"an unknown rule target", group + "rules:\n  - DOMAIN,example.com,Nowhere\n  - MATCH,Proxy\n", []string{r.group2}, "unknown_rule_target:Nowhere"},
		{"no group", group, nil, "unused"},
		{"several", "proxy-groups:\n  - name: Proxy\n    type: select\n    proxies: [DIRECT]\ndns:\n  enable: true\nrules:\n  - MATCH,Gone\n", nil, "no_server_group own_dns unknown_rule_target:Gone unused"},
	} {
		resp, err := r.preview(t, svc, alice, tc.text, tc.groups...)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := warningCodes(resp); got != tc.want {
			t.Errorf("%s: warnings %q, want %q", tc.name, got, tc.want)
		}
	}

	// preset_rules_skipped: the person's preset has a split (rules the template does not get); one with only resolvers does not
	for preset, want := range map[string]string{splitPreset: "preset_rules_skipped", plainPreset: "", "dns_builtin_cn_split": "preset_rules_skipped"} {
		resp, err := r.previewService(t, preset).PreviewMihomoTemplate(r.ctx, connect.NewRequest(&adminv1.PreviewMihomoTemplateRequest{UserId: alice, Yaml: group, GroupIds: []string{r.group2}}))
		if err != nil {
			t.Fatal(err)
		}
		if got := warningCodes(resp.Msg); got != want {
			t.Errorf("preset %s: warnings %q, want %q", preset, got, want)
		}
	}
}
