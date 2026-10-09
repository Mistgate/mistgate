package access

import (
	"slices"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func inboundForSubscriptionOrder(country, location, nodeName, nodeID, protocol, egress, profileName, inboundID string, createdAt time.Time) store.AccessInboundFull {
	return store.AccessInboundFull{
		Node: store.AccessNode{ID: nodeID, Name: nodeName, CountryCode: country, Location: location},
		Profile: store.AccessProfile{
			Protocol: protocol, Name: profileName, SettingsJSON: `{"egress":"` + egress + `"}`,
		},
		Inbound: store.AccessInbound{ID: inboundID, CreatedAt: createdAt},
	}
}

func TestSubscriptionInboundComparator(t *testing.T) {
	first := time.Unix(1, 0)
	second := time.Unix(2, 0)
	base := func() store.AccessInboundFull {
		return inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_a", first)
	}
	tests := []struct {
		name string
		a, b store.AccessInboundFull
		want int
	}{
		{
			name: "country code",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("EE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_a", first),
			want: -1,
		},
		{
			name: "empty country last",
			a:    base(),
			b:    inboundForSubscriptionOrder("", "Aachen", "aa1", "nod_aa1", "hysteria2", "direct", "HY2", "inb_a", first),
			want: -1,
		},
		{
			name: "location",
			a:    inboundForSubscriptionOrder("DE", "Aachen", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_a", first),
			b:    base(),
			want: -1,
		},
		{
			name: "node name",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_a", "hysteria2", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de2", "nod_b", "hysteria2", "direct", "HY2", "inb_a", first),
			want: -1,
		},
		{
			name: "node id",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_a", "hysteria2", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_b", "hysteria2", "direct", "HY2", "inb_a", first),
			want: -1,
		},
		{
			name: "protocol rank hysteria2 before vless",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "vless", "direct", "HY2", "inb_a", first),
			want: -1,
		},
		{
			name: "protocol rank vless before awg",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "vless", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "awg", "direct", "HY2", "inb_a", first),
			want: -1,
		},
		{
			name: "protocol rank awg before other ids",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "awg", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "other", "direct", "HY2", "inb_a", first),
			want: -1,
		},
		{
			name: "other protocol ids alphabetical",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "alpha", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "zeta", "direct", "HY2", "inb_a", first),
			want: -1,
		},
		{
			name: "direct before warp",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "warp", "HY2", "inb_a", first),
			want: -1,
		},
		{
			name: "profile name",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2 443", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2 WARP", "inb_a", first),
			want: -1,
		},
		{
			name: "inbound creation time",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_b", second),
			want: -1,
		},
		{
			name: "inbound id",
			a:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_a", first),
			b:    inboundForSubscriptionOrder("DE", "Berlin", "de1", "nod_de1", "hysteria2", "direct", "HY2", "inb_b", first),
			want: -1,
		},
		{
			name: "equal keys",
			a:    base(),
			b:    base(),
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compareSubscriptionInbounds(tt.a, tt.b)
			if got < 0 {
				got = -1
			} else if got > 0 {
				got = 1
			}
			if got != tt.want {
				t.Errorf("compareSubscriptionInbounds() = %d, want %d", got, tt.want)
			}
		})
	}

	firstTie, secondTie := base(), base()
	firstTie.Inbound.PortOverride, secondTie.Inbound.PortOverride = 443, 8443
	tied := []store.AccessInboundFull{firstTie, secondTie}
	slices.SortStableFunc(tied, compareSubscriptionInbounds)
	if tied[0].Inbound.PortOverride != 443 || tied[1].Inbound.PortOverride != 8443 {
		t.Errorf("stable tie order = %d, %d", tied[0].Inbound.PortOverride, tied[1].Inbound.PortOverride)
	}
}
