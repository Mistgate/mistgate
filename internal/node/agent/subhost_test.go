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

// The host role on the agent with real engines, on loopback: the subscription name and the agent's site keep their
// routes beside an inbound. Without the panel the subscription name is answered by the site; a role the inbounds
// do not fit installs nothing and says why; without the role nothing of it is left.
func TestSubHostRoleOnTheAgent(t *testing.T) {
	r := newShareRig(t)
	withSubHostPort(t, r.port)
	a := r.h.a
	role := &pb.SubHostRole{Names: []string{"sub.example.test"}, Path: "/sub-host-bridge-path", SiteName: "site.example.test"}
	a.applySubHost(&pb.NodeSettings{SubHost: role})

	// One TrustTunnel inbound: it has its own name, the configured agent site remains the default.
	r.wantRunning(r.apply(r.tt("inb_a", "tt.example.test")), "inb_a")
	if sp := r.front(); sp == nil || sp.def != plugin.SubHostSiteRouteID {
		t.Fatalf("front %+v, want the agent's site as the default", sp)
	}
	if got := r.cn("tt.example.test"); got != "tt.example.test" {
		t.Fatalf("the inbound name answered by %q, want its own site", got)
	}
	if got := r.cn("sub.example.test"); got != "site.example.test" {
		t.Fatalf("subscription name without the panel answered by %q, want the agent's site", got)
	}
	if st := a.subHostStatus(); st == nil || st.Error != "" || st.PanelConns != 0 || st.SitePinSha256 == "" {
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

func TestSubHostSiteSurvivesFailedInboundOnItsName(t *testing.T) {
	r := newShareRig(t)
	withSubHostPort(t, r.port)
	const site = "site.example.test"
	r.h.a.applySubHost(&pb.NodeSettings{SubHost: &pb.SubHostRole{
		Names: []string{"sub.example.test"}, Path: "/sub-host-bridge-path", SiteName: site,
	}})
	r.apply()
	conn := r.open(site)
	conn.get()

	in := r.wsTLS("inb_w", site)
	in.Spec.Egress = "warp"
	r.wantFailed(r.apply(in), in.InboundId, errWarpNotConfigured)
	conn.get()
	if got := r.cn(site); got != site {
		t.Fatalf("host name after a failed apply answered by %q, want %q", got, site)
	}
	r.wantFailed(r.sweep(), in.InboundId, errWarpNotConfigured)
	conn.get()

	in.Spec = r.wsTLS(in.InboundId, site).Spec
	in.Spec.SpecVersion++
	r.wantRunning(r.apply(in), in.InboundId)
	conn.get()
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

// The host's route is the intake's to mark down (the panel away): a reconcile does not bring it back up behind its back, so
// a subscription name goes straight to the site, never through the intake's admit-then-divert (L3).
func TestReconcileLeavesTheHostRouteToTheIntake(t *testing.T) {
	r := newShareRig(t)
	withSubHostPort(t, r.port)
	a := r.h.a
	a.applySubHost(&pb.NodeSettings{SubHost: &pb.SubHostRole{Names: []string{"sub.example.test"}, Path: "/sub-host-bridge-path", SiteName: "site.example.test"}})
	r.apply()
	diverted := func() uint64 {
		a.hostMu.Lock()
		defer a.hostMu.Unlock()
		return a.sh.intake.Stats().Diverted
	}
	eventually(t, func() bool { _ = r.cn("site.example.test"); return true }, "the site")
	r.apply() // a reconcile: markRoutes runs
	before := diverted()
	if got := r.cn("sub.example.test"); got != "site.example.test" {
		t.Fatalf("subscription name answered by %q", got)
	}
	if after := diverted(); after != before {
		t.Fatalf("the route was up again after a reconcile: the intake admitted and diverted (%d -> %d)", before, after)
	}
}
