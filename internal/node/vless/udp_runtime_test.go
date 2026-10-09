package vless

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/transport"
	"github.com/xtls/xray-core/transport/pipe"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestVLESSUDPDispatchCountsAndClosesOneSocketPerLink(t *testing.T) {
	for _, transportName := range []string{"tcp", "xhttp"} {
		for _, mode := range []string{"plain", "xudp"} {
			t.Run(transportName+"/"+mode, func(t *testing.T) {
				f := newRuntimeFixture(t, false)
				egress := &fakeUDPEgress{}
				f.engine.out = func(string) (engine.Egress, error) { return egress, nil }
				spec := f.spec("node-a", transportName)
				cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
				if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
					t.Fatal(err)
				}
				initial, err := xnet.ParseDestination("udp:203.0.113.10:53")
				if err != nil {
					t.Fatal(err)
				}
				targets := []xnet.Destination{initial}
				if mode == "xudp" {
					second, err := xnet.ParseDestination("udp:203.0.113.20:5353")
					if err != nil {
						t.Fatal(err)
					}
					targets = append(targets, second)
				}
				upReader, upWriter := pipe.New()
				downReader, downWriter := pipe.New()
				link := &transport.Link{Reader: upReader, Writer: downWriter}
				ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: &protocol.MemoryUser{Email: cred.CredID}})
				ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: initial}})
				done := make(chan error, 1)
				in := f.engine.inbounds[spec.ID]
				go func() { done <- in.dispatch(ctx, link) }()

				var bytesSent uint64
				for i, target := range targets {
					body := []byte{byte('a' + i), 'u', 'd', 'p'}
					packet := buf.FromBytes(body)
					if mode == "xudp" {
						packet.UDP = &target
					}
					if err := upWriter.WriteMultiBuffer(buf.MultiBuffer{packet}); err != nil {
						t.Fatal(err)
					}
					bytesSent += uint64(len(body))
					reply, err := downReader.ReadMultiBufferTimeout(3 * time.Second)
					if err != nil {
						t.Fatal(err)
					}
					if got := reply.String(); got != string(body) {
						buf.ReleaseMulti(reply)
						t.Fatalf("UDP response = %q, want %q", got, body)
					}
					buf.ReleaseMulti(reply)
				}
				_ = upWriter.Close()
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("UDP link did not end after client EOF")
				}
				if got := egress.dials.Load(); got != 1 {
					t.Fatalf("Egress.UDP calls = %d, want one per link", got)
				}
				if egress.conn == nil {
					t.Fatal("Egress.UDP returned no socket")
				}
				select {
				case <-egress.conn.closed:
				case <-time.After(time.Second):
					t.Fatal("UDP socket remained open after the link ended")
				}
				collected, err := f.engine.Collect(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				up, down := trafficOf(collected, cred.CredID)
				if up != bytesSent || down != bytesSent {
					t.Fatalf("UDP traffic counters = up %d/down %d, want %d/%d", up, down, bytesSent, bytesSent)
				}
				if got := egress.conn.writeAddresses(); len(got) != len(targets) {
					t.Fatalf("UDP writes = %d, want %d", len(got), len(targets))
				} else {
					for i, target := range targets {
						if got[i] != target.NetAddr() {
							t.Errorf("UDP write %d destination = %q, want %q", i, got[i], target.NetAddr())
						}
					}
				}
				if got := sessionsOf(collected, cred.CredID); got != 0 {
					t.Fatalf("ended UDP link remained in Collect: %+v", collected.Sessions)
				}
			})
		}
	}
}

func TestNaturalVLESSUDPLinkEndPreservesSibling(t *testing.T) {
	f := newRuntimeFixture(t, false)
	egress := &multiSocketUDPEgress{}
	f.engine.out = func(string) (engine.Egress, error) { return egress, nil }
	spec := f.spec("node-a", "xhttp")
	cred := testCredential("alice", "66ad4540-b58c-4ad2-9926-ea63445a9b57")
	if _, err := f.engine.Apply(context.Background(), spec, []plugin.UserCred{cred}); err != nil {
		t.Fatal(err)
	}
	in := f.engine.inbounds[spec.ID]
	inboundClient, inboundPeer := net.Pipe()
	defer inboundPeer.Close()
	trackedClient := &closeCountConn{Conn: inboundClient}
	firstTarget, err := xnet.ParseDestination("udp:203.0.113.10:53")
	if err != nil {
		t.Fatal(err)
	}
	secondTarget, err := xnet.ParseDestination("udp:203.0.113.20:5353")
	if err != nil {
		t.Fatal(err)
	}
	first := startManualUDPLink(t, in, cred, trackedClient, firstTarget)
	second := startManualUDPLink(t, in, cred, trackedClient, secondTarget)
	first.sendAndExpect(t, []byte("first before end"))
	second.sendAndExpect(t, []byte("sibling before end"))

	_ = first.up.Close()
	select {
	case <-first.done:
	case <-time.After(3 * time.Second):
		t.Fatal("first UDP link did not end after its client EOF")
	}
	if got := trackedClient.closes.Load(); got != 0 {
		t.Fatalf("natural UDP link end closed the shared inbound connection %d times", got)
	}
	second.sendAndExpect(t, []byte("sibling after end"))
	_ = second.up.Close()
	select {
	case <-second.done:
	case <-time.After(3 * time.Second):
		t.Fatal("sibling UDP link did not end after its client EOF")
	}
	if got := egress.dials.Load(); got != 2 {
		t.Fatalf("Egress.UDP calls = %d, want one per link", got)
	}
}

type manualUDPLink struct {
	up   *pipe.Writer
	down *pipe.Reader
	done chan error
}

func startManualUDPLink(t *testing.T, in *inbound, cred plugin.UserCred, inboundConn net.Conn, target xnet.Destination) manualUDPLink {
	t.Helper()
	upReader, upWriter := pipe.New()
	downReader, downWriter := pipe.New()
	link := &transport.Link{Reader: upReader, Writer: downWriter}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{
		User: &protocol.MemoryUser{Email: cred.CredID}, Conn: inboundConn,
	})
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{Target: target}})
	done := make(chan error, 1)
	go func() { done <- in.dispatch(ctx, link) }()
	return manualUDPLink{up: upWriter, down: downReader, done: done}
}

func (l manualUDPLink) sendAndExpect(t *testing.T, body []byte) {
	t.Helper()
	if err := l.up.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes(body)}); err != nil {
		t.Fatal(err)
	}
	reply, err := l.down.ReadMultiBufferTimeout(3 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := reply.String(); got != string(body) {
		buf.ReleaseMulti(reply)
		t.Fatalf("UDP sibling response = %q, want %q", got, body)
	}
	buf.ReleaseMulti(reply)
}

type fakeUDPEgress struct {
	dials atomic.Uint64
	conn  *fakeUDPConn
}

type multiSocketUDPEgress struct {
	dials atomic.Uint64
}

func (*multiSocketUDPEgress) TCP(string) (net.Conn, error) {
	return nil, errors.New("unexpected TCP dial")
}

func (e *multiSocketUDPEgress) UDP(string) (engine.EgressUDP, error) {
	e.dials.Add(1)
	return &fakeUDPConn{reads: make(chan fakeUDPPacket, 8), closed: make(chan struct{})}, nil
}

func (*multiSocketUDPEgress) CheckUDP(string) error { return nil }

type closeCountConn struct {
	net.Conn
	closes atomic.Uint64
}

func (c *closeCountConn) Close() error {
	c.closes.Add(1)
	return c.Conn.Close()
}

func (e *fakeUDPEgress) TCP(string) (net.Conn, error) { return nil, errors.New("unexpected TCP dial") }

func (e *fakeUDPEgress) UDP(string) (engine.EgressUDP, error) {
	e.dials.Add(1)
	e.conn = &fakeUDPConn{reads: make(chan fakeUDPPacket, 8), closed: make(chan struct{})}
	return e.conn, nil
}

func (*fakeUDPEgress) CheckUDP(string) error { return nil }

type fakeUDPPacket struct {
	data []byte
	addr string
}

type fakeUDPConn struct {
	reads     chan fakeUDPPacket
	closed    chan struct{}
	once      sync.Once
	mu        sync.Mutex
	addresses []string
}

func (c *fakeUDPConn) ReadFrom(p []byte) (int, string, error) {
	select {
	case packet := <-c.reads:
		return copy(p, packet.data), packet.addr, nil
	case <-c.closed:
		return 0, "", net.ErrClosed
	}
}

func (c *fakeUDPConn) WriteTo(p []byte, addr string) (int, error) {
	data := append([]byte(nil), p...)
	c.mu.Lock()
	c.addresses = append(c.addresses, addr)
	c.mu.Unlock()
	select {
	case c.reads <- fakeUDPPacket{data: data, addr: addr}:
		return len(p), nil
	case <-c.closed:
		return 0, net.ErrClosed
	}
}

func (c *fakeUDPConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func (c *fakeUDPConn) writeAddresses() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.addresses...)
}
