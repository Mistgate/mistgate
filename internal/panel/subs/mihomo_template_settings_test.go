package subs_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The Mihomo templates of the settings (SubscriptionSettings.mihomo_templates) through the admin API: the save names the groups it
// refuses, a group that was deleted since drops out, and what is saved is what the next read returns.

const groupTemplate = "proxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\n"

func mihomoTemplate(name string, groups ...string) *adminv1.MihomoTemplate {
	return &adminv1.MihomoTemplate{Name: name, Yaml: groupTemplate, GroupIds: groups}
}

func TestMihomoTemplatesThroughTheSettingsAPI(t *testing.T) {
	m := newM3Rig(t)
	svc := headerService(m)
	ctx := auth.WithAdmin(m.ctx, store.Admin{ID: "adm_templates", Role: store.RoleOwner})
	temp := must(m.svc.CreateGroup(m.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "temp", ProfileIds: []string{m.profile}}))).Msg.Group.Id
	save := func(list ...*adminv1.MihomoTemplate) (*adminv1.SubscriptionSettings, error) {
		set := subsettings.Defaults()
		set.MihomoTemplates = list
		r, err := svc.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set}))
		if err != nil {
			return nil, err
		}
		return r.Msg.Settings, nil
	}
	invalid := func(what string, list ...*adminv1.MihomoTemplate) {
		t.Helper()
		if _, err := save(list...); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s: %v", what, err)
		}
	}

	invalid("a group that never existed", mihomoTemplate("router", "grp_nosuchgroup"))
	invalid("a group in two templates", mihomoTemplate("router", m.group2), mihomoTemplate("pc", m.group2))
	invalid("a text that does not parse", &adminv1.MihomoTemplate{Name: "router", Yaml: "a: [\n", GroupIds: []string{m.group2}})
	invalid("a group that refers to a server", &adminv1.MihomoTemplate{Name: "router", Yaml: groupTemplate + "  - name: Other\n    type: select\n    proxies: [de1]\n"})

	got, err := save(mihomoTemplate("router", temp, m.group2), mihomoTemplate("pc", m.group))
	if err != nil || len(got.MihomoTemplates) != 2 || len(got.MihomoTemplates[0].GroupIds) != 2 || got.MihomoTemplates[1].Name != "pc" {
		t.Fatalf("two templates: %v, %v", got, err)
	}
	// What was saved comes back through the settings read, text and all.
	read := must(svc.GetSubscriptionSettings(ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))).Msg.Settings
	if len(read.MihomoTemplates) != 2 || read.MihomoTemplates[0].Yaml != groupTemplate {
		t.Fatalf("read back: %v", read.MihomoTemplates)
	}

	// The group of the template is deleted; the editor of another tab still sends the old list: the group drops out, the save passes.
	must(m.svc.DeleteGroup(m.ctx, connect.NewRequest(&adminv1.DeleteGroupRequest{GroupId: temp})))
	got, err = save(mihomoTemplate("router", temp, m.group2), mihomoTemplate("pc", m.group))
	if err != nil || len(got.MihomoTemplates[0].GroupIds) != 1 || got.MihomoTemplates[0].GroupIds[0] != m.group2 {
		t.Fatalf("a deleted group: %v, %v", got, err)
	}
	invalid("a group that never existed, still", mihomoTemplate("router", "grp_nosuchgroup"))
	// A deleted group is not forgiven in the other list: it was in a template, not in a header.
	set := subsettings.Defaults()
	set.ResponseHeaders = []*adminv1.ResponseHeader{ownHeader("flclashx-view", "dark", nil, temp)}
	if _, err := svc.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("a deleted group in a header that never had it: %v", err)
	}

	// The audit row counts the templates and keeps their texts out.
	if _, err := save(mihomoTemplate("router", m.group2)); err != nil {
		t.Fatal(err)
	}
	for _, row := range must(m.st.ListAudit(m.ctx, "", 0, 10)) {
		if row.Action != "subscription_settings_update" {
			continue
		}
		var params map[string]any
		if err := json.Unmarshal([]byte(row.Params), &params); err != nil {
			t.Fatal(err)
		}
		if params["mihomo_templates"] != float64(1) {
			t.Fatalf("audit: %v", row.Params)
		}
		if len(row.Params) > 600 {
			t.Fatalf("the audit row carries the texts: %s", row.Params)
		}
		return
	}
	t.Fatal("no audit row")
}

func TestMihomoTemplateGroupIdentityFollowsID(t *testing.T) {
	r := newTemplateRig(t, func(cfg *subs.Config) { cfg.MinInterval = -1 })
	settings, preview := headerService(r.m3rig), r.previewService(t, plainPreset)
	owner := auth.WithAdmin(r.ctx, store.Admin{ID: "adm_templates", Role: store.RoleOwner})
	createGroup := func(name string) string {
		t.Helper()
		return must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: name, ProfileIds: []string{r.profile}}))).Msg.Group.Id
	}
	old := createGroup("Router")
	text := groupTemplate + "mode: rule\n" + fmt.Sprintf("# @mistgate-if [%q]\nmixed-port: 1080\n# @mistgate-end\n", old)
	set := subsettings.Defaults()
	set.MihomoTemplates = []*adminv1.MihomoTemplate{{Name: "home", Yaml: text, GroupIds: []string{r.group, old}}}
	save := func() *adminv1.MihomoTemplate {
		t.Helper()
		got := must(settings.UpdateSubscriptionSettings(owner, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set}))).Msg.Settings.MihomoTemplates[0]
		r.cache.Invalidate() // mirrors the shared settings cache used by the panel
		if got.Yaml != text {
			t.Fatalf("group changes rewrote the source:\n%s", got.Yaml)
		}
		return got
	}
	check := func(user, token, name string, port any) {
		t.Helper()
		body := groupAnswer(t, r.body(t, token))
		resp, err := r.preview(t, preview, user, text, set.MihomoTemplates[0].GroupIds...)
		if err != nil {
			t.Fatal(err)
		}
		draft := groupAnswer(t, resp.Yaml)
		if resp.GroupName != name || body["mode"] != "rule" || draft["mode"] != "rule" || body["mixed-port"] != port || draft["mixed-port"] != port {
			t.Errorf("group %q: subscription mode/port = %v/%v, preview group/mode/port = %q/%v/%v",
				name, body["mode"], body["mixed-port"], resp.GroupName, draft["mode"], draft["mixed-port"])
		}
	}
	save()
	router, token := r.user("router", func(req *adminv1.CreateUserRequest) { req.GroupId = old })
	check(router, token, "Router", 1080)
	renamed := "Renamed Router"
	must(r.svc.UpdateGroup(r.ctx, connect.NewRequest(&adminv1.UpdateGroupRequest{GroupId: old, Name: &renamed})))
	check(router, token, renamed, 1080)

	must(r.svc.DeleteGroup(r.ctx, connect.NewRequest(&adminv1.DeleteGroupRequest{GroupId: old, MoveUsersTo: r.group})))
	got := save() // a tab still sends the old assignment; the API prunes it without touching the source
	if !slices.Equal(got.GroupIds, []string{r.group}) {
		t.Fatalf("deleted group's assignment was retained: %v", got.GroupIds)
	}
	set.MihomoTemplates[0].GroupIds = got.GroupIds
	check(router, token, "g", nil)
	recreated := createGroup(renamed)
	if recreated == old {
		t.Fatal("the recreated group unexpectedly reused the deleted ID")
	}
	set.MihomoTemplates[0].GroupIds = append(set.MihomoTemplates[0].GroupIds, recreated)
	save()
	newRouter, newToken := r.user("new-router", func(req *adminv1.CreateUserRequest) { req.GroupId = recreated })
	check(newRouter, newToken, renamed, nil)
}

func TestMihomoTemplateInvalidVariantLeavesSettingsUnchanged(t *testing.T) {
	r := newTemplateRig(t, nil)
	svc := headerService(r.m3rig)
	preview := r.previewService(t, plainPreset)
	owner := auth.WithAdmin(r.ctx, store.Admin{ID: "adm_templates", Role: store.RoleOwner})
	user, _ := r.user("phone", nil)
	groups := []string{r.group, r.group2}
	original := "mode: rule\n" + groupTemplate
	r.save(t, nil, &adminv1.MihomoTemplate{Name: "shared", Yaml: original, GroupIds: groups})
	bad := original + fmt.Sprintf("# @mistgate-if [%q]\nmixed-port: 7890\n# @mistgate-end\n# @mistgate-if [%q]\nmode: global\n# @mistgate-end\n", r.group, r.group2)
	set := subsettings.Defaults()
	set.Title = "rejected title"
	set.MihomoTemplates = []*adminv1.MihomoTemplate{{Name: "shared", Yaml: bad, GroupIds: groups}}
	_, saveErr := svc.UpdateSubscriptionSettings(owner, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set}))
	_, previewErr := r.preview(t, preview, user, bad, groups...) // phone's variant is valid, but the whole draft is rejected
	for _, err := range []error{saveErr, previewErr} {
		if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), r.group2) || !strings.Contains(err.Error(), "line 10") {
			t.Errorf("expected the later error in the router variant, got %v", err)
		}
	}
	read := must(svc.GetSubscriptionSettings(owner, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))).Msg.Settings
	if read.Title == set.Title || len(read.MihomoTemplates) != 1 || read.MihomoTemplates[0].Yaml != original {
		t.Fatal("the rejected draft modified the saved settings")
	}
}
