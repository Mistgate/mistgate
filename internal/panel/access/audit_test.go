package access

import (
	"encoding/json"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Whatever people (and their tokens) change in users, groups, profiles and profiles on nodes is in the audit log, with
// the names the owner reads ("deleted the user Marina", "added hy2 · 443 to de1"), and never a secret.
func TestPeoplesChangesAreAudited(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.profile("hy2 443", "")
	in := e.inbound(p.Id, "nod_de1")
	g := e.group("friends", p.Id)
	created := e.user("Марина", g, nil)
	u := created.User
	newName, port, off := "Марина К.", uint32(8443), false
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, Name: &newName, QuotaBytes: new(uint64)})))
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{u.Id}, Enabled: false})))
	must(e.s.SetUsersEnabled(e.ctx, req(&adminv1.SetUsersEnabledRequest{UserIds: []string{u.Id}, Enabled: true})))
	must(e.s.ExtendUsers(e.ctx, req(&adminv1.ExtendUsersRequest{UserIds: []string{u.Id}, Days: 30})))
	gName := "close friends"
	must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: g, Name: &gName})))
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, PortOverride: &port})))
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, Enabled: &off})))
	before := len(must(e.st.ListAudit(e.ctx, "", 0, 100)))
	pName := "hy2 main"
	must(e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, Name: &pName, DryRun: true})))
	if after := len(must(e.st.ListAudit(e.ctx, "", 0, 100))); after != before {
		t.Error("a dry run of a profile change was audited")
	}
	must(e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, Name: &pName})))
	must(e.s.DeleteUsers(e.ctx, req(&adminv1.DeleteUsersRequest{UserIds: []string{u.Id}})))
	must(e.s.DeleteInbound(e.ctx, req(&adminv1.DeleteInboundRequest{InboundId: in.Id})))
	must(e.s.DeleteGroup(e.ctx, req(&adminv1.DeleteGroupRequest{GroupId: g})))
	must(e.s.DeleteProfile(e.ctx, req(&adminv1.DeleteProfileRequest{ProfileId: p.Id})))

	rows := must(e.st.ListAudit(e.ctx, "", 0, 100))
	want := []struct {
		action string
		params map[string]any
	}{
		{"profile_create", map[string]any{"name": "hy2 443", "protocol": "hysteria2"}},
		{"inbound_add", map[string]any{"profile": "hy2 443", "node": "de1"}},
		{"group_create", map[string]any{"name": "friends"}},
		{"user_create", map[string]any{"name": "Марина"}},
		{"user_update", map[string]any{"name": "Марина К.", "fields": "name,quota"}},
		{"users_disable", map[string]any{"count": float64(1), "names": "Марина К."}},
		{"users_enable", map[string]any{"count": float64(1), "names": "Марина К."}},
		{"users_extend", map[string]any{"count": float64(1), "days": float64(30)}},
		{"group_update", map[string]any{"name": "close friends"}},
		{"inbound_update", map[string]any{"profile": "hy2 443", "node": "de1", "port": float64(8443), "enabled": true}},
		{"inbound_update", map[string]any{"node": "de1", "enabled": false}},
		{"profile_update", map[string]any{"name": "hy2 main", "settings_changed": false}},
		{"user_delete", map[string]any{"count": float64(1), "names": "Марина К."}},
		{"inbound_remove", map[string]any{"profile": "hy2 main", "node": "de1"}},
		{"group_delete", map[string]any{"name": "close friends"}},
		{"profile_delete", map[string]any{"name": "hy2 main", "protocol": "hysteria2"}},
	}
	var got []store.AuditRow
	for i := len(rows) - 1; i >= 0; i-- { // oldest first, as they happened
		got = append(got, rows[i])
	}
	if len(got) != len(want) {
		var names []string
		for _, r := range got {
			names = append(names, r.Action)
		}
		t.Fatalf("audit rows: %v", names)
	}
	token := created.SubscriptionUrl[strings.LastIndex(created.SubscriptionUrl, "/")+1:]
	for i, w := range want {
		r := got[i]
		var params map[string]any
		if err := json.Unmarshal([]byte(r.Params), &params); err != nil {
			t.Fatalf("%s: params %q: %v", r.Action, r.Params, err)
		}
		if r.Action != w.action || r.Result != "ok" || r.Actor == "" {
			t.Errorf("row %d: %+v, want %s", i, r, w.action)
		}
		for k, v := range w.params {
			if params[k] != v {
				t.Errorf("%s: %s = %v, want %v (%s)", r.Action, k, params[k], v, r.Params)
			}
		}
		if strings.Contains(r.Params, token) || strings.Contains(r.Params, created.PagePassword) && created.PagePassword != "" {
			t.Errorf("%s carries a secret: %s", r.Action, r.Params)
		}
	}

	// A change made with a token is attributed to the token's channel.
	g2 := e.group("api")
	must(e.s.DeleteGroup(store.WithAuditSource(e.ctx, store.AuditMCP), req(&adminv1.DeleteGroupRequest{GroupId: g2})))
	if top := must(e.st.ListAudit(e.ctx, "", 0, 1)); top[0].Action != "group_delete" || top[0].Source != store.AuditMCP {
		t.Errorf("token's change: %+v", top)
	}
}

func TestNameListIsShort(t *testing.T) {
	if got := nameList([]string{"a", "b", "c", "d", "e", "f", "g"}); got != "a, b, c, d, e, …" {
		t.Errorf("nameList = %q", got)
	}
	if got := nameList([]string{"Марина"}); got != "Марина" {
		t.Errorf("nameList = %q", got)
	}
}
