package agent

import (
	"context"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestBuildBatchLinkCounterWindow(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	h.a.statsEvery = 10 * time.Second
	h.a.linkWindow = time.Minute
	start := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	collect := func(from, to time.Time, c engine.Collected) *pb.StatsBatch {
		t.Helper()
		h.eng.mu.Lock()
		h.eng.collectQueue = append(h.eng.collectQueue, c)
		h.eng.mu.Unlock()
		return h.a.buildBatch(context.Background(), from, to)
	}
	session := plugin.Session{CredID: "crd_a", InboundID: "inb_1", Since: start}

	first := collect(start, start.Add(10*time.Second), engine.Collected{
		Links:    []plugin.LinkStat{{CredID: "crd_a", InboundID: "inb_1", Connects: 1, Hellos: 2, Handshakes: 1}},
		Sessions: []plugin.Session{session, session},
	})
	if len(first.Links) != 1 || first.Links[0].Connects != 1 || first.Links[0].OnlineS != 0 || first.Links[0].Hellos != 0 || first.Links[0].Handshakes != 0 {
		t.Fatalf("events are immediate and volume is held: %+v", first.Links)
	}

	second := collect(start.Add(10*time.Second), start.Add(20*time.Second), engine.Collected{
		Links:    []plugin.LinkStat{{CredID: "crd_a", InboundID: "inb_1", Rejects: 1, Hellos: 3, Handshakes: 2}},
		Sessions: []plugin.Session{session, session, {CredID: "crd_b", InboundID: "inb_1", Since: start}},
	})
	if len(second.Links) != 1 || second.Links[0].CredId != "crd_a" || second.Links[0].Rejects != 1 || second.Links[0].Hellos != 0 {
		t.Fatalf("event deltas leave before the volume boundary: %+v", second.Links)
	}

	boundary := collect(start.Add(20*time.Second), start.Add(time.Minute), engine.Collected{
		Links:    []plugin.LinkStat{{CredID: "crd_a", InboundID: "inb_1", Drops: 1, Hellos: 1, Handshakes: 1}},
		Sessions: []plugin.Session{session, session, {CredID: "crd_b", InboundID: "inb_1", Since: start}},
	})
	if len(boundary.Links) != 2 {
		t.Fatalf("boundary should flush one row for each pair: %+v", boundary.Links)
	}
	rows := make(map[string]*pb.LinkCounters, len(boundary.Links))
	for _, row := range boundary.Links {
		rows[row.CredId] = row
	}
	wantA := &pb.LinkCounters{CredId: "crd_a", InboundId: "inb_1", Drops: 1, OnlineS: 40, Hellos: 6, Handshakes: 4}
	wantB := &pb.LinkCounters{CredId: "crd_b", InboundId: "inb_1", OnlineS: 30}
	if got := rows["crd_a"]; !sameLinkCounters(got, wantA) {
		t.Fatalf("event and volume counters should merge, and session time should dedupe and cap: got=%+v want=%+v", got, wantA)
	}
	if got := rows["crd_b"]; !sameLinkCounters(got, wantB) {
		t.Fatalf("second session pair = %+v, want %+v", got, wantB)
	}

	cleared := collect(start.Add(time.Minute), start.Add(time.Minute+10*time.Second), engine.Collected{})
	if len(cleared.Links) != 0 {
		t.Fatalf("flushed volume must restart its accumulator: %+v", cleared.Links)
	}
}

func sameLinkCounters(a, b *pb.LinkCounters) bool {
	return a != nil && b != nil &&
		a.CredId == b.CredId && a.InboundId == b.InboundId && a.Connects == b.Connects && a.Rejects == b.Rejects &&
		a.Drops == b.Drops && a.Stalls == b.Stalls && a.OnlineS == b.OnlineS && a.UnknownKeys == b.UnknownKeys &&
		a.Hellos == b.Hellos && a.Handshakes == b.Handshakes
}
