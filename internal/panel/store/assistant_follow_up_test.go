//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A follow-up lives in its conversation: deleting the conversation, all of an admin's, or retention takes it too; one for a
// conversation that is gone is refused. Claim, defer and finish hold one run at a time.
func TestAssistantFollowUpLifecycle(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	conv := func(id string, updated time.Time) {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_conversation(id,admin_id,created_at,updated_at) VALUES(?,?,?,?)`, id, "adm_a", unix(updated), unix(updated)); err != nil {
			t.Fatal(err)
		}
	}
	add := func(id, conversation string) {
		t.Helper()
		if err := st.InsertAssistantFollowUp(ctx, AssistantFollowUp{ID: id, ConversationID: conversation, AdminID: "adm_a", NoteEnc: []byte("x"), CreatedAt: now, DueAt: now.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		var n int
		_ = st.R.QueryRowContext(ctx, `SELECT count(*) FROM assistant_follow_up`).Scan(&n)
		return n
	}
	if err := st.InsertAssistantFollowUp(ctx, AssistantFollowUp{ID: "afu_gone", ConversationID: "cnv_none", AdminID: "adm_a", CreatedAt: now, DueAt: now}); !errors.Is(err, ErrAssistantConversationGone) {
		t.Fatalf("a follow-up for no conversation: %v", err)
	}
	conv("cnv_a", now)
	conv("cnv_old", now.Add(-200*24*time.Hour))
	add("afu_a", "cnv_a")
	add("afu_old", "cnv_old")

	// one run at a time; a deferred one comes back later, a finished one goes
	if ok, _ := st.ClaimAssistantFollowUp(ctx, "afu_a", "lease_1", now, now.Add(time.Hour)); ok {
		t.Fatal("claimed before it was due")
	}
	at := now.Add(2 * time.Minute)
	if ok, err := st.ClaimAssistantFollowUp(ctx, "afu_a", "lease_1", at, at.Add(time.Hour)); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if ok, _ := st.ClaimAssistantFollowUp(ctx, "afu_a", "lease_2", at, at.Add(time.Hour)); ok {
		t.Fatal("claimed twice")
	}
	if due, _ := st.DueAssistantFollowUps(ctx, at, 10); len(due) != 1 || due[0].ID != "afu_old" {
		t.Fatalf("due while one runs: %+v", due)
	}
	if err := st.DeferAssistantFollowUp(ctx, "afu_a", "lease_1", at.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	if f, _ := st.AssistantFollowUp(ctx, "adm_a", "afu_a"); f.Running(at) || f.Attempts != 1 || !f.DueAt.Equal(at.Add(time.Minute)) {
		t.Fatalf("deferred: %+v", f)
	}
	if n, err := st.RecoverAssistantFollowUps(ctx); err != nil || n != 0 {
		t.Fatalf("recovered %d with nothing running: %v", n, err)
	}
	unread, next, err := st.UnreadAssistantConversations(ctx, "adm_a")
	if err != nil || unread != 0 || !next.Equal(now.Add(time.Minute)) {
		t.Fatalf("unread %d, next %s: %v", unread, next, err)
	}

	if _, err := st.RetainAssistantConversations(ctx, now.Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if count() != 1 {
		t.Fatalf("retention left %d follow-ups", count())
	}
	if _, err := st.DeleteAssistantConversation(ctx, "adm_a", "cnv_a"); err != nil {
		t.Fatal(err)
	}
	if count() != 0 {
		t.Fatal("a deleted conversation's follow-up stayed")
	}
	conv("cnv_b", now)
	add("afu_b", "cnv_b")
	if _, err := st.DeleteAssistantConversations(ctx, "adm_a", false); err != nil || count() != 0 {
		t.Fatalf("delete all: %d left, %v", count(), err)
	}
}
