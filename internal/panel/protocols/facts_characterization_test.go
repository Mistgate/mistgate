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
		lines = append(lines, fmt.Sprintf("%-28s ServerCDN=%v IPv6Unsupported=%q", c.ID, ServerCDN(s), IPv6Unsupported(s)))
	}
	// a certificate for the node's IP address
	lines = append(lines, fmt.Sprintf("acme_ip                      IPv6Unsupported=%q", IPv6Unsupported(plugin.InboundSpec{TLS: plugin.TLS{Mode: plugin.TLSAcmeIP}})))
	plugintest.Golden(t, "facts_copies_protocols.golden", lines)
}
