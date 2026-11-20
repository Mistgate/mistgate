package mtproto

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/front"
	"github.com/mistgate/mistgate/internal/node/front/fronttest"
	"github.com/mistgate/mistgate/internal/node/nets"
	"github.com/mistgate/mistgate/internal/node/netstest"
	"github.com/mistgate/mistgate/internal/node/sitehttp/sitehttptest"
)

// On a port the agent shares the engine accepts from its route of the front, and everything it does with a connection
// works as when it binds the port: the client's own address is in Opened, the decoy answers a browser, Let's Encrypt's
// TLS-ALPN-01 and a stranger with another name, and the decoy speaks HTTP/2 like every other site.
func TestSharedPortGivesTheEngineItsRoute(t *testing.T) {
	agg := nets.New(2)
	db, err := netstest.LoopbackDB(64500)
	if err != nil {
		t.Fatal(err)
	}
	agg.SetTable(db, "fixture-sha")
	certs := &fakeCerts{acme: testCert(t, "acme."+ownDomain), self: testCert(t, ownDomain)}
	shared := fronttest.New(t, front.Route{ID: "i1", Names: []string{ownDomain}})
	env := engine.Env{Certs: certs, Nets: agg, SharedListener: shared.Listener, Masquerade: func(string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "decoy") })
	}}
	c := credential("crd_alice", key(1))
	c.UserID = "alice"
	f := newFixture(t, env, `{"domains":["de1.example.com"],"fallback":"decoy"}`, c)

	client := f.client(key(1), ownDomain)
	in := f.inbound()
	in.smu.Lock()
	if len(in.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(in.sessions))
	}
	for _, s := range in.sessions {
		if want := client.Conn.LocalAddr().String(); s.info.Remote != want || s.info.SNI != ownDomain {
			t.Fatalf("Opened got remote %q SNI %q, want the client's own %q and %q", s.info.Remote, s.info.SNI, want, ownDomain)
		}
	}
	in.smu.Unlock()
	_ = client.Conn.Close()
	if _, people := agg.Take(); len(people) != 1 || people[0].Asn != 64500 {
		t.Fatalf("person counters = %+v", people)
	}

	if body, cn := f.get(ownDomain); body != "decoy" || cn != ownDomain {
		t.Fatalf("a browser got %q from %q", body, cn)
	}
	if body, _ := f.get(otherDomain); body != "decoy" {
		t.Fatalf("a stranger's name got %q", body)
	}
	conn, err := tls.Dial("tcp", f.addr, &tls.Config{ServerName: ownDomain, NextProtos: []string{"acme-tls/1"}, InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("TLS-ALPN-01: %v", err)
	}
	cs := conn.ConnectionState()
	_ = conn.Close()
	if cs.NegotiatedProtocol != "acme-tls/1" || cs.PeerCertificates[0].Subject.CommonName != "acme."+ownDomain {
		t.Fatalf("TLS-ALPN-01 got %q from %q", cs.NegotiatedProtocol, cs.PeerCertificates[0].Subject.CommonName)
	}
	sitehttptest.Check(t, "MTProto decoy behind the front", f.dial())

	// all five connections came through the front; the names it did not know (the stranger's, the check's) went to the default route
	if stats := shared.Front().Stats(); stats.Delivered["i1"] != 5 || stats.UnknownName != 2 {
		t.Fatalf("front stats = %+v", stats)
	}
}

func TestWebInboundThroughSharedFront(t *testing.T) {
	agg := nets.New(2)
	db, err := netstest.LoopbackDB(64500)
	if err != nil {
		t.Fatal(err)
	}
	agg.SetTable(db, "web-shared-fixture-sha")
	certs := &fakeCerts{acme: testCert(t, "acme."+webDomain), self: testCert(t, webDomain)}
	shared := fronttest.New(t, front.Route{ID: "i1", Names: []string{webDomain}})
	cred := credential("alice", key(7))
	cred.UserID = "alice"
	f := newFixture(t, engine.Env{
		Certs: certs, Nets: agg, SharedListener: shared.Listener,
		Masquerade: func(string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "shared site") })
		},
	}, `{"domains":["tg.example.com"],"fallback":"decoy","mode":"web"}`, cred)

	client := newWebClient(t, f, key(7), webDomain)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := client.Bootstrap(ctx); err != nil {
		cancel()
		t.Fatalf("bootstrap through front: %v", err)
	}
	cancel()
	if body, _ := f.getPath(webDomain, "/?bridge="+strings.Repeat("A", 43)); body != "shared site" {
		t.Fatalf("shared-front stranger got %q", body)
	}
	stream, mt := openWebStream(t, client, key(7))
	webRoundTrip(t, mt)

	collected, err := f.e.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(collected.Sessions) != 1 || collected.Sessions[0].CredID != "alice" {
		t.Fatalf("shared Web collection = %+v", collected)
	}
	_ = stream.Close() // the core reports final traffic when the stream ends
	waitForWebTraffic(t, f)
	_, people := agg.Take()
	if len(people) != 1 || people[0].CredId != "alice" || people[0].Asn != 64500 {
		t.Fatalf("shared Web ASN counters = %+v", people)
	}

	sitehttptest.Check(t, "MTProto Web behind the shared front", f.dial())
	alpn, err := tls.Dial("tcp", f.addr, &tls.Config{ServerName: webDomain, NextProtos: []string{"acme-tls/1"}, InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("shared TLS-ALPN-01: %v", err)
	}
	state := alpn.ConnectionState()
	_ = alpn.Close()
	if state.NegotiatedProtocol != "acme-tls/1" || state.PeerCertificates[0].Subject.CommonName != "acme."+webDomain {
		t.Fatalf("shared TLS-ALPN-01 got %q from %q", state.NegotiatedProtocol, state.PeerCertificates[0].Subject.CommonName)
	}
	if stats := shared.Front().Stats(); stats.Delivered["i1"] < 4 {
		t.Fatalf("shared front delivered only %d connections to Web inbound", stats.Delivered["i1"])
	}
}
