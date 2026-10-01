package access

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestUserAndGroupDNSPreset(t *testing.T) {
	f := newFixture(t)
	e := f.e
	ctx := e.ctx

	// nothing set anywhere: the built-in default
	u := e.user("ann", f.group, nil).User
	if u.DnsPresetId != "" || u.EffectiveDnsPresetId != "dns_builtin_ru_split" || u.EffectiveDnsPresetName != "Россия: .ru напрямую" ||
		u.DnsSource != adminv1.DnsSource_DNS_SOURCE_DEFAULT {
		t.Fatalf("default: %v", u)
	}

	// a group with a preset: its users inherit it
	g := must(e.s.CreateGroup(ctx, req(&adminv1.CreateGroupRequest{Name: "family", DnsPresetId: "dns_builtin_family"}))).Msg.Group
	if g.DnsPresetId != "dns_builtin_family" {
		t.Fatalf("group = %v", g)
	}
	bob := e.user("bob", g.Id, nil).User
	if bob.DnsPresetId != "" || bob.EffectiveDnsPresetId != "dns_builtin_family" || bob.DnsSource != adminv1.DnsSource_DNS_SOURCE_GROUP {
		t.Fatalf("bob: %v", bob)
	}

	// the user's own preset wins over the group's
	cy := e.user("cy", g.Id, func(m *adminv1.CreateUserRequest) { m.DnsPresetId = "dns_builtin_quad9" }).User
	if cy.DnsPresetId != "dns_builtin_quad9" || cy.EffectiveDnsPresetId != "dns_builtin_quad9" || cy.DnsSource != adminv1.DnsSource_DNS_SOURCE_USER {
		t.Fatalf("cy: %v", cy)
	}

	// unknown preset ids are refused
	_, err := e.s.CreateUser(ctx, req(&adminv1.CreateUserRequest{Name: "dan", GroupId: f.group, DnsPresetId: "dns_nope"}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.CreateGroup(ctx, req(&adminv1.CreateGroupRequest{Name: "g2", DnsPresetId: "dns_nope"}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: cy.Id, DnsPresetId: new("dns_nope")}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.UpdateGroup(ctx, req(&adminv1.UpdateGroupRequest{GroupId: g.Id, DnsPresetId: new("dns_nope")}))
	wantCode(t, err, connect.CodeNotFound)

	// updates: set, clear (= inherit), untouched when absent. DNS is client-side: nodes are not notified.
	e.resetNotify()
	up := must(e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, DnsPresetId: new("dns_builtin_adblock")}))).Msg.User
	if up.DnsPresetId != "dns_builtin_adblock" || up.EffectiveDnsPresetName != "AdGuard: без рекламы" || up.DnsSource != adminv1.DnsSource_DNS_SOURCE_USER {
		t.Fatalf("set: %v", up)
	}
	up = must(e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, Name: new("ann2")}))).Msg.User
	if up.DnsPresetId != "dns_builtin_adblock" {
		t.Fatalf("a name edit changed the preset: %v", up)
	}
	up = must(e.s.UpdateUser(ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, DnsPresetId: new("")}))).Msg.User
	if up.DnsPresetId != "" || up.DnsSource != adminv1.DnsSource_DNS_SOURCE_DEFAULT {
		t.Fatalf("clear: %v", up)
	}
	if n := e.notify.n.Load(); n != 0 {
		t.Errorf("DNS edits notified the fleet %d times", n)
	}

	g = must(e.s.UpdateGroup(ctx, req(&adminv1.UpdateGroupRequest{GroupId: g.Id, Name: new("family2")}))).Msg.Group
	if g.DnsPresetId != "dns_builtin_family" {
		t.Fatalf("a rename changed the group preset: %v", g)
	}
	g = must(e.s.UpdateGroup(ctx, req(&adminv1.UpdateGroupRequest{GroupId: g.Id, DnsPresetId: new("")}))).Msg.Group
	if g.DnsPresetId != "" {
		t.Fatalf("group clear: %v", g)
	}
	got := must(e.s.GetUser(ctx, req(&adminv1.GetUserRequest{UserId: bob.Id}))).Msg.User
	if got.EffectiveDnsPresetId != "dns_builtin_ru_split" || got.DnsSource != adminv1.DnsSource_DNS_SOURCE_DEFAULT {
		t.Fatalf("bob after the group cleared: %v", got)
	}

	// the list carries the same fields
	list := must(e.s.ListUsers(ctx, req(&adminv1.ListUsersRequest{}))).Msg.Users
	for _, lu := range list {
		if lu.EffectiveDnsPresetId == "" || lu.EffectiveDnsPresetName == "" {
			t.Errorf("list row %s has no effective preset", lu.Name)
		}
	}
}
