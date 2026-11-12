package vless

import (
	"bytes"
	"context"
	"net"
	"runtime"
	"testing"
	"time"

	agentpb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/nets"
	"github.com/mistgate/mistgate/internal/node/netstest"
	"github.com/mistgate/mistgate/internal/plugin"
	"google.golang.org/protobuf/proto"
)

func TestNetworkCountsOnlyForRawRealityTCP(t *testing.T) {
	for _, transport := range []string{"tcp", "xhttp"} {
		t.Run(transport, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			agg := nets.New(2)
			db, err := netstest.LoopbackDB(64500)
			if err != nil {
				t.Fatal(err)
			}
			agg.SetTable(db, "fixture-sha")
			f.engine.env.Nets = agg
			spec := f.spec("inb_de1", transport)
			cred := testCredential("crd_alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
			cred.UserID = "alice"
			if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
				t.Fatal(err)
			}
			echo := startEchoServer(t)
			client := newXrayClient(t, spec.Listen.Port, transport, cred.CredID, stringID(cred))
			conn := client.dial(t, echo.Addr().String())
			if _, err := exchange(conn, []byte("network count")); err != nil {
				t.Fatal(err)
			}
			_ = conn.Close()

			networks, people := agg.Take()
			if transport == "xhttp" {
				if len(networks) != 0 || len(people) != 0 {
					t.Fatalf("XHTTP produced network rows: %+v %+v", networks, people)
				}
				return
			}
			if len(networks) != 1 || networks[0].Asn != 0 || networks[0].Attempts == 0 || networks[0].Connects != 1 || networks[0].People != 1 {
				t.Fatalf("network counters = %+v", networks)
			}
			if len(people) != 1 || people[0].CredId != "crd_alice" || people[0].Asn != 64500 || people[0].Connects != 1 {
				t.Fatalf("person counters = %+v", people)
			}
		})
	}
}

func TestTLSFrontCountsDirectPeerWithoutPersonIdentity(t *testing.T) {
	f, certs := newTLSRuntimeFixture(t)
	agg := nets.New(2)
	db, err := netstest.LoopbackDB(64500)
	if err != nil {
		t.Fatal(err)
	}
	agg.SetTable(db, "fixture-sha")
	f.engine.env.Nets = agg
	certs.setIssued()
	spec := f.tlsSpec("inb_de1", "xhttp", "auto")
	cred := testCredential("crd_alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	cred.UserID = "alice"
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	client := newXrayTLSClient(t, certs, spec.Listen.Port, "xhttp", "auto", "h2", cred.CredID, stringID(cred))
	conn := client.dial(t, echo.Addr().String())
	if _, err := exchange(conn, []byte("TLS front network count")); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	networks, people := agg.Take()
	if len(networks) != 1 || networks[0].Asn != 0 || networks[0].Attempts == 0 || networks[0].Connects != 1 || networks[0].People != 0 {
		t.Fatalf("TLS front network counters = %+v", networks)
	}
	if len(people) != 0 {
		t.Fatalf("TLS front emitted person rows without a visible credential: %+v", people)
	}
}

func TestRealityLoopbackAddressNeverLeavesEngine(t *testing.T) {
	checkLoopbackSourceBinding(t)
	f := newRuntimeFixture(t, false)
	db, err := netstest.LoopbackDB(64500)
	if err != nil {
		t.Fatal(err)
	}
	agg := nets.New(1)
	agg.SetTable(db, "fixture-sha")
	f.engine.env.Nets = agg
	spec := f.spec("inb_de1", "tcp")
	ids := []string{"alice", "bob", "de1"}
	uuid := []string{
		"00000000-0000-4000-8000-000000000001",
		"00000000-0000-4000-8000-000000000002",
		"00000000-0000-4000-8000-000000000003",
	}
	creds := make([]plugin.UserCred, 0, len(ids))
	for i, id := range ids {
		c := testCredential("crd_"+id, uuid[i])
		c.UserID = id
		creds = append(creds, c)
	}
	if _, err := f.engine.Apply(context.Background(), spec, creds); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	for i := range ids {
		client := newXrayClientShortIDFrom(t, "localhost", "6ba85179", spec.Listen.Port, "tcp", creds[i].CredID, stringID(creds[i]), "xtls-rprx-vision", false, "127.0.0.2")
		conn := client.dial(t, echo.Addr().String())
		if _, err := exchange(conn, []byte("network count")); err != nil {
			t.Fatal("REALITY loopback-source exchange failed")
		}
		_ = conn.Close()
	}

	networks, people := agg.Take()
	if len(networks) != 1 || networks[0].Asn != 64500 || networks[0].Connects != 3 || networks[0].People != 3 || len(people) != 0 {
		t.Fatalf("REALITY network counters = %+v", networks)
	}
	wire, err := proto.Marshal(&agentpb.StatsBatch{Networks: networks})
	if err != nil {
		t.Fatal(err)
	}
	forms, err := netstest.AddressForms("127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range forms {
		if bytes.Contains(wire, form) || bytes.Contains([]byte(f.logs.String()), form) {
			t.Fatal("REALITY output contains a client address form")
		}
	}
}

type countedAddr struct{ calls *int }

func (a countedAddr) Network() string { return "tcp" }
func (a countedAddr) String() string {
	(*a.calls)++
	return "127.0.0.2:1234"
}

type remoteAddressConn struct {
	net.Conn
	addr net.Addr
}

func (c remoteAddressConn) RemoteAddr() net.Addr { return c.addr }

func TestRealityModeZeroDoesNotInspectPeerAddress(t *testing.T) {
	agg := nets.New(0)
	db, err := netstest.LoopbackDB(64500)
	if err != nil {
		t.Fatal(err)
	}
	agg.SetTable(db, "fixture-sha")
	tracker := newTCPConnTracker("inb_de1", 1)
	tracker.stalls, tracker.nets = true, agg
	left, right := net.Pipe()
	calls := 0
	tracked := tracker.track(remoteAddressConn{Conn: left, addr: countedAddr{calls: &calls}})
	_ = tracked.Close()
	_ = right.Close()
	if calls != 0 {
		t.Fatalf("mode-zero engine inspected a peer address %d times", calls)
	}
}

func checkLoopbackSourceBinding(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	local := &net.TCPAddr{IP: net.ParseIP("127.0.0.2")}
	conn, err := (&net.Dialer{LocalAddr: local, Timeout: time.Second}).Dial("tcp", listener.Addr().String())
	if err != nil {
		if runtime.GOOS == "windows" {
			t.Skip("127.0.0.2 source binding is unavailable on Windows; this check runs in WSL")
		}
		t.Fatal("could not bind the loopback source for the REALITY privacy check")
	}
	_ = conn.Close()
}
