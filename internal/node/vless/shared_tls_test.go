package vless

import (
	"context"
	"crypto/tls"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	agentpb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/front"
	"github.com/mistgate/mistgate/internal/node/front/fronttest"
	"github.com/mistgate/mistgate/internal/node/nets"
	"github.com/mistgate/mistgate/internal/node/netstest"
	"github.com/mistgate/mistgate/internal/node/sitehttp/sitehttptest"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The TLS front of a VLESS inbound (XHTTP, WebSocket) accepts from its route: real clients carry traffic through the front.
func TestSharedTLSFrontThroughTheFront(t *testing.T) {
	for _, tc := range []struct{ transport, mode, alpn string }{
		{"xhttp", "stream-up", "h2"}, {"xhttp", "packet-up", "h2"}, {"ws", "", "http/1.1"},
	} {
		t.Run(tc.transport+"/"+tc.mode, func(t *testing.T) {
			f, certs := newTLSRuntimeFixture(t)
			certs.setIssued()
			spec := f.tlsSpec("inb_de1", tc.transport, tc.mode)
			shared := fronttest.New(t, front.Route{ID: spec.ID, Names: []string{tlsTestServerName}})
			f.engine.env.SharedListener = shared.Listener
			cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
				t.Fatal(err)
			}
			echo := startEchoServer(t)
			client := newXrayTLSClient(t, certs, spec.Listen.Port, tc.transport, tc.mode, tc.alpn, cred.CredID, stringID(cred))
			conn := client.dial(t, echo.Addr().String())
			defer conn.Close()
			const size = 64 << 10
			if _, err := exchange(conn, payload(size)); err != nil {
				t.Fatalf("exchange through the front: %v; health=%+v logs=%s", err, f.engine.Health(), f.logs.String())
			}
			if up, down, _ := waitTraffic(t, f.engine, cred.CredID, size); !withinPercent(up, size, 1) || !withinPercent(down, size, 1) {
				t.Fatalf("traffic counters up=%d down=%d, want %d in both directions", up, down, size)
			}
			if stats := shared.Front().Stats(); stats.Delivered[spec.ID] == 0 || stats.UnknownName != 0 {
				t.Fatalf("front stats = %+v", stats)
			}
		})
	}
}

// TLS-ALPN-01 reaches the node's certificate source through the front, for the route's name and for a stranger's (the
// default route), and the address the front hands over is the client's: the counters keep its network.
func TestSharedTLSFrontALPNAndAddress(t *testing.T) {
	f, certs := newTLSRuntimeFixture(t)
	certs.setIssued()
	agg := nets.New(2)
	db, err := netstest.LoopbackDB(64500)
	if err != nil {
		t.Fatal(err)
	}
	agg.SetTable(db, "fixture-sha")
	f.engine.env.Nets = agg
	spec := f.tlsSpec("inb_de1", "xhttp", "auto")
	shared := fronttest.New(t, front.Route{ID: spec.ID, Names: []string{tlsTestServerName}})
	f.engine.env.SharedListener = shared.Listener
	if _, err := f.engine.Apply(context.Background(), spec, nil); err != nil {
		t.Fatal(err)
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(spec.Listen.Port)))
	for _, protocol := range []string{"h2", "http/1.1", "acme-tls/1"} {
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: tlsTestServerName, InsecureSkipVerify: true, NextProtos: []string{protocol}})
		if err != nil {
			t.Fatalf("%s: TLS handshake through the front: %v", protocol, err)
		}
		if got := conn.ConnectionState().NegotiatedProtocol; got != protocol {
			t.Fatalf("negotiated ALPN %q, want %q", got, protocol)
		}
		if cn := conn.ConnectionState().PeerCertificates[0].Subject.CommonName; protocol == "acme-tls/1" && !strings.Contains(cn, "challenge") {
			t.Fatalf("TLS-ALPN-01 presented %q, want the challenge certificate", cn)
		}
		_ = conn.Close()
	}
	// the validation of a name the route does not hold is the default route's: the same inbound answers
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: "other.example.com", InsecureSkipVerify: true, NextProtos: []string{"acme-tls/1"}})
	if err != nil {
		t.Fatalf("TLS-ALPN-01 for another name: %v", err)
	}
	_ = conn.Close()
	if stats := shared.Front().Stats(); stats.Delivered[spec.ID] != 4 || stats.UnknownName != 1 {
		t.Fatalf("front stats = %+v", stats)
	}

	// A validation never becomes a connect, so its row keeps the network of the address the front handed over. The two connects above
	// (h2, http/1.1) are counted in the background once the front has dialed the inbound: they are taken first, or a late one lands in
	// the row below.
	for seen, deadline := uint32(0), time.Now().Add(5*time.Second); seen < 2 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		rows, _ := agg.Take()
		for _, r := range rows {
			seen += r.Connects
		}
	}
	conn, err = tls.Dial("tcp", addr, &tls.Config{ServerName: tlsTestServerName, InsecureSkipVerify: true, NextProtos: []string{"acme-tls/1"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	var networks []*agentpb.NetworkCounters
	for deadline := time.Now().Add(5 * time.Second); len(networks) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		networks, _ = agg.Take()
	}
	if len(networks) != 1 || networks[0].Asn != 64500 || networks[0].Attempts != 1 || networks[0].Connects != 0 {
		t.Fatalf("network counters = %+v", networks)
	}
}

// The REALITY inbound with the node's own site as its target, on a shared port: the site answers a browser, Let's
// Encrypt and a stranger's name through the front as it does alone, speaks HTTP/2 like every other site, and a client
// of ours is served.
func TestSharedSelfSiteThroughTheFront(t *testing.T) {
	f := newSelfFixture(t)
	spec := f.spec("node-a-self")
	shared := fronttest.New(t, front.Route{ID: spec.ID, Names: []string{selfName}})
	f.e.env.SharedListener = shared.Listener
	const uuid = "66ad4540-b58c-4ad2-9926-ea63445a9b57"
	f.apply(t, spec, testCredential("c1", uuid))
	port := spec.Listen.Port
	f.certs.setIssued()

	if body, leaf := visit(t, port, "http/1.1"); body != selfMarker || pinOf(leaf) != infoOfCert(f.certs.acmeChain).PinSHA256 {
		t.Fatalf("a browser got %q with certificate %s", body, pinOf(leaf))
	}
	conn, err := tls.Dial("tcp", "127.0.0.1:"+strconv.Itoa(int(port)), &tls.Config{ServerName: selfName, InsecureSkipVerify: true, NextProtos: []string{"acme-tls/1"}})
	if err != nil {
		t.Fatalf("TLS-ALPN-01 handshake through the front and REALITY: %v", err)
	}
	state := conn.ConnectionState()
	_ = conn.Close()
	if state.NegotiatedProtocol != "acme-tls/1" || pinOf(state.PeerCertificates[0]) != infoOfCert(f.certs.challenge).PinSHA256 {
		t.Fatalf("validation got protocol %q and certificate %s, want the challenge certificate", state.NegotiatedProtocol, pinOf(state.PeerCertificates[0]))
	}
	// the check's name is one the route does not hold: the default route, which is this inbound, takes it
	raw, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(int(port)))
	if err != nil {
		t.Fatal(err)
	}
	sitehttptest.Check(t, "VLESS self behind the front", raw)

	echo := startEchoServer(t)
	client := newXrayClientSNI(t, selfName, port, "xhttp", "c1", uuid, "", false)
	conn2 := client.dial(t, echo.Addr().String())
	defer conn2.Close()
	if _, err := exchange(conn2, payload(200_000)); err != nil {
		t.Fatalf("traffic through the front and the REALITY inbound with target self: %v", err)
	}
	if stats := shared.Front().Stats(); stats.UnknownName != 1 || stats.Delivered[spec.ID] < 4 {
		t.Fatalf("front stats = %+v", stats)
	}
}
