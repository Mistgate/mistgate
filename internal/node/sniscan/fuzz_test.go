package sniscan

import (
	"crypto/x509"
	"net/netip"
	"strings"
	"testing"
)

// What a neighbour sends is parsed here: names of a certificate and the Location of a redirect. Whatever the bytes are,
// the functions must not panic, and a name they let through must be a plain host name.

func FuzzHostNames(f *testing.F) {
	for _, s := range []string{"shop.example.com", "*.example.com", "EXAMPLE.com.", "192.0.2.1", "a..b", "-a.example.com", "xn--e1afmkfd.xn--p1ai",
		"a.b", "例え.jp", "a b.example.com", "1.2.3.4.5", "::1", "x.1", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 130) + "com", "\x00.example.com"} {
		f.Add(s, "https://"+s+"/", "shop.example.com")
	}
	f.Add("shop.example.com", "/en", "shop.example.com")
	f.Add("shop.example.com", "//evil.example.net/x", "shop.example.com")
	f.Add("shop.example.com", "http://[::1", "shop.example.com")
	f.Fuzz(func(t *testing.T, san, location, name string) {
		if validHost(san) {
			if len(san) > 253 || san != strings.ToLower(san) || strings.ContainsAny(san, "*: /\x00") || strings.HasPrefix(san, ".") || strings.HasSuffix(san, ".") {
				t.Fatalf("validHost let %q through", san)
			}
			if _, err := netip.ParseAddr(san); err == nil {
				t.Fatalf("validHost let the address %q through", san)
			}
		}
		_ = offsite(location, name)
		_ = registrable(name)
		names, _ := certNames(&x509.Certificate{DNSNames: []string{san, location, name}})
		if len(names) > MaxNames {
			t.Fatalf("%d names", len(names))
		}
		for _, n := range names {
			if !validHost(n) || strings.Contains(n, "*") {
				t.Fatalf("certNames returned %q", n)
			}
		}
	})
}
