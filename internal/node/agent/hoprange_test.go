package agent

import (
	"strings"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/hostctl"
)

func withPort(in *pb.InboundState, port uint32) *pb.InboundState {
	in.Spec.Listen.Port = port
	return in
}

// F12: a hop range the node must not redirect is refused for that inbound with a clear InboundResult
// error; the DNAT is never installed, and the other inbounds are not affected.
func TestBadHopRangesAreRejectedPerInbound(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.host.ssh = []uint16{2222}
	h.panel.push(fullState(1,
		inb("inb_a", 20000, 29999, cred("crd_a")),                  // fine
		inb("inb_b", 1, 65535, cred("crd_b")),                      // whole port space, covers DNS/NTP/ssh
		inb("inb_c", 2000, 2500, cred("crd_c")),                    // covers the sshd port 2222
		inb("inb_d", 25000, 26000, cred("crd_d")),                  // overlaps inb_a
		withPort(inb("inb_f", 0, 0, cred("crd_f")), 30050),         // a real inbound on 30050/udp
		inb("inb_g", 30000, 30100, cred("crd_g")),                  // covers inb_f
		inb("inb_h", 22, 100, cred("crd_h")),                       // below 1024, includes 22
		inb("inb_i", 40000, 60001, cred("crd_i")),                  // 20002 ports wide
		withPort(inb("inb_j", 31000, 31999, cred("crd_j")), 31500), // its own port inside its range: fine
	))
	r := h.panel.nextApply()

	want := map[string]string{
		"inb_b": "below 1024", "inb_c": "port 2222", "inb_d": "overlaps", "inb_g": "port 30050", "inb_h": "below 1024", "inb_i": "wide",
	}
	got := map[string]string{}
	for _, ir := range r.Inbounds {
		got[ir.InboundId] = ir.Error
	}
	for id, sub := range want {
		if !strings.HasPrefix(got[id], "port hop rejected: ") || !strings.Contains(got[id], sub) {
			t.Errorf("%s: error %q, want a hop rejection mentioning %q", id, got[id], sub)
		}
	}
	for _, id := range []string{"inb_a", "inb_f", "inb_j"} {
		if got[id] != "" {
			t.Errorf("%s must be fine, got %q", id, got[id])
		}
	}
	if r.Status != pb.ApplyStatus_APPLY_STATUS_PARTIAL {
		t.Errorf("status %v, want PARTIAL", r.Status)
	}

	// Only the two acceptable hops reach the firewall.
	calls := h.host.hops()
	if len(calls) != 1 || len(calls[0]) != 2 ||
		calls[0][0] != (hostctl.Hop{InboundID: "inb_a", Network: "udp", From: 20000, To: 29999, Port: 443}) ||
		calls[0][1] != (hostctl.Hop{InboundID: "inb_j", Network: "udp", From: 31000, To: 31999, Port: 31500}) {
		t.Fatalf("hop calls: %+v", calls)
	}
	eventually(t, func() bool { return h.panel.hasEvent("hop_rejected") }, "hop_rejected event")
}
