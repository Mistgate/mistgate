package protocols

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// Why a server has no IPv6 twin, on the inputs the packages that read the settings are tested with (plugintest.Corpus).
func TestIPv6UnsupportedOnTheSharedInputs(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := plugin.InboundSpec{ID: "x", Protocol: c.Protocol, Listen: plugin.Listen{Network: "tcp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		lines = append(lines, fmt.Sprintf("%-28s IPv6Unsupported=%q", c.ID, IPv6Unsupported(s)))
	}
	// a certificate for the node's IP address
	lines = append(lines, fmt.Sprintf("acme_ip                      IPv6Unsupported=%q", IPv6Unsupported(plugin.InboundSpec{TLS: plugin.TLS{Mode: plugin.TLSAcmeIP}})))
	plugintest.Golden(t, "ipv6.golden", lines)
}

// A server is passed on by a relay entry, and has an IPv6 twin, under one rule (the relay and the subscription ask this one).
func TestIPv6UnsupportedReasons(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, settings, want string
		tls                            plugin.TLSMode
	}{
		{"plain hysteria2", "hysteria2", `{}`, "", plugin.TLSSelfSigned},
		{"vless reality", "vless", `{"reality":{"target":"www.example.org:443"}}`, "", 0},
		{"vless behind a CDN", "vless", `{"cdn":{"host":"cdn.example.org","port":443}}`, "cdn", plugin.TLSAcmeDomain},
		{"vless with an empty CDN host", "vless", `{"cdn":{"host":""}}`, "", plugin.TLSAcmeDomain},
		{"certificate on an IP", "hysteria2", `{}`, "acme_ip", plugin.TLSAcmeIP},
		{"awg", "awg", `{}`, "", 0},
	} {
		spec := plugin.InboundSpec{Protocol: tc.protocol, Settings: []byte(tc.settings), TLS: plugin.TLS{Mode: tc.tls}}
		if got := IPv6Unsupported(spec); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}
