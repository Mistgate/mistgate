package auth

import (
	"net/http"
	"net/netip"
	"testing"
)

func TestParseProxies(t *testing.T) {
	got, err := ParseProxies([]string{"10.0.0.0/8", " 192.0.2.7 ", "", "2001:db8::/32", "::ffff:198.51.100.1"})
	if err != nil || len(got) != 4 {
		t.Fatalf("%v %v", got, err)
	}
	tr := NewProxyTrust(got)
	for _, ok := range []string{"10.1.2.3", "192.0.2.7", "2001:db8:1::5", "198.51.100.1"} {
		if !tr.trusts(netip.MustParseAddr(ok)) {
			t.Errorf("%s not trusted", ok)
		}
	}
	for _, bad := range []string{"192.0.2.8", "11.0.0.1", "2001:db9::1"} {
		if tr.trusts(netip.MustParseAddr(bad)) {
			t.Errorf("%s trusted", bad)
		}
	}
	for _, bad := range []string{"nonsense", "10.0.0.0/33", "300.1.1.1"} {
		if _, err := ParseProxies([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func TestClientIP(t *testing.T) {
	proxies, _ := ParseProxies([]string{"10.0.0.0/8", "127.0.0.1", "fd00::/8"})
	tr := NewProxyTrust(proxies)
	none := NewProxyTrust(nil)
	for _, tc := range []struct {
		name  string
		trust ProxyTrust
		peer  string
		h     http.Header
		want  string
	}{
		{"no proxies configured: headers are ignored", none, "198.51.100.9:1234", hdr("X-Forwarded-For", "203.0.113.5"), "198.51.100.9"},
		{"untrusted peer cannot forge", tr, "198.51.100.9:1234", hdr("X-Forwarded-For", "203.0.113.5", "Forwarded", "for=203.0.113.6"), "198.51.100.9"},
		{"trusted peer, no header", tr, "10.0.0.1:1", nil, "10.0.0.1"},
		{"trusted peer, XFF", tr, "10.0.0.1:1", hdr("X-Forwarded-For", "203.0.113.5"), "203.0.113.5"},
		{"client-supplied left entries are not believed", tr, "10.0.0.1:1", hdr("X-Forwarded-For", "1.2.3.4, 203.0.113.5"), "203.0.113.5"},
		{"several trusted hops are skipped", tr, "10.0.0.1:1", hdr("X-Forwarded-For", "9.9.9.9, 203.0.113.5, 10.0.0.2, 127.0.0.1"), "203.0.113.5"},
		{"split header lines", tr, "10.0.0.1:1", hdr("X-Forwarded-For", "9.9.9.9", "X-Forwarded-For", "203.0.113.5"), "203.0.113.5"},
		{"all trusted: the leftmost", tr, "10.0.0.1:1", hdr("X-Forwarded-For", "10.0.0.9, 10.0.0.2"), "10.0.0.9"},
		{"garbage in the chain: fall back to the peer", tr, "10.0.0.1:1", hdr("X-Forwarded-For", "203.0.113.5, unknown"), "10.0.0.1"},
		{"empty header value", tr, "10.0.0.1:1", hdr("X-Forwarded-For", ""), "10.0.0.1"},
		{"XFF with port and IPv4-mapped", tr, "10.0.0.1:1", hdr("X-Forwarded-For", "[::ffff:203.0.113.5]:4711"), "203.0.113.5"},
		{"Forwarded", tr, "10.0.0.1:1", hdr("Forwarded", `for=203.0.113.5;proto=https;by=10.0.0.1`), "203.0.113.5"},
		{"Forwarded, quoted IPv6 with port, chain", tr, "10.0.0.1:1", hdr("Forwarded", `for=1.2.3.4, for="[2001:db8::7]:4711";proto=https, For=10.0.0.3`), "2001:db8::7"},
		{"Forwarded obfuscated node", tr, "10.0.0.1:1", hdr("Forwarded", `for=_hidden`), "10.0.0.1"},
		{"Forwarded element without for", tr, "10.0.0.1:1", hdr("Forwarded", `proto=https`), "10.0.0.1"},
		{"both headers agree", tr, "10.0.0.1:1", hdr("X-Forwarded-For", "203.0.113.5", "Forwarded", "for=203.0.113.5"), "203.0.113.5"},
		{"both headers disagree: neither is believed", tr, "10.0.0.1:1", hdr("X-Forwarded-For", "203.0.113.5", "Forwarded", "for=203.0.113.99"), "10.0.0.1"},
		{"IPv6 proxy", tr, "[fd00::1]:443", hdr("X-Forwarded-For", "2001:db8::9"), "2001:db8::9"},
		{"IPv4-mapped peer", tr, "[::ffff:10.0.0.1]:443", hdr("X-Forwarded-For", "203.0.113.5"), "203.0.113.5"},
		{"unparseable peer", tr, "garbage", hdr("X-Forwarded-For", "203.0.113.5"), ""},
	} {
		got := tc.trust.ClientIP(tc.peer, tc.h)
		if g := map[bool]string{true: got.String(), false: ""}[got.IsValid()]; g != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, g, tc.want)
		}
	}
}

func TestSourceKey(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.5":               "203.0.113.5",
		"::ffff:203.0.113.5":        "203.0.113.5",
		"2001:db8:1:2:3:4:5:6":      "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff::1": "2001:db8:1:2::/64", // same /64, same key
		"2001:db8:1:3::1":           "2001:db8:1:3::/64",
		"fe80::1":                   "fe80::/64",
	} {
		if got := SourceKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
	if SourceKey(netip.Addr{}) != "unknown" {
		t.Error("invalid address key")
	}
}
