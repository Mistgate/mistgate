// Package speedtest measures how fast a node reaches the internet, for the panel's "network capacity" suggestion
// (agent.proto "BANDWIDTH TEST"). It is an estimate from one short test against a public server that needs no account:
// parallel downloads for a few seconds, then parallel uploads, with a hard cap on the bytes moved and a deadline on every
// phase. It asks nothing of the host and keeps nothing.
package speedtest

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// ErrUnreachable means no endpoint gave a usable answer (blocked, down, or the node has no route out).
var ErrUnreachable = errors.New("no speed test server answered")

// Endpoint is one public test server.
type Endpoint struct {
	// Name is what the result says, e.g. "speed.cloudflare.com".
	Name string
	// DownURL answers a GET with a body to read; the stream reads it to the end and asks again until the time is up.
	DownURL string
	// UpURL takes a POST with a body of any size up to a few MB; "" = this server has no upload, the result says 0.
	UpURL string
}

// Config is the whole test. Default() is the production one; tests shrink it and point it at a local server.
type Config struct {
	Endpoints []Endpoint
	// Client overrides the HTTP client (tests). Nil = a client without proxy or HTTP/2: every stream is its own TCP connection.
	Client *http.Client
	// DownStreams and UpStreams run in parallel.
	DownStreams, UpStreams int
	// DownFor and UpFor are the lengths of the two phases; in-flight uploads may finish up to upGrace later.
	DownFor, UpFor time.Duration
	// Warm is the start of the download that does not count: TCP slow start. A run cut short (the cap) counts as a whole.
	Warm time.Duration
	// DownCap and UpCap are hard limits on the bytes moved in a phase. Together they are the 1 GB the admin UI promises.
	DownCap, UpCap int64
	// Overall ends everything, whatever the endpoints do.
	Overall time.Duration
}

// Result is what the panel gets.
type Result struct {
	// Mbps are megabits per second (10^6 bit/s), the unit of node.bandwidth_mbps. UpMbps is 0 when it could not be measured.
	DownMbps, UpMbps float64
	Server           string
	DownStreams      int
	DownBytes        int64
	UpBytes          int64
	Seconds          float64
}

const (
	userAgent = "mistgate-node bandwidth-test"
	// minWindow is the shortest stretch after the warm-up that a rate is taken from; a shorter one falls back to the whole run.
	minWindow = 500 * time.Millisecond
	upGrace   = 2 * time.Second
	// A stream gives up after this many requests in a row that failed or returned nothing.
	maxFails = 3
	// Upload requests start small, so a slow link finishes some within the phase, and double up to maxUpChunk.
	firstUpChunk = 256 << 10
	maxUpChunk   = 8 << 20
)

// Default is the production test: 6 streams down for 6 s (the first second is not counted), 4 up for 3 s, 700 MB down and
// 300 MB up at most, 40 s in all. Cloudflare answers any size up to 50 MB per request and takes uploads; the two fallbacks
// are plain files for a node that cannot reach it.
func Default() Config {
	return Config{
		Endpoints: []Endpoint{
			{Name: "speed.cloudflare.com", DownURL: "https://speed.cloudflare.com/__down?bytes=25000000", UpURL: "https://speed.cloudflare.com/__up"},
			{Name: "proof.ovh.net", DownURL: "https://proof.ovh.net/files/1Gb.dat"},
			{Name: "cachefly.net", DownURL: "https://cachefly.cachefly.net/100mb.test"},
		},
		DownStreams: 6, UpStreams: 4,
		DownFor: 6 * time.Second, UpFor: 3 * time.Second, Warm: time.Second,
		DownCap: 700_000_000, UpCap: 300_000_000,
		Overall: 40 * time.Second,
	}
}

func (c Config) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:                 nil, // the node's own route, not whatever the environment says
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		MaxIdleConnsPerHost:   max(c.DownStreams, c.UpStreams),
		DisableCompression:    true,
		// A non-nil empty map turns HTTP/2 off: it would carry every stream over one connection and measure one flow.
		TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
		IdleConnTimeout: 10 * time.Second,
	}}
}

// Run measures with the first endpoint that works: its download, then its upload when it has one. A failed upload does
// not fail the run (UpMbps stays 0); a download that moved no byte moves on to the next endpoint.
func Run(ctx context.Context, cfg Config) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.Overall)
	defer cancel()
	cl := cfg.client()
	if t, ok := cl.Transport.(*http.Transport); ok && cfg.Client == nil {
		defer t.CloseIdleConnections()
	}
	start := time.Now()
	var last error
	for _, ep := range cfg.Endpoints {
		if ctx.Err() != nil {
			break
		}
		down, err := download(ctx, cl, cfg, ep)
		if err != nil {
			last = fmt.Errorf("%s: %w", ep.Name, err)
			continue
		}
		res := Result{Server: ep.Name, DownMbps: down.mbps, DownStreams: cfg.DownStreams, DownBytes: down.bytes}
		if ep.UpURL != "" && ctx.Err() == nil {
			if up, err := upload(ctx, cl, cfg, ep); err == nil {
				res.UpMbps, res.UpBytes = up.mbps, up.bytes
			}
		}
		res.Seconds = time.Since(start).Seconds()
		return res, nil
	}
	if last == nil {
		last = ctx.Err()
	}
	return Result{}, fmt.Errorf("%w: %v", ErrUnreachable, last)
}

type phase struct {
	bytes int64
	mbps  float64
}

func mbps(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

// download reads from the endpoint on DownStreams connections until DownFor is over or DownCap bytes have come.
func download(ctx context.Context, cl *http.Client, cfg Config, ep Endpoint) (phase, error) {
	dctx, stop := context.WithTimeout(ctx, cfg.DownFor)
	defer stop()
	var n, warmN, warmAt atomic.Int64 // warmAt: nanoseconds after the start when the warm-up ended, 0 = not yet
	start := time.Now()
	markWarm := func() { warmN.Store(n.Load()); warmAt.Store(int64(time.Since(start))) }
	if cfg.Warm > 0 {
		t := time.AfterFunc(cfg.Warm, markWarm)
		defer t.Stop()
	}
	var lastErr atomic.Value
	var wg sync.WaitGroup
	for range cfg.DownStreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fails := 0; dctx.Err() == nil && fails < maxFails; {
				got, err := get(dctx, cl, ep.DownURL, &n, cfg.DownCap, stop)
				if got == 0 {
					fails++
					if err != nil && dctx.Err() == nil {
						lastErr.Store(err.Error())
					}
					sleep(dctx, 200*time.Millisecond)
					continue
				}
				fails = 0
			}
		}()
	}
	wg.Wait()
	end := time.Since(start)
	total := n.Load()
	if total == 0 {
		msg, _ := lastErr.Load().(string)
		if msg == "" {
			msg = "no data"
		}
		return phase{}, errors.New(msg)
	}
	b, d := total, end
	if at := time.Duration(warmAt.Load()); at > 0 && end-at >= minWindow {
		b, d = total-warmN.Load(), end-at
	}
	return phase{bytes: total, mbps: mbps(b, d)}, nil
}

// get reads one response to the end (or until ctx ends) and adds what came to n; it asks for the phase to stop at the cap.
func get(ctx context.Context, cl *http.Client, url string, n *atomic.Int64, limit int64, stop func()) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := cl.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("http %d", resp.StatusCode)
	}
	buf := make([]byte, 64<<10)
	var got int64
	for {
		k, err := resp.Body.Read(buf)
		if k > 0 {
			got += int64(k)
			if n.Add(int64(k)) >= limit {
				stop()
			}
		}
		if errors.Is(err, io.EOF) {
			return got, nil
		}
		if err != nil {
			return got, err
		}
	}
}

// upload POSTs zeros on UpStreams connections. Only requests that were answered count: the bytes a socket has accepted
// are not the bytes that left (the send buffers hide seconds of a slow link), so no new request starts after UpFor and the
// ones in flight may finish within upGrace.
func upload(ctx context.Context, cl *http.Client, cfg Config, ep Endpoint) (phase, error) {
	uctx, stop := context.WithTimeout(ctx, cfg.UpFor)
	defer stop()
	rctx, cancel := context.WithTimeout(ctx, cfg.UpFor+upGrace)
	defer cancel()
	zeros := make([]byte, maxUpChunk)
	var n atomic.Int64
	start := time.Now()
	var wg sync.WaitGroup
	for range cfg.UpStreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunk := firstUpChunk
			for fails := 0; uctx.Err() == nil && fails < maxFails; {
				if err := post(rctx, cl, ep.UpURL, zeros[:chunk]); err != nil {
					fails++
					sleep(uctx, 200*time.Millisecond)
					continue
				}
				fails = 0
				if n.Add(int64(chunk)) >= cfg.UpCap {
					stop()
				}
				chunk = min(chunk*2, maxUpChunk)
			}
		}()
	}
	wg.Wait()
	total := n.Load()
	if total == 0 {
		return phase{}, errors.New("no upload")
	}
	return phase{bytes: total, mbps: mbps(total, time.Since(start))}, nil
}

func post(ctx context.Context, cl *http.Client, url string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
