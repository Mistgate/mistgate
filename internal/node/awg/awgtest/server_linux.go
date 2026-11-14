//go:build linux

package awgtest

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awguapi"
)

// ServerCfg is a listening AmneziaWG interface: the exit of a relay test. Unlike the engine's stand it is a bare
// amneziawg-go process, so a test can give it any peers and read what it learned about them.
type ServerCfg struct {
	Flavor string
	Addrs  []string // the interface's own addresses, CIDR
	Priv   [32]byte
	Port   uint16
	Obf    awgcfg.Obfuscation
	MTU    int // 0 = 1280
	Peers  []awgcfg.Peer
}

// StartServer starts amneziawg-go in ns listening on cfg.Port (both IPv4 and IPv6), adds the peers, addresses the
// TUN and brings it up. The returned Client is only a handle on the process: use Stats/Peer, not Handshaken.
func StartServer(t testing.TB, ns NS, cfg ServerCfg) *Client {
	t.Helper()
	c := startDaemon(t, ns, cfg.Flavor, "mg3s")
	text := deviceText(cfg.Flavor, cfg.Priv, cfg.Port, cfg.Obf) + awguapi.PeersSet(false, cfg.Peers)
	if err := c.uapi("set=1\n" + text); err != nil {
		t.Fatalf("configure server (%s): %v\n%s", cfg.Flavor, err, c.proc.Output())
	}
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = 1280
	}
	ns.Must(t, "ip", "link", "set", c.Iface, "mtu", fmt.Sprint(mtu))
	for _, a := range cfg.Addrs {
		ns.Must(t, "ip", "addr", "add", a, "dev", c.Iface)
	}
	ns.Must(t, "ip", "link", "set", c.Iface, "up")
	t.Cleanup(c.Stop)
	return c
}

// Peer is the interface's current view of one peer (endpoint, last handshake, counters).
func (c *Client) Peer(pub [32]byte) (awgcfg.PeerStat, bool) {
	st, err := c.Stats()
	if err != nil {
		return awgcfg.PeerStat{}, false
	}
	for _, p := range st {
		if p.PublicKey == pub {
			return p, true
		}
	}
	return awgcfg.PeerStat{}, false
}

// SetEndpointText sends a raw peer "endpoint=" line and returns the UAPI error ("" = accepted). It is how a test finds
// out what the daemon does with a host name (it does not resolve: that is the config tool's job).
func (c *Client) SetEndpointText(pub [32]byte, endpoint string) string {
	if err := c.uapi("set=1\n" + awguapi.PeersSet(false, []awgcfg.Peer{{PublicKey: pub, UpdateOnly: true, Endpoint: endpoint}})); err != nil {
		return err.Error()
	}
	return ""
}
