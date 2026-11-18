package subs_test

import (
	"context"
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

// The routing profiles of Happ and INCY are the owner's, like the Mihomo templates: a helper may save the rest of the settings with
// the profiles as they are stored, and is refused (PERMISSION_DENIED, routing_profiles_owner_only) when the save changes one.
func TestRoutingProfilesAreTheOwnersToChange(t *testing.T) {
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
	changed := func(edit func(*adminv1.SubscriptionSettings)) *adminv1.SubscriptionSettings {
		set := proto.Clone(read()).(*adminv1.SubscriptionSettings)
		edit(set)
		return set
	}
	denied := func(what string, set *adminv1.SubscriptionSettings) {
		t.Helper()
		err := save(helper, set)
		if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.HasPrefix(err.Error(), "permission_denied: routing_profiles_owner_only") {
			t.Fatalf("%s: %v", what, err)
		}
	}

	// the owner saves two profiles
	first := subsettings.Defaults()
	first.RoutingProfiles = []*adminv1.RoutingProfile{
		routingEntry(ownProfile, false, m.group2, temp),
		routingEntry(`{"Name":"rest","DirectSites":["domain:example.org"]}`, true, m.group),
	}
	if err := save(owner, first); err != nil {
		t.Fatalf("the owner: %v", err)
	}

	denied("a new rule", changed(func(s *adminv1.SubscriptionSettings) {
		s.RoutingProfiles[1].Profile = `{"Name":"rest","DirectSites":["domain:example.net"]}`
		s.Title = "by helper"
	}))
	denied("another name", changed(func(s *adminv1.SubscriptionSettings) {
		s.RoutingProfiles[1].Profile = `{"Name":"rest2","DirectSites":["domain:example.org"]}`
	}))
	denied("the mode", changed(func(s *adminv1.SubscriptionSettings) { s.RoutingProfiles[1].OnlyAdd = false }))
	denied("fewer groups", changed(func(s *adminv1.SubscriptionSettings) {
		s.RoutingProfiles[0].GroupIds = s.RoutingProfiles[0].GroupIds[:1]
	}))
	denied("for everybody", changed(func(s *adminv1.SubscriptionSettings) {
		s.RoutingProfiles[1].OnlyGroups, s.RoutingProfiles[1].GroupIds = false, nil
	}))
	denied("a new profile", changed(func(s *adminv1.SubscriptionSettings) {
		s.RoutingProfiles = append(s.RoutingProfiles, routingEntry(`{"Name":"third"}`, false))
	}))
	denied("a removed profile", changed(func(s *adminv1.SubscriptionSettings) { s.RoutingProfiles = s.RoutingProfiles[:1] }))
	denied("all removed", changed(func(s *adminv1.SubscriptionSettings) { s.RoutingProfiles = nil }))
	if got := read(); len(got.RoutingProfiles) != 2 || got.Title == "by helper" {
		t.Fatalf("a refused save changed something: %v", got)
	}

	// the same profiles in another order, the groups in another order, as the form sends them: the helper saves the other settings
	if err := save(helper, changed(func(s *adminv1.SubscriptionSettings) {
		s.Title = "by helper"
		s.RoutingProfiles[0], s.RoutingProfiles[1] = s.RoutingProfiles[1], s.RoutingProfiles[0]
		g := s.RoutingProfiles[1].GroupIds
		g[0], g[1] = g[1], g[0]
	})); err != nil || read().Title != "by helper" {
		t.Fatalf("the helper's save of another field: %v", err)
	}

	// a group that was deleted since stays in the stored list; a helper's form that still sends it, or no longer does, changes nothing
	must(m.svc.DeleteGroup(m.ctx, connect.NewRequest(&adminv1.DeleteGroupRequest{GroupId: temp})))
	if err := save(helper, changed(func(s *adminv1.SubscriptionSettings) { s.Title = "after the delete" })); err != nil {
		t.Fatalf("the helper's save with a deleted group in a profile: %v", err)
	}
	if err := save(helper, changed(func(s *adminv1.SubscriptionSettings) {
		for _, e := range s.RoutingProfiles {
			e.GroupIds = slices.DeleteFunc(e.GroupIds, func(id string) bool { return id == temp })
		}
	})); err != nil {
		t.Fatalf("the helper's save without the deleted group: %v", err)
	}

	// the owner changes them all the same
	if err := save(owner, changed(func(s *adminv1.SubscriptionSettings) {
		s.RoutingProfiles = []*adminv1.RoutingProfile{routingEntry(`{"Name":"mobile"}`, false, m.group)}
	})); err != nil {
		t.Fatalf("the owner's change: %v", err)
	}
	if got := read().RoutingProfiles; len(got) != 1 || got[0].Info.Name != "mobile" {
		t.Fatalf("after the owner's change: %v", got)
	}
}

// A change of a template is refused with its own code whatever the routing list holds, and a pasted profile is compared as the
// panel stores it: the form that sends back the profile it read is no change.
func TestRoutingOwnerCheckKeepsTheTemplatesAndReadsPastedProfiles(t *testing.T) {
	m := newM3Rig(t)
	svc := headerService(m)
	helper := auth.WithAdmin(m.ctx, store.Admin{ID: "adm_helper", Role: store.RoleHelper})
	set := subsettings.Defaults()
	set.MihomoTemplates = []*adminv1.MihomoTemplate{mihomoTemplate("pc", m.group)}
	_, err := svc.UpdateSubscriptionSettings(helper, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set}))
	if err == nil || !strings.HasPrefix(err.Error(), "permission_denied: mihomo_templates_owner_only") {
		t.Fatalf("a template: %v", err)
	}
	p, err := subsettings.ParseRoutingInput(ownProfile)
	if err != nil {
		t.Fatal(err)
	}
	pasted := []*adminv1.RoutingProfile{routingEntry(ownProfile, false, "g1", "g2")}
	stored := []*adminv1.RoutingProfile{routingEntry(p.Doc.Canonical(), false, "g2", "g1")}
	if subsettings.RoutingProfilesDiffer(pasted, stored, nil) {
		t.Error("the same profile, pasted and stored")
	}
	stored[0].GroupIds = []string{"g1", "gone"}
	if !subsettings.RoutingProfilesDiffer(pasted, stored, nil) || subsettings.RoutingProfilesDiffer(pasted, stored[:1], func(id string) bool { return id == "gone" || id == "g2" }) {
		t.Error("a group that differs is a change unless it is skipped")
	}
}
