// Package speedtest measures how fast a node reaches the internet, for the panel's "network capacity" suggestion
// (agent.proto "BANDWIDTH TEST"). It is an estimate from a few short tests against a public server that needs no account:
// three runs back to back, each with parallel downloads for a few seconds and then parallel uploads, the best run per
// direction wins; a hard cap on the bytes moved and a deadline on every phase. In the counted window of every run the
// node's main network interface is read too, so the traffic of the people already using the node counts as part of the
// capacity instead of being lost from it. The servers are tried in this order: the nearest Ookla (Speedtest.net) servers
// to the node (the public server list is geolocated by the node's address, the two with the lowest latency are used), then
// Cloudflare, OVH and CacheFly. The result says how many runs worked and why the others did not. It asks nothing of the
// host and keeps nothing.
package speedtest

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ErrUnreachable means no endpoint gave a usable answer (blocked, down, or the node has no route out).
var ErrUnreachable = errors.New("no speed test server answered")

// Reasons a run did not work (Result.Failures); the panel passes them on and the UI words them. http_<code> is any other
// status the server answered.
const (
	ReasonRateLimited = "rate_limited" // HTTP 429 that did not clear within the phase
	ReasonTimeout     = "timeout"      // no data before the deadline, or no time was left to start the run
	ReasonUnreachable = "unreachable"  // refused, reset, DNS or TLS failure, an empty answer
)

// statusError is a non-2xx answer; wait is the server's Retry-After (0 = none).
type statusError struct {
	code int
	wait time.Duration
}

func (e *statusError) Error() string { return "http " + strconv.Itoa(e.code) }

func statusOf(resp *http.Response) error {
	e := &statusError{code: resp.StatusCode}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		e.wait = time.Duration(s) * time.Second
	}
	return e
}

func limited(err error) bool {
	var se *statusError
	return errors.As(err, &se) && se.code == http.StatusTooManyRequests
}

// reason is the short code for why a run failed.
func reason(err error) string {
	var se *statusError
	var ne net.Error
	switch {
	case limited(err):
		return ReasonRateLimited
	case errors.As(err, &se):
		return "http_" + strconv.Itoa(se.code)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return ReasonTimeout
	}
	return ReasonUnreachable
}

// Endpoint is one public test server.
type Endpoint struct {
	// Name is what the result says, e.g. "speed.cloudflare.com" or "Ookla".
	Name string
	// Detail says which server of the provider, e.g. the sponsor and city of an Ookla server; "" = the name is enough.
	Detail string
	// DownURL answers a GET with a body to read; the stream reads it to the end and asks again until the time is up.
	DownURL string
	// UpURL takes a POST with a body of any size up to a few MB; "" = this server has no upload, the result says 0.
	UpURL string
	// Pause between runs on this server; 0 = Config.Pause. A server that rate-limits is given more room.
	Pause time.Duration
	ookla bool // only Ookla endpoints use the dial-time public-address check and redirect policy
}

// Counters reads the byte counters of the node's main network interface: its name (a changed name between two reads
// means the route moved and the pair is useless), bytes received and bytes sent since boot. ok = false when it cannot.
type Counters func() (iface string, rx, tx uint64, ok bool)

// Config is the whole test. Default() is the production one; tests shrink it and point it at a local server.
type Config struct {
	// OoklaList is the URL of the public Speedtest.net server list (JSON, geolocated by the requester's address); "" = no
	// Ookla. The nearest servers by latency go before Endpoints; when the list or every server fails, Endpoints alone are used.
	OoklaList string
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

	beforeRun        func(run int)       // tests: called before each run, 0-based
	ooklaScheme      string              // tests: "http" for a local server; "" = https
	ooklaLocal       bool                // tests: the listed servers are on this machine (loopback), not to be refused
	ooklaDialAddress func(string) string // tests: map a listed address before dialing, to model DNS rebinding
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
	// Detail is the sponsor and city of an Ookla server ("" for the others).
	Detail      string
	DownStreams int
	// Runs that gave a result, of RunsTotal asked for; Failures has one reason code (Reason*, http_<code>) per run that did not.
	Runs, RunsTotal int
	Failures        []string
	DownBytes       int64 // all runs together
	UpBytes         int64
	Seconds         float64
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

	// A stream that gets 429 waits before asking again (the server's Retry-After, else 0.5 s doubling), at most this long.
	// It does not count as a failure: the phase's own deadline ends it.
	firstBackoff, maxBackoff = 500 * time.Millisecond, 2 * time.Second

	// Ookla: the whole discovery (list, then latency of every server in it) gets ooklaWait; the ooklaUse fastest servers are
	// used, one after another when the first fails. A server's size=N download is capped by what the phase reads.
	ooklaWait    = 6 * time.Second
	ooklaUse     = 2
	ooklaDownURL = "/download?size=100000000"
)

// Default is the production test: three runs of 6 streams down for 5 s (the first second is not counted) and 4 up for 2 s,
// a second apart, at most 400 MB down and 100 MB up per run (1.5 GB in all), 45 s in all. The nearest Ookla servers come first
// (a server in the node's own country answers in milliseconds and is not throttled the way Cloudflare is in some). Then
// Cloudflare, which answers any size up to 50 MB per request (the largest it takes: it rate-limits an address with 429, so
// the runs are 3 s apart and a 429 is waited out) and takes uploads; the two last are plain files for a node that cannot reach
// the others. counters may be nil.
func Default(counters Counters) Config {
	return Config{
		OoklaList: "https://www.speedtest.net/api/js/servers?engine=js&limit=10&https_functional=true",
		Endpoints: []Endpoint{
			{Name: "speed.cloudflare.com", DownURL: "https://speed.cloudflare.com/__down?bytes=50000000", UpURL: "https://speed.cloudflare.com/__up", Pause: 3 * time.Second},
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
	return &http.Client{Transport: c.transport(false, false)}
}

func (c Config) ooklaClient() *http.Client {
	if c.Client != nil && c.ooklaLocal {
		client := *c.Client
		client.CheckRedirect = rejectOoklaRedirect
		return &client
	}
	return &http.Client{Transport: c.transport(true, c.ooklaLocal), CheckRedirect: rejectOoklaRedirect}
}

func (c Config) transport(ookla, allowLocal bool) *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 15 * time.Second}
	if ookla && !allowLocal {
		dialer.Control = rejectNonPublicOoklaAddress
	}
	dial := dialer.DialContext
	if ookla && c.ooklaDialAddress != nil {
		base := dial
		dial = func(ctx context.Context, network, address string) (net.Conn, error) {
			return base(ctx, network, c.ooklaDialAddress(address))
		}
	}
	return &http.Transport{
		Proxy:                 nil, // the node's own route, not whatever the environment says
		DialContext:           dial,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		MaxIdleConnsPerHost:   max(c.DownStreams, c.UpStreams),
		DisableCompression:    true,
		// A non-nil empty map turns HTTP/2 off: it would carry every stream over one connection and measure one flow.
		TLSNextProto:    map[string]func(string, *tls.Conn) http.RoundTripper{},
		IdleConnTimeout: 10 * time.Second,
	}
}

func rejectOoklaRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func rejectNonPublicOoklaAddress(_ string, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("reject Ookla dial address %q: %w", address, err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !publicAddr(ip.WithZone("")) {
		return fmt.Errorf("reject non-public Ookla dial address %q", address)
	}
	return nil
}

// run is one download + upload.
type run struct {
	down, up             float64 // the estimates: test or interface, the larger
	peopleDown, peopleUp float64
	downBytes, upBytes   int64
}

// Run measures Runs times with the first endpoint that works for the first run, and keeps the best run per direction. A
// failed upload does not fail a run (its up stays 0); a download that moved no byte moves the first run on to the next
// endpoint; a later run that fails, or does not fit in what is left of Overall, is skipped and named in Result.Failures. It
// fails only when no run worked.
func Run(ctx context.Context, cfg Config) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.Overall)
	defer cancel()
	cl := cfg.client()
	if cfg.Client == nil {
		defer cl.CloseIdleConnections()
	}
	start := time.Now()
	eps := cfg.Endpoints
	var ooklaCl *http.Client
	if cfg.OoklaList != "" {
		// The list itself comes from speedtest.net over HTTPS with the ordinary client; the servers it names are a third
		// party's answer, so they get the client that refuses non-public addresses at dial time and never follows a redirect.
		ooklaCl = cfg.ooklaClient()
		if cfg.Client == nil || !cfg.ooklaLocal {
			defer ooklaCl.CloseIdleConnections()
		}
		eps = append(cfg.ookla(ctx, cl, ooklaCl), eps...)
	}
	total := max(cfg.Runs, 1)
	res := Result{DownStreams: cfg.DownStreams, RunsTotal: total}
	var best struct{ down, up run }
	var chosen *Endpoint
	var last error
	var failed []string
	one := func(ep Endpoint, i int) (run, error) {
		if cfg.beforeRun != nil {
			cfg.beforeRun(i)
		}
		client := cl
		if ep.ookla {
			client = ooklaCl
		}
		return doRun(ctx, client, cfg, ep)
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
	for i := range total {
		if i > 0 {
			pause := cfg.Pause
			if chosen != nil && chosen.Pause > 0 {
				pause = chosen.Pause
			}
			sleep(ctx, pause)
			if d, ok := ctx.Deadline(); (ok && time.Until(d) < need) || ctx.Err() != nil {
				break
			}
		}
		if chosen != nil {
			if r, err := one(*chosen, i); err == nil {
				take(r)
			} else {
				last = fmt.Errorf("%s: %w", chosen.Name, err)
				failed = append(failed, reason(err))
			}
			continue
		}
		for _, ep := range eps {
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
	for len(failed)+res.Runs < total { // runs that were not started: the time ran out
		failed = append(failed, ReasonTimeout)
	}
	res.Server, res.Detail, res.Failures = chosen.Name, chosen.Detail, failed
	res.DownMbps, res.PeopleDownMbps = best.down.down, best.down.peopleDown
	res.UpMbps, res.PeopleUpMbps = best.up.up, best.up.peopleUp
	res.Seconds = time.Since(start).Seconds()
	return res, nil
}

// ookla returns the ooklaUse Ookla servers with the lowest latency from the node, fastest first; nil when the list cannot
// be had or no server in it answers (the caller then goes on with its own endpoints).
func (c Config) ookla(ctx context.Context, listClient, serverClient *http.Client) []Endpoint {
	ctx, cancel := context.WithTimeout(ctx, ooklaWait)
	defer cancel()
	servers, err := ooklaServers(ctx, listClient, c.OoklaList)
	if err != nil {
		return nil
	}
	base := cmp.Or(c.ooklaScheme, "https") + "://"
	type cand struct {
		ep  Endpoint
		rtt time.Duration // 0 = did not answer
	}
	cands := make([]cand, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		cands[i].ep = Endpoint{
			Name: "Ookla", Detail: s.label(), ookla: true,
			DownURL: base + s.Host + ooklaDownURL, UpURL: base + s.Host + "/upload",
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			cands[i].rtt = latency(ctx, serverClient, base+s.Host+"/hello")
		}()
	}
	wg.Wait()
	cands = slices.DeleteFunc(cands, func(c cand) bool { return c.rtt == 0 })
	slices.SortStableFunc(cands, func(a, b cand) int { return cmp.Compare(a.rtt, b.rtt) })
	var out []Endpoint
	for _, c := range cands[:min(len(cands), ooklaUse)] {
		out = append(out, c.ep)
	}
	return out
}

// ooklaServer is what the public list says about one server.
type ooklaServer struct {
	Host    string `json:"host"` // host:port, which serves HTTPS too
	Sponsor string `json:"sponsor"`
	Name    string `json:"name"` // the city
}

func (s ooklaServer) label() string {
	return strings.Join(slices.DeleteFunc([]string{strings.TrimSpace(s.Sponsor), strings.TrimSpace(s.Name)}, func(p string) bool { return p == "" }), ", ")
}

// ooklaServers reads the list; entries without a plain host:port are dropped.
func ooklaServers(ctx context.Context, cl *http.Client, listURL string) ([]ooklaServer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := cl.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusOf(resp)
	}
	var all []ooklaServer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&all); err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(s ooklaServer) bool {
		u, err := url.Parse("//" + s.Host)
		return err != nil || s.Host == "" || u.Host != s.Host || u.Path != "" || u.User != nil
	}), nil
}

// latency is the better of two GETs of u (the second one on the connection the first opened), 0 when it did not answer 200.
func latency(ctx context.Context, cl *http.Client, u string) time.Duration {
	var best time.Duration
	for range 2 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return 0
		}
		req.Header.Set("User-Agent", userAgent)
		t0 := time.Now()
		resp, err := cl.Do(req)
		if err != nil {
			return 0
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<12))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return 0
		}
		if d := max(time.Since(t0), time.Microsecond); best == 0 || d < best {
			best = d
		}
	}
	return best
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
	var lastErr error // a 429 stays: it says more than what came after it
	var errMu sync.Mutex
	var wg sync.WaitGroup
	for range cfg.DownStreams {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var back time.Duration
			for fails := 0; dctx.Err() == nil && fails < maxFails; {
				got, err := get(dctx, cl, ep.DownURL, &n, cfg.DownCap, stop)
				if got == 0 {
					if err != nil && dctx.Err() == nil {
						errMu.Lock()
						if lastErr == nil || !limited(lastErr) {
							lastErr = err
						}
						errMu.Unlock()
					}
					if limited(err) { // wait it out; the phase's deadline is the limit
						back = nextBackoff(back, err)
						sleep(dctx, back)
						continue
					}
					fails++
					sleep(dctx, 200*time.Millisecond)
					continue
				}
				fails, back = 0, 0
			}
		}()
	}
	wg.Wait()
	end := time.Since(start)
	endNIC := cfg.sample()
	total := n.Load()
	if total == 0 {
		if lastErr == nil { // nothing came and nothing failed: the server held the answer until the phase ended
			lastErr = fmt.Errorf("no data: %w", context.DeadlineExceeded)
		}
		return phase{}, lastErr
	}
	mu.Lock()
	defer mu.Unlock()
	b, d, from := total, end, startNIC
	if warmAt > 0 && end-warmAt >= minWindow {
		b, d, from = total-warmN, end-warmAt, warmNIC
	}
	return phase{bytes: total, mbps: mbps(b, d), nic: nicRate(from, endNIC, d, true)}, nil
}

// nextBackoff is how long a stream waits after a 429: the server's Retry-After when it gave one, else double the last wait,
// never more than maxBackoff (a phase lasts a few seconds).
func nextBackoff(prev time.Duration, err error) time.Duration {
	var se *statusError
	if errors.As(err, &se) && se.wait > 0 {
		return min(se.wait, maxBackoff)
	}
	if prev == 0 {
		return firstBackoff
	}
	return min(prev*2, maxBackoff)
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
		return 0, statusOf(resp)
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
			var back time.Duration
			for fails := 0; uctx.Err() == nil && fails < maxFails; {
				if err := post(rctx, cl, ep.UpURL, zeros[:chunk]); err != nil {
					if limited(err) {
						back = nextBackoff(back, err)
						sleep(uctx, back)
						continue
					}
					fails++
					sleep(uctx, 200*time.Millisecond)
					continue
				}
				fails, back = 0, 0
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
		return statusOf(resp)
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

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// publicAddr is an address the test may send bytes to: a global unicast one that is not private (RFC 1918, fc00::/7) or
// shared address space. Loopback, link-local, unspecified, multicast and broadcast are not global unicast.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsGlobalUnicast() && !a.IsPrivate() && !cgnat.Contains(a)
}
