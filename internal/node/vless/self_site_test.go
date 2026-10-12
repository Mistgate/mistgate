package vless

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	xtlsreality "github.com/xtls/reality"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

// ---- the site behind REALITY ----

type selfFixture struct {
	e     *eng
	certs *fakeCerts
	logs  *lockedLogBuffer
}

func newSelfFixture(t *testing.T) *selfFixture {
	t.Helper()
	oldBindIP := BindIP
	BindIP = net.ParseIP("127.0.0.1")
	t.Cleanup(func() { BindIP = oldBindIP })
	certs := newFakeCerts(t, selfName)
	logs := &lockedLogBuffer{}
	env := engine.Env{
		Log:   slog.New(slog.NewTextHandler(logs, nil)),
		Certs: certs,
		Egress: func(string) (engine.Egress, error) {
			return localEgress{}, nil
		},
		Masquerade: func(string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, selfMarker) })
		},
	}
	instance, err := New(env)
	if err != nil {
		t.Fatal(err)
	}
	e := instance.(*eng)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	return &selfFixture{e: e, certs: certs, logs: logs}
}

func (f *selfFixture) spec(id string) plugin.InboundSpec {
	return selfSpec(id, freeTCPPort(f.e, nil), selfName)
}

func (f *selfFixture) apply(t *testing.T, spec plugin.InboundSpec, creds ...plugin.UserCred) engine.ApplyReport {
	t.Helper()
	rep, err := f.e.Apply(context.Background(), spec, creds)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return rep
}

// visit is what a browser sees on the REALITY port: a TLS client that is not one of ours.
func visit(t *testing.T, port uint16, alpn ...string) (body string, leaf *x509.Certificate) {
	t.Helper()
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{ServerName: selfName, InsecureSkipVerify: true, NextProtos: alpn},
			DisableKeepAlives: true,
		},
	}
	resp, err := client.Get("https://127.0.0.1:" + strconv.Itoa(int(port)) + "/")
	if err != nil {
		t.Fatalf("visit: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), resp.TLS.PeerCertificates[0]
}

func pinOf(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

func TestSelfSiteServesBehindREALITY(t *testing.T) {
	f := newSelfFixture(t)
	spec := f.spec("node-a-self")
	const uuid = "66ad4540-b58c-4ad2-9926-ea63445a9b57"
	f.apply(t, spec, testCredential("c1", uuid))
	port := spec.Listen.Port

	h := f.e.Health()
	if len(h) != 1 || h[0].State != plugin.RunRunning || h[0].Detail != "" || h[0].CertPinSHA256 != "" || !h[0].CertNotAfter.IsZero() {
		t.Fatalf("health before issuance = %+v", h)
	}

	// Before issuance a browser gets the site with the persisted self-signed certificate; the ACME entry is not asked
	// (autocert would block the handshake for the whole issuance).
	body, leaf := visit(t, port, "http/1.1")
	if body != selfMarker {
		t.Fatalf("the site answered %q", body)
	}
	if want := infoOfCert(f.certs.fallback).PinSHA256; pinOf(leaf) != want {
		t.Fatalf("certificate before issuance = %s, want the self-signed fallback %s", pinOf(leaf), want)
	}
	if hellos := f.certs.acmeHellos(); len(hellos) != 0 {
		t.Fatalf("the ACME entry was asked before it issued anything: %+v", hellos)
	}

	// Let's Encrypt validates over a connection with ALPN acme-tls/1 only. It is not a client: REALITY passes it to the
	// site raw, and the site hands it to the certificate source, which answers with the challenge certificate.
	conn, err := tls.Dial("tcp", "127.0.0.1:"+strconv.Itoa(int(port)), &tls.Config{ServerName: selfName, InsecureSkipVerify: true, NextProtos: []string{"acme-tls/1"}})
	if err != nil {
		t.Fatalf("TLS-ALPN-01 handshake through REALITY: %v", err)
	}
	state := conn.ConnectionState()
	_ = conn.Close()
	if state.NegotiatedProtocol != "acme-tls/1" || pinOf(state.PeerCertificates[0]) != infoOfCert(f.certs.challenge).PinSHA256 {
		t.Fatalf("validation got protocol %q and certificate %s, want the challenge certificate", state.NegotiatedProtocol, pinOf(state.PeerCertificates[0]))
	}
	if hellos := f.certs.acmeHellos(); len(hellos) != 1 || hellos[0].ServerName != selfName || !slices.Equal(hellos[0].SupportedProtos, []string{"acme-tls/1"}) {
		t.Fatalf("hellos at the certificate source: %+v", hellos)
	}

	// A client of ours is served as before, and gets through only once REALITY has measured the site's records.
	echo := startEchoServer(t)
	client := newXrayClientSNI(t, selfName, port, "xhttp", "c1", uuid, "", false)
	conn2 := client.dial(t, echo.Addr().String())
	if _, err := exchange(conn2, payload(200_000)); err != nil {
		t.Fatalf("traffic through the REALITY inbound with target self: %v", err)
	}
	_ = conn2.Close()
	site := f.e.inbounds["node-a-self"].site
	var lens [][]int
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if lens = recordLens(site, selfName); len(lens) == 3 && len(lens[2]) > 0 {
			break
		}
	}
	// REALITY stored "no records" at once (it cannot verify the self-signed certificate); the engine's own measurement
	// replaced that with the h2 class's tickets and settings. Without an h2 offer Go sends no ticket, so the other two stay empty.
	if len(lens) != 3 || len(lens[2]) == 0 {
		t.Fatalf("record lengths after the first client: %v", lens)
	}

	// After the first issuance every visitor gets the issued certificate and Health reports its pin and expiry.
	f.certs.setIssued()
	body, leaf = visit(t, port, "http/1.1")
	want := infoOfCert(f.certs.acmeChain)
	if body != selfMarker || pinOf(leaf) != want.PinSHA256 {
		t.Fatalf("after issuance: body %q, certificate %s, want %s", body, pinOf(leaf), want.PinSHA256)
	}
	h = f.e.Health()
	if h[0].CertPinSHA256 != want.PinSHA256 || !h[0].CertNotAfter.Equal(want.NotAfter) {
		t.Fatalf("health after issuance = %+v, want pin %s expiring %v", h[0], want.PinSHA256, want.NotAfter)
	}
	if rep := f.apply(t, spec, testCredential("c1", uuid)); rep.Cert.PinSHA256 != want.PinSHA256 {
		t.Fatalf("a repeated Apply reports %+v", rep.Cert)
	}
	if strings.Contains(f.logs.String(), "probe") {
		t.Fatalf("a self inbound probed a target:\n%s", f.logs.String())
	}
}

// recordLens reads what REALITY stored for the site: one entry per ALPN class (none, http/1.1, h2).
func recordLens(s *site, sni string) [][]int {
	var out [][]int
	for alpn := range 3 {
		if v, ok := xtlsreality.GlobalPostHandshakeRecordsLens.Load(s.addr + " " + sni + " " + strconv.Itoa(alpn)); ok {
			if l, ok := v.([]int); ok {
				out = append(out, l)
			}
		}
	}
	return out
}

func TestSelfSiteFailsTheInboundWhenItCannotListen(t *testing.T) {
	f := newSelfFixture(t)
	old := siteSocketFn
	t.Cleanup(func() { siteSocketFn = old })
	siteSocketFn = func(string) (string, string, func(), error) {
		return "unix", filepath.Join(t.TempDir(), "missing", "dir", "site.sock"), func() {}, nil
	}
	spec := f.spec("node-a-self")
	if _, err := f.e.Apply(context.Background(), spec, nil); err == nil || !strings.Contains(err.Error(), "site") {
		t.Fatalf("Apply error = %v, want a site listener failure", err)
	}
	h := f.e.Health()
	if len(h) != 1 || h[0].State != plugin.RunFailed || !strings.Contains(h[0].Detail, "site") {
		t.Fatalf("health = %+v", h)
	}
	if _, released := f.certs.counts(); released != 1 {
		t.Fatalf("the failed start released the certificate claim %d times, want 1", released)
	}

	// The same inbound starts when the address can be used again.
	siteSocketFn = old
	f.apply(t, spec)
	if h := f.e.Health(); h[0].State != plugin.RunRunning {
		t.Fatalf("health after the retry = %+v", h)
	}
}

func TestSelfSiteNeedsACertificateSource(t *testing.T) {
	f := newSelfFixture(t)
	f.e.env.Certs = nil
	if _, err := f.e.Apply(context.Background(), f.spec("node-a-self"), nil); err == nil || !strings.Contains(err.Error(), "certificate source") {
		t.Fatalf("Apply error = %v", err)
	}
}

func siteReachable(in *inbound) bool {
	if in.site == nil {
		return false
	}
	conn, err := net.DialTimeout(in.site.network, in.site.addr, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func TestSelfSiteLifecycleAndCertificateClaim(t *testing.T) {
	f := newSelfFixture(t)
	spec := f.spec("node-a-self")
	f.apply(t, spec)
	first := f.e.inbounds[spec.ID]
	if !siteReachable(first) || !first.certHeld {
		t.Fatalf("running self inbound: site reachable=%v claim held=%v", siteReachable(first), first.certHeld)
	}

	// A users-only change keeps the very same listener.
	f.apply(t, spec, testCredential("c1", "66ad4540-b58c-4ad2-9926-ea63445a9b57"))
	if f.e.inbounds[spec.ID] != first {
		t.Fatal("a users-only change rebuilt the inbound")
	}

	// A restart for another port rebinds the site and keeps the claim: no release, so the issued leaf survives.
	moved := spec
	moved.Listen.Port = freeTCPPort(f.e, map[uint16]bool{spec.Listen.Port: true})
	f.apply(t, moved)
	second := f.e.inbounds[spec.ID]
	if second == first || !siteReachable(second) || !second.certHeld {
		t.Fatalf("after the restart: same=%v reachable=%v held=%v", second == first, siteReachable(second), second.certHeld)
	}
	if first.site != nil {
		t.Fatal("the old site was not closed")
	}
	if _, released := f.certs.counts(); released != 0 {
		t.Fatalf("the restart released the certificate claim %d times", released)
	}

	// Another site as the target gives the claim back, and the site goes away.
	elsewhere := moved
	elsewhere.TLS = plugin.TLS{}
	elsewhere.Settings = json.RawMessage(strings.NewReplacer(`"target":"self"`, `"target":"127.0.0.1:1"`, `["site.example.com"]`, `["www.example.org"]`).Replace(string(moved.Settings)))
	f.apply(t, elsewhere)
	if _, released := f.certs.counts(); released != 1 {
		t.Fatalf("after switching to another site the claim was released %d times, want 1", released)
	}
	if siteReachable(second) || f.e.inbounds[spec.ID].site != nil || f.e.inbounds[spec.ID].certHeld {
		t.Fatal("the inbound that targets another site still has a site or a claim")
	}

	// Back to self, then disabled: the site is gone and the claim returned.
	f.apply(t, moved)
	third := f.e.inbounds[spec.ID]
	if !siteReachable(third) || !third.certHeld {
		t.Fatal("self again did not bring the site back")
	}
	off := moved
	off.Enabled = false
	f.apply(t, off)
	if siteReachable(third) || third.certHeld {
		t.Fatal("a disabled inbound keeps its site or claim")
	}
	if _, released := f.certs.counts(); released != 2 {
		t.Fatalf("released %d times, want 2", released)
	}

	// Enabled once more, then removed.
	f.apply(t, moved)
	fourth := f.e.inbounds[spec.ID]
	if err := f.e.Remove(context.Background(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if siteReachable(fourth) || fourth.certHeld {
		t.Fatal("a removed inbound keeps its site or claim")
	}
	if _, released := f.certs.counts(); released != 3 {
		t.Fatalf("released %d times, want 3", released)
	}
}

func TestSocketNameIsPlainOrHashed(t *testing.T) {
	if got := socketName("inb_01HZ"); got != "inb_01HZ" {
		t.Fatalf("plain id = %q", got)
	}
	long := strings.Repeat("a", 49)
	for _, id := range []string{"", "has space", "slash/inside", long} {
		got := socketName(id)
		if len(got) != 24 || strings.ContainsAny(got, " /") {
			t.Fatalf("socketName(%q) = %q", id, got)
		}
	}
	if socketName("a b") == socketName("a c") {
		t.Fatal("two ids share a socket name")
	}
}

// ---- record lengths ----

// REALITY copies the target's post-handshake records into every authenticated handshake, measured once per process and
// target (design/vless.md section 7.5). The site presents a self-signed certificate until Let's Encrypt has issued one;
// this checks that the measurement does not depend on which certificate the site holds, so that nothing needs measuring
// again after the first issuance. If a future Go changes that (a session ticket that carries the certificate chain, say),
// this test fails and the site must be measured again after issuance, storing over the old lengths and never deleting them.
func TestSiteRecordLengthsDoNotDependOnTheCertificate(t *testing.T) {
	certs := map[string]*tls.Certificate{
		"self-signed P-256":   selfSignedCert(t, selfName),
		"chain, P-256 leaf":   chainCert(t, selfName, false),
		"chain, RSA-2048":     chainCert(t, selfName, true),
		"another self-signed": selfSignedCert(t, "other-"+selfName),
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, selfMarker) })
	sites := map[string]*site{}
	for name, c := range certs {
		s, err := startSite("lens-"+strconv.Itoa(len(sites)), func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return c, nil }, handler)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Close)
		sites[name] = s
		go measureSiteRecords(context.Background(), s, selfName)
	}
	deadline := time.Now().Add(40 * time.Second)
	got := map[string][][]int{}
	for time.Now().Before(deadline) && len(got) < len(sites) {
		time.Sleep(500 * time.Millisecond)
		for name, s := range sites {
			if _, done := got[name]; !done {
				if lens := recordLens(s, selfName); len(lens) == 3 {
					got[name] = lens
				}
			}
		}
	}
	if len(got) != len(sites) {
		t.Fatalf("measured %d of %d sites in time: %v", len(got), len(sites), got)
	}
	reference := got["self-signed P-256"]
	if len(reference[2]) == 0 {
		t.Fatalf("no records measured for the h2 class (the check would prove nothing): %v", reference)
	}
	for name, lens := range got {
		if fmt.Sprint(lens) != fmt.Sprint(reference) {
			t.Errorf("%s: record lengths %v differ from the self-signed site's %v", name, lens, reference)
		}
	}
	t.Logf("record lengths per ALPN class (none, http/1.1, h2) for every certificate: %v", reference)
}

// REALITY'S own measurement verifies the site's certificate, so it can never measure the self-signed one: it stores "no
// records" and keeps that for the life of the process. This is why the engine measures the site itself.
func TestRealityCannotMeasureASelfSignedSite(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, selfMarker) })
	cert := selfSignedCert(t, selfName)
	s, err := startSite("own-measure", func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil }, handler)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	xtlsreality.DetectPostHandshakeRecordsLens(&xtlsreality.Config{Type: s.network, Dest: s.addr, ServerNames: map[string]bool{selfName: true}})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if lens := recordLens(s, selfName); len(lens) == 3 {
			for alpn, l := range lens {
				if len(l) != 0 {
					t.Fatalf("REALITY measured %v for ALPN class %d of a self-signed site: its handshake should not verify", l, alpn)
				}
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("REALITY stored nothing for the site")
}
