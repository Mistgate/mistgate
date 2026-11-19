package hysteria2

import (
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// The panel and the agent ask plugin.Hy2SitePort where the site of a Hysteria2 inbound is; the engine says it by what it listens on.
// The two agree on every Hysteria2 input of the shared set: an inbound whose settings the engine accepts has its site on that port,
// one it refuses has none.
func TestHy2SitePortIsWhereTheEngineListens(t *testing.T) {
	for _, c := range plugintest.Corpus() {
		if c.Protocol != Protocol || c.TLSMode == 0 { // no TLS mode: no certificate, the panel's rule; the engine's settings do not read it
			continue
		}
		spec := plugin.InboundSpec{ID: "de1", Protocol: Protocol, Enabled: true, Listen: plugin.Listen{Network: "udp", Port: 443},
			TLS: plugin.TLS{Mode: plugin.TLSMode(c.TLSMode), ServerName: c.SNI}, Settings: []byte(c.Settings)}
		want := 0
		if cfg, err := parseSpec(spec); err == nil {
			want = cfg.tcpPort
		}
		if got := plugin.Hy2SitePort(spec); int(got) != want {
			t.Errorf("%s: plugin.Hy2SitePort = %d, the engine listens on %d", c.ID, got, want)
		}
	}
}
