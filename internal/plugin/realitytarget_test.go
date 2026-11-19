package plugin

import "testing"

func TestNormalizeRealityTarget(t *testing.T) {
	for in, want := range map[string]string{
		"shop.example.com:443":      "shop.example.com:443",
		" Shop.Example.com.:0443 ":  "shop.example.com:443",
		"SHOP.example.COM:8443":     "shop.example.com:8443",
		"shop.example.com":          "shop.example.com", // not host:port: left for the panel to refuse
		"":                          "",
		"[2001:db8::1]:443":         "[2001:db8::1]:443",
		"shop.example.com:notaport": "shop.example.com:notaport",
	} {
		if got := NormalizeRealityTarget(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
