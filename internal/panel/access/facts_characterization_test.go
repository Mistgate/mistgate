package access

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// What the copies of the protocol facts that lived in access answered, on the inputs all the copies are asked about.
func TestFactsCopiesInAccess(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := plugin.InboundSpec{ID: "x", Protocol: c.Protocol, Listen: plugin.Listen{Network: "tcp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		v, _ := plugin.VLESSOf(s)
		lines = append(lines, fmt.Sprintf("%-28s hy2TCPPort=%d vlessTargetSelf=%v vlessTargetHost=%q relayUnsupported=%q",
			c.ID, plugin.Hy2SitePort(s), v.TargetSelf(), v.TargetHost(), protocols.IPv6Unsupported(s)))
	}
	lines = append(lines, fmt.Sprintf("acme_ip                      relayUnsupported=%q", protocols.IPv6Unsupported(plugin.InboundSpec{TLS: plugin.TLS{Mode: plugin.TLSAcmeIP}})))
	plugintest.Golden(t, "facts_copies_access.golden", lines)
}
