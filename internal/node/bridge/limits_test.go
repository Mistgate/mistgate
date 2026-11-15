package bridge

import (
	"context"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

func TestBridgeEntryResourceLimits(t *testing.T) {
	e := NewEntry()
	defer e.Close()
	peer := e.peer("nod_de1")
	now := time.Now()

	if MaxConnectionsPerExit < 2*MaxPoolSize {
		t.Fatalf("exit connection limit = %d, want at least twice max pool %d", MaxConnectionsPerExit, MaxPoolSize)
	}
	for i := 0; i < MaxConnectionsPerExit; i++ {
		key := [32]byte{byte(i + 1)}
		if code := e.acquireHalf(peer, key, now); code != "" {
			t.Fatalf("connection %d: %s", i, code)
		}
	}
	if code := e.acquireHalf(peer, [32]byte{MaxConnectionsPerExit + 1}, now); code != "connection_limit" {
		t.Fatalf("connection limit error = %q", code)
	}
	for key := range peer.connections {
		e.releaseHalf(peer, key)
	}

	peer.active = MaxStreamsPerExit
	_, _, code := e.joinPair(peer, pairKey{id: "limit-exit"}, pairDown)
	if code != "" {
		t.Fatalf("first half rejected before pair completion: %q", code)
	}
	_, _, code = e.joinPair(peer, pairKey{id: "limit-exit"}, pairUp)
	if code != "stream_limit" {
		t.Fatalf("per-exit stream limit error = %q", code)
	}
	peer.active = 0

	e.totalStreams = MaxStreamsPerEntry
	_, _, code = e.joinPair(peer, pairKey{id: "limit-entry"}, pairDown)
	if code != "" {
		t.Fatalf("entry limit first half = %q", code)
	}
	_, _, code = e.joinPair(peer, pairKey{id: "limit-entry"}, pairUp)
	if code != "stream_limit" {
		t.Fatalf("per-entry stream limit error = %q", code)
	}
	e.totalStreams = 0

	for i := 0; i < MaxPendingClients; i++ {
		if !e.acquireClient(peer) {
			t.Fatalf("pending client %d was rejected early", i)
		}
	}
	if e.acquireClient(peer) {
		t.Fatal("per-exit pending client limit was not enforced")
	}
	for i := 0; i < MaxPendingClients; i++ {
		e.releaseClient(peer)
	}
	if AcceptQueueSize < 512 {
		t.Fatalf("accept queue size = %d: a burst of 200 people must fit (they arrive faster than the listener hands them out)", AcceptQueueSize)
	}

	q := newPendingQueue()
	first := testPair("first")
	second := testPair("second")
	for i := 0; i < PendingStreamsPerExit; i++ {
		if !q.offer(testPair("pending")) {
			t.Fatalf("pending pair %d was rejected early", i)
		}
	}
	if q.offer(testPair("overflow")) {
		t.Fatal("pending stream queue limit was not enforced")
	}
	for _, pair := range q.items {
		close(pair.done)
	}
	if !q.offer(first) || !q.offer(second) {
		t.Fatal("queue rejected pairs after stale entries were available")
	}
	got, err := q.take(context.Background(), time.Second)
	if err != nil || got != second {
		t.Fatalf("queue did not take the newest pair first: got %v, err %v", got, err)
	}
	got, err = q.take(context.Background(), time.Second)
	if err != nil || got != first {
		t.Fatalf("queue second take = %v, err %v", got, err)
	}
}

func testPair(id string) *bridgePair {
	return &bridgePair{key: pairKey{id: id}, done: make(chan struct{})}
}

// The panel tells the owner how many people a bridge carries from plugin.BridgePeoplePerConn; it has to follow the agent's own
// waiting-pair count, or the figure on the screen is a different bridge's.
func TestPanelCapacityFigureFollowsTheWaitingPairs(t *testing.T) {
	if plugin.BridgePendingPairs != PendingWorkersPerConn {
		t.Fatalf("plugin.BridgePendingPairs = %d, the dialer keeps %d waiting pairs a connection", plugin.BridgePendingPairs, PendingWorkersPerConn)
	}
	want := (plugin.BridgeHTTP2Streams - 2*PendingWorkersPerConn) / 2
	if plugin.BridgePeoplePerConn != want {
		t.Fatalf("people a connection carries = %d, want %d", plugin.BridgePeoplePerConn, want)
	}
	if want != 121 {
		t.Fatalf("the figure moved to %d: the screens and the notes say about 120 people a connection", want)
	}
}
