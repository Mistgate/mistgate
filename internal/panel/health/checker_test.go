package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	coreErrs "github.com/apernet/hysteria/core/v2/errors"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// localTunnel is a tunnel that goes straight to a local server, recording what it was asked to dial. host
// names map to the server, like the node resolving them.
type localTunnel struct {
	mu    sync.Mutex
	dials []string
	to    string // host:port of the local server
}

func (l *localTunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	l.mu.Lock()
	l.dials = append(l.dials, addr)
	l.mu.Unlock()
	return (&net.Dialer{}).DialContext(ctx, network, l.to)
}
func (l *localTunnel) Close() error { return nil }

// probeServer answers the two probes; its behaviour is set per test.
type probeServer struct {
	srv           *httptest.Server
	status204     int32
	trace         atomic.Value // string
	url204, urlTr string
}

func newProbeServer(t *testing.T) *probeServer {
	t.Helper()
	p := &probeServer{}
	p.status204 = http.StatusNoContent
	p.trace.Store("fl=1\nip=203.0.113.9\nloc=de\nts=1\n")
	mux := http.NewServeMux()
	mux.HandleFunc("/generate_204", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(int(atomic.LoadInt32(&p.status204))) })
	mux.HandleFunc("/cdn-cgi/trace", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, p.trace.Load().(string)) })
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	_, port, _ := net.SplitHostPort(p.srv.Listener.Addr().String())
	p.url204, p.urlTr = "http://probe.test:"+port+"/generate_204", "http://probe.test:"+port+"/cdn-cgi/trace"
	return p
}

func (p *probeServer) tunnel() *localTunnel { return &localTunnel{to: p.srv.Listener.Addr().String()} }

func TestProbeStatuses(t *testing.T) {
	p := newProbeServer(t)
	e := newEnv(t, func(c *Config) { c.Probe204URL, c.ProbeTraceURL = p.url204, p.urlTr })

	t.Run("ok", func(t *testing.T) {
		tn := p.tunnel()
		r := e.s.probe(e.ctx, tn, nodeRow("203.0.113.9"), time.Now().Add(-40*time.Millisecond))
		if r.Status != cOK || r.ExitIP != "203.0.113.9" || r.ExitCountry != "DE" || r.LatencyMS < 40 || r.ErrorCode != "" || r.ErrorDetail != "" {
			t.Fatalf("%+v", r)
		}
		if len(tn.dials) != 2 || !strings.HasPrefix(tn.dials[0], "probe.test:") { // the host name went into the tunnel unresolved
			t.Fatalf("dials: %v", tn.dials)
		}
	})
	t.Run("exit differs from a literal node address is only noted", func(t *testing.T) {
		r := e.s.probe(e.ctx, p.tunnel(), nodeRow("198.51.100.1"), time.Now())
		if r.Status != cOK || !strings.Contains(r.ErrorDetail, "differs") || r.ErrorCode != "" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("204 fails: degraded", func(t *testing.T) {
		atomic.StoreInt32(&p.status204, 500)
		defer atomic.StoreInt32(&p.status204, 204)
		r := e.s.probe(e.ctx, p.tunnel(), nodeRow("n.example.com"), time.Now())
		if r.Status != cDeg || r.ErrorCode != "http_status" || r.ExitIP != "203.0.113.9" || r.LatencyMS == 0 {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("trace without an ip: degraded", func(t *testing.T) {
		p.trace.Store("fl=1\nloc=de\n")
		defer p.trace.Store("fl=1\nip=203.0.113.9\nloc=de\n")
		r := e.s.probe(e.ctx, p.tunnel(), nodeRow("n.example.com"), time.Now())
		if r.Status != cDeg || r.ErrorCode != "http_status" || r.ExitIP != "" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("nothing answers through the tunnel: failed", func(t *testing.T) {
		dead := &localTunnel{to: "127.0.0.1:1"}
		r := e.s.probe(e.ctx, dead, nodeRow("n.example.com"), time.Now())
		if r.Status != cFail || r.ErrorCode != "exit_unreachable" || r.LatencyMS != 0 || r.ExitIP != "" {
			t.Fatalf("%+v", r)
		}
	})
}

func TestTraceParsingDropsGarbage(t *testing.T) {
	ip, loc := exitFromTrace(strings.NewReader("ip=not-an-ip\nloc=germany\n"))
	if ip != "" || loc != "" {
		t.Fatalf("%q %q", ip, loc)
	}
	ip, loc = exitFromTrace(strings.NewReader("ip= 2001:db8::1 \nloc=nl\n"))
	if ip != "2001:db8::1" || loc != "NL" {
		t.Fatalf("%q %q", ip, loc)
	}
}

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{
		{&ProbeError{Code: "timeout", Detail: "x"}, "timeout"},
		{coreErrs.ConnectError{Err: errors.New("CRYPTO_ERROR 0x12a (local): tls: bad certificate")}, "tls"},
		{coreErrs.ConnectError{Err: errors.New("x509: certificate is valid for a.com, not b.com")}, "tls"},
		{coreErrs.ConnectError{Err: errors.New("certificate pin mismatch")}, "tls"},
		{coreErrs.AuthError{StatusCode: 404}, "auth"},
		{coreErrs.ConnectError{Err: errors.New("read udp: connection refused")}, "refused"},
		{coreErrs.ConnectError{Err: errors.New("timeout: no recent network activity")}, "timeout"},
		{errors.New("something odd"), "timeout"},
	} {
		if code, detail := classify(tc.err); code != tc.code || detail == "" {
			t.Errorf("%v: %s %q, want %s", tc.err, code, detail, tc.code)
		}
	}
	if _, detail := classify(errors.New(strings.Repeat("é", 400))); len(detail) > 200 {
		t.Errorf("detail is %d bytes", len(detail))
	}
}

// fixture with one node, one inbound and a dialer the test controls.
type dialFix struct {
	*env
	node  string
	in    string
	calls atomic.Int32
	dial  func(t Target) (Tunnel, error)
	probe *probeServer
}

func newDialFix(t *testing.T, mut ...func(*Config)) *dialFix {
	t.Helper()
	f := &dialFix{probe: newProbeServer(t)}
	cfg := func(c *Config) {
		c.Probe204URL, c.ProbeTraceURL = f.probe.url204, f.probe.urlTr
		c.Dialers = map[string]Dialer{"hysteria2": func(_ context.Context, tg Target, _ DialOptions) (Tunnel, error) {
			f.calls.Add(1)
			if tg.Secret == "" || tg.Spec.ID == "" || tg.Spec.Listen.Port == 0 {
				t.Errorf("incomplete target: %+v", tg)
			}
			return f.dial(tg)
		}}
	}
	f.env = newEnv(t, append([]func(*Config){cfg}, mut...)...)
	f.node = "de1"
	f.env.node(f.node, "hetzner", true)
	f.in = f.env.inbound(f.node, 443)
	f.dial = func(Target) (Tunnel, error) { return f.probe.tunnel(), nil }
	return f
}

func (f *dialFix) samples() []string {
	rows, err := f.st.RecentSamples(f.ctx, f.in, 50)
	if err != nil {
		f.t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%d:%s", r.Status, r.ErrorCode))
	}
	return out
}

// A round is an attempt and one retry: a failure that the retry fixes is recorded as OK, two failures as one
// failed round, and the node must still be there for the retry.
func TestRoundRetriesOnceAndRecordsTheLastOutcome(t *testing.T) {
	f := newDialFix(t)
	tries := 0
	f.dial = func(Target) (Tunnel, error) {
		tries++
		if tries == 1 {
			return nil, &ProbeError{Code: "timeout", Detail: "handshake timeout after 8s"}
		}
		return f.probe.tunnel(), nil
	}
	res, ok := f.s.round(f.ctx, f.in)
	if !ok || res.Status != cOK || tries != 2 {
		t.Fatalf("retry: %+v ok=%v tries=%d", res, ok, tries)
	}
	if got := f.samples(); len(got) != 1 || got[0] != "1:" {
		t.Fatalf("samples %v", got)
	}

	f.dial = func(Target) (Tunnel, error) { return nil, coreErrs.AuthError{StatusCode: 404} }
	f.clock.Advance(time.Minute)
	res, ok = f.s.round(f.ctx, f.in)
	if !ok || !res.failed() || res.ErrorCode != "auth" {
		t.Fatalf("two failures: %+v", res)
	}
	if c := f.s.cellOf(f.ctx, f.in); c.streak != 1 || c.last.ErrorCode != "auth" {
		t.Fatalf("cell: %+v", c)
	}
	if got := f.samples(); len(got) != 2 || got[0] != "3:auth" {
		t.Fatalf("samples %v", got)
	}
}

func TestSkipsAreNotFailuresAndLeaveTheStreak(t *testing.T) {
	f := newDialFix(t)
	f.dial = func(Target) (Tunnel, error) { return nil, &ProbeError{Code: "timeout", Detail: "x"} }
	f.clock.Advance(time.Second)
	f.s.round(f.ctx, f.in)
	if c := f.s.cellOf(f.ctx, f.in); c.streak != 1 {
		t.Fatalf("streak %d", c.streak)
	}
	before := f.calls.Load()

	f.fl.set(f.node, liveState{up: false, caps: []string{capDoctor}}) // node offline: nothing is dialled
	if _, ok := f.s.round(f.ctx, f.in); ok || f.calls.Load() != before {
		t.Fatal("an offline node was probed")
	}
	f.fl.set(f.node, liveState{up: true, caps: []string{capDoctor}})
	f.exec(`UPDATE inbound SET state = 'failed' WHERE id = ?`, f.in) // not active
	if _, ok := f.s.round(f.ctx, f.in); ok || f.calls.Load() != before {
		t.Fatal("an inbound that is not active was probed")
	}
	f.exec(`UPDATE inbound SET state = 'active' WHERE id = ?`, f.in)
	f.dial = func(Target) (Tunnel, error) { return nil, errClientUnsupported } // no client for it
	if _, ok := f.s.round(f.ctx, f.in); ok {
		t.Fatal("an unsupported inbound was recorded")
	}
	if c := f.s.cellOf(f.ctx, f.in); c.streak != 1 || len(f.samples()) != 1 {
		t.Fatalf("skips touched the streak: %+v, samples %v", c, f.samples())
	}

	// the node goes away during the retry wait: not a failure of the inbound
	f.dial = func(Target) (Tunnel, error) {
		f.fl.set(f.node, liveState{up: false, caps: []string{capDoctor}})
		return nil, &ProbeError{Code: "timeout", Detail: "x"}
	}
	if _, ok := f.s.round(f.ctx, f.in); ok || len(f.samples()) != 1 {
		t.Fatalf("a round that lost its node was recorded: %v", f.samples())
	}
}

func TestStreakSurvivesARestart(t *testing.T) {
	f := newDialFix(t)
	for range 3 {
		f.clock.Advance(time.Minute)
		f.s.record(f.ctx, f.in, Result{Status: cFail, At: f.clock.Now(), ErrorCode: "tls"})
	}
	again := New(f.st, f.v, f.s.reg, f.fl, f.s.cfg)
	c := again.cellOf(f.ctx, f.in)
	if c.streak != 3 || c.last == nil || c.last.ErrorCode != "tls" {
		t.Fatalf("rebuilt cell: %+v", c)
	}
	f.clock.Advance(time.Minute)
	f.s.record(f.ctx, f.in, Result{Status: cOK, At: f.clock.Now()})
	if c := New(f.st, f.v, f.s.reg, f.fl, f.s.cfg).cellOf(f.ctx, f.in); c.streak != 0 {
		t.Fatalf("streak after a pass: %d", c.streak)
	}
}

func TestNextRunKeepsAStablePhaseWithJitter(t *testing.T) {
	const interval = 300 * time.Second
	now := time.Date(2026, 9, 30, 12, 3, 27, 0, time.UTC)
	spread := map[time.Duration]int{}
	for i := range 50 {
		id := fmt.Sprintf("inb_%d", i)
		ph := phase(id, interval)
		if ph != phase(id, interval) || ph < 0 || ph >= interval {
			t.Fatalf("phase %v", ph)
		}
		spread[ph/(30*time.Second)]++
		for _, rnd := range []float64{0, 0.5, 0.999} {
			at := nextRun(now, id, interval, rnd)
			if !at.After(now) || at.Sub(now) > interval+2*jitter {
				t.Fatalf("%s: next %v from %v", id, at, now)
			}
			off := at.Sub(now.Truncate(interval)) % interval // distance from the interval start
			d := off - ph
			if d > interval/2 {
				d -= interval
			} else if d < -interval/2 {
				d += interval
			}
			if d < -jitter || d > jitter {
				t.Fatalf("%s rnd %v: %v away from its phase %v", id, rnd, d, ph)
			}
		}
	}
	if len(spread) < 8 { // 50 inbounds spread over the ten 30 s slices
		t.Fatalf("phases are clumped: %v", spread)
	}
}

func TestScheduleDueAndNextRunAfterARound(t *testing.T) {
	f := newDialFix(t, func(c *Config) { c.Interval = 300 * time.Second; c.FailedEvery = 60 * time.Second })
	f.s.scheduleDue(f.ctx)
	sc := f.s.sched[f.in]
	if sc == nil || sc.next.Before(f.clock.Now()) || sc.next.After(f.clock.Now().Add(firstRunSpan)) {
		t.Fatalf("first run: %+v", sc)
	}
	select {
	case <-f.s.work:
		t.Fatal("queued before its time")
	default:
	}
	f.clock.Advance(31 * time.Second)
	f.s.scheduleDue(f.ctx)
	select {
	case id := <-f.s.work:
		if id != f.in {
			t.Fatalf("queued %s", id)
		}
	default:
		t.Fatal("not queued when due")
	}
	f.s.scheduleDue(f.ctx) // queued once, not twice
	select {
	case id := <-f.s.work:
		t.Fatalf("queued twice: %s", id)
	default:
	}

	f.dial = func(Target) (Tunnel, error) { return nil, &ProbeError{Code: "timeout", Detail: "x"} }
	f.s.runRound(f.ctx, f.in)
	if got := f.s.sched[f.in].next.Sub(f.clock.Now()); got != 60*time.Second {
		t.Fatalf("after a failed round the next is in %v", got)
	}
	f.dial = func(Target) (Tunnel, error) { return f.probe.tunnel(), nil }
	f.s.runRound(f.ctx, f.in)
	if got := f.s.sched[f.in].next.Sub(f.clock.Now()); got <= 0 || got > 300*time.Second+jitter {
		t.Fatalf("after a good round the next is in %v", got)
	}

	f.fl.set(f.node, liveState{up: false, caps: []string{capDoctor}}) // not probed now: forgotten
	f.s.scheduleDue(f.ctx)
	if f.s.sched[f.in] != nil {
		t.Fatal("an offline node keeps a schedule")
	}
}

func TestRunChecksNowRateLimit(t *testing.T) {
	f := newDialFix(t)
	f.env.node("de2", "hostera", false)
	f.env.inbound("de2", 443)
	f.env.node("de3", "hostera", true)
	f.exec(`UPDATE node SET state = 'pending' WHERE id = 'de3'`)

	scheduled, skipped, _, err := f.s.RunChecksNow(f.ctx, "")
	if err != nil || scheduled != 1 || skipped != 1 { // de2 is offline
		t.Fatalf("first: %d scheduled %d skipped %v", scheduled, skipped, err)
	}
	if len(f.s.work) != 1 {
		t.Fatalf("queued %d", len(f.s.work))
	}
	// the round starts and finishes; asking again within 30 s is refused with the seconds left
	f.s.runRound(f.ctx, <-f.s.work)
	f.clock.Advance(10 * time.Second)
	scheduled, _, retry, _ := f.s.RunChecksNow(f.ctx, f.node)
	if scheduled != 0 || retry < 19*time.Second || retry > 21*time.Second {
		t.Fatalf("again after 10 s: %d scheduled, retry %v", scheduled, retry)
	}
	_, err = rpc{f.s}.RunChecksNow(f.ctx, connect.NewRequest(&adminv1.RunChecksNowRequest{NodeId: f.node}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "20 s") {
		t.Fatalf("rpc: %v", err)
	}
	f.clock.Advance(21 * time.Second)
	if scheduled, _, _, _ := f.s.RunChecksNow(f.ctx, f.node); scheduled != 1 {
		t.Fatalf("after 31 s: %d", scheduled)
	}
	if _, err := (rpc{f.s}).RunChecksNow(f.ctx, connect.NewRequest(&adminv1.RunChecksNowRequest{NodeId: "nod_nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown node: %v", err)
	}
}

// The worker pool never runs more rounds than configured.
func TestWorkerPoolIsBounded(t *testing.T) {
	var active, peak, rounds atomic.Int32
	f := newDialFix(t, func(c *Config) {
		c.Workers, c.Tick, c.Interval, c.FailedEvery = 2, 5*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond
	})
	f.dial = func(Target) (Tunnel, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		rounds.Add(1)
		return f.probe.tunnel(), nil
	}
	for i := range 5 {
		id := fmt.Sprintf("n%d", i)
		f.env.node(id, "p"+id, true)
		f.env.inbound(id, 443)
	}
	// the clock is real here: the scheduler compares with the fake one, so advance it with real time
	ctx, cancel := context.WithCancel(f.ctx)
	done := make(chan struct{})
	go func() { f.s.runChecker(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for rounds.Load() < 12 && time.Now().Before(deadline) {
		f.clock.Advance(20 * time.Millisecond)
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if rounds.Load() < 12 {
		t.Fatalf("only %d rounds ran", rounds.Load())
	}
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency %d, want exactly 2 (the pool size, reached under load)", peak.Load())
	}
}
