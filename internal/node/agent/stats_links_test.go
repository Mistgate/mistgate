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
	h.a.linkWindow = 10 * time.Minute
	start := time.Date(2026, 10, 23, 12, 9, 30, 0, time.UTC)
	collect := func(from, to time.Time, c engine.Collected) *pb.StatsBatch {
		t.Helper()
		h.eng.mu.Lock()
		h.eng.collectQueue = append(h.eng.collectQueue, c)
		h.eng.mu.Unlock()
		return h.a.buildBatch(context.Background(), from, to)
	}
	session := plugin.Session{CredID: "crd_a", InboundID: "inb_1", Since: start}

	first := collect(start, start.Add(10*time.Second), engine.Collected{
		Links: []plugin.LinkStat{
			{CredID: "crd_a", InboundID: "inb_1", Connects: 1, Hellos: 2, Handshakes: 1, ClientCloses: 1, Freezes: 2, Quiet: 3, RTTMSSum: 120, RTTSamples: 2, PktsSent: 50, PktsLost: 2},
			{InboundID: "inb_1", Accepted: 2, HellosForeign: 1},
		},
		Sessions: []plugin.Session{session, session},
	})
	if len(first.Links) != 1 || first.Links[0].Connects != 1 || first.Links[0].ClientCloses != 1 || first.Links[0].Freezes != 2 || first.Links[0].Quiet != 3 ||
		first.Links[0].OnlineS != 0 || first.Links[0].Hellos != 0 || first.Links[0].Handshakes != 0 || first.Links[0].Accepted != 0 || first.Links[0].HellosForeign != 0 ||
		first.Links[0].RttMsSum != 0 || first.Links[0].RttSamples != 0 || first.Links[0].PktsSent != 0 || first.Links[0].PktsLost != 0 {
		t.Fatalf("events are immediate and every volume is held: %+v", first.Links)
	}

	second := collect(start.Add(10*time.Second), start.Add(20*time.Second), engine.Collected{
		Links: []plugin.LinkStat{
			{CredID: "crd_a", InboundID: "inb_1", Rejects: 1, Hellos: 3, Handshakes: 2, RTTMSSum: 180, RTTSamples: 3, PktsSent: 70, PktsLost: 4},
			{InboundID: "inb_1", Accepted: 3, HellosForeign: 2},
		},
		Sessions: []plugin.Session{session, session, {CredID: "crd_b", InboundID: "inb_1", Since: start}},
	})
	if len(second.Links) != 1 || second.Links[0].CredId != "crd_a" || second.Links[0].Rejects != 1 || second.Links[0].Hellos != 0 ||
		second.Links[0].Accepted != 0 || second.Links[0].HellosForeign != 0 || second.Links[0].RttMsSum != 0 || second.Links[0].RttSamples != 0 || second.Links[0].PktsSent != 0 || second.Links[0].PktsLost != 0 {
		t.Fatalf("event deltas leave before the volume boundary: %+v", second.Links)
	}

	boundary := collect(start.Add(20*time.Second), start.Add(30*time.Second), engine.Collected{
		Links: []plugin.LinkStat{
			{CredID: "crd_a", InboundID: "inb_1", Drops: 1, Hellos: 1, Handshakes: 1, RTTMSSum: 120, RTTSamples: 1, PktsSent: 30, PktsLost: 1},
			{InboundID: "inb_1", Accepted: 1, HellosForeign: 1},
		},
		Sessions: []plugin.Session{session, session, {CredID: "crd_b", InboundID: "inb_1", Since: start}},
	})
	if len(boundary.Links) != 3 {
		t.Fatalf("boundary should flush one row per credential and inbound: %+v", boundary.Links)
	}
	rows := make(map[string]*pb.LinkCounters, len(boundary.Links))
	for _, row := range boundary.Links {
		rows[row.CredId] = row
	}
	wantA := &pb.LinkCounters{CredId: "crd_a", InboundId: "inb_1", Drops: 1, OnlineS: 30, Hellos: 6, Handshakes: 4, RttMsSum: 420, RttSamples: 6, PktsSent: 150, PktsLost: 7}
	wantB := &pb.LinkCounters{CredId: "crd_b", InboundId: "inb_1", OnlineS: 20}
	wantInbound := &pb.LinkCounters{InboundId: "inb_1", Accepted: 6, HellosForeign: 4}
	if got := rows["crd_a"]; !sameLinkCounters(got, wantA) {
		t.Fatalf("event and volume counters should merge, and session time should dedupe and cap: got=%+v want=%+v", got, wantA)
	}
	if got := rows["crd_b"]; !sameLinkCounters(got, wantB) {
		t.Fatalf("second session pair = %+v, want %+v", got, wantB)
	}
	if got := rows[""]; !sameLinkCounters(got, wantInbound) {
		t.Fatalf("inbound volume row = %+v, want %+v", got, wantInbound)
	}

	cleared := collect(start.Add(30*time.Second), start.Add(40*time.Second), engine.Collected{})
	if len(cleared.Links) != 0 {
		t.Fatalf("flushed volume must restart its accumulator: %+v", cleared.Links)
	}
}

// links/3: the TCP stalls are events (the next batch), the handshake failures and the AmneziaWG handshake counts are volume
// (held to the 10-minute boundary with hellos and accepted, so their ratios hold within one bucket).
func TestBuildBatchLinkCountersV3(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	h.a.statsEvery = 10 * time.Second
	h.a.linkWindow = 10 * time.Minute
	start := time.Date(2026, 11, 7, 12, 9, 40, 0, time.UTC)
	collect := func(from, to time.Time, c engine.Collected) *pb.StatsBatch {
		t.Helper()
		h.eng.mu.Lock()
		h.eng.collectQueue = append(h.eng.collectQueue, c)
		h.eng.mu.Unlock()
		return h.a.buildBatch(context.Background(), from, to)
	}
	first := collect(start, start.Add(10*time.Second), engine.Collected{Links: []plugin.LinkStat{
		{CredID: "crd_v", InboundID: "inb_v", Connects: 1, TCPStalls: 2, TCPStalls16K: 1},
		{InboundID: "inb_v", Hellos: 5, HellosForeign: 1, RealityFallback: 2, AuthFailed: 1, HsUnfinished: 1},
		{InboundID: "inb_h", TLSAlertsSent: 1, TLSAlertsRecv: 2, TLSCertRefused: 2, HsUnfinished: 3},
		{CredID: "crd_w", InboundID: "inb_w", Initiations: 3, Responses: 3, ResponsesUnmatched: 2},
	}})
	if len(first.Links) != 1 || !sameLinkCounters(first.Links[0], &pb.LinkCounters{CredId: "crd_v", InboundId: "inb_v", Connects: 1}) ||
		first.Links[0].TcpStalls != 2 || first.Links[0].TcpStalls_16K != 1 {
		t.Fatalf("the stalls leave at once, the rest waits for the boundary: %+v", first.Links)
	}
	boundary := collect(start.Add(10*time.Second), start.Add(20*time.Second), engine.Collected{Links: []plugin.LinkStat{
		{CredID: "crd_w", InboundID: "inb_w", Initiations: 1, Responses: 1},
	}})
	rows := map[string]*pb.LinkCounters{}
	for _, r := range boundary.Links {
		rows[r.InboundId+"/"+r.CredId] = r
	}
	if r := rows["inb_v/"]; r == nil || r.Hellos != 5 || r.HellosForeign != 1 || r.RealityFallback != 2 || r.AuthFailed != 1 || r.HsUnfinished != 1 {
		t.Fatalf("VLESS inbound row = %+v", r)
	}
	if r := rows["inb_h/"]; r == nil || r.TlsAlertsSent != 1 || r.TlsAlertsRecv != 2 || r.TlsCertRefused != 2 || r.HsUnfinished != 3 {
		t.Fatalf("Hysteria2 inbound row = %+v", r)
	}
	if r := rows["inb_w/crd_w"]; r == nil || r.Initiations != 4 || r.Responses != 4 || r.ResponsesUnmatched != 2 {
		t.Fatalf("AmneziaWG key row = %+v", r)
	}
	if len(boundary.Links) != 3 {
		t.Fatalf("boundary rows = %+v", boundary.Links)
	}
}

func TestCapabilitiesAdvertiseLinkCountersV3(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	if !contains(h.a.capabilities(), "links/3") {
		t.Fatalf("capabilities = %v, want links/3", h.a.capabilities())
	}
}

func TestCapabilitiesAdvertiseLinkCountersV2(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	if !contains(h.a.capabilities(), "links/2") {
		t.Fatalf("capabilities = %v, want links/2", h.a.capabilities())
	}
}

func sameLinkCounters(a, b *pb.LinkCounters) bool {
	return a != nil && b != nil &&
		a.CredId == b.CredId && a.InboundId == b.InboundId && a.Connects == b.Connects && a.Rejects == b.Rejects &&
		a.Drops == b.Drops && a.Stalls == b.Stalls && a.OnlineS == b.OnlineS && a.UnknownKeys == b.UnknownKeys &&
		a.Hellos == b.Hellos && a.Handshakes == b.Handshakes && a.ClientCloses == b.ClientCloses && a.Freezes == b.Freezes &&
		a.Quiet == b.Quiet && a.Accepted == b.Accepted && a.HellosForeign == b.HellosForeign && a.RttMsSum == b.RttMsSum &&
		a.RttSamples == b.RttSamples && a.PktsSent == b.PktsSent && a.PktsLost == b.PktsLost
}
