//go:build !js

package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/assistant"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// followUpFixture stores a conversation of the owner and a follow-up in it, due at due.
func followUpFixture(t *testing.T, f *fixture, id string, due time.Time) store.AssistantFollowUp {
	t.Helper()
	ctx := context.Background()
	conv := "cnv_" + id
	if _, err := f.st.W.ExecContext(ctx, `INSERT INTO assistant_conversation(id,admin_id,created_at,updated_at) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING`, conv, f.owner.ID, f.now().Unix(), f.now().Unix()); err != nil {
		t.Fatal(err)
	}
	fu := store.AssistantFollowUp{ID: id, ConversationID: conv, AdminID: f.owner.ID, NoteEnc: []byte("sealed"), CreatedAt: f.now().Add(-10 * time.Minute), DueAt: due}
	if err := f.st.InsertAssistantFollowUp(ctx, fu); err != nil {
		t.Fatal(err)
	}
	return fu
}

func (f *fixture) followUpRows(t *testing.T) map[string]store.AssistantFollowUp {
	t.Helper()
	out := map[string]store.AssistantFollowUp{}
	for _, conv := range []string{"cnv_afu_a", "cnv_afu_b", "cnv_afu_c"} {
		list, err := f.st.AssistantFollowUps(context.Background(), f.owner.ID, conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range list {
			out[r.ID] = r
		}
	}
	return out
}

func (f *fixture) runFollowUpsDue(t *testing.T) {
	t.Helper()
	f.svc.dispatchFollowUps(context.Background())
	f.svc.wg.Wait()
}

// A due follow-up runs once: under its lease a second tick does not start it again, and the row goes when its run ends. One
// not due yet waits.
func TestFollowUpRunsOnceUnderItsLease(t *testing.T) {
	f := newFixture(t)
	due := followUpFixture(t, f, "afu_a", f.now().Add(-time.Second))
	followUpFixture(t, f, "afu_b", f.now().Add(time.Hour))
	enter, hold := make(chan struct{}, 1), make(chan struct{})
	f.asst.followEnter, f.asst.followHold = enter, hold
	f.svc.dispatchFollowUps(context.Background())
	<-enter
	// the run holds its lease: another tick finds nothing to start
	f.svc.dispatchFollowUps(context.Background())
	rows := f.followUpRows(t)
	if !rows["afu_a"].Running(f.now()) {
		t.Fatalf("the running follow-up holds no lease: %+v", rows["afu_a"])
	}
	close(hold)
	f.svc.wg.Wait()
	f.asst.mu.Lock()
	runs := append([]store.AssistantFollowUp(nil), f.asst.followUps...)
	f.asst.mu.Unlock()
	if len(runs) != 1 || runs[0].ID != due.ID || runs[0].ConversationID != due.ConversationID || runs[0].AdminID != f.owner.ID {
		t.Fatalf("runs: %+v", runs)
	}
	rows = f.followUpRows(t)
	if _, still := rows["afu_a"]; still {
		t.Fatal("the follow-up that ran is still waiting")
	}
	if _, waits := rows["afu_b"]; !waits {
		t.Fatal("the follow-up not due yet is gone")
	}
}

// A run that says "deferred" gives the follow-up back with a later time; a busy conversation does not count as a try, a
// failed store does.
func TestFollowUpDeferred(t *testing.T) {
	f := newFixture(t)
	followUpFixture(t, f, "afu_a", f.now().Add(-time.Second))
	f.asst.followOut = assistant.FollowUpOutcome{Status: "deferred", Reason: "conversation_busy", RetryAfter: 30 * time.Second}
	f.runFollowUpsDue(t)
	row := f.followUpRows(t)["afu_a"]
	if row.Running(f.now()) || !row.DueAt.Equal(f.now().Add(30*time.Second)) || row.Attempts != 0 {
		t.Fatalf("after a busy conversation: %+v", row)
	}
	f.advance(31 * time.Second)
	f.asst.followOut = assistant.FollowUpOutcome{Status: "deferred", Reason: "internal", RetryAfter: 2 * time.Minute, Counted: true}
	f.runFollowUpsDue(t)
	if row = f.followUpRows(t)["afu_a"]; row.Attempts != 1 {
		t.Fatalf("after a store failure: %+v", row)
	}
	f.advance(3 * time.Minute)
	f.asst.followOut = assistant.FollowUpOutcome{Status: "failed", Reason: "budget_reached"}
	f.runFollowUpsDue(t)
	if _, still := f.followUpRows(t)["afu_a"]; still {
		t.Fatal("a failed follow-up keeps waiting (its note is in the conversation)")
	}
}

// A follow-up a stop of the panel left running is given back at start and runs again (the try is counted); one missed while
// the panel was down runs once on start. Whether it is too late is the assistant's to say (FollowUpLate): the scheduler gives
// it the follow-up as it was.
func TestFollowUpRecoveryAfterARestart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	stale := followUpFixture(t, f, "afu_a", f.now().Add(-time.Minute))
	if ok, err := f.st.ClaimAssistantFollowUp(ctx, stale.ID, "lease_stale", f.now(), f.now().Add(24*time.Hour)); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	missed := followUpFixture(t, f, "afu_b", f.now().Add(30*time.Minute))
	tooLate := followUpFixture(t, f, "afu_c", f.now().Add(-26*time.Hour))
	// the panel was down for six hours
	f.advance(6 * time.Hour)
	runCtx, stop := context.WithCancel(ctx)
	// two follow-ups run at a time: the third goes on a later tick
	f.svc.cfg.Tick = 20 * time.Millisecond
	done := make(chan struct{})
	go func() { f.svc.Run(runCtx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.followUpRows(t)) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	<-done
	if left := f.followUpRows(t); len(left) != 0 {
		t.Fatalf("still waiting after the restart: %+v", left)
	}
	f.asst.mu.Lock()
	runs := map[string]store.AssistantFollowUp{}
	for _, r := range f.asst.followUps {
		if _, dup := runs[r.ID]; dup {
			t.Errorf("%s ran twice", r.ID)
		}
		runs[r.ID] = r
	}
	f.asst.mu.Unlock()
	if r := runs[stale.ID]; r.Attempts != 1 || r.LeaseID == "lease_stale" {
		t.Fatalf("the interrupted one: %+v", r)
	}
	if r := runs[missed.ID]; !r.DueAt.Equal(missed.DueAt) {
		t.Fatalf("the missed one: %+v", r)
	}
	if r, ok := runs[tooLate.ID]; !ok || !r.DueAt.Equal(tooLate.DueAt) {
		t.Fatalf("the one a day late was not handed over as it was: %+v", r)
	}
}

// Without the assistant no follow-up runs, and the rows stay for when it is back.
func TestFollowUpsWaitWithoutTheAssistant(t *testing.T) {
	f := newFixture(t)
	followUpFixture(t, f, "afu_a", f.now().Add(-time.Second))
	f.svc.cfg.Assistant = nil
	f.runFollowUpsDue(t)
	if _, waits := f.followUpRows(t)["afu_a"]; !waits {
		t.Fatal("the follow-up went without a run")
	}
}
