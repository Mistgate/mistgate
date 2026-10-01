package subs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/dns"
)

// The Mihomo core is the judge of the profile: `mihomo -t` parses every field of every proxy and of the dns section.
// The test needs the binary (Linux; scripts/e2e/m3.sh builds v1.19.32 once into ~/.cache/mistgate-tests/) and is skipped without
// it: set MIHOMO_BIN to use another one.
//
//	wsl -d Ubuntu -u root -- bash -c 'cd <repo> && go test ./internal/panel/subs -run MihomoCore -v'

func mihomoBin(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("MIHOMO_BIN"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	p := filepath.Join(home, ".cache", "mistgate-tests", "mihomo-v1.19.32")
	if _, err := os.Stat(p); err != nil {
		t.Skip("no mihomo binary: set MIHOMO_BIN or build v1.19.32 into ~/.cache/mistgate-tests/mihomo-v1.19.32")
	}
	return p
}

// mihomoTest runs `mihomo -t` on a config and returns whether it accepted it, with the core's output.
func mihomoTest(t *testing.T, config []byte) (bool, string) {
	t.Helper()
	bin := mihomoBin(t)
	dir := t.TempDir() // an empty home: no geo files, nothing downloaded for a profile that needs none
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, config, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bin, "-t", "-d", dir, "-f", cfg).CombinedOutput()
	return err == nil && strings.Contains(string(out), "test is successful"), string(out)
}

// MihomoCheck fails the test when the core refuses the profile (exported for the tests of this package's users).
func MihomoCheck(t *testing.T, config []byte) {
	t.Helper()
	if ok, out := mihomoTest(t, config); !ok {
		t.Fatalf("mihomo -t refused the profile:\n%s\n%s", out, config)
	}
}

// Every proxy shape the plugins produce, under every shape of DNS preset, is accepted by the core.
func TestMihomoCoreAcceptsTheProfiles(t *testing.T) {
	mihomoBin(t)
	all := []string{
		fixture(t, hy2Line), fixture(t, "../protocols/hysteria2/testdata/mihomo_gecko_hop.yaml"),
		fixture(t, "../protocols/hysteria2/testdata/mihomo_pinned.yaml"), fixture(t, "../protocols/hysteria2/testdata/mihomo_noobfs.yaml"),
		fixture(t, awgLine), fixture(t, "../protocols/awg/testdata/awg20.mihomo.yaml"),
	}
	names := []string{"\U0001F1E9\U0001F1EA de1 · hy2", "ru · gecko hop", "pinned", "no obfs", "awg 3.1", "awg 2.0"}
	for _, c := range []struct {
		name   string
		title  string
		preset *dns.Preset
	}{
		{"no dns section", "Example VPN", nil},
		{"russia split, plain", "Example VPN", ruSplit()},
		{"a title with a space, unicode and a percent", "Моя сеть 50%", ruSplit()},
		{"DoH", "Example", &dns.Preset{Name: "doh", Transport: dns.KindDoH, Servers: []dns.Server{{Variant: "adguard/default"}, {Variant: "cloudflare/standard"}}}},
		{"DoT", "Example", &dns.Preset{Name: "dot", Transport: dns.KindDoT, Servers: []dns.Server{{Variant: "quad9/standard"}}}},
		{"DoT with a split", "Example", &dns.Preset{Name: "dot-split", Transport: dns.KindDoT, SplitDirect: true,
			Servers: []dns.Server{{Variant: "google/standard"}}, Split: []dns.SplitRule{{Suffixes: []string{".ru", ".рф"}, Servers: []dns.Server{{Variant: "yandex/basic"}}}}}},
		{"custom: a port, DoT with a port, an IPv6 server", "Example", &dns.Preset{Name: "custom", Transport: dns.KindPlain,
			Servers: []dns.Server{{Kind: dns.KindPlain, Address: "9.9.9.9:5353"}, {Kind: dns.KindDoT, Address: "dns.example.com:8853"}, {Kind: dns.KindPlain, Address: "2606:4700:4700::1111"}}}},
		{"an IPv6-only preset", "Example", &dns.Preset{Name: "v6", Transport: dns.KindPlain, Servers: []dns.Server{{Kind: dns.KindPlain, Address: "2606:4700:4700::1111"}}}},
		{"no plain server (the system resolver for the nodes)", "Example", &dns.Preset{Name: "mullvad", Transport: dns.KindPlain, Servers: []dns.Server{{Variant: "mullvad/standard"}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b, err := mihomoProfile{title: c.title, lines: all, names: names, preset: c.preset}.build()
			if err != nil {
				t.Fatal(err)
			}
			MihomoCheck(t, b)
		})
	}

	// The check is a real one: the core refuses what it does not understand.
	b, _ := mihomoProfile{title: "T", lines: all[:1], names: names[:1]}.build()
	if ok, out := mihomoTest(t, []byte(strings.Replace(string(b), "obfs: salamander", "obfs: nothing", 1))); ok || !strings.Contains(out, "obfs") {
		t.Errorf("a broken profile was accepted: %v\n%s", ok, out)
	}
}
