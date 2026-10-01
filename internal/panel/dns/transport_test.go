package dns

import (
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// line renders an endpoint as "doh:https://..." for compact expectations.
func line(eps []Endpoint) []string {
	var out []string
	for _, e := range eps {
		out = append(out, string(e.Kind)+":"+e.Address)
	}
	return out
}

func variantServers(ids ...string) []Server {
	var s []Server
	for _, id := range ids {
		s = append(s, Server{Variant: id})
	}
	return s
}

// Every client x every preference for [Cloudflare, Google]: the client gets the preferred transport when it can
// carry it, else the next one down (DoH -> DoT -> plain).
func TestResolveClientsByPreference(t *testing.T) {
	cfGoogle := variantServers("cloudflare/standard", "google/standard")
	plainV4 := []string{"plain:1.1.1.1", "plain:8.8.8.8", "plain:1.0.0.1", "plain:8.8.4.4"}
	plainAll := append(slices.Clone(plainV4), "plain:2606:4700:4700::1111", "plain:2001:4860:4860::8888", "plain:2606:4700:4700::1001", "plain:2001:4860:4860::8844")
	doh := []string{"doh:https://cloudflare-dns.com/dns-query", "doh:https://dns.google/dns-query"}
	dot := []string{"dot:one.one.one.one", "dot:dns.google"}
	cases := []struct {
		client Client
		pref   Kind
		want   []string
	}{
		{ClientHapp, KindDoH, doh},
		{ClientHapp, KindDoT, plainV4}, // no DoT in Happ: down to plain
		{ClientHapp, KindPlain, plainV4},
		{ClientMihomo, KindDoH, doh},
		{ClientMihomo, KindDoT, dot},
		{ClientMihomo, KindPlain, plainAll},
		{ClientAmneziaWG, KindDoH, plainV4}, // a dns1/dns2 pair of IPv4 addresses, whatever was asked
		{ClientAmneziaWG, KindDoT, plainV4},
		{ClientAmneziaWG, KindPlain, plainV4},
	}
	for _, c := range cases {
		got, notes := Resolve(c.client, c.pref, cfGoogle, false)
		if !slices.Equal(line(got), c.want) || len(notes) != 0 {
			t.Errorf("%s / %s:\n got %v %v\nwant %v", c.client, c.pref, line(got), notes, c.want)
		}
	}
	// AmneziaWG writes the first two: one address of each provider, not two of the first
	got, _ := Resolve(ClientAmneziaWG, KindDoH, cfGoogle, false)
	if got[0].Address != "1.1.1.1" || got[1].Address != "8.8.8.8" {
		t.Errorf("amnezia pair = %v %v", got[0].Address, got[1].Address)
	}
	// ipv4_only drops the IPv6 addresses a client would take
	got, _ = Resolve(ClientMihomo, KindPlain, cfGoogle, true)
	if !slices.Equal(line(got), plainV4) {
		t.Errorf("mihomo ipv4_only = %v", line(got))
	}
	// DoH/DoT endpoints carry the host's addresses for bootstrap
	got, _ = Resolve(ClientHapp, KindDoH, cfGoogle, false)
	if !slices.Equal(got[0].IPs, []string{"1.1.1.1", "1.0.0.1"}) || got[0].Variant != "cloudflare/standard" {
		t.Errorf("doh endpoint = %+v", got[0])
	}
}

// A variant without an endpoint in the preferred transport steps down; one with nothing further down steps up as a
// last resort; a client that cannot carry any transport of a variant drops it and says so.
func TestResolveVariantGaps(t *testing.T) {
	// OpenDNS FamilyShield: plain and DoH, no DoT -> DoT falls to plain on Mihomo
	got, _ := Resolve(ClientMihomo, KindDoT, variantServers("opendns/familyshield"), false)
	if !slices.Equal(line(got), []string{"plain:208.67.222.123", "plain:208.67.220.123"}) {
		t.Errorf("opendns, DoT: %v", line(got))
	}
	got, _ = Resolve(ClientMihomo, KindDoH, variantServers("opendns/familyshield"), false)
	if !slices.Equal(line(got), []string{"doh:https://familyshield.opendns.com/dns-query"}) {
		t.Errorf("opendns, DoH: %v", line(got))
	}
	// Mullvad answers no plain DNS: a plain preset still reaches it over DoH on Happ, and is dropped for Amnezia
	got, _ = Resolve(ClientHapp, KindPlain, variantServers("mullvad/adblock"), false)
	if !slices.Equal(line(got), []string{"doh:https://adblock.dns.mullvad.net/dns-query"}) || got[0].IPs[0] != "194.242.2.3" {
		t.Errorf("mullvad on Happ: %+v", got)
	}
	got, notes := Resolve(ClientAmneziaWG, KindPlain, variantServers("mullvad/adblock", "quad9/standard"), false)
	if !slices.Equal(line(got), []string{"plain:9.9.9.9", "plain:149.112.112.112"}) || len(notes) != 1 || !strings.Contains(notes[0], "mullvad/adblock") {
		t.Errorf("mullvad on Amnezia: %v %v", line(got), notes)
	}
	// a variant that left the catalog in a release is skipped, not fatal
	got, notes = Resolve(ClientHapp, KindDoH, variantServers("gone/variant", "google/standard"), false)
	if len(got) != 1 || got[0].Variant != "google/standard" || len(notes) != 1 || !strings.Contains(notes[0], "not in the catalog") {
		t.Errorf("unknown variant: %v %v", line(got), notes)
	}
}

// Custom servers keep their kind: used as typed when the client carries it, dropped otherwise; the preference
// never rewrites them.
func TestResolveCustomServers(t *testing.T) {
	custom := []Server{
		{Kind: KindDoT, Address: "dns.example.com"},
		{Kind: KindPlain, Address: "10.0.0.1:5353"},
		{Kind: KindDoH, Address: "https://unknown.example.com/dns-query"},
		{Kind: KindDoH, Address: "https://dns.adguard-dns.com/dns-query"}, // typed by hand, but the catalog knows the host
		{Kind: KindPlain, Address: "2606:4700:4700::1111"},
		{Kind: KindPlain, Address: "10.0.0.3"},
	}
	tests := []struct {
		client Client
		want   []string
	}{
		{ClientHapp, []string{"doh:https://dns.adguard-dns.com/dns-query", "plain:10.0.0.3"}},
		{ClientMihomo, []string{"dot:dns.example.com", "plain:10.0.0.1:5353", "doh:https://unknown.example.com/dns-query", "doh:https://dns.adguard-dns.com/dns-query", "plain:2606:4700:4700::1111", "plain:10.0.0.3"}},
		{ClientAmneziaWG, []string{"plain:10.0.0.3"}},
	}
	for _, c := range tests {
		for _, pref := range []Kind{KindPlain, KindDoT, KindDoH} {
			got, _ := Resolve(c.client, pref, custom, false)
			// the round-robin order puts single endpoints in list order
			if !slices.Equal(line(got), c.want) {
				t.Errorf("%s / %s:\n got %v\nwant %v", c.client, pref, line(got), c.want)
			}
		}
	}
	got, _ := Resolve(ClientHapp, KindDoH, custom, false)
	if !slices.Equal(got[0].IPs, []string{"94.140.14.14", "94.140.15.15"}) {
		t.Errorf("bootstrap of a hand-typed catalog host = %v", got[0].IPs)
	}
	// a preset that mixes catalog and custom servers keeps the list order
	got, _ = Resolve(ClientMihomo, KindDoH, []Server{{Kind: KindPlain, Address: "10.0.0.3"}, {Variant: "google/standard"}}, false)
	if !slices.Equal(line(got), []string{"plain:10.0.0.3", "doh:https://dns.google/dns-query"}) {
		t.Errorf("mixed = %v", line(got))
	}
}

// The preset helpers feed Resolve with the preset's own transport, split rules included.
func TestPresetEndpointsFor(t *testing.T) {
	p := Preset{Name: "x", Transport: KindDoH, Servers: variantServers("cloudflare/standard"), SplitDirect: true,
		Split: []SplitRule{{Suffixes: []string{".ru"}, Servers: variantServers("yandex/basic")}}}
	got, _ := p.EndpointsFor(ClientMihomo)
	if !slices.Equal(line(got), []string{"doh:https://cloudflare-dns.com/dns-query"}) {
		t.Errorf("main = %v", line(got))
	}
	got, _ = p.SplitEndpointsFor(ClientMihomo, 0)
	if !slices.Equal(line(got), []string{"doh:https://common.dot.dns.yandex.net/dns-query"}) {
		t.Errorf("split = %v", line(got))
	}
	got, _ = p.SplitEndpointsFor(ClientAmneziaWG, 0)
	if !slices.Equal(line(got), []string{"plain:77.88.8.8", "plain:77.88.8.1"}) {
		t.Errorf("split on Amnezia = %v", line(got))
	}
}

// The Happ header follows the preference: DoH gives a DoH resolver with its pin, DoT steps down to plain, and the
// Russian split resolves over DoH through Yandex (the case where the protocol matters: that traffic goes direct).
func TestHappRoutingByPreference(t *testing.T) {
	p := builtin(t, "dns_builtin_ru_split")
	p.Transport = KindDoH
	_, _, err := happRender(p)
	if err != nil {
		t.Fatal(err)
	}
	prof, _, _ := happBuild(p)
	if prof.RemoteDNSType != "DoH" || prof.RemoteDNSDomain != "https://cloudflare-dns.com/dns-query" || prof.RemoteDNSIP != "1.1.1.1" ||
		prof.DomesticDNSType != "DoH" || prof.DomesticDNSDom != "https://common.dot.dns.yandex.net/dns-query" || prof.DomesticDNSIP != "77.88.8.8" ||
		prof.DnsHosts["cloudflare-dns.com"] != "1.1.1.1" || prof.DnsHosts["common.dot.dns.yandex.net"] != "77.88.8.8" {
		t.Errorf("DoH profile = %+v", prof)
	}
	p.Transport = KindDoT
	prof, _, _ = happBuild(p)
	if prof.RemoteDNSType != "DoU" || prof.RemoteDNSIP != "1.1.1.1" || prof.DomesticDNSIP != "77.88.8.8" || len(prof.DnsHosts) != 0 {
		t.Errorf("DoT profile (Happ has no DoT) = %+v", prof)
	}
}

// The catalog is data written by hand: every entry must pass the validation a custom server goes through, ids must
// be unique and agree with their provider, and every variant must be reachable by at least one transport.
func TestCatalog(t *testing.T) {
	cats := map[Category]bool{CategoryRussia: true, CategoryRegular: true, CategoryNoAds: true, CategoryFamily: true, CategorySecurity: true}
	seen := map[string]bool{}
	for _, p := range Catalog() {
		if p.ID == "" || p.Name == "" || len(p.Variants) == 0 {
			t.Errorf("provider %+v", p)
		}
		for _, v := range p.Variants {
			if seen[v.ID] || !strings.HasPrefix(v.ID, p.ID+"/") {
				t.Errorf("variant id %q (provider %s)", v.ID, p.ID)
			}
			seen[v.ID] = true
			if !cats[v.Category] || v.NameRU == "" || v.NameEN == "" || v.NoteRU == "" || v.NoteEN == "" {
				t.Errorf("variant %s: category %q, names/notes missing", v.ID, v.Category)
			}
			if len(v.IPv4) == 0 {
				t.Errorf("variant %s has no IPv4 address", v.ID)
			}
			for _, a := range append(slices.Clone(v.IPv4), v.IPv6...) {
				if n, err := NormalizeServer(Server{Kind: KindPlain, Address: a}); err != nil || n.Address != a {
					t.Errorf("variant %s address %q: %v %v", v.ID, a, n, err)
				}
			}
			if v.DoHURL == "" && v.DoTHost == "" && v.NoPlain {
				t.Errorf("variant %s cannot be reached at all", v.ID)
			}
			if v.DoHURL != "" {
				if n, err := NormalizeServer(Server{Kind: KindDoH, Address: v.DoHURL}); err != nil || n.Address != v.DoHURL {
					t.Errorf("variant %s DoH %q: %v %v", v.ID, v.DoHURL, n, err)
				}
			}
			if v.DoTHost != "" {
				if n, err := NormalizeServer(Server{Kind: KindDoT, Address: v.DoTHost}); err != nil || n.Address != v.DoTHost || v.DoTPort != 853 {
					t.Errorf("variant %s DoT %q:%d: %v %v", v.ID, v.DoTHost, v.DoTPort, n, err)
				}
			}
		}
	}
	if _, _, ok := LookupVariant("cloudflare/family"); !ok {
		t.Error("cloudflare/family missing")
	}
	if n, err := NormalizeServer(Server{Variant: " cloudflare/family "}); err != nil || n != (Server{Variant: "cloudflare/family"}) {
		t.Errorf("variant server = %+v %v", n, err)
	}
	if _, err := NormalizeServer(Server{Variant: "cloudflare/nope"}); err == nil {
		t.Error("unknown variant accepted")
	}
}

// The API: variants and the transport round-trip, a client that predates the field keeps the stored one, the
// list carries the catalog and what each app supports.
func TestAPIVariantsAndTransport(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	variant := func(id string) *adminv1.DnsServer { return &adminv1.DnsServer{ProviderVariant: id} }

	r, err := e.s.CreateDnsPreset(ctx, connect.NewRequest(&adminv1.CreateDnsPresetRequest{
		Name: "Мой DoH", PreferredTransport: adminv1.DnsTransport_DNS_TRANSPORT_DOH,
		Servers: []*adminv1.DnsServer{variant("cloudflare/family"), plain("9.9.9.9")},
		Split:   []*adminv1.DnsSplitRule{{Suffixes: []string{".ru"}, Servers: []*adminv1.DnsServer{variant("yandex/basic")}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	p := r.Msg.Preset
	if p.PreferredTransport != adminv1.DnsTransport_DNS_TRANSPORT_DOH || p.Servers[0].ProviderVariant != "cloudflare/family" ||
		p.Servers[0].Address != "1.1.1.3" || p.Servers[1].ProviderVariant != "" || p.Servers[1].Address != "9.9.9.9" ||
		p.Category != adminv1.DnsCategory_DNS_CATEGORY_FAMILY || p.Split[0].Servers[0].ProviderVariant != "yandex/basic" {
		t.Fatalf("created = %v", p)
	}

	// a client that knows nothing of the field: the transport stays, and so do the catalog servers it echoes back
	u, err := e.s.UpdateDnsPreset(ctx, connect.NewRequest(&adminv1.UpdateDnsPresetRequest{Id: p.Id, Name: "Мой DoH 2", Servers: p.Servers, Split: p.Split}))
	if err != nil || u.Msg.Preset.PreferredTransport != adminv1.DnsTransport_DNS_TRANSPORT_DOH {
		t.Fatalf("update without the field: %v %v", u, err)
	}
	u, err = e.s.UpdateDnsPreset(ctx, connect.NewRequest(&adminv1.UpdateDnsPresetRequest{Id: p.Id, Name: "Мой DoH 2", Servers: p.Servers, PreferredTransport: adminv1.DnsTransport_DNS_TRANSPORT_DOT}))
	if err != nil || u.Msg.Preset.PreferredTransport != adminv1.DnsTransport_DNS_TRANSPORT_DOT || len(u.Msg.Preset.Split) != 0 {
		t.Fatalf("update to DoT: %v %v", u, err)
	}
	// created without the field: plain
	q, err := e.create("Без поля", plain("9.9.9.9"))
	if err != nil || q.PreferredTransport != adminv1.DnsTransport_DNS_TRANSPORT_PLAIN {
		t.Fatalf("default transport: %v %v", q, err)
	}
	// an unknown variant is the admin's mistake
	_, err = e.create("Плохой", variant("cloudflare/nope"))
	wantCode(t, err, connect.CodeInvalidArgument)

	// what the renderers get: the stored preset resolves per client
	row, err := e.st.DNS().Get(ctx, p.Id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := fromRow(row)
	if err != nil || got.Transport != KindDoT {
		t.Fatalf("stored = %+v %v", got, err)
	}
	if eps, _ := got.EndpointsFor(ClientMihomo); !slices.Equal(line(eps), []string{"dot:family.cloudflare-dns.com", "plain:9.9.9.9"}) {
		t.Errorf("mihomo = %v", line(eps))
	}

	// the list: the catalog and the support table
	l, err := e.s.ListDnsPresets(ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Msg.Providers) != len(Catalog()) || l.Msg.Providers[0].Id != "cloudflare" || len(l.Msg.Providers[0].Variants) != 3 {
		t.Fatalf("providers = %d", len(l.Msg.Providers))
	}
	mv := l.Msg.Providers[0].Variants[0]
	if mv.Id != "cloudflare/standard" || mv.DohUrl != "https://cloudflare-dns.com/dns-query" || mv.DotHost != "one.one.one.one" || mv.DotPort != 853 || !mv.Plain ||
		mv.Ipv4[0] != "1.1.1.1" || mv.Category != adminv1.DnsCategory_DNS_CATEGORY_REGULAR || mv.NoteRu == "" {
		t.Errorf("variant = %v", mv)
	}
	sup := map[adminv1.DnsClient][]adminv1.DnsTransport{}
	for _, c := range l.Msg.ClientSupport {
		sup[c.Client] = c.Transports
	}
	if len(l.Msg.ClientSupport) != 3 ||
		!slices.Equal(sup[adminv1.DnsClient_DNS_CLIENT_HAPP], []adminv1.DnsTransport{adminv1.DnsTransport_DNS_TRANSPORT_DOH, adminv1.DnsTransport_DNS_TRANSPORT_PLAIN}) ||
		!slices.Equal(sup[adminv1.DnsClient_DNS_CLIENT_MIHOMO], []adminv1.DnsTransport{adminv1.DnsTransport_DNS_TRANSPORT_DOH, adminv1.DnsTransport_DNS_TRANSPORT_DOT, adminv1.DnsTransport_DNS_TRANSPORT_PLAIN}) ||
		!slices.Equal(sup[adminv1.DnsClient_DNS_CLIENT_AMNEZIAWG], []adminv1.DnsTransport{adminv1.DnsTransport_DNS_TRANSPORT_PLAIN}) {
		t.Errorf("client support = %v", l.Msg.ClientSupport)
	}
}
