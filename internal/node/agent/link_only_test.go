package agent

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Step 5c: link-only agents (enrolled with --link-url). Design: design/cloudflare-edition/AGENT-LINK.md §6.2.

func TestEnrollLinkURLCannotBeCombinedOrMalformed(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	base := EnrollConfig{StateDir: t.TempDir(), CASHA256: pin, Token: "t", LinkURL: "wss://de1.example.com/" + strings.Repeat("p", 24) + "/"}
	for name, mutate := range map[string]func(*EnrollConfig){
		"with --panel":  func(c *EnrollConfig) { c.Panel = "de1.example.com:443" },
		"with --sni":    func(c *EnrollConfig) { c.SNI = "agent.example.com" },
		"plain ws":      func(c *EnrollConfig) { c.LinkURL = "ws://de1.example.com/" + strings.Repeat("p", 24) + "/" },
		"no secret":     func(c *EnrollConfig) { c.LinkURL = "wss://de1.example.com/" },
		"query":         func(c *EnrollConfig) { c.LinkURL += "?x=1" },
		"no token":      func(c *EnrollConfig) { c.Token = "" },
		"bad pin":       func(c *EnrollConfig) { c.CASHA256 = "zz" },
		"already there": func(c *EnrollConfig) { _ = os.WriteFile(filepath.Join(c.StateDir, fileIdentity), []byte("x"), 0o600) },
	} {
		cfg := base
		cfg.StateDir = t.TempDir()
		cfg.httpClient = &http.Client{Transport: failingTransport{t}} // must never be reached
		mutate(&cfg)
		if _, err := Enroll(context.Background(), cfg); err == nil {
			t.Errorf("%s: Enroll accepted it", name)
		}
	}
}

type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("a refused enrolment still sent a request to %s", r.URL.Host)
	return nil, http.ErrNotSupported
}

func TestEnrollLinkRefusesAPinMismatch(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{noEnroll: true})
	_, err := r.enrol(strings.Repeat("00", 32))
	if err == nil || !strings.Contains(err.Error(), "does not match --ca-sha256") || !strings.Contains(err.Error(), "create a new enrolment token") {
		t.Fatalf("Enroll with another CA pin: %v", err)
	}
	if IsEnrolled(r.stateDir) {
		t.Error("a pin mismatch left an identity behind")
	}
	if _, err := os.Stat(filepath.Join(r.stateDir, fileMeta)); err == nil {
		t.Error("a pin mismatch left agent.json behind")
	}
}

func TestEnrollLinkSavesLinkMeta(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{})
	raw, err := os.ReadFile(filepath.Join(r.stateDir, fileMeta))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m["link"] != r.linkURL() || m["panel"] != "" || m["sni"] != "" || m["node_id"] != r.nodeID {
		t.Errorf("agent.json = %v", m)
	}
	// A VPS enrolment leaves agent.json exactly as before: no "link" key.
	vps, _ := json.Marshal(Meta{Panel: "de1.example.com:443", SNI: "agent.example.com", NodeID: "nod_x"})
	if strings.Contains(string(vps), "link") {
		t.Errorf("VPS agent.json = %s", vps)
	}

	a, err := New(Config{StateDir: r.stateDir, DoctorEnv: testDoctorEnv(t)}, nil, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	if !a.linkOnly() || a.cfg.LinkURL != r.linkURL() {
		t.Errorf("linkOnly = %v, link %q", a.linkOnly(), a.cfg.LinkURL)
	}

}

func TestLinkOnlyAgentListsNoSelfUpdate(t *testing.T) {
	f := newUpdFix(t)
	var cfg Config
	cfg.StateDir = t.TempDir()
	f.cfg(1000, "0.1.0", nil)(&cfg)
	a := &Agent{cfg: cfg, host: &fakeHost{}, upd: cfg.Updater}
	if caps := a.capabilities(); !contains(caps, "update/1") || !contains(caps, "update-guard/1") {
		t.Fatalf("control: a VPS agent lists %v", caps)
	}
	a.meta.Link = "wss://de1.example.com/" + strings.Repeat("p", 24) + "/"
	caps := a.capabilities()
	if contains(caps, "update/1") || contains(caps, "update-guard/1") || !contains(caps, "doctor/1") || !contains(caps, "ws-link/1") {
		t.Errorf("a link-only agent lists %v", caps)
	}
}

// A link-only agent never dials mTLS (agent.json here even names a panel address and an SNI, a trap that counts), and
// against a dead panel it waits the reconnect backoff between link attempts instead of looping hot.
func TestLinkOnlyAgentNeverDialsMTLSAndBacksOffOnADeadPanel(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{backoff: 100 * time.Millisecond})
	trap, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer trap.Close()
	var trapped atomic.Int32
	go func() {
		for {
			c, err := trap.Accept()
			if err != nil {
				return
			}
			trapped.Add(1)
			_ = c.Close()
		}
	}()
	path := filepath.Join(r.stateDir, fileMeta)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Panel, meta.SNI = trap.Addr().String(), "agent.example.com"
	raw, _ = json.Marshal(meta)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	r.ts.Close() // the panel is dead

	var dials atomic.Int32
	ag := r.start(func(a *Agent) {
		a.linkAdvertised.Store(true) // whatever an earlier HelloAck said
		a.linkHTTPClient = &http.Client{Transport: countingTransport{n: &dials}}
	})
	time.Sleep(1200 * time.Millisecond)
	if err := ag.stop(); err != nil {
		t.Fatal(err)
	}
	if n := dials.Load(); n < 3 || n > 20 {
		t.Errorf("link attempts in 1.2 s with a 100 ms backoff = %d, want a handful (a hot loop would be thousands)", n)
	}
	if n := trapped.Load(); n != 0 {
		t.Errorf("the link-only agent dialled the mTLS address %d times", n)
	}
	if a := ag.a; a.cur.Load() != nil || !a.linkOnly() {
		t.Error("agent state after a dead panel")
	}
}

type countingTransport struct{ n *atomic.Int32 }

func (c countingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	c.n.Add(1)
	return nil, &net.OpError{Op: "dial", Err: net.ErrClosed}
}

// Renewal over the link without a session, with a session that never answers, and with a session that ends.
func TestRenewOverLinkNeedsASessionAndWaitsForTheAnswer(t *testing.T) {
	a := &Agent{renewTimeout: 50 * time.Millisecond}
	if err := a.renewOverLink(context.Background()); err == nil || !strings.Contains(err.Error(), "no panel session") {
		t.Fatalf("no session: %v", err)
	}
	newSession := func() (*session, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		return &session{a: a, ctx: ctx, cancel: cancel, ctl: make(chan ctlMsg, 4)}, cancel
	}

	s, cancel := newSession()
	defer cancel()
	a.cur.Store(s)
	start := time.Now()
	if err := a.renewOverLink(context.Background()); err == nil || !strings.Contains(err.Error(), "in time") || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("silent panel: %v after %v", err, time.Since(start))
	}
	select {
	case c := <-s.ctl:
		if c.m.GetRenew() == nil || len(c.m.GetRenew().CsrDer) == 0 || c.m.Seq != 0 {
			t.Errorf("the request on the session = %+v", c.m)
		}
	default:
		t.Error("no ConnectRequest.renew was queued")
	}
	if a.renewReply != nil {
		t.Error("the waiter was left registered")
	}

	s2, cancel2 := newSession()
	a.cur.Store(s2)
	a.renewTimeout = time.Minute
	go func() { time.Sleep(20 * time.Millisecond); cancel2() }()
	if err := a.renewOverLink(context.Background()); err == nil || !strings.Contains(err.Error(), "session ended") {
		t.Fatalf("ended session: %v", err)
	}
}

// A transport error must not print the link URL: its path is the secret prefix.
func TestEnrollLinkErrorDoesNotEchoTheSecretPrefix(t *testing.T) {
	r := newEdgeRig(t, edgeOpts{noEnroll: true})
	r.ts.Close()
	_, err := r.enrol(r.f.CAFingerprint())
	if err == nil {
		t.Fatal("Enroll against a dead panel succeeded")
	}
	if strings.Contains(err.Error(), strings.Trim(r.prefix, "/")) || strings.Contains(err.Error(), "edge-link-enrollment-token") {
		t.Errorf("the error leaks the secret prefix or the token: %v", err)
	}
}

// A link-only agent back from an outage with its certificate near the end must not wait renewEvery (an hour) for a
// session: with no session the renewal is tried again after renewRetry.
func TestRenewLoopRetriesSoonWhenThereIsNoSession(t *testing.T) {
	logs := &lockedLogBuffer{}
	a := &Agent{renewEvery: time.Hour, renewRetry: 20 * time.Millisecond, log: slog.New(slog.NewTextHandler(logs, nil))}
	a.meta.Link = "wss://de1.example.com/" + strings.Repeat("p", 24) + "/"
	a.id.Store(&identity{cert: tls.Certificate{Leaf: &x509.Certificate{NotAfter: time.Now().Add(time.Hour)}}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.renewLoop(ctx); close(done) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if n := strings.Count(logs.String(), "certificate renewal failed"); n < 4 {
		t.Errorf("renewal attempts without a session in 300 ms = %d, want several (retry every 20 ms, not every hour)", n)
	}
}
