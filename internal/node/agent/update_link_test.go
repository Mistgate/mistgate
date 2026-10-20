package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/update"
)

const linkDownloadToken = "download-token-for-test"

func newLinkUpdateAgent(t *testing.T, handler http.Handler, mutators ...func(*update.Config)) (*Agent, *updFix, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	linkURL := "wss" + strings.TrimPrefix(srv.URL, "https") + "/" + strings.Repeat("p", 24) + "/"

	f := newUpdFix(t)
	cfg := Config{StateDir: t.TempDir(), LinkURL: linkURL}
	f.cfg(oldBuilt, "0.1.0-old", func(c *update.Config) {
		for _, mutate := range mutators {
			mutate(c)
		}
	})(&cfg)
	a := &Agent{
		cfg: cfg,
		meta: Meta{
			Link: linkURL, NodeID: "nod_alice",
		},
		upd:            cfg.Updater,
		linkHTTPClient: srv.Client(),
	}
	a.settings.Store(&pb.NodeSettings{})
	return a, f, srv
}

func applyLinkUpdate(ctx context.Context, a *Agent, f *updFix, token string, content []byte) (*pb.CommandResult, update.Finish) {
	manifest, signature := f.manifest(content, newBuilt)
	req := &pb.UpdateAgent{
		RequestId: "update-link-test", Manifest: manifest, Signature: signature, DownloadToken: token,
	}
	return a.upd.Apply(ctx, req, updSource{a: a, token: req.GetDownloadToken()})
}

func TestUpdateLinkFetchFullFileAndEscapesPath(t *testing.T) {
	content := []byte("signed update bytes")
	requests := make(chan *http.Request, 1)
	a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	a.meta.NodeID = "nod/alice"

	var got bytes.Buffer
	total, err := (updSource{a: a, token: linkDownloadToken}).Fetch(context.Background(), "mistgate node.bin", 0, &got)
	if err != nil {
		t.Fatal(err)
	}
	if total != uint64(len(content)) || !bytes.Equal(got.Bytes(), content) {
		t.Fatalf("Fetch = %d bytes %q, want total %d and %q", got.Len(), got.Bytes(), len(content), content)
	}
	r := <-requests
	wantPath := "/" + strings.Repeat("p", 24) + "/update/nod%2Falice/mistgate%20node.bin"
	if r.Method != http.MethodGet || r.TLS == nil || r.RequestURI != wantPath {
		t.Errorf("request = %s %s, want GET https://host%s", r.Method, r.RequestURI, wantPath)
	}
	if r.Header.Get("Authorization") != "Bearer "+linkDownloadToken {
		t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
	}
	if r.Header.Get("Range") != "" || r.Header.Get("Accept-Encoding") != "identity" {
		t.Errorf("headers = %v", r.Header)
	}
	if strings.Contains(r.RequestURI, linkDownloadToken) || r.URL.RawQuery != "" {
		t.Errorf("download token leaked into URL %q", r.RequestURI)
	}
}

func TestUpdateLinkFetchResumesAfterCutBody(t *testing.T) {
	content := bytes.Repeat([]byte("node-update-"), 512)
	cut := len(content) / 2
	var calls atomic.Int32
	a, f, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			if r.Header.Get("Range") != "" {
				t.Errorf("first request Range = %q", r.Header.Get("Range"))
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content[:cut])
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case 2:
			if got := r.Header.Get("Range"); got != fmt.Sprintf("bytes=%d-", cut) {
				t.Errorf("retry Range = %q, want bytes=%d-", got, cut)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(content)-1, len(content)))
			w.Header().Set("Content-Length", fmt.Sprint(len(content)-cut))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[cut:])
		default:
			t.Errorf("unexpected request %d", calls.Load())
			http.Error(w, "too many requests", http.StatusInternalServerError)
		}
	}))

	result, finish := applyLinkUpdate(context.Background(), a, f, linkDownloadToken, content)
	if result == nil || !result.Ok || finish == nil {
		t.Fatalf("Apply = (%v, %v), want success and finish", result, finish != nil)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
	if got, err := os.ReadFile(f.exe); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("updated executable = %d bytes, err %v", len(got), err)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if got := f.waitExec(); got != f.exe {
		t.Errorf("exec path = %q, want %q", got, f.exe)
	}
}

func TestUpdateLinkFetchResumesWithUnknownContentRangeTotal(t *testing.T) {
	content := bytes.Repeat([]byte("x"), 100)
	cut := 40
	var calls atomic.Int32
	a, f, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content[:cut])
		case 2:
			if got, want := r.Header.Get("Range"), fmt.Sprintf("bytes=%d-", cut); got != want {
				t.Errorf("retry Range = %q, want %q", got, want)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/*", cut, len(content)-1))
			w.Header().Set("Content-Length", fmt.Sprint(len(content)-cut))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[cut:])
		default:
			t.Errorf("unexpected request %d", calls.Load())
			http.Error(w, "too many requests", http.StatusInternalServerError)
		}
	}))

	result, finish := applyLinkUpdate(context.Background(), a, f, linkDownloadToken, content)
	if result == nil || !result.Ok || finish == nil {
		t.Fatalf("Apply = (%v, %v), want success and finish", result, finish != nil)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("request count = %d, want one interrupted request and one resumed request", got)
	}
	if got, err := os.ReadFile(f.exe); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("updated executable = %d bytes, err %v", len(got), err)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if got := f.waitExec(); got != f.exe {
		t.Errorf("exec path = %q, want %q", got, f.exe)
	}
}

func TestUpdateLinkFetchRetriesStalledDownload(t *testing.T) {
	content := bytes.Repeat([]byte("node-update-"), 512)
	cut := len(content) / 2
	var calls atomic.Int32
	firstCancelled := make(chan struct{})
	a, f, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content[:cut])
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			<-r.Context().Done()
			close(firstCancelled)
		case 2:
			if got, want := r.Header.Get("Range"), fmt.Sprintf("bytes=%d-", cut); got != want {
				t.Errorf("retry Range = %q, want %q", got, want)
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(content)-1, len(content)))
			w.Header().Set("Content-Length", fmt.Sprint(len(content)-cut))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[cut:])
		default:
			t.Errorf("unexpected request %d", calls.Load())
			http.Error(w, "too many requests", http.StatusInternalServerError)
		}
	}))
	a.linkPingTimeout = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, finish := applyLinkUpdate(ctx, a, f, linkDownloadToken, content)
	if result == nil || !result.Ok || finish == nil {
		t.Fatalf("Apply = (%v, %v), want success and finish", result, finish != nil)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("request count = %d, want a stalled first request and one resume", got)
	}
	select {
	case <-firstCancelled:
	case <-time.After(time.Second):
		t.Fatal("server did not observe the stalled request cancellation")
	}
	if got, err := os.ReadFile(f.exe); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("updated executable = %d bytes, err %v", len(got), err)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	if got := f.waitExec(); got != f.exe {
		t.Errorf("exec path = %q, want %q", got, f.exe)
	}
}

func TestUpdateLinkFetchMapsPermanentStatuses(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
			}))
			_, err := (updSource{a: a, token: linkDownloadToken}).Fetch(context.Background(), binName, 0, io.Discard)
			if !errors.Is(err, update.ErrPermanent) {
				t.Fatalf("Fetch error = %v, want ErrPermanent", err)
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("request count = %d, want 1", got)
			}
			if strings.Contains(err.Error(), linkDownloadToken) {
				t.Errorf("error leaked the token: %v", err)
			}
		})
	}
}

func TestUpdateLinkFetchMapsTooManyRequestsToRetryLater(t *testing.T) {
	a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	_, err := (updSource{a: a, token: linkDownloadToken}).Fetch(context.Background(), binName, 0, io.Discard)
	if !errors.Is(err, update.ErrRetryLater) {
		t.Fatalf("Fetch error = %v, want ErrRetryLater", err)
	}
}

func TestUpdateLinkFetchRetriesServiceUnavailable(t *testing.T) {
	content := []byte("available after one retry")
	var calls atomic.Int32
	a, f, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Range") != "" {
			t.Errorf("retry Range = %q, want none", r.Header.Get("Range"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))

	result, finish := applyLinkUpdate(context.Background(), a, f, linkDownloadToken, content)
	if result == nil || !result.Ok || finish == nil {
		t.Fatalf("Apply = (%v, %v), want success and finish", result, finish != nil)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("request count = %d, want retry-later loop to make 2 requests", got)
	}
}

func TestUpdateLinkFetchDoesNotFollowRedirect(t *testing.T) {
	var redirected atomic.Int32
	a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			redirected.Add(1)
			return
		}
		http.Redirect(w, r, "/redirect", http.StatusFound)
	}))
	_, err := (updSource{a: a, token: linkDownloadToken}).Fetch(context.Background(), binName, 0, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("Fetch error = %v, want transient HTTP 302 error", err)
	}
	if redirected.Load() != 0 {
		t.Errorf("followed redirect %d times", redirected.Load())
	}
}

func TestUpdateLinkFetchRejectsWrongContentRangeStart(t *testing.T) {
	a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 4-7/8")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "data")
	}))
	_, err := (updSource{a: a, token: linkDownloadToken}).Fetch(context.Background(), binName, 5, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "starts at") {
		t.Fatalf("Fetch error = %v, want wrong Content-Range start error", err)
	}
}

func TestUpdateLinkFetchContentRangeEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name         string
		contentRange string
		wantErr      bool
	}{
		{name: "unknown total", contentRange: "bytes 5-8/*"},
		{name: "malformed", contentRange: "bytes broken", wantErr: true},
		{name: "missing", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.contentRange != "" {
					w.Header().Set("Content-Range", tc.contentRange)
				}
				w.Header().Set("Content-Length", "4")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, "data")
			}))
			total, err := (updSource{a: a, token: linkDownloadToken}).Fetch(context.Background(), binName, 5, io.Discard)
			if tc.wantErr && err == nil {
				t.Fatal("Fetch succeeded with an invalid Content-Range")
			}
			if !tc.wantErr && (err != nil || total != 0) {
				t.Fatalf("Fetch total=%d err=%v, want unknown total and no error", total, err)
			}
		})
	}
}

func TestUpdateLinkFetchNetworkErrorOmitsLinkPath(t *testing.T) {
	a, _, srv := newLinkUpdateAgent(t, http.NotFoundHandler())
	srv.Close()
	_, err := (updSource{a: a, token: linkDownloadToken}).Fetch(context.Background(), binName, 0, io.Discard)
	if err == nil {
		t.Fatal("Fetch succeeded against a closed TLS server")
	}
	if strings.Contains(err.Error(), strings.Repeat("p", 24)) || strings.Contains(err.Error(), linkDownloadToken) {
		t.Errorf("network error exposed a secret: %v", err)
	}
}

func TestUpdateLinkFetchUpdaterLogOmitsLinkPath(t *testing.T) {
	logs := &lockedLogBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, nil))
	a, f, srv := newLinkUpdateAgent(t, http.NotFoundHandler(), func(c *update.Config) { c.Log = logger })
	srv.Close()

	result, finish := applyLinkUpdate(context.Background(), a, f, linkDownloadToken, []byte("new binary"))
	if result == nil || result.Ok || finish != nil {
		t.Fatalf("Apply = (%v, %v), want a refused update", result, finish != nil)
	}
	if strings.Contains(logs.String(), strings.Repeat("p", 24)) || strings.Contains(logs.String(), linkDownloadToken) {
		t.Errorf("updater log exposed a secret: %s", logs.String())
	}
}

func TestUpdateLinkFetchDiscardsPrefixWhenRangeGets200(t *testing.T) {
	content := []byte("0123456789")
	var rangeHeader string
	a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader = r.Header.Get("Range")
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
	}))
	var got bytes.Buffer
	total, err := (updSource{a: a, token: linkDownloadToken}).Fetch(context.Background(), binName, 5, &got)
	if err != nil {
		t.Fatal(err)
	}
	if rangeHeader != "bytes=5-" || total != uint64(len(content)) || got.String() != string(content[5:]) {
		t.Errorf("Fetch total=%d range=%q body=%q", total, rangeHeader, got.String())
	}
}

func TestUpdateLinkFetchRequiresToken(t *testing.T) {
	var calls atomic.Int32
	a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	_, err := (updSource{a: a}).Fetch(context.Background(), binName, 0, io.Discard)
	if !errors.Is(err, update.ErrPermanent) || calls.Load() != 0 {
		t.Fatalf("Fetch error=%v requests=%d, want permanent error without a request", err, calls.Load())
	}
}
