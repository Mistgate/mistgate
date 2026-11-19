package plugin

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

func corpusSpec(c plugintest.Case) InboundSpec {
	return InboundSpec{ID: "x", Protocol: c.Protocol, Listen: Listen{Network: "tcp", Port: 443}, TLS: TLS{Mode: TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
}

// What the copies of a protocol fact that live in plugin answer today, on the inputs all the copies are asked about.
func TestFactsCopiesInPlugin(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := corpusSpec(c)
		r, ok := tcpRoute(s)
		target, name, tok := RealityTarget(s)
		lines = append(lines,
			fmt.Sprintf("%-28s tcpOwnSite=%v hy2SitePort=%d", c.ID, tcpOwnSite(s), Hy2SitePort(s)),
			fmt.Sprintf("%-28s tcpRoute=%v reality=%v site=%v ok=%v", c.ID, r.Names, r.Reality, r.Site, ok),
			fmt.Sprintf("%-28s RealityTarget=%q,%q,%v shown=%q", c.ID, target, name, tok, RealityTargetShown(s)))
	}
	plugintest.Golden(t, "facts_copies_plugin.golden", lines)
}
