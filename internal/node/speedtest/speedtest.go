// Package speedtest measures how fast a node reaches the internet, for the panel's "network capacity" suggestion
// (agent.proto "BANDWIDTH TEST"). It is an estimate from a few short tests against a public server that needs no account:
// three runs back to back, each with parallel downloads for a few seconds and then parallel uploads, the best run per
// direction wins; a hard cap on the bytes moved and a deadline on every phase. In the counted window of every run the
// node's main network interface is read too, so the traffic of the people already using the node counts as part of the
// capacity instead of being lost from it. It asks nothing of the host and keeps nothing.
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

// Counters reads the byte counters of the node's main network interface: its name (a changed name between two reads
// means the route moved and the pair is useless), bytes received and bytes sent since boot. ok = false when it cannot.
type Counters func() (iface string, rx, tx uint64, ok bool)

// Config is the whole test. Default() is the production one; tests shrink it and point it at a local server.
type Config struct {
	Endpoints []Endpoint
	// Client overrides the HTTP client (tests). Nil = a client without proxy or HTTP/2: every stream is its own TCP connection.
	Client *http.Client
	// Runs of download + upload one after another (at least 1), Pause between them.
	Runs  int
	Pause time.Duration
	// DownStreams and UpStreams run in parallel.
	DownStreams, UpStreams int
	// DownFor and UpFor are the lengths of the two phases of a run; in-flight uploads may finish up to upGrace later.
	DownFor, UpFor time.Duration
	// Warm is the start of the download that does not count: TCP slow start. A run cut short (the cap) counts as a whole.
	Warm time.Duration
	// DownCap and UpCap are hard limits on the bytes moved in a phase of one run. Runs times both is the cap of a click.
	DownCap, UpCap int64
	// Overall ends everything, whatever the endpoints do. A run is not started when what is left would not fit it.
	Overall time.Duration
	// Counters is the host's interface counters; nil = the test alone is measured.
	Counters Counters

	beforeRun func(run int) // tests: called before each run, 0-based
}

// Result is what the panel gets: the best run per direction.
type Result struct {
	// Mbps are megabits per second (10^6 bit/s), the unit of node.bandwidth_mbps. Each is the better of what the test alone
	// moved and what the interface carried in the same window, so people's traffic is part of it. UpMbps is 0 when it
	// could not be measured.
	DownMbps, UpMbps float64
	// PeopleDownMbps and PeopleUpMbps are what the interface carried beyond the test in the best run of that direction
	// (traffic of people using the node), 0 when it was not more than the test's own headers, or there are no counters.
	PeopleDownMbps, PeopleUpMbps float64
	Server                       string
	DownStreams                  int
	// Runs that gave a result.
	Runs      int
	DownBytes int64 // all runs together
	UpBytes   int64
	Seconds   float64
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
	maxUpChunk   = 4 << 20
	// wireOverhead: the interface counts every byte on the wire (IP, TCP and TLS headers, ACKs), the test only the payload.
	// What the interface carried beyond the payload times this is people's traffic; the rest is the test's own overhead.
	wireOverhead = 1.05
)

// Default is the production test: three runs of 6 streams down for 5 s (the first second is not counted) and 4 up for 2 s,
// a second apart, at most 400 MB down and 100 MB up per run (1.5 GB in all), 45 s in all. Cloudflare answers any size up to
// 50 MB per request (50 MB a request keeps the number of requests low: it rate-limits an address with 429) and takes uploads; the two fallbacks are plain files for a node that cannot reach it. counters may be nil.
func Default(counters Counters) Config {
	return Config{
		Endpoints: []Endpoint{
			{Name: "speed.cloudflare.com", DownURL: "https://speed.cloudflare.com/__down?bytes=50000000", UpURL: "https://speed.cloudflare.com/__up"},
			{Name: "proof.ovh.net", DownURL: "https://proof.ovh.net/files/1Gb.dat"},
			{Name: "cachefly.net", DownURL: "https://cachefly.cachefly.net/100mb.test"},
		},
		Runs: 3, Pause: time.Second,
		DownStreams: 6, UpStreams: 4,
		DownFor: 5 * time.Second, UpFor: 2 * time.Second, Warm: time.Second,
		DownCap: 400_000_000, UpCap: 100_000_000,
		Overall:  45 * time.Second,
		Counters: counters,
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

// run is one download + upload.
type run struct {
	down, up             float64 // the estimates: test or interface, the larger
	peopleDown, peopleUp float64
	downBytes, upBytes   int64
}

// Run measures Runs times with the first endpoint that works for the first run, and keeps the best run per direction. A
// failed upload does not fail a run (its up stays 0); a download that moved no byte moves the first run on to the next
// endpoint; a later run that fails, or does not fit in what is left of Overall, is skipped. It fails only when no run worked.
func Run(ctx context.Context, cfg Config) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.Overall)
	defer cancel()
	cl := cfg.client()
	if t, ok := cl.Transport.(*http.Transport); ok && cfg.Client == nil {
		defer t.CloseIdleConnections()
	}
	start := time.Now()
	res := Result{DownStreams: cfg.DownStreams}
	var best struct{ down, up run }
	var chosen *Endpoint
	var last error
	one := func(ep Endpoint, i int) (run, error) {
		if cfg.beforeRun != nil {
			cfg.beforeRun(i)
		}
		return doRun(ctx, cl, cfg, ep)
	}
	take := func(r run) {
		res.Runs++
		res.DownBytes += r.downBytes
		res.UpBytes += r.upBytes
		if res.Runs == 1 || r.down > best.down.down {
			best.down = r
		}
		if r.up > best.up.up {
			best.up = r
		}
	}
	need := cfg.DownFor + cfg.UpFor + upGrace
	for i := range max(cfg.Runs, 1) {
		if i > 0 {
			sleep(ctx, cfg.Pause)
			if d, ok := ctx.Deadline(); (ok && time.Until(d) < need) || ctx.Err() != nil {
				break
			}
		}
		if chosen != nil {
			if r, err := one(*chosen, i); err == nil {
				take(r)
			} else {
				last = fmt.Errorf("%s: %w", chosen.Name, err)
			}
			continue
		}
		for _, ep := range cfg.Endpoints {
			if ctx.Err() != nil {
				break
			}
			r, err := one(ep, i)
			if err != nil {
				last = fmt.Errorf("%s: %w", ep.Name, err)
				continue
			}
			take(r)
			chosen = &ep
			break
		}
		if chosen == nil {
			break // nothing answers: more runs would only repeat it
		}
	}
	if chosen == nil {
		if last == nil {
			last = ctx.Err()
		}
		return Result{}, fmt.Errorf("%w: %v", ErrUnreachable, last)
	}
	res.Server = chosen.Name
	res.DownMbps, res.PeopleDownMbps = best.down.down, best.down.peopleDown
	res.UpMbps, res.PeopleUpMbps = best.up.up, best.up.peopleUp
	res.Seconds = time.Since(start).Seconds()
	return res, nil
}

// doRun is one download, then (when the endpoint takes one) one upload.
func doRun(ctx context.Context, cl *http.Client, cfg Config, ep Endpoint) (run, error) {
	down, err := download(ctx, cl, cfg, ep)
	if err != nil {
		return run{}, err
	}
	r := run{downBytes: down.bytes}
	r.down, r.peopleDown = combine(down)
	if ep.UpURL != "" && ctx.Err() == nil {
		if up, err := upload(ctx, cl, cfg, ep); err == nil {
			r.upBytes = up.bytes
			r.up, r.peopleUp = combine(up)
		}
	}
	return r, nil
}

// combine takes the better of the test's rate and the interface's in the same window, and what the interface carried beyond
// the test (with room for the test's own headers).
func combine(p phase) (est, people float64) {
	return max(p.mbps, p.nic), max(0, p.nic-p.mbps*wireOverhead)
}

// phase is one direction of one run: the test's payload rate, and the interface's rate in the same window (0 = not known).
type phase struct {
	bytes int64
	mbps  float64
	nic   float64
}

func mbps(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

// nicSample is one read of the counters.
type nicSample struct {
	iface  string
	rx, tx uint64
	ok     bool
}

func (c Config) sample() nicSample {
	if c.Counters == nil {
		return nicSample{}
	}
	i, rx, tx, ok := c.Counters()
	return nicSample{iface: i, rx: rx, tx: tx, ok: ok}
}

// nicRate is the interface's rate between two reads in Mbps: received bytes for a download, sent bytes for an upload. 0
// when either read failed, the interface changed or a counter went back.
func nicRate(a, b nicSample, d time.Duration, received bool) float64 {
	if !a.ok || !b.ok || a.iface != b.iface {
		return 0
	}
	from, to := a.tx, b.tx
	if received {
		from, to = a.rx, b.rx
	}
	if to < from {
		return 0
	}
	return mbps(int64(to-from), d)
}

// download reads from the endpoint on DownStreams connections until DownFor is over or DownCap bytes have come.
func download(ctx context.Context, cl *http.Client, cfg Config, ep Endpoint) (phase, error) {
	dctx, stop := context.WithTimeout(ctx, cfg.DownFor)
	defer stop()
	var n atomic.Int64
	var mu sync.Mutex // guards the warm-up mark
	var warmN int64
	var warmAt time.Duration // after the start, 0 = not yet
	var warmNIC nicSample
	startNIC := cfg.sample()
	start := time.Now()
	if cfg.Warm > 0 {
		t := time.AfterFunc(cfg.Warm, func() {
			s := cfg.sample()
			mu.Lock()
			warmN, warmAt, warmNIC = n.Load(), time.Since(start), s
			mu.Unlock()
		})
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
	endNIC := cfg.sample()
	total := n.Load()
	if total == 0 {
		msg, _ := lastErr.Load().(string)
		if msg == "" {
			msg = "no data"
		}
		return phase{}, errors.New(msg)
	}
	mu.Lock()
	defer mu.Unlock()
	b, d, from := total, end, startNIC
	if warmAt > 0 && end-warmAt >= minWindow {
		b, d, from = total-warmN, end-warmAt, warmNIC
	}
	return phase{bytes: total, mbps: mbps(b, d), nic: nicRate(from, endNIC, d, true)}, nil
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
		// the cap reached, by this stream or another: stop reading now, or what the connection has buffered keeps coming
		// after the cancel (a fast link overshot an 8 MB cap to 18 MB)
		if n.Load() >= limit {
			stop()
			return got, nil
		}
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
// ones in flight may finish within upGrace. The whole phase is the window (the interface is read at both ends of it).
func upload(ctx context.Context, cl *http.Client, cfg Config, ep Endpoint) (phase, error) {
	uctx, stop := context.WithTimeout(ctx, cfg.UpFor)
	defer stop()
	rctx, cancel := context.WithTimeout(ctx, cfg.UpFor+upGrace)
	defer cancel()
	zeros := make([]byte, maxUpChunk)
	var n atomic.Int64
	startNIC := cfg.sample()
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
	d := time.Since(start)
	endNIC := cfg.sample()
	total := n.Load()
	if total == 0 {
		return phase{}, errors.New("no upload")
	}
	return phase{bytes: total, mbps: mbps(total, d), nic: nicRate(startNIC, endNIC, d, false)}, nil
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
