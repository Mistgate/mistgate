package doctor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// testCA is a certificate authority of the test: its roots are what a target's certificate must lead to.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
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
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// leaf issues a server certificate for names that ends left after t0 (negative: already over).
func (ca *testCA) leaf(t *testing.T, left time.Duration, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: t0.Add(-60 * 24 * time.Hour), NotAfter: t0.Add(left),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// tlsTarget is a TLS server on the loopback: the handshake is completed and the connection closed.
func tlsTarget(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
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
			go func() {
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_ = c.(*tls.Conn).Handshake()
				_ = c.Close()
			}()
		}
	}()
	return ln.Addr().String()
}

func hangingTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var open []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range open {
			_ = c.Close()
		}
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
	return ln.Addr().String()
}

func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

const shop = "shop.example.com"

// The real handshake against local servers (the model of internal/node/vless/review_round2_test.go): what each kind of
// target is called, with a test authority standing for the system roots.
func TestProbeTLSTargetClassifiesLocalTargets(t *testing.T) {
	ca := newTestCA(t)
	other := newTestCA(t)
	good := ca.leaf(t, 60*24*time.Hour, shop)
	mlkem := []tls.CurveID{tls.X25519MLKEM768}
	x25519 := []tls.CurveID{tls.X25519}
	srv := func(c tls.Certificate, min, max uint16, curves []tls.CurveID) string {
		return tlsTarget(t, &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: min, MaxVersion: max, CurvePreferences: curves})
	}
	for _, tc := range []struct {
		name    string
		addr    string
		timeout time.Duration
		want    TargetFacts
	}{
		{"nothing listens", closedPort(t), 2 * time.Second, TargetFacts{Err: "unreachable"}},
		{"never answers", hangingTarget(t), 150 * time.Millisecond, TargetFacts{Err: "unreachable"}},
		{"TLS 1.2 only", srv(good, tls.VersionTLS12, tls.VersionTLS12, x25519), 3 * time.Second, TargetFacts{Err: "no_tls13"}},
		{"X25519 only", srv(good, tls.VersionTLS13, tls.VersionTLS13, x25519), 3 * time.Second,
			TargetFacts{NotAfter: t0.Add(60 * 24 * time.Hour)}},
		{"good", srv(good, tls.VersionTLS13, tls.VersionTLS13, mlkem), 3 * time.Second,
			TargetFacts{MLKEM: true, NotAfter: t0.Add(60 * 24 * time.Hour)}},
		{"another name", srv(ca.leaf(t, 60*24*time.Hour, "other.example.net"), tls.VersionTLS13, tls.VersionTLS13, mlkem), 3 * time.Second,
			TargetFacts{MLKEM: true, Cert: "name_mismatch", NotAfter: t0.Add(60 * 24 * time.Hour)}},
		{"another authority", srv(other.leaf(t, 60*24*time.Hour, shop), tls.VersionTLS13, tls.VersionTLS13, mlkem), 3 * time.Second,
			TargetFacts{MLKEM: true, Cert: "untrusted", NotAfter: t0.Add(60 * 24 * time.Hour)}},
		{"over", srv(ca.leaf(t, -time.Hour, shop), tls.VersionTLS13, tls.VersionTLS13, mlkem), 3 * time.Second,
			TargetFacts{MLKEM: true, Cert: "expired", NotAfter: t0.Add(-time.Hour)}},
		{"ends in 3 days", srv(ca.leaf(t, 72*time.Hour, shop), tls.VersionTLS13, tls.VersionTLS13, mlkem), 3 * time.Second,
			TargetFacts{MLKEM: true, NotAfter: t0.Add(72 * time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			got := probeTLSTarget(ctx, tc.addr, shop, ca.pool, t0)
			// The server clock of the test is the real one, the leaf's dates are the fake clock's: compare the dates by
			// instant, the rest as they are.
			if got.Err != tc.want.Err || got.MLKEM != tc.want.MLKEM || got.Cert != tc.want.Cert || !got.NotAfter.Truncate(time.Second).Equal(tc.want.NotAfter) {
				t.Fatalf("probeTLSTarget = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestClassifyTarget(t *testing.T) {
	soon, later := t0.Add(6*24*time.Hour+time.Hour), t0.Add(8*24*time.Hour)
	for _, tc := range []struct {
		name string
		f    TargetFacts
		st   Status
		hint string
		days int
	}{
		{"unreachable", TargetFacts{Err: "unreachable"}, Fail, "unreachable", 0},
		{"tls 1.2", TargetFacts{Err: "no_tls13"}, Fail, "no_tls13", 0},
		{"no mlkem", TargetFacts{NotAfter: later}, Fail, "no_mlkem", 0},
		{"name", TargetFacts{MLKEM: true, Cert: "name_mismatch", NotAfter: later}, Warn, "name_mismatch", 0},
		{"untrusted", TargetFacts{MLKEM: true, Cert: "untrusted", NotAfter: later}, Warn, "untrusted", 0},
		{"expired by the chain", TargetFacts{MLKEM: true, Cert: "expired", NotAfter: later}, Warn, "expired", 0},
		{"expired by the date", TargetFacts{MLKEM: true, NotAfter: t0}, Warn, "expired", 0},
		{"6 days", TargetFacts{MLKEM: true, NotAfter: soon}, Warn, "expiring", 6},
		{"8 days", TargetFacts{MLKEM: true, NotAfter: later}, OK, "", 0},
		{"no date known", TargetFacts{MLKEM: true}, OK, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, hint, days := classifyTarget(tc.f, t0)
			if st != tc.st || hint != tc.hint || days != tc.days {
				t.Fatalf("classifyTarget = %s %q %d, want %s %q %d", st, hint, days, tc.st, tc.hint, tc.days)
			}
		})
	}
}

func realityInbound(id, target, name string) Inbound {
	return Inbound{ID: id, Protocol: "vless", Network: "tcp", Port: 443, Enabled: true, State: "running", RealityTarget: target, RealityName: name}
}

// scripted answers ProbeTarget from a table and counts the calls per target.
type scripted struct {
	mu    sync.Mutex
	calls map[string]int
	reply func(addr string, call int) TargetFacts
}

func (s *scripted) probe(_ context.Context, addr, _ string) TargetFacts {
	s.mu.Lock()
	s.calls[addr]++
	n := s.calls[addr]
	s.mu.Unlock()
	return s.reply(addr, n)
}

func (s *scripted) count(addr string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[addr]
}

func withTargets(s *scripted, ins ...Inbound) func(*Env) {
	return func(e *Env) {
		e.Inbounds = func() []Inbound { return ins }
		e.ProbeTarget = s.probe
	}
}

var goodFacts = TargetFacts{MLKEM: true, NotAfter: t0.Add(60 * 24 * time.Hour)}

func TestRealityTargetCheck(t *testing.T) {
	f := newFake(t)
	all := func(facts TargetFacts) *scripted {
		return &scripted{calls: map[string]int{}, reply: func(string, int) TargetFacts { return facts }}
	}

	t.Run("nothing to look at", func(t *testing.T) {
		s := all(goodFacts)
		self := realityInbound("inb_self", "", "") // the node's own site: the agent leaves the target empty
		off := realityInbound("inb_off", "a.example.com:443", "a.example.com")
		off.Enabled = false
		failed := realityInbound("inb_failed", "b.example.com:443", "b.example.com")
		failed.State = "failed"
		hy2 := Inbound{ID: "inb_hy", Protocol: "hysteria2", Enabled: true, State: "running"}
		r := run(t, f.doctor(withTargets(s, self, off, failed, hy2)), CheckRealityTarget)
		want(t, r, Skip, "")
		code(t, r, CodeRealityNone)
		if n := s.count("a.example.com:443") + s.count("b.example.com:443"); n != 0 {
			t.Fatalf("probed %d times", n)
		}
	})
	t.Run("all good", func(t *testing.T) {
		s := all(goodFacts)
		r := run(t, f.doctor(withTargets(s, realityInbound("inb_1", "a.example.com:443", "a.example.com"), realityInbound("inb_2", "b.example.com:443", "b.example.com"))), CheckRealityTarget)
		want(t, r, OK, "")
		code(t, r, CodeRealityOK, "checked", "2")
		if s.count("a.example.com:443") != 1 {
			t.Fatalf("a good target is probed once, got %d", s.count("a.example.com:443"))
		}
	})
	t.Run("two inbounds, one target, one probe", func(t *testing.T) {
		s := all(TargetFacts{})
		r := run(t, f.doctor(withTargets(s, realityInbound("inb_1", "a.example.com:443", "a.example.com"), realityInbound("inb_2", "A.example.com:443", "a.example.com"))), CheckRealityTarget)
		want(t, r, Fail, "")
		code(t, r, CodeRealityNoMLKEM, "hint", "no_mlkem", "inbound_id", "inb_1", "inbounds", "inb_1,inb_2", "target", "a.example.com:443", "server_name", "a.example.com", "checked", "1")
		if s.count("a.example.com:443") != 2 { // the first try and the retry, not four
			t.Fatalf("probed %d times", s.count("a.example.com:443"))
		}
	})
	t.Run("each failure", func(t *testing.T) {
		for _, tc := range []struct {
			f    TargetFacts
			st   Status
			code string
			days string
		}{
			{TargetFacts{Err: "unreachable"}, Fail, CodeRealityUnreachable, ""},
			{TargetFacts{Err: "no_tls13"}, Fail, CodeRealityNoTLS13, ""},
			{TargetFacts{NotAfter: t0.Add(60 * 24 * time.Hour)}, Fail, CodeRealityNoMLKEM, ""},
			{TargetFacts{MLKEM: true, Cert: "name_mismatch"}, Warn, CodeRealityNameMismatch, ""},
			{TargetFacts{MLKEM: true, Cert: "untrusted"}, Warn, CodeRealityUntrusted, ""},
			{TargetFacts{MLKEM: true, Cert: "expired", NotAfter: t0.Add(-time.Hour)}, Warn, CodeRealityExpired, "0"},
			{TargetFacts{MLKEM: true, NotAfter: t0.Add(5*24*time.Hour + time.Hour)}, Warn, CodeRealityExpiring, "5"},
		} {
			s := all(tc.f)
			r := run(t, f.doctor(withTargets(s, realityInbound("inb_1", "a.example.com:443", "a.example.com"))), CheckRealityTarget)
			want(t, r, tc.st, "")
			code(t, r, tc.code, "inbound_id", "inb_1", "days_left", tc.days)
			if len(r.Detail) > 200 {
				t.Errorf("detail is %d bytes", len(r.Detail))
			}
		}
	})
	t.Run("the worst one is named, all are listed", func(t *testing.T) {
		s := &scripted{calls: map[string]int{}, reply: func(addr string, _ int) TargetFacts {
			switch addr {
			case "warn.example.com:443":
				return TargetFacts{MLKEM: true, Cert: "name_mismatch"}
			case "fail.example.com:443":
				return TargetFacts{Err: "unreachable"}
			}
			return goodFacts
		}}
		r := run(t, f.doctor(withTargets(s,
			realityInbound("inb_1", "warn.example.com:443", "warn.example.com"),
			realityInbound("inb_2", "fine.example.com:443", "fine.example.com"),
			realityInbound("inb_3", "fail.example.com:443", "fail.example.com"))), CheckRealityTarget)
		want(t, r, Fail, "")
		code(t, r, CodeRealityUnreachable, "inbound_id", "inb_3", "inbounds", "inb_1,inb_3", "checked", "3")
	})
	t.Run("a hiccup is not an alert", func(t *testing.T) {
		s := &scripted{calls: map[string]int{}, reply: func(_ string, call int) TargetFacts {
			if call == 1 {
				return TargetFacts{Err: "unreachable"}
			}
			return goodFacts
		}}
		r := run(t, f.doctor(withTargets(s, realityInbound("inb_1", "a.example.com:443", "a.example.com"))), CheckRealityTarget)
		want(t, r, OK, "")
		if s.count("a.example.com:443") != 2 {
			t.Fatalf("probed %d times", s.count("a.example.com:443"))
		}
	})
	t.Run("a bad certificate is not retried", func(t *testing.T) {
		s := all(TargetFacts{MLKEM: true, Cert: "name_mismatch"})
		want(t, run(t, f.doctor(withTargets(s, realityInbound("inb_1", "a.example.com:443", "a.example.com"))), CheckRealityTarget), Warn, "")
		if s.count("a.example.com:443") != 1 {
			t.Fatalf("probed %d times", s.count("a.example.com:443"))
		}
	})
	t.Run("at most eight targets", func(t *testing.T) {
		s := all(goodFacts)
		var ins []Inbound
		for i := range 12 {
			ins = append(ins, realityInbound("inb_"+strconv.Itoa(i), "t"+strconv.Itoa(i)+".example.com:443", "t"+strconv.Itoa(i)+".example.com"))
		}
		r := run(t, f.doctor(withTargets(s, ins...)), CheckRealityTarget)
		code(t, r, CodeRealityOK, "checked", "8")
	})
}

// Three targets that never answer finish inside the check's 10 s budget: the targets are probed side by side and one
// target costs two tries and the pause between them.
func TestRealityTargetBudgetWithThreeSilentTargets(t *testing.T) {
	if 2*realityAttempt+realityRetryGap >= checkBudget {
		t.Fatalf("2 x %v + %v does not fit the %v budget of a check", realityAttempt, realityRetryGap, checkBudget)
	}
	oldA, oldG := realityAttempt, realityRetryGap
	realityAttempt, realityRetryGap = 60*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { realityAttempt, realityRetryGap = oldA, oldG })

	var tries atomic.Int32
	block := func(ctx context.Context, _, _ string) TargetFacts {
		tries.Add(1)
		<-ctx.Done()
		return TargetFacts{Err: "unreachable"}
	}
	f := newFake(t)
	d := f.doctor(func(e *Env) {
		e.Inbounds = func() []Inbound {
			return []Inbound{realityInbound("inb_1", "a.example.com:443", "a.example.com"), realityInbound("inb_2", "b.example.com:443", "b.example.com"), realityInbound("inb_3", "c.example.com:443", "c.example.com")}
		}
		e.ProbeTarget = block
	})
	start := time.Now()
	r := run(t, d, CheckRealityTarget)
	if took := time.Since(start); took > time.Second {
		t.Fatalf("three silent targets took %v", took)
	}
	want(t, r, Fail, "")
	code(t, r, CodeRealityUnreachable, "inbounds", "inb_1,inb_2,inb_3")
	if tries.Load() != 6 {
		t.Fatalf("%d tries, want 2 per target", tries.Load())
	}
}

// The whole path with the real handshake: the doctor reads the inbounds, probes loopback servers and says what is wrong.
func TestRealityTargetCheckEndToEnd(t *testing.T) {
	ca := newTestCA(t)
	cfg := func(c tls.Certificate, curves []tls.CurveID) *tls.Config {
		return &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: curves}
	}
	mlkem := []tls.CurveID{tls.X25519MLKEM768}
	good := tlsTarget(t, cfg(ca.leaf(t, 60*24*time.Hour, shop), mlkem))
	f := newFake(t)
	withRoots := func(ins ...Inbound) func(*Env) {
		return func(e *Env) {
			e.Inbounds = func() []Inbound { return ins }
			e.TargetRoots = ca.pool
		}
	}
	r := run(t, f.doctor(withRoots(realityInbound("inb_1", good, shop))), CheckRealityTarget)
	want(t, r, OK, "")

	old := tlsTarget(t, cfg(ca.leaf(t, 60*24*time.Hour, shop), []tls.CurveID{tls.X25519}))
	r = run(t, f.doctor(withRoots(realityInbound("inb_1", good, shop), realityInbound("inb_2", old, shop))), CheckRealityTarget)
	want(t, r, Fail, "")
	code(t, r, CodeRealityNoMLKEM, "inbound_id", "inb_2", "inbounds", "inb_2", "checked", "2")

	foreign := tlsTarget(t, cfg(ca.leaf(t, 60*24*time.Hour, "other.example.net"), mlkem))
	r = run(t, f.doctor(withRoots(realityInbound("inb_1", foreign, shop))), CheckRealityTarget)
	want(t, r, Warn, "")
	code(t, r, CodeRealityNameMismatch)
}
