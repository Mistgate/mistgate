package access

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

const tlsVLESS = `{"security":"tls","transport":"ws","tls_mode":"acme_domain","ws":{"path":"/ws","host":""}}`

// A profile cannot go from REALITY to TLS while an inbound that is switched on shows a target of its node: the refusal names the node and
// the target, so that the editor says where to reset it.
func TestProfileToTLSIsRefusedWhileAServerHasATarget(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	site := e.vlessProfile("vless site")
	in := e.inbound(site.Id, "nod_de1")
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: new("shop.example.com:443")})))

	tlsSettings := tlsVLESS
	_, err := e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: site.Id, ExpectedVersion: 1, SettingsJson: &tlsSettings}))
	wantCoded(t, err, connect.CodeInvalidArgument, "reality_target_unsupported", map[string]string{"target": "shop.example.com:443", "node": "de1"})
}

// The target of a server that is switched off can still be reset after its profile went to TLS: the old spec no longer builds, and
// that must not stop the change that makes it build again.
func TestTargetOfASwitchedOffServerCanBeResetAfterTheProfileWentToTLS(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	site := e.vlessProfile("vless site")
	in := e.inbound(site.Id, "nod_de1")
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: new("shop.example.com:443"), Enabled: new(false)})))
	tlsSettings := tlsVLESS
	must(e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: site.Id, ExpectedVersion: 1, SettingsJson: &tlsSettings})))

	// switching it on with the target still there is refused, and says why
	_, err := e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, Enabled: new(true)}))
	wantCoded(t, err, connect.CodeInvalidArgument, "reality_target_unsupported", map[string]string{"target": "shop.example.com:443", "node": "de1"})
	// resetting the target, and then switching on, works
	for _, validateOnly := range []bool{true, false} {
		if _, err := e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: new(""), ValidateOnly: validateOnly})); err != nil {
			t.Fatalf("reset (validate_only=%v): %v", validateOnly, err)
		}
	}
	if stored := must(e.st.Access().Inbound(e.ctx, in.Id)); stored.RealityTargetOverride != "" {
		t.Fatalf("stored %+v", stored)
	}
	if _, err := e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, Enabled: new(true)})); err != nil {
		t.Fatalf("switch on: %v", err)
	}
}

// The target is kept as the node gets it: the same site written another way is no change, no new spec version and no restart.
func TestTargetOfTheNodeIsKeptNormalized(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	site := e.vlessProfile("vless site")
	in := e.inbound(site.Id, "nod_de1")
	e.sql(`UPDATE inbound SET state = 'active' WHERE id = ?`, in.Id)

	set := must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: new(" Shop.Example.com.:0443 ")}))).Msg.Inbound
	stored := must(e.st.Access().Inbound(e.ctx, in.Id))
	if stored.RealityTargetOverride != "shop.example.com:443" || set.RealityTargetOverride != "shop.example.com:443" || stored.SpecVersion != 2 {
		t.Fatalf("stored %+v, answer %q", stored, set.RealityTargetOverride)
	}
	e.resetNotify()
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, RealityTargetOverride: new("SHOP.example.com:443")})))
	if stored = must(e.st.Access().Inbound(e.ctx, in.Id)); stored.SpecVersion != 2 || stored.RealityTargetOverride != "shop.example.com:443" || e.notify.n.Load() != 0 {
		t.Fatalf("the same site again: %+v (notified %d)", stored, e.notify.n.Load())
	}

	made := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: site.Id, NodeId: "nod_nl1", RealityTargetOverride: "Shop.Example.com.:443"}))).Msg.Inbound
	if made.RealityTargetOverride != "shop.example.com:443" {
		t.Fatalf("created %q", made.RealityTargetOverride)
	}
	if n := e.count(`SELECT count(*) FROM inbound WHERE reality_target_override = 'shop.example.com:443'`); n != 2 {
		t.Fatalf("stored normalized: %d", n)
	}
}
