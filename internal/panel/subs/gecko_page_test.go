package subs_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// geckoRig: node nod_1 runs the plain Hysteria2 profile and a Gecko one (only Mihomo apps speak Gecko), node nod_2 runs the
// Gecko one alone. Two groups: both profiles, and the Gecko one alone.
func geckoRig(t *testing.T) (r *rig, both, geckoOnly string) {
	t.Helper()
	r = newRig(t, "/k3xq8")
	r.st.W.Exec(`INSERT INTO node (id, name, address, country_code, state, created_at) VALUES ('nod_2', 'inner-nl1', 'nl1.example.com', 'NL', 'active', 1)`)
	gk := must(r.svc.CreateProfile(r.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "gecko", SettingsJson: `{"obfs":{"type":"gecko"},"port":8444}`}))).Msg.Profile
	for _, n := range []string{"nod_1", "nod_2"} {
		must(r.svc.CreateInbound(r.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: gk.Id, NodeId: n})))
	}
	group := func(name string, ids ...string) string {
		return must(r.svc.CreateGroup(r.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: name, ProfileIds: ids}))).Msg.Group.Id
	}
	return r, group("both", r.profile, gk.Id), group("gecko", gk.Id)
}

// The page lists every node the person can use, a Gecko one included, marks what only Mihomo apps can use, and counts
// nodes: two profiles on one node are one server.
func TestPageListsNodesOnlyMihomoAppsCanUse(t *testing.T) {
	r, both, _ := geckoRig(t)
	h, _ := r.handler(nil)
	_, tok := r.user("alice", func(c *adminv1.CreateUserRequest) { c.GroupId = both })
	d, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())

	if len(arr(d["servers"])) != 2 || d["server_count"] != float64(2) {
		t.Fatalf("servers = %v, server_count = %v, want the two nodes", d["servers"], d["server_count"])
	}
	conns := func(id string) (all, mihomo int) {
		for _, c := range arr(obj(serverByID(d, id))["connections"]) {
			all++
			if obj(c)["mihomo_only"] == true {
				mihomo++
			}
		}
		return
	}
	if all, m := conns("nod_1"); all != 2 || m != 1 {
		t.Errorf("nod_1: %d connections, %d for Mihomo apps only, want 2 and 1", all, m)
	}
	if all, m := conns("nod_2"); all != 1 || m != 1 {
		t.Errorf("nod_2: %d connections, %d for Mihomo apps only, want 1 and 1", all, m)
	}
	if names := strs(obj(serverByID(d, "nod_2"))["app_names"]); len(names) != 0 {
		t.Errorf("nod_2 app_names = %v: Happ lists nothing of it", names)
	}
}

// A person whose every server is Gecko gets a Happ list with one entry that says why, not an empty list.
func TestListOfGeckoOnlyUserSaysWhy(t *testing.T) {
	r, _, geckoOnly := geckoRig(t)
	h, _ := r.handler(nil)
	_, tok := r.user("alice", func(c *adminv1.CreateUserRequest) { c.GroupId = geckoOnly })
	rec := fetch(h, "/"+tok, happUA)
	lines := strings.Split(decode(t, rec.Body.String()), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "hysteria2://off@0.0.0.0:1/#") || !strings.Contains(lines[0], "Mihomo") {
		t.Fatalf("list = %q, want the placeholder that names the reason", lines)
	}
	// The page still lists both nodes.
	d, _ := pageData(t, fetch(h, "/"+tok, chrome).Body.String())
	if len(arr(d["servers"])) != 2 || d["server_count"] != float64(2) {
		t.Errorf("servers = %v, server_count = %v", d["servers"], d["server_count"])
	}
}

func TestListOmitsGeckoPlaceholderWhenOrdinaryURIExists(t *testing.T) {
	r, both, _ := geckoRig(t)
	h, _ := r.handler(nil)
	_, tok := r.user("alice", func(c *adminv1.CreateUserRequest) { c.GroupId = both })
	lines := strings.Split(decode(t, fetch(h, "/"+tok, happUA).Body.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "de1.example.com") || strings.Contains(lines[0], "0.0.0.0") {
		t.Fatalf("list = %q, want the ordinary server and no Gecko placeholder", lines)
	}
}
