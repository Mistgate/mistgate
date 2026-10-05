package egress

import (
	"fmt"
	"net"
	"sync"

	"github.com/apernet/hysteria/extras/v2/outbounds"
)

// WithDevice makes the egress leave through the named network device (SO_BINDTODEVICE on every TCP dialer
// and UDP socket, Linux only). This is how the WARP egress works: the device is the WARP tunnel, the routing
// rules of internal/node/warp send anything bound to it into the WARP table, and a missing device or a dead
// table makes a dial fail instead of falling back to the direct route (fail closed).
//
// The dialer is built on first use and again after a failure, never at construction: the hysteria
// constructor insists that the device exists right now, and the tunnel can come and go (an agent start, a
// pause, a link flap). A built dialer keeps working across a re-creation of the device, because the bind is
// by name. Everything else (node resolver, blocklist) is the same stage as Direct.
func WithDevice(name string) Option { return func(s *stage) { s.device = name } }

// IPv4Only makes the dialer use IPv4 addresses only (hysteria DirectOutboundMode4). The WARP egress sets it while
// the account has no IPv6 address: new accounts often have none, and a v6 dial would only hang or fail.
func IPv4Only() Option { return func(s *stage) { s.ipv4Only = true } }

// IPv4OnlyWhen makes the egress use IPv4 only while on() returns true, asked on every dial and every datagram
// ("IPv6 for clients" off on the node: a hoster's IPv6 range may be geolocated in another country). A destination
// that has only IPv6 addresses is refused with ErrNoIPv4. Connections that are already open keep their address family.
func IPv4OnlyWhen(on func() bool) Option { return func(s *stage) { s.v4When = on } }

// deviceOutbound builds the device-bound hysteria dialer lazily and re-tries the build after a failure.
type deviceOutbound struct {
	name string
	mode outbounds.DirectOutboundMode

	mu  sync.Mutex
	out outbounds.PluggableOutbound
}

func (d *deviceOutbound) get() (outbounds.PluggableOutbound, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.out != nil {
		return d.out, nil
	}
	o, err := outbounds.NewDirectOutboundWithOptions(outbounds.DirectOutboundOptions{Mode: d.mode, DeviceName: d.name})
	if err != nil {
		return nil, fmt.Errorf("egress: device %s: %w", d.name, err)
	}
	d.out = o
	return o, nil
}

func (d *deviceOutbound) TCP(a *outbounds.AddrEx) (net.Conn, error) {
	o, err := d.get()
	if err != nil {
		return nil, err
	}
	return o.TCP(a)
}

func (d *deviceOutbound) CheckUDP(a *outbounds.AddrEx) error {
	o, err := d.get()
	if err != nil {
		return err
	}
	return o.CheckUDP(a)
}

func (d *deviceOutbound) UDP(a *outbounds.AddrEx) (outbounds.UDPConn, error) {
	o, err := d.get()
	if err != nil {
		return nil, err
	}
	return o.UDP(a)
}
