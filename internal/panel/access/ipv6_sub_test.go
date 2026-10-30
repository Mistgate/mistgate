package access

import (
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The IPv6 entry in the subscriptions: next to each server of a node that has one, the same server at the node's IPv6 address,
// in every format. Documentation addresses only.

const (
	entryAddr    = "2001:db8::5"
	entryBracket = "[2001:db8::5]"
)

var linkFormats = []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo, plugin.FormatXrayJSON, plugin.FormatSingbox}

func (f *relayFix) entry(nodeID, addr string) {
	f.sql(`UPDATE node SET ipv6_entry = ? WHERE id = ?`, addr, nodeID)
}

func (f *relayFix) vlessWith(name, settings string) *adminv1.ProfileSummary {
	f.t.Helper()
	return must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "vless", Name: name, SettingsJson: settings}))).Msg.Profile
}

func TestIPv6TwinOfAHysteria2ServerInEveryFormat(t *testing.T) {
	f := newRelayFix(t)
	f.entry("nod_de1", entryAddr)
	token := f.tokenOf(f.user("alice", f.group("g", f.base.Id), nil).User.Id)

	v := f.twinView(token, plugin.FormatURIList)
	if len(v.Servers) != 2 || len(v.Lines) != 2 || v.Lines[1] != v.Servers[1].URI {
		t.Fatalf("servers %d, lines %d, want the server and its IPv6 twin", len(v.Servers), len(v.Lines))
	}
	srv, twin := v.Servers[0], v.Servers[1]
	if srv.Mark != "" || twin.Mark != MarkIPv6 || twin.NodeID != srv.NodeID || twin.Made != srv.Made || twin.Profile != srv.Profile || twin.CountryCode != srv.CountryCode {
		t.Errorf("server %+v, twin %+v", srv, twin)
	}
	// brackets in the URI, the name of the node as the TLS name: the certificate is the node's, whatever is dialled
	if !strings.HasPrefix(twin.URI, "hysteria2://") || !strings.Contains(twin.URI, "@"+entryBracket+":443/") || !strings.Contains(twin.URI, "sni=de1.example.com") {
		t.Errorf("URI: %s", twin.URI)
	}
	if got, want := swapAddress(withoutFragment(srv.URI), "@de1.example.com:443/", "@"+entryBracket+":443/"), withoutFragment(twin.URI); got != want {
		t.Errorf("twin\n%s\nwant\n%s", want, got)
	}
	// the page lists it as a link of the node, marked IPv6, and no second row for the node
	if len(v.Nodes) != 1 || len(v.Nodes[0].Conns) != 2 || !v.Nodes[0].Conns[1].IPv6 || v.Nodes[0].Conns[1].Relay || v.Nodes[0].Conns[1].Server != 1 || v.Nodes[0].Conns[0].IPv6 {
		t.Errorf("page connections: %+v", v.Nodes)
	}

	// no brackets outside a URI: the address as it is in Mihomo, Xray JSON and sing-box, and the same everything else
	for _, c := range []struct {
		format plugin.ClientFormat
		want   string
	}{
		{plugin.FormatMihomo, `server: "` + entryAddr + `"`},
		{plugin.FormatXrayJSON, `"address":"` + entryAddr + `"`},
		{plugin.FormatSingbox, `"server":"` + entryAddr + `"`},
	} {
		m := f.twinView(token, c.format)
		if len(m.Lines) != 2 {
			t.Fatalf("%s: %d lines", c.format, len(m.Lines))
		}
		compact := strings.ReplaceAll(m.Lines[1], ": ", ":")
		if !strings.Contains(m.Lines[1], c.want) && !strings.Contains(compact, strings.ReplaceAll(c.want, ": ", ":")) {
			t.Errorf("%s twin lacks %s:\n%s", c.format, c.want, m.Lines[1])
		}
		if strings.Contains(m.Lines[1], entryBracket) {
			t.Errorf("%s: a bracketed address outside a URI:\n%s", c.format, m.Lines[1])
		}
		if strings.Contains(m.Lines[0], entryAddr) {
			t.Errorf("%s: the plain server holds the IPv6 address:\n%s", c.format, m.Lines[0])
		}
		if got, want := strings.ReplaceAll(m.Lines[1], entryAddr, "de1.example.com"), m.Lines[0]; got != want {
			t.Errorf("%s: the twin differs from the server in more than its address:\n%s\nwant\n%s", c.format, got, want)
		}
	}
}

func TestIPv6TwinOfAVLESSServerKeepsRealityAndTheNames(t *testing.T) {
	f := newRelayFix(t)
	f.entry("nod_de1", entryAddr)
	p := f.vlessWith("vl", `{"transport":"tcp","reality":{"target":"www.example.org:443"}}`)
	f.inbound(p.Id, "nod_de1")
	token := f.tokenOf(f.user("alice", f.group("g", p.Id), nil).User.Id)

	v := f.twinView(token, plugin.FormatURIList)
	if len(v.Servers) != 2 {
		t.Fatalf("servers: %+v", v.Servers)
	}
	srv, twin := v.Servers[0], v.Servers[1]
	if got, want := swapAddress(withoutFragment(srv.URI), "@de1.example.com:443?", "@"+entryBracket+":443?"), withoutFragment(twin.URI); got != want {
		t.Errorf("twin\n%s\nwant\n%s", twin.URI, got)
	}
	// REALITY is the same: the public key, the short id and the server name are the target's, not the address's
	for _, key := range []string{"security=reality", "pbk=", "sid=", "sni=www.example.org"} {
		if !strings.Contains(twin.URI, key) {
			t.Errorf("the twin lacks %s: %s", key, twin.URI)
		}
	}
	for _, format := range linkFormats[1:] {
		m := f.twinView(token, format)
		if len(m.Lines) != 2 || !strings.Contains(m.Lines[1], entryAddr) || strings.Contains(m.Lines[1], entryBracket) || strings.Contains(m.Lines[0], entryAddr) {
			t.Errorf("%s:\n%s", format, strings.Join(m.Lines, "\n"))
			continue
		}
		if got, want := strings.ReplaceAll(m.Lines[1], entryAddr, "de1.example.com"), m.Lines[0]; got != want {
			t.Errorf("%s: the twin differs from the server in more than its address:\n%s\nwant\n%s", format, got, want)
		}
	}
}

// A VLESS server with a TLS front keeps the domain as the TLS name: the node's certificate is for it, not for the address.
func TestIPv6TwinOfAVLESSTLSServerKeepsTheDomainAsTheTLSName(t *testing.T) {
	f := newRelayFix(t)
	f.entry("nod_de1", entryAddr)
	p := f.vlessWith("vltls", `{"security":"tls","transport":"ws","tls_mode":"acme_domain","sni":"origin.example.com","ws":{"path":"/ws","host":""}}`)
	f.inbound(p.Id, "nod_de1")
	token := f.tokenOf(f.user("alice", f.group("g", p.Id), nil).User.Id)
	v := f.twinView(token, plugin.FormatURIList)
	if len(v.Servers) != 2 || !strings.Contains(v.Servers[1].URI, "@"+entryBracket+":443?") || !strings.Contains(v.Servers[1].URI, "sni=origin.example.com") {
		t.Fatalf("servers: %+v", v.Servers)
	}
}

// A self-signed server is dialled by IP: the line has no TLS name, and the pin is what the client checks, so the IPv6 address
// works with the same pin.
func TestIPv6TwinOfASelfSignedServerKeepsThePin(t *testing.T) {
	f := newRelayFix(t)
	f.node("nod_ip", "ip1", "203.0.113.10", "active")
	f.entry("nod_ip", entryAddr)
	p := f.profile("self", `{"tls_mode":"self_signed","obfs":{"type":"none"}}`)
	f.inbound(p.Id, "nod_ip")
	pin := strings.Repeat("ab", 32)
	f.sql(`UPDATE inbound SET cert_pin_sha256 = ?, state = 'active'`, pin)
	token := f.tokenOf(f.user("alice", f.group("g", p.Id), nil).User.Id)

	v := f.twinView(token, plugin.FormatURIList)
	if len(v.Servers) != 2 {
		t.Fatalf("servers: %+v", v.Servers)
	}
	want := "hysteria2://"
	for _, s := range v.Servers {
		if !strings.HasPrefix(s.URI, want) || !strings.Contains(s.URI, "?insecure=1&pinSHA256="+pin+"#") || strings.Contains(s.URI, "sni=") {
			t.Errorf("line: %s", s.URI)
		}
	}
	if !strings.Contains(v.Servers[0].URI, "@203.0.113.10:443/") || !strings.Contains(v.Servers[1].URI, "@"+entryBracket+":443/") {
		t.Errorf("addresses: %s | %s", v.Servers[0].URI, v.Servers[1].URI)
	}
	m := f.twinView(token, plugin.FormatMihomo)
	if len(m.Lines) != 2 || !strings.Contains(m.Lines[1], `server: "`+entryAddr+`"`) || !strings.Contains(m.Lines[1], `fingerprint: "`+pin+`"`) {
		t.Errorf("Mihomo:\n%s", strings.Join(m.Lines, "\n"))
	}
	// sing-box and Xray JSON do not take a self-signed Hysteria2 server at all, with or without the twin
	if s := f.twinView(token, plugin.FormatSingbox); len(s.Lines) != 0 {
		t.Errorf("sing-box took a self-signed server: %v", s.Lines)
	}
}

// What cannot go to the node's IPv6 address gets no twin: a server reached through a CDN host, and one whose certificate is for
// the node's IPv4 address.
func TestNoIPv6TwinForACDNServer(t *testing.T) {
	f := newRelayFix(t)
	f.entry("nod_de1", entryAddr)
	cdn := f.vlessWith("cdn", `{"security":"tls","transport":"ws","tls_mode":"acme_domain","sni":"origin.example.com","cdn":{"host":"cdn.example.org","port":443},"ws":{"path":"/ws","host":""}}`)
	f.inbound(cdn.Id, "nod_de1")
	token := f.tokenOf(f.user("alice", f.group("g", cdn.Id, f.base.Id), nil).User.Id)
	for _, format := range linkFormats {
		for _, s := range f.twinView(token, format).Servers {
			if s.Mark == MarkIPv6 && s.Protocol == "vless" {
				t.Errorf("%s: an IPv6 twin of a CDN server: %s", format, s.URI)
			}
		}
	}
	// the node's other server (Hysteria2) still has its twin
	if n := countMark(f.twinView(token, plugin.FormatURIList), MarkIPv6); n != 1 {
		t.Errorf("%d IPv6 twins, want the Hysteria2 server's alone", n)
	}
}

func countMark(v SubView, mark string) (n int) {
	for _, s := range v.Servers {
		if s.Mark == mark {
			n++
		}
	}
	return n
}

// No twin unless the entry is a usable IPv6 address of a node whose own address is not one.
func TestNoIPv6TwinWithoutAUsableEntry(t *testing.T) {
	f := newRelayFix(t)
	token := f.tokenOf(f.user("alice", f.group("g", f.base.Id), nil).User.Id)
	for name, c := range map[string]struct {
		node, entry, address string
		want                 int
	}{
		"entry off":              {"nod_de1", "", "", 1},
		"entry on":               {"nod_de1", entryAddr, "", 2},
		"an IPv4 address":        {"nod_de1", "203.0.113.5", "", 1},
		"a unique-local address": {"nod_de1", "fd00::5", "", 1},
		"link-local":             {"nod_de1", "fe80::5", "", 1},
		"junk":                   {"nod_de1", "not an address", "", 1},
	} {
		f.entry(c.node, c.entry)
		addr := c.address
		if addr == "" {
			addr = "de1.example.com"
		}
		f.sql(`UPDATE node SET address = ? WHERE id = ?`, addr, c.node)
		if got := len(f.twinView(token, plugin.FormatURIList).Servers); got != c.want {
			t.Errorf("%s: %d servers, want %d", name, got, c.want)
		}
	}
	// a node whose own address is IPv6 has nothing to add (its server, a name for the TLS certificate, would not even build)
	if got := ipv6EntryAddr(store.AccessNode{Address: "2001:db8::1", IPv6Entry: entryAddr}); got != "" {
		t.Errorf("a node at an IPv6 address has the entry %q", got)
	}
	if got := ipv6EntryAddr(store.AccessNode{Address: "de1.example.com", IPv6Entry: " [" + entryAddr + "] "}); got != entryAddr {
		t.Errorf("entry = %q", got)
	}
}

// The static AmneziaWG keys keep the address they were issued with, whatever the entry says; only the Mihomo proxy of AmneziaWG,
// which refreshes with the subscription, goes to the IPv6 address too.
func TestIPv6EntryLeavesAmneziaWGKeysAlone(t *testing.T) {
	f := newRelayFix(t)
	f.entry("nod_de1", entryAddr)
	awg := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "awg", Name: "awg31"}))).Msg.Profile
	f.inbound(awg.Id, "nod_de1")
	u := f.user("alice", f.group("g", awg.Id), amnOnly()).User
	token := f.tokenOf(u.Id)

	// the Mihomo subscription: the proxy and its IPv6 twin, in the same order as the servers
	m := must(f.s.SubscriptionWith(f.ctx, token, SubOptions{Format: plugin.FormatMihomo, NoPageData: true}))
	if len(m.Lines) != 2 || !strings.Contains(m.Lines[0], "de1.example.com") || strings.Contains(m.Lines[0], entryAddr) ||
		!strings.Contains(m.Lines[1], `server: "`+entryAddr+`"`) || m.Servers[1].Mark != MarkIPv6 {
		t.Fatalf("Mihomo AmneziaWG proxies:\n%s", strings.Join(m.Lines, "\n"))
	}
	// the key (.conf and vpn://) of the device: the node's own address
	d := must(f.s.CreateAwgDevice(f.ctx, req(&adminv1.CreateAwgDeviceRequest{UserId: u.Id, ProfileId: awg.Id, Platform: "ios", Label: "phone"}))).Msg
	if len(d.Configs) == 0 {
		t.Fatal("no key was made")
	}
	for _, c := range d.Configs {
		for _, text := range []string{c.Conf, c.VpnKey} {
			if strings.Contains(text, entryAddr) || strings.Contains(text, "2001:db8") {
				t.Errorf("a static key carries the IPv6 entry: %s", text)
			}
		}
		if !strings.Contains(c.Conf, "Endpoint = de1.example.com:") {
			t.Errorf(".conf endpoint: %s", c.Conf)
		}
	}
}

// A node with an IPv6 entry and a relay: the server, its IPv6 twin, then the relay's twin; the relay's twin goes to the entry
// node's address and is not doubled for IPv6.
func TestIPv6TwinAndRelayTwinOfOneServer(t *testing.T) {
	f := newTwinFix(t)
	f.entry("nod_de1", entryAddr)
	f.entry("nod_fi1", "2001:db8:1::9") // the relay's entry node has an IPv6 entry of its own; it does not matter for de1's server
	r := must(f.create(f.server.Id, "nod_fi1", 8443))
	f.switchOn(r.Id)
	token := f.tokenOf(f.user("alice", f.group("g", f.base.Id), nil).User.Id)

	v := f.twinView(token, plugin.FormatURIList)
	if len(v.Servers) != 3 {
		t.Fatalf("%d servers, want the server, its IPv6 twin and the relay's twin: %+v", len(v.Servers), v.Servers)
	}
	marks := []string{v.Servers[0].Mark, v.Servers[1].Mark, v.Servers[2].Mark}
	if marks[0] != "" || marks[1] != MarkIPv6 || marks[2] != "via:FI" {
		t.Fatalf("order of the marks: %q", marks)
	}
	if !strings.Contains(v.Servers[0].URI, "@de1.example.com:443/") || !strings.Contains(v.Servers[1].URI, "@"+entryBracket+":443/") ||
		!strings.Contains(v.Servers[2].URI, "@fi1.example.com:8443/") || strings.Contains(v.Servers[2].URI, "2001:db8") {
		t.Errorf("lines:\n%s", strings.Join(v.Lines, "\n"))
	}
	// the page: the node's three ways, in the same order, each pointing at its line
	conns := v.Nodes[0].Conns
	if len(conns) != 3 || conns[0].IPv6 || conns[0].Relay || !conns[1].IPv6 || conns[1].Relay || conns[1].Server != 1 || conns[2].IPv6 || !conns[2].Relay || conns[2].Server != 2 || conns[2].Via != "FI" {
		t.Errorf("connections: %+v", conns)
	}
	for _, format := range linkFormats[1:] {
		m := f.twinView(token, format)
		if len(m.Lines) != 3 || !strings.Contains(m.Lines[1], entryAddr) || !strings.Contains(m.Lines[2], "fi1.example.com") || strings.Contains(m.Lines[2], "2001:db8") {
			t.Errorf("%s:\n%s", format, strings.Join(m.Lines, "\n"))
		}
	}
}
