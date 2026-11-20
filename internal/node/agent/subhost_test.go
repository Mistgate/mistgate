package agent

import (
	"context"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

func withSubHostPort(t *testing.T, port uint16) {
	t.Helper()
	old := subHostPort
	subHostPort = port
	t.Cleanup(func() { subHostPort = old })
}

// The host role on the agent with real engines, on loopback: the subscription name gets a route of the shared port, beside
// an inbound's site or the agent's own; without the panel it is answered by the site, never reset; a role the inbounds do
// not fit installs nothing and says why; without the role nothing of it is left.
func TestSubHostRoleOnTheAgent(t *testing.T) {
	r := newShareRig(t)
	withSubHostPort(t, r.port)
	a := r.h.a
	role := &pb.SubHostRole{Names: []string{"sub.example.test"}, Path: "/sub-host-bridge-path", SiteName: "site.example.test"}
	a.applySubHost(&pb.NodeSettings{SubHost: role})

	// One TrustTunnel inbound: its site is the default, the host's route makes the port shared.
	r.wantRunning(r.apply(r.tt("inb_a", "tt.example.test")), "inb_a")
	if sp := r.front(); sp == nil || sp.def != "inb_a" {
		t.Fatalf("front %+v, want the inbound's site as the default", sp)
	}
	if got := r.cn("sub.example.test"); got != "tt.example.test" {
		t.Fatalf("subscription name without the panel answered by %q, want the node's site", got)
	}
	if st := a.subHostStatus(); st == nil || st.Error != "" || st.PanelConns != 0 || st.SitePinSha256 != "" {
		t.Fatalf("status %+v", st)
	}
	if cfg := a.hostEntry.Serving(); cfg.Path != role.Path || cfg.Peer != 1 || !cfg.PassClient {
		t.Fatalf("host entry %+v", cfg)
	}

	// Nothing else on the port: the agent's own site, for the site name and for strangers.
	r.apply()
	if sp := r.front(); sp == nil || sp.def != plugin.SubHostSiteRouteID {
		t.Fatalf("front %+v, want the agent's site as the default", sp)
	}
	for _, sni := range []string{"site.example.test", "unknown.example.test", "sub.example.test"} {
		if got := r.cn(sni); got != "site.example.test" {
			t.Fatalf("%s answered by %q", sni, got)
		}
	}
	if st := a.subHostStatus(); st.Error != "" || st.SitePinSha256 == "" {
		t.Fatalf("status %+v, want the site's pin", st)
	}

	// REALITY with someone else's site does not fit: no route, the inbound runs alone, the status says why.
	foreign := r.spec("inb_r", "vless", "", `{"security":"reality","reality":{"target":"www.example.org:443","server_names":["www.example.org"],"private_key":"aGVsbG8taGVsbG8taGVsbG8taGVsbG8taGVsbG8taGU","short_ids":[""]}}`,
		&pb.Credential{CredId: "crd_r", UserId: "alice", DeviceId: "dev", DataJson: `{"id":"66ad4540-b58c-4ad2-9926-ea63445a9b57"}`})
	r.apply(foreign)
	if st := a.subHostStatus(); st == nil || st.Error != plugin.SubHostRealityForeign {
		t.Fatalf("status %+v, want %s", st, plugin.SubHostRealityForeign)
	}
	r.wantNoFront()

	// The role goes: nothing of it is left.
	a.applySubHost(&pb.NodeSettings{})
	r.apply(r.tt("inb_a", "tt.example.test"))
	r.wantNoFront()
	if st := a.subHostStatus(); st != nil {
		t.Fatalf("status %+v without a role", st)
	}
	if cfg := a.hostEntry.Serving(); cfg.Path != "" {
		t.Fatal("the host entry still serves")
	}
}

// A host freshly installed over SSH has its role in agent.json and no state: the agent routes at its start, before the panel
// sent anything (its first session runs through that role), and a sweep at revision 0 keeps it.
func TestSubHostRoleRoutesBeforeAnyState(t *testing.T) {
	r := newShareRig(t)
	withSubHostPort(t, r.port)
	a := r.h.a
	role := &pb.SubHostRole{Names: []string{"sub.example.test"}, Path: "/sub-host-bridge-path", SiteName: "site.example.test"}
	a.applySubHost(&pb.NodeSettings{SubHost: role})
	if m := a.snapshotModel(); m != nil && m.revision != 0 {
		t.Fatalf("precondition: revision %d", m.revision)
	}
	a.restore(context.Background())
	if sp := r.front(); sp == nil || sp.def != plugin.SubHostSiteRouteID {
		t.Fatalf("front after restore %+v, want the agent's site", sp)
	}
	if cfg := a.hostEntry.Serving(); cfg.Path != role.Path {
		t.Fatal("the host entry does not serve after restore")
	}
	if got := r.cn("site.example.test"); got != "site.example.test" {
		t.Fatalf("site answered by %q", got)
	}
	a.handle(context.Background(), job{sweep: true})
	if sp := r.front(); sp == nil || sp.def != plugin.SubHostSiteRouteID {
		t.Fatalf("front after a sweep %+v", sp)
	}
}
