package trusttunnel

import (
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// stallsOf adds up the inbound row's TCP stall counters of the next Collect.
func stallsOf(t *testing.T, h *testHarness) (stalled, first16K uint32) {
	t.Helper()
	for _, link := range collect(t, h.e).Links {
		if link.CredID == "" {
			stalled += link.TCPStalls
			first16K += link.TCPStalls16K
		}
	}
	return
}

// waitStalls waits for the server to notice that the client went away and to count the connection.
func waitStalls(t *testing.T, h *testHarness, want uint32) (stalled, first16K uint32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, f := stallsOf(t, h)
		stalled, first16K = stalled+s, first16K+f
		if stalled >= want || time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func fakeStall(t *testing.T, stalled, first16K bool) *atomic.Int32 {
	t.Helper()
	reads := &atomic.Int32{}
	old := tcpStall.Load()
	tcpStall.Store(func(net.Conn) (bool, bool) {
		reads.Add(1)
		return stalled, first16K
	})
	t.Cleanup(func() { tcpStall.Store(old) })
	return reads
}

// A connection that served a request and ended the way the rule calls a stall is counted once, with its 16 KiB mark; one that
// ended in good order, and one that never got past the TLS handshake, are not.
func TestTCPStallsAreCountedOnTheInboundRow(t *testing.T) {
	reads := fakeStall(t, true, true)
	h := newHarness(t, nil, &testEgress{}, timings{})

	// never past the handshake: a scanner that connects and goes away
	raw, err := net.Dial("tcp", h.addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw.Close()
	time.Sleep(100 * time.Millisecond)
	if s, f := stallsOf(t, h); s != 0 || f != 0 || reads.Load() != 0 {
		t.Fatalf("a connection that sent nothing counted %d/%d stalls after %d reads", s, f, reads.Load())
	}

	client := newHTTP2Client(t, h.addr)
	if resp := client.get(t); resp.StatusCode == 0 {
		t.Fatal("no answer")
	}
	client.kill()
	if s, f := waitStalls(t, h, 1); s != 1 || f != 1 {
		t.Fatalf("stalls %d, first 16K %d; want 1 and 1", s, f)
	}
	if s, f := stallsOf(t, h); s != 0 || f != 0 {
		t.Fatalf("a second Collect repeated the stall: %d/%d", s, f)
	}
}

func TestTCPNoStallIsNotCounted(t *testing.T) {
	reads := fakeStall(t, false, false)
	h := newHarness(t, nil, &testEgress{}, timings{})
	client := newHTTP2Client(t, h.addr)
	_ = client.get(t)
	client.kill()
	deadline := time.Now().Add(5 * time.Second)
	for reads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if reads.Load() == 0 {
		t.Fatal("the closed connection was never read")
	}
	if s, f := stallsOf(t, h); s != 0 || f != 0 {
		t.Fatalf("a healthy connection counted %d/%d stalls", s, f)
	}
}

// Behind the shared port the connection is the front's: the close is still read once, with the real rule, and a healthy
// connection is no stall (the freeze itself needs root and nftables, see internal/node/vless/tcpstall_linux_test.go).
func TestTCPStallReadThroughTheSharedFront(t *testing.T) {
	var reads, stalls atomic.Int32
	real := tcpStall.Load().(func(net.Conn) (bool, bool))
	tcpStall.Store(func(c net.Conn) (bool, bool) {
		stalled, first := real(c)
		reads.Add(1)
		if stalled || first {
			stalls.Add(1)
		}
		return stalled, first
	})
	t.Cleanup(func() { tcpStall.Store(real) })
	h, _ := sharedHarness(t, nil, &testEgress{})
	client := newHTTP2Client(t, h.addr)
	_ = client.get(t)
	client.kill()
	deadline := time.Now().Add(5 * time.Second)
	for reads.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if reads.Load() != 1 || stalls.Load() != 0 {
		t.Fatalf("reads %d, stalls %d; want one read of a healthy connection", reads.Load(), stalls.Load())
	}
}
