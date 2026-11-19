package access

import (
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Services that stand on one TCP port of a node and are told apart by the name in the ClientHello (plugin.TCPSharePlan): what the
// panel lets stand together, says no to (sni_taken, share_needs_site, share_unsupported, share_agent_old), and what it warns of.

type shareFix struct {
	*env
	node string
}

// errorParam is one value of a coded refusal ("sni_taken: name=...&share=1").
func errorParam(err error, key string) string {
	_, query, _ := strings.Cut(strings.TrimPrefix(err.Error(), connect.CodeOf(err).String()+": "), ": ")
	v, _ := url.ParseQuery(query)
	return v.Get(key)
}

func newShareFix(t *testing.T, caps string) *shareFix {
	t.Helper()
	e := newEnv(t)
	e.s.reg = builtin.Registry()
	e.sql(`INSERT INTO node (id, name, address, state, agent_caps, created_at) VALUES ('nod_de1', 'de1', 'de1.example.com', 'active', ?, 1)`, caps)
	return &shareFix{env: e, node: "nod_de1"}
}

func (f *shareFix) profileOf(protocol, name, settings string) *adminv1.ProfileSummary {
	f.t.Helper()
	return must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: protocol, Name: name, SettingsJson: settings}))).Msg.Profile
}

// add puts the profile on the node with the name the admin wrote for it there.
func (f *shareFix) add(p *adminv1.ProfileSummary, sni string) (*adminv1.CreateInboundResponse, error) {
	f.t.Helper()
	r, err := f.s.CreateInbound(f.ctx, req(&adminv1.CreateInboundRequest{ProfileId: p.Id, NodeId: f.node, TlsServerNameOverride: sni}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

// must adds a profile that has to be accepted and returns the inbound id.
func (f *shareFix) mustAdd(p *adminv1.ProfileSummary, sni string) string {
	f.t.Helper()
	r, err := f.add(p, sni)
	if err != nil {
		f.t.Fatalf("add %s: %v", p.Name, err)
	}
	return r.Inbound.Id
}

func warningCodes(r *adminv1.CreateInboundResponse) []string {
	var out []string
	for _, w := range r.Warnings {
		out = append(out, w.Code)
	}
	return out
}

const (
	shareCaps   = "vless/1 tcp-share/1"
	reality443  = `{"port":443}`                                                       // REALITY on the node's own site
	otherSite   = `{"port":443,"reality":{"target":"www.example.org:443"}}`            // REALITY on another site: its name is the target's
	mtDecoy     = `{"port":443}`                                                       // MTProto with the node's decoy: the inbound's own name
	mtSite      = `{"port":443,"fake_tls":{"kind":"site","domain":"www.example.net"}}` // MTProto showing another site
	ttOn443Name = `{"port":443,"h3":false,"sni":"tt.example.com","tls_mode":"acme_domain","egress":"direct"}`
)

func TestShareTCPPort(t *testing.T) {
	t.Run("two services with their own names stand together, and the admin is told", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		f.mustAdd(f.profileOf("vless", "reality", reality443), "")
		r, err := f.add(f.profileOf("mtproto", "telegram", mtDecoy), "tg.example.com")
		if err != nil {
			t.Fatalf("MTProto beside REALITY under its own name: %v", err)
		}
		if got := warningCodes(r); len(got) != 1 || got[0] != "tcp_port_shared" {
			t.Fatalf("warnings = %v", got)
		}
		w := r.Warnings[0].Params
		// the default is the lower of two random ids: either of the two
		if w["port"] != "443" || w["node"] != "de1" || (w["default"] != "reality" && w["default"] != "telegram") || w["restarts"] != "reality" || !strings.Contains(w["profiles"], "telegram") {
			t.Fatalf("tcp_port_shared params = %v", w)
		}
		// a third joins: the two that run are not touched, so nobody is named as restarting
		r, err = f.add(f.profileOf("trusttunnel", "tt", ttOn443Name), "")
		if err != nil {
			t.Fatalf("a third service: %v", err)
		}
		if _, restarts := r.Warnings[0].Params["restarts"]; restarts {
			t.Fatalf("a third service restarts nobody but itself: %v", r.Warnings[0].Params)
		}
	})

	t.Run("the same name twice is refused, the other service named", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		f.mustAdd(f.profileOf("vless", "reality", reality443), "")
		// MTProto's own name is the node's address too: the name REALITY's site has
		_, err := f.add(f.profileOf("mtproto", "telegram", mtDecoy), "")
		wantCoded(t, err, connect.CodeAlreadyExists, "sni_taken", map[string]string{"port": "443", "node": "de1", "name": "de1.example.com", "profile": "reality", "free": "8443", "share": "1", "suggest": "cdn.de1.example.com"})
		if n := f.count(`SELECT count(*) FROM inbound`); n != 1 {
			t.Fatalf("a refused inbound was written: %d", n)
		}
	})

	t.Run("the name suggested is not one a service of the port answers to, and a node known by an address has none", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		f.mustAdd(f.profileOf("vless", "reality", reality443), "")
		f.mustAdd(f.profileOf("trusttunnel", "tt", ttOn443Name), "cdn.de1.example.com")
		_, err := f.add(f.profileOf("mtproto", "telegram", mtDecoy), "")
		wantCoded(t, err, connect.CodeAlreadyExists, "sni_taken", map[string]string{"name": "de1.example.com", "share": "1", "suggest": "static.de1.example.com"})

		ip := newShareFix(t, shareCaps)
		ip.sql(`UPDATE node SET address = '203.0.113.7' WHERE id = 'nod_de1'`)
		ip.mustAdd(ip.profileOf("trusttunnel", "tt", ttOn443Name), "")
		_, err = ip.add(ip.profileOf("mtproto", "telegram", mtDecoy), "tt.example.com")
		wantCoded(t, err, connect.CodeAlreadyExists, "sni_taken", map[string]string{"share": "1"})
		if got := errorParam(err, "suggest"); got != "" {
			t.Fatalf("a node known by an IP address has no name to suggest: %q", got)
		}
	})

	t.Run("a service whose name is not the admin's to set is not offered a new one", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		f.mustAdd(f.profileOf("mtproto", "telegram", mtSite), "")
		// REALITY of another site answers to the target's name, which the name on the node does not change
		_, err := f.add(f.profileOf("vless", "other", `{"port":443,"reality":{"target":"www.example.net:443"}}`), "")
		wantCoded(t, err, connect.CodeAlreadyExists, "sni_taken", map[string]string{"name": "www.example.net", "profile": "telegram"})
		if got := errorParam(err, "share"); got != "" {
			t.Fatalf("share = %q", got)
		}
	})

	t.Run("the name is compared as the node says it: case and a dot do not tell two apart", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		f.mustAdd(f.profileOf("trusttunnel", "tt", ttOn443Name), "")
		_, err := f.add(f.profileOf("mtproto", "telegram", mtDecoy), "TT.Example.com.")
		wantCoded(t, err, connect.CodeAlreadyExists, "sni_taken", map[string]string{"name": "tt.example.com", "profile": "tt"})
	})

	t.Run("a switched-off neighbour keeps its name", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		in := f.mustAdd(f.profileOf("trusttunnel", "tt", ttOn443Name), "")
		off := false
		must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in, Enabled: &off})))
		_, err := f.add(f.profileOf("mtproto", "telegram", mtDecoy), "tt.example.com")
		wantCoded(t, err, connect.CodeAlreadyExists, "sni_taken", map[string]string{"name": "tt.example.com", "profile": "tt"})
		if _, err := f.add(f.profileOf("mtproto", "telegram2", mtDecoy), "tg.example.com"); err != nil {
			t.Fatalf("a name of its own beside the switched-off one: %v", err)
		}
	})

	t.Run("the CDN host of a VLESS TLS inbound is a name too", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		f.mustAdd(f.profileOf("vless", "cdn", `{"port":443,"security":"tls","tls_mode":"acme_domain","transport":"ws","ws":{"path":"/w"},"cdn":{"host":"cdn.example.com","port":443}}`), "origin.example.com")
		_, err := f.add(f.profileOf("mtproto", "telegram", mtDecoy), "cdn.example.com")
		wantCoded(t, err, connect.CodeAlreadyExists, "sni_taken", map[string]string{"name": "cdn.example.com", "profile": "cdn"})
		if _, err := f.add(f.profileOf("mtproto", "telegram2", mtDecoy), "tg.example.com"); err != nil {
			t.Fatalf("a name of its own beside the CDN inbound: %v", err)
		}
	})

	t.Run("no service shows the node's own site", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		f.mustAdd(f.profileOf("vless", "other site", otherSite), "")
		_, err := f.add(f.profileOf("mtproto", "telegram", mtSite), "")
		wantCoded(t, err, connect.CodeFailedPrecondition, "share_needs_site", map[string]string{"port": "443", "node": "de1", "profile": "other site"})
	})

	t.Run("a service of another site beside one with the node's own is allowed, with a warning that two faces show", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		f.mustAdd(f.profileOf("trusttunnel", "tt", ttOn443Name), "")
		r, err := f.add(f.profileOf("vless", "other site", otherSite), "")
		if err != nil {
			t.Fatal(err)
		}
		got := warningCodes(r)
		if len(got) != 2 || got[0] != "tcp_port_shared" || got[1] != "share_two_faces" || r.Warnings[1].Params["profile"] != "other site" || r.Warnings[1].Params["default"] != "tt" {
			t.Fatalf("warnings = %v %v", got, r.Warnings)
		}
	})

	t.Run("another port and a UDP port are no neighbours", func(t *testing.T) {
		f := newShareFix(t, shareCaps)
		f.mustAdd(f.profileOf("trusttunnel", "tt", ttOn443Name), "")
		if _, err := f.add(f.profileOf("mtproto", "telegram", `{"port":8443}`), ""); err != nil {
			t.Fatalf("another port: %v", err)
		}
		if _, err := f.add(f.profileOf("hysteria2", "hy2", `{"port":443}`), ""); err != nil {
			t.Fatalf("Hysteria2 on UDP 443 beside TCP 443: %v", err)
		}
	})
}

func TestShareNeedsAnAgentThatCan(t *testing.T) {
	t.Run("an agent that lists its capabilities without tcp-share/1 is refused", func(t *testing.T) {
		f := newShareFix(t, "vless/1 mtproto/1")
		f.mustAdd(f.profileOf("vless", "reality", reality443), "")
		_, err := f.add(f.profileOf("mtproto", "telegram", mtDecoy), "tg.example.com")
		wantCoded(t, err, connect.CodeFailedPrecondition, "share_agent_old", map[string]string{"port": "443", "node": "de1", "profile": "reality"})
	})
	t.Run("a node that has not said hello yet is not", func(t *testing.T) {
		f := newShareFix(t, "")
		f.mustAdd(f.profileOf("vless", "reality", reality443), "")
		if _, err := f.add(f.profileOf("mtproto", "telegram", mtDecoy), "tg.example.com"); err != nil {
			t.Fatalf("a new node without a Hello: %v", err)
		}
	})
	t.Run("an old agent still takes a service of a port of its own", func(t *testing.T) {
		f := newShareFix(t, "vless/1")
		f.mustAdd(f.profileOf("vless", "reality", reality443), "")
		if _, err := f.add(f.profileOf("mtproto", "telegram", `{"port":8443}`), ""); err != nil {
			t.Fatal(err)
		}
	})
}

// UpdateInbound is how a service is moved to the port of another: the override of the port and the name, checked before the click.
func TestShareByUpdatingAnInbound(t *testing.T) {
	f := newShareFix(t, shareCaps)
	f.mustAdd(f.profileOf("vless", "reality", reality443), "")
	mt := f.mustAdd(f.profileOf("mtproto", "telegram", `{"port":8443}`), "")
	port443 := uint32(443)
	// the name MTProto has is the node's: the one REALITY holds
	_, err := f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: mt, PortOverride: &port443, ValidateOnly: true}))
	wantCoded(t, err, connect.CodeAlreadyExists, "sni_taken", map[string]string{"name": "de1.example.com", "profile": "reality"})
	name := "tg.example.com"
	r, err := f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: mt, PortOverride: &port443, TlsServerNameOverride: &name, ValidateOnly: true}))
	if err != nil {
		t.Fatalf("moving beside REALITY under a name of its own: %v", err)
	}
	if len(r.Msg.Warnings) != 1 || r.Msg.Warnings[0].Code != "tcp_port_shared" {
		t.Fatalf("warnings = %v", r.Msg.Warnings)
	}
	// nothing was written by the check
	if got := f.count(`SELECT count(*) FROM inbound WHERE port_override = 443`); got != 0 {
		t.Fatalf("the check wrote the port: %d", got)
	}
	// a switched-off inbound is not judged; switching it on is
	off := false
	must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: mt, Enabled: &off})))
	must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: mt, PortOverride: &port443})))
	on := true
	_, err = f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{InboundId: mt, Enabled: &on}))
	wantCoded(t, err, connect.CodeAlreadyExists, "sni_taken", map[string]string{"name": "de1.example.com"})
}

// The panel's own protocols always give an inbound a name, so a service that has none is made by hand here: the one that cannot be
// told apart is named, whether it is the one asked for or a neighbour, and an unknown protocol keeps the port to itself.
func TestShareRefusalOfAServiceWithNoName(t *testing.T) {
	spec := func(id, protocol, settings string) plugin.InboundSpec {
		return plugin.InboundSpec{ID: id, Protocol: protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSAcmeDomain, ServerName: "tt.example.com"}, Settings: []byte(settings)}
	}
	neighbour := func(profile string, s plugin.InboundSpec) nodeListen {
		return nodeListen{profile: profile, protocol: s.Protocol, enabled: true, l: s.Listen, spec: s}
	}
	byIP := spec("inb_b", "vless", `{"security":"reality","reality":{"target":"192.0.2.9:443"}}`)
	tt := spec("inb_a", "trusttunnel", `{}`)

	err := shareRefusal(byIP, "de1", []nodeListen{neighbour("tt", tt)}, portSubject{Profile: "by ip"}, func() uint16 { return 8443 })
	wantCoded(t, err, connect.CodeFailedPrecondition, "share_unsupported", map[string]string{"port": "443", "node": "de1", "profile": "by ip", "protocol": "vless", "free": "8443"})
	err = shareRefusal(tt, "de1", []nodeListen{neighbour("by ip", byIP)}, portSubject{Profile: "tt"}, func() uint16 { return 0 })
	wantCoded(t, err, connect.CodeFailedPrecondition, "share_unsupported", map[string]string{"port": "443", "profile": "by ip", "protocol": "vless"})

	// a protocol that cannot share is no peer: the old answer, port_taken, comes from the clash check
	fake := spec("inb_c", "fake", `{}`)
	if peers := sharePeers(tt, []nodeListen{neighbour("fake", fake)}); len(peers) != 0 {
		t.Fatalf("a protocol that cannot share is a peer: %v", peers)
	}
	// a relay's forward has no inbound behind it
	if peers := sharePeers(tt, []nodeListen{{profile: "relay", protocol: relayProtocol, enabled: true, l: tt.Listen}}); len(peers) != 0 {
		t.Fatalf("a relay is a peer: %v", peers)
	}
}
