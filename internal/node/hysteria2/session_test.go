package hysteria2

import (
	"net"
	"testing"
	"time"

	"github.com/apernet/quic-go"
)

// The old EventLogger methods remain usable by an unpatched core. The patched core uses connection-keyed callbacks.
func TestLegacyEventLoggerSessionsRemainAddressKeyed(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	in := &inbound{e: &eng{now: func() time.Time { return now }}, legacySessions: map[string]*session{}}
	alice, bob := &credState{id: "crd_alice"}, &credState{id: "crd_bob"}
	in.idx.Store(&index{byID: map[string]*credState{"crd_alice": alice, "crd_bob": bob}})
	addr := func(s string) net.Addr { a, _ := net.ResolveUDPAddr("udp", s); return a }

	in.Connect(addr("203.0.113.7:40000"), "crd_alice", 0) // the older of alice's two connections
	now = now.Add(time.Second)
	in.Connect(addr("198.51.100.9:50000"), "crd_alice", 0)
	in.Connect(addr("203.0.113.8:41000"), "crd_bob", 0)

	// This compatibility API cannot identify a moved address; the core patch routes production disconnects by *quic.Conn.
	in.Disconnect(addr("203.0.113.7:40000"), "crd_alice", &quic.IdleTimeoutError{})
	if _, left := in.legacySessions["203.0.113.7:40000"]; left || len(in.legacySessions) != 2 {
		t.Fatalf("sessions after the disconnect: %v", in.legacySessions)
	}
	if got := alice.drops.Load(); got != 1 {
		t.Errorf("drops = %d, want 1", got)
	}
	if got := alice.conns.Load(); got != 1 {
		t.Errorf("alice's open connections = %d, want 1", got)
	}

	// The second exact address ends and the other credential remains untouched.
	in.Disconnect(addr("198.51.100.9:50000"), "crd_alice", nil)
	if _, left := in.legacySessions["198.51.100.9:50000"]; left {
		t.Fatalf("alice's last session is still online: %v", in.legacySessions)
	}
	if _, kept := in.legacySessions["203.0.113.8:41000"]; !kept || bob.conns.Load() != 1 {
		t.Fatalf("bob's session was touched: %v", in.legacySessions)
	}

	// an unknown credential's disconnect changes nothing
	in.Disconnect(addr("192.0.2.2:1"), "crd_nobody", nil)
	if len(in.legacySessions) != 1 {
		t.Fatalf("sessions = %v", in.legacySessions)
	}
}
