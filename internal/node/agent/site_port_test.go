package agent

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// Where the Hysteria2 site is for the agent, on the inputs the packages that read the settings are tested with (plugintest.Corpus):
// the TCP port whose certificate the doctor reads, and the TCP ports the tunnel allowlist publishes for an enabled inbound.
func TestSitePortsOnTheSharedInputs(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		network := "tcp"
		if c.Protocol == "hysteria2" {
			network = "udp"
		}
		s := plugin.InboundSpec{ID: "x", Protocol: c.Protocol, Enabled: true, Listen: plugin.Listen{Network: network, Port: 8443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		m := &model{inbounds: map[string]*inbound{"x": {spec: s}}}
		var tcp []uint32
		for _, p := range tunnelPublishedPorts(m, nil) {
			if p.Network == "tcp" {
				tcp = append(tcp, p.Port)
			}
		}
		lines = append(lines, fmt.Sprintf("%-28s tlsPortOf=%d publishedTCP=%v", c.ID, tlsPortOf(s), tcp))
	}
	plugintest.Golden(t, "site_port.golden", lines)
}
