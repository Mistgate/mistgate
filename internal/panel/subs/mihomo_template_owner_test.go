package subs_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The Mihomo templates are the owner's: a helper may save the rest of the settings, with the templates as they are stored, and is
// refused (PERMISSION_DENIED, mihomo_templates_owner_only) when the save adds, removes or changes one.
func TestMihomoTemplatesAreTheOwnersToChange(t *testing.T) {
	m := newM3Rig(t)
	svc := headerService(m)
	owner := auth.WithAdmin(m.ctx, store.Admin{ID: "adm_owner", Role: store.RoleOwner})
	helper := auth.WithAdmin(m.ctx, store.Admin{ID: "adm_helper", Role: store.RoleHelper})
	temp := must(m.svc.CreateGroup(m.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "temp", ProfileIds: []string{m.profile}}))).Msg.Group.Id
	read := func() *adminv1.SubscriptionSettings {
		return must(svc.GetSubscriptionSettings(owner, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))).Msg.Settings
	}
	save := func(ctx context.Context, set *adminv1.SubscriptionSettings) error {
		_, err := svc.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set}))
		return err
	}
	denied := func(what string, set *adminv1.SubscriptionSettings) {
		t.Helper()
		err := save(helper, set)
		if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.HasPrefix(err.Error(), "permission_denied: mihomo_templates_owner_only") {
			t.Fatalf("%s: %v", what, err)
		}
	}
	changed := func(edit func(*adminv1.SubscriptionSettings)) *adminv1.SubscriptionSettings {
		set := proto.Clone(read()).(*adminv1.SubscriptionSettings)
		edit(set)
		return set
	}

	// the owner saves two templates
	first := subsettings.Defaults()
	first.MihomoTemplates = []*adminv1.MihomoTemplate{mihomoTemplate("router", m.group2, temp), mihomoTemplate("pc", m.group)}
	first.MihomoTemplates[0].Yaml += fmt.Sprintf("# @mistgate-if [%q]\nmixed-port: 1080\n# @mistgate-end\n", temp)
	if err := save(owner, first); err != nil {
		t.Fatalf("the owner: %v", err)
	}
	title := func() string { return read().Title }

	// a helper changes a template: the name, the text, the groups, a new one, a removed one: refused, and nothing is saved
	denied("a new text", changed(func(s *adminv1.SubscriptionSettings) { s.MihomoTemplates[0].Yaml += "# more\n"; s.Title = "by helper" }))
	denied("the group condition", changed(func(s *adminv1.SubscriptionSettings) {
		s.MihomoTemplates[0].Yaml = strings.Replace(s.MihomoTemplates[0].Yaml, temp, m.group2, 1)
	}))
	denied("a new name", changed(func(s *adminv1.SubscriptionSettings) { s.MihomoTemplates[0].Name = "router2" }))
	denied("fewer groups", changed(func(s *adminv1.SubscriptionSettings) {
		s.MihomoTemplates[0].GroupIds = s.MihomoTemplates[0].GroupIds[:1]
	}))
	denied("a group moved", changed(func(s *adminv1.SubscriptionSettings) {
		s.MihomoTemplates[1].GroupIds = append(s.MihomoTemplates[1].GroupIds, m.group2)
	}))
	denied("a new template", changed(func(s *adminv1.SubscriptionSettings) {
		s.MihomoTemplates = append(s.MihomoTemplates, mihomoTemplate("mobile"))
	}))
	denied("a removed template", changed(func(s *adminv1.SubscriptionSettings) { s.MihomoTemplates = s.MihomoTemplates[:1] }))
	denied("all removed", changed(func(s *adminv1.SubscriptionSettings) { s.MihomoTemplates = nil }))
	if got := read(); len(got.MihomoTemplates) != 2 || got.Title == "by helper" || strings.Contains(got.MihomoTemplates[0].Yaml, "# more") {
		t.Fatalf("a refused save changed something: %v", got)
	}

	// the same templates, in another order, with the groups in another order and a name in another case: no change
	if err := save(helper, changed(func(s *adminv1.SubscriptionSettings) {
		s.Title = "by helper"
		s.MihomoTemplates[0], s.MihomoTemplates[1] = s.MihomoTemplates[1], s.MihomoTemplates[0]
		g := s.MihomoTemplates[1].GroupIds
		g[0], g[1] = g[1], g[0]
		s.MihomoTemplates[0].Name = " PC "
	})); err != nil || title() != "by helper" {
		t.Fatalf("the helper's save of another field: %v, title %q", err, title())
	}
	if got := read().MihomoTemplates; len(got) != 2 || got[0].Name != "pc" && got[0].Name != "PC" {
		t.Fatalf("the templates after the helper's save: %v", got)
	}

	// a group that was deleted since stays in the stored list; a helper's form that still sends it, or no longer does, changes nothing
	must(m.svc.DeleteGroup(m.ctx, connect.NewRequest(&adminv1.DeleteGroupRequest{GroupId: temp})))
	if err := save(helper, changed(func(s *adminv1.SubscriptionSettings) { s.Title = "after the delete" })); err != nil {
		t.Fatalf("the helper's save with a deleted group in a template: %v", err)
	}
	if err := save(helper, changed(func(s *adminv1.SubscriptionSettings) {
		s.MihomoTemplates[0].GroupIds = slices.DeleteFunc(s.MihomoTemplates[0].GroupIds, func(id string) bool { return id == temp })
		s.MihomoTemplates[1].GroupIds = slices.DeleteFunc(s.MihomoTemplates[1].GroupIds, func(id string) bool { return id == temp })
	})); err != nil {
		t.Fatalf("the helper's save without the deleted group: %v", err)
	}

	// the owner changes them all the same
	if err := save(owner, changed(func(s *adminv1.SubscriptionSettings) {
		s.MihomoTemplates = []*adminv1.MihomoTemplate{mihomoTemplate("mobile", m.group)}
	})); err != nil {
		t.Fatalf("the owner's change: %v", err)
	}
	if got := read().MihomoTemplates; len(got) != 1 || got[0].Name != "mobile" {
		t.Fatalf("after the owner's change: %v", got)
	}
}
