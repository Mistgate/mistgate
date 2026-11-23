package fleet

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// The name the TCP check sends a VLESS listener, and whether it is the node's own, on the inputs the packages that read the settings
// are tested with (plugintest.Corpus).
func TestVLESSProbeNameOnTheSharedInputs(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := plugin.InboundSpec{ID: "x", Protocol: c.Protocol, Listen: plugin.Listen{Network: "tcp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		name, own := vlessProbeName(s)
		lines = append(lines, fmt.Sprintf("%-28s vlessProbeName=%q,%v", c.ID, name, own))
	}
	plugintest.Golden(t, "vless_probe_name.golden", lines)
}
