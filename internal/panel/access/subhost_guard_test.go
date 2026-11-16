package access

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// TCP 443 of the subscription host carries the panel's tunnel and the subscription subdomain through the node's own
// site. REALITY with another site as its target would hand the port to that site, so it is refused on the host and
// nowhere else; REALITY whose target is the node itself, and another port, stay allowed.
func TestSubscriptionHostRefusesForeignRealityOn443(t *testing.T) {
	e := newEnv(t)
	e.s.cfg.SubscriptionHost = func(nodeID string) bool { return nodeID == "nod_host" }
	e.node("nod_host", "host", "host.example.com", "active")
	e.node("nod_other", "other", "other.example.com", "active")
	site := e.vlessProfile("vless site")
	self := e.vlessSelfProfile("vless self")

	_, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_host"}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "sub_host_reality_foreign", map[string]string{"node": "host"})
	if n := e.count(`SELECT count(*) FROM inbound`); n != 0 {
		t.Fatalf("a refused inbound was written: %d", n)
	}
	if _, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_host", PortOverride: 8443})); err != nil {
		t.Fatalf("another site on another port of the host: %v", err)
	}
	if _, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_other"})); err != nil {
		t.Fatalf("another site on a node that is not the host: %v", err)
	}
	if _, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: self.Id, NodeId: "nod_host"})); err != nil {
		t.Fatalf("the node's own site on the host: %v", err)
	}
	// The same profile turned to another site later, port unchanged: refused while it sits on the host's 443.
	settings := `{"reality":{"target_kind":"site","target":"www.example.org:443"}}`
	ver := must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: self.Id}))).Msg.Profile.Version
	_, err = e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: self.Id, ExpectedVersion: ver, SettingsJson: &settings}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "sub_host_reality_foreign", map[string]string{"node": "host"})
}
