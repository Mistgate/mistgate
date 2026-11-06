//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

func TestInboundsFullIncludesNodeBandwidth(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "bandwidth")
	execT(t, s, `UPDATE node SET bandwidth_mbps = 1200 WHERE id = ?`, nodeID)

	got, err := s.Access().InboundsFull(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("inbounds = %d, want 1", len(got))
	}
	if got[0].Node.BandwidthMbps != 1200 {
		t.Fatalf("node bandwidth = %d, want 1200", got[0].Node.BandwidthMbps)
	}
}

func TestInboundDirectInSubscriptionsRoundTripsAndDefaultsTrue(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, inboundID := fixtureInbound(t, s, "direct_in_subscriptions")
	a := s.Access()

	in, err := a.Inbound(ctx, inboundID)
	if err != nil {
		t.Fatal(err)
	}
	if !in.DirectInSubscriptions {
		t.Fatal("new inbound is not listed directly by default")
	}

	full, err := a.InboundsFull(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	byNode, err := a.InboundsOfNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(full) != 1 || !full[0].Inbound.DirectInSubscriptions || len(byNode) != 1 || !byNode[0].DirectInSubscriptions {
		t.Fatalf("inbound readers missed the default: full=%+v byNode=%+v", full, byNode)
	}

	in.DirectInSubscriptions = false
	if err := a.UpdateInbound(ctx, in, false, time.Unix(123, 0)); err != nil {
		t.Fatal(err)
	}
	got, err := a.Inbound(ctx, inboundID)
	if err != nil {
		t.Fatal(err)
	}
	fleet, err := s.FleetInbound(ctx, inboundID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DirectInSubscriptions || fleet.DirectInSubscriptions {
		t.Fatalf("round trip: access=%+v fleet=%+v", got, fleet)
	}
}
