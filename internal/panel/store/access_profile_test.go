package store

import (
	"context"
	"testing"
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
