package health

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestRelayDownloadStallAfter16KBIsFreeze(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/generate_204", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/cdn-cgi/trace", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "ip=192.0.2.10\nloc=DE\n") })
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(relayDownloadBytes))
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(make([]byte, 16<<10)); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	base := "http://probe.test:" + port
	e := newEnv(t, func(c *Config) {
		c.Probe204URL = base + "/generate_204"
		c.ProbeTraceURL = base + "/cdn-cgi/trace"
		c.ProbeDownloadURL = base + "/download"
		c.ProbeTimeout = 250 * time.Millisecond
	})
	tn := &localTunnel{to: srv.Listener.Addr().String()}
	res := e.s.probeWithDownload(e.ctx, tn, nodeRow("192.0.2.10"), time.Now(), e.s.cfg.ProbeDownloadURL)
	if res.Status != cFail || res.ErrorCode != "freeze" || res.ErrorDetail != "download stopped after 16 KB" {
		t.Fatalf("relay probe = %+v", res)
	}
	if got := relayStageForCode(res.ErrorCode); got != "freeze" {
		t.Fatalf("freeze maps to monitor stage %q", got)
	}
}

// budgetTunnel is a transport that carries every request of a round in one flow (XHTTP): all its connections share one budget of
// bytes, and when it is spent the flow stalls, as a filter that freezes a flow after 16 KB does.
type budgetTunnel struct {
	localTunnel
	left atomic.Int64
	hang bool // the budget's end hangs the reader (a filter that drops packets); false: the helper hangs up (EOF), as Xray does on a stalled XHTTP flow
}

type budgetConn struct {
	net.Conn
	t *budgetTunnel
}

func (c *budgetConn) take(n int) bool { return c.t.left.Add(-int64(n)) >= 0 }

func (c *budgetConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && !c.take(n) {
		if !c.t.hang {
			return 0, io.EOF
		}
		<-time.After(time.Hour) // stalled: the caller's deadline ends it (a read deadline set on the real connection is not seen here)
	}
	return n, err
}

func (t *budgetTunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	c, err := t.localTunnel.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return &budgetConn{Conn: c, t: t}, nil
}

// A frozen flow that already used its budget on the first requests of the round (XHTTP carries them all) is a freeze, not
// whatever the request that met the stall happens to say.
func TestRelayFlowThatStallsBeforeTheTraceIsFreeze(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/generate_204", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/cdn-cgi/trace", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "ip=192.0.2.10\nloc=DE\n") })
	mux.HandleFunc("/download", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(relayDownloadBytes))
		_, _ = w.Write(make([]byte, relayDownloadBytes))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	base := "http://probe.test:" + port
	e := newEnv(t, func(c *Config) {
		c.Probe204URL = base + "/generate_204"
		c.ProbeTraceURL = base + "/cdn-cgi/trace"
		c.ProbeDownloadURL = base + "/download"
		c.ProbeTimeout = 400 * time.Millisecond
	})
	for _, hang := range []bool{true, false} {
		tn := &budgetTunnel{localTunnel: localTunnel{to: srv.Listener.Addr().String()}, hang: hang}
		tn.left.Store(1200) // the first request, its reuse and the start of the download: the trace would be the one to meet the end
		res := e.s.probeWithDownload(e.ctx, tn, nodeRow("192.0.2.10"), time.Now(), e.s.cfg.ProbeDownloadURL)
		if res.Status != cFail || res.ErrorCode != "freeze" {
			t.Fatalf("a flow that stalled after the first requests (hang %v) = %+v, want a freeze", hang, res)
		}
	}
	healthy := &budgetTunnel{localTunnel: localTunnel{to: srv.Listener.Addr().String()}}
	healthy.left.Store(1 << 30)
	if res := e.s.probeWithDownload(e.ctx, healthy, nodeRow("192.0.2.10"), time.Now(), e.s.cfg.ProbeDownloadURL); res.Status != cOK {
		t.Fatalf("a healthy flow = %+v", res)
	}
}
