package trusttunnel

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/egress"
	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	wire "github.com/mistgate/mistgate/internal/trusttunnel"
)

func udpFrame(srcIP string, srcPort int, dstIP string, dstPort int, payload string) wire.UDPFrame {
	return wire.UDPFrame{
		Source:  netip.AddrPortFrom(netip.MustParseAddr(srcIP), uint16(srcPort)),
		Target:  netip.AddrPortFrom(netip.MustParseAddr(dstIP), uint16(dstPort)),
		Payload: []byte(payload),
	}
}

// socketLog is an egress that hands out echo sockets and remembers them in order.
type socketLog struct {
	mu      sync.Mutex
	sockets []*testUDP
}

func (l *socketLog) egress(configure func(*testUDP), fail func(target string) error) *testEgress {
	return &testEgress{udpFactory: func(target string) (nodeengine.EgressUDP, error) {
		if fail != nil {
			if err := fail(target); err != nil {
				return nil, err
			}
		}
		socket := newTestUDP(true)
		if configure != nil {
			configure(socket)
		}
		l.mu.Lock()
		l.sockets = append(l.sockets, socket)
		l.mu.Unlock()
		return socket, nil
	}}
}

func (l *socketLog) get(i int) *testUDP {
	l.mu.Lock()
	defer l.mu.Unlock()
	if i >= len(l.sockets) {
		return nil
	}
	return l.sockets[i]
}

func (l *socketLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.sockets)
}

func openUDP(t *testing.T, h *testHarness, cred credentialFixture) (*testHTTP2Client, io.ReadCloser, *io.PipeWriter) {
	t.Helper()
	client := newHTTP2Client(t, h.addr)
	response, writer := client.connect(t, "_udp2", authValue(t, cred), true)
	if response.StatusCode != 200 {
		t.Fatalf("_udp2 status = %d", response.StatusCode)
	}
	return client, response.Body, writer
}

// The reply is addressed as the official client looks its flow up: from the address it really came from, to the client's
// source; flows of different sources do not mix, and one source may talk to several remotes through one socket.
func TestUDPReplyAddressingIPv4IPv6AndSeveralRemotesPerSource(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	h := newHarness(t, []plugin.UserCred{alice.cred}, sockets.egress(func(u *testUDP) {
		u.replyFrom = func(address string) string { // the answer comes from a different port than the one asked
			ap := netip.MustParseAddrPort(address)
			return netip.AddrPortFrom(ap.Addr(), ap.Port()+1).String()
		}
	}, nil), timings{})
	_, body, writer := openUDP(t, h, alice)

	sent := []wire.UDPFrame{
		udpFrame("192.0.2.1", 1111, "192.0.2.10", 443, "v4 to one"),
		udpFrame("192.0.2.1", 1111, "192.0.2.20", 8443, "v4 to another"),
		udpFrame("2001:db8:c::2", 5000, "2001:db8::1", 443, "v6"),
	}
	for _, frame := range sent {
		writeUDPFrame(t, writer, frame)
	}
	got := map[string]wire.UDPFrame{}
	for range sent {
		frame, err := readUDPFrame(body)
		if err != nil {
			t.Fatal(err)
		}
		got[string(frame.Payload)] = frame
	}
	for _, frame := range sent {
		reply, ok := got[string(frame.Payload)]
		wantSource := netip.AddrPortFrom(frame.Target.Addr(), frame.Target.Port()+1)
		if !ok || reply.Source != wantSource || reply.Target != frame.Source {
			t.Fatalf("reply to %q = %+v (present %v), want source %s and target %s", frame.Payload, reply, ok, wantSource, frame.Source)
		}
	}
	if sockets.count() != 2 {
		t.Fatalf("egress sockets = %d, want one per client source", sockets.count())
	}
	_ = writer.Close()
}

// A datagram to a forbidden address (or one the node cannot reach: IPv6 with IPv6 off) is dropped; the stream and the flows
// next to it carry on.
func TestUDPForbiddenDestinationDropsTheDatagramNotTheStream(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	outbound := sockets.egress(func(u *testUDP) {
		u.writeErr = func(address string) error {
			if address == "10.0.0.1:53" {
				return egress.ErrBlocked
			}
			return nil
		}
	}, func(target string) error {
		switch target {
		case "127.0.0.1:53":
			return egress.ErrBlocked
		case "[2001:db8::1]:443":
			return egress.ErrNoIPv4
		}
		return nil
	})
	h := newHarness(t, []plugin.UserCred{alice.cred}, outbound, timings{})
	_, body, writer := openUDP(t, h, alice)

	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1001, "127.0.0.1", 53, "blocked first"))
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1002, "2001:db8::1", 443, "no ipv6"))
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1003, "192.0.2.50", 443, "fine"))
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1003, "10.0.0.1", 53, "blocked on a live flow"))
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1003, "192.0.2.51", 443, "fine again"))
	for _, want := range []string{"fine", "fine again"} {
		frame, err := readUDPFrame(body)
		if err != nil || string(frame.Payload) != want {
			t.Fatalf("reply = %q, %v, want %q", frame.Payload, err, want)
		}
	}
	stats := collect(t, h.e)
	if up, down := trafficFor(stats, "alice"); up != uint64(len("fine")+len("fine again")) || down != up {
		t.Fatalf("traffic up/down = %d/%d: a dropped datagram must not be counted", up, down)
	}
	_ = writer.Close()
}

// A frame longer than the protocol allows is skipped as a whole, in step with the stream.
func TestUDPOversizedFrameIsSkippedAndTheStreamStaysInStep(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	h := newHarness(t, []plugin.UserCred{alice.cred}, sockets.egress(nil, nil), timings{})
	_, body, writer := openUDP(t, h, alice)

	junk := make([]byte, 4+70000)
	binary.BigEndian.PutUint32(junk, 70000)
	if _, err := writer.Write(junk); err != nil {
		t.Fatal(err)
	}
	short := make([]byte, 4+10)
	binary.BigEndian.PutUint32(short, 10) // shorter than the fixed part of a frame
	if _, err := writer.Write(short); err != nil {
		t.Fatal(err)
	}
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1001, "192.0.2.10", 443, "after the bad frames"))
	frame, err := readUDPFrame(body)
	if err != nil || string(frame.Payload) != "after the bad frames" {
		t.Fatalf("reply = %q, %v", frame.Payload, err)
	}
	_ = writer.Close()
}

// A client that does not read its replies loses datagrams instead of holding the sockets' readers, and the memory it can
// make the node hold stays bounded. (net/http queues a stream's window updates behind its blocked response data, so an
// upload also stops once the client's reply window and the upload window are full; it resumes when the client reads.)
func TestUDPSlowClientLosesRepliesAndResumesWhenItReads(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	h := newHarness(t, []plugin.UserCred{alice.cred}, sockets.egress(nil, nil), timings{})
	_, body, writer := openUDP(t, h, alice)

	const total, size = 12000, 1000
	payload := string(make([]byte, size))
	go func() {
		for range total {
			packet, err := wire.EncodeUDPClientFrame(udpFrame("192.0.2.1", 1001, "192.0.2.10", 443, payload))
			if err != nil {
				return
			}
			if _, err := writer.Write(packet); err != nil {
				return
			}
		}
	}()
	// The client reads nothing until the flow of datagrams has stopped by itself.
	last, steady := int64(-1), 0
	for steady < 10 {
		time.Sleep(100 * time.Millisecond)
		var writes int64
		if s := sockets.get(0); s != nil {
			writes = s.writes.Load()
		}
		if writes > 0 && writes == last {
			steady++
		} else {
			steady = 0
		}
		last = writes
	}
	stalled := last
	var received atomic.Int64
	go func() {
		for {
			if _, err := readUDPFrame(body); err != nil {
				return
			}
			received.Add(1)
		}
	}()
	waitFor(t, 20*time.Second, func() bool { return sockets.get(0).writes.Load() == total })
	time.Sleep(time.Second) // let the last replies drain
	got := received.Load()
	if got == 0 || got >= total {
		t.Fatalf("the slow client received %d of %d replies (the upload had stalled at %d), want some but not all", got, total, stalled)
	}
	stats := collect(t, h.e)
	if up, down := trafficFor(stats, "alice"); up != total*size || down != uint64(got)*size {
		t.Fatalf("traffic up/down = %d/%d, want %d/%d", up, down, total*size, uint64(got)*size)
	}
	_ = writer.CloseWithError(errors.New("done"))
}

// When the 257th flow arrives, the one that was silent longest goes, not the oldest by creation.
func TestUDPFlowLimitEvictsTheLeastRecentlyActive(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	h := newHarness(t, []plugin.UserCred{alice.cred}, sockets.egress(nil, nil), timings{})
	_, body, writer := openUDP(t, h, alice)
	go io.Copy(io.Discard, body)
	for i := range maxUDPFlows {
		writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1000+i, "192.0.2.10", 443, "x"))
	}
	waitFor(t, 5*time.Second, func() bool { return sockets.count() == maxUDPFlows })
	// Every flow has taken its echo (that is activity too) before the order of activity is set up by hand.
	quiet := func() bool {
		for i := range sockets.count() {
			if len(sockets.get(i).replies) != 0 {
				return false
			}
		}
		return true
	}
	waitFor(t, 5*time.Second, quiet)
	time.Sleep(5 * time.Millisecond)
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1000, "192.0.2.10", 443, "again")) // flow 0 is busy again
	waitFor(t, 5*time.Second, func() bool { return sockets.get(0).writes.Load() == 2 })
	waitFor(t, 5*time.Second, quiet)
	time.Sleep(5 * time.Millisecond)
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 5000, "192.0.2.10", 443, "new"))
	waitFor(t, 5*time.Second, func() bool { return sockets.count() == maxUDPFlows+1 })
	// Which of the quiet flows is the least recently active depends on when each one's echo was read, so one of them goes,
	// not necessarily the first.
	waitFor(t, 5*time.Second, func() bool {
		for i := 1; i <= maxUDPFlows; i++ {
			if sockets.get(i).isClosed.Load() {
				return true
			}
		}
		return false
	})
	if sockets.get(0).isClosed.Load() {
		t.Fatal("the flow that had just been active was evicted")
	}
	open := 0
	for i := range maxUDPFlows + 1 {
		if !sockets.get(i).isClosed.Load() {
			open++
		}
	}
	if open != maxUDPFlows {
		t.Fatalf("open sockets = %d, want %d", open, maxUDPFlows)
	}
	_ = writer.Close()
}

// A busy flow outlives the idle time; a quiet one ends with its socket closed.
func TestUDPBusyFlowStaysAndQuietFlowCloses(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	ts := defaultTimings
	ts.udpFlowIdle = 150 * time.Millisecond
	h := newHarness(t, []plugin.UserCred{alice.cred}, sockets.egress(nil, nil), ts)
	_, body, writer := openUDP(t, h, alice)
	go io.Copy(io.Discard, body)
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1001, "192.0.2.10", 443, "quiet"))
	for range 10 { // 500 ms, more than three idle times
		writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1002, "192.0.2.11", 443, "busy"))
		time.Sleep(50 * time.Millisecond)
	}
	waitFor(t, time.Second, func() bool { return sockets.get(0).isClosed.Load() })
	if sockets.get(1).isClosed.Load() {
		t.Fatal("a flow that kept talking was closed as idle")
	}
	waitFor(t, time.Second, func() bool { return sockets.get(1).isClosed.Load() })
	_ = writer.Close()
}

// A DNS flow is done when its queries have their answers; a flow that also talks elsewhere is not.
func TestUDPDNSFlowClosesAfterItsAnswers(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	h := newHarness(t, []plugin.UserCred{alice.cred}, sockets.egress(nil, nil), timings{})
	_, body, writer := openUDP(t, h, alice)
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1001, "192.0.2.53", 53, "query"))
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1002, "192.0.2.60", 443, "not only dns"))
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1002, "192.0.2.53", 53, "query"))
	for range 3 {
		if _, err := readUDPFrame(body); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, time.Second, func() bool { return sockets.get(0).isClosed.Load() })
	time.Sleep(100 * time.Millisecond)
	if sockets.get(1).isClosed.Load() {
		t.Fatal("a flow that is not only DNS was closed after a DNS answer")
	}
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1001, "192.0.2.53", 53, "again"))
	frame, err := readUDPFrame(body)
	if err != nil || string(frame.Payload) != "again" {
		t.Fatalf("a new query from the closed flow's source = %q, %v", frame.Payload, err)
	}
	_ = writer.Close()
}

// A datagram that fills the receive buffer may have been cut, so it is not forwarded; smaller ones are.
func TestUDPFullBufferDatagramIsDropped(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	h := newHarness(t, []plugin.UserCred{alice.cred}, sockets.egress(nil, nil), timings{})
	_, body, writer := openUDP(t, h, alice)
	writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1001, "192.0.2.10", 443, "open the flow"))
	if _, err := readUDPFrame(body); err != nil {
		t.Fatal(err)
	}
	socket := sockets.get(0)
	socket.replies <- udpReply{payload: make([]byte, udpReadBuffer), address: "192.0.2.10:443"}
	socket.replies <- udpReply{payload: []byte("short"), address: "not an address"}
	socket.replies <- udpReply{payload: make([]byte, udpReadBuffer-1), address: "192.0.2.10:443"}
	frame, err := readUDPFrame(body)
	if err != nil || len(frame.Payload) != udpReadBuffer-1 {
		t.Fatalf("next reply has %d bytes, %v, want the largest datagram that fits", len(frame.Payload), err)
	}
	_ = writer.Close()
}

// Kick ends a UDP stream at once and closes its sockets.
func TestUDPKickClosesStreamAndSockets(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	h := newHarness(t, []plugin.UserCred{alice.cred}, sockets.egress(nil, nil), timings{})
	_, body, writer := openUDP(t, h, alice)
	for i := range 3 {
		writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1000+i, "192.0.2.10", 443+i, "x"))
	}
	for range 3 {
		if _, err := readUDPFrame(body); err != nil {
			t.Fatal(err)
		}
	}
	if affected, _ := h.e.Kick(context.Background(), []string{"alice"}); affected != 1 {
		t.Fatalf("Kick affected %d", affected)
	}
	if !endsWithin(body, time.Second) {
		t.Fatal("the UDP stream survived Kick")
	}
	for i := range 3 {
		i := i
		waitFor(t, time.Second, func() bool { return sockets.get(i).isClosed.Load() })
	}
}

// A connection carries one _udp2 stream per credential. A second one replaces the first, which is reset and gives its
// sockets back; the second works.
func TestSecondUDP2OnAConnectionReplacesTheFirst(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	var sockets socketLog
	h := newHarness(t, []plugin.UserCred{alice.cred}, sockets.egress(nil, nil), timings{})
	client := newHTTP2Client(t, h.addr)

	first, firstWriter := client.connect(t, "_udp2", authValue(t, alice), true)
	writeUDPFrame(t, firstWriter, udpFrame("192.0.2.1", 1111, "192.0.2.10", 443, "first"))
	if reply, err := readUDPFrame(first.Body); err != nil || string(reply.Payload) != "first" {
		t.Fatalf("first stream echo = %+v, %v", reply, err)
	}
	second, secondWriter := client.connect(t, "_udp2", authValue(t, alice), true)
	if second.StatusCode != 200 {
		t.Fatalf("second _udp2 status = %d", second.StatusCode)
	}
	if n := authenticatedConnectionCount(h.in); n != 1 {
		t.Fatalf("the two streams used %d connections, the test needs one", n)
	}
	if err, ended := streamErrorWithin(first.Body, time.Second); !ended || err == nil {
		t.Fatalf("the replaced _udp2 stream ended = %v, err = %v; it must be reset within a second", ended, err)
	}
	waitFor(t, time.Second, func() bool { return sockets.get(0).isClosed.Load() })

	writeUDPFrame(t, secondWriter, udpFrame("192.0.2.1", 2222, "192.0.2.10", 443, "second"))
	if reply, err := readUDPFrame(second.Body); err != nil || string(reply.Payload) != "second" {
		t.Fatalf("second stream echo = %+v, %v", reply, err)
	}
	// What the writer has taken off the queue is given back to the credential's bound.
	credential := h.in.idx.Load().byID["alice"]
	waitFor(t, time.Second, func() bool { return credential.udpQueued.Load() == 0 })
	// It is the one stream of the credential on this connection now: a third replaces it in turn.
	third, _ := client.connect(t, "_udp2", authValue(t, alice), true)
	if err, ended := streamErrorWithin(second.Body, time.Second); !ended || err == nil {
		t.Fatalf("the second stream survived a third: ended = %v, err = %v", ended, err)
	}
	_ = third.Body.Close()
}

// The sockets of all the _udp2 streams of a credential together stay at maxUDPFlows, however many connections carry
// them; the least recently active ones close, on whichever stream. Another credential keeps its own.
func TestUDPFlowsOfACredentialAreBoundedAcrossConnections(t *testing.T) {
	alice := makeCredential(t, "alice", "alice", "person-alice", 0, 0)
	bob := makeCredential(t, "bob", "bob", "person-bob", 0, 0)
	var sockets socketLog
	h := newHarness(t, []plugin.UserCred{alice.cred, bob.cred}, sockets.egress(nil, nil), timings{})

	_, bobBody, bobWriter := openUDP(t, h, bob)
	go io.Copy(io.Discard, bobBody)
	writeUDPFrame(t, bobWriter, udpFrame("192.0.2.2", 7000, "192.0.2.10", 443, "bob"))
	waitFor(t, 5*time.Second, func() bool { return sockets.count() == 1 })
	bobSocket := sockets.get(0)

	const connections, perConnection = 3, 120 // 360 flows against a bound of 256
	var writers []*io.PipeWriter
	for c := 0; c < connections; c++ {
		_, body, writer := openUDP(t, h, alice)
		go io.Copy(io.Discard, body)
		writers = append(writers, writer)
		for i := 0; i < perConnection; i++ {
			writeUDPFrame(t, writer, udpFrame("192.0.2.1", 1000+i, "192.0.2.10", 443, "x"))
		}
		waitFor(t, 5*time.Second, func() bool { return sockets.count() == 1+(c+1)*perConnection })
		time.Sleep(5 * time.Millisecond) // the connections' flows are ordered by when they were last active
	}
	waitFor(t, 5*time.Second, func() bool {
		open := 0
		for i := 1; i < sockets.count(); i++ {
			if !sockets.get(i).isClosed.Load() {
				open++
			}
		}
		return open == maxUDPFlows
	})
	if bobSocket.isClosed.Load() {
		t.Fatal("another credential's flow was evicted")
	}
	// The newest flows are the ones that stayed.
	for i := sockets.count() - 10; i < sockets.count(); i++ {
		if sockets.get(i).isClosed.Load() {
			t.Fatalf("socket %d of %d, among the newest, was closed", i, sockets.count())
		}
	}
	credential := h.in.idx.Load().byID["alice"]
	credential.udpMu.Lock()
	counted := len(credential.udpFlows)
	credential.udpMu.Unlock()
	if counted != maxUDPFlows {
		t.Fatalf("the credential counts %d flows, want %d", counted, maxUDPFlows)
	}
	for _, writer := range writers {
		_ = writer.Close()
	}
	// Ending the streams gives every socket back.
	waitFor(t, 5*time.Second, func() bool {
		credential.udpMu.Lock()
		defer credential.udpMu.Unlock()
		return len(credential.udpFlows) == 0
	})
}

// streamErrorWithin reads body to its end and returns what ended it, or reports that it did not end within d.
func streamErrorWithin(body io.Reader, d time.Duration) (error, bool) {
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, body)
		done <- err
	}()
	select {
	case err := <-done:
		return err, true
	case <-time.After(d):
		return nil, false
	}
}
