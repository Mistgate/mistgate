// Package tspuprobe finds out whether TCP SYNs to an address die at the national DPI box (the Russian TSPU), seen from the
// machine it runs on. It is a traceroute with a TTL limit and no privileges: one plain TCP socket per probe, the TTL and
// IP_RECVERR set, and a router's ICMP Time Exceeded read from the socket while the connection is in SYN_SENT (hop_linux.go).
//
// The method (a re-implementation of the idea described by porutchik/tspu-check and LowderPlay/cheburcheck; no code is shared):
//
//  1. dpi_hop. SYNs to an address that is certainly blocked in the country go out with TTL 1..max_ttl. The DPI box drops them
//     without an ICMP answer, so the highest TTL a router answered is the hop in front of the box.
//  2. Control. SYNs to a certainly-open address with TTL dpi_hop+1..dpi_hop+hop_limit must not all die, else the method
//     does not apply to this network (ErrControlFailed; that is no verdict about any target).
//  3. Targets, the same way: an established connection or an ICMP answer from behind the box is "open", a RST "refused",
//     silence at every hop "blocked". A blocked answer is checked again, alone and with more patience, because many
//     probes at once trip the ICMP rate limit of routers and look like a blocked address.
//
// Only the addresses of the request are probed. The package is portable; the probe itself is Linux only.
package tspuprobe

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/udpcheck"
)

const (
	VerdictBlocked = "blocked"
	VerdictOpen    = "open"
	VerdictRefused = "refused"
	// VerdictTimeout: no verdict was reached for that host:port (the deadline ran out, or the name did not resolve).
	VerdictTimeout = "timeout"

	// The caps of one run (agent.proto "COUNTRY PROBE").
	MaxTargets      = 8
	MaxPorts        = 4
	MaxPairs        = 16
	MaxHopLimit     = 8
	MaxMaxTTL       = 32
	DefaultHopLimit = 3
	DefaultMaxTTL   = 30
	TotalDeadline   = 120 * time.Second
	// ControlPort is the port of the control target and of the blocked reference.
	ControlPort = 443

	maxHostLen = 253
	// concurrent is how many sockets are in SYN_SENT at once.
	concurrent = 32
)

var (
	ErrBadParams       = errors.New("tspuprobe: bad parameters")
	ErrUnsupportedHost = errors.New("tspuprobe: unsupported destination")
	// ErrUnsupported: this host cannot run the probe (not Linux, or the socket options are refused).
	ErrUnsupported = errors.New("tspuprobe: unsupported on this host")
	// ErrNoDPIHop: the DPI box cannot be placed: no blocked reference, it was silent from the first hop, or it was reachable.
	// The error a run returns is a *NoDPIHopError that says which (errors.Is matches this value).
	ErrNoDPIHop = errors.New("tspuprobe: dpi hop not found")
	// ErrControlFailed: the control target did not get past the DPI box, the method does not apply on this network.
	ErrControlFailed = errors.New("tspuprobe: control failed")
	// ErrDeadline: the total deadline ran out before the DPI hop and the control were settled.
	ErrDeadline = errors.New("tspuprobe: deadline")
)

// Why the DPI box could not be placed (NoDPIHopError.Reason; the "reason" param of the agent's no_dpi_hop answer).
const (
	// ReasonRefOpen: the blocked reference answered with a SYN-ACK, so it passes on this path.
	ReasonRefOpen = "ref_open"
	// ReasonRefRefused: the reference answered with a RST: something resets it, and this method does not see that filter.
	ReasonRefRefused = "ref_refused"
	// ReasonNoHops: no router answered at any TTL, so the route is hidden.
	ReasonNoHops = "no_hops"
	// ReasonNoRef: the request had no blocked reference.
	ReasonNoRef = "no_ref"
)

// NoDPIHopError is ErrNoDPIHop with the reason the box could not be placed.
type NoDPIHopError struct{ Reason string }

func (e *NoDPIHopError) Error() string        { return ErrNoDPIHop.Error() + ": " + e.Reason }
func (e *NoDPIHopError) Is(target error) bool { return target == ErrNoDPIHop }

// Target is one host and its TCP ports.
type Target struct {
	Host  string
	Ports []uint16
}

// Request is one run. HopLimit and MaxTTL are 0 for their defaults.
type Request struct {
	Control    string // IPv4 literal, port 443
	BlockedRef string // IPv4 literal, port 443; "" = ErrNoDPIHop
	Targets    []Target
	HopLimit   int
	MaxTTL     int
}

// Result is a finished run. Verdicts are keyed by Key(host, port) with the host as the request spelled it.
type Result struct {
	DPIHop   int
	Verdicts map[string]string
}

// Key is the name of a host:port in Result.Verdicts.
func Key(host string, port uint16) string { return host + ":" + strconv.Itoa(int(port)) }

// hop is what one TTL-limited SYN got back.
type hop int

const (
	hopSilent  hop = iota // nothing within the wait
	hopICMP               // a router answered (Time Exceeded, or Unreachable)
	hopRefused            // a RST came back
	hopOpen               // the connection was established
)

// env is what a run needs of the machine; tests replace it.
type env struct {
	hop     func(ctx context.Context, dst netip.AddrPort, ttl int, wait time.Duration) (hop, error)
	resolve func(ctx context.Context, host string) (netip.Addr, error)
	// passWait is how long a probe of the first, parallel pass waits; recheckWait the alone pass, long enough for a
	// second SYN of the kernel's own retransmission.
	passWait, recheckWait time.Duration
	// stagger spaces the probes of the DPI hop search.
	stagger  time.Duration
	deadline time.Duration
}

// Run runs the probe with the machine's sockets. It returns ErrUnsupported where the host cannot.
func Run(ctx context.Context, req Request) (Result, error) {
	if !Supported() {
		return Result{}, ErrUnsupported
	}
	return run(ctx, req, env{hop: synHop, resolve: udpcheck.Resolve, passWait: 1100 * time.Millisecond,
		recheckWait: 2500 * time.Millisecond, stagger: 25 * time.Millisecond, deadline: TotalDeadline})
}

// Validate checks the request before any socket is opened. Hosts that are literals are checked here; a name is checked
// when it is resolved.
func Validate(req Request) error {
	if _, err := literal(req.Control); err != nil {
		return err
	}
	if req.BlockedRef != "" {
		if _, err := literal(req.BlockedRef); err != nil {
			return err
		}
	}
	if len(req.Targets) == 0 || len(req.Targets) > MaxTargets || req.HopLimit < 0 || req.HopLimit > MaxHopLimit ||
		req.MaxTTL < 0 || req.MaxTTL > MaxMaxTTL {
		return ErrBadParams
	}
	pairs := 0
	hosts := make(map[string]struct{}, len(req.Targets))
	for _, t := range req.Targets {
		host := strings.TrimSpace(t.Host)
		if host == "" || host != t.Host || len(host) > maxHostLen || len(t.Ports) == 0 || len(t.Ports) > MaxPorts {
			return ErrBadParams
		}
		if _, dup := hosts[host]; dup {
			return ErrBadParams
		}
		hosts[host] = struct{}{}
		if ip, err := netip.ParseAddr(host); err == nil {
			if ip = ip.Unmap(); !ip.Is4() || unsupportedDestination(ip) {
				return ErrUnsupportedHost
			}
		}
		seen := make(map[uint16]struct{}, len(t.Ports))
		for _, port := range t.Ports {
			if _, dup := seen[port]; dup || port == 0 {
				return ErrBadParams
			}
			seen[port] = struct{}{}
		}
		if pairs += len(t.Ports); pairs > MaxPairs {
			return ErrBadParams
		}
	}
	return nil
}

// literal parses an IPv4 address that may be probed.
func literal(s string) (netip.Addr, error) {
	ip, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, ErrBadParams
	}
	if ip = ip.Unmap(); !ip.Is4() || unsupportedDestination(ip) {
		return netip.Addr{}, ErrUnsupportedHost
	}
	return ip, nil
}

// unsupportedDestination refuses addresses that are no node: unspecified, multicast, broadcast and link-local unicast
// (169.254.0.0/16 holds cloud metadata services), as internal/tcpcheck does. Loopback stays: the tests run on it.
func unsupportedDestination(ip netip.Addr) bool {
	return !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() ||
		ip == netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// check is one host:port to settle. A dst that is not valid has no address (its name did not resolve).
type check struct {
	key     string
	dst     netip.AddrPort
	verdict string
}

// pending marks a check that was blocked in the first pass and waits for its second look.
const pending = "pending"

func run(ctx context.Context, req Request, e env) (Result, error) {
	if err := Validate(req); err != nil {
		return Result{}, err
	}
	if req.BlockedRef == "" {
		return Result{}, &NoDPIHopError{ReasonNoRef}
	}
	if req.HopLimit == 0 {
		req.HopLimit = DefaultHopLimit
	}
	if req.MaxTTL == 0 {
		req.MaxTTL = DefaultMaxTTL
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, e.deadline)
	defer cancel()
	r := &runner{e: e, sem: make(chan struct{}, concurrent)}
	fail := func(err error) (Result, error) {
		if parent.Err() != nil {
			return Result{}, parent.Err()
		}
		if ctx.Err() != nil {
			return Result{}, ErrDeadline
		}
		return Result{}, err
	}

	// every name is resolved, and judged, before the first SYN leaves
	checks := make([]*check, 0, MaxPairs)
	for _, t := range req.Targets {
		ip, err := r.address(ctx, t.Host)
		if errors.Is(err, ErrUnsupportedHost) {
			return Result{}, err
		}
		for _, port := range t.Ports {
			c := &check{key: Key(t.Host, port)}
			if err == nil {
				c.dst = netip.AddrPortFrom(ip, port)
			}
			checks = append(checks, c)
		}
	}

	ref, _ := literal(req.BlockedRef)
	dpi, err := r.findDPIHop(ctx, netip.AddrPortFrom(ref, ControlPort), req.MaxTTL)
	if err != nil {
		return fail(err)
	}
	res := Result{DPIHop: dpi, Verdicts: map[string]string{}}
	ttls := make([]int, 0, req.HopLimit)
	for t := dpi + 1; t <= dpi+req.HopLimit; t++ {
		ttls = append(ttls, t)
	}

	control, _ := literal(req.Control)
	ctl := &check{dst: netip.AddrPortFrom(control, ControlPort)}
	if err := r.settle(ctx, []*check{ctl}, ttls); err != nil {
		return fail(err)
	}
	if ctl.verdict == VerdictBlocked {
		return res, ErrControlFailed
	}

	var live []*check
	for _, c := range checks {
		if c.dst.IsValid() {
			live = append(live, c)
		}
	}
	// A deadline in the middle leaves the rest at "timeout"; the verdicts that were reached are still good.
	if err := r.settle(ctx, live, ttls); err != nil {
		if parent.Err() != nil {
			return Result{}, parent.Err()
		}
		if ctx.Err() == nil {
			return Result{}, err
		}
	}
	for _, c := range checks {
		res.Verdicts[c.key] = c.verdict
		if c.verdict == "" {
			res.Verdicts[c.key] = VerdictTimeout
		}
	}
	return res, nil
}

// runner carries a run's seams and the limit on sockets in flight.
type runner struct {
	e   env
	sem chan struct{}
}

// address resolves a target host: an IPv4 address or a name (IPv4 first). A name with no IPv4 address, or that does not
// resolve, has no address (the pair is "timeout"); one that resolves to an address nobody may probe is refused.
func (r *runner) address(ctx context.Context, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.Unmap(), nil
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ip, err := r.e.resolve(rctx, host)
	if err != nil {
		return netip.Addr{}, err
	}
	if ip = ip.Unmap(); !ip.Is4() {
		return netip.Addr{}, fmt.Errorf("%s: no IPv4 address", host)
	}
	if unsupportedDestination(ip) {
		return netip.Addr{}, fmt.Errorf("%w: %s", ErrUnsupportedHost, ip)
	}
	return ip, nil
}

// probe takes a place among the sockets in flight and sends one SYN.
func (r *runner) probe(ctx context.Context, dst netip.AddrPort, ttl int, wait time.Duration) (hop, error) {
	select {
	case r.sem <- struct{}{}:
	case <-ctx.Done():
		return hopSilent, ctx.Err()
	}
	defer func() { <-r.sem }()
	return r.e.hop(ctx, dst, ttl, wait)
}

// sweep sends one SYN per TTL at once and returns what each got back, in the order of ttls. gap spaces the starts.
func (r *runner) sweep(ctx context.Context, dst netip.AddrPort, ttls []int, wait, gap time.Duration) ([]hop, error) {
	out := make([]hop, len(ttls))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for i, ttl := range ttls {
		if i > 0 && gap > 0 {
			select {
			case <-time.After(gap):
			case <-ctx.Done():
			}
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h, err := r.probe(ctx, dst, ttl, wait)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && first == nil {
				first = err
			}
			out[i] = h
		}()
	}
	wg.Wait()
	if first == nil {
		first = ctx.Err()
	}
	return out, first
}

// verdictOf folds the answers of the TTLs of one address into its verdict.
func verdictOf(hops []hop) string {
	v := VerdictBlocked
	for _, h := range hops {
		switch h {
		case hopOpen:
			return VerdictOpen
		case hopRefused:
			v = VerdictRefused
		case hopICMP:
			if v == VerdictBlocked {
				v = VerdictOpen
			}
		}
	}
	return v
}

// passed is the error of a blocked reference that got an answer from its end: a SYN-ACK or a RST.
func passed(h hop) error {
	if h == hopOpen {
		return &NoDPIHopError{ReasonRefOpen}
	}
	return &NoDPIHopError{ReasonRefRefused}
}

// findDPIHop is step 1: the highest TTL a router answered for the blocked reference, with a look one hop further for an
// answer the first pass lost to a rate limit.
func (r *runner) findDPIHop(ctx context.Context, ref netip.AddrPort, maxTTL int) (int, error) {
	ttls := make([]int, maxTTL)
	for i := range ttls {
		ttls[i] = i + 1
	}
	hops, err := r.sweep(ctx, ref, ttls, r.e.passWait, r.e.stagger)
	if err != nil {
		return 0, err
	}
	top := 0
	for i, h := range hops {
		switch h {
		case hopOpen, hopRefused:
			return 0, passed(h) // the reference got through: there is no box in the way that this method can place
		case hopICMP:
			top = i + 1
		}
	}
	if top == 0 {
		return 0, &NoDPIHopError{ReasonNoHops}
	}
	for more := true; more; {
		more = false
		for ttl := top + 1; ttl <= min(top+2, maxTTL); ttl++ {
			h, err := r.probe(ctx, ref, ttl, r.e.recheckWait)
			if err != nil {
				return 0, err
			}
			switch h {
			case hopOpen, hopRefused:
				return 0, passed(h)
			case hopICMP:
				top, more = ttl, true
			}
		}
	}
	return top, nil
}

// settle sets the verdict of every check: all of them at once first, then every blocked one alone. A check that is not
// settled when an error ends it has no verdict ("").
func (r *runner) settle(ctx context.Context, checks []*check, ttls []int) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var first error
	for _, c := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hops, err := r.sweep(ctx, c.dst, ttls, r.e.passWait, 0)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = err
				}
				return
			}
			if v := verdictOf(hops); v != VerdictBlocked {
				c.verdict = v
			} else {
				c.verdict = pending
			}
		}()
	}
	wg.Wait()
	if first != nil {
		for _, c := range checks {
			if c.verdict == pending {
				c.verdict = ""
			}
		}
		return first
	}
	for _, c := range checks {
		if c.verdict != pending {
			continue
		}
		c.verdict = ""
		hops := make([]hop, 0, len(ttls))
		for _, ttl := range ttls {
			h, err := r.probe(ctx, c.dst, ttl, r.e.recheckWait)
			if err != nil {
				return err
			}
			if hops = append(hops, h); h == hopOpen || h == hopRefused {
				break
			}
		}
		c.verdict = verdictOf(hops)
	}
	return nil
}
