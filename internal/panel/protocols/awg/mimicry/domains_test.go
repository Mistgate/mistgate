package mimicry

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func TestDomainPool(t *testing.T) {
	pool := Domains()
	if len(pool) < 60 || len(pool) > 150 {
		t.Errorf("%d domains in the pool, want 60-150", len(pool))
	}
	seen := map[string]bool{}
	for _, d := range pool {
		if seen[d] {
			t.Errorf("%s is in the pool twice", d)
		}
		seen[d] = true
		if got, err := NormalizeDomain(d); err != nil || got != d {
			t.Errorf("pool entry %q: normalizes to %q, %v", d, got, err)
		}
	}
	// nothing the product promises to stay away from
	for _, bad := range []string{"youtube", "instagram", "facebook", "twitter", "x.com", "telegram", "whatsapp", "discord", "linkedin", "porn", "xxx", "navalny", "meduza", "bbc.", "dw.com", "tiktok"} {
		for _, d := range pool {
			if strings.Contains(d, bad) {
				t.Errorf("pool entry %q matches the blocked or political keyword %q", d, bad)
			}
		}
	}
	pool[0] = "changed.example"
	if Domains()[0] == "changed.example" {
		t.Error("Domains hands out the pool itself")
	}
}

func TestNormalizeDomain(t *testing.T) {
	ok := map[string]string{
		"example.com":                    "example.com",
		"  Example.COM. ":                "example.com",
		"a.bc":                           "a.bc",
		"sub-1.my-site.co.uk":            "sub-1.my-site.co.uk",
		"xn--80ak6aa92e.com":             "xn--80ak6aa92e.com",
		"123.example.com":                "123.example.com",
		strings.Repeat("a", 63) + ".com": strings.Repeat("a", 63) + ".com",
	}
	for in, want := range ok {
		if got, err := NormalizeDomain(in); err != nil || got != want {
			t.Errorf("NormalizeDomain(%.40q) = %.40q, %v; want %.40q", in, got, err, want)
		}
	}
	bad := map[string]string{
		"":                               "empty",
		"   ":                            "blank",
		"localhost":                      "one label",
		".com":                           "empty label",
		"a..com":                         "empty label inside",
		"example..":                      "two trailing dots",
		"-a.example.com":                 "leading hyphen",
		"a-.example.com":                 "trailing hyphen",
		"exa mple.com":                   "space",
		"exa_mple.com":                   "underscore",
		"пример.рф":                      "not punycode",
		"example.com/path":               "a URL",
		"https://example.com":            "a URL with a scheme",
		"user@example.com":               "an address",
		"example.com:443":                "with a port",
		"*.example.com":                  "wildcard",
		"1.2.3.4":                        "an IPv4 address",
		"example.1":                      "numeric TLD",
		"example.c":                      "one-letter TLD",
		"ab--cd.example.com":             "-- without xn",
		strings.Repeat("a", 64) + ".com": "label of 64",
		strings.Repeat("a", 60) + "." + strings.Repeat("b", 60): "over the length cap",
		"exam\x00ple.com": "NUL",
	}
	for in, why := range bad {
		if got, err := NormalizeDomain(in); err == nil {
			t.Errorf("NormalizeDomain(%.40q) = %q accepted (%s)", in, got, why)
		}
	}
	long := strings.Repeat("a", 63) + "." + strings.Repeat("b", 30) + ".com" // 98
	if _, err := NormalizeDomain(long); err != nil {
		t.Errorf("98 characters rejected: %v", err)
	}
	if _, err := NormalizeDomain(long + "m.com"); err == nil {
		t.Error("over MaxDomainLen accepted")
	}
}

func TestGenerateChainDomain(t *testing.T) {
	rng := func() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }
	if _, err := GenerateChain(DNS, Options{Domain: "not a domain"}, rng()); err == nil {
		t.Error("an invalid domain was accepted")
	}
	if _, err := GenerateChain(NTP, Options{Domain: "not a domain"}, rng()); err == nil {
		t.Error("an invalid domain must be rejected whatever the preset")
	}
	// a given domain shows up, upper case and a trailing dot are normalized
	for _, id := range []string{QUIC, CurlQUIC, DNS, SIP} {
		for _, raw := range []string{"Example.com", "example.com."} {
			pkts := chainPkts(t, id, raw, 3)
			for i, p := range pkts {
				if id == QUIC || id == CurlQUIC {
					_, exts := parseHello(t, openInitial(t, p).hello)
					if sniOf(t, exts) != "example.com" {
						t.Errorf("%s: SNI %q", id, sniOf(t, exts))
					}
				} else if !strings.Contains(string(p), "example.com") && !strings.Contains(string(p), "\x07example\x03com\x00") {
					t.Errorf("%s packet %d lacks the domain: %q", id, i, p)
				}
			}
		}
	}
	// no domain: the pool, and every name of the pool turns up
	seen := map[string]bool{}
	for seed := uint64(0); seed < 4000; seed++ {
		pkts := chainPkts(t, SIP, "", seed)
		first := strings.Fields(string(pkts[0]))[1] // "sip:<domain>"
		d := strings.TrimPrefix(first, "sip:")
		if !slices.Contains(domainPool, d) {
			t.Fatalf("seed %d: %q is not from the pool", seed, d)
		}
		seen[d] = true
	}
	if len(seen) < len(domainPool)*9/10 {
		t.Errorf("4000 draws reached only %d of %d pool names", len(seen), len(domainPool))
	}
	// presets without a name do not look at the option, and do not spend rng on it
	for _, id := range []string{STUN, WebRTC, NTP, RTP, SSDP, DTLS} {
		a, _ := GenerateChain(id, Options{}, rng())
		b, _ := GenerateChain(id, Options{Domain: "example.com"}, rng())
		if a != b {
			t.Errorf("%s: the domain changed a packet that has no host name", id)
		}
	}
}
