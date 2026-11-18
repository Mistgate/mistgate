package warp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// readReachTestdata parses a stored raw HTTP response the way reachGet reads a live one.
func readReachTestdata(t *testing.T, name string) (int, http.Header, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "reach", name))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(raw)), nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, reachBodyLimit))
	return resp.StatusCode, resp.Header, body
}

// The table of block pages: the shapes of real answers, kept by hand in testdata/reach (no network in a test).
func TestClassifyReachOnStoredPages(t *testing.T) {
	for file, want := range map[string]string{
		"cloudflare_1020_access_denied.http": ReachBlocked,
		"cloudflare_1009_region.http":        ReachBlocked,
		"cloudflare_1006_ip_banned.http":     ReachBlocked,
		"cloudflare_sorry_blocked.http":      ReachBlocked,
		"akamai_access_denied.http":          ReachBlocked,
		"cloudfront_geo.http":                ReachBlocked,
		"region_text_403.http":               ReachBlocked,
		"legal_451.http":                     ReachBlocked,
		"cloudflare_challenge.http":          ReachError, // a challenge asks for a person, it does not refuse one
		"cloudflare_1015_rate_limited.http":  ReachError, // 429, a rate limit
		"plain_403_nginx.http":               ReachError, // a bare 403 is a site's own filter as likely as WARP's colo
		"server_503.http":                    ReachError,
		"ok_200_home.http":                   ReachOK,
		"ok_200_article_about_blocks.http":   ReachOK, // talks about the pages, is not one
		"redirect_301.http":                  ReachOK,
	} {
		status, header, body := readReachTestdata(t, file)
		if got := classifyReach(status, header, body); got != want {
			t.Errorf("%s: %q, want %q", file, got, want)
		}
	}
}

// Each pattern of a rule is needed: Cloudflare's code without Cloudflare's name, or Akamai's words without its reference, are
// not a block page.
func TestClassifyReachNeedsEveryPatternOfARule(t *testing.T) {
	for name, body := range map[string]string{
		"code without the name": `<h1>Error <span class="cf-error-code">1020</span></h1>`,
		"name without the code": `<p>Cloudflare Ray ID: abc</p><h1>Access denied</h1>`,
		"akamai without a ref":  `<h1>Access Denied</h1>You don't have permission to access "/" on this server.`,
		"other 1xxx code":       `<p>Cloudflare</p><span class="cf-error-code">1033</span>`,
		"empty":                 ``,
	} {
		if got := classifyReach(403, http.Header{}, []byte(body)); got != ReachError {
			t.Errorf("%s: %q, want %q", name, got, ReachError)
		}
	}
	// the same words on a page that was served are not a block either
	if got := classifyReach(200, http.Header{}, []byte(`Cloudflare Error 1020 Sorry, you have been blocked`)); got != ReachOK {
		t.Errorf("a 200 with the words: %q", got)
	}
	for status, want := range map[int]string{200: ReachOK, 204: ReachOK, 302: ReachOK, 401: ReachOK, 404: ReachOK, 405: ReachOK,
		429: ReachError, 500: ReachError, 502: ReachError, 451: ReachBlocked} {
		if got := classifyReach(status, http.Header{}, nil); got != want {
			t.Errorf("status %d: %q, want %q", status, got, want)
		}
	}
}

// reachSite is a loopback HTTPS server standing for every domain: the Host header says which site is asked.
type reachSite struct {
	srv   *httptest.Server
	mu    sync.Mutex
	hosts []string
}

func newReachSite(t *testing.T, pages map[string]func(w http.ResponseWriter, r *http.Request)) *reachSite {
	t.Helper()
	s := &reachSite{}
	s.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.hosts = append(s.hosts, r.Host)
		s.mu.Unlock()
		if h, ok := pages[r.Host]; ok {
			h(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func servePage(t *testing.T, file string) func(http.ResponseWriter, *http.Request) {
	status, header, body := readReachTestdata(t, file)
	return func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range header {
			w.Header()[k] = v
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}
}

// upRig is a manager whose tunnel is up and whose requests go to site, whatever the name.
func upRig(t *testing.T, site *reachSite) *rig {
	t.Helper()
	r := newRig(t)
	if err := r.m.Apply(context.Background(), spec()); err != nil {
		t.Fatal(err)
	}
	r.m.mu.Lock()
	r.m.state = StateUp
	r.m.mu.Unlock()
	addr := site.srv.Listener.Addr().String()
	r.m.reachDial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	r.m.reachTLS = &tls.Config{InsecureSkipVerify: true}
	return r
}

func TestReachReportsEachDomainAndTheColo(t *testing.T) {
	site := newReachSite(t, map[string]func(http.ResponseWriter, *http.Request){
		"de1.example.com":   servePage(t, "ok_200_home.http"),
		"nl1.example.com":   servePage(t, "cloudflare_1009_region.http"),
		"plain.example.com": servePage(t, "plain_403_nginx.http"),
		"down.example.com":  servePage(t, "server_503.http"),
	})
	r := upRig(t, site)
	rep, err := r.m.Reach(context.Background(), []string{"de1.example.com", "nl1.example.com", "plain.example.com", "down.example.com", "gone.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Colo != "FRA" {
		t.Errorf("colo %q, want FRA from the trace", rep.Colo)
	}
	want := []ReachItem{{ReachOK, 200, 0}, {ReachBlocked, 403, 0}, {ReachError, 403, 0}, {ReachError, 503, 0}, {ReachOK, 404, 0}}
	if len(rep.Items) != len(want) {
		t.Fatalf("items %+v", rep.Items)
	}
	for i, it := range rep.Items {
		if it.Verdict != want[i].Verdict || it.Code != want[i].Code {
			t.Errorf("domain %d: %+v, want %s %d", i, it, want[i].Verdict, want[i].Code)
		}
	}
}

// A redirect is followed once, to a page that is then judged; a second one is not followed and counts as served.
func TestReachFollowsOneRedirect(t *testing.T) {
	redirect := func(to string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, to, http.StatusFound) }
	}
	site := newReachSite(t, map[string]func(http.ResponseWriter, *http.Request){
		"one.example.com":     redirect("https://www.one.example.com/"),
		"www.one.example.com": servePage(t, "cloudflare_1020_access_denied.http"),
		"two.example.com":     redirect("https://a.two.example.com/"),
		"a.two.example.com":   redirect("https://b.two.example.com/"),
		"b.two.example.com":   servePage(t, "cloudflare_1020_access_denied.http"),
		"plain.example.com":   redirect("http://insecure.example.com/"),
	})
	r := upRig(t, site)
	rep, err := r.m.Reach(context.Background(), []string{"one.example.com", "two.example.com", "plain.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if it := rep.Items[0]; it.Verdict != ReachBlocked || it.Code != 403 {
		t.Errorf("one redirect to a block page: %+v", it)
	}
	if it := rep.Items[1]; it.Verdict != ReachOK || it.Code != 302 {
		t.Errorf("two redirects stop at the second: %+v", it)
	}
	if it := rep.Items[2]; it.Verdict != ReachOK || it.Code != 302 {
		t.Errorf("a redirect to plain http is not followed: %+v", it)
	}
	site.mu.Lock()
	defer site.mu.Unlock()
	for _, h := range site.hosts {
		if h == "b.two.example.com" || h == "insecure.example.com" {
			t.Errorf("the node followed a redirect it must not: %v", site.hosts)
		}
	}
}

// Only the first 64 KiB of a page are read: a marker after them decides nothing.
func TestReachReadsAtMostTheBodyLimit(t *testing.T) {
	site := newReachSite(t, map[string]func(http.ResponseWriter, *http.Request){
		"big.example.com": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(strings.Repeat("x", reachBodyLimit+100)))
			_, _ = w.Write([]byte(`Cloudflare <span class="cf-error-code">1020</span>`))
		},
	})
	r := upRig(t, site)
	rep, err := r.m.Reach(context.Background(), []string{"big.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if it := rep.Items[0]; it.Verdict != ReachError || it.Code != 403 {
		t.Errorf("a marker past the limit: %+v", it)
	}
}

func TestReachUnreachableWhenNothingAnswers(t *testing.T) {
	site := newReachSite(t, nil)
	r := upRig(t, site)
	r.m.reachDial = func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connection refused") }
	rep, err := r.m.Reach(context.Background(), []string{"de1.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if it := rep.Items[0]; it.Verdict != ReachUnreachable || it.Code != 0 {
		t.Errorf("no answer: %+v", it)
	}
}

func TestReachRefusesWhatItCannotRunSafely(t *testing.T) {
	site := newReachSite(t, nil)
	r := upRig(t, site)
	ctx := context.Background()
	for _, bad := range [][]string{nil, {""}, {"127.0.0.1"}, {"localhost"}, {"de1.example.com:8443"}, {"https://de1.example.com"}, {"de1.example.com/x"},
		{"DE1.example.com"}, {"-a.example.com"}, {"a b.example.com"}, {"de1.example.com", "1.2.3.4"},
		{"1.example.com", "2.example.com", "3.example.com", "4.example.com", "5.example.com", "6.example.com", "7.example.com", "8.example.com", "9.example.com"}} {
		if _, err := r.m.Reach(ctx, bad); !errors.Is(err, ErrReachBadName) {
			t.Errorf("%q: %v, want ErrReachBadName", bad, err)
		}
	}
	site.mu.Lock()
	n := len(site.hosts)
	site.mu.Unlock()
	if n != 0 {
		t.Errorf("a refused list sent %d requests", n)
	}
}

func TestReachNeedsTheTunnelUpAndKeepsTheInterval(t *testing.T) {
	site := newReachSite(t, map[string]func(http.ResponseWriter, *http.Request){"de1.example.com": servePage(t, "ok_200_home.http")})
	r := upRig(t, site)
	ctx := context.Background()
	names := []string{"de1.example.com"}

	r.m.mu.Lock()
	r.m.state = StateDown
	r.m.mu.Unlock()
	if _, err := r.m.Reach(ctx, names); !errors.Is(err, ErrNotActive) {
		t.Fatalf("a tunnel that is down: %v", err)
	}
	r.m.mu.Lock()
	r.m.state = StateUp
	r.m.mu.Unlock()

	if _, err := r.m.Reach(ctx, names); err != nil {
		t.Fatal(err)
	}
	if _, err := r.m.Reach(ctx, names); !errors.Is(err, ErrReachTooSoon) {
		t.Fatalf("a second run at once: %v", err)
	}
	r.clk.add(r.m.o.Interval - time.Second)
	if _, err := r.m.Reach(ctx, names); !errors.Is(err, ErrReachTooSoon) {
		t.Fatalf("a run just under a health interval later: %v", err)
	}
	r.clk.add(2 * time.Second)
	if _, err := r.m.Reach(ctx, names); err != nil {
		t.Fatalf("a run a health interval later: %v", err)
	}
}

func TestReachRunsOneAtATime(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	site := newReachSite(t, map[string]func(http.ResponseWriter, *http.Request){"de1.example.com": func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}})
	r := upRig(t, site)
	done := make(chan error, 1)
	go func() { _, err := r.m.Reach(context.Background(), []string{"de1.example.com"}); done <- err }()
	<-entered
	if _, err := r.m.Reach(context.Background(), []string{"de1.example.com"}); !errors.Is(err, ErrReachBusy) {
		t.Fatalf("a second run while one goes: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// fakeDNS answers A queries for the names in answers (and nothing for AAAA) on a loopback UDP port.
func fakeDNS(t *testing.T, answers map[string]netip.Addr) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			if n < 17 {
				continue
			}
			var name []string
			i := 12
			for i < n && q[i] != 0 {
				name = append(name, string(q[i+1:i+1+int(q[i])]))
				i += int(q[i]) + 1
			}
			end := i + 5 // the root label, type and class
			if end > n {
				continue
			}
			qtype := binary.BigEndian.Uint16(q[i+1:])
			resp := append([]byte(nil), q[:end]...)
			ip, known := answers[strings.Join(name, ".")]
			resp[2], resp[3] = 0x81, 0x80 // a response, recursion available
			resp[6], resp[7], resp[8], resp[9], resp[10], resp[11] = 0, 0, 0, 0, 0, 0
			switch {
			case !known:
				resp[3] |= 3 // NXDOMAIN
			case qtype == 1 && ip.Is4():
				resp[7] = 1
				resp = append(resp, 0xC0, 0x0C, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
				resp = append(resp, ip.AsSlice()...)
			}
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr().String()
}

// A name that resolves to the node's own network reaches nothing: the guard is the dial every request uses, after the
// resolve, whatever the name looked like.
func TestReachRefusesNamesThatResolveInsideTheNode(t *testing.T) {
	dns := fakeDNS(t, map[string]netip.Addr{
		"loop.example.com":    netip.MustParseAddr("127.0.0.1"),
		"private.example.com": netip.MustParseAddr("10.1.2.3"),
		"cgnat.example.com":   netip.MustParseAddr("100.64.0.9"),
		"meta.example.com":    netip.MustParseAddr("169.254.169.254"),
		"doc.example.com":     netip.MustParseAddr("192.0.2.77"),
	})
	r := newRig(t)
	r.m.o.AllowPrivate = false
	r.m.o.DNS = func() []string { return []string{dns} }
	for _, name := range []string{"loop.example.com", "private.example.com", "cgnat.example.com", "meta.example.com"} {
		_, err := r.m.probeDialContext(context.Background(), "tcp", net.JoinHostPort(name, "443"))
		if err == nil || !strings.Contains(err.Error(), "not allowed") {
			t.Errorf("%s: %v, want the destination refused", name, err)
		}
	}
	// the same names through Reach: unreachable, never a connection
	if err := r.m.Apply(context.Background(), spec()); err != nil {
		t.Fatal(err)
	}
	r.m.mu.Lock()
	r.m.state = StateUp
	r.m.mu.Unlock()
	rep, err := r.m.Reach(context.Background(), []string{"loop.example.com", "private.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for i, it := range rep.Items {
		if it.Verdict != ReachUnreachable || it.Code != 0 {
			t.Errorf("domain %d: %+v, want unreachable", i, it)
		}
	}
}
