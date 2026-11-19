package access

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// A target set on a node is a site of the internet: never the node itself, another node of the fleet, the panel, or a name a service of
// the fleet holds a certificate for (reality_target_own). The names REALITY accepts at somebody else's site are not the fleet's.
func TestTargetOfTheNodeIsNotTheFleetsOwn(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	e.node("nod_ip", "ip1", "192.0.2.7", "active") // a node known by its IP address
	e.node("nod_old", "old1", "old.example.com", "retired")
	e.s.cfg.OwnHosts = []string{"https://panel.example.com/admin/", "agent.example.com", "192.0.2.50:8443"}
	site := e.vlessProfile("vless site")
	de1 := e.inbound(site.Id, "nod_de1")
	nl1 := e.inbound(site.Id, "nod_nl1")
	ip1 := e.inbound(site.Id, "nod_ip")
	// services with a certificate of their own: a site name of TrustTunnel on the IP node, a TLS VLESS behind a CDN on nl1
	tt := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "trusttunnel", Name: "tt", SettingsJson: `{"port":8443,"h3":false,"sni":"tt.example.com","tls_mode":"acme_domain","egress":"direct"}`}))).Msg.Profile
	e.inbound(tt.Id, "nod_ip")
	cdn := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "cdn", SettingsJson: `{"port":2053,"security":"tls","transport":"ws","tls_mode":"acme_domain","sni":"origin.example.com","cdn":{"host":"cdn.example.org","port":443},"ws":{"path":"/ws","host":""}}`}))).Msg.Profile
	e.inbound(cdn.Id, "nod_nl1")

	update := func(id, target string) error {
		_, err := e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: id, RealityTargetOverride: &target, ValidateOnly: true}))
		return err
	}
	own := func(err error, target, node string) {
		t.Helper()
		wantCoded(t, err, connect.CodeInvalidArgument, "reality_target_own", map[string]string{"target": target, "node": node})
	}
	// each node shows the other's address, at any port; the name is compared the way the target is cleaned
	own(update(de1.Id, "nl1.example.com:8443"), "nl1.example.com:8443", "de1")
	own(update(nl1.Id, "de1.example.com:8443"), "de1.example.com:8443", "nl1")
	own(update(de1.Id, "de1.example.com:8443"), "de1.example.com:8443", "de1") // its own name at another port
	own(update(de1.Id, " NL1.Example.com.:443 "), "nl1.example.com:443", "de1")
	// the name of a service with a certificate of its own, on a node known by its IP address
	own(update(ip1.Id, "tt.example.com:443"), "tt.example.com:443", "ip1")
	own(update(de1.Id, "tt.example.com:443"), "tt.example.com:443", "de1") // of another node: the fleet's all the same
	own(update(de1.Id, "origin.example.com:443"), "origin.example.com:443", "de1")
	own(update(de1.Id, "cdn.example.org:443"), "cdn.example.org:443", "de1")
	// the panel: its site, its agent name, an address it listens on
	own(update(de1.Id, "panel.example.com:443"), "panel.example.com:443", "de1")
	own(update(de1.Id, "agent.example.com:443"), "agent.example.com:443", "de1")
	own(update(de1.Id, "sub.example.com:443"), "sub.example.com:443", "de1") // the subscription address
	// the same on adding the profile, as a check and for real
	other := e.vlessProfile("other site")
	for _, validateOnly := range []bool{true, false} {
		_, err := e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: other.Id, NodeId: "nod_de1", RealityTargetOverride: "nl1.example.com:8443", ValidateOnly: validateOnly}))
		own(err, "nl1.example.com:8443", "de1")
	}
	if n := e.count(`SELECT count(*) FROM inbound WHERE reality_target_override != ''`); n != 0 {
		t.Fatalf("a refused target was written: %d", n)
	}

	// not the fleet's: a site of the internet, a retired node's name, and the name another REALITY server accepts (its target is a
	// stranger's site, which a second node may show too)
	for _, target := range []string{"shop.example.com:443", "old.example.com:443", "www.example.org:443"} {
		if err := update(de1.Id, target); err != nil {
			t.Errorf("%s: %v", target, err)
		}
	}
	// the inbound's own name does not count against it: setting the same site again, and putting the profile's target back
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: de1.Id, RealityTargetOverride: new("shop.example.com:443")})))
	must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: nl1.Id, RealityTargetOverride: new("shop.example.com:443")})))
	if err := update(de1.Id, "shop.example.com:443"); err != nil {
		t.Errorf("the same target again: %v", err)
	}
	if err := update(de1.Id, ""); err != nil {
		t.Errorf("reset: %v", err)
	}
}
