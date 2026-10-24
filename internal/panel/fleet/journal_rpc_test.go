package fleet

import (
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func connectJournal(a *agent, instance string) *conn {
	a.e.t.Helper()
	c := a.open()
	h := hello(instance, 0, "")
	h.GetHello().Capabilities = []string{capJournal}
	c.send(0, h)
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	ds := c.desired()
	m := newModel()
	m.apply(ds)
	c.send(0, applied(ds, m.hash()))
	return c
}

func journalBatch(entries ...*agentv1.JournalEntry) *agentv1.ConnectRequest {
	now := time.Now().Unix()
	m := statsBatch(now-10, now, nil, nil)
	m.GetStats().Journal = entries
	return m
}

func TestGetNodeJournalRPC(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c := connectJournal(a, "journal-rpc")
	now := time.Now().Unix()
	c.send(1, journalBatch(
		&agentv1.JournalEntry{Level: agentv1.Severity_SEVERITY_WARNING, Source: "awg", Message: "Handshake failed", InboundId: ids.i1,
			Attrs: map[string]string{"code": "stall"}, Count: 2, FirstUnix: now - 2, LastUnix: now - 1},
		&agentv1.JournalEntry{Level: agentv1.Severity_SEVERITY_ERROR, Source: "xray", Message: "AUTH%_FAILED", InboundId: ids.i1,
			Attrs: map[string]string{"code": "auth_failed", "detail": "auth%_failed"}, Count: 1, FirstUnix: now - 3, LastUnix: now},
		&agentv1.JournalEntry{Level: agentv1.Severity_SEVERITY_WARNING, Source: "awg", Message: "foreign inbound", InboundId: ids.i3,
			Attrs: map[string]string{"code": "foreign"}, Count: 1, FirstUnix: now, LastUnix: now},
	))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 1 })

	node := nodeService{e.f}
	r, err := node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{
		NodeId: a.nodeID, Source: "awg", InboundId: ids.i1, Query: "HANDSHAKE", Limit: 1,
	}))
	if err != nil || len(r.Msg.Entries) != 1 || r.Msg.Truncated {
		t.Fatalf("filtered journal = %+v, %v", r, err)
	}
	entry := r.Msg.Entries[0]
	if entry.NodeId != a.nodeID || entry.NodeName != "nodea" || entry.ProfileName != "p1" || entry.InboundId != ids.i1 ||
		entry.Level != adminv1.EventSeverity_EVENT_SEVERITY_WARNING || entry.Count != 2 || entry.Attrs["code"] != "stall" {
		t.Errorf("journal entry = %+v", entry)
	}
	if r.Msg.NowUnix-r.Msg.SinceUnix != 24*3600 {
		t.Errorf("default hours = %d s, want 24 h", r.Msg.NowUnix-r.Msg.SinceUnix)
	}

	r, err = node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{
		NodeId: a.nodeID, MinLevel: adminv1.EventSeverity_EVENT_SEVERITY_INFO, Source: "xray", Query: "auth%_failed",
	}))
	if err != nil || len(r.Msg.Entries) != 1 || r.Msg.Entries[0].Level != adminv1.EventSeverity_EVENT_SEVERITY_ERROR {
		t.Errorf("ERROR/escaped attribute query = %+v, %v", r, err)
	}
	r, err = node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{NodeId: a.nodeID, Query: "foreign inbound"}))
	if err != nil || len(r.Msg.Entries) != 1 || r.Msg.Entries[0].InboundId != "" || r.Msg.Entries[0].ProfileName != "" {
		t.Errorf("foreign inbound was attributed: %+v, %v", r, err)
	}

	all, err := node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{Limit: 1}))
	if err != nil || len(all.Msg.Entries) != 1 || !all.Msg.Truncated {
		t.Errorf("truncated all-node journal = %+v, %v", all, err)
	}
	without := false
	for _, id := range all.Msg.NodesWithoutJournal {
		if id == ids.nodeB {
			without = true
		}
	}
	if !without {
		t.Errorf("nodes_without_journal = %v, missing %s", all.Msg.NodesWithoutJournal, ids.nodeB)
	}
	wide, err := node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{NodeId: a.nodeID, Hours: 999}))
	if err != nil || wide.Msg.NowUnix-wide.Msg.SinceUnix != int64(168*3600) {
		t.Errorf("maximum hours = %+v, %v", wide, err)
	}
	if _, err := node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{NodeId: "nod_missing"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("unknown node error = %v, want NotFound", err)
	}

	for batch := 0; batch < 4; batch++ {
		entries := make([]*agentv1.JournalEntry, 64)
		for i := range entries {
			kind := batch*64 + i
			code := fmt.Sprintf("bulk-%03d", kind)
			entries[i] = &agentv1.JournalEntry{Level: agentv1.Severity_SEVERITY_WARNING, Source: "bulk", Message: code,
				InboundId: ids.i1, Attrs: map[string]string{"code": code}, Count: 1, FirstUnix: now, LastUnix: now}
		}
		c.send(uint64(batch+2), journalBatch(entries...))
		seq := uint64(batch + 2)
		c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= seq })
	}
	page, err := node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{NodeId: a.nodeID, Source: "bulk", Limit: 201}))
	if err != nil || len(page.Msg.Entries) != 200 || !page.Msg.Truncated {
		t.Errorf("capped limit page = entries %d truncated %v err %v, want 200 and true", len(page.Msg.Entries), page.Msg.GetTruncated(), err)
	}
}
