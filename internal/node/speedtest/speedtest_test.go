package speedtest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

	cfg = small(ep(paced(t, 0)))
	cfg.Runs, cfg.Pause, cfg.DownFor, cfg.UpFor, cfg.Warm = 5, 10*time.Millisecond, 300*time.Millisecond, 100*time.Millisecond, 0
	cfg.Overall = 3 * time.Second // each run needs DownFor + UpFor + 2 s of grace in what is left
	r, err = Run(context.Background(), cfg)
	if err != nil || r.Runs < 1 || r.Runs >= 5 {
		t.Errorf("runs = %d (%v): the ones that do not fit in the deadline must not start", r.Runs, err)
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
			n := uint64(1e9)
			return func() (string, uint64, uint64, bool) { n -= 1e6; return "eth0", n, n, true }
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
