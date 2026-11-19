package fleet

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// What the copies of the protocol facts that lived in fleet answered, on the inputs all the copies are asked about.
func TestFactsCopiesInFleet(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := plugin.InboundSpec{ID: "x", Protocol: c.Protocol, Listen: plugin.Listen{Network: "tcp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		v, _ := plugin.VLESSOf(s)
		name, own := vlessProbeName(s)
		lines = append(lines,
			fmt.Sprintf("%-28s hy2SitePort=%d", c.ID, plugin.Hy2SitePort(s)),
			fmt.Sprintf("%-28s vlessProbeName=%q,%v vlessTLS=%v vlessTargetSelf=%v vlessExternalReality=%v", c.ID, name, own, v.TLS(), v.TargetSelf(), v.Security == "reality" && !v.TargetSelf()))
	}
	plugintest.Golden(t, "facts_copies_fleet.golden", lines)
}
