package speedtest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The measurement logic against local servers: no internet. A server that paces every connection to a known rate says
// what the number should be.

// paced serves zeros on /down at perStream bytes per second on each connection (0 = as fast as the loopback allows) and
// takes uploads on /up, answering after the whole body is read.
func paced(t *testing.T, perStream int) *httptest.Server {
	t.Helper()
	var rate atomic.Int64
	rate.Store(int64(perStream))
	return pacedAt(t, &rate)
}

// pacedAt is paced with a rate the test can change between runs.
func pacedAt(t *testing.T, rate *atomic.Int64) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 16<<10)
		for sent := 0; sent < 64<<20; sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if perStream := rate.Load(); perStream > 0 {
				select {
				case <-time.After(time.Duration(float64(len(chunk)) / float64(perStream) * float64(time.Second))):
				case <-r.Context().Done():
					return
				}
			}
		}
	})
	mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 16<<10)
		for {
			n, err := r.Body.Read(buf)
			if perStream := rate.Load(); perStream > 0 && n > 0 { // the upload is paced like the download
				time.Sleep(time.Duration(float64(n) / float64(perStream) * float64(time.Second)))
			}
			if err != nil {
				return
			}
		}
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func small(eps ...Endpoint) Config {
	return Config{
		Endpoints:   eps,
		DownStreams: 4, UpStreams: 2,
		DownFor: 1500 * time.Millisecond, UpFor: 600 * time.Millisecond, Warm: 300 * time.Millisecond,
		DownCap: 1 << 30, UpCap: 1 << 30,
		Overall: 10 * time.Second,
	}
}

func TestMeasuresDownloadAndUpload(t *testing.T) {
	s := paced(t, 1_000_000) // 4 streams x 1 MB/s = 32 Mbit/s
	r, err := Run(context.Background(), small(Endpoint{Name: "local", DownURL: s.URL + "/down", UpURL: s.URL + "/up"}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Server != "local" || r.DownStreams != 4 {
		t.Errorf("server %q, streams %d", r.Server, r.DownStreams)
	}
	// pacing sleeps overshoot a little, so the measured rate sits a bit under the ideal 32 Mbit/s
	if r.DownMbps < 20 || r.DownMbps > 34 {
		t.Errorf("download %.1f Mbit/s, want about 32", r.DownMbps)
	}
	if r.DownBytes < 3_000_000 {
		t.Errorf("only %d bytes downloaded", r.DownBytes)
	}
	if r.UpMbps <= 0 || r.UpBytes <= 0 {
		t.Errorf("upload %.1f Mbit/s, %d bytes", r.UpMbps, r.UpBytes)
	}
	if r.Seconds < 1.5 || r.Seconds > 6 {
		t.Errorf("took %.1f s", r.Seconds)
	}
}

func TestTheByteCapEndsThePhaseEarly(t *testing.T) {
	s := paced(t, 0)
	cfg := small(Endpoint{Name: "local", DownURL: s.URL + "/down", UpURL: s.URL + "/up"})
	cfg.DownFor, cfg.UpFor = 20*time.Second, 20*time.Second
	cfg.DownCap, cfg.UpCap = 8<<20, 4<<20
	t0 := time.Now()
	r, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(t0) > 10*time.Second {
		t.Errorf("the caps did not stop the test: %s", time.Since(t0))
	}
	// every stream may add the read it was in the middle of; nothing near another megabyte per stream
	if r.DownBytes < 8<<20 || r.DownBytes > 8<<20+int64(cfg.DownStreams)*(1<<20) {
		t.Errorf("downloaded %d bytes with a cap of %d", r.DownBytes, 8<<20)
	}
	if r.UpBytes < 4<<20 || r.UpBytes > 4<<20+int64(cfg.UpStreams)*maxUpChunk {
		t.Errorf("uploaded %d bytes with a cap of %d", r.UpBytes, 4<<20)
	}
	if r.DownMbps <= 0 {
		t.Errorf("a run cut by the cap still has a rate, got %.1f", r.DownMbps)
	}
}

func TestFallsBackToTheNextServer(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusServiceUnavailable) }))
	defer down.Close()
	s := paced(t, 0)
	cfg := small(
		Endpoint{Name: "first", DownURL: down.URL + "/down", UpURL: down.URL + "/up"},
		Endpoint{Name: "second", DownURL: s.URL + "/down"},
	)
	cfg.DownFor = 800 * time.Millisecond
	r, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Server != "second" || r.DownMbps <= 0 {
		t.Errorf("got %+v", r)
	}
	if r.UpMbps != 0 || r.UpBytes != 0 {
		t.Errorf("a server without an upload reports one: %+v", r)
	}
}

func TestAFailedUploadKeepsTheDownload(t *testing.T) {
	s := paced(t, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.URL+"/down", http.StatusFound)
	})
	mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) })
	front := httptest.NewServer(mux)
	defer front.Close()
	cfg := small(Endpoint{Name: "front", DownURL: front.URL + "/down", UpURL: front.URL + "/up"})
	cfg.DownFor = 600 * time.Millisecond
	r, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.DownMbps <= 0 || r.UpMbps != 0 {
		t.Errorf("got %+v", r)
	}
}

func TestNothingReachableIsUnreachable(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusInternalServerError) }))
	gone := httptest.NewServer(http.NotFoundHandler())
	goneURL := gone.URL
	gone.Close() // a port nobody listens on: connection refused
	cfg := small(Endpoint{Name: "bad", DownURL: bad.URL}, Endpoint{Name: "gone", DownURL: goneURL})
	defer bad.Close()
	t0 := time.Now()
	_, err := Run(context.Background(), cfg)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(t0) > 8*time.Second {
		t.Errorf("took %s to give up", time.Since(t0))
	}
}

func TestAHangingServerEndsAtTheOverallDeadline(t *testing.T) {
	var hits atomic.Int32
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-r.Context().Done()
	}))
	defer hang.Close()
	cfg := small(Endpoint{Name: "hang", DownURL: hang.URL}, Endpoint{Name: "hang2", DownURL: hang.URL})
	cfg.DownFor = 30 * time.Second
	cfg.Overall = 700 * time.Millisecond
	t0 := time.Now()
	_, err := Run(context.Background(), cfg)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("the overall deadline did not hold: %s", d)
	}
	if hits.Load() == 0 {
		t.Error("the server was never asked")
	}
}

func TestACancelledRunStops(t *testing.T) {
	s := paced(t, 100_000)
	ctx, cancel := context.WithCancel(context.Background())
	cfg := small(Endpoint{Name: "local", DownURL: s.URL + "/down"})
	cfg.DownFor = 30 * time.Second
	time.AfterFunc(300*time.Millisecond, cancel)
	t0 := time.Now()
	r, err := Run(ctx, cfg)
	if time.Since(t0) > 3*time.Second {
		t.Errorf("a cancelled run went on for %s", time.Since(t0))
	}
	// whatever was read before the cancel is a result or an error, never a hang
	_ = r
	_ = err
}

func ep(s *httptest.Server) Endpoint {
	return Endpoint{Name: "local", DownURL: s.URL + "/down", UpURL: s.URL + "/up"}
}

// Three runs back to back and the best one wins, per direction: the first run is slow, the second fast, the third in between.
func TestThreeRunsTheBestWins(t *testing.T) {
	var rate atomic.Int64
	s := pacedAt(t, &rate)
	cfg := small(ep(s))
	cfg.Runs, cfg.Pause = 3, 30*time.Millisecond
	cfg.DownFor, cfg.UpFor, cfg.Warm = 800*time.Millisecond, 300*time.Millisecond, 200*time.Millisecond
	perStream := []int64{500_000, 2_000_000, 1_000_000} // 4 streams: 16, 64 and 32 Mbit/s
	var started []int
	cfg.beforeRun = func(i int) { started = append(started, i); rate.Store(perStream[i]) }
	t0 := time.Now()
	r, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Runs != 3 || len(started) != 3 {
		t.Errorf("runs = %d, started %v", r.Runs, started)
	}
	if r.DownMbps < 45 || r.DownMbps > 66 {
		t.Errorf("download %.1f Mbit/s, want the best run's, about 64 (not 16 or 32)", r.DownMbps)
	}
	if r.UpMbps <= 0 {
		t.Errorf("upload %.1f", r.UpMbps)
	}
	if time.Since(t0) < 3*time.Second { // three runs of >= 1.1 s each, with the pauses
		t.Errorf("three runs took only %s", time.Since(t0))
	}
	if r.DownBytes < 3*400_000 {
		t.Errorf("bytes of all runs: %d", r.DownBytes)
	}
}

// A later run that fails is skipped; the first one's result stands. And a run that does not fit in the time left is not started.
func TestARunThatFailsOrDoesNotFitIsSkipped(t *testing.T) {
	var rate atomic.Int64
	rate.Store(1_000_000)
	var down atomic.Int32
	mux := http.NewServeMux()
	inner := pacedAt(t, &rate)
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		if down.Add(1) > 4 { // the first run's four streams are served, then it all breaks
			http.Error(w, "no", http.StatusServiceUnavailable)
			return
		}
		http.Redirect(w, r, inner.URL+"/down", http.StatusFound)
	})
	front := httptest.NewServer(mux)
	defer front.Close()
	cfg := small(Endpoint{Name: "local", DownURL: front.URL + "/down"})
	cfg.Runs, cfg.Pause, cfg.DownFor, cfg.Warm = 3, 10*time.Millisecond, 500*time.Millisecond, 100*time.Millisecond
	r, err := Run(context.Background(), cfg)
	if err != nil || r.Runs != 1 || r.DownMbps <= 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if r.RunsTotal != 3 || !slices.Equal(r.Failures, []string{"http_503", "http_503"}) {
		t.Errorf("the result must say 1 of 3 worked and why: total %d, failures %v", r.RunsTotal, r.Failures)
	}

	cfg = small(ep(paced(t, 0)))
	cfg.Runs, cfg.Pause, cfg.DownFor, cfg.UpFor, cfg.Warm = 5, 10*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond, 0
	cfg.Overall = 3 * time.Second // each run needs DownFor + UpFor + 2 s of grace in what is left
	r, err = Run(context.Background(), cfg)
	if err != nil || r.Runs < 1 || r.Runs >= 5 {
		t.Errorf("runs = %d (%v): the ones that do not fit in the deadline must not start", r.Runs, err)
	}
	if len(r.Failures) != 5-r.Runs || slices.ContainsFunc(r.Failures, func(s string) bool { return s != ReasonTimeout }) {
		t.Errorf("runs that never started are failures of time: %v with %d of 5 worked", r.Failures, r.Runs)
	}
}

// after is an endpoint that serves the first n downloads from inner and then answers bad.
func after(t *testing.T, inner *httptest.Server, n int32, bad http.HandlerFunc) Endpoint {
	t.Helper()
	var served atomic.Int32
	front := http.NewServeMux()
	front.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		if served.Add(1) > n {
			bad(w, r)
			return
		}
		http.Redirect(w, r, inner.URL+"/down", http.StatusFound)
	})
	s := httptest.NewServer(front)
	t.Cleanup(s.Close)
	return Endpoint{Name: "flaky", DownURL: s.URL + "/down"}
}

func TestRunFailuresCarryAReasonCode(t *testing.T) {
	for name, tc := range map[string]struct {
		bad  http.HandlerFunc
		want string
	}{
		"limited": {func(w http.ResponseWriter, r *http.Request) { http.Error(w, "slow down", http.StatusTooManyRequests) }, "rate_limited"},
		"error":   {func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusBadGateway) }, "http_502"},
		"silent":  {func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }, "timeout"},
	} {
		inner := paced(t, 1_000_000)
		e := after(t, inner, 4, tc.bad) // the first run's four streams are served
		cfg := small(e)
		cfg.Runs, cfg.Pause, cfg.DownFor, cfg.Warm = 3, 10*time.Millisecond, 400*time.Millisecond, 100*time.Millisecond
		r, err := Run(context.Background(), cfg)
		if err != nil || r.Runs != 1 || r.RunsTotal != 3 || !slices.Equal(r.Failures, []string{tc.want, tc.want}) {
			t.Errorf("%s: %+v %v, want 1 of 3 and failures [%s %s]", name, r, err, tc.want, tc.want)
		}
	}
}

func TestReasonOfAnError(t *testing.T) {
	for want, err := range map[string]error{
		"rate_limited": &statusError{code: 429},
		"http_503":     &statusError{code: 503},
		"timeout":      context.DeadlineExceeded,
		"unreachable":  errors.New("dial tcp: connection refused"),
	} {
		if got := reason(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
	if got := reason(errors.Join(errors.New("x"), &statusError{code: 429})); got != "rate_limited" {
		t.Errorf("a wrapped 429: %q", got)
	}
}

// A 429 is waited out, not given up on: the server limits the first requests and answers after Retry-After.
func TestATooManyRequestsAnswerIsWaitedOut(t *testing.T) {
	inner := paced(t, 0)
	var hits atomic.Int32
	limiter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) <= 4 { // every stream of the run is turned away once
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		http.Redirect(w, r, inner.URL+"/down", http.StatusFound)
	}))
	defer limiter.Close()
	cfg := small(Endpoint{Name: "limited", DownURL: limiter.URL})
	cfg.DownFor, cfg.Warm = 2500*time.Millisecond, 0
	r, err := Run(context.Background(), cfg)
	if err != nil || r.Runs != 1 || r.DownMbps <= 0 || len(r.Failures) != 0 || r.DownBytes == 0 {
		t.Fatalf("a run that was limited at first and then served: %+v %v", r, err)
	}
	// without the wait, every stream gives up after three quick failures and the run fails
}

func TestBackoffFollowsRetryAfterAndIsCapped(t *testing.T) {
	limited := func(wait time.Duration) error { return &statusError{code: 429, wait: wait} }
	if got := nextBackoff(0, limited(0)); got != firstBackoff {
		t.Errorf("first wait %s", got)
	}
	if got := nextBackoff(firstBackoff, limited(0)); got != 2*firstBackoff {
		t.Errorf("it doubles: %s", got)
	}
	if got := nextBackoff(maxBackoff, limited(0)); got != maxBackoff {
		t.Errorf("it is capped: %s", got)
	}
	if got := nextBackoff(0, limited(time.Second)); got != time.Second {
		t.Errorf("Retry-After: %s", got)
	}
	if got := nextBackoff(0, limited(time.Minute)); got != maxBackoff {
		t.Errorf("a long Retry-After is capped: %s", got)
	}
}

// An Ookla server of the tests: /hello answers after delay, /download?size=N gives N zeros, /upload takes a body. status
// != 0 makes the download fail with it. The counters say what was asked of it.
type fakeOokla struct {
	host            string
	hello, down, up atomic.Int32
	sizes           atomic.Value // the last size= asked
}

// closedHost is a host:port nobody listens on.
func closedHost() string {
	s := httptest.NewServer(http.NotFoundHandler())
	s.Close()
	return strings.TrimPrefix(s.URL, "http://")
}

func newFakeOokla(t *testing.T, delay time.Duration, status int) *fakeOokla {
	t.Helper()
	f := &fakeOokla{}
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		f.hello.Add(1)
		time.Sleep(delay)
		_, _ = w.Write([]byte("hello 2.11"))
	})
	mux.HandleFunc("/download", func(w http.ResponseWriter, r *http.Request) {
		f.down.Add(1)
		f.sizes.Store(r.URL.Query().Get("size"))
		if status != 0 {
			http.Error(w, "no", status)
			return
		}
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		chunk := make([]byte, 16<<10)
		for sent := 0; sent < min(size, 64<<20); sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		f.up.Add(1)
		n, _ := io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte("size=" + strconv.FormatInt(n, 10)))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	f.host = strings.TrimPrefix(s.URL, "http://")
	return f
}

// list serves the Speedtest.net list the way the real one answers: a JSON array with host:port, sponsor and city.
func list(t *testing.T, servers ...map[string]any) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(servers)
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func entry(host, sponsor, city string) map[string]any {
	return map[string]any{"url": "http://" + host + "/speedtest/upload.php", "name": city, "country": "Russia", "cc": "RU", "sponsor": sponsor,
		"id": "1234", "distance": 12, "https_functional": 1, "host": host}
}

func ooklaCfg(listURL string, fallback ...Endpoint) Config {
	cfg := small(fallback...)
	cfg.OoklaList, cfg.ooklaScheme, cfg.ooklaLocal = listURL, "http", true
	return cfg
}

// The list comes from a third party: a server that is not on a public address (a tampered answer pointing at the node's own
// network) is never contacted, not even for latency, and the chain behind Ookla answers instead.
func TestOoklaServersAtNonPublicAddressesAreNeverContacted(t *testing.T) {
	local := newFakeOokla(t, 0, 0) // 127.0.0.1
	s := paced(t, 0)
	chain := Endpoint{Name: "local", DownURL: s.URL + "/down", UpURL: s.URL + "/up"}
	var hosts []map[string]any
	hosts = append(hosts, entry(local.host, "Loopback", "Here"))
	for _, h := range []string{"10.1.2.3:8080", "192.168.0.7:8080", "172.16.5.5:8080", "100.64.1.1:8080", "169.254.169.254:80", "0.0.0.0:8080", "224.0.0.1:8080",
		"[::1]:8080", "[fe80::1]:8080", "[fd00::1]:8080", "[::ffff:10.0.0.1]:8080", "[::]:8080", "localhost:8080"} {
		hosts = append(hosts, entry(h, "Internal", "Nowhere"))
	}
	cfg := ooklaCfg(list(t, hosts...), chain)
	cfg.ooklaLocal = false
	cfg.DownFor, cfg.UpFor = 600*time.Millisecond, 300*time.Millisecond
	r, err := Run(context.Background(), cfg)
	if err != nil || r.Server != "local" {
		t.Fatalf("%+v %v", r, err)
	}
	if local.hello.Load() != 0 || local.down.Load() != 0 || local.up.Load() != 0 {
		t.Errorf("a loopback server was contacted: %d hello, %d down, %d up", local.hello.Load(), local.down.Load(), local.up.Load())
	}
}

func TestOoklaRejectsLoopbackAtDialTimeAfterAPublicListing(t *testing.T) {
	local := newFakeOokla(t, 0, 0)
	_, port, err := net.SplitHostPort(local.host)
	if err != nil {
		t.Fatal(err)
	}
	listed := net.JoinHostPort("93.184.216.34", port)
	chain := paced(t, 0)
	cfg := ooklaCfg(list(t, entry(listed, "Public", "Example")), Endpoint{Name: "local", DownURL: chain.URL + "/down"})
	cfg.ooklaLocal = false
	cfg.ooklaDialAddress = func(address string) string {
		host, port, err := net.SplitHostPort(address)
		if err == nil && host == "93.184.216.34" {
			return net.JoinHostPort("127.0.0.1", port)
		}
		return address
	}
	cfg.DownFor, cfg.UpFor = 300*time.Millisecond, 100*time.Millisecond
	r, err := Run(context.Background(), cfg)
	if err != nil || r.Server != "local" {
		t.Fatalf("%+v %v", r, err)
	}
	if local.hello.Load() != 0 || local.down.Load() != 0 || local.up.Load() != 0 {
		t.Errorf("a listed public address that dialed to loopback was contacted: %d hello, %d down, %d up", local.hello.Load(), local.down.Load(), local.up.Load())
	}
}

func TestOoklaDoesNotFollowServerRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		_, _ = w.Write([]byte("hello 2.11"))
	}))
	defer target.Close()
	var sourceHits atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hello" {
			sourceHits.Add(1)
			http.Redirect(w, r, target.URL+"/hello", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	defer source.Close()
	chain := paced(t, 0)
	cfg := ooklaCfg(list(t, entry(strings.TrimPrefix(source.URL, "http://"), "Source", "Example")), Endpoint{Name: "local", DownURL: chain.URL + "/down"})
	cfg.DownFor, cfg.UpFor = 300*time.Millisecond, 100*time.Millisecond
	r, err := Run(context.Background(), cfg)
	if err != nil || r.Server != "local" {
		t.Fatalf("%+v %v", r, err)
	}
	if sourceHits.Load() == 0 || redirected.Load() != 0 {
		t.Errorf("source hits=%d, redirected target hits=%d", sourceHits.Load(), redirected.Load())
	}
}

func TestPublicAddr(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.216.34": true, "2606:4700::1111": true, "8.8.8.8": true,
		"127.0.0.1": false, "10.0.0.1": false, "172.31.255.255": false, "192.168.1.1": false, "100.127.0.1": false, "169.254.1.1": false,
		"0.0.0.0": false, "224.0.0.251": false, "255.255.255.255": false, "::1": false, "::": false, "fe80::1": false, "fc00::1": false,
		"ff02::1": false, "::ffff:127.0.0.1": false, "::ffff:8.8.8.8": true,
	} {
		if got := publicAddr(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicAddr(%s) = %v, want %v", addr, got, want)
		}
	}
}

// The list is parsed and the servers are ranked by latency: the nearest answers, the others are only probed.
func TestOoklaUsesTheServerWithTheLowestLatency(t *testing.T) {
	slow := newFakeOokla(t, 150*time.Millisecond, 0)
	near := newFakeOokla(t, 0, 0)
	mid := newFakeOokla(t, 60*time.Millisecond, 0)
	url := list(t,
		entry(slow.host, "Far ISP", "Farville"),
		entry(closedHost(), "Nobody", "Nowhere"), // nothing listens there
		entry("not a/host", "Broken", "Entry"),   // dropped, not asked
		entry(mid.host, "Mid ISP", "Midtown"),
		entry(near.host, "МТС", "Москва"),
	)
	cfg := ooklaCfg(url)
	cfg.DownFor, cfg.UpFor = 800*time.Millisecond, 400*time.Millisecond
	r, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.Server != "Ookla" || r.Detail != "МТС, Москва" {
		t.Errorf("server %q, detail %q", r.Server, r.Detail)
	}
	if near.down.Load() == 0 || near.up.Load() == 0 {
		t.Errorf("the nearest server saw %d downloads and %d uploads", near.down.Load(), near.up.Load())
	}
	if size, _ := near.sizes.Load().(string); size != "100000000" {
		t.Errorf("download asked for size=%q", size)
	}
	if slow.hello.Load() == 0 || slow.down.Load() != 0 || mid.down.Load() != 0 {
		t.Errorf("a slower server is probed but not downloaded from: slow %d/%d, mid %d", slow.hello.Load(), slow.down.Load(), mid.down.Load())
	}
	if r.DownMbps <= 0 || r.UpMbps <= 0 || r.DownBytes == 0 || r.UpBytes == 0 {
		t.Errorf("%+v", r)
	}
	if r.Runs != 1 || r.RunsTotal != 1 || len(r.Failures) != 0 {
		t.Errorf("runs %d of %d, failures %v", r.Runs, r.RunsTotal, r.Failures)
	}
}

// The second nearest takes over when the first one cannot serve; the others of the fallback chain are not touched.
func TestOoklaGoesToTheNextNearestWhenTheFirstFails(t *testing.T) {
	broken := newFakeOokla(t, 0, http.StatusServiceUnavailable)
	next := newFakeOokla(t, 40*time.Millisecond, 0)
	never := newFakeOokla(t, 0, 0)
	cfg := ooklaCfg(list(t, entry(broken.host, "A", "One"), entry(next.host, "B", "Two")), Endpoint{Name: "local", DownURL: "http://" + never.host + "/download?size=1000000"})
	cfg.DownFor, cfg.UpFor = 600*time.Millisecond, 300*time.Millisecond
	r, err := Run(context.Background(), cfg)
	if err != nil || r.Server != "Ookla" || r.Detail != "B, Two" || r.Runs != 1 {
		t.Fatalf("%+v %v", r, err)
	}
	if broken.down.Load() == 0 || never.down.Load() != 0 {
		t.Errorf("the broken server was asked %d times, the fallback %d", broken.down.Load(), never.down.Load())
	}
}

// No Ookla: the list is down, not JSON, empty, or lists servers that do not answer. The configured chain runs as before.
func TestFallsBackToTheChainWhenOoklaIsNotUsable(t *testing.T) {
	s := paced(t, 0)
	chain := Endpoint{Name: "local", DownURL: s.URL + "/down", UpURL: s.URL + "/up"}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) }))
	defer down.Close()
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>captcha</html>")) }))
	defer junk.Close()
	dead := closedHost()
	for name, listURL := range map[string]string{
		"the list answers 403":     down.URL,
		"the list is not JSON":     junk.URL,
		"the list is unreachable":  "http://" + dead,
		"the list is empty":        list(t),
		"no listed server answers": list(t, entry(dead, "Dead", "Town")),
		"a server without /hello":  list(t, entry(strings.TrimPrefix(down.URL, "http://"), "Forbidden", "Town")),
	} {
		cfg := ooklaCfg(listURL, chain)
		cfg.DownFor, cfg.UpFor = 600*time.Millisecond, 300*time.Millisecond
		r, err := Run(context.Background(), cfg)
		if err != nil || r.Server != "local" || r.Detail != "" || r.DownMbps <= 0 || r.Runs != 1 {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
}

// With an Ookla server that moves no byte and a chain behind it, the chain answers; Failures stays empty because the run that
// worked was the first run on the server that did.
func TestAnOoklaServerThatAnswersHelloButNoDataFallsThrough(t *testing.T) {
	bad := newFakeOokla(t, 0, http.StatusTooManyRequests)
	s := paced(t, 0)
	cfg := ooklaCfg(list(t, entry(bad.host, "A", "One")), Endpoint{Name: "local", DownURL: s.URL + "/down"})
	cfg.DownFor = 500 * time.Millisecond
	r, err := Run(context.Background(), cfg)
	if err != nil || r.Server != "local" || len(r.Failures) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestDefaultAsksOoklaFirstAndSpacesCloudflareOut(t *testing.T) {
	d := Default(nil)
	if !strings.HasPrefix(d.OoklaList, "https://www.speedtest.net/api/js/servers?") || !strings.Contains(d.OoklaList, "https_functional=true") {
		t.Errorf("list %q", d.OoklaList)
	}
	if d.Endpoints[0].Name != "speed.cloudflare.com" || d.Endpoints[0].Pause <= d.Pause || d.Endpoints[0].Pause < 2*time.Second {
		t.Errorf("Cloudflare is 429-limited: runs on it must be spaced out, got %v", d.Endpoints[0])
	}
	if d.Runs != 3 || d.DownCap*int64(d.Runs)+d.UpCap*int64(d.Runs) > 1_500_000_000 || d.Overall > 45*time.Second {
		t.Errorf("the limits moved: %+v", d)
	}
}

// counters is a fake host interface that moves at a fixed rate whatever the test does: people already using the node.
func counters(iface string, rxBps, txBps float64) Counters {
	t0 := time.Now()
	return func() (string, uint64, uint64, bool) {
		el := time.Since(t0).Seconds()
		return iface, uint64(el * rxBps / 8), uint64(el * txBps / 8), true
	}
}

// What the interface carries beyond the test is the people's: the estimate is the interface's rate, and the share is the
// difference.
func TestPeopleOnTheNodeCountAsCapacity(t *testing.T) {
	s := paced(t, 1_000_000) // the test alone: 32 Mbit/s
	cfg := small(ep(s))
	cfg.UpFor = 1200 * time.Millisecond
	cfg.Counters = counters("eth0", 100e6, 50e6) // the interface moves 100 Mbit/s in and 50 out
	r, err := Run(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r.DownMbps < 90 || r.DownMbps > 110 {
		t.Errorf("download %.1f, want the interface's 100", r.DownMbps)
	}
	if want := 100 - 32*wireOverhead; r.PeopleDownMbps < want-12 || r.PeopleDownMbps > want+12 {
		t.Errorf("people's download %.1f, want about %.0f", r.PeopleDownMbps, want)
	}
	if r.UpMbps < 40 || r.UpMbps > 60 || r.PeopleUpMbps <= 0 {
		t.Errorf("upload %.1f, people %.1f: the interface sends 50", r.UpMbps, r.PeopleUpMbps)
	}
}

// An interface that carries no more than the test (its headers apart) adds no people, and the test alone is the estimate when
// the interface is slower or cannot be read.
func TestInterfaceCountersNeverLowerTheResult(t *testing.T) {
	s := paced(t, 1_000_000)
	for name, c := range map[string]Counters{
		"the same as the test (plus headers)": counters("eth0", 32e6*1.04, 8e6),
		"slower than the test":                counters("eth0", 5e6, 1e6),
		"unreadable":                          func() (string, uint64, uint64, bool) { return "", 0, 0, false },
		"the route moved":                     moving(),
		"a counter went back": func() func() (string, uint64, uint64, bool) {
			var n atomic.Uint64 // read from the warm-up timer and from the main goroutine
			n.Store(1e9)
			return func() (string, uint64, uint64, bool) { v := n.Add(^uint64(1e6 - 1)); return "eth0", v, v, true }
		}(),
		"none": nil,
	} {
		cfg := small(ep(s))
		cfg.DownFor, cfg.UpFor, cfg.Warm = 800*time.Millisecond, 300*time.Millisecond, 200*time.Millisecond
		cfg.Counters = c
		r, err := Run(context.Background(), cfg)
		if err != nil {
			t.Fatal(name, err)
		}
		if r.DownMbps < 20 || r.DownMbps > 36 {
			t.Errorf("%s: download %.1f Mbit/s, want about 32 (the test's own)", name, r.DownMbps)
		}
		if r.PeopleDownMbps > 3 {
			t.Errorf("%s: %.1f Mbit/s of people where there are none", name, r.PeopleDownMbps)
		}
	}
}

// moving is a source whose interface changes at every read: the pair of reads is not comparable and is thrown away.
func moving() Counters {
	var n atomic.Int64
	return func() (string, uint64, uint64, bool) {
		k := n.Add(1)
		return "eth" + string(rune('0'+k%10)), uint64(k) * 1e9, uint64(k) * 1e9, true
	}
}
