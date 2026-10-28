package health

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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
