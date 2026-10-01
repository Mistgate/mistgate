package dns

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// builtin loads a seeded preset as the subscription code will get it.
func builtin(t *testing.T, id string) Preset {
	t.Helper()
	e := newEnv(t)
	row, err := e.st.DNS().Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	p, err := fromRow(row)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func decode(t *testing.T, header string) string {
	t.Helper()
	b64, ok := strings.CutPrefix(header, "happ://routing/onadd/")
	if !ok {
		t.Fatalf("header = %q", header)
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(b) {
		t.Fatalf("not JSON: %s", b)
	}
	return string(b)
}

// Golden outputs: change them only on purpose, they are what Happ clients receive.
const goldenRuSplit = `{"Name":"DNS","GlobalProxy":"true","RemoteDNSType":"DoU","RemoteDNSDomain":"","RemoteDNSIP":"1.1.1.1",` +
	`"DomesticDNSType":"DoU","DomesticDNSDomain":"","DomesticDNSIP":"77.88.8.8","LastUpdated":"","DnsHosts":{},` +
	`"DirectSites":["domain:ru","domain:su","domain:xn--p1ai","domain:yandex.com","domain:yandex.net","domain:yastatic.net","domain:vk.com","domain:userapi.com","domain:sberbank.com","domain:vtb.com"],` +
	`"DirectIp":[],"ProxySites":[],"ProxyIp":[],"BlockSites":[],"BlockIp":[],"DomainStrategy":"IPIfNonMatch","FakeDNS":"false"}`

const goldenStandard = `{"Name":"DNS","GlobalProxy":"true","RemoteDNSType":"DoU","RemoteDNSDomain":"","RemoteDNSIP":"1.1.1.1",` +
	`"DomesticDNSType":"DoU","DomesticDNSDomain":"","DomesticDNSIP":"1.1.1.1","LastUpdated":"","DnsHosts":{},` +
	`"DirectSites":[],"DirectIp":[],"ProxySites":[],"ProxyIp":[],"BlockSites":[],"BlockIp":[],"DomainStrategy":"IPIfNonMatch","FakeDNS":"false"}`

const goldenDoH = `{"Name":"DNS","GlobalProxy":"true","RemoteDNSType":"DoH","RemoteDNSDomain":"https://dns.adguard-dns.com/dns-query","RemoteDNSIP":"94.140.14.14",` +
	`"DomesticDNSType":"DoH","DomesticDNSDomain":"https://dns.adguard-dns.com/dns-query","DomesticDNSIP":"94.140.14.14","LastUpdated":"","DnsHosts":{"dns.adguard-dns.com":"94.140.14.14"},` +
	`"DirectSites":[],"DirectIp":[],"ProxySites":[],"ProxyIp":[],"BlockSites":[],"BlockIp":[],"DomainStrategy":"IPIfNonMatch","FakeDNS":"false"}`

func TestHappRoutingGolden(t *testing.T) {
	ru := builtin(t, "dns_builtin_ru_split")
	h, err := HappRouting(ru)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, h); got != goldenRuSplit {
		t.Errorf("ru split:\n got %s\nwant %s", got, goldenRuSplit)
	}
	// deterministic: same header twice
	if h2, _ := HappRouting(ru); h2 != h {
		t.Error("header differs between calls")
	}

	h, err = HappRouting(builtin(t, "dns_builtin_standard"))
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, h); got != goldenStandard {
		t.Errorf("standard:\n got %s\nwant %s", got, goldenStandard)
	}

	// split_direct off: the same split is ignored, so the profile equals the no-split one; the built-in
	// "Russia through the VPN" has the same main servers and no split
	off := ru
	off.SplitDirect = false
	h, err = HappRouting(off)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, h); got != goldenStandard {
		t.Errorf("ru split, split_direct off:\n got %s\nwant %s", got, goldenStandard)
	}
	h, err = HappRouting(builtin(t, "dns_builtin_ru_proxied"))
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, h); got != goldenStandard {
		t.Errorf("ru proxied:\n got %s\nwant %s", got, goldenStandard)
	}

	// split_direct on without a split changes nothing
	on := builtin(t, "dns_builtin_standard")
	on.SplitDirect = true
	if h2, _ := HappRouting(on); decode(t, h2) != goldenStandard {
		t.Error("split_direct on, no split: profile changed")
	}

	// a DoH-only preset: URL + bootstrap IP + DnsHosts pin
	h, err = HappRouting(Preset{Name: "doh", Servers: []Server{{Kind: KindDoH, Address: "https://dns.adguard-dns.com/dns-query"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, h); got != goldenDoH {
		t.Errorf("doh:\n got %s\nwant %s", got, goldenDoH)
	}
}

func TestHappRoutingMapping(t *testing.T) {
	// the built-in ad-blocking preset: AdGuard on the plain transport, two addresses; the second is a fallback Happ cannot keep
	_, notes, err := happRender(builtin(t, "dns_builtin_adblock"))
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "1 fallback") {
		t.Errorf("adblock notes = %v, %v", notes, err)
	}

	// unsupported leading servers are skipped: DoT, plain with a port, DoH of an unknown host
	p := Preset{Name: "mix", IPv4Only: true, Servers: []Server{
		{Kind: KindDoT, Address: "dns.example.com"}, {Kind: KindPlain, Address: "10.0.0.1:5353"}, {Kind: KindDoH, Address: "https://unknown.example.com/dns-query"},
		{Kind: KindPlain, Address: "10.0.0.2:53"}, {Kind: KindPlain, Address: "10.0.0.3"},
	}}
	h, notes, err := happRender(p)
	if err != nil {
		t.Fatal(err)
	}
	var prof happProfile
	if err := json.Unmarshal([]byte(decode(t, h)), &prof); err != nil {
		t.Fatal(err)
	}
	if prof.RemoteDNSType != "DoU" || prof.RemoteDNSIP != "10.0.0.2" || prof.DomesticDNSIP != "10.0.0.2" {
		t.Errorf("profile = %+v", prof)
	}
	joined := strings.Join(notes, "|")
	for _, want := range []string{"DoT not supported", ": port", "no bootstrap IP", "1 fallback", "ipv4_only ignored"} {
		if !strings.Contains(joined, want) {
			t.Errorf("notes %q lack %q", joined, want)
		}
	}

	// nothing expressible: an error, the caller leaves the header out
	if _, err := HappRouting(Preset{Name: "dot", Servers: []Server{{Kind: KindDoT, Address: "dns.example.com"}}}); err == nil {
		t.Error("DoT-only preset produced a header")
	}

	// SplitDirect off: the split is ignored and said so
	ru := builtin(t, "dns_builtin_ru_split")
	ru.SplitDirect = false
	h, notes, err = happRender(ru)
	if err != nil {
		t.Fatal(err)
	}
	if got := decode(t, h); got != goldenStandard || !strings.Contains(strings.Join(notes, "|"), "split ignored") {
		t.Errorf("split off: %s %v", got, notes)
	}

	// a second split rule with another resolver cannot be kept (one Domestic resolver)
	two := Preset{Name: "two", SplitDirect: true, Servers: []Server{{Kind: KindPlain, Address: "1.1.1.1"}}, Split: []SplitRule{
		{Suffixes: []string{".ru"}, Servers: []Server{{Kind: KindPlain, Address: "77.88.8.8"}}},
		{Suffixes: []string{".de"}, Servers: []Server{{Kind: KindPlain, Address: "9.9.9.9"}}},
		{Suffixes: []string{".su"}, Servers: []Server{{Kind: KindPlain, Address: "77.88.8.8"}}},
	}}
	h, notes, err = happRender(two)
	if err != nil {
		t.Fatal(err)
	}
	prof = happProfile{}
	json.Unmarshal([]byte(decode(t, h)), &prof)
	if strings.Join(prof.DirectSites, ",") != "domain:ru,domain:su" || prof.DomesticDNSIP != "77.88.8.8" ||
		!strings.Contains(strings.Join(notes, "|"), "split rule 2 dropped") {
		t.Errorf("two rules: %+v %v", prof, notes)
	}

	// the header stays a header value: ASCII, no spaces
	for _, r := range h {
		if r > 126 || r <= 32 {
			t.Fatalf("header has %q", r)
		}
	}
}
