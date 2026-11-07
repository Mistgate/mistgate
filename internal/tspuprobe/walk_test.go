package tspuprobe

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

func walkEnv(path *fakePath) env {
	return env{hop: path.hop, resolve: func(_ context.Context, host string) (netip.Addr, error) {
		if host == "de1.example.net" {
			return netip.MustParseAddr(tDE1), nil
		}
		return netip.Addr{}, errors.New("no such host")
	}}
}

func TestWalkStopsAtTheDestinationAndNamesNoAddress(t *testing.T) {
	path := &fakePath{length: 4}
	hops, reached, err := walk(context.Background(), walkEnv(path), tDE1, 443, 30, time.Second)
	if err != nil || !reached || len(hops) != 4 {
		t.Fatalf("walk = %+v, %v, %v", hops, reached, err)
	}
	for i, h := range hops[:3] {
		if h.TTL != i+1 || h.Kind != HopICMP || !h.Answered() {
			t.Errorf("hop %d = %+v", i+1, h)
		}
	}
	if hops[3].Kind != HopOpen || hops[3].TTL != 4 {
		t.Errorf("the destination hop = %+v", hops[3])
	}
	// a closed port answers with a RST, which ends the walk as well
	path = &fakePath{length: 3, closed: map[string]bool{tDE1 + ":443": true}}
	hops, reached, err = walk(context.Background(), walkEnv(path), tDE1, 443, 30, time.Second)
	if err != nil || !reached || hops[len(hops)-1].Kind != HopRefused {
		t.Fatalf("closed port: %+v, %v, %v", hops, reached, err)
	}
}

func TestWalkSilentHopsAndTheTTLCap(t *testing.T) {
	// the DPI box of the fake path swallows everything behind hop 2: the walk is silent to the end and does not reach
	path := &fakePath{dpi: 2, length: 9, blocked: map[string]bool{tDE1: true}}
	hops, reached, err := walk(context.Background(), walkEnv(path), tDE1, 443, 6, time.Second)
	if err != nil || reached || len(hops) != 6 {
		t.Fatalf("walk = %+v, %v, %v", hops, reached, err)
	}
	for _, h := range hops[2:] {
		if h.Kind != HopSilent || h.Answered() || h.RTT != 0 {
			t.Errorf("hop %d = %+v, want silent with no time", h.TTL, h)
		}
	}
	if hops, _, _ := walk(context.Background(), walkEnv(&fakePath{length: 40}), tDE1, 443, 0, time.Second); len(hops) != WalkDefaultTTL {
		t.Errorf("the default cap gave %d hops, want %d", len(hops), WalkDefaultTTL)
	}
}

func TestWalkRefusesWhatItMayNotProbe(t *testing.T) {
	e := walkEnv(&fakePath{length: 3})
	for name, tc := range map[string]struct {
		host string
		port uint16
		ttl  int
		want error
	}{
		"empty host":   {"", 443, 10, ErrBadParams},
		"port zero":    {tDE1, 0, 10, ErrBadParams},
		"ttl too high": {tDE1, 443, WalkMaxTTL + 1, ErrBadParams},
		"negative ttl": {tDE1, 443, -1, ErrBadParams},
		"unspecified":  {"0.0.0.0", 443, 10, ErrUnsupportedHost},
		"multicast":    {"224.0.0.1", 443, 10, ErrUnsupportedHost},
		"link local":   {"169.254.169.254", 443, 10, ErrUnsupportedHost},
		"ipv6 literal": {"2001:db8::1", 443, 10, ErrUnsupportedHost},
		"no such name": {"nope.example.net", 443, 10, ErrNoRoute},
	} {
		if hops, _, err := walk(context.Background(), e, tc.host, tc.port, tc.ttl, time.Second); !errors.Is(err, tc.want) || len(hops) != 0 {
			t.Errorf("%s: %v, %d hops; want %v", name, err, len(hops), tc.want)
		}
	}
	if _, _, err := walk(context.Background(), e, "de1.example.net", 443, 10, time.Second); err != nil {
		t.Errorf("a name that resolves: %v", err)
	}
}

func TestWalkStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	path := &fakePath{length: 30}
	e := walkEnv(path)
	inner := e.hop
	n := 0
	e.hop = func(c context.Context, dst netip.AddrPort, ttl int, wait time.Duration) (hop, error) {
		if n++; n == 3 {
			cancel()
		}
		return inner(c, dst, ttl, wait)
	}
	hops, _, err := walk(ctx, e, tDE1, 443, 30, time.Second)
	if !errors.Is(err, context.Canceled) || len(hops) > 3 {
		t.Fatalf("walk = %d hops, %v", len(hops), err)
	}
}
