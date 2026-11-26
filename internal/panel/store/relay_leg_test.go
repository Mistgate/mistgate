//go:build !js

package store

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"
)

// The address family of a relay's leg: the owner's choice (leg), the family in use (active_leg) and the address the panel picks
// for it. The store never resolves anything; it only says which address a relay would use with what is known of the node now.
func TestRelayHostFor(t *testing.T) {
	doctor := IPv6Doctor{State: IPv6OK, Src: "2001:db8::a", Addrs: []netip.Prefix{netip.MustParsePrefix("2001:db8::a/64")}}
	both := NodeRow{Address: "de1.example.com", IPv6Entry: "2001:db8::5"}
	cases := []struct {
		name      string
		active    string
		node      NodeRow
		doctor    IPv6Doctor
		host, fam string
		ok        bool
	}{
		{"ipv4 by default", "", both, doctor, "de1.example.com", "4", true},
		{"ipv4 with no address", "", NodeRow{}, doctor, "", "4", false},
		{"ipv6 entry wins over the doctor", "6", both, doctor, "2001:db8::5", "6", true},
		{"ipv6 from the doctor", "6", NodeRow{Address: "de1.example.com"}, doctor, "2001:db8::a", "6", true},
		{"ipv6 with nothing known", "6", NodeRow{Address: "de1.example.com"}, IPv6Doctor{}, "", "6", false},
		{"a ULA entry is not usable", "6", NodeRow{Address: "de1.example.com", IPv6Entry: "fd00::5"}, IPv6Doctor{}, "", "6", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, fam, ok := RelayHostFor(Relay{ActiveLeg: tc.active}, tc.node.Address, tc.node.IPv6Entry, tc.doctor)
			if host != tc.host || fam != tc.fam || ok != tc.ok {
				t.Fatalf("RelayHostFor = %q %q %v; want %q %q %v", host, fam, ok, tc.host, tc.fam, tc.ok)
			}
		})
	}
}

// The leg columns (migration 00160): a new relay is 'auto' on IPv4, the owner may pin a family, and the panel may move the family
// in use without touching the owner's choice.
func TestRelayLegStore(t *testing.T) {
	st := openTemp(t)
	relayFixture(t, st)
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	if err := st.CreateRelay(ctx, Relay{ID: "rly_a", InboundID: "inb_de1_hy", ViaNodeID: "nod_fi1", Network: "udp", Port: 8443}, now); err != nil {
		t.Fatal(err)
	}
	if l := mustRelay(t, st, "rly_a"); l.Leg != RelayLegAuto || l.ActiveFamily() != RelayFamily4 || !l.LegTriedAt.IsZero() {
		t.Fatalf("a new relay = leg %q, active %q, tried %v; want auto, 4, never", l.Leg, l.ActiveFamily(), l.LegTriedAt)
	}
	if err := st.CreateRelay(ctx, Relay{ID: "rly_b", InboundID: "inb_nl1_hy", ViaNodeID: "nod_fi1", Network: "tcp", Port: 9443, Leg: RelayLegIPv6}, now); err != nil {
		t.Fatal(err)
	}
	if l := mustRelay(t, st, "rly_b"); l.Leg != RelayLegIPv6 {
		t.Fatalf("owner's leg = %q, want ipv6", l.Leg)
	}
	leg := RelayLegIPv4
	if err := st.UpdateRelay(ctx, "rly_b", RelayPatch{Leg: &leg}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if l := mustRelay(t, st, "rly_b"); l.Leg != RelayLegIPv4 {
		t.Fatalf("patched leg = %q, want ipv4", l.Leg)
	}
	tried := now.Add(2 * time.Minute)
	if err := st.SetRelayActiveLeg(ctx, "rly_b", RelayFamily6, tried, tried); err != nil {
		t.Fatal(err)
	}
	if l := mustRelay(t, st, "rly_b"); l.ActiveFamily() != RelayFamily6 || !l.LegTriedAt.Equal(tried) || l.Leg != RelayLegIPv4 {
		t.Fatalf("moved leg = active %q, tried %v, owner %q; want 6, %v, ipv4", l.ActiveFamily(), l.LegTriedAt, l.Leg, tried)
	}
	if err := st.SetRelayActiveLeg(ctx, "rly_b", "7", tried, tried); err == nil {
		t.Fatal("a family that is neither 4 nor 6 was stored")
	}
	if err := st.SetRelayActiveLeg(ctx, "rly_nope", RelayFamily4, tried, tried); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown relay = %v, want ErrNotFound", err)
	}
}
