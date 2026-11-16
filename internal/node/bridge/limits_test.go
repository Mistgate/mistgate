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

	if MaxPeopleConnsPerExit < 2*MaxPoolSize {
		t.Fatalf("people connection limit = %d, want the pool and its draining connections (%d)", MaxPeopleConnsPerExit, 2*MaxPoolSize)
	}
	for i := 0; i < MaxPeopleConnsPerExit; i++ {
		key := [32]byte{byte(i + 1)}
		if code := e.acquireHalf(peer, key, now, ""); code != "" {
			t.Fatalf("connection %d: %s", i, code)
		}
	}
	if code := e.acquireHalf(peer, [32]byte{MaxConnectionsPerExit + 1}, now, ""); code != "connection_limit" {
		t.Fatalf("connection limit error = %q", code)
	}
	for key := range peer.connections {
		e.releaseHalf(peer, key)
	}

	peer.active = MaxStreamsPerExit
	_, _, code := e.joinPair(peer, pairKey{id: "limit-exit"}, pairDown, "")
	if code != "" {
		t.Fatalf("first half rejected before pair completion: %q", code)
	}
	_, _, code = e.joinPair(peer, pairKey{id: "limit-exit"}, pairUp, "")
	if code != "stream_limit" {
		t.Fatalf("per-exit stream limit error = %q", code)
	}
	peer.active = 0

	e.totalStreams = MaxStreamsPerEntry
	_, _, code = e.joinPair(peer, pairKey{id: "limit-entry"}, pairDown, "")
	if code != "" {
		t.Fatalf("entry limit first half = %q", code)
	}
	_, _, code = e.joinPair(peer, pairKey{id: "limit-entry"}, pairUp, "")
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

// The control link and the people have separate connection budgets, and neither can use up the other's.
func TestControlLinkAndPeopleKeepSeparateConnectionBudgets(t *testing.T) {
	e := NewEntry()
	defer e.Close()
	peer := e.peer("nod_de1")
	now := time.Now()

	// People fill their whole budget: the control link still gets in, and only up to its own.
	for i := 0; i < MaxPeopleConnsPerExit; i++ {
		if code := e.acquireHalf(peer, [32]byte{1, byte(i)}, now, ""); code != "" {
			t.Fatalf("people connection %d: %s", i, code)
		}
	}
	if code := e.acquireHalf(peer, [32]byte{2, 0}, now, ""); code != "connection_limit" {
		t.Fatalf("the people's limit = %q", code)
	}
	for i := 0; i < MaxPanelConnsPerExit; i++ {
		if code := e.acquireHalf(peer, [32]byte{3, byte(i)}, now, StreamKindPanel); code != "" {
			t.Fatalf("control connection %d is refused although people hold %d connections: %s", i, MaxPeopleConnsPerExit, code)
		}
	}
	if code := e.acquireHalf(peer, [32]byte{3, 99}, now, StreamKindPanel); code != "control_limit" {
		t.Fatalf("the control link's limit = %q", code)
	}
	if got := peerConns(e, "nod_de1"); got != MaxPeopleConnsPerExit {
		t.Fatalf("the pool the panel reads counts %d connections, want the people's %d without the control link's", got, MaxPeopleConnsPerExit)
	}

	// The other way round: a control link in place does not take a person's connection.
	e.releaseHalf(peer, [32]byte{1, 0})
	if code := e.acquireHalf(peer, [32]byte{4, 0}, now, ""); code != "" {
		t.Fatalf("a person's connection is refused while the control link is up: %s", code)
	}

	// A connection keeps the class of its first request.
	if code := e.acquireHalf(peer, [32]byte{3, 0}, now, ""); code != "stream_kind_mismatch" {
		t.Fatalf("a person's request on the control link's connection = %q", code)
	}
	if code := e.acquireHalf(peer, [32]byte{4, 0}, now, StreamKindPanel); code != "stream_kind_mismatch" {
		t.Fatalf("a control request on a person's connection = %q", code)
	}
}

// People can fill every stream slot of an exit and of the entry; the control link needs none and is not refused. And the
// other way: it takes none from them.
func TestControlLinkTakesNoStreamSlot(t *testing.T) {
	e := NewEntry()
	defer e.Close()
	peer := e.peer("nod_de1")
	peer.active = MaxStreamsPerExit
	e.totalStreams = MaxStreamsPerEntry

	key := pairKey{id: "control-link-slot"}
	if _, _, code := e.joinPair(peer, key, pairDown, StreamKindPanel); code != "" {
		t.Fatalf("first half: %q", code)
	}
	pair, paired, code := e.joinPair(peer, key, pairUp, StreamKindPanel)
	if code != "" || !paired {
		t.Fatalf("control link with every stream slot taken: paired %t, code %q", paired, code)
	}
	if peer.active != MaxStreamsPerExit || e.totalStreams != MaxStreamsPerEntry {
		t.Fatalf("the control link took a stream slot: exit %d, entry %d", peer.active, e.totalStreams)
	}
	e.finishPair(pair, "closed", true)
	if peer.active != MaxStreamsPerExit || e.totalStreams != MaxStreamsPerEntry {
		t.Fatalf("ending the control link gave back a slot it never took: exit %d, entry %d", peer.active, e.totalStreams)
	}

	peer.active, e.totalStreams = MaxStreamsPerExit-1, MaxStreamsPerEntry-1
	ctl := pairKey{id: "control-link-first"}
	e.joinPair(peer, ctl, pairDown, StreamKindPanel)
	e.joinPair(peer, ctl, pairUp, StreamKindPanel)
	person := pairKey{id: "person-after-control"}
	if _, _, code := e.joinPair(peer, person, pairDown, ""); code != "" {
		t.Fatal(code)
	}
	if _, paired, code := e.joinPair(peer, person, pairUp, ""); code != "" || !paired {
		t.Fatalf("a person is refused the last slot because a control link exists: paired %t, code %q", paired, code)
	}
}

func peerConns(e *Entry, id string) int {
	for _, s := range e.States() {
		if s.PeerNodeID == id {
			return int(s.Conns)
		}
	}
	return -1
}
