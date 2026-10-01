// Package egress is the node's direct outbound for Hysteria2 (engine.Egress).
//
// Two jobs the hysteria defaults do not do:
//
//   - Domains are resolved with the node's own resolver list (a RU node must ask Yandex DNS or
//     gosuslugi breaks). Together with the sniffing RequestHook this means the client's
//     own DNS answer is not trusted: a sniffed domain replaces the IP the client sent and the node
//     resolves it. The default outbound would use the system resolver, and its UDP path would call the
//     system resolver once per datagram.
//   - Destinations that are not public unicast (loopback, RFC 1918, CGNAT, link-local, cloud metadata,
//     multicast) and every address of this host's own interfaces (v4 and v6, its public IP included:
//     Linux would route that through lo, straight to sshd and the exporters) are refused, so an
//     authenticated VPN user cannot reach the node's own services or its private network.
//     AllowPrivate lifts this for tests and dev.
//
// WARP is this same egress bound to a device: New(dns, WithDevice("mgwarp")), see device.go. Nothing here is
// WARP-specific.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/apernet/hysteria/extras/v2/outbounds"

	"github.com/mistgate/mistgate/internal/node/engine"
)

const (
	perServerTimeout = 3 * time.Second // the whole budget for one resolver
	attemptTimeout   = time.Second     // one try: a lost UDP datagram costs this, not the whole budget
	cacheTTL         = 30 * time.Second
	cacheMax         = 4096
	selfTTL          = 15 * time.Second
)

// ErrBlocked is returned for destinations that are not public unicast addresses.
var ErrBlocked = errors.New("egress: destination address not allowed")

// BindIP4 is the local IPv4 address of the outgoing UDP sockets; nil, the production value, lets the system choose
// (all interfaces). Tests set it to loopback in TestMain: a test binary with a wildcard UDP socket makes Windows ask for a
// firewall rule on each run.
var BindIP4 net.IP

func directOutbound(mode outbounds.DirectOutboundMode) outbounds.PluggableOutbound {
	if BindIP4 != nil {
		if o, err := outbounds.NewDirectOutboundBindToIPs(mode, BindIP4, nil); err == nil {
			return o
		}
	}
	return outbounds.NewDirectOutboundSimple(mode)
}

// Direct dials the internet from this host. Safe for concurrent use.
type Direct struct {
	ad *outbounds.PluggableOutboundAdapter
}

var _ engine.Egress = (*Direct)(nil)

// Option tunes New.
type Option func(*stage)

// AllowPrivate permits loopback, private and link-local destinations and the node's own addresses (tests, dev).
func AllowPrivate() Option { return func(s *stage) { s.allowPrivate = true } }

// New builds the direct egress. resolvers is called per lookup (engine.Env.DNS), so a node settings
// change needs no restart; "host" or "host:port" entries, tried in order; nil or empty = system resolver.
func New(resolvers func() []string, opts ...Option) *Direct {
	s := &stage{
		// IPv4 first, IPv6 only for a name without an A record. "auto" races both, so the exit flips between the two per
		// connection, and a hoster's IPv6 range often geolocates to another country (live: an EE node exiting as FI).
		next: directOutbound(outbounds.DirectOutboundMode46),
		res:  &resolver{servers: resolvers, cache: map[string]cacheEntry{}},
		self: &selfAddrs{list: interfaceAddrs, now: time.Now},
	}
	for _, o := range opts {
		o(s)
	}
	if s.device != "" || s.ipv4Only {
		mode := outbounds.DirectOutboundModeAuto
		if s.ipv4Only {
			mode = outbounds.DirectOutboundMode4
		}
		if s.device != "" {
			s.next = &deviceOutbound{name: s.device, mode: mode}
		} else {
			s.next = directOutbound(mode)
		}
	}
	return &Direct{ad: &outbounds.PluggableOutboundAdapter{PluggableOutbound: s}}
}

func (d *Direct) TCP(addr string) (net.Conn, error) { return d.ad.TCP(addr) }

func (d *Direct) CheckUDP(addr string) error { return d.ad.CheckUDP(addr) }

func (d *Direct) UDP(addr string) (engine.EgressUDP, error) {
	c, err := d.ad.UDP(addr)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// stage resolves and vets the destination, then hands it to the real dialer with ResolveInfo filled in
// (so the dialer never falls back to the system resolver).
type stage struct {
	next         outbounds.PluggableOutbound
	res          *resolver
	self         *selfAddrs
	allowPrivate bool
	device       string // WithDevice
	ipv4Only     bool   // IPv4Only
}

func (s *stage) TCP(a *outbounds.AddrEx) (net.Conn, error) {
	if err := s.prepare(a); err != nil {
		return nil, err
	}
	return s.next.TCP(a)
}

func (s *stage) CheckUDP(a *outbounds.AddrEx) error {
	if err := s.prepare(a); err != nil {
		return err
	}
	return s.next.CheckUDP(a)
}

func (s *stage) UDP(a *outbounds.AddrEx) (outbounds.UDPConn, error) {
	if err := s.prepare(a); err != nil {
		return nil, err
	}
	c, err := s.next.UDP(a)
	if err != nil {
		return nil, err
	}
	return &udpConn{UDPConn: c, s: s}, nil
}

// udpConn re-checks every outgoing datagram: one UDP session may be told a new destination, and the
// dialer resolves domains itself when ResolveInfo is empty.
type udpConn struct {
	outbounds.UDPConn
	s *stage
}

func (c *udpConn) WriteTo(b []byte, a *outbounds.AddrEx) (int, error) {
	if err := c.s.prepare(a); err != nil {
		return 0, err
	}
	return c.UDPConn.WriteTo(b, a)
}

func (s *stage) prepare(a *outbounds.AddrEx) error {
	var v4, v6 net.IP
	if a.ResolveInfo != nil {
		v4, v6 = a.ResolveInfo.IPv4, a.ResolveInfo.IPv6
	} else if ip, err := netip.ParseAddr(a.Host); err == nil {
		ip = ip.Unmap()
		if ip.Is4() {
			v4 = net.IP(ip.AsSlice())
		} else {
			v6 = net.IP(ip.AsSlice())
		}
	} else {
		var err error
		if v4, v6, err = s.res.lookup(a.Host); err != nil {
			return fmt.Errorf("resolve %s: %w", a.Host, err)
		}
	}
	if !s.allowPrivate {
		v4, v6 = s.self.vet(v4), s.self.vet(v6)
		if v4 == nil && v6 == nil {
			return ErrBlocked
		}
	}
	a.ResolveInfo = &outbounds.ResolveInfo{IPv4: v4, IPv6: v6}
	return nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// vet returns ip when it is public unicast, else nil.
// Documentation/benchmark ranges (192.0.2.0/24, 198.18.0.0/15) and NAT64 are not filtered.
func vet(ip net.IP) net.IP {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return nil
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a) {
		return nil
	}
	return ip
}

// selfAddrs is the set of addresses assigned to this host's interfaces, re-read when it is older than
// selfTTL (a network change, a floating IP, a DHCP renewal show up within seconds). Safe for concurrent use.
// Lazy refresh on use instead of a netlink subscription; the ceiling is selfTTL of staleness.
type selfAddrs struct {
	list func() ([]netip.Addr, error)
	now  func() time.Time

	mu   sync.Mutex
	set  map[netip.Addr]struct{}
	read time.Time
}

func interfaceAddrs() ([]netip.Addr, error) {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := make([]netip.Addr, 0, len(as))
	for _, a := range as {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ia, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, ia.Unmap().WithZone(""))
		}
	}
	return out, nil
}

// has reports whether ip is one of the host's own addresses. If the interface list cannot be read and
// there is no earlier copy, it answers true: failing closed is safer than letting the node reach itself.
func (s *selfAddrs) has(ip netip.Addr) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now := s.now(); s.set == nil || now.Sub(s.read) > selfTTL {
		if l, err := s.list(); err == nil {
			s.set = make(map[netip.Addr]struct{}, len(l))
			for _, a := range l {
				s.set[a] = struct{}{}
			}
			s.read = now
		} else if s.set == nil {
			return true
		} else {
			s.read = now.Add(-selfTTL + time.Second) // keep the old copy, retry in a second
		}
	}
	_, own := s.set[ip.Unmap().WithZone("")]
	return own
}

// vet is the package-level vet plus the own-address check.
func (s *selfAddrs) vet(ip net.IP) net.IP {
	ip = vet(ip)
	if a, ok := netip.AddrFromSlice(ip); ok && s.has(a) {
		return nil
	}
	return ip
}

// resolver asks the configured servers one after another. Go's own resolver does the DNS work (CNAME
// chains, retries, A+AAAA in parallel); only its transport is pointed at our server.
type resolver struct {
	servers func() []string

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	v4, v6 net.IP
	exp    time.Time
}

func (r *resolver) lookup(host string) (v4, v6 net.IP, err error) {
	key := strings.ToLower(strings.TrimSuffix(host, "."))
	if key == "" {
		return nil, nil, errors.New("empty host")
	}
	r.mu.Lock()
	e, ok := r.cache[key]
	r.mu.Unlock()
	if ok && time.Now().Before(e.exp) {
		return e.v4, e.v6, nil
	}

	var ips []net.IPAddr
	var servers []string
	if r.servers != nil {
		servers = r.servers()
	}
	if len(servers) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*perServerTimeout)
		defer cancel()
		ips, err = net.DefaultResolver.LookupIPAddr(ctx, key)
	} else {
		ips, err = lookupVia(servers, key)
	}
	if err != nil {
		return nil, nil, err
	}
	for _, ia := range ips {
		a, ok := netip.AddrFromSlice(ia.IP)
		if !ok {
			continue
		}
		a = a.Unmap()
		if a.Is4() && v4 == nil {
			v4 = net.IP(a.AsSlice())
		} else if a.Is6() && v6 == nil {
			v6 = net.IP(a.AsSlice())
		}
	}
	if v4 == nil && v6 == nil {
		return nil, nil, errors.New("no address")
	}

	r.mu.Lock()
	// Fixed 30 s TTL ignoring the DNS TTL, wholesale reset when full; it only has to absorb the
	// burst of connections a browser opens to one name.
	if len(r.cache) >= cacheMax {
		r.cache = map[string]cacheEntry{}
	}
	r.cache[key] = cacheEntry{v4, v6, time.Now().Add(cacheTTL)}
	r.mu.Unlock()
	return v4, v6, nil
}

// lookupVia tries the servers in order. A definitive "no such host" stops the search; timeouts and
// server failures move on to the next server.
func lookupVia(servers []string, host string) ([]net.IPAddr, error) {
	var lastErr error = errors.New("no usable resolver")
	for _, s := range servers {
		hp := s
		if _, _, err := net.SplitHostPort(s); err != nil {
			hp = net.JoinHostPort(s, "53")
		}
		h, _, err := net.SplitHostPort(hp)
		if err != nil {
			continue
		}
		if _, err := netip.ParseAddr(h); err != nil {
			continue // resolvers must be IP literals
		}
		res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, hp)
		}}
		// Go's resolver does not resend inside a short context, so one lost datagram would burn the whole
		// budget: try again every attemptTimeout until perServerTimeout is used up. Only a timeout is
		// retried; an answer (even a failure) or a refusal is final for this server.
		var ips []net.IPAddr
		for i := 0; i < int(perServerTimeout/attemptTimeout); i++ {
			ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
			ips, err = res.LookupIPAddr(ctx, host+".")
			cancel()
			if de := new(net.DNSError); err == nil || !errors.As(err, &de) || !de.IsTimeout {
				break
			}
		}
		if err == nil && len(ips) > 0 {
			return ips, nil
		}
		if err == nil {
			err = errors.New("no address")
		}
		lastErr = err
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return nil, err
		}
	}
	return nil, lastErr
}
