package fleet

import (
	"testing"
	"time"
)

func TestAgentFrameBucketAllowsHonestOutboxReplayAndNormalRate(t *testing.T) {
	var bucket agentFrameBucket
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	// The worst honest start of a session: a full outbox, a 20,000-line debug capture (313 chunks) and a few dozen control
	// frames (apply results, command results, an OS status, a doctor report), all at once.
	for i := 0; i < 2160+313+60; i++ {
		if !bucket.allow(now) {
			t.Fatalf("honest frame %d of the opening burst was limited", i+1)
		}
	}
	// Then two log follow streams (two chunks a second each) for ten minutes, on top of the stats every ten seconds.
	for i := 1; i <= 600*4; i++ {
		if !bucket.allow(now.Add(time.Duration(i) * 250 * time.Millisecond)) {
			t.Fatalf("follow-stream frame %d was limited", i)
		}
	}
	if bucket.totalDrops != 0 {
		t.Fatalf("dropped %d honest frames", bucket.totalDrops)
	}
}

func TestAgentFrameBucketCountsAndClosesPersistentFlood(t *testing.T) {
	var bucket agentFrameBucket
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for i := 0; i < agentFrameBurst; i++ {
		if !bucket.allow(now) {
			t.Fatalf("burst frame %d was limited", i+1)
		}
	}
	if bucket.allow(now) {
		t.Fatal("frame beyond the burst was accepted")
	}
	for i := 0; i < agentFrameDropsToClose; i++ {
		closed, _ := bucket.drop(now)
		if closed != (i+1 == agentFrameDropsToClose) {
			t.Fatalf("drop %d closed=%t", i+1, closed)
		}
	}
	if bucket.totalDrops != agentFrameDropsToClose {
		t.Fatalf("counted %d dropped frames, want %d", bucket.totalDrops, agentFrameDropsToClose)
	}
}
