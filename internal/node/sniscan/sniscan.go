// Package sniscan looks for REALITY targets among the neighbours of a node: sites in the same /24 that answer on TCP 443
// with TLS 1.3 and X25519MLKEM768 and a certificate that holds for their name (agent.proto "NEIGHBOR SITES").
//
// This is outgoing traffic from the node to addresses that are not the owner's, so the package is built not to be a
// scanner of anything else:
//
//   - The only input is an address, and it must be a public IPv4 address that is assigned to one of this host's own
//     interfaces. The range is that address's /24 and nothing else; the panel cannot name another one (ErrNotMyAddress).
//   - The port is 443. Addresses of this host are left out.
//   - The limits are constants: at most MaxPerSecond new connections a second for the whole scan, MaxParallel at a time,
//     Budget in all (what was done by then is returned as partial), short timeouts per step, at most MaxNames names and
//     two connections per address after the first.
//   - What comes back from a neighbour is reduced at once to host names from its certificate and a few flags (names.go);
//     no other data of a response leaves the package, and the body of a response is never read.
//
// Everything that touches the outside is a field of Scanner (Dial, Resolve, Roots, Self, Now, Sleep), so the tests run
// against servers in the process and a fake clock, never against a network.
package sniscan

import (
	"bufio"
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/node/hostctl"
)

// The limits. Constants on purpose: the owner cannot widen them from the panel.
const (
	// Port is the only port the scan connects to.
	Port = 443
	// MaxPerSecond is the most new TCP connections a second for the whole scan, whatever the number of workers.
	MaxPerSecond = 10
	// MaxParallel is the most addresses handled at the same time.
	MaxParallel = 16
	// MaxNames is the most names tried per address: one more connection each.
	MaxNames = 2
	// Budget is the longest a scan runs; what is done by then is returned, marked partial.
	Budget = 50 * time.Second
	// MaxCandidates is the most candidates returned.
	MaxCandidates = 20
	// MinDaysLeft is how long a certificate must still hold for its site to be a candidate.
	MinDaysLeft = 14
	// MaxHeader bounds what is read of an HTTP response: its head. The body is never read.
	MaxHeader = 16 << 10
)

// Per-step timeouts.
const (
	connectTimeout = 2 * time.Second
	tlsTimeout     = 3 * time.Second
	dnsTimeout     = 2 * time.Second
	httpTimeout    = 3 * time.Second
)

// DNS answers about where a candidate's name leads.
const (
	DNSHere      = "here"      // the name has this very address
	DNSSubnet    = "subnet"    // another address of the same /24
	DNSElsewhere = "elsewhere" // somewhere else: a CDN or a balancer in front, usually
	DNSNone      = "none"      // no IPv4 answer
)

var (
	// ErrNotMyAddress: the address is not a public IPv4 address of one of this host's interfaces (a name behind a CDN,
	// a stale DNS record, a NAT: the scan would not start from that address).
	ErrNotMyAddress = errors.New("not_my_address")
	// ErrNoRoots: this host has no CA roots, so no certificate can be judged; an empty list would be a wrong answer.
	ErrNoRoots = errors.New("no_roots")
	// ErrBadAddress: not an IPv4 address.
	ErrBadAddress = errors.New("bad_params")
)

// Candidate is one site that can serve as a REALITY target.
type Candidate struct {
	IP         string `json:"ip"`
	Name       string `json:"name"`
	DNS        string `json:"dns"`
	H2         bool   `json:"h2"`
	SingleSite bool   `json:"single_site"`
	// HTTP is the status of GET / (redirects not followed), 0 = no answer.
	HTTP int `json:"http"`
	// Ms is the TLS handshake in milliseconds.
	Ms int `json:"ms"`
	// Days is the days the certificate still holds.
	Days int `json:"days"`

	httpRank int // 0 2xx, 1 3xx to the same domain, 2 4xx, 3 the rest (a 3xx away, 5xx, no answer)
}

// Result is what a scan found. The counters count addresses: how many were looked at, how many answered on 443, how many
// of those speak TLS 1.3, how many have a name whose handshake picked X25519MLKEM768, and how many of those are good
// candidates.
type Result struct {
	Addresses, Open, TLS13, MLKEM, Valid int
	// Partial: the budget ran out before every address was done.
	Partial    bool
	Candidates []Candidate
}

// Params is the result as CommandResult.params of the SniScan command.
func (r *Result) Params() map[string]string {
	cands := r.Candidates
	if cands == nil {
		cands = []Candidate{}
	}
	js, _ := json.Marshal(cands)
	partial := "0"
	if r.Partial {
		partial = "1"
	}
	return map[string]string{
		"addresses": strconv.Itoa(r.Addresses), "open": strconv.Itoa(r.Open), "tls13": strconv.Itoa(r.TLS13),
		"mlkem": strconv.Itoa(r.MLKEM), "valid": strconv.Itoa(r.Valid), "partial": partial, "candidates": string(js),
	}
}

// Scanner runs scans. Create it with New; tests replace the fields.
type Scanner struct {
	// Dial opens a TCP connection to remote from the local address.
	Dial func(ctx context.Context, local netip.Addr, remote netip.AddrPort) (net.Conn, error)
	// Resolve returns the IPv4 addresses of a name, with this host's resolver.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Roots are the CA roots a certificate must lead to.
	Roots func() (*x509.CertPool, error)
	// Self lists the addresses assigned to this host's interfaces.
	Self func() ([]netip.Addr, error)
	// Now is the clock; Sleep waits until a time or until ctx ends.
	Now   func() time.Time
	Sleep func(ctx context.Context, until time.Time) error

	lim limits
}

type limits struct {
	budget, connect, tls, dns, http time.Duration
	parallel                        int
	every                           time.Duration // the gap between two new connections
}

// New returns a Scanner for this host.
func New() *Scanner {
	return &Scanner{
		Dial: func(ctx context.Context, local netip.Addr, remote netip.AddrPort) (net.Conn, error) {
			d := net.Dialer{LocalAddr: &net.TCPAddr{IP: local.AsSlice()}}
			return d.DialContext(ctx, "tcp4", remote.String())
		},
		Resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
		},
		Roots: x509.SystemCertPool,
		Self:  hostAddrs,
		Now:   time.Now,
		Sleep: sleepUntil,
	}
}

func (s *Scanner) limits() limits {
	l := s.lim
	for _, f := range []struct {
		v *time.Duration
		d time.Duration
	}{{&l.budget, Budget}, {&l.connect, connectTimeout}, {&l.tls, tlsTimeout}, {&l.dns, dnsTimeout}, {&l.http, httpTimeout}, {&l.every, time.Second / MaxPerSecond}} {
		if *f.v == 0 {
			*f.v = f.d
		}
	}
	if l.parallel == 0 {
		l.parallel = MaxParallel
	}
	return l
}

func hostAddrs() ([]netip.Addr, error) {
	as, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range as {
		if p, err := netip.ParsePrefix(a.String()); err == nil {
			out = append(out, p.Addr().Unmap())
		}
	}
	return out, nil
}

func sleepUntil(ctx context.Context, until time.Time) error {
	d := time.Until(until)
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// pacer hands out the moments at which a new connection may start, one every `every` for the whole scan.
type pacer struct {
	mu    sync.Mutex
	next  time.Time
	every time.Duration
	now   func() time.Time
	sleep func(ctx context.Context, until time.Time) error
}

func (p *pacer) wait(ctx context.Context) error {
	p.mu.Lock()
	t := p.now()
	if p.next.After(t) {
		t = p.next
	}
	p.next = t.Add(p.every)
	p.mu.Unlock()
	return p.sleep(ctx, t)
}

// neighbors lists the addresses to look at: the /24 of addr without .0, .255 and the host's own addresses.
func neighbors(addr netip.Addr, self []netip.Addr) []netip.Addr {
	b := addr.As4()
	var out []netip.Addr
	for last := 1; last <= 254; last++ {
		a := netip.AddrFrom4([4]byte{b[0], b[1], b[2], byte(last)})
		if !slices.Contains(self, a) {
			out = append(out, a)
		}
	}
	return out
}

// Scan looks at the /24 of addr, which must be a public IPv4 address of this host. It returns when every address is done or
// the budget is over (Partial); a cancelled ctx gives its error and no result.
func (s *Scanner) Scan(ctx context.Context, addr netip.Addr) (*Result, error) {
	addr = addr.Unmap()
	if !addr.Is4() {
		return nil, ErrBadAddress
	}
	self, err := s.Self()
	if err != nil || !hostctl.PublicIPv4(addr) || !slices.Contains(self, addr) {
		return nil, ErrNotMyAddress
	}
	roots, err := s.Roots()
	if err != nil || roots == nil || roots.Equal(x509.NewCertPool()) {
		return nil, ErrNoRoots
	}

	lim := s.limits()
	sctx, cancel := context.WithTimeout(ctx, lim.budget)
	defer cancel()
	run := &scan{s: s, lim: lim, roots: roots, local: addr, prefix: netip.PrefixFrom(addr, 24).Masked(),
		pace: &pacer{every: lim.every, now: s.Now, sleep: s.Sleep}}

	targets := neighbors(addr, self)
	work := make(chan netip.Addr)
	var wg sync.WaitGroup
	for range min(lim.parallel, len(targets)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ip := range work {
				run.one(sctx, ip)
			}
		}()
	}
feed:
	for _, ip := range targets {
		select {
		case work <- ip:
		case <-sctx.Done():
			break feed
		}
	}
	close(work)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res := &Result{Addresses: int(run.done.Load()), Open: int(run.open.Load()), TLS13: int(run.tls13.Load()),
		MLKEM: int(run.mlkem.Load()), Valid: int(run.valid.Load())}
	res.Partial = res.Addresses < len(targets)
	res.Candidates = rank(run.cands)
	return res, nil
}

// scan is the state of one Scan.
type scan struct {
	s      *Scanner
	lim    limits
	roots  *x509.CertPool
	local  netip.Addr
	prefix netip.Prefix
	pace   *pacer

	done, open, tls13, mlkem, valid atomic.Int32
	mu                              sync.Mutex
	cands                           []Candidate
}

// one handles an address; it counts it as done only when the work was not cut short.
func (r *scan) one(ctx context.Context, ip netip.Addr) {
	defer func() {
		if recover() != nil { // a neighbour's data is parsed here: a bug must not take the agent down
			r.done.Add(1) // that address is over, not left for the budget
		}
	}()
	if r.address(ctx, ip) && ctx.Err() == nil {
		r.done.Add(1)
	}
}

// connect takes a place in the pace and opens a connection.
func (r *scan) connect(ctx context.Context, ip netip.Addr) (net.Conn, error) {
	if err := r.pace.wait(ctx); err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, r.lim.connect)
	defer cancel()
	return r.s.Dial(cctx, r.local, netip.AddrPortFrom(ip, Port))
}

func (r *scan) tlsConfig(name string, min, max uint16) *tls.Config {
	return &tls.Config{
		ServerName: name, MinVersion: min, MaxVersion: max,
		CurvePreferences:   []tls.CurveID{tls.X25519MLKEM768, tls.X25519},
		NextProtos:         []string{"h2", "http/1.1"},
		InsecureSkipVerify: true, // #nosec G402 -- the chain is checked by hand below, to count the steps apart
	}
}

// handshake runs the TLS handshake of conn under the TLS timeout and returns how long it took.
func (r *scan) handshake(ctx context.Context, conn net.Conn, cfg *tls.Config) (*tls.Conn, time.Duration, error) {
	hctx, cancel := context.WithTimeout(ctx, r.lim.tls)
	defer cancel()
	tc := tls.Client(conn, cfg)
	start := r.s.Now()
	err := tc.HandshakeContext(hctx)
	return tc, r.s.Now().Sub(start), err
}

// address looks at one address; false when the scan ended before it was done.
func (r *scan) address(ctx context.Context, ip netip.Addr) bool {
	conn, err := r.connect(ctx, ip)
	if err != nil {
		return ctx.Err() == nil // nothing on 443 (or nothing that answers in time)
	}
	r.open.Add(1)

	// Without a name: only to see the version and the names the certificate holds.
	tc, _, err := r.handshake(ctx, conn, r.tlsConfig("", tls.VersionTLS12, tls.VersionTLS13))
	_ = conn.Close()
	if err != nil {
		return ctx.Err() == nil
	}
	st := tc.ConnectionState()
	if st.Version != tls.VersionTLS13 || len(st.PeerCertificates) == 0 {
		return true
	}
	r.tls13.Add(1)
	names, single := certNames(st.PeerCertificates[0])

	gotMLKEM, gotValid := false, false
	for _, name := range names {
		if ctx.Err() != nil {
			return false
		}
		c, ok, mlkem := r.name(ctx, ip, name, single)
		gotMLKEM = gotMLKEM || mlkem
		if ok {
			gotValid = true
			r.mu.Lock()
			r.cands = append(r.cands, c)
			r.mu.Unlock()
		}
	}
	if gotMLKEM {
		r.mlkem.Add(1)
	}
	if gotValid {
		r.valid.Add(1)
	}
	return ctx.Err() == nil
}

// name asks the address for one name the way a REALITY client would: TLS 1.3 only, the name as SNI.
func (r *scan) name(ctx context.Context, ip netip.Addr, name string, single bool) (c Candidate, ok, mlkem bool) {
	conn, err := r.connect(ctx, ip)
	if err != nil {
		return c, false, false
	}
	defer conn.Close()
	tc, took, err := r.handshake(ctx, conn, r.tlsConfig(name, tls.VersionTLS13, tls.VersionTLS13))
	if err != nil {
		return c, false, false
	}
	st := tc.ConnectionState()
	if st.CurveID != tls.X25519MLKEM768 || len(st.PeerCertificates) == 0 {
		return c, false, false
	}
	mlkem = true
	leaf := st.PeerCertificates[0]
	now := r.s.Now()
	inter := x509.NewCertPool()
	for _, ic := range st.PeerCertificates[1:] {
		inter.AddCert(ic)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: r.roots, Intermediates: inter, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return c, false, mlkem
	}
	left := leaf.NotAfter.Sub(now)
	if left < MinDaysLeft*24*time.Hour {
		return c, false, mlkem
	}
	c = Candidate{IP: ip.String(), Name: name, H2: st.NegotiatedProtocol == "h2", SingleSite: single,
		Ms: int(took / time.Millisecond), Days: int(left / (24 * time.Hour))}
	c.DNS = r.dns(ctx, name, ip)
	var away bool
	c.HTTP, away = r.get(ctx, tc, name)
	c.httpRank = httpRank(c.HTTP, away)
	return c, true, mlkem
}

// dns says where the name leads, from this host's resolver.
func (r *scan) dns(ctx context.Context, name string, ip netip.Addr) string {
	dctx, cancel := context.WithTimeout(ctx, r.lim.dns)
	defer cancel()
	ips, err := r.s.Resolve(dctx, name)
	if err != nil || len(ips) == 0 {
		return DNSNone
	}
	subnet := false
	for _, a := range ips {
		a = a.Unmap()
		if a == ip {
			return DNSHere
		}
		subnet = subnet || r.prefix.Contains(a)
	}
	if subnet {
		return DNSSubnet
	}
	return DNSElsewhere
}

// get sends GET / with the name as Host over the connection of the handshake and reads the head of the answer: its status,
// and whether a redirect leads away from the name's domain. Redirects are not followed and the body is not read. 0 = no answer.
func (r *scan) get(ctx context.Context, tc *tls.Conn, name string) (code int, away bool) {
	hctx, cancel := context.WithTimeout(ctx, r.lim.http)
	defer cancel()
	_ = tc.SetDeadline(time.Now().Add(r.lim.http)) // a real deadline on real I/O, whatever the scan's clock says
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, "https://"+name+"/", nil)
	if err != nil {
		return 0, false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Accept", "*/*")
	var resp *http.Response
	if tc.ConnectionState().NegotiatedProtocol == "h2" {
		cc, err := (&http2.Transport{MaxHeaderListSize: MaxHeader}).NewClientConn(tc)
		if err != nil {
			return 0, false
		}
		defer cc.Close()
		resp, err = cc.RoundTrip(req)
		if err != nil {
			return 0, false
		}
	} else {
		req.Close = true
		if err := req.Write(tc); err != nil {
			return 0, false
		}
		resp, err = http.ReadResponse(bufio.NewReader(io.LimitReader(tc, MaxHeader)), req)
		if err != nil {
			return 0, false
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		away = offsite(resp.Header.Get("Location"), name)
	}
	return resp.StatusCode, away
}

func httpRank(code int, away bool) int {
	switch {
	case code >= 200 && code < 300:
		return 0
	case code >= 300 && code < 400 && !away:
		return 1
	case code >= 400 && code < 500:
		return 2
	}
	return 3
}

func dnsRank(d string) int {
	switch d {
	case DNSHere:
		return 0
	case DNSSubnet:
		return 1
	case DNSElsewhere:
		return 2
	}
	return 3
}

// rank orders the candidates and keeps MaxCandidates: where the name leads (here, the subnet, elsewhere, nowhere), HTTP/2,
// the certificate of one site over a shared one, the answer to GET /, the time of the handshake; then by address and name,
// so the same findings always come in the same order.
func rank(cs []Candidate) []Candidate {
	b2i := func(b bool) int {
		if b {
			return 0
		}
		return 1
	}
	slices.SortFunc(cs, func(a, b Candidate) int {
		for _, d := range []int{
			dnsRank(a.DNS) - dnsRank(b.DNS), b2i(a.H2) - b2i(b.H2), b2i(a.SingleSite) - b2i(b.SingleSite),
			a.httpRank - b.httpRank, a.Ms - b.Ms,
		} {
			if d != 0 {
				return d
			}
		}
		if c := netip.MustParseAddr(a.IP).Compare(netip.MustParseAddr(b.IP)); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	if len(cs) > MaxCandidates {
		cs = cs[:MaxCandidates]
	}
	return cs
}

// String is a short line for logs: no names, only counts.
func (r *Result) String() string {
	return fmt.Sprintf("addresses=%d open=%d tls13=%d mlkem=%d valid=%d partial=%v", r.Addresses, r.Open, r.TLS13, r.MLKEM, r.Valid, r.Partial)
}
