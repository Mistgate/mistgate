package subs

import (
	"slices"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/dns"
)

// A preset with TorrentDirect, the ad and Max blocks and the Russian split: the torrent rules come after the blocks and before the split,
// and a preset without the flag has none of them in any format.

func ruTorrent(torrents bool) *dns.Preset {
	return &dns.Preset{Name: "ru", Transport: dns.KindPlain, SplitDirect: true, BlockMax: true, BlockAds: true, TorrentDirect: torrents,
		Servers: []dns.Server{{Variant: "cloudflare/standard"}, {Variant: "google/standard"}},
		Split:   []dns.SplitRule{{Suffixes: []string{".ru"}, Servers: []dns.Server{{Variant: "yandex/basic"}}}}}
}

func TestMihomoProfileTorrentDirect(t *testing.T) {
	build := func(p *dns.Preset) (profileDoc, string) {
		b, err := mihomoProfile{title: "Example VPN", lines: []string{fixture(t, hy2Line)}, names: []string{"de1"}, preset: p}.build()
		if err != nil {
			t.Fatal(err)
		}
		return parseProfile(t, b), string(b)
	}
	d, text := build(ruTorrent(true))
	idx := func(rule string) int { return slices.Index(d.Rules, rule) }
	first, last := idx("PROCESS-NAME,qbittorrent.exe,DIRECT"), idx("RULE-SET,geosite-category-public-tracker,DIRECT")
	if first < 0 || last < first || idx("PROCESS-NAME,org.proninyaroslav.libretorrent,DIRECT") < first || idx("PROCESS-NAME,org.proninyaroslav.libretorrent,DIRECT") > last {
		t.Fatalf("torrent rules: %v", d.Rules)
	}
	if !(idx("DOMAIN-SUFFIX,oneme.ru,REJECT") < first && idx("RULE-SET,geosite-category-ads-all,REJECT") < first && last < idx("DOMAIN-SUFFIX,ru,DIRECT") && idx("DOMAIN-SUFFIX,ru,DIRECT") < idx("MATCH,Example VPN")) {
		t.Errorf("the order of the rules: %v", d.Rules)
	}
	if !strings.Contains(text, "category-public-tracker.mrs") {
		t.Error("the tracker rule provider is missing")
	}
	if _, text := build(ruTorrent(false)); strings.Contains(text, "PROCESS-NAME") || strings.Contains(text, "public-tracker") {
		t.Error("flag off: the profile names torrents")
	}
}

func TestSingboxProfileTorrentDirect(t *testing.T) {
	lines := fixtures(t, singboxFragments)
	b, err := singboxProfile{title: "Example VPN", lines: lines, names: jsonNames, preset: ruTorrent(true)}.build()
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeObject(t, b)
	var seq []string
	for _, r := range doc["route"].(map[string]any)["rules"].([]any) {
		m := r.(map[string]any)
		switch {
		case m["ip_is_private"] != nil:
			seq = append(seq, "private")
		case m["process_name"] != nil:
			seq = append(seq, "process")
		case m["package_name"] != nil:
			seq = append(seq, "package")
		case m["protocol"] == "bittorrent":
			seq = append(seq, "bittorrent")
		case m["domain_suffix"] != nil && m["action"] == "route":
			seq = append(seq, "split")
		case m["rule_set"] != nil && m["action"] == "route":
			seq = append(seq, "trackers")
			if m["outbound"] != "direct" {
				t.Errorf("tracker rule = %v", m)
			}
		default:
			seq = append(seq, m["action"].(string))
		}
	}
	if want := []string{"sniff", "hijack-dns", "private", "reject", "reject", "process", "package", "bittorrent", "trackers", "split"}; !slices.Equal(seq, want) {
		t.Errorf("route rules = %v, want %v", seq, want)
	}
	b, _ = singboxProfile{title: "Example VPN", lines: lines, names: jsonNames, preset: ruTorrent(false)}.build()
	if strings.Contains(string(b), "bittorrent") || strings.Contains(string(b), "public-tracker") {
		t.Error("flag off: the document names torrents")
	}
}

func TestXrayJSONTorrentDirect(t *testing.T) {
	lines := fixtures(t, xrayFragments)
	b, err := xrayProfile{lines: lines, names: jsonNames, preset: ruTorrent(true)}.build()
	if err != nil {
		t.Fatal(err)
	}
	for i, cfg := range decodeArray(t, b) {
		block, proto, trackers, direct := -1, -1, -1, -1
		for j, r := range cfg["routing"].(map[string]any)["rules"].([]any) {
			m := r.(map[string]any)
			if p, ok := m["protocol"].([]any); ok && len(p) == 1 && p[0] == "bittorrent" && m["outboundTag"] == "direct" {
				proto = j
			}
			d, _ := m["domain"].([]any)
			switch {
			case m["outboundTag"] == "block":
				block = j
			case len(d) == 1 && d[0] == "geosite:category-public-tracker" && m["outboundTag"] == "direct":
				trackers = j
			case len(d) > 0 && d[0] == "domain:ru" && m["outboundTag"] == "direct":
				direct = j
			}
		}
		if block < 0 || proto < block || trackers < proto || direct < trackers {
			t.Errorf("config %d: block %d, protocol %d, trackers %d, .ru direct %d", i, block, proto, trackers, direct)
		}
	}
	b, _ = xrayProfile{lines: lines, names: jsonNames, preset: ruTorrent(false)}.build()
	if strings.Contains(string(b), "bittorrent") || strings.Contains(string(b), "public-tracker") {
		t.Error("flag off: the document names torrents")
	}
}
