package tspuprobe

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/udpcheck"
)

// Walk is the TCP SYN TTL walk of the panel's node_trace: SYNs to one host:port with TTL 1..max, one at a time, saying for each
// hop whether a router answered, how long it took, or that the destination itself answered. It reuses the socket of the country
// probe (IP_TTL and IP_RECVERR on a plain TCP socket: no privilege, no raw socket) and none of its DPI logic. It never reports an
// address: the kernel's error queue holds the router's, and the walk does not read it.

// ErrNoRoute: the name did not resolve or the SYN could not be sent from this host.
var ErrNoRoute = errors.New("tspuprobe: no route")

// The kinds of a WalkHop.
const (
	HopICMP    = "icmp"    // a router answered (Time Exceeded, or Unreachable)
	HopSilent  = "silent"  // nothing within the wait
	HopOpen    = "open"    // the destination accepted the connection
	HopRefused = "refused" // the destination answered with a RST
)

const (
	// WalkMaxTTL is the highest TTL of a walk; WalkDefaultTTL is used for 0.
	WalkMaxTTL     = MaxMaxTTL
	WalkDefaultTTL = DefaultMaxTTL
	// WalkHopWait is how long one hop waits; WalkDeadline bounds the whole walk (32 hops fit).
	WalkHopWait  = 1500 * time.Millisecond
	WalkDeadline = 60 * time.Second
)

// WalkHop is the answer to one TTL.
type WalkHop struct {
	TTL  int
	Kind string
	RTT  time.Duration // until the answer; 0 for a silent hop
}

// Answered says that something answered the hop.
func (h WalkHop) Answered() bool { return h.Kind != HopSilent }

// Walk walks to host:port with the machine's sockets. reached says the destination answered (open or refused), which ends the
// walk. It returns ErrUnsupported where the host cannot, ErrBadParams, ErrUnsupportedHost, or a wrapped resolve or send error
// (the agent answers no_route).
func Walk(ctx context.Context, host string, port uint16, maxTTL int) (hops []WalkHop, reached bool, err error) {
	if !Supported() {
		return nil, false, ErrUnsupported
	}
	return walk(ctx, env{hop: synHop, resolve: udpcheck.Resolve}, host, port, maxTTL, WalkHopWait)
}

func walk(ctx context.Context, e env, host string, port uint16, maxTTL int, wait time.Duration) ([]WalkHop, bool, error) {
	host = strings.TrimSpace(host)
	if host == "" || len(host) > maxHostLen || port == 0 || maxTTL < 0 || maxTTL > WalkMaxTTL {
		return nil, false, ErrBadParams
	}
	if maxTTL == 0 {
		maxTTL = WalkDefaultTTL
	}
	ctx, cancel := context.WithTimeout(ctx, WalkDeadline)
	defer cancel()
	r := &runner{e: e, sem: make(chan struct{}, 1)}
	ip, err := r.address(ctx, host)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if errors.Is(err, ErrUnsupportedHost) {
			return nil, false, ErrUnsupportedHost
		}
		return nil, false, fmt.Errorf("%w: %v", ErrNoRoute, err)
	}
	if !ip.Is4() || unsupportedDestination(ip) {
		return nil, false, ErrUnsupportedHost
	}
	dst := netip.AddrPortFrom(ip, port)
	var hops []WalkHop
	for ttl := 1; ttl <= maxTTL; ttl++ {
		start := time.Now()
		h, err := r.probe(ctx, dst, ttl, wait)
		if err != nil {
			if ctx.Err() != nil {
				return hops, false, ctx.Err()
			}
			return hops, false, fmt.Errorf("%w: %v", ErrNoRoute, err)
		}
		hop := WalkHop{TTL: ttl, RTT: time.Since(start)}
		switch h {
		case hopICMP:
			hop.Kind = HopICMP
		case hopOpen:
			hop.Kind = HopOpen
		case hopRefused:
			hop.Kind = HopRefused
		default:
			hop.Kind, hop.RTT = HopSilent, 0
		}
		hops = append(hops, hop)
		if h == hopOpen || h == hopRefused {
			return hops, true, nil
		}
	}
	return hops, false, nil
}
