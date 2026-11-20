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
	return a.upd.Apply(ctx, req, &updSource{a: a, token: req.GetDownloadToken()})
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
	// The timer covers a fresh TLS handshake and the response head of the resumed request, so it must be far
	// longer than a loaded -race runner needs for that; the stalled first request still ends within it.
	a.updateLinkIdle = 400 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	var failure *update.FetchFailure
	if !errors.As(err, &failure) || failure.Code != "http_302" || errors.Is(err, update.ErrPermanent) {
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
	var failure *update.FetchFailure
	if !errors.As(err, &failure) || failure.Code != "size" || !errors.Is(err, update.ErrPermanent) {
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

func TestUpdateLinkSlowRange200PrefixKeepsIdleTimerAlive(t *testing.T) {
	// What the test proves: discarding the ignored prefix alone takes longer than the idle timer (cut*pause >
	// idle), yet it must not fire because every byte resets it. The gap between bytes is a tenth of the timer,
	// so a scheduling stall under -race has to exceed 360 ms before it can fake a stalled download.
	const (
		idle  = 400 * time.Millisecond
		pause = 40 * time.Millisecond
		cut   = 12
	)
	content := bytes.Repeat([]byte("x"), 20)
	var calls atomic.Int32
	a, f, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content[:cut])
		case 2:
			if got, want := r.Header.Get("Range"), fmt.Sprintf("bytes=%d-", cut); got != want {
				t.Errorf("range = %q, want %q", got, want)
			}
			w.Header().Set("Content-Length", fmt.Sprint(len(content)))
			w.WriteHeader(http.StatusOK) // the origin ignores Range; the agent must discard the prefix
			flusher, _ := w.(http.Flusher)
			for i := range content {
				if _, err := w.Write(content[i : i+1]); err != nil {
					return
				}
				if flusher != nil {
					flusher.Flush()
				}
				time.Sleep(pause)
			}
		default:
			t.Errorf("unexpected request %d", calls.Load())
		}
	}), func(c *update.Config) { c.IdleTimeout = idle })
	a.updateLinkIdle = idle

	result, finish := applyLinkUpdate(context.Background(), a, f, linkDownloadToken, content)
	if result == nil || !result.Ok || finish == nil {
		t.Fatalf("Apply = (%v, %v), want success and finish", result, finish != nil)
	}
	if got, err := os.ReadFile(f.exe); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("updated executable = %d bytes, err %v", len(got), err)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
}

// Without the command's token and before a session gave one, there is nothing to ask with: retry later, no request.
func TestUpdateLinkFetchWithoutTokenRetriesLater(t *testing.T) {
	var calls atomic.Int32
	a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	_, err := (updSource{a: a}).Fetch(context.Background(), binName, 0, io.Discard)
	var failure *update.FetchFailure
	if !errors.As(err, &failure) || failure.Code != "busy" || calls.Load() != 0 {
		t.Fatalf("Fetch error=%v requests=%d, want a busy (retry-later) failure without a request", err, calls.Load())
	}
}

// A session token the panel no longer knows (it restarted, or the grace after the session ended) is a 401: retry later, never
// a failed file; the next session brings a new token.
func TestLinkFetchUnknownTokenRetriesLater(t *testing.T) {
	a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	token := "a-token-from-before-the-panel-restarted"
	a.fetchToken.Store(&token)
	_, err := (updSource{a: a}).Fetch(context.Background(), networksBundleName, 0, io.Discard)
	var failure *update.FetchFailure
	if !errors.As(err, &failure) || failure.Code != "busy" || !errors.Is(err, update.ErrRetryLater) {
		t.Fatalf("Fetch error=%v, want a busy (retry-later) failure", err)
	}
}

// With the session's fetch token (a VPS panel), a file comes from the /fetch/ route, resumed by Range.
func TestLinkFetchUsesTheSessionToken(t *testing.T) {
	const sessionToken = "session-fetch-token-that-must-not-be-logged"
	content := []byte("network table bytes")
	requests := make(chan *http.Request, 2)
	a, _, _ := newLinkUpdateAgent(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Clone(r.Context())
		if r.Header.Get("Range") == "bytes=8-" {
			w.Header().Set("Content-Range", "bytes 8-18/19")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(content[8:])
			return
		}
		_, _ = w.Write(content)
	}))
	token := sessionToken
	a.fetchToken.Store(&token)
	var got bytes.Buffer
	if _, err := (updSource{a: a}).Fetch(context.Background(), networksBundleName, 0, &got); err != nil || !bytes.Equal(got.Bytes(), content) {
		t.Fatalf("Fetch = %q, %v", got.String(), err)
	}
	r := <-requests
	if r.URL.Path != "/"+strings.Repeat("p", 24)+"/fetch/nod_alice/"+networksBundleName || r.Header.Get("Authorization") != "Bearer "+sessionToken {
		t.Fatalf("request %s with %q", r.URL.Path, r.Header.Get("Authorization"))
	}
	got.Reset()
	if _, err := (updSource{a: a}).Fetch(context.Background(), networksBundleName, 8, &got); err != nil || got.String() != string(content[8:]) {
		t.Fatalf("resumed Fetch = %q, %v", got.String(), err)
	}
}
