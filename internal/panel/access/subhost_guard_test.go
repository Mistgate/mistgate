package access

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// TCP 443 of the subscription host must stay the node's own site: REALITY with another site as its target there is refused on
// the host and nowhere else; REALITY whose target is the node itself, and another port, stay allowed; a profile turned to
// another site later is refused while it sits on the host's 443; and the inbound that keeps the host fit cannot go.
func TestSubscriptionHostRefusesWhatMakesItUnfit(t *testing.T) {
	e := newEnv(t)
	e.s.SetSubHostRoles(func(nodeID string) (plugin.SubHostRole, bool) {
		return plugin.SubHostRole{Names: []string{"sub.example.com"}, Path: "/p"}, nodeID == "nod_host"
	})
	e.node("nod_host", "host", "host.example.com", "active")
	e.node("nod_other", "other", "other.example.com", "active")
	site := e.vlessProfile("vless site")
	self := e.vlessSelfProfile("vless self")

	unfit := map[string]string{"node": "host", "reason": plugin.SubHostRealityForeign}
	_, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_host"}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "sub_host_inbound_unfit", unfit)
	if n := e.count(`SELECT count(*) FROM inbound`); n != 0 {
		t.Fatalf("a refused inbound was written: %d", n)
	}
	if _, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_host", PortOverride: 8443})); err != nil {
		t.Fatalf("another site on another port of the host: %v", err)
	}
	if _, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_other"})); err != nil {
		t.Fatalf("another site on a node that is not the host: %v", err)
	}
	created, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: self.Id, NodeId: "nod_host"}))
	if err != nil {
		t.Fatalf("the node's own site on the host: %v", err)
	}
	settings := `{"reality":{"target_kind":"site","target":"www.example.org:443"}}`
	ver := must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: self.Id}))).Msg.Profile.Version
	_, err = e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: self.Id, ExpectedVersion: ver, SettingsJson: &settings}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "sub_host_inbound_unfit", unfit)

	// The host's only site on 443 cannot be removed or switched off: the role would have no site to fall back to.
	_, err = e.s.DeleteInbound(e.ctx, req(&adminv1.DeleteInboundRequest{InboundId: created.Msg.Inbound.Id}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "sub_host_inbound_unfit", map[string]string{"node": "host", "reason": plugin.SubHostSiteNameRequired})
	off := false
	_, err = e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: created.Msg.Inbound.Id, Enabled: &off}))
	wantCoded(t, err, connect.CodeFailedPrecondition, "sub_host_inbound_unfit", map[string]string{"node": "host", "reason": plugin.SubHostSiteNameRequired})
}
