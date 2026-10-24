package hysteria2

import (
	"net"
	"testing"
	"time"

	"github.com/apernet/quic-go"
)

// The core reports Disconnect with the connection's current address, which port hopping and NAT rebinding move after
// Connect: the session must still end (no ghost online, the drop counted, the open-connection count back down).
func TestDisconnectFindsAMovedSession(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	in := &inbound{e: &eng{now: func() time.Time { return now }}, sessions: map[string]session{}}
	alice, bob := &credState{id: "crd_alice"}, &credState{id: "crd_bob"}
	in.idx.Store(&index{byID: map[string]*credState{"crd_alice": alice, "crd_bob": bob}})
	addr := func(s string) net.Addr { a, _ := net.ResolveUDPAddr("udp", s); return a }

	in.Connect(addr("203.0.113.7:40000"), "crd_alice", 0) // the older of alice's two connections
	now = now.Add(time.Second)
	in.Connect(addr("198.51.100.9:50000"), "crd_alice", 0)
	in.Connect(addr("203.0.113.8:41000"), "crd_bob", 0)

	// alice's first connection hopped to another port of the same IP, then went idle at once: a drop
	in.Disconnect(addr("203.0.113.7:40999"), "crd_alice", &quic.IdleTimeoutError{})
	if _, left := in.sessions["203.0.113.7:40000"]; left || len(in.sessions) != 2 {
		t.Fatalf("sessions after the moved disconnect: %v", in.sessions)
	}
	if got := alice.drops.Load(); got != 1 {
		t.Errorf("drops = %d, want 1", got)
	}
	if got := alice.conns.Load(); got != 1 {
		t.Errorf("alice's open connections = %d, want 1", got)
	}

	// no session of that IP left: the credential's oldest one ends, never another credential's
	in.Disconnect(addr("192.0.2.1:1234"), "crd_alice", nil)
	if _, left := in.sessions["198.51.100.9:50000"]; left {
		t.Fatalf("alice's last session is still online: %v", in.sessions)
	}
	if _, kept := in.sessions["203.0.113.8:41000"]; !kept || bob.conns.Load() != 1 {
		t.Fatalf("bob's session was touched: %v", in.sessions)
	}

	// an unknown credential's disconnect changes nothing
	in.Disconnect(addr("192.0.2.2:1"), "crd_nobody", nil)
	if len(in.sessions) != 1 {
		t.Fatalf("sessions = %v", in.sessions)
	}
}
