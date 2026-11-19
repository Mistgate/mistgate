package plugin

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

func corpusSpec(c plugintest.Case) InboundSpec {
	return InboundSpec{ID: "x", Protocol: c.Protocol, Listen: Listen{Network: "tcp", Port: 443}, TLS: TLS{Mode: TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
}

// What the facts of an inbound's settings are on the inputs the packages that ask them are tested with (plugintest.Corpus).
func TestProtocolFactsOnTheSharedInputs(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		s := corpusSpec(c)
		r, ok := tcpRoute(s)
		target, name, tok := RealityTarget(s)
		lines = append(lines,
			fmt.Sprintf("%-28s tcpOwnSite=%v Hy2SitePort=%d ServerCDN=%v", c.ID, tcpOwnSite(s), Hy2SitePort(s), ServerCDN(s)),
			fmt.Sprintf("%-28s tcpRoute=%v reality=%v site=%v ok=%v", c.ID, r.Names, r.Reality, r.Site, ok),
			fmt.Sprintf("%-28s RealityTarget=%q,%q,%v shown=%q", c.ID, target, name, tok, RealityTargetShown(s)))
	}
	plugintest.Golden(t, "facts_corpus.golden", lines)
}
