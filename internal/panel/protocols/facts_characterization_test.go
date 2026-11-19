package protocols

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// What the copy of the CDN fact that lives in protocols answers today, on the inputs all the copies are asked about.
func TestFactsCopiesInProtocols(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := plugin.InboundSpec{ID: "x", Protocol: c.Protocol, Listen: plugin.Listen{Network: "tcp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		lines = append(lines, fmt.Sprintf("%-28s ServerCDN=%v IPv6Unsupported=%q", c.ID, plugin.ServerCDN(s), IPv6Unsupported(s)))
	}
	// a certificate for the node's IP address
	lines = append(lines, fmt.Sprintf("acme_ip                      IPv6Unsupported=%q", IPv6Unsupported(plugin.InboundSpec{TLS: plugin.TLS{Mode: plugin.TLSAcmeIP}})))
	plugintest.Golden(t, "facts_copies_protocols.golden", lines)
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
