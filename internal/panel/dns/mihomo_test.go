package dns

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func variant(id string) Server { return Server{Variant: id} }
func plainSrv(a string) Server { return Server{Kind: KindPlain, Address: a} }

func mustDNS(t *testing.T, p Preset, group string) MihomoDNS {
	t.Helper()
	d, err := p.MihomoDNS(group)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s:\n got %q\nwant %q", what, got, want)
	}
}

// The matrix for Mihomo: which servers go where for each shape of preset.
func TestMihomoDNSMatrix(t *testing.T) {
	yandex := []string{"77.88.8.8", "77.88.8.1"}
	cases := []struct {
		name   string
		p      Preset
		group  string
		ns     []string // nameserver
		psn    []string // proxy-server-nameserver
		boot   []string // default-nameserver
		policy map[string][]string
		direct []string
	}{
		{
			name:  "plain servers, no split: all through the group, no policy",
			p:     Preset{Name: "std", Servers: []Server{variant("cloudflare/standard"), variant("google/standard")}, Transport: KindPlain},
			group: "G",
			// IPv6 addresses are left out; round-robin over the servers: 1.1.1.1, 8.8.8.8, then the second addresses
			ns:  []string{"1.1.1.1#G", "8.8.8.8#G", "1.0.0.1#G", "8.8.4.4#G"},
			psn: []string{"1.1.1.1", "8.8.8.8", "1.0.0.1", "8.8.4.4"},
		},
		{
			name: "russia split with SplitDirect: the split's servers answer its suffixes, directly",
			p: Preset{Name: "ru", Servers: []Server{variant("cloudflare/standard")}, Transport: KindPlain, SplitDirect: true,
				Split: []SplitRule{{Suffixes: []string{".ru", "su", ".xn--p1ai"}, Servers: []Server{variant("yandex/basic")}}}},
			group:  "G",
			ns:     []string{"1.1.1.1#G", "1.0.0.1#G"},
			psn:    []string{"1.1.1.1", "1.0.0.1"},
			policy: map[string][]string{"+.ru": yandex, "+.su": yandex, "+.xn--p1ai": yandex},
			direct: []string{"ru", "su", "xn--p1ai"},
		},
		{
			name: "split without SplitDirect is ignored",
			p: Preset{Name: "ru-off", Servers: []Server{plainSrv("1.1.1.1")}, Transport: KindPlain,
				Split: []SplitRule{{Suffixes: []string{".ru"}, Servers: []Server{plainSrv("77.88.8.8")}}}},
			group: "G",
			ns:    []string{"1.1.1.1#G"},
			psn:   []string{"1.1.1.1"},
		},
		{
			name:  "DoH preference: the URL through the group, the bootstrap addresses from the catalog, the system resolver for nodes",
			p:     Preset{Name: "doh", Servers: []Server{variant("adguard/default")}, Transport: KindDoH},
			group: "G",
			ns:    []string{"https://dns.adguard-dns.com/dns-query#G"},
			psn:   []string{"system"},
			boot:  []string{"94.140.14.14", "94.140.15.15"},
		},
		{
			name:  "DoT preference",
			p:     Preset{Name: "dot", Servers: []Server{variant("quad9/standard")}, Transport: KindDoT},
			group: "G",
			ns:    []string{"tls://dns.quad9.net#G"},
			psn:   []string{"system"},
			boot:  []string{"9.9.9.9", "149.112.112.112"},
		},
		{
			name:  "a variant without plain DNS steps up to DoH even when plain is preferred",
			p:     Preset{Name: "mullvad", Servers: []Server{variant("mullvad/standard")}, Transport: KindPlain},
			group: "G",
			ns:    []string{"https://dns.mullvad.net/dns-query#G"},
			psn:   []string{"system"},
			boot:  []string{"194.242.2.2"},
		},
		{
			name:  "a custom server with a port and a custom DoT",
			p:     Preset{Name: "custom", Servers: []Server{plainSrv("9.9.9.9:5353"), {Kind: KindDoT, Address: "dns.example.com:8853"}}, Transport: KindPlain},
			group: "G",
			ns:    []string{"9.9.9.9:5353#G", "tls://dns.example.com:8853#G"},
			psn:   []string{"9.9.9.9:5353"},
		},
		{
			name:  "an IPv6-only preset keeps its resolver (bracketed)",
			p:     Preset{Name: "v6", Servers: []Server{plainSrv("2606:4700:4700::1111")}, Transport: KindPlain},
			group: "G",
			ns:    []string{"[2606:4700:4700::1111]#G"},
			psn:   []string{"[2606:4700:4700::1111]"},
		},
		{
			name:  "no group: no fragment",
			p:     Preset{Name: "nogroup", Servers: []Server{plainSrv("1.1.1.1")}, Transport: KindPlain},
			group: "",
			ns:    []string{"1.1.1.1"},
			psn:   []string{"1.1.1.1"},
		},
		{
			name: "two rules with the same suffix: the first wins; a rule with no usable server is dropped; a bad suffix is skipped",
			p: Preset{Name: "dups", Servers: []Server{plainSrv("1.1.1.1")}, Transport: KindPlain, SplitDirect: true, Split: []SplitRule{
				{Suffixes: []string{"a.example", "bad,suffix"}, Servers: []Server{plainSrv("77.88.8.8")}},
				{Suffixes: []string{"a.example", "b.example"}, Servers: []Server{plainSrv("77.88.8.1")}},
				{Suffixes: []string{"c.example"}, Servers: []Server{variant("no/such")}},
			}},
			group:  "G",
			ns:     []string{"1.1.1.1#G"},
			psn:    []string{"1.1.1.1"},
			policy: map[string][]string{"+.a.example": {"77.88.8.8"}, "+.b.example": {"77.88.8.1"}},
			direct: []string{"a.example", "b.example"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := mustDNS(t, c.p, c.group)
			eq(t, "nameserver", d.Nameserver, c.ns)
			eq(t, "proxy-server-nameserver", d.ProxyServerNameserver, c.psn)
			eq(t, "default-nameserver", d.DefaultNameserver, c.boot)
			eq(t, "direct", d.Direct, c.direct)
			got := map[string][]string{}
			var order []string
			for _, e := range d.Policy {
				got[e.Domain] = e.Servers
				order = append(order, strings.TrimPrefix(e.Domain, "+."))
			}
			if len(got) != len(c.policy) {
				t.Errorf("policy = %v, want %v", got, c.policy)
			}
			for k, want := range c.policy {
				eq(t, "policy "+k, got[k], want)
			}
			eq(t, "policy order is the direct order", order, c.direct)
		})
	}
}

// A non-ASCII suffix that reached the store raw is sent as punycode; the fragment of a hostile group name cannot
// carry a second parameter, and a plain "#" never appears twice.
func TestMihomoDNSSuffixesAndGroup(t *testing.T) {
	p := Preset{Name: "idn", Servers: []Server{plainSrv("1.1.1.1")}, Transport: KindPlain, SplitDirect: true,
		Split: []SplitRule{{Suffixes: []string{".рф", "Example.COM"}, Servers: []Server{plainSrv("77.88.8.8")}}}}
	d := mustDNS(t, p, "Моя сеть %41 ✓")
	eq(t, "direct", d.Direct, []string{"xn--p1ai", "example.com"})
	if len(d.Nameserver) != 1 || strings.Count(d.Nameserver[0], "#") != 1 {
		t.Fatalf("nameserver = %q", d.Nameserver)
	}
	// What the core does with it: parse as a URL and take the fragment as the group name.
	u, err := url.Parse("udp://" + d.Nameserver[0])
	if err != nil || u.Fragment != "Моя сеть %41 ✓" || u.Host != "1.1.1.1" {
		t.Errorf("parsed %q: %+v %v", d.Nameserver[0], u, err)
	}
}

func TestMihomoDNSNoMainServer(t *testing.T) {
	// Only a server the client cannot carry: no dns section (the caller leaves it out).
	if _, err := (Preset{Name: "x", Servers: []Server{variant("no/such")}, Transport: KindPlain}).MihomoDNS("G"); err == nil {
		t.Error("an empty server list must be an error")
	}
	if _, err := (Preset{Name: "none"}).MihomoDNS("G"); err == nil {
		t.Error("a preset without servers must be an error")
	}
}

// Every built-in preset, as seeded, renders; only the Russia split carries a policy and direct rules, and nothing
// in any string can break a YAML scalar or a rule line.
func TestMihomoDNSBuiltins(t *testing.T) {
	e := newEnv(t)
	rows, err := e.st.DNS().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 15 {
		t.Fatalf("%d presets", len(rows))
	}
	for _, row := range rows {
		p, err := fromRow(row)
		if err != nil {
			t.Fatal(err)
		}
		d, err := p.MihomoDNS("G")
		if err != nil {
			t.Errorf("%s: %v", row.ID, err)
			continue
		}
		if len(d.Nameserver) == 0 || len(d.ProxyServerNameserver) == 0 {
			t.Errorf("%s: empty %+v", row.ID, d)
		}
		for _, s := range append(append([]string{}, d.Nameserver...), d.ProxyServerNameserver...) {
			if strings.ContainsAny(s, " \t\r\n,\"") {
				t.Errorf("%s: %q", row.ID, s)
			}
		}
		if (len(d.Policy) > 0) != (row.ID == "dns_builtin_ru_split") || len(d.Policy) != len(d.Direct) {
			t.Errorf("%s: policy %v direct %v", row.ID, d.Policy, d.Direct)
		}
	}
	// The stock Russia preset: ten suffixes go to Yandex, directly.
	var ru Preset
	for _, row := range rows {
		if row.ID == "dns_builtin_ru_split" {
			ru, _ = fromRow(row)
		}
	}
	d := mustDNS(t, ru, "G")
	eq(t, "direct", d.Direct, []string{"ru", "su", "xn--p1ai", "yandex.com", "yandex.net", "yastatic.net", "vk.com", "userapi.com", "sberbank.com", "vtb.com"})
	eq(t, "policy servers", d.Policy[0].Servers, []string{"77.88.8.8", "77.88.8.1"})
	if d.Policy[2].Domain != "+.xn--p1ai" {
		t.Errorf("the .rf suffix is %q", d.Policy[2].Domain)
	}
}
