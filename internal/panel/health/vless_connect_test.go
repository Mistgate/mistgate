//go:build !js

package health

import (
	"testing"
	"time"
)

// The marker is stamped once, also when Xray's line arrives in two writes.
func TestTailBufferStampsTheTunnelMarker(t *testing.T) {
	b := &tailBuffer{limit: 64}
	if !b.markerTime().IsZero() {
		t.Fatal("marker before any write")
	}
	_, _ = b.Write([]byte("[Info] proxy/vless/outbound: tunneling re"))
	if !b.markerTime().IsZero() {
		t.Fatal("marker from half a line")
	}
	_, _ = b.Write([]byte("quest to tcp:probe.test:80 via 203.0.113.9:443"))
	first := b.markerTime()
	if first.IsZero() {
		t.Fatal("marker not stamped")
	}
	time.Sleep(2 * time.Millisecond)
	_, _ = b.Write([]byte(" tunneling request again"))
	if !b.markerTime().Equal(first) {
		t.Fatal("marker stamped twice")
	}
}
