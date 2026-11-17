package vless

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xtls/xray-core/proxy"

	"github.com/mistgate/mistgate/internal/node/front"
	"github.com/mistgate/mistgate/internal/node/front/fronttest"
	"github.com/mistgate/mistgate/internal/node/nets"
	"github.com/mistgate/mistgate/internal/node/netstest"
	"github.com/mistgate/mistgate/internal/plugin"
)

// trackedConns are the connections Xray holds for the inbound right now.
func trackedConns(in *inbound) []*trackedTCPConn {
	tr := in.connTracker
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return slices.Collect(maps.Keys(tr.conns))
}

// REALITY on a shared port: the real client gets through the front for both transports, Xray holds the client's own
// address, and it unwraps the connection to the TCP socket once the hello is read (Vision keeps splice on it).
func TestSharedRealityThroughTheFront(t *testing.T) {
	checkLoopbackSourceBinding(t)
	for _, tc := range []struct{ transport, flow string }{{"tcp", "xtls-rprx-vision"}, {"xhttp", ""}} {
		t.Run(tc.transport, func(t *testing.T) {
			f := newRuntimeFixture(t, false)
			agg := nets.New(2)
			db, err := netstest.LoopbackDB(64500)
			if err != nil {
				t.Fatal(err)
			}
			agg.SetTable(db, "fixture-sha")
			f.engine.env.Nets = agg
			spec := f.spec("inb_de1", tc.transport)
			shared := fronttest.New(t, front.Route{ID: spec.ID, Names: []string{"localhost"}})
			f.engine.env.SharedListener = shared.Listener
			// three people, so that the counters keep their network (k=3)
			ids := []string{"alice", "bob", "de1"}
			uuids := []string{"00000000-0000-4000-8000-000000000001", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000003"}
			var creds []plugin.UserCred
			for i, id := range ids {
				c := testCredential("crd_"+id, uuids[i])
				c.UserID = id
				creds = append(creds, c)
			}
			if _, err := f.engine.Apply(context.Background(), spec, creds); err != nil {
				t.Fatal(err)
			}
			echo := startEchoServer(t)
			var open []net.Conn
			for _, c := range creds {
				client := newXrayClientShortIDFrom(t, "localhost", "6ba85179", spec.Listen.Port, tc.transport, c.CredID, stringID(c), tc.flow, false, "127.0.0.2")
				conn := client.dial(t, echo.Addr().String())
				defer conn.Close()
				if _, err := exchange(conn, payload(2<<20)); err != nil {
					t.Fatalf("exchange through the front: %v; logs: %s", err, f.logs.String())
				}
				open = append(open, conn)
			}

			in := f.engine.inbounds[spec.ID]
			conns := trackedConns(in)
			if len(conns) == 0 {
				t.Fatal("Xray holds no connection")
			}
			for _, c := range conns {
				if addr, ok := c.RemoteAddr().(*net.TCPAddr); !ok || !addr.IP.Equal(net.ParseIP("127.0.0.2")) {
					t.Fatalf("the tracker sees the client at %v, want its own 127.0.0.2", c.RemoteAddr())
				}
				if raw, _, _ := proxy.UnwrapRawConn(c); raw == nil {
					t.Fatal("no raw connection")
				} else if _, ok := raw.(*net.TCPConn); !ok {
					t.Fatalf("Xray unwraps the connection to %T, want the TCP socket (the head was read: splice must stay)", raw)
				}
			}
			if tc.transport == "xhttp" {
				in.connTracker.mu.Lock()
				found := false
				for ap := range in.connTracker.byAddr {
					found = found || ap.Addr().String() == "127.0.0.2"
				}
				in.connTracker.mu.Unlock()
				if !found {
					t.Fatal("XHTTP finds no connection by the client's address")
				}
			}
			for _, conn := range open {
				_ = conn.Close()
			}
			if tc.transport == "tcp" {
				networks, _ := agg.Take()
				if len(networks) != 1 || networks[0].Asn != 64500 || networks[0].Connects != 3 {
					t.Fatalf("network counters = %+v", networks)
				}
			}
			if stats := shared.Front().Stats(); stats.Delivered[spec.ID] == 0 || stats.UnknownName != 0 {
				t.Fatalf("front stats = %+v", stats)
			}
		})
	}
}

// What the tracker wraps is the front's connection, and it half-closes like the socket: the server ends its side after the
// client's request (the bytes the front read come first), the client sees the end and can still send, and its own end
// reaches the server. Xray's inbound conn needs this for the same reason the engine's dispatch does (halfclose_test.go).
func TestHalfCloseThroughTheFront(t *testing.T) {
	shared := fronttest.New(t, front.Route{ID: "x", Names: []string{"localhost"}})
	ln := shared.Listener("x", reservePort(t))
	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	const request = "GET / HTTP/1.0\r\n\r\n" // not TLS: the front hands it to the default route as it is
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	accepted, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	server := newTCPConnTracker("x", 1).track(accepted)
	defer server.Close()
	_ = server.SetDeadline(time.Now().Add(10 * time.Second))
	got := make([]byte, len(request))
	if _, err := io.ReadFull(server, got); err != nil || string(got) != request {
		t.Fatalf("the server read %q, %v; want the request the front had read", got, err)
	}
	if err := server.(interface{ CloseWrite() error }).CloseWrite(); err != nil {
		t.Fatalf("CloseWrite on the connection the front handed over: %v", err)
	}
	if n, err := client.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("the client read %d bytes, %v after the server's half-close; want the end", n, err)
	}
	if _, err := io.WriteString(client, "more"); err != nil {
		t.Fatal(err)
	}
	if err := client.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if rest, err := io.ReadAll(server); err != nil || string(rest) != "more" {
		t.Fatalf("the server read %q, %v after its own half-close; want the client's last bytes", rest, err)
	}
}

// The route outlives the engine's listener: a REALITY inbound whose spec changes restarts, takes its route again (and the
// private socket Xray opens for it) and serves the next client through the same front.
func TestSharedRealityRestartKeepsTheRoute(t *testing.T) {
	f := newRuntimeFixture(t, false)
	spec := f.spec("node-a", "tcp")
	shared := fronttest.New(t, front.Route{ID: spec.ID, Names: []string{"localhost"}})
	f.engine.env.SharedListener = shared.Listener
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	echo := startEchoServer(t)
	for i, shortIDs := range []string{`"6ba85179"`, `"6ba85179","0a1b2c3d"`} {
		if i > 0 {
			spec.Settings = json.RawMessage(strings.Replace(string(spec.Settings), `"short_ids":["6ba85179"]`, `"short_ids":[`+shortIDs+`]`, 1))
			report, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred})
			if err != nil || !report.Restarted {
				t.Fatalf("the spec change did not restart the inbound: %+v, %v", report, err)
			}
		}
		conn := newXrayClient(t, spec.Listen.Port, "tcp", cred.CredID, stringID(cred)).dial(t, echo.Addr().String())
		if _, err := exchange(conn, payload(64<<10)); err != nil {
			t.Fatalf("round %d: %v; logs: %s", i, err, f.logs.String())
		}
		_ = conn.Close()
	}
	if stats := shared.Front().Stats(); stats.Delivered[spec.ID] < 2 || stats.Refused != 0 {
		t.Fatalf("front stats = %+v", stats)
	}
}
