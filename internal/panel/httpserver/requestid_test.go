//go:build !js

package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/logring"
	"github.com/mistgate/mistgate/internal/panel/rpcstat"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/reqid"
)

// The admin API's load (GetPanelResources) counts the /api calls with their status and time, and nothing else the admin
// surface serves: not the SPA's pages and assets, not /mcp or /metrics. A stream is counted without its duration.
func TestRequestIDCountsAdminAPICalls(t *testing.T) {
	stats := rpcstat.New(nil)
	s := &Server{cfg: Config{Log: slog.New(slog.DiscardHandler), RPCStats: stats}}
	handler := s.requestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/x.Service/Fail":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/api/x.Service/Deny":
			http.Error(w, "no", http.StatusForbidden)
		case "/api/x.Service/Stream":
			time.Sleep(30 * time.Millisecond)
			w.Write([]byte("chunk"))
		default:
			w.Write([]byte("ok"))
		}
	}))
	for _, p := range []string{"/api/x.Service/Do", "/api/x.Service/Do", "/api/x.Service/Fail", "/api/x.Service/Deny", "/", "/assets/a.js", "/mcp", "/metrics"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, p, nil))
	}
	stream := httptest.NewRequest(http.MethodPost, "/api/x.Service/Stream", nil)
	stream.Header.Set("Content-Type", "application/connect+proto")
	handler.ServeHTTP(httptest.NewRecorder(), stream)
	got := stats.Snapshot()
	if got.Requests != 5 || got.ServerErrors != 1 || got.ClientErrors != 1 {
		t.Fatalf("counted %+v", got)
	}
	if got.P95ms >= 30 {
		t.Fatalf("the stream's 30 ms reached the latency: p95 %v", got.P95ms)
	}
}

// A request gets an id the handler, the log records and the audit rows share; the response header carries it only once the
// caller was accepted (a stranger must see what the decoy shows); a client's own X-Request-Id is never used.
func TestRequestIDReachesTheHandlerLogsAuditAndHeader(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log, ring, lv := logring.New(io.Discard, "text", slog.LevelInfo, 50)
	s := &Server{cfg: Config{Log: log}}

	var inHandler string
	handler := s.requestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inHandler = reqid.From(r.Context())
		switch r.URL.Path {
		case "/api/x.Service/Do":
			reqid.MarkAuthed(r.Context()) // what the session middleware does
			if err := st.Audit(r.Context(), time.Now(), store.AuditEntry{Actor: "adm_1", Action: "thing.done", Params: `{"a":"b"}`, Result: "ok"}); err != nil {
				t.Error(err)
			}
			if err := st.Audit(r.Context(), time.Now(), store.AuditEntry{Actor: "adm_1", Action: "thing.empty", Result: "ok"}); err != nil {
				t.Error(err)
			}
			w.Write([]byte("ok"))
		case "/api/x.Service/Fail":
			reqid.MarkAuthed(r.Context())
			http.Error(w, "boom", http.StatusInternalServerError)
		default: // a stranger's request: no session was checked
			w.Write([]byte("decoy"))
		}
	}))
	do := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set("X-Request-Id", "client-chosen")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}

	w := do("/api/x.Service/Do")
	id := w.Header().Get(reqid.Header)
	if len(id) != 8 || id == "client-chosen" || inHandler != id {
		t.Fatalf("header %q, handler saw %q", id, inHandler)
	}
	rows, err := st.ListAudit(ctx, "", 0, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("audit rows: %v %v", rows, err)
	}
	for _, a := range rows {
		if !strings.Contains(a.Params, `"req":"`+id+`"`) {
			t.Errorf("audit row %s does not carry the request id: %s", a.Action, a.Params)
		}
	}
	if !strings.Contains(rows[1].Params, `"a":"b"`) || rows[0].Params != `{"req":"`+id+`"}` {
		t.Errorf("audit params: %q and %q", rows[1].Params, rows[0].Params)
	}

	if h := do("/anything/else").Header().Get(reqid.Header); h != "" {
		t.Fatalf("an unauthenticated response carries X-Request-Id %q", h)
	}

	w = do("/api/x.Service/Fail")
	failID := w.Header().Get(reqid.Header)
	if failID == "" || failID == id {
		t.Fatalf("failed call header %q", failID)
	}
	recs := ring.Snapshot(0, 0, func(r *logring.Record) bool { return r.Code == "http_server_error" })
	if len(recs) != 1 || recs[0].RequestID != failID || recs[0].Level != slog.LevelWarn {
		t.Fatalf("the failure record: %+v", recs)
	}
	var path string
	for _, a := range recs[0].Attrs {
		if a.Key == "path" {
			path = a.Value
		}
	}
	if path != "/api/x.Service/Fail" {
		t.Errorf("logged path %q", path)
	}
	if got := ring.Snapshot(0, 0, nil); len(got) != 1 {
		t.Errorf("successful requests are debug records, not kept at info: %d records", len(got))
	}
	lv.Set(slog.LevelDebug)
	do("/some/secretprefixzzzz/page")
	for _, r := range ring.Snapshot(0, 0, nil) {
		for _, a := range r.Attrs {
			if strings.Contains(a.Value, "secretprefixzzzz") {
				t.Errorf("a path that may hold a secret was logged: %+v", r)
			}
		}
	}
}
