package subs_test

import (
	"encoding/json"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
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
