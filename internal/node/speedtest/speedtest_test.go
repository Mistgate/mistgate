package speedtest

import (
	"context"
	"errors"
	"io"
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
	mux := http.NewServeMux()
	mux.HandleFunc("/down", func(w http.ResponseWriter, r *http.Request) {
		chunk := make([]byte, 16<<10)
		for sent := 0; sent < 64<<20; sent += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			if perStream > 0 {
				select {
				case <-time.After(time.Duration(float64(len(chunk)) / float64(perStream) * float64(time.Second))):
				case <-r.Context().Done():
					return
				}
			}
		}
	})
	mux.HandleFunc("/up", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
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
