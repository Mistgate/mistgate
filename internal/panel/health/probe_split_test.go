package health

import (
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// A working tunnel gives a connect time and a response time. A handshake that takes long raises connect_ms and
// latency_ms, not response_ms, which is one request on the open connection.
func TestRoundSplitsConnectFromResponse(t *testing.T) {
	const handshake = 300 * time.Millisecond
	f := newDialFix(t)
	f.dial = func(Target) (Tunnel, error) {
		time.Sleep(handshake) // the fake of a QUIC handshake that needed a retry
		return f.probe.tunnel(), nil
	}
	res, ok := f.s.round(f.ctx, f.in)
	if !ok || res.Status != cOK {
		t.Fatalf("round: %+v ok=%v", res, ok)
	}
	if res.ConnectMS < uint32(handshake.Milliseconds()) || res.LatencyMS < res.ConnectMS {
		t.Fatalf("connect %d ms, latency %d ms: the handshake of %v is missing", res.ConnectMS, res.LatencyMS, handshake)
	}
	if res.ResponseMS == 0 || res.ResponseMS >= res.LatencyMS || res.ResponseMS >= uint32(handshake.Milliseconds()) {
		t.Fatalf("response %d ms with latency %d ms: the handshake leaked into the response time", res.ResponseMS, res.LatencyMS)
	}
	rows, err := f.st.RecentSamples(f.ctx, f.in, 1)
	if err != nil || len(rows) != 1 || rows[0].ConnectMS != res.ConnectMS || rows[0].ResponseMS != res.ResponseMS || rows[0].LatencyMS != res.LatencyMS {
		t.Fatalf("stored %+v err %v, want connect %d response %d", rows, err, res.ConnectMS, res.ResponseMS)
	}
}

// probeWith serves generate_204 with h and the Cloudflare trace as usual, and counts the TCP connections accepted.
func probeWith(t *testing.T, h http.HandlerFunc) (u204, uTrace string, conns *atomic.Int32, tn *localTunnel) {
	t.Helper()
	conns = new(atomic.Int32)
	mux := http.NewServeMux()
	mux.HandleFunc("/generate_204", h)
	mux.HandleFunc("/cdn-cgi/trace", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ip=203.0.113.9\nloc=de\n")) })
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	base := "http://probe.test:" + port
	return base + "/generate_204", base + "/cdn-cgi/trace", conns, &localTunnel{to: srv.Listener.Addr().String()}
}

func TestResponseTimeUsesTheOpenConnection(t *testing.T) {
	u204, uTrace, conns, tn := probeWith(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	e := newEnv(t, func(c *Config) { c.Probe204URL, c.ProbeTraceURL = u204, uTrace })
	r := e.s.probe(e.ctx, tn, nodeRow("203.0.113.9"), time.Now())
	if r.Status != cOK || r.ConnectMS == 0 || r.ResponseMS == 0 {
		t.Fatalf("%+v", r)
	}
	// generate_204 twice on one connection, then the trace on a new one that the server closes after the answer
	if n := conns.Load(); n != 2 {
		t.Fatalf("%d connections, want 2", n)
	}
}

// A server that hangs up after the first answer: the second request opens a new connection, which would measure a
// handshake and not a response time, so it is not counted. The round itself is not affected.
func TestResponseTimeIsZeroWhenTheConnectionIsNotReused(t *testing.T) {
	u204, uTrace, _, tn := probeWith(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusNoContent)
	})
	e := newEnv(t, func(c *Config) { c.Probe204URL, c.ProbeTraceURL = u204, uTrace })
	r := e.s.probe(e.ctx, tn, nodeRow("203.0.113.9"), time.Now())
	if r.Status != cOK || r.ConnectMS == 0 || r.LatencyMS == 0 || r.ResponseMS != 0 {
		t.Fatalf("%+v", r)
	}
}

// The second request failing (here a 500) leaves response_ms at 0 and does not fail or degrade the round.
func TestFailedSecondRequestOnlyLeavesResponseEmpty(t *testing.T) {
	var n atomic.Int32
	u204, uTrace, _, tn := probeWith(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	e := newEnv(t, func(c *Config) { c.Probe204URL, c.ProbeTraceURL = u204, uTrace })
	r := e.s.probe(e.ctx, tn, nodeRow("203.0.113.9"), time.Now())
	if r.Status != cOK || r.ErrorCode != "" || r.ResponseMS != 0 || r.ConnectMS == 0 {
		t.Fatalf("%+v", r)
	}
}

// A failed round has no connect or response time.
func TestFailedRoundHasNoSplit(t *testing.T) {
	e := newEnv(t)
	r := e.s.probe(e.ctx, &localTunnel{to: "127.0.0.1:1"}, nodeRow("n.example.com"), time.Now())
	if r.Status != cFail || r.ConnectMS != 0 || r.ResponseMS != 0 {
		t.Fatalf("%+v", r)
	}
}

// reportingTunnel dials at once (like the SOCKS5 reply of the VLESS helper) and says later when the node was reached.
type reportingTunnel struct {
	*localTunnel
	at time.Time
}

func (r reportingTunnel) ConnectedAt() time.Time { return r.at }

func TestConnectRunsToTheMomentAReportingTunnelIsUp(t *testing.T) {
	u204, uTrace, _, tn := probeWith(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	e := newEnv(t, func(c *Config) { c.Probe204URL, c.ProbeTraceURL = u204, uTrace })
	start := time.Now()
	r := e.s.probe(e.ctx, reportingTunnel{tn, start.Add(700 * time.Millisecond)}, nodeRow("203.0.113.9"), start)
	if r.ConnectMS != 700 {
		t.Fatalf("connect %d ms, want 700 (the reported moment)", r.ConnectMS)
	}
	r = e.s.probe(e.ctx, reportingTunnel{tn, time.Time{}}, nodeRow("203.0.113.9"), time.Now())
	if r.ConnectMS == 0 || r.ConnectMS > 500 {
		t.Fatalf("connect %d ms without a report, want the dial time", r.ConnectMS)
	}
}
