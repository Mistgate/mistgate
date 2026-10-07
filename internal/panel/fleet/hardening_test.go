package fleet

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
)

// sync waits until the panel has processed everything the agent sent before: a stats batch is acked only
// after the messages in front of it were handled (one goroutine per stream).
func (c *conn) sync(seq uint64) {
	c.t.Helper()
	c.send(seq, statsBatch(time.Now().Unix()-1, time.Now().Unix(), nil, nil))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= seq })
}

// F4 + F5: what a node reports as text or pin cannot break the admin API or reach a subscription.
func TestNodeTextIsClippedAndPinValidated(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, ds := connectFull(a, "inst1")

	// 'a' + 400 Cyrillic letters (2 bytes each): byte 512 is inside a letter, so a byte-wise cut is invalid UTF-8.
	cyr := "a" + strings.Repeat("я", 400)
	hostile := "aa&sni=evil.example\nhysteria2://x@evil.example:1/"
	good := strings.Repeat("ab", 32)
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: ds.Revision, Status: agentv1.ApplyStatus_APPLY_STATUS_PARTIAL, StateHash: ds.StateHash,
		Inbounds: []*agentv1.InboundResult{
			{InboundId: ids.i1, State: agentv1.InboundRunState_INBOUND_RUN_STATE_FAILED, Error: cyr, CertPinSha256: hostile},
			{InboundId: ids.i2, State: agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING, CertPinSha256: strings.ToUpper(good)},
		}}}})
	c.sync(1)

	in1, err := e.st.Access().Inbound(e.ctx, ids.i1)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(in1.LastError) || len(in1.LastError) > 512 || len(in1.LastError) < 500 {
		t.Errorf("last_error: valid=%v len=%d, want valid UTF-8 cut on a letter boundary", utf8.ValidString(in1.LastError), len(in1.LastError))
	}
	if in1.CertPinSHA256 != "" {
		t.Errorf("hostile pin stored: %q", in1.CertPinSHA256)
	}
	if in2, _ := e.st.Access().Inbound(e.ctx, ids.i2); in2.CertPinSHA256 != good {
		t.Errorf("valid pin stored as %q, want %q (normalised)", in2.CertPinSHA256, good)
	}

	// The page that broke: GetNode marshals Inbound.last_error (proto3 strings must be valid UTF-8).
	resp, err := nodeService{e.f}.GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := proto.Marshal(resp.Msg); err != nil {
		t.Errorf("GetNode does not marshal: %v", err)
	}

	// Event parameters are bounded the same way.
	c.send(2, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		TimeUnix: time.Now().Unix(), Severity: 2, Code: "x", Params: map[string]string{"k": cyr + cyr}}}})
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 2 })
	var params string
	e.st.R.QueryRow(`SELECT params_json FROM event WHERE code = 'x'`).Scan(&params)
	if !utf8.ValidString(params) || strings.ContainsRune(params, utf8.RuneError) {
		t.Errorf("event params %q", params)
	}
}

// F3: one bad batch must neither corrupt used_bytes nor wedge the stream.
func TestStatsBatchCannotCorruptAccounting(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	now := time.Now().Unix()

	used := func(user string) (n int64) {
		e.st.R.QueryRow(`SELECT used_bytes FROM user WHERE id = ?`, user).Scan(&n)
		return
	}
	buckets := func() int64 { return e.count(`SELECT count(*) FROM traffic_bucket`) }

	// The scenario of the review: 4096 deltas of 2^50 each summed to -2^63 in used_bytes.
	var many []*agentv1.TrafficDelta
	for i := 0; i < 4096; i++ {
		many = append(many, &agentv1.TrafficDelta{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 1 << 50, BytesDown: 1 << 50})
	}
	c.send(1, statsBatch(now-10, now, many, nil))
	c.ack()
	if u := used("usr_alice"); u != 0 || buckets() != 0 {
		t.Fatalf("after the flood used_bytes = %d, buckets = %d, want untouched", u, buckets())
	}
	if n := e.count(`SELECT count(*) FROM event WHERE code = 'stats_rejected' AND node_id = ?`, a.nodeID); n != 1 {
		t.Errorf("stats_rejected events: %d, want 1", n)
	}

	// One 1 PiB delta, and values that are "negative" as int64: dropped, the user is not limited.
	c.send(2, statsBatch(now-10, now, []*agentv1.TrafficDelta{{CredId: "crd_hank_hy", InboundId: ids.i1, BytesUp: 1 << 50}}, nil))
	c.send(3, statsBatch(now-10, now, []*agentv1.TrafficDelta{{CredId: "crd_hank_hy", InboundId: ids.i1, BytesDown: ^uint64(0)}}, nil))
	c.send(4, statsBatch(now-10, now, []*agentv1.TrafficDelta{
		{CredId: "crd_hank_hy", InboundId: ids.i1, BytesUp: 8_000_000_000}, // each is below 10 Gbit/s x 10 s ...
		{CredId: "crd_hank_hy", InboundId: ids.i1, BytesUp: 8_000_000_000}, // ... their sum is not
	}, nil))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 4 })
	if u := used("usr_hank"); u != 0 {
		t.Errorf("hank used_bytes = %d after hostile deltas", u)
	}
	var status string
	e.st.R.QueryRow(`SELECT status FROM user WHERE id = 'usr_hank'`).Scan(&status)
	if status != "active" {
		t.Errorf("hank status %q: a node limited a user with invented traffic", status)
	}

	// A bad delta does not take the honest ones of the same batch with it, and the stream carries on.
	c.send(5, statsBatch(now-10, now, []*agentv1.TrafficDelta{
		{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 1000, BytesDown: 5000},
		{CredId: "crd_erin_hy", InboundId: ids.i1, BytesUp: 1 << 50},
	}, nil))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 5 })
	if u := used("usr_alice"); u != 6000 {
		t.Errorf("alice used_bytes = %d, want 6000 from the honest delta", u)
	}
	if u := used("usr_erin"); u != 0 {
		t.Errorf("erin used_bytes = %d", u)
	}
	// Events are rate limited per stream: the five bad batches above produced one, not five.
	if n := e.count(`SELECT count(*) FROM event WHERE code = 'stats_rejected'`); n != 1 {
		t.Errorf("stats_rejected events after 5 bad batches: %d, want 1", n)
	}
}

// A credential is only valid on an inbound of its own protocol (a node cannot book hysteria traffic of a user
// on a WireGuard inbound or the other way round).
func TestStatsCredentialMustMatchInboundProtocol(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	now := time.Now().Unix()
	c.send(1, statsBatch(now-10, now, []*agentv1.TrafficDelta{{CredId: "crd_alice_wg", InboundId: ids.i1, BytesUp: 500}}, nil))
	c.ack()
	if n := e.count(`SELECT count(*) FROM traffic_bucket`); n != 0 {
		t.Errorf("a wg credential was booked on a hysteria inbound (%d buckets)", n)
	}
}

// An end time from the future used to freeze the live view: later honest batches looked "older".
func TestStatsFutureEndDoesNotFreezeLiveView(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	now := time.Now().Unix()
	year := now + 365*24*3600

	c.send(1, statsBatch(year-10, year, nil, []*agentv1.Session{{CredId: "crd_alice_hy", InboundId: ids.i1, ConnectedAtUnix: year}}))
	c.send(2, statsBatch(now-10, now, nil, []*agentv1.Session{{CredId: "crd_erin_hy", InboundId: ids.i1, ConnectedAtUnix: now - 5}}))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 2 })
	on := e.f.Online()
	if len(on) != 1 || on[0].UserID != "usr_erin" {
		t.Fatalf("online after a future-dated batch and an honest one: %+v", on)
	}
	if on[0].ConnectedAt.After(time.Now()) {
		t.Errorf("connected_at %v is in the future", on[0].ConnectedAt)
	}
}

// A batch the database refuses is retried once (maybe transient) and then dropped and acked: it must not be
// resent forever and wedge the node's accounting.
func TestPoisonBatchIsDroppedAfterASecondRefusal(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c, _, _ := connectFull(a, "inst1")
	now := time.Now().Unix()
	e.exec(`CREATE TRIGGER poison BEFORE UPDATE OF used_bytes ON user WHEN NEW.used_bytes = 4242 BEGIN SELECT RAISE(ABORT, 'poison'); END`)
	poison := func() *agentv1.ConnectRequest {
		return statsBatch(now-10, now, []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 4242}}, nil)
	}

	c.send(1, poison())
	if err := c.ended(); code(err) != connect.CodeInternal {
		t.Fatalf("first refusal: stream ended with %v, want INTERNAL (the agent resends)", err)
	}
	c2 := a.open()
	c2.send(0, hello("inst1", 1, ""))
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	c2.desired()
	c2.send(1, poison()) // resent after the reconnect
	if ack := c2.ack(); ack.UpToSeq != 1 {
		t.Fatalf("ack %d, want the poison batch acked and dropped", ack.UpToSeq)
	}
	if n := e.count(`SELECT count(*) FROM event WHERE code = 'stats_dropped'`); n != 1 {
		t.Errorf("stats_dropped events: %d", n)
	}
	// The stream and the accounting go on.
	c2.send(2, statsBatch(now-10, now, []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 7}}, nil))
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 2 })
	var u int64
	e.st.R.QueryRow(`SELECT used_bytes FROM user WHERE id = 'usr_alice'`).Scan(&u)
	if u != 7 {
		t.Errorf("used_bytes = %d, want 7", u)
	}
}

// F2: the Enroll limiter counts an IPv6 /64 as one source and an IPv4 address as one, and cannot grow without bound.
func TestEnrollLimiterSourcesAndBound(t *testing.T) {
	ip := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	now := time.Now()
	ctx := context.Background()

	l := securitylimit.NewMemory(func() time.Time { return now }, 0)
	fail := func(key string) {
		t.Helper()
		peek, err := l.Peek(ctx, enrollmentWindow, key)
		if err != nil {
			t.Fatal(err)
		}
		if !peek.Allowed {
			return
		}
		decision, err := l.Record(ctx, enrollmentWindow, key)
		if err != nil {
			t.Fatal(err)
		}
		if !decision.Allowed {
			t.Fatalf("failure record for %q was refused", key)
		}
	}
	blocked := func(key string) bool {
		t.Helper()
		decision, err := l.Peek(ctx, enrollmentWindow, key)
		if err != nil {
			t.Fatal(err)
		}
		return !decision.Allowed
	}
	for i := 0; i < 10; i++ {
		// ten different addresses of one /64: one source
		fail(limiterKey(ip(fmt.Sprintf("2001:db8:1:2:%x::1", i+1)), ""))
	}
	if !blocked(limiterKey(ip("2001:db8:1:2:ffff::9"), "")) {
		t.Error("another address of the same /64 is not blocked")
	}
	if blocked(limiterKey(ip("2001:db8:1:3::1"), "")) {
		t.Error("a neighbouring /64 was blocked")
	}
	if limiterKey(ip("::ffff:203.0.113.9"), "") != limiterKey(ip("203.0.113.9"), "") || limiterKey(ip("203.0.113.9"), "") == limiterKey(ip("203.0.113.10"), "") {
		t.Error("IPv4 sources: a mapped address is the same source, another address is not")
	}
	if limiterKey(netip.Addr{}, "198.51.100.4") != limiterKey(ip("198.51.100.4"), "") {
		t.Error("without a resolved client address the peer is used")
	}

	// Bounded: a flood of rotating sources keeps the table at its cap, and is O(1) per call.
	l = securitylimit.NewMemory(func() time.Time { return now }, 0)
	for i := 0; i < 50_000; i++ {
		fail(fmt.Sprintf("src-%d", i))
	}
	if n := l.Size("enrollment-failure"); n != securitylimit.DefaultMaxKeysPerName {
		t.Errorf("limiter holds %d sources, want the cap %d", n, securitylimit.DefaultMaxKeysPerName)
	}
	// The most recent failures are the ones remembered.
	for i := 0; i < 10; i++ {
		fail("src-49999")
	}
	if !blocked("src-49999") {
		t.Error("a recent offender is not blocked")
	}
	if blocked("src-0") {
		t.Error("the oldest source should have been evicted")
	}
}

// F9: after Renew the previous certificate dies after a short grace; a later inbound event closes its stream.
func TestRenewedOutCertificateExpiresAfterGraceAndClosesStream(t *testing.T) {
	e := newEnv(t)
	var offset atomic.Int64 // seconds
	e.f.now = func() time.Time { return time.Now().Add(time.Duration(offset.Load()) * time.Second) }
	e.f.certCheck = 40 * time.Millisecond
	a := e.enroll("nodea")

	old := a.open()
	old.send(0, hello("i", 0, ""))
	old.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })

	key2, csr2 := newCSR(t)
	cli := agentv1connect.NewEnrollmentServiceClient(e.httpClient(&a.cert, testSNI), e.srv.URL)
	rr, err := cli.Renew(e.ctx, connect.NewRequest(&agentv1.RenewRequest{CsrDer: csr2}))
	if err != nil {
		t.Fatal(err)
	}
	fresh := e.identity(a.nodeID, key2, rr.Msg.CertificatePem)

	// Inside the grace both work, and the running stream is left alone.
	offset.Store(1)
	old.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: 1}}})
	select {
	case err := <-old.errc:
		t.Fatalf("stream closed inside the grace: %v", err)
	default:
	}
	// A second Renew with the old certificate (proof that it still works inside the grace) replaces the first
	// answer, and must not push the end of the old certificate out.
	rr2, err := cli.Renew(e.ctx, connect.NewRequest(&agentv1.RenewRequest{CsrDer: csr2}))
	if err != nil {
		t.Fatal(err)
	}
	fresh = e.identity(a.nodeID, key2, rr2.Msg.CertificatePem)

	offset.Store(int64(oldCertGrace/time.Second) + 60)
	old.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: 1}}})
	if err := old.ended(); code(err) != connect.CodeUnauthenticated {
		t.Errorf("stream on the renewed-out certificate: %v, want UNAUTHENTICATED", err)
	}
	stale := a.open()
	stale.st.Send(hello("i3", 0, ""))
	if err := stale.ended(); err == nil {
		t.Error("the renewed-out certificate still connects after the grace")
	}
	nc := fresh.open()
	nc.send(0, hello("i4", 0, ""))
	nc.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
}

// F9: a stream is closed on the next inbound event after its certificate expires.
func TestStreamClosesWhenItsCertificateExpires(t *testing.T) {
	e := newEnv(t)
	var offset atomic.Int64
	e.f.now = func() time.Time { return time.Now().Add(time.Duration(offset.Load()) * time.Second) }
	e.f.certCheck = 40 * time.Millisecond
	a := e.enroll("nodea")
	c := a.open()
	c.send(0, hello("i", 0, ""))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	offset.Store(int64(nodeCertTTL/time.Second) + 3600)
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: 1}}})
	if err := c.ended(); code(err) != connect.CodeUnauthenticated {
		t.Errorf("stream with an expired certificate: %v", err)
	}
}

// F9: retiring a node closes its stream at the next check even without the retire path's own cancel
// (certificates are revoked in the database).
func TestStreamClosesWhenItsCertificateIsRevoked(t *testing.T) {
	e := newEnv(t)
	var offset atomic.Int64
	e.f.now = func() time.Time { return time.Now().Add(time.Duration(offset.Load()) * time.Second) }
	e.f.certCheck = 40 * time.Millisecond
	a := e.enroll("nodea")
	c := a.open()
	c.send(0, hello("i", 0, ""))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	e.exec(`UPDATE node_cert SET revoked_at = ?, revoke_reason = 'test' WHERE node_id = ?`, time.Now().Unix()-1, a.nodeID)
	offset.Store(1)
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: 1}}})
	if err := c.ended(); code(err) != connect.CodeUnauthenticated {
		t.Errorf("stream with a revoked certificate: %v", err)
	}
}

// F8: nothing in what the agent endpoint presents names the product.
func TestCertificatesAreProductNeutral(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")

	var chain []*x509.Certificate
	cfg := &tls.Config{RootCAs: e.pool, ServerName: testSNI, MinVersion: tls.VersionTLS13,
		VerifyConnection: func(cs tls.ConnectionState) error { chain = cs.PeerCertificates; return nil }}
	conn, err := tls.Dial("tcp", e.srv.Listener.Addr().String(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if len(chain) != 2 {
		t.Fatalf("chain of %d certificates", len(chain))
	}
	leaf, caCert := chain[0], chain[1]
	if leaf.Subject.CommonName != testSNI || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != testSNI {
		t.Errorf("server certificate names %q %v, want the host name only", leaf.Subject.CommonName, leaf.DNSNames)
	}
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(caCert.Subject.CommonName) {
		t.Errorf("CA CN %q", caCert.Subject.CommonName)
	}
	for name, c := range map[string]*x509.Certificate{"server": leaf, "CA": caCert, "node": a.leaf} {
		if bytes.Contains(bytes.ToLower(c.Raw), []byte("mistgate")) {
			t.Errorf("%s certificate contains the product name: subject %q issuer %q uris %v", name, c.Subject, c.Issuer, c.URIs)
		}
	}
	if !bytes.Equal(leaf.RawIssuer, caCert.RawSubject) {
		t.Error("issuer is not the CA subject")
	}
}

// A node's location, provider and notes are limited in characters, as the admin's forms count them, not in bytes.
func TestNodeTextCountsCharacters(t *testing.T) {
	for _, c := range []struct {
		s   string
		max int
		ok  bool
	}{
		{strings.Repeat("я", maxNodeText), maxNodeText, true},
		{strings.Repeat("я", maxNodeText+1), maxNodeText, false},
		{strings.Repeat("ж", maxNodeNotes), maxNodeNotes, true},
		{strings.Repeat("ж", maxNodeNotes+1), maxNodeNotes, false},
		{"Франкфурт\x7f", maxNodeText, false},
		{"bad\xff", maxNodeText, false},
	} {
		if got := validText(c.s, c.max); got != c.ok {
			t.Errorf("validText(%d runes, %d) = %v, want %v", len([]rune(c.s)), c.max, got, c.ok)
		}
	}
}
