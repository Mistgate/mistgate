//go:build !js

package fleet

import (
	"context"
	"net"
	"net/netip"

	"github.com/mistgate/mistgate/internal/tcpcheck"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

const panelSendsUDP = true

// The panel sends the probes of a port check itself when no node can (the VPS panel is the sender of last resort).
var (
	defaultSendUDP  = udpcheck.Send
	defaultProbeTCP = tcpcheck.Probe
)

// panelHasIPv6 says whether the panel's own host has a route over IPv6 to the internet: a UDP "connect" to a public resolver
// sends nothing and gives the source address the kernel would use. Without one the panel cannot check an IPv6 address from outside.
func panelHasIPv6(ctx context.Context) bool {
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp6", "[2001:4860:4860::8888]:53")
	if err != nil {
		return false
	}
	defer c.Close()
	ap, err := netip.ParseAddrPort(c.LocalAddr().String())
	return err == nil && ap.Addr().IsGlobalUnicast() && !ap.Addr().IsPrivate()
}
