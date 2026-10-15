package tspuprobe

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Documentation addresses only (RFC 5737); nothing here opens a socket.
const (
	tControl = "198.51.100.10"
	tRef     = "198.51.100.66"
	tDE1     = "203.0.113.10"
	tNL1     = "203.0.113.20"
	tDE2     = "203.0.113.30"
)

// fakePath is a path of routers: the DPI box sits in front of hop dpi+1 and eats the SYNs to the blocked addresses; every
// address is length hops away, and answers there (open, or a RST for a closed port).
type fakePath struct {
	dpi     int
	length  int
	blocked map[string]bool
	closed  map[string]bool // "ip:port" that answers a RST
	// quiet: a router that answers the first pass (short wait) of this address never, the second always (a rate limit)
	quiet map[string]bool
	// lostAt: the first pass loses the answer of this TTL of the address
	lostAt map[string]int

	passWait time.Duration
	mu       sync.Mutex
	seen     map[string]int
}

func (p *fakePath) hop(_ context.Context, dst netip.AddrPort, ttl int, wait time.Duration) (hop, error) {
	p.mu.Lock()
	if p.seen == nil {
		p.seen = map[string]int{}
	}
	p.seen[dst.Addr().String()]++
	p.mu.Unlock()
	ip := dst.Addr().String()
	if p.blocked[ip] && ttl > p.dpi {
		return hopSilent, nil
	}
	if p.quiet[ip] && wait == p.passWait {
		return hopSilent, nil
	}
	if n, ok := p.lostAt[ip]; ok && n == ttl && wait == p.passWait {
		return hopSilent, nil
	}
	if ttl < p.length {
		return hopICMP, nil
	}
	if p.closed[dst.String()] {
		return hopRefused, nil
	}
	return hopOpen, nil
}

func newPath() *fakePath {
	return &fakePath{dpi: 2, length: 5, blocked: map[string]bool{tRef: true, tNL1: true},
		closed: map[string]bool{tDE1 + ":8443": true}, passWait: time.Millisecond}
}

func testEnv(p *fakePath) env {
	return env{hop: p.hop, resolve: func(_ context.Context, host string) (netip.Addr, error) { return netip.ParseAddr(host) },
		passWait: p.passWait, recheckWait: 2 * time.Millisecond, deadline: 5 * time.Second}
}

func baseRequest() Request {
	return Request{Control: tControl, BlockedRef: tRef, Targets: []Target{
		{Host: tDE1, Ports: []uint16{443, 8443}}, {Host: tNL1, Ports: []uint16{443}}, {Host: tDE2, Ports: []uint16{443, 80}}}}
}

func TestRunFindsTheHopAndTheVerdicts(t *testing.T) {
	p := newPath()
	res, err := run(context.Background(), baseRequest(), testEnv(p))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{tDE1 + ":443": VerdictOpen, tDE1 + ":8443": VerdictRefused, tNL1 + ":443": VerdictBlocked,
		tDE2 + ":443": VerdictOpen, tDE2 + ":80": VerdictOpen}
	if res.DPIHop != 2 {
		t.Fatalf("dpi_hop = %d, want 2", res.DPIHop)
	}
	for k, v := range want {
		if res.Verdicts[k] != v {
			t.Errorf("%s = %q, want %q", k, res.Verdicts[k], v)
		}
	}
	if len(res.Verdicts) != len(want) {
		t.Errorf("verdicts = %v", res.Verdicts)
	}
}

func TestRunHopLimitCapsTheWindow(t *testing.T) {
	// the box is at 2; with hop_limit 1 only TTL 3 is tried, which is not the end of the path: an open port and a closed
	// one both look open (a RST needs the probe to reach the node)
	p := newPath()
	req := baseRequest()
	req.HopLimit = 1
	res, err := run(context.Background(), req, testEnv(p))
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdicts[tDE1+":8443"] != VerdictOpen || res.Verdicts[tNL1+":443"] != VerdictBlocked {
		t.Fatalf("verdicts = %v", res.Verdicts)
	}
}

func TestRunOnlyProbesTheAddressesItWasGiven(t *testing.T) {
	p := newPath()
	if _, err := run(context.Background(), baseRequest(), testEnv(p)); err != nil {
		t.Fatal(err)
	}
	var got []string
	for ip := range p.seen {
		got = append(got, ip)
	}
	sort.Strings(got)
	want := []string{tControl, tRef, tDE1, tNL1, tDE2}
	sort.Strings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("probed %v, want exactly %v", got, want)
	}
}

func TestRunLooksAgainAtABlockedAnswer(t *testing.T) {
	// a router that answers only the patient second look must not turn an open node into a blocked one
	p := newPath()
	p.quiet = map[string]bool{tDE1: true, tDE2: true}
	res, err := run(context.Background(), baseRequest(), testEnv(p))
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdicts[tDE1+":443"] != VerdictOpen || res.Verdicts[tDE2+":80"] != VerdictOpen || res.Verdicts[tNL1+":443"] != VerdictBlocked {
		t.Fatalf("verdicts = %v", res.Verdicts)
	}
	if p.seen[tDE1] <= 6 { // 2 ports x 3 TTLs in the first pass, then the alone pass
		t.Fatalf("the blocked-looking address was probed %d times, no second look", p.seen[tDE1])
	}
}

func TestRunDPIHopLooksOneHopFurther(t *testing.T) {
	// the first pass loses the answer of hop 3 to a rate limit: the confirm step finds it
	p := newPath()
	p.dpi = 3
	p.lostAt = map[string]int{tRef: 3}
	res, err := run(context.Background(), baseRequest(), testEnv(p))
	if err != nil {
		t.Fatal(err)
	}
	if res.DPIHop != 3 {
		t.Fatalf("dpi_hop = %d, want 3", res.DPIHop)
	}
}

func TestRunControlFailed(t *testing.T) {
	p := newPath()
	p.blocked[tControl] = true
	res, err := run(context.Background(), baseRequest(), testEnv(p))
	if !errors.Is(err, ErrControlFailed) || res.DPIHop != 2 || len(res.Verdicts) != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if p.seen[tDE1] != 0 {
		t.Fatal("a target was probed after the control failed")
	}
}

func TestRunNoDPIHop(t *testing.T) {
	for name, tc := range map[string]struct {
		ref    string
		path   func(*fakePath)
		reason string
	}{
		"no reference":           {ref: "", reason: ReasonNoRef},
		"the reference connects": {ref: tDE2, reason: ReasonRefOpen},
		"silent from hop one":    {ref: tRef, path: func(p *fakePath) { p.dpi = 0 }, reason: ReasonNoHops},
		"the reference is refused": {ref: tDE1, reason: ReasonRefRefused, path: func(p *fakePath) {
			p.closed = map[string]bool{tDE1 + ":443": true}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPath()
			if tc.path != nil {
				tc.path(p)
			}
			req := baseRequest()
			req.BlockedRef = tc.ref
			_, err := run(context.Background(), req, testEnv(p))
			var noHop *NoDPIHopError
			if !errors.Is(err, ErrNoDPIHop) || !errors.As(err, &noHop) || noHop.Reason != tc.reason {
				t.Fatalf("err = %v, want ErrNoDPIHop with reason %q", err, tc.reason)
			}
		})
	}
}

func TestRunStopsOnALocalError(t *testing.T) {
	boom := errors.New("local error: operation not permitted")
	e := testEnv(newPath())
	e.hop = func(context.Context, netip.AddrPort, int, time.Duration) (hop, error) { return hopSilent, boom }
	if _, err := run(context.Background(), baseRequest(), e); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the local error, not a verdict", err)
	}
}

func TestRunNames(t *testing.T) {
	p := newPath()
	e := testEnv(p)
	e.resolve = func(_ context.Context, host string) (netip.Addr, error) {
		switch host {
		case "de1.example.net":
			return netip.MustParseAddr(tDE1), nil
		case "v6only.example.net":
			return netip.MustParseAddr("2001:db8::1"), nil
		case "meta.example.net":
			return netip.MustParseAddr("169.254.169.254"), nil
		}
		return netip.Addr{}, errors.New("no such host")
	}
	req := baseRequest()
	req.Targets = []Target{{Host: "de1.example.net", Ports: []uint16{443}}, {Host: "v6only.example.net", Ports: []uint16{443}},
		{Host: "gone.example.net", Ports: []uint16{443}}}
	res, err := run(context.Background(), req, e)
	if err != nil {
		t.Fatal(err)
	}
	if res.Verdicts["de1.example.net:443"] != VerdictOpen || res.Verdicts["v6only.example.net:443"] != VerdictTimeout ||
		res.Verdicts["gone.example.net:443"] != VerdictTimeout {
		t.Fatalf("verdicts = %v", res.Verdicts)
	}

	// a name that points at a forbidden address stops the whole run before the first SYN
	p = newPath()
	e.hop = p.hop
	req.Targets = append(req.Targets, Target{Host: "meta.example.net", Ports: []uint16{80}})
	if _, err := run(context.Background(), req, e); !errors.Is(err, ErrUnsupportedHost) {
		t.Fatalf("err = %v, want ErrUnsupportedHost", err)
	}
	if len(p.seen) != 0 {
		t.Fatalf("a SYN left before the names were judged: %v", p.seen)
	}
}

func TestRunDeadline(t *testing.T) {
	hold := func(ctx context.Context, _ netip.AddrPort, _ int, _ time.Duration) (hop, error) {
		<-ctx.Done()
		return hopSilent, ctx.Err()
	}
	t.Run("while placing the box", func(t *testing.T) {
		e := testEnv(newPath())
		e.hop, e.deadline = hold, 50*time.Millisecond
		if _, err := run(context.Background(), baseRequest(), e); !errors.Is(err, ErrDeadline) {
			t.Fatalf("err = %v, want ErrDeadline", err)
		}
	})
	t.Run("while on the targets", func(t *testing.T) {
		p := newPath()
		e := testEnv(p)
		e.deadline = 300 * time.Millisecond
		e.hop = func(ctx context.Context, dst netip.AddrPort, ttl int, wait time.Duration) (hop, error) {
			if dst.Addr().String() == tDE2 {
				<-ctx.Done()
				return hopSilent, ctx.Err()
			}
			return p.hop(ctx, dst, ttl, wait)
		}
		res, err := run(context.Background(), baseRequest(), e)
		if err != nil {
			t.Fatal(err)
		}
		if res.Verdicts[tDE1+":443"] != VerdictOpen || res.Verdicts[tDE2+":443"] != VerdictTimeout || res.Verdicts[tDE2+":80"] != VerdictTimeout {
			t.Fatalf("verdicts = %v: the ones that were reached stay, the rest is timeout", res.Verdicts)
		}
	})
	t.Run("the caller gives up", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		e := testEnv(newPath())
		e.hop = func(ctx context.Context, _ netip.AddrPort, _ int, _ time.Duration) (hop, error) {
			cancel()
			<-ctx.Done()
			return hopSilent, ctx.Err()
		}
		if _, err := run(ctx, baseRequest(), e); !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

func TestRunSocketsInFlightAreCapped(t *testing.T) {
	var now, peak atomic.Int32
	p := newPath()
	e := testEnv(p)
	e.hop = func(ctx context.Context, dst netip.AddrPort, ttl int, wait time.Duration) (hop, error) {
		n := now.Add(1)
		defer now.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		return p.hop(ctx, dst, ttl, wait)
	}
	req := Request{Control: tControl, BlockedRef: tRef, HopLimit: 8}
	for i := range MaxTargets {
		req.Targets = append(req.Targets, Target{Host: fmt.Sprintf("203.0.113.%d", 100+i), Ports: []uint16{80, 443}})
	}
	if _, err := run(context.Background(), req, e); err != nil {
		t.Fatal(err)
	}
	if peak.Load() > concurrent {
		t.Fatalf("%d sockets at once, cap is %d", peak.Load(), concurrent)
	}
}

func TestValidate(t *testing.T) {
	ok := baseRequest()
	if err := Validate(ok); err != nil {
		t.Fatal(err)
	}
	tooMany := baseRequest()
	tooMany.Targets = nil
	for i := range MaxTargets + 1 {
		tooMany.Targets = append(tooMany.Targets, Target{Host: fmt.Sprintf("203.0.113.%d", 100+i), Ports: []uint16{443}})
	}
	tooManyPairs := baseRequest()
	tooManyPairs.Targets = nil
	for i := range 5 {
		tooManyPairs.Targets = append(tooManyPairs.Targets, Target{Host: fmt.Sprintf("203.0.113.%d", 100+i), Ports: []uint16{1, 2, 3, 4}})
	}
	with := func(f func(*Request)) Request { r := baseRequest(); f(&r); return r }
	for name, tc := range map[string]struct {
		req  Request
		want error
	}{
		"no control":            {with(func(r *Request) { r.Control = "" }), ErrBadParams},
		"a name as the control": {with(func(r *Request) { r.Control = "ya.ru" }), ErrBadParams},
		"control unspecified":   {with(func(r *Request) { r.Control = "0.0.0.0" }), ErrUnsupportedHost},
		"control ipv6":          {with(func(r *Request) { r.Control = "2001:db8::1" }), ErrUnsupportedHost},
		"ref multicast":         {with(func(r *Request) { r.BlockedRef = "224.0.0.1" }), ErrUnsupportedHost},
		"no targets":            {with(func(r *Request) { r.Targets = nil }), ErrBadParams},
		"nine targets":          {tooMany, ErrBadParams},
		"seventeen pairs":       {tooManyPairs, ErrBadParams},
		"empty host":            {with(func(r *Request) { r.Targets[0].Host = "" }), ErrBadParams},
		"padded host":           {with(func(r *Request) { r.Targets[0].Host = " " + tDE1 }), ErrBadParams},
		"same host twice":       {with(func(r *Request) { r.Targets[1].Host = tDE1 }), ErrBadParams},
		"unspecified":           {with(func(r *Request) { r.Targets[0].Host = "0.0.0.0" }), ErrUnsupportedHost},
		"multicast":             {with(func(r *Request) { r.Targets[0].Host = "239.1.1.1" }), ErrUnsupportedHost},
		"broadcast":             {with(func(r *Request) { r.Targets[0].Host = "255.255.255.255" }), ErrUnsupportedHost},
		"link local":            {with(func(r *Request) { r.Targets[0].Host = "169.254.169.254" }), ErrUnsupportedHost},
		"ipv6 literal":          {with(func(r *Request) { r.Targets[0].Host = "2001:db8::10" }), ErrUnsupportedHost},
		"port zero":             {with(func(r *Request) { r.Targets[0].Ports = []uint16{0} }), ErrBadParams},
		"no ports":              {with(func(r *Request) { r.Targets[0].Ports = nil }), ErrBadParams},
		"five ports":            {with(func(r *Request) { r.Targets[0].Ports = []uint16{1, 2, 3, 4, 5} }), ErrBadParams},
		"duplicate port":        {with(func(r *Request) { r.Targets[0].Ports = []uint16{443, 443} }), ErrBadParams},
		"hop limit 9":           {with(func(r *Request) { r.HopLimit = 9 }), ErrBadParams},
		"max ttl 33":            {with(func(r *Request) { r.MaxTTL = 33 }), ErrBadParams},
		"negative hop limit":    {with(func(r *Request) { r.HopLimit = -1 }), ErrBadParams},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Validate(tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// loopback is allowed: the panel may run on the node's own host
	loop := baseRequest()
	loop.Targets[0].Host = "127.0.0.1"
	if err := Validate(loop); err != nil {
		t.Fatalf("loopback: %v", err)
	}
}

func TestVerdictOf(t *testing.T) {
	for name, tc := range map[string]struct {
		hops []hop
		want string
	}{
		"silence":                     {[]hop{hopSilent, hopSilent, hopSilent}, VerdictBlocked},
		"an answer of a router":       {[]hop{hopICMP, hopSilent}, VerdictOpen},
		"a RST":                       {[]hop{hopSilent, hopRefused}, VerdictRefused},
		"routers then a RST":          {[]hop{hopICMP, hopICMP, hopRefused}, VerdictRefused},
		"routers then the connection": {[]hop{hopICMP, hopOpen}, VerdictOpen},
		"a connection beats a RST":    {[]hop{hopRefused, hopOpen}, VerdictOpen},
	} {
		if got := verdictOf(tc.hops); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
}
