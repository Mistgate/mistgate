//go:build !js

package store

import (
	"context"
	"errors"
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

	was := in
	in.DirectInSubscriptions = false
	if err := a.UpdateInbound(ctx, in, was, false, time.Unix(123, 0)); err != nil {
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

// Two edits that read the same inbound row and write different fields: the second is refused and the first field stays.
func TestUpdateInboundRefusesAStaleRead(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	_, inboundID := fixtureInbound(t, s, "lost_update")
	a := s.Access()
	was, err := a.Inbound(ctx, inboundID)
	if err != nil {
		t.Fatal(err)
	}

	first := was
	first.RealityTargetOverride = "shop.example.com:443"
	first.SpecVersion++
	if err := a.UpdateInbound(ctx, first, was, true, time.Unix(200, 0)); err != nil {
		t.Fatal(err)
	}
	second := was // the same read, another field
	second.PortOverride = 4443
	second.SpecVersion++
	if err := a.UpdateInbound(ctx, second, was, true, time.Unix(201, 0)); !errors.Is(err, ErrAccessVersion) {
		t.Fatalf("second write over the same read = %v, want ErrAccessVersion", err)
	}
	got, err := a.Inbound(ctx, inboundID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RealityTargetOverride != "shop.example.com:443" || got.PortOverride != 0 || got.SpecVersion != first.SpecVersion {
		t.Fatalf("the first write was lost: %+v", got)
	}
	// the same edit on a fresh read goes through
	if err := a.UpdateInbound(ctx, withPort(got, 4443), got, true, time.Unix(202, 0)); err != nil {
		t.Fatal(err)
	}
	// a report of the node (state, certificate) in between is no reason to refuse
	fresh, _ := a.Inbound(ctx, inboundID)
	if err := a.SetInboundBuildError(ctx, inboundID, "boom", time.Unix(203, 0)); err != nil {
		t.Fatal(err)
	}
	if err := a.UpdateInbound(ctx, withPort(fresh, 4444), fresh, false, time.Unix(204, 0)); err != nil {
		t.Fatalf("a node report must not refuse an edit: %v", err)
	}
	if err := a.UpdateInbound(ctx, was, was, false, time.Unix(205, 0)); !errors.Is(err, ErrAccessVersion) {
		t.Fatalf("an old read = %v", err)
	}
	gone := was
	gone.ID = "inb_nope"
	if err := a.UpdateInbound(ctx, gone, gone, false, time.Unix(206, 0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing inbound = %v", err)
	}
}

func withPort(i AccessInbound, port uint16) AccessInbound {
	i.PortOverride = port
	i.SpecVersion++
	return i
}

func TestProfileSelectiveBumpSQLite(t *testing.T) { profileSelectiveBumpProof(t, openTemp(t)) }
