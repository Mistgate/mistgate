package health

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// What the copies of the protocol facts that live in health answer today, on the inputs all the copies are asked about. The
// target carries the settings in both places, as the snapshot builds it (the profile's merged settings and the node-side spec).
func TestFactsCopiesInHealth(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := plugin.InboundSpec{ID: "x", Protocol: c.Protocol, Listen: plugin.Listen{Network: "tcp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		tg := &target{in: store.FleetInboundRow{Protocol: c.Protocol}, spec: s, settings: []byte(c.Settings)}
		lines = append(lines, fmt.Sprintf("%-28s hy2DecoyPort=%d cdnHost=%q probeName=%q", c.ID, plugin.Hy2SitePort(tg.spec), cdnHost(tg), probeName(s)))
	}
	plugintest.Golden(t, "facts_copies_health.golden", lines)
}
