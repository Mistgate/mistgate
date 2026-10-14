package dns

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var updateDNSGolden = flag.Bool("update-dns-golden", false, "rewrite testdata/{xray,singbox}_dns_*.json")

// planPresets are the presets the Xray and sing-box tables share.
var planPresets = []struct {
	name string
	p    Preset
}{
	{"standard", Preset{Name: "std", Servers: variantServers("cloudflare/standard", "google/standard"), Transport: KindPlain}},
	{"russia_split", Preset{Name: "ru", Servers: variantServers("cloudflare/standard"), Transport: KindPlain, SplitDirect: true,
		Split: []SplitRule{{Suffixes: []string{".ru", "su", ".xn--p1ai"}, Servers: variantServers("yandex/basic")}}}},
	{"russia_split_off", Preset{Name: "ru-off", Servers: variantServers("cloudflare/standard"), Transport: KindPlain,
		Split: []SplitRule{{Suffixes: []string{".ru"}, Servers: variantServers("yandex/basic")}}}},
	{"doh_adblock", Preset{Name: "doh", Servers: variantServers("adguard/default"), Transport: KindDoH}},
	{"dot_secure", Preset{Name: "dot", Servers: variantServers("quad9/standard"), Transport: KindDoT}},
	{"custom_port_dot_doh", Preset{Name: "custom", Transport: KindPlain, Servers: []Server{
		plainSrv("9.9.9.9:5353"), {Kind: KindDoT, Address: "dns.example.com:8853"}, // no IP known: sing-box drops it, Xray has no DoT
		{Kind: KindDoT, Address: "dns.quad9.net"},
		{Kind: KindDoH, Address: "https://unknown.example.com/dns-query"}, // no IP known: dropped
		{Kind: KindDoH, Address: "https://dns.adguard-dns.com/custom"},    // the catalog knows the host
		{Kind: KindDoH, Address: "https://1.1.1.1:8443/dns-query"},        // IP literal
	}}},
	{"ipv6_ignored", Preset{Name: "v6", Transport: KindPlain, Servers: []Server{plainSrv("2606:4700:4700::1111"), plainSrv("[2606:4700:4700::1111]:53"), plainSrv("1.1.1.1")}}},
	{"first_rule_wins", Preset{Name: "dups", Servers: []Server{plainSrv("1.1.1.1")}, Transport: KindPlain, SplitDirect: true, Split: []SplitRule{
		{Suffixes: []string{"a.example", "bad,suffix"}, Servers: []Server{plainSrv("77.88.8.8")}},
		{Suffixes: []string{"a.example", "b.example"}, Servers: []Server{plainSrv("77.88.8.1"), plainSrv("77.88.8.2")}},
		{Suffixes: []string{"a.example"}, Servers: []Server{plainSrv("77.88.8.3")}}, // every suffix claimed
		{Suffixes: []string{"c.example"}, Servers: variantServers("no/such")},       // no usable server
	}}},
	{"custom_doh_path", Preset{Name: "doh-path", Transport: KindDoH, Servers: []Server{{Kind: KindDoH, Address: "https://1.1.1.1:8443/custom"}}}},
	{"split_doh", Preset{Name: "split-doh", Servers: variantServers("adguard/default"), Transport: KindDoH, SplitDirect: true,
		Split: []SplitRule{{Suffixes: []string{"ru"}, Servers: variantServers("google/standard")}}}},
	{"mullvad_doh_only", Preset{Name: "mullvad", Servers: variantServers("mullvad/standard"), Transport: KindPlain}},
	{"empty", Preset{Name: "empty", Transport: KindPlain}},
}

func presetNamed(name string) Preset {
	for _, pp := range planPresets {
		if pp.name == name {
			return pp.p
		}
	}
	panic("no preset " + name)
}

func splitLine(sp SplitPlan) string {
	return fmt.Sprintf("%d %s => %s", sp.Rule, strings.Join(sp.Suffixes, ","), strings.Join(line(sp.Endpoints), " "))
}

func TestClientPlan(t *testing.T) {
	type want struct {
		main  string   // line(main), space separated; "" = an error
		split []string // splitLine
		notes []string // substrings, in order
	}
	cases := []struct {
		preset string
		xray   want
		singb  want
	}{
		{"standard",
			want{main: "plain:1.1.1.1 plain:8.8.8.8 plain:1.0.0.1 plain:8.8.4.4"},
			want{main: "plain:1.1.1.1 plain:8.8.8.8 plain:1.0.0.1 plain:8.8.4.4"}},
		{"russia_split",
			want{main: "plain:1.1.1.1 plain:1.0.0.1", split: []string{"0 ru,su,xn--p1ai => plain:77.88.8.8 plain:77.88.8.1"}},
			want{main: "plain:1.1.1.1 plain:1.0.0.1", split: []string{"0 ru,su,xn--p1ai => plain:77.88.8.8 plain:77.88.8.1"}}},
		{"russia_split_off",
			want{main: "plain:1.1.1.1 plain:1.0.0.1", notes: []string{"split ignored (SplitDirect off)"}},
			want{main: "plain:1.1.1.1 plain:1.0.0.1", notes: []string{"split ignored (SplitDirect off)"}}},
		{"doh_adblock",
			want{main: "doh:https://dns.adguard-dns.com/dns-query"},
			want{main: "doh:https://dns.adguard-dns.com/dns-query"}},
		{"dot_secure", // Xray has no DoT: a catalog server steps down to plain
			want{main: "plain:9.9.9.9 plain:149.112.112.112"},
			want{main: "dot:dns.quad9.net"}},
		{"custom_port_dot_doh",
			want{main: "plain:9.9.9.9:5353 doh:https://dns.adguard-dns.com/custom doh:https://1.1.1.1:8443/dns-query",
				notes: []string{"server dns.example.com:8853 skipped: DoT not supported", "server dns.quad9.net skipped: DoT not supported", "server https://unknown.example.com/dns-query skipped: no bootstrap IP"}},
			want{main: "plain:9.9.9.9:5353 dot:dns.quad9.net doh:https://dns.adguard-dns.com/custom doh:https://1.1.1.1:8443/dns-query",
				notes: []string{"server dns.example.com:8853 skipped: no bootstrap IP", "server https://unknown.example.com/dns-query skipped: no bootstrap IP"}}},
		{"ipv6_ignored",
			want{main: "plain:1.1.1.1", notes: []string{"server 2606:4700:4700::1111 skipped: IPv6", "server [2606:4700:4700::1111]:53 skipped: IPv6"}},
			want{main: "plain:1.1.1.1", notes: []string{"server 2606:4700:4700::1111 skipped: IPv6", "server [2606:4700:4700::1111]:53 skipped: IPv6"}}},
		{"first_rule_wins",
			want{main: "plain:1.1.1.1", split: []string{"0 a.example => plain:77.88.8.8", "1 b.example => plain:77.88.8.1 plain:77.88.8.2", "2  => plain:77.88.8.3"},
				notes: []string{`suffix "bad,suffix" skipped`, "server no/such skipped: not in the catalog", "split rule 4 dropped: no server Xray can carry"}},
			want{main: "plain:1.1.1.1", split: []string{"0 a.example => plain:77.88.8.8", "1 b.example => plain:77.88.8.1 plain:77.88.8.2", "2  => plain:77.88.8.3"},
				notes: []string{`suffix "bad,suffix" skipped`, "server no/such skipped: not in the catalog", "split rule 4 dropped: no server sing-box can carry"}}},
		{"mullvad_doh_only", // no plain DNS: both step up to DoH
			want{main: "doh:https://dns.mullvad.net/dns-query"},
			want{main: "doh:https://dns.mullvad.net/dns-query"}},
		{"empty", want{}, want{}},
	}
	for _, c := range cases {
		for _, cl := range []struct {
			c Client
			w want
		}{{ClientXray, c.xray}, {ClientSingbox, c.singb}} {
			t.Run(c.preset+"/"+string(cl.c), func(t *testing.T) {
				main, split, notes, err := presetNamed(c.preset).ClientPlan(cl.c)
				if cl.w.main == "" {
					if err == nil {
						t.Fatalf("want an error, got main %q", line(main))
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				eq(t, "main", line(main), strings.Fields(cl.w.main))
				var got []string
				for _, sp := range split {
					got = append(got, splitLine(sp))
				}
				eq(t, "split", got, cl.w.split)
				i := 0
				for _, sub := range cl.w.notes { // in order
					j := slices.IndexFunc(notes[i:], func(n string) bool { return strings.Contains(n, sub) })
					if j < 0 {
						t.Fatalf("notes %q lack %q (after %d)", notes, sub, i)
					}
					i += j + 1
				}
				if len(notes) != len(cl.w.notes) {
					t.Errorf("notes = %q, want %d of them", notes, len(cl.w.notes))
				}
			})
		}
	}
}

// ClientPlan keeps what Mihomo had: an IPv6-only preset keeps its resolver for the client that takes IPv6 (unless the
// preset is IPv4Only), and is an error for the two IPv4-only clients.
func TestClientPlanIPv6Only(t *testing.T) {
	p := Preset{Name: "v6", Servers: []Server{plainSrv("2606:4700:4700::1111")}, Transport: KindPlain}
	if main, _, _, err := p.ClientPlan(ClientMihomo); err != nil || len(main) != 1 {
		t.Fatalf("Mihomo: %v %v", main, err)
	}
	for _, c := range []Client{ClientXray, ClientSingbox} {
		if _, _, _, err := p.ClientPlan(c); err == nil {
			t.Fatalf("%s: want an error", c)
		}
	}
	p.IPv4Only = true
	if _, _, _, err := p.ClientPlan(ClientMihomo); err == nil {
		t.Fatal("Mihomo with IPv4Only: want an error")
	}
}

// goldenJSON compares v, indented, with testdata/<name>; -update-dns-golden rewrites the file.
func goldenJSON(t *testing.T, name string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	path := filepath.Join("testdata", name)
	if *updateDNSGolden {
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update-dns-golden)", err)
	}
	if string(want) != string(b) {
		t.Errorf("%s differs (run go test -update-dns-golden):\n got %s\nwant %s", name, b, want)
	}
}
