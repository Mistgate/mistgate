package trusttunnel

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/front"
	"github.com/mistgate/mistgate/internal/node/front/fronttest"
	"github.com/mistgate/mistgate/internal/node/sitehttp/sitehttptest"
	"github.com/mistgate/mistgate/internal/plugin"
)

// sharedHarness is a harness whose engine gets its listener from a real front on the harness's port, as when another
// service shares that port.
func sharedHarness(t *testing.T, creds []plugin.UserCred, outbound *testEgress) (*testHarness, *fronttest.Port) {
	t.Helper()
	shared := fronttest.New(t, front.Route{ID: "de1", Names: []string{"example.com"}, KeepAlive: &KeepAlive})
	h := newHarnessEnv(t, creds, outbound, timings{}, func(env *nodeengine.Env) { env.SharedListener = shared.Listener })
	return h, shared
}

// On a shared port the tunnel works as on its own: a CONNECT with a half-close both ways, counted once, and the site
// answers a stranger with the frames of every other site of the node.
func TestSharedPortTunnelAndSite(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	backend := echoBackend(t)
	h, shared := sharedHarness(t, []plugin.UserCred{alice.cred}, &testEgress{tcpAddr: backend.listener.Addr().String()})
	client := newHTTP2Client(t, h.addr)

	response, writer := client.connect(t, "example.com:80", authValue(t, alice), true)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d", response.StatusCode)
	}
	payload := []byte("half-close travels through the front")
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want exact payload", got)
	}
	if up, down := trafficFor(collect(t, h.e), "alice"); up != uint64(len(payload)) || down != uint64(len(payload)) {
		t.Fatalf("traffic up/down = %d/%d, want %d/%d", up, down, len(payload), len(payload))
	}

	if resp := client.get(t); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a stranger's GET got %d", resp.StatusCode)
	}
	conn, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	sitehttptest.Check(t, "TrustTunnel behind the front", conn)
	if stats := shared.Front().Stats(); stats.Delivered["de1"] < 2 || stats.UnknownName != 0 {
		t.Fatalf("front stats = %+v", stats)
	}
}

// The site's limit of anonymous connections works per source, so the front must hand over the client's own address: a
// ninth connection from one address closes the oldest, and the others stay.
func TestSharedPortSiteSeesTheClientAddress(t *testing.T) {
	h, _ := sharedHarness(t, nil, &testEgress{})
	conns := make([]*tls.Conn, 0, 9)
	for range 9 {
		c, err := tls.Dial("tcp", h.addr, &tls.Config{ServerName: "example.com", InsecureSkipVerify: true, NextProtos: []string{"h2"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		conns = append(conns, c)
	}
	if !closesWithin(conns[0], 5*time.Second) {
		t.Fatal("the oldest of nine anonymous connections from one address was not closed: the limiter did not see its source")
	}
	if closesWithin(conns[8], 300*time.Millisecond) {
		t.Fatal("the newest connection was closed")
	}
}

// closesWithin reads (the server's first HTTP/2 frames included) until the connection ends or d passes.
func closesWithin(c *tls.Conn, d time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(d))
	for {
		if _, err := c.Read(make([]byte, 4096)); err != nil {
			ne, timeout := err.(net.Error)
			return !(timeout && ne.Timeout())
		}
	}
}
