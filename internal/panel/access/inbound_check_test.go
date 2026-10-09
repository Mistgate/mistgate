package access

import (
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The add-profile dialog asks CreateInbound / UpdateInbound with validate_only on every change: the same refusals as the
// real call, as codes with their values, and nothing written.

func (e *env) check(m *adminv1.CreateInboundRequest) (*adminv1.CreateInboundResponse, error) {
	m.ValidateOnly = true
	r, err := e.s.CreateInbound(e.ctx, req(m))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

// wantCoded checks a coded refusal: its Connect code, the code word and its values (the message is "code: k=v&k=v").
func wantCoded(t *testing.T, err error, code connect.Code, word string, vals map[string]string) {
	t.Helper()
	wantCode(t, err, code)
	msg := connect.CodeOf(err).String() + ": "
	head, query, _ := strings.Cut(strings.TrimPrefix(err.Error(), msg), ": ")
	if head != word {
		t.Fatalf("code word = %q, want %q (%v)", head, word, err)
	}
	got, perr := url.ParseQuery(query)
	if perr != nil {
		t.Fatalf("values of %v: %v", err, perr)
	}
	for k, v := range vals {
		if got.Get(k) != v {
			t.Errorf("%s: %s = %q, want %q (%v)", word, k, got.Get(k), v, err)
		}
	}
}

func TestInboundCheckBeforeTheClick(t *testing.T) {
	e := newEnv(t)
	e.node("nod_ip", "ip1", "203.0.113.10", "active")
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_old", "old1", "old1.example.com", "retired")
	main := e.profile("main", "") // Let's Encrypt, UDP 443
	inbounds := func() int { return e.count(`SELECT count(*) FROM inbound`) }
	events := func() int { return e.count(`SELECT count(*) FROM event`) }
	in0, ev0 := inbounds(), events()
	e.resetNotify()

	// An IP node and a Let's Encrypt profile: refused before the click, with the address to show.
	_, err := e.check(&adminv1.CreateInboundRequest{ProfileId: main.Id, NodeId: "nod_ip"})
	wantCoded(t, err, connect.CodeInvalidArgument, "acme_needs_domain", map[string]string{"address": "203.0.113.10", "node": "ip1"})
	// A domain for this node makes it pass, and the answer is the inbound as it would be.
	ok, err := e.check(&adminv1.CreateInboundRequest{ProfileId: main.Id, NodeId: "nod_ip", TlsServerNameOverride: "vpn.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Inbound.TlsServerName != "vpn.example.com" || ok.Inbound.Port != 443 || ok.Inbound.NodeName != "ip1" || len(ok.Warnings) != 0 {
		t.Errorf("checked inbound = %+v, warnings %v", ok.Inbound, ok.Warnings)
	}
	// An IP typed where Let's Encrypt wants a domain is the field's problem, said as such.
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: main.Id, NodeId: "nod_ip", TlsServerNameOverride: "203.0.113.10"})
	wantCoded(t, err, connect.CodeInvalidArgument, "sni_needs_domain", map[string]string{"name": "203.0.113.10"})
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: main.Id, NodeId: "nod_ip", TlsServerNameOverride: "not a domain"})
	wantCoded(t, err, connect.CodeInvalidArgument, "sni_needs_domain", map[string]string{"name": "not a domain"})
	// The node's own domain is the effective name when nothing is typed.
	if ok, err = e.check(&adminv1.CreateInboundRequest{ProfileId: main.Id, NodeId: "nod_de1"}); err != nil || ok.Inbound.TlsServerName != "de1.example.com" {
		t.Fatalf("domain node: %+v, %v", ok, err)
	}
	if inbounds() != in0 || events() != ev0 || e.notify.n.Load() != 0 {
		t.Fatalf("a check wrote something: inbounds %d->%d, events %d->%d, notified %d", in0, inbounds(), ev0, events(), e.notify.n.Load())
	}

	// A second Hysteria2 profile on 443 of the same node: the port is taken by "main", 8443 is free.
	e.inbound(main.Id, "nod_de1")
	second := e.profile("second", "")
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: second.Id, NodeId: "nod_de1"})
	wantCoded(t, err, connect.CodeAlreadyExists, "port_taken", map[string]string{"port": "443", "profile": "main", "node": "de1", "free": "8443", "hop": ""})
	// The real call refuses the same way.
	_, err = e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: second.Id, NodeId: "nod_de1"}))
	wantCoded(t, err, connect.CodeAlreadyExists, "port_taken", map[string]string{"port": "443", "free": "8443"})
	// Taking the suggested port passes; the check also offers a port to move to (not the one asked for).
	ok, err = e.check(&adminv1.CreateInboundRequest{ProfileId: second.Id, NodeId: "nod_de1", PortOverride: 8443})
	if err != nil || ok.Inbound.Port != 8443 || ok.FreePort == 0 || ok.FreePort == 8443 || ok.FreePort == 443 {
		t.Fatalf("on 8443: %+v, %v", ok, err)
	}
	// The same profile twice is refused, and the dialog says so.
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: main.Id, NodeId: "nod_de1"})
	wantCoded(t, err, connect.CodeAlreadyExists, "already_on_node", nil)
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: main.Id, NodeId: "nod_old"})
	wantCoded(t, err, connect.CodeFailedPrecondition, "node_retired", nil)

	// Hop ranges: a port inside another profile's range names that range; a profile whose own range overlaps cannot be
	// fixed by a port, so there is no port to offer.
	hopper := e.profile("hopper", `{"port":10000,"hop":{"from":20000,"to":30000}}`)
	e.inbound(hopper.Id, "nod_de1")
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: second.Id, NodeId: "nod_de1", PortOverride: 25000})
	wantCoded(t, err, connect.CodeAlreadyExists, "port_taken", map[string]string{"port": "25000", "profile": "hopper", "hop": "20000-30000", "free": "8443"})
	wide := e.profile("wide", `{"port":5000,"hop":{"from":9000,"to":12000}}`) // covers hopper's 10000
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: wide.Id, NodeId: "nod_de1"})
	wantCoded(t, err, connect.CodeAlreadyExists, "hop_taken", map[string]string{"from": "9000", "to": "12000", "profile": "hopper", "node": "de1"})
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: hopper.Id, NodeId: "nod_ip", PortOverride: 25000, TlsServerNameOverride: "vpn.example.com"})
	wantCoded(t, err, connect.CodeInvalidArgument, "port_in_hop", map[string]string{"from": "20000", "to": "30000"})

	// A taken first choice is skipped: with 8443 taken as well, the offer is the next candidate.
	must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: second.Id, NodeId: "nod_de1", PortOverride: 8443})))
	third := e.profile("third", "")
	_, err = e.check(&adminv1.CreateInboundRequest{ProfileId: third.Id, NodeId: "nod_de1"})
	wantCoded(t, err, connect.CodeAlreadyExists, "port_taken", map[string]string{"port": "443", "profile": "main", "free": "4443"})
}

// "Change the port" of an inbound: a check of the inbound as it is answers with a port to move to, and a check of a change
// refuses what the real change would refuse, writing nothing.
func TestInboundUpdateCheck(t *testing.T) {
	f := newFixture(t) // "hy2 443" on de1
	e := f.e
	other := e.profile("other", "")
	o := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: other.Id, NodeId: f.nodeID, PortOverride: 8443}))).Msg.Inbound
	e.resetNotify()

	r := must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: f.inbound, ValidateOnly: true}))).Msg
	if r.FreePort != 4443 || r.Inbound.Port != 443 {
		t.Errorf("free port = %d (8443 is taken by %q), inbound %+v", r.FreePort, o.ProfileName, r.Inbound)
	}
	_, err := e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: f.inbound, PortOverride: new(uint32(8443)), ValidateOnly: true}))
	wantCoded(t, err, connect.CodeAlreadyExists, "port_taken", map[string]string{"port": "8443", "profile": "other", "free": "4443"})
	r = must(e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: f.inbound, PortOverride: new(uint32(2053)), Enabled: new(false), ValidateOnly: true}))).Msg
	if r.Inbound.Port != 2053 {
		t.Errorf("checked port = %d", r.Inbound.Port)
	}
	var port, version int
	var enabled bool
	if err := e.st.R.QueryRow(`SELECT COALESCE(port_override, 0), spec_version, enabled FROM inbound WHERE id = ?`, f.inbound).Scan(&port, &version, &enabled); err != nil {
		t.Fatal(err)
	}
	if port != 0 || version != 1 || !enabled || e.notify.n.Load() != 0 {
		t.Errorf("a check changed the inbound: port %d version %d enabled %v, notified %d", port, version, enabled, e.notify.n.Load())
	}
}

// A WARP exit on a node without a working WARP account is said before the click, without stopping it.
func TestInboundWarpWarning(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	direct := e.profile("direct", "")
	viaWarp := e.profile("via warp", `{"port":8443,"egress":"warp"}`)
	warning := func(profileID string) string {
		t.Helper()
		r, err := e.check(&adminv1.CreateInboundRequest{ProfileId: profileID, NodeId: "nod_de1"})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Warnings) == 0 {
			return ""
		}
		return r.Warnings[0].Code + ":" + r.Warnings[0].Params["state"]
	}
	if w := warning(direct.Id); w != "" {
		t.Errorf("direct exit warned %q", w)
	}
	if w := warning(viaWarp.Id); w != "warp_missing:none" {
		t.Errorf("no account: %q", w)
	}
	acct := store.WarpAccountRow{NodeID: "nod_de1", Source: store.WarpImported, SecretEnc: []byte{1}, PeerPublicKey: "k", EndpointV4: "162.159.192.1",
		AddressV4: "172.16.0.2/32", Enabled: false, CreatedAt: e.clock, UpdatedAt: e.clock}
	if err := e.st.CreateWarpAccount(e.ctx, acct); err != nil {
		t.Fatal(err)
	}
	if w := warning(viaWarp.Id); w != "warp_missing:paused" {
		t.Errorf("paused account: %q", w)
	}
	if err := e.st.SetWarpEnabled(e.ctx, "nod_de1", true, e.clock); err != nil {
		t.Fatal(err)
	}
	if w := warning(viaWarp.Id); w != "" {
		t.Errorf("working account warned %q", w)
	}
	// the node's last report says the tunnel is down
	if err := e.st.SetWarpHealth(e.ctx, "nod_de1", `{"state":"WARP_STATE_DOWN"}`, e.clock); err != nil {
		t.Fatal(err)
	}
	if w := warning(viaWarp.Id); w != "warp_missing:down" {
		t.Errorf("tunnel down: %q", w)
	}
	if err := e.st.SetWarpHealth(e.ctx, "nod_de1", `{"state":"WARP_STATE_UP"}`, e.clock); err != nil {
		t.Fatal(err)
	}
	if w := warning(viaWarp.Id); w != "" {
		t.Errorf("tunnel up warned %q", w)
	}
	// the real call carries the warning too, and is not stopped by it
	if err := e.st.SetWarpEnabled(e.ctx, "nod_de1", false, e.clock); err != nil {
		t.Fatal(err)
	}
	r := must(e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{ProfileId: viaWarp.Id, NodeId: "nod_de1"}))).Msg
	if len(r.Warnings) != 1 || r.Inbound.Id == "" {
		t.Errorf("real call: %+v", r)
	}
}

func TestHysteriaDecoyWarningForTCPListener(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	hy2 := e.profile("hy2", "")
	e.inbound(hy2.Id, "nod_de1")
	others, _, err := e.s.nodeInbounds(e.ctx, "nod_de1", "")
	if err != nil {
		t.Fatal(err)
	}

	tcp := plugin.InboundSpec{Listen: plugin.Listen{Network: "tcp", Port: 443}}
	warnings, err := e.s.inboundWarnings(e.ctx, "nod_de1", tcp, others)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || warnings[0].Code != "hy2_decoy_moves" ||
		warnings[0].Params["port"] != "443" || warnings[0].Params["profile"] != "hy2" {
		t.Fatalf("TCP listener warning = %+v", warnings)
	}

	tcp.Listen.Port = 8443
	warnings, err = e.s.inboundWarnings(e.ctx, "nod_de1", tcp, others)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unclaimed TCP port warned: %+v", warnings)
	}
	udp := plugin.InboundSpec{Listen: plugin.Listen{Network: "udp", Port: 443}}
	warnings, err = e.s.inboundWarnings(e.ctx, "nod_de1", udp, others)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("UDP listener warned about the TCP decoy: %+v", warnings)
	}
}

// An AmneziaWG check builds the inbound with a throwaway key: no key is kept, and a key parked by a removal stays parked
// for the real re-add.
func TestInboundCheckKeepsAWGKeys(t *testing.T) {
	f := newAWGFixture(t)
	e := f.e
	f.dropInbound(f.inbound)
	if n := e.count(`SELECT count(*) FROM awg_retained_key`); n != 1 {
		t.Fatalf("parked keys = %d", n)
	}
	r, err := e.check(&adminv1.CreateInboundRequest{ProfileId: f.profile, NodeId: f.nodeID})
	if err != nil {
		t.Fatal(err)
	}
	if r.Inbound.Protocol != "awg" || r.Inbound.Port == 0 {
		t.Errorf("checked inbound = %+v", r.Inbound)
	}
	if n := e.count(`SELECT count(*) FROM awg_retained_key`); n != 1 {
		t.Errorf("a check took the parked key: %d left", n)
	}
	if n := e.count(`SELECT count(*) FROM inbound`); n != 0 {
		t.Errorf("a check made an inbound: %d", n)
	}
}
