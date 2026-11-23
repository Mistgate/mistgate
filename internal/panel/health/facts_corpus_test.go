package health

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// The CDN host and the name a probe sends, on the inputs the packages that read the settings are tested with (plugintest.Corpus).
func TestCDNHostAndProbeNameOnTheSharedInputs(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := plugin.InboundSpec{ID: "x", Protocol: c.Protocol, Listen: plugin.Listen{Network: "tcp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		tg := &target{in: store.FleetInboundRow{Protocol: c.Protocol}, spec: s}
		lines = append(lines, fmt.Sprintf("%-28s cdnHost=%q probeName=%q", c.ID, cdnHost(tg), probeName(s)))
	}
	plugintest.Golden(t, "facts_corpus.golden", lines)
}
