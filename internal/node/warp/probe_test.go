package warp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestHTTPProbeDefaultBudgetAllowsSlowResponse(t *testing.T) {
	const responseDelay = 14 * time.Second
	if probeDNSBudget != 10*time.Second {
		t.Fatalf("default DNS budget = %s, want 10s", probeDNSBudget)
	}
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		timer := time.NewTimer(responseDelay)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = w.Write([]byte("warp=on\ncolo=FRA\n"))
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	p := &httpProber{
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
		aURL: server.URL,
	}

	started := time.Now()
	flag, colo, err := p.A(context.Background())
	if err != nil {
		t.Fatalf("slow probe failed after %s: %v", time.Since(started), err)
	}
	if flag != "on" || colo != "FRA" {
		t.Fatalf("probe result = (%q, %q), want (on, FRA)", flag, colo)
	}
	if elapsed := time.Since(started); elapsed < responseDelay || elapsed >= probeTotalTimeout {
		t.Fatalf("probe elapsed %s, want at least %s and below the %s budget", elapsed, responseDelay, probeTotalTimeout)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("slow probe handler did not finish")
	}
}

func TestHTTPProbeCancelsOverBudgetResponse(t *testing.T) {
	handlerDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		<-r.Context().Done()
	}))
	defer server.Close()

	p := &httpProber{
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
		aURL:    server.URL,
		timeout: 120 * time.Millisecond,
	}
	started := time.Now()
	_, _, err := p.A(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("over-budget probe error = %v, want deadline exceeded", err)
	}
	if got := probeFailureCode(err, ""); got != "timeout" {
		t.Fatalf("failure code = %q, want timeout", got)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("probe returned after %s, want prompt cancellation at its deadline", elapsed)
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("HTTP handler remained blocked after probe cancellation")
	}
}

func TestHTTPProbeCancelsDialWithRequestContext(t *testing.T) {
	entered := make(chan context.Context, 1)
	p := &httpProber{
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if network != "tcp" || addr != "probe.example:80" {
				t.Errorf("dial target = %s %s", network, addr)
			}
			entered <- ctx
			<-ctx.Done()
			return nil, ctx.Err()
		},
		timeout: 80 * time.Millisecond,
	}

	started := time.Now()
	_, _, err := p.get(context.Background(), "http://probe.example/check")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe error = %v, want request deadline", err)
	}
	if got := probeFailureCode(err, ""); got != "timeout" {
		t.Fatalf("failure code = %q, want timeout", got)
	}
	if time.Since(started) > time.Second {
		t.Fatalf("probe exceeded its deadline by too much: %s", time.Since(started))
	}
	select {
	case ctx := <-entered:
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("dial context error = %v, want request deadline", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP probe did not call its dialer")
	}
}

func TestLookupProbeIPsRetriesResolversWithinHTTPBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	var tried []string
	start := time.Now()
	ips, err := lookupProbeIPs(ctx, "probe.example", []string{"192.0.2.53", "[2001:db8::53]:5353"},
		func(ctx context.Context, resolver, _ string) ([]netip.Addr, error) {
			tried = append(tried, resolver)
			if len(tried) == 1 {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return []netip.Addr{netip.MustParseAddr("203.0.113.8")}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if len(tried) != 2 || tried[0] != "192.0.2.53:53" || tried[1] != "[2001:db8::53]:5353" {
		t.Fatalf("resolvers tried = %v", tried)
	}
	if len(ips) != 1 || ips[0] != netip.MustParseAddr("203.0.113.8") {
		t.Fatalf("addresses = %v", ips)
	}
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("resolver fallback took %s, exceeding the HTTP check's DNS budget", elapsed)
	}
}

func TestLookupProbeIPsStopsRetriesWhenRequestIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls int
	_, err := lookupProbeIPs(ctx, "probe.example", []string{"192.0.2.53", "192.0.2.54"},
		func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
			calls++
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lookup error = %v, want canceled", err)
	}
	if calls != 1 {
		t.Fatalf("tried %d resolvers after request cancellation", calls)
	}
}

func TestProbeCandidatesRespectAddressFamilies(t *testing.T) {
	ips := []netip.Addr{
		netip.MustParseAddr("2001:db8::8"),
		netip.MustParseAddr("203.0.113.8"),
		netip.MustParseAddr("203.0.113.9"),
	}
	if got := probeCandidates(ips, "tcp", false); len(got) != 1 || got[0] != netip.MustParseAddr("203.0.113.8") {
		t.Fatalf("IPv4-only candidates = %v", got)
	}
	if got := probeCandidates(ips, "tcp", true); len(got) != 2 || got[0] != netip.MustParseAddr("203.0.113.8") || got[1] != netip.MustParseAddr("2001:db8::8") {
		t.Fatalf("dual-stack candidates = %v", got)
	}
	if got := probeCandidates(ips, "tcp6", true); len(got) != 1 || got[0] != netip.MustParseAddr("2001:db8::8") {
		t.Fatalf("IPv6-only candidates = %v", got)
	}
}
