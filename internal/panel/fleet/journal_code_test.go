package fleet

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The node journal is filterable by the "code" attribute of its entries: an exact, case-insensitive match, before the page is cut.
func TestGetNodeJournalFiltersByCode(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	c := connectJournal(a, "journal-code")
	now := time.Now().Unix()
	c.send(1, journalBatch(
		&agentv1.JournalEntry{Level: agentv1.Severity_SEVERITY_WARNING, Source: "awg", Message: "one", InboundId: ids.i1,
			Attrs: map[string]string{"code": "engine_failed"}, Count: 1, FirstUnix: now - 2, LastUnix: now - 2},
		&agentv1.JournalEntry{Level: agentv1.Severity_SEVERITY_WARNING, Source: "awg", Message: "two", InboundId: ids.i1,
			Attrs: map[string]string{"code": "engine_failed_twice"}, Count: 1, FirstUnix: now - 1, LastUnix: now - 1},
		&agentv1.JournalEntry{Level: agentv1.Severity_SEVERITY_ERROR, Source: "awg", Message: "three", InboundId: ids.i1,
			Attrs: map[string]string{"detail": "no code"}, Count: 1, FirstUnix: now, LastUnix: now},
	))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= 1 })

	node := nodeService{e.f}
	r, err := node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{NodeId: a.nodeID, Code: "ENGINE_FAILED"}))
	if err != nil || len(r.Msg.Entries) != 1 || r.Msg.Entries[0].Message != "one" {
		t.Fatalf("by code = %+v, %v", r, err)
	}
	r, err = node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{NodeId: a.nodeID, Code: "engine_failed", Limit: 1}))
	if err != nil || len(r.Msg.Entries) != 1 || r.Msg.Truncated {
		t.Fatalf("the filter must apply before the limit: %+v, %v", r, err)
	}
	r, err = node.GetNodeJournal(e.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{NodeId: a.nodeID, Code: "nope"}))
	if err != nil || len(r.Msg.Entries) != 0 {
		t.Fatalf("unknown code = %+v, %v", r, err)
	}
}
