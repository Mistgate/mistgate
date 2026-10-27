package cheburcheck

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
)

type fakeReply struct {
	status int
	body   string
	hold   bool
}

type recordedRequest struct {
	method, path, query, accept, userAgent, authorization string
}

type fakeSite struct {
	srv *httptest.Server

	mu             sync.Mutex
	requests       []recordedRequest
	checks         int
	probeCount     int
	checkResponses []fakeReply
	probeResponses []fakeReply
	probeStarted   chan struct{}
	startedOnce    sync.Once
}

func newFakeSite() *fakeSite {
	f := &fakeSite{probeStarted: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

func (f *fakeSite) close() { f.srv.Close() }

func (f *fakeSite) serve(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	f.mu.Lock()
	f.requests = append(f.requests, recordedRequest{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
		accept: r.Header.Get("Accept"), userAgent: r.Header.Get("User-Agent"), authorization: r.Header.Get("Authorization")})
	var reply fakeReply
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/status":
		w.Header().Set("Content-Type", "application/json")
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"domain_count":7,"v4_count":9,"last_update":"2026-10-27T08:34:39Z","version":"1.5.4"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/check":
		f.checks++
		n := f.checks
		if n <= len(f.checkResponses) {
			reply = f.checkResponses[n-1]
		}
		f.mu.Unlock()
		if reply.status != 0 && reply.status != http.StatusOK {
			http.Error(w, `{"code":429}`, reply.status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if reply.body != "" {
			_, _ = io.WriteString(w, reply.body)
			return
		}
		_, _ = io.WriteString(w, `{"id":"context-`+strconv.Itoa(n)+`"}`)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/probe/"):
		f.probeCount++
		n := f.probeCount
		if n <= len(f.probeResponses) {
			reply = f.probeResponses[n-1]
		}
		f.mu.Unlock()
		f.startedOnce.Do(func() { close(f.probeStarted) })
		if reply.status != 0 && reply.status != http.StatusOK {
			http.Error(w, `{"code":401}`, reply.status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if reply.body == "" {
			reply.body = completeFrames()
		}
		_, _ = io.WriteString(w, reply.body)
		if reply.hold {
			if flush, ok := w.(http.Flusher); ok {
				flush.Flush()
			}
			<-r.Context().Done()
		}
	default:
		f.mu.Unlock()
		http.NotFound(w, r)
	}
}

func (f *fakeSite) snapshot() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recordedRequest(nil), f.requests...)
}

func (f *fakeSite) counts() (checks, probes int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.probeCount
}

func completeFrames() string {
	return "event: started\ndata: {\"online_probes\":2}\n\n" +
		"event: result\ndata: {\"probe_id\":1,\"region\":\"Example Region\",\"provider\":\"Example ISP\",\"asn\":\"AS64496\",\"verdicts\":[\"ok\"]}\n\n" +
		"event: result\ndata: {\"probe_id\":2,\"region\":\"Sample Region\",\"provider\":\"Sample Network\",\"asn\":64497,\"verdicts\":[\"tspu_block\"]}\n\n" +
		"event: done\ndata: {\"status\":\"done\",\"online_probes\":2}\n\n"
}
