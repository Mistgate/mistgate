package sniscan

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"sync"
	"testing"
	"time"
)

// Nothing here touches a network: the neighbours are servers in the process on the loopback, reached through the Dial seam
// as addresses of 192.0.2.0/24, the names are answered by the Resolve seam, the certificates come from a test authority
// and the clock is a fake.

var (
	t0   = time.Unix(1_800_000_000, 0)
	node = netip.MustParseAddr("192.0.2.10")
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Sleep moves the clock to the moment asked for; nothing waits.
func (c *fakeClock) Sleep(ctx context.Context, until time.Time) error {
	c.mu.Lock()
	if until.After(c.now) {
		c.now = until
	}
	c.mu.Unlock()
	return ctx.Err()
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test root"},
		NotBefore: t0.Add(-365 * 24 * time.Hour), NotAfter: t0.Add(365 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// issue makes a certificate for names that ends left after t0, signed by ca, or self-signed when ca is nil.
func issue(t *testing.T, ca *testCA, left time.Duration, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "site"}, DNSNames: names,
		NotBefore: t0.Add(-60 * 24 * time.Hour), NotAfter: t0.Add(left),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	parent, signer := tpl, key
	if ca != nil {
		parent, signer = ca.cert, ca.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// site describes a neighbour.
type site struct {
	cert     tls.Certificate
	min, max uint16
	curves   []tls.CurveID
	h2       bool
	status   int    // of GET /, 200 when 0
	location string // Location of a redirect
}

var (
	mlkemOnly = []tls.CurveID{tls.X25519MLKEM768}
	x25519    = []tls.CurveID{tls.X25519}
)

// good is a site as a REALITY target should be.
func good(cert tls.Certificate) site {
	return site{cert: cert, min: tls.VersionTLS13, max: tls.VersionTLS13, curves: mlkemOnly, h2: true}
}

type dialRec struct {
	local, remote netip.Addr
	port          uint16
	at            time.Time
}

// world is the network of a test.
type world struct {
	t     *testing.T
	ca    *testCA
	clock *fakeClock

	mu     sync.Mutex
	sites  map[netip.Addr]string // address -> the server in the process
	dials  []dialRec
	names  map[string][]netip.Addr          // what the resolver says
	onDial func(ctx context.Context, n int) // called with the number of the dial, in the dial's goroutine
}

func newWorld(t *testing.T) *world {
	return &world{t: t, ca: newCA(t), clock: &fakeClock{now: t0}, sites: map[netip.Addr]string{}, names: map[string][]netip.Addr{}}
}

func (w *world) addSite(ip string, s site) {
	w.t.Helper()
	h := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if s.location != "" {
			rw.Header().Set("Location", s.location)
		}
		if s.status != 0 {
			rw.WriteHeader(s.status)
		}
		_, _ = io.WriteString(rw, "a page the scan must not read")
	})
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = s.h2
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: s.min, MaxVersion: s.max, CurvePreferences: s.curves}
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	w.t.Cleanup(srv.Close)
	w.addRaw(ip, srv.Listener.Addr().String())
}

func (w *world) addRaw(ip, addr string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sites[netip.MustParseAddr(ip)] = addr
}

// silent is an address that accepts the connection and never says anything.
func (w *world) silent(ip string) {
	w.t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		w.t.Fatal(err)
	}
	var mu sync.Mutex
	var open []net.Conn
	w.t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, c := range open {
			_ = c.Close()
		}
		mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			open = append(open, c)
			mu.Unlock()
		}
	}()
	w.addRaw(ip, ln.Addr().String())
}

func (w *world) dial(ctx context.Context, local netip.Addr, remote netip.AddrPort) (net.Conn, error) {
	w.mu.Lock()
	w.dials = append(w.dials, dialRec{local: local, remote: remote.Addr(), port: remote.Port(), at: w.clock.Now()})
	n := len(w.dials)
	addr, ok := w.sites[remote.Addr()]
	hook := w.onDial
	w.mu.Unlock()
	if hook != nil {
		hook(ctx, n)
	}
	if !ok {
		return nil, errors.New("connection refused")
	}
	return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
}

func (w *world) resolve(_ context.Context, host string) ([]netip.Addr, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if ips, ok := w.names[host]; ok {
		return ips, nil
	}
	return nil, errors.New("no such host")
}

func (w *world) scanner(self ...string) *Scanner {
	if len(self) == 0 {
		self = []string{node.String()}
	}
	var own []netip.Addr
	for _, s := range self {
		own = append(own, netip.MustParseAddr(s))
	}
	return &Scanner{
		Dial: w.dial, Resolve: w.resolve, Roots: func() (*x509.CertPool, error) { return w.ca.pool, nil },
		Self: func() ([]netip.Addr, error) { return own, nil }, Now: w.clock.Now, Sleep: w.clock.Sleep,
		lim: limits{tls: 2 * time.Second},
	}
}

func (w *world) dialsTo(ip string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, d := range w.dials {
		if d.remote == netip.MustParseAddr(ip) {
			n++
		}
	}
	return n
}

func runScan(t *testing.T, s *Scanner) *Result {
	t.Helper()
	res, err := s.Scan(context.Background(), node)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	return res
}

func TestNoCandidateFromWhatIsNotGood(t *testing.T) {
	for _, tc := range []struct {
		name string
		site func(w *world) site
		want Result // counters only
	}{
		{"TLS 1.2", func(w *world) site {
			return site{cert: issue(t, w.ca, 90*24*time.Hour, "shop.example.com"), min: tls.VersionTLS12, max: tls.VersionTLS12, curves: x25519}
		}, Result{Open: 1}},
		{"TLS 1.3 without X25519MLKEM768", func(w *world) site {
			s := good(issue(t, w.ca, 90*24*time.Hour, "shop.example.com"))
			s.curves = x25519
			return s
		}, Result{Open: 1, TLS13: 1}},
		{"a certificate of a mask only", func(w *world) site {
			return good(issue(t, w.ca, 90*24*time.Hour, "*.example.com"))
		}, Result{Open: 1, TLS13: 1}},
		{"a self-signed certificate", func(w *world) site { return good(issue(t, nil, 90*24*time.Hour, "shop.example.com")) },
			Result{Open: 1, TLS13: 1, MLKEM: 1}},
		{"a certificate of another authority", func(w *world) site {
			return good(issue(t, newCA(t), 90*24*time.Hour, "shop.example.com"))
		}, Result{Open: 1, TLS13: 1, MLKEM: 1}},
		{"a certificate with 13 days left", func(w *world) site {
			return good(issue(t, w.ca, 13*24*time.Hour+time.Hour, "shop.example.com"))
		}, Result{Open: 1, TLS13: 1, MLKEM: 1}},
		{"a certificate that is over", func(w *world) site { return good(issue(t, w.ca, -time.Hour, "shop.example.com")) },
			Result{Open: 1, TLS13: 1, MLKEM: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.addSite("192.0.2.20", tc.site(w))
			res := runScan(t, w.scanner())
			if len(res.Candidates) != 0 || res.Open != tc.want.Open || res.TLS13 != tc.want.TLS13 || res.MLKEM != tc.want.MLKEM || res.Valid != 0 {
				t.Fatalf("result %+v, want counters %+v and no candidate", res, tc.want)
			}
			if res.Addresses != 253 || res.Partial {
				t.Fatalf("addresses %d, partial %v", res.Addresses, res.Partial)
			}
		})
	}
}

func TestGoodSiteIsACandidate(t *testing.T) {
	w := newWorld(t)
	// the fake clock runs a few seconds ahead during a scan (the pace), hence the half day
	w.addSite("192.0.2.20", good(issue(t, w.ca, 90*24*time.Hour+12*time.Hour, "shop.example.com")))
	w.names["shop.example.com"] = []netip.Addr{netip.MustParseAddr("192.0.2.20")}
	res := runScan(t, w.scanner())
	if len(res.Candidates) != 1 {
		t.Fatalf("result %+v", res)
	}
	c := res.Candidates[0]
	if c.IP != "192.0.2.20" || c.Name != "shop.example.com" || c.DNS != DNSHere || !c.H2 || !c.SingleSite || c.HTTP != 200 || c.Days != 90 {
		t.Fatalf("candidate %+v", c)
	}
	if res.Open != 1 || res.TLS13 != 1 || res.MLKEM != 1 || res.Valid != 1 {
		t.Fatalf("counters %+v", res)
	}
	// the node sends from its own address, to port 443, once without a name and once for the name
	if got := w.dialsTo("192.0.2.20"); got != 2 {
		t.Errorf("%d connections to the site, want 2", got)
	}
	for _, d := range w.dials {
		if d.local != node || d.port != 443 {
			t.Fatalf("dial %+v", d)
		}
	}
}

func TestHTTP1SiteAndItsAnswers(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		location string
		code     int
		rank     int
	}{
		{"200", 0, "", 200, 0},
		{"a redirect on the same domain", 301, "https://www.shop.example.com/en", 301, 1},
		{"a relative redirect", 302, "/en/", 302, 1},
		{"a redirect to another domain", 301, "https://elsewhere.example.net/", 301, 3},
		{"a redirect nobody can read", 301, "http://[::1", 301, 3},
		{"403", 403, "", 403, 2},
		{"503", 503, "", 503, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			s := good(issue(t, w.ca, 90*24*time.Hour, "shop.example.com"))
			s.h2, s.status, s.location = false, tc.status, tc.location
			w.addSite("192.0.2.20", s)
			res := runScan(t, w.scanner())
			if len(res.Candidates) != 1 {
				t.Fatalf("result %+v", res)
			}
			if c := res.Candidates[0]; c.H2 || c.HTTP != tc.code || c.httpRank != tc.rank {
				t.Fatalf("candidate %+v, want status %d rank %d on HTTP/1.1", c, tc.code, tc.rank)
			}
		})
	}
}

func TestSiteThatDoesNotAnswerGetStillCounts(t *testing.T) {
	// the TLS handshake is fine, then the connection is cut: the candidate has no HTTP answer and ranks last
	w := newWorld(t)
	cert := issue(t, w.ca, 90*24*time.Hour, "shop.example.com")
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13, CurvePreferences: mlkemOnly})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = c.(*tls.Conn).Handshake(); _ = c.Close() }()
		}
	}()
	w.addRaw("192.0.2.20", ln.Addr().String())
	res := runScan(t, w.scanner())
	if len(res.Candidates) != 1 || res.Candidates[0].HTTP != 0 || res.Candidates[0].httpRank != 3 {
		t.Fatalf("result %+v", res)
	}
}

func TestMaskIsLeftOutAndAtMostTwoNamesAreAsked(t *testing.T) {
	w := newWorld(t)
	w.addSite("192.0.2.20", good(issue(t, w.ca, 90*24*time.Hour, "*.shop.example.com", "shop.example.com", "www.shop.example.com", "m.shop.example.com")))
	res := runScan(t, w.scanner())
	var names []string
	for _, c := range res.Candidates {
		names = append(names, c.Name)
		if !c.SingleSite {
			t.Errorf("a certificate of one domain is not a single site: %+v", c)
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, []string{"shop.example.com", "www.shop.example.com"}) {
		t.Fatalf("names %v", names)
	}
	if got := w.dialsTo("192.0.2.20"); got != 3 { // without a name, and one per name
		t.Fatalf("%d connections, want 3", got)
	}
	if res.Valid != 1 || res.MLKEM != 1 { // addresses, not names
		t.Fatalf("counters %+v", res)
	}
}

func TestSharedCertificateIsNotASingleSite(t *testing.T) {
	w := newWorld(t)
	w.addSite("192.0.2.20", good(issue(t, w.ca, 90*24*time.Hour, "a.example.com", "b.example.net", "c.example.org", "d.example.io", "e.example.dev")))
	res := runScan(t, w.scanner())
	if len(res.Candidates) != 2 {
		t.Fatalf("result %+v", res)
	}
	for _, c := range res.Candidates {
		if c.SingleSite {
			t.Errorf("a certificate of five domains is a single site: %+v", c)
		}
	}
	if _, single := certNames(parse(t, issue(t, w.ca, time.Hour, "example.co.uk", "www.example.co.uk", "api.example.co.uk"))); !single {
		t.Error("one registrable domain under a two-part public suffix is one site")
	}
}

func parse(t *testing.T, c tls.Certificate) *x509.Certificate {
	t.Helper()
	x, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return x
}

func TestDNSAnswers(t *testing.T) {
	w := newWorld(t)
	for i, name := range []string{"here", "subnet", "elsewhere", "none", "mixed"} {
		ip := "192.0.2." + []string{"21", "22", "23", "24", "25"}[i]
		w.addSite(ip, good(issue(t, w.ca, 90*24*time.Hour, name+".example.com")))
	}
	w.names["here.example.com"] = []netip.Addr{netip.MustParseAddr("192.0.2.21")}
	w.names["subnet.example.com"] = []netip.Addr{netip.MustParseAddr("192.0.2.99")}
	w.names["elsewhere.example.com"] = []netip.Addr{netip.MustParseAddr("198.51.100.7")}
	// none.example.com does not resolve; a name with several answers is "here" when one of them is the address
	w.names["mixed.example.com"] = []netip.Addr{netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("192.0.2.25")}
	sc := w.scanner()
	sc.lim.parallel = 1 // with one worker the fake clock does not move during a handshake: every Ms is 0 and the order is by the rules above it
	res := runScan(t, sc)
	got := map[string]string{}
	for _, c := range res.Candidates {
		got[c.Name] = c.DNS
	}
	want := map[string]string{"here.example.com": DNSHere, "subnet.example.com": DNSSubnet, "elsewhere.example.com": DNSElsewhere,
		"none.example.com": DNSNone, "mixed.example.com": DNSHere}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: dns %q, want %q", k, got[k], v)
		}
	}
	// and the order: here first (mixed and here tie on everything else, so by address), none last
	var order []string
	for _, c := range res.Candidates {
		order = append(order, c.Name)
	}
	if !slices.Equal(order, []string{"here.example.com", "mixed.example.com", "subnet.example.com", "elsewhere.example.com", "none.example.com"}) {
		t.Fatalf("order %v", order)
	}
}

func TestRankOrder(t *testing.T) {
	// each line differs from the next in the one thing named, and the earlier rule wins over every later one
	c := func(dns string, h2, single bool, rank, ms int, ip string) Candidate {
		return Candidate{IP: ip, Name: "x" + ip + ".example.com", DNS: dns, H2: h2, SingleSite: single, httpRank: rank, Ms: ms}
	}
	want := []Candidate{
		c(DNSHere, true, true, 0, 50, "192.0.2.1"),
		c(DNSHere, true, true, 0, 90, "192.0.2.2"),     // slower handshake
		c(DNSHere, true, true, 1, 5, "192.0.2.3"),      // 3xx on the same domain
		c(DNSHere, true, true, 2, 5, "192.0.2.4"),      // 4xx
		c(DNSHere, true, true, 3, 5, "192.0.2.5"),      // 5xx, away or nothing
		c(DNSHere, true, false, 0, 1, "192.0.2.6"),     // a shared certificate
		c(DNSHere, false, true, 0, 1, "192.0.2.7"),     // no HTTP/2
		c(DNSSubnet, true, true, 0, 1, "192.0.2.8"),    // the name leads to the subnet
		c(DNSElsewhere, true, true, 0, 1, "192.0.2.9"), // elsewhere
		c(DNSNone, true, true, 0, 1, "192.0.2.10"),     // nowhere
		c(DNSNone, true, true, 0, 1, "192.0.2.11"),     // equal: by address
	}
	got := slices.Clone(want)
	slices.Reverse(got)
	got = rank(got)
	for i := range want {
		if got[i].IP != want[i].IP {
			t.Fatalf("position %d is %s, want %s", i, got[i].IP, want[i].IP)
		}
	}
	// at most 20 are kept
	var many []Candidate
	for i := range 30 {
		many = append(many, c(DNSHere, true, true, 0, i, "192.0.2.1"))
	}
	if n := len(rank(many)); n != MaxCandidates {
		t.Fatalf("%d candidates kept, want %d", n, MaxCandidates)
	}
}

func TestOwnAddressesAndTheEdgesAreLeftOut(t *testing.T) {
	w := newWorld(t)
	res := runScan(t, w.scanner("192.0.2.10", "192.0.2.77", "198.51.100.5"))
	if res.Addresses != 252 { // 254 minus the two own addresses of this /24
		t.Fatalf("%d addresses, want 252", res.Addresses)
	}
	seen := map[netip.Addr]bool{}
	for _, d := range w.dials {
		seen[d.remote] = true
		if !netip.MustParsePrefix("192.0.2.0/24").Contains(d.remote) {
			t.Fatalf("dialled %s, outside the /24", d.remote)
		}
	}
	for _, skip := range []string{"192.0.2.0", "192.0.2.255", "192.0.2.10", "192.0.2.77"} {
		if seen[netip.MustParseAddr(skip)] {
			t.Errorf("%s was scanned", skip)
		}
	}
	if len(seen) != 252 {
		t.Errorf("%d distinct addresses dialled", len(seen))
	}
}

func TestOnlyAnAddressOfTheHostStartsAScan(t *testing.T) {
	w := newWorld(t)
	for _, tc := range []struct {
		name string
		addr string
		self []string
		err  error
	}{
		{"not on an interface", "192.0.2.10", []string{"192.0.2.11"}, ErrNotMyAddress},
		{"a private address", "10.0.0.5", []string{"10.0.0.5"}, ErrNotMyAddress},
		{"a CGNAT address", "100.64.1.5", []string{"100.64.1.5"}, ErrNotMyAddress},
		{"loopback", "127.0.0.1", []string{"127.0.0.1"}, ErrNotMyAddress},
		{"an IPv6 address", "2001:db8::1", []string{"2001:db8::1"}, ErrBadAddress},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := w.scanner(tc.self...).Scan(context.Background(), netip.MustParseAddr(tc.addr))
			if !errors.Is(err, tc.err) || res != nil {
				t.Fatalf("Scan = %v, %v", res, err)
			}
		})
	}
	if len(w.dials) != 0 {
		t.Fatalf("%d connections went out for a refused address", len(w.dials))
	}
	// an address of the host that is mapped into IPv6 notation is the same address
	if _, err := w.scanner().Scan(context.Background(), netip.MustParseAddr("::ffff:192.0.2.10")); err != nil {
		t.Fatalf("mapped address: %v", err)
	}
}

func TestNoRootsIsAnErrorNotAnEmptyList(t *testing.T) {
	w := newWorld(t)
	for name, roots := range map[string]func() (*x509.CertPool, error){
		"an error": func() (*x509.CertPool, error) { return nil, errors.New("no roots") },
		"empty":    func() (*x509.CertPool, error) { return x509.NewCertPool(), nil },
		"nil":      func() (*x509.CertPool, error) { return nil, nil },
	} {
		s := w.scanner()
		s.Roots = roots
		if res, err := s.Scan(context.Background(), node); !errors.Is(err, ErrNoRoots) || res != nil {
			t.Errorf("%s: %v, %v", name, res, err)
		}
	}
}

func TestSilentAddressIsGivenUpOn(t *testing.T) {
	w := newWorld(t)
	w.silent("192.0.2.20")
	w.addSite("192.0.2.30", good(issue(t, w.ca, 90*24*time.Hour, "shop.example.com")))
	s := w.scanner()
	s.lim.tls = 100 * time.Millisecond
	res := runScan(t, s)
	if res.Partial || res.Open != 2 || res.TLS13 != 1 || len(res.Candidates) != 1 {
		t.Fatalf("result %+v", res)
	}
}

func TestBudgetEndsTheScanAndReturnsWhatWasDone(t *testing.T) {
	w := newWorld(t)
	w.addSite("192.0.2.30", good(issue(t, w.ca, 90*24*time.Hour, "shop.example.com")))
	w.onDial = func(ctx context.Context, n int) {
		if n > 40 { // the first forty addresses answer at once, then the network goes quiet
			<-ctx.Done()
		}
	}
	s := w.scanner()
	s.lim.budget, s.lim.connect = 300*time.Millisecond, 10*time.Second
	start := time.Now()
	res := runScan(t, s)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("the scan took %v although its budget is 300 ms", took)
	}
	if !res.Partial || res.Addresses >= 253 {
		t.Fatalf("result %+v, want partial", res)
	}
}

func TestCancelledScanGivesNoResult(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithCancel(context.Background())
	w.onDial = func(_ context.Context, n int) {
		if n == 5 {
			cancel()
		}
	}
	res, err := w.scanner().Scan(ctx, node)
	if !errors.Is(err, context.Canceled) || res != nil {
		t.Fatalf("Scan = %v, %v", res, err)
	}
	if len(w.dials) > 5+MaxParallel+4 {
		t.Fatalf("%d connections after the cancel", len(w.dials))
	}
}

// At most ten new connections in any second, by the fake clock, whatever happens at the neighbours.
func TestAtMostTenConnectionsASecond(t *testing.T) {
	w := newWorld(t)
	w.addSite("192.0.2.20", good(issue(t, w.ca, 90*24*time.Hour, "a.example.com", "b.example.com")))
	w.addSite("192.0.2.21", good(issue(t, w.ca, 90*24*time.Hour, "c.example.com", "d.example.com")))
	s := w.scanner()
	s.lim.parallel = 1 // one worker takes the moments in order, so the clock at a dial is its moment
	res := runScan(t, s)
	if res.Addresses != 253 {
		t.Fatalf("result %+v", res)
	}
	times := make([]time.Time, len(w.dials))
	for i, d := range w.dials {
		times[i] = d.at
	}
	if len(times) < 253+4 {
		t.Fatalf("only %d connections", len(times))
	}
	for i := range times {
		n := 0
		for j := i; j < len(times) && times[j].Sub(times[i]) < time.Second; j++ {
			n++
		}
		if n > MaxPerSecond {
			t.Fatalf("%d connections within a second from #%d", n, i)
		}
	}
	if span := times[len(times)-1].Sub(times[0]); span < time.Duration(len(times)-1)*100*time.Millisecond {
		t.Fatalf("%d connections in %v: faster than 10 a second", len(times), span)
	}
}

// Sixteen workers share one pace: the moments they are given never come closer than 100 ms.
func TestPacerSharesOnePaceAcrossWorkers(t *testing.T) {
	clock := &fakeClock{now: t0}
	var mu sync.Mutex
	var slots []time.Time
	p := &pacer{every: 100 * time.Millisecond, now: clock.Now, sleep: func(_ context.Context, until time.Time) error {
		mu.Lock()
		slots = append(slots, until)
		mu.Unlock()
		return nil
	}}
	var wg sync.WaitGroup
	for range MaxParallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				_ = p.wait(context.Background())
			}
		}()
	}
	wg.Wait()
	slices.SortFunc(slots, func(a, b time.Time) int { return a.Compare(b) })
	if len(slots) != 320 {
		t.Fatalf("%d slots", len(slots))
	}
	for i := 1; i < len(slots); i++ {
		if slots[i].Sub(slots[i-1]) < 100*time.Millisecond {
			t.Fatalf("slots %d and %d are %v apart", i-1, i, slots[i].Sub(slots[i-1]))
		}
	}
}

func TestAgentGetsOnlyNamesAndFlagsOut(t *testing.T) {
	w := newWorld(t)
	w.addSite("192.0.2.20", good(issue(t, w.ca, 90*24*time.Hour+12*time.Hour, "shop.example.com")))
	sc := w.scanner()
	sc.lim.parallel = 1 // see TestDNSAnswers: Ms is 0 only while the fake clock stands still
	res := runScan(t, sc)
	p := res.Params()
	for _, k := range []string{"addresses", "open", "tls13", "mlkem", "valid", "partial", "candidates"} {
		if _, ok := p[k]; !ok {
			t.Errorf("params lack %q", k)
		}
	}
	if len(p) != 7 || p["partial"] != "0" || p["valid"] != "1" {
		t.Fatalf("params %v", p)
	}
	const want = `[{"ip":"192.0.2.20","name":"shop.example.com","dns":"none","h2":true,"single_site":true,"http":200,"ms":0,"days":90}]`
	if p["candidates"] != want {
		t.Fatalf("candidates %s", p["candidates"])
	}
	if got := (&Result{}).Params()["candidates"]; got != "[]" {
		t.Fatalf("an empty list is %q", got)
	}
}
