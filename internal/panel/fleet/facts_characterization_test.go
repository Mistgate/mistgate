package fleet

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// What the copies of the protocol facts that live in fleet answer today, on the inputs all the copies are asked about.
func TestFactsCopiesInFleet(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := plugin.InboundSpec{ID: "x", Protocol: c.Protocol, Listen: plugin.Listen{Network: "tcp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		name, own := vlessProbeName(s)
		lines = append(lines,
			fmt.Sprintf("%-28s hy2SitePort=%d", c.ID, hy2SitePort(s)),
			fmt.Sprintf("%-28s vlessProbeName=%q,%v vlessTLS=%v vlessTargetSelf=%v vlessExternalReality=%v", c.ID, name, own, vlessTLS(s), vlessTargetSelf(s), vlessExternalReality(s)))
	}
	plugintest.Golden(t, "facts_copies_fleet.golden", lines)
}
