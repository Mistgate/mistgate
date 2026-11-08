//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func followUpArgs(seconds int, note string) []byte {
	b, _ := json.Marshal(map[string]any{"in_seconds": seconds, "note": note})
	return b
}

// setFollowUp calls the follow_up tool as the model would, in a conversation of the fixture's owner.
func setFollowUp(t *testing.T, f *assistantFixture, conversationID string, raw []byte) toolExecution {
	t.Helper()
	return f.service.executeLocalTool(context.Background(), f.admin, conversationID, followUpTool, raw)
}

func onlyFollowUp(t *testing.T, f *assistantFixture, conversationID string) store.AssistantFollowUp {
	t.Helper()
	list, err := f.st.AssistantFollowUps(context.Background(), f.admin.ID, conversationID)
	if err != nil || len(list) != 1 {
		t.Fatalf("follow-ups of %s: %+v %v", conversationID, list, err)
	}
	return list[0]
}

// The tool checks its time and note, scrubs the note, keeps it sealed, does not set the same follow-up twice, and holds the
// limits: 3 waiting in a conversation, 20 for an admin. follow_up_cancel removes one of this conversation only.
func TestFollowUpToolChecksAndLimits(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	conv, err := f.service.createConversation(ctx, f.admin.ID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{followUpArgs(10, "too soon"), followUpArgs(86401, "too late"), followUpArgs(600, "   ")} {
		if got := setFollowUp(t, f, conv, bad); !got.output.err {
			t.Fatalf("accepted %s: %s", bad, got.output.text)
		}
	}
	secret := "sk-ant-api03-" + strings.Repeat("Q7w8", 10)
	raw := "read node_debug_logs of req_1 with key " + secret + " password: hunter2-secret"
	set := setFollowUp(t, f, conv, followUpArgs(600, raw))
	if set.output.err || set.followUp == nil || set.followUp.DueUnix == 0 {
		t.Fatalf("set: %+v", set)
	}
	if strings.Contains(set.output.text, secret) || strings.Contains(set.output.text, "hunter2-secret") || strings.Contains(set.followUp.Note, "hunter2-secret") {
		t.Fatalf("a secret stayed in the note: %s", set.output.text)
	}
	if !strings.Contains(set.output.text, `"at_utc"`) || !strings.Contains(set.output.text, `"at_panel_time"`) {
		t.Fatalf("the model is not told when: %s", set.output.text)
	}
	stored := onlyFollowUp(t, f, conv)
	if strings.Contains(string(stored.NoteEnc), "node_debug_logs") {
		t.Fatal("the note is stored in clear")
	}
	if due := time.Until(stored.DueAt); due < 590*time.Second || due > 610*time.Second {
		t.Fatalf("due in %s, want 10 min", due)
	}
	// the same note at the same moment is the same follow-up
	again := setFollowUp(t, f, conv, followUpArgs(620, raw))
	if again.output.err || !strings.Contains(again.output.text, `"already_set":true`) || again.followUp.Id != stored.ID {
		t.Fatalf("set twice: %s", again.output.text)
	}
	onlyFollowUp(t, f, conv)
	setFollowUp(t, f, conv, followUpArgs(1200, "second"))
	setFollowUp(t, f, conv, followUpArgs(1800, "third"))
	if got := setFollowUp(t, f, conv, followUpArgs(2400, "fourth")); !got.output.err || !strings.Contains(got.output.text, "3 follow-ups") {
		t.Fatalf("a fourth in one conversation: %+v", got.output)
	}
	// 20 for the admin across conversations
	for i := 0; i < 6; i++ {
		other, err := f.service.createConversation(ctx, f.admin.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 3 && i*3+j < 17; j++ {
			if got := setFollowUp(t, f, other, followUpArgs(600+j*60, "check "+other)); got.output.err {
				t.Fatalf("follow-up %d: %s", i*3+j, got.output.text)
			}
		}
	}
	last, _ := f.service.createConversation(ctx, f.admin.ID, time.Now())
	if got := setFollowUp(t, f, last, followUpArgs(600, "one too many")); !got.output.err || !strings.Contains(got.output.text, "20 follow-ups") {
		t.Fatalf("the 21st: %+v", got.output)
	}
	// a plan of another conversation is not this one's
	if got := setFollowUp(t, f, last, []byte(`{"in_seconds":600,"note":"x","plan_id":"pln_elsewhere"}`)); !got.output.err {
		t.Fatalf("a foreign plan: %s", got.output.text)
	}
	cancel := f.service.executeLocalTool(ctx, f.admin, last, followUpCancelTool, []byte(`{"id":"`+stored.ID+`"}`))
	if !cancel.output.err {
		t.Fatal("a follow-up of another conversation was cancelled")
	}
	cancel = f.service.executeLocalTool(ctx, f.admin, conv, followUpCancelTool, []byte(`{"id":"`+stored.ID+`"}`))
	if cancel.output.err || cancel.cancelled != stored.ID {
		t.Fatalf("cancel: %+v", cancel)
	}
	if list, _ := f.st.AssistantFollowUps(ctx, f.admin.ID, conv); len(list) != 2 {
		t.Fatalf("after cancel: %d", len(list))
	}
}

// A chat turn sets a follow-up (the stream says so); at its time the follow-up continues that conversation as its admin, with
// the conversation so far, the admin's own profile and the chat's tools; its two messages are marked, the conversation is
// unread until opened, and the chat button counts it.
func TestFollowUpContinuesTheConversationAsItsAdmin(t *testing.T) {
	note := "capture req_1 on de1 ends 12:41Z; read node_debug_logs and give the verdict"
	f := newAssistantFixture(t,
		modelResponse{Tools: []modelToolCall{scriptedTool(followUpTool, "call_fu", string(followUpArgs(600, note)))}},
		modelResponse{Text: "I will come back at 12:41 with the verdict."},
		modelResponse{Text: `{"title":"Debug de1","summary":"A capture on de1."}`},
		// the follow-up
		modelResponse{Text: "Reading the capture.", Tools: []modelToolCall{scriptedTool("read_node", "call_read", `{"node":"de1"}`)}},
		modelResponse{Text: "The capture shows TLS alerts from one network."},
	)
	ctx := context.Background()
	stream, err := f.client.Chat(ctx, connect.NewRequest(&adminv1.ChatRequest{Text: "Capture de1's debug log for 10 minutes and tell me what you see."}))
	if err != nil {
		t.Fatal(err)
	}
	var conv string
	var announced *adminv1.FollowUp
	for _, ev := range drainChat(t, stream) {
		if s := ev.GetStarted(); s != nil {
			conv = s.ConversationId
		}
		if fu := ev.GetFollowUp(); fu != nil {
			announced = fu
		}
	}
	if announced == nil || announced.Note != note || announced.DueUnix == 0 {
		t.Fatalf("the stream did not announce the follow-up: %+v", announced)
	}
	got, err := f.client.GetConversation(ctx, connect.NewRequest(&adminv1.GetConversationRequest{Id: conv}))
	if err != nil || len(got.Msg.FollowUps) != 1 || got.Msg.FollowUps[0].Id != announced.Id {
		t.Fatalf("the conversation does not list it: %+v %v", got, err)
	}
	fu := onlyFollowUp(t, f, conv)
	before := len(f.adapter.requests())
	f.runtime.mu.Lock()
	f.runtime.identities = nil
	f.runtime.mu.Unlock()

	out := f.service.RunFollowUp(ctx, fu)
	if out.Status != "ran" {
		t.Fatalf("outcome: %+v", out)
	}
	reqs := f.adapter.requests()[before:]
	if len(reqs) != 2 {
		t.Fatalf("model calls of the follow-up: %d", len(reqs))
	}
	first := reqs[0]
	if !strings.Contains(first.System, "follow-up you set earlier") {
		t.Fatalf("the system prompt lacks the follow-up note: %q", first.System)
	}
	history := make([]string, 0, len(first.History))
	for _, raw := range first.History {
		history = append(history, string(raw))
	}
	joined := strings.Join(history, "\n")
	lastLine := history[len(history)-1]
	if !strings.Contains(joined, "Capture de1's debug log") || !strings.Contains(joined, "I will come back at 12:41") {
		t.Fatalf("the model did not see the conversation so far: %s", joined)
	}
	if !strings.Contains(lastLine, "Follow-up you set at") || !strings.Contains(lastLine, "read node_debug_logs") {
		t.Fatalf("the turn does not start from the follow-up line: %s", lastLine)
	}
	for _, tool := range first.Tools {
		if strings.HasSuffix(tool.Name, "_apply") || tool.Name == followUpVerdictTool {
			t.Fatalf("a follow-up's turn was offered %s", tool.Name)
		}
	}
	f.runtime.mu.Lock()
	identities := append([]mcp.AssistantIdentity(nil), f.runtime.identities...)
	f.runtime.mu.Unlock()
	if len(identities) == 0 || identities[0].Scheduled || identities[0].AdminID != f.admin.ID || identities[0].Profile != store.ProfileAdmin {
		t.Fatalf("the follow-up did not act as the conversation's admin: %+v", identities)
	}

	access, err := f.client.GetAssistantAccess(ctx, connect.NewRequest(&adminv1.GetAssistantAccessRequest{}))
	if err != nil || access.Msg.Unread != 1 {
		t.Fatalf("the chat button does not count the answer: %+v %v", access, err)
	}
	list, err := f.client.ListConversations(ctx, connect.NewRequest(&adminv1.ListConversationsRequest{}))
	if err != nil || len(list.Msg.Conversations) != 1 || !list.Msg.Conversations[0].Unread {
		t.Fatalf("the list does not show it unread: %+v %v", list, err)
	}
	got, err = f.client.GetConversation(ctx, connect.NewRequest(&adminv1.GetConversationRequest{Id: conv}))
	if err != nil {
		t.Fatal(err)
	}
	msgs := got.Msg.Messages
	line, answer := msgs[len(msgs)-2], msgs[len(msgs)-1]
	if line.Role != adminv1.ChatRole_CHAT_ROLE_USER || line.Text != note || line.FollowUp.GetId() != fu.ID || line.FollowUp.GetSetUnix() != fu.CreatedAt.Unix() {
		t.Fatalf("the line: %+v", line)
	}
	if answer.Role != adminv1.ChatRole_CHAT_ROLE_ASSISTANT || answer.FollowUp.GetId() != fu.ID || answer.FollowUp.GetOutcome() != "" || !strings.Contains(answer.Text, "TLS alerts") {
		t.Fatalf("the answer: %+v", answer)
	}
	if access, _ = f.client.GetAssistantAccess(ctx, connect.NewRequest(&adminv1.GetAssistantAccessRequest{})); access.Msg.Unread != 0 {
		t.Fatal("opening the conversation did not mark it read")
	}
}

// A follow-up's turn has the chat's approval rules: an apply tool is not there to call, and a plan it makes waits for the
// human's click on its card.
func TestFollowUpTurnCannotApply(t *testing.T) {
	f := newAssistantFixture(t,
		modelResponse{Tools: []modelToolCall{
			scriptedTool("remove_user_apply", "call_apply", `{"confirm_token":"made-up"}`),
			scriptedTool("remove_user_plan", "call_plan", `{"user_id":"user-1"}`),
		}},
		modelResponse{Text: "I prepared the removal; apply it on its card."},
	)
	ctx := context.Background()
	conv, _ := f.service.createConversation(ctx, f.admin.ID, time.Now())
	setFollowUp(t, f, conv, followUpArgs(60, "remove user-1 if they still share the link"))
	out := f.service.RunFollowUp(ctx, onlyFollowUp(t, f, conv))
	if out.Status != "ran" {
		t.Fatalf("outcome: %+v", out)
	}
	f.runtime.mu.Lock()
	calls := strings.Join(f.runtime.calls, ",")
	f.runtime.mu.Unlock()
	if calls != "remove_user_plan" {
		t.Fatalf("panel tools called: %s", calls)
	}
	got, err := f.client.GetConversation(ctx, connect.NewRequest(&adminv1.GetConversationRequest{Id: conv}))
	if err != nil {
		t.Fatal(err)
	}
	answer := got.Msg.Messages[len(got.Msg.Messages)-1]
	if len(answer.Plans) != 1 || answer.Plans[0].State != adminv1.PlanState_PLAN_STATE_PROPOSED {
		t.Fatalf("the plan card: %+v", answer.Plans)
	}
	for _, tc := range answer.Tools {
		if tc.Tool == "remove_user_apply" && tc.Ok {
			t.Fatal("the apply ran")
		}
	}
}

func followUpNoteOf(t *testing.T, f *assistantFixture, conv string) *adminv1.ChatMessage {
	t.Helper()
	got, err := f.client.GetConversation(context.Background(), connect.NewRequest(&adminv1.GetConversationRequest{Id: conv}))
	if err != nil || len(got.Msg.Messages) == 0 {
		t.Fatalf("conversation: %+v %v", got, err)
	}
	return got.Msg.Messages[len(got.Msg.Messages)-1]
}

// What keeps a follow-up from running leaves a note with the reason and spends nothing: more than a day late (the panel was
// down), the monthly budget spent, the assistant off, a run the panel's stops interrupted too often.
func TestFollowUpNotesWhyItDidNotRun(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	newOne := func(note string) (string, store.AssistantFollowUp) {
		conv, _ := f.service.createConversation(ctx, f.admin.ID, time.Now())
		setFollowUp(t, f, conv, followUpArgs(60, note))
		return conv, onlyFollowUp(t, f, conv)
	}

	conv, late := newOne("read the speed test")
	late.DueAt = time.Now().Add(-25 * time.Hour)
	if out := f.service.RunFollowUp(ctx, late); out.Status != "missed" {
		t.Fatalf("too late: %+v", out)
	}
	if m := followUpNoteOf(t, f, conv); m.FollowUp.GetOutcome() != "missed" || m.FollowUp.GetNote() != "read the speed test" || m.Text != "" {
		t.Fatalf("the missed note: %+v", m)
	}
	// a day late at most still runs
	conv, onTime := newOne("still worth it")
	onTime.DueAt = time.Now().Add(-23 * time.Hour)
	if out := f.service.RunFollowUp(ctx, onTime); out.Status != "ran" {
		t.Fatalf("23 h late: %+v", out)
	}

	conv, tried := newOne("interrupted thrice")
	tried.Attempts = followUpTries
	if out := f.service.RunFollowUp(ctx, tried); out.Status != "failed" || out.Reason != "interrupted" {
		t.Fatalf("interrupted: %+v", out)
	}

	before := len(f.adapter.requests())
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_provider SET budget_usd=0 WHERE provider=?`, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK)); err != nil {
		t.Fatal(err)
	}
	conv, broke := newOne("check the rollout")
	if out := f.service.RunFollowUp(ctx, broke); out.Status != "failed" || out.Reason != "budget_reached" {
		t.Fatalf("budget: %+v", out)
	}
	if m := followUpNoteOf(t, f, conv); m.FollowUp.GetOutcome() != "failed" || m.FollowUp.GetReason() != "budget_reached" {
		t.Fatalf("the budget note: %+v", m)
	}
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_provider SET budget_usd=20 WHERE provider=?`, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_settings SET enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	conv, off := newOne("check de1")
	if out := f.service.RunFollowUp(ctx, off); out.Status != "failed" || out.Reason != "assistant_off" {
		t.Fatalf("assistant off: %+v", out)
	}
	if len(f.adapter.requests()) != before {
		t.Fatal("a follow-up that could not run reached the provider")
	}
	var unread int
	_ = f.st.R.QueryRowContext(ctx, `SELECT count(*) FROM assistant_conversation WHERE unread=1`).Scan(&unread)
	// five conversations got a note or an answer; the two this test opened are read
	if unread != 3 {
		t.Fatalf("unread conversations: %d, want 3", unread)
	}
}

// A provider that fails is tried again with a wait; one that keeps failing hands the follow-up to the fallback provider; without
// one the turn fails, and its answer says why.
func TestFollowUpRetriesFallsBackOrFails(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	f.service.retryWaits = []time.Duration{0}
	conv, _ := f.service.createConversation(ctx, f.admin.ID, time.Now())
	setFollowUp(t, f, conv, followUpArgs(60, "check de1 again"))
	fu := onlyFollowUp(t, f, conv)

	flaky := &flakyAdapter{scriptedAdapter: &scriptedAdapter{queue: []modelResponse{{Text: "de1 is fine now."}}}, fails: 2, code: "provider_overloaded"}
	f.service.providers[adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK] = flaky
	if out := f.service.RunFollowUp(ctx, fu); out.Status != "ran" || flaky.calls != 3 {
		t.Fatalf("retry: %+v after %d calls", out, flaky.calls)
	}

	down := &flakyAdapter{scriptedAdapter: &scriptedAdapter{}, fails: 100, code: "provider_unreachable"}
	f.service.providers[adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK] = down
	if out := f.service.RunFollowUp(ctx, fu); out.Status != "failed" || out.Reason != "provider_unreachable" {
		t.Fatalf("without a fallback: %+v", out)
	}
	if m := followUpNoteOf(t, f, conv); m.FollowUp.GetOutcome() != "failed" || m.FollowUp.GetReason() != "provider_unreachable" || m.End != "error" {
		t.Fatalf("the failed answer: %+v", m)
	}

	backup := &scriptedAdapter{queue: []modelResponse{{Text: "Checked on the fallback: de1 answers."}}}
	f.service.providers[adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD] = backup
	sealed := f.vault.Seal([]byte("fallback-key-never-return"), ProviderKeyAAD(adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD))
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_provider SET key_enc=?,budget_usd=5 WHERE provider=?`, sealed, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_settings SET fallback_provider=? WHERE id=1`, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD)); err != nil {
		t.Fatal(err)
	}
	if out := f.service.RunFollowUp(ctx, fu); out.Status != "ran" || len(backup.requests()) != 1 {
		t.Fatalf("fallback: %+v, %d calls", out, len(backup.requests()))
	}
	if m := followUpNoteOf(t, f, conv); !strings.Contains(m.Text, "Checked on the fallback") || m.FollowUp.GetOutcome() != "" {
		t.Fatalf("the fallback's answer: %+v", m)
	}
}

// A conversation in a turn makes the follow-up wait (it runs after); one interrupted by a stop of the panel runs again
// without a second line.
func TestFollowUpWaitsForABusyConversationAndResumes(t *testing.T) {
	f := newAssistantFixture(t, modelResponse{Text: "Done."})
	ctx := context.Background()
	conv, _ := f.service.createConversation(ctx, f.admin.ID, time.Now())
	setFollowUp(t, f, conv, followUpArgs(60, "read the result"))
	fu := onlyFollowUp(t, f, conv)
	now := time.Now()
	if ok, err := f.st.AcquireAssistantTurn(ctx, conv, f.admin.ID, "assturn_person", now, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("lock: %v %v", ok, err)
	}
	if out := f.service.RunFollowUp(ctx, fu); out.Status != "deferred" || out.Reason != "conversation_busy" || out.Counted || out.RetryAfter <= 0 {
		t.Fatalf("busy: %+v", out)
	}
	if n, _ := f.service.messageCount(ctx, f.admin.ID, conv); n != 0 {
		t.Fatalf("a busy run wrote %d messages", n)
	}
	_ = f.st.ReleaseAssistantTurn(ctx, conv, "assturn_person")

	// the panel stopped after the line was written
	line, _ := json.Marshal(map[string]any{"role": "user", "content": "Follow-up you set at ...: read the result"})
	if _, err := f.service.saveMessage(ctx, conv, f.admin.ID, "user", 0, messageBody{Text: "read the result", FollowUp: &adminv1.FollowUpTurn{Id: fu.ID}, Transcript: []json.RawMessage{line}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	fu.Attempts = 1
	if out := f.service.RunFollowUp(ctx, fu); out.Status != "ran" {
		t.Fatalf("resume: %+v", out)
	}
	got := followUpNoteOf(t, f, conv)
	conversation, _ := f.client.GetConversation(ctx, connect.NewRequest(&adminv1.GetConversationRequest{Id: conv}))
	if len(conversation.Msg.Messages) != 2 || got.Text != "Done." {
		t.Fatalf("after the resume: %d messages", len(conversation.Msg.Messages))
	}
	reqs := f.adapter.requests()
	hist := reqs[len(reqs)-1].History
	lines := 0
	for _, raw := range hist {
		if strings.Contains(string(raw), "Follow-up you set at") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("the model read the line %d times", lines)
	}
}

// With notify_telegram the follow-up's turn gives a verdict, and a problem goes to the admin's Telegram; without it the verdict
// tool is not there and nothing is sent.
func TestFollowUpTelegramOnProblem(t *testing.T) {
	f := newAssistantFixture(t,
		modelResponse{Tools: []modelToolCall{scriptedTool(followUpVerdictTool, "call_v", `{"problem":true,"summary":"de1 drops TLS from one network"}`)}},
		modelResponse{Text: "de1 drops TLS handshakes from one network."},
		modelResponse{Text: "All quiet."},
	)
	ctx := context.Background()
	var mu sync.Mutex
	var sent []string
	f.service.SetFollowUpNotifier(func(adminID, summary string) {
		mu.Lock()
		sent = append(sent, adminID+": "+summary)
		mu.Unlock()
	})
	conv, _ := f.service.createConversation(ctx, f.admin.ID, time.Now())
	setFollowUp(t, f, conv, []byte(`{"in_seconds":60,"note":"read the capture","notify_telegram":true}`))
	if out := f.service.RunFollowUp(ctx, onlyFollowUp(t, f, conv)); out.Status != "ran" {
		t.Fatalf("%+v", out)
	}
	mu.Lock()
	got := strings.Join(sent, "|")
	mu.Unlock()
	if got != f.admin.ID+": de1 drops TLS from one network" {
		t.Fatalf("telegram: %q", got)
	}
	quiet, _ := f.service.createConversation(ctx, f.admin.ID, time.Now())
	setFollowUp(t, f, quiet, followUpArgs(60, "read the capture"))
	before := len(f.adapter.requests())
	f.service.RunFollowUp(ctx, onlyFollowUp(t, f, quiet))
	for _, tool := range f.adapter.requests()[before].Tools {
		if tool.Name == followUpVerdictTool {
			t.Fatal("the verdict was offered without Telegram")
		}
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d messages", len(sent))
	}
}

// A follow-up tied to a plan waits for the person's apply: its time counts from the apply; a plan that ends otherwise takes it.
// The plan's result tells the model to set one.
func TestFollowUpStartsWhenItsPlanIsApplied(t *testing.T) {
	f := newAssistantFixture(t,
		modelResponse{Tools: []modelToolCall{scriptedTool("remove_user_plan", "call_plan", `{"user_id":"user-1"}`)}},
		modelResponse{Text: "Apply it and I will check."},
	)
	ctx := context.Background()
	stream, err := f.client.Chat(ctx, connect.NewRequest(&adminv1.ChatRequest{Text: "prepare it"}))
	if err != nil {
		t.Fatal(err)
	}
	conv := drainChat(t, stream)[0].GetStarted().ConversationId
	insertMCPPlanForAssistant(t, f, "remove_user", store.PlanPlanned, false)
	set := setFollowUp(t, f, conv, []byte(`{"in_seconds":300,"note":"check user-1 is gone","plan_id":"plan_assistant_test"}`))
	if set.output.err || set.followUp.DueUnix != 0 || set.followUp.PlanId != "plan_assistant_test" || set.followUp.DelayS != 300 {
		t.Fatalf("set: %+v", set)
	}
	if n, err := f.st.ArmAssistantFollowUps(ctx); err != nil || n != 0 || !onlyFollowUp(t, f, conv).DueAt.IsZero() {
		t.Fatalf("armed before the apply: %d %v", n, err)
	}
	applied := time.Now().Add(-time.Minute).Truncate(time.Second)
	if _, err = f.st.W.ExecContext(ctx, `UPDATE mcp_plan SET status='applied',applied_at=? WHERE id='plan_assistant_test'`, applied.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.ArmAssistantFollowUps(ctx); err != nil {
		t.Fatal(err)
	}
	if due := onlyFollowUp(t, f, conv).DueAt; !due.Equal(applied.Add(300 * time.Second)) {
		t.Fatalf("due %s, want 5 min after the apply at %s", due, applied)
	}
	// a plan that was not applied takes its follow-up with it
	if _, err = f.st.W.ExecContext(ctx, `UPDATE mcp_plan SET status='planned' WHERE id='plan_assistant_test'`); err != nil {
		t.Fatal(err)
	}
	setFollowUp(t, f, conv, []byte(`{"in_seconds":120,"note":"another check","plan_id":"plan_assistant_test"}`))
	if _, err = f.st.W.ExecContext(ctx, `UPDATE mcp_plan SET status='cancelled' WHERE id='plan_assistant_test'`); err != nil {
		t.Fatal(err)
	}
	if n, err := f.st.ArmAssistantFollowUps(ctx); err != nil || n != 1 {
		t.Fatalf("dropped %d: %v", n, err)
	}
	onlyFollowUp(t, f, conv)

	hint := withFollowUpHint(`{"plan_id":"pln_cap"}`, &adminv1.PlanCard{PlanId: "pln_cap", Tool: "node_debug_capture", Facts: []*adminv1.ApprovalFact{{Key: "duration", Value: "600 s"}}})
	var parsed map[string]string
	if err = json.Unmarshal([]byte(hint), &parsed); err != nil || !strings.Contains(parsed["follow_up_hint"], "plan_id pln_cap") || !strings.Contains(parsed["follow_up_hint"], "about 660") {
		t.Fatalf("hint: %s %v", hint, err)
	}
	if plain := withFollowUpHint(`{"plan_id":"p"}`, &adminv1.PlanCard{Tool: "user_disable"}); plain != `{"plan_id":"p"}` {
		t.Fatalf("a plan that ends at once got a hint: %s", plain)
	}
}

// The person cancels a waiting follow-up from its line; nobody else can, and a running one stays.
func TestCancelFollowUp(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	conv, _ := f.service.createConversation(ctx, f.admin.ID, time.Now())
	setFollowUp(t, f, conv, followUpArgs(600, "read the result"))
	fu := onlyFollowUp(t, f, conv)
	other := store.Admin{ID: "adm_other_fu", DisplayName: "Helper", Role: store.RoleHelper}
	if _, err := f.st.W.ExecContext(ctx, `INSERT INTO admin(id,display_name,role,user_handle,created_at) VALUES(?,?,?,?,?)`, other.ID, other.DisplayName, other.Role, []byte("other-fu"), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&adminv1.CancelFollowUpRequest{ConversationId: conv, Id: fu.ID})
	if _, err := f.service.CancelFollowUp(auth.WithAdmin(ctx, other), req); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("another admin: %v", err)
	}
	if ok, _ := f.st.ClaimAssistantFollowUp(ctx, fu.ID, "lease_x", fu.DueAt, fu.DueAt.Add(time.Hour)); !ok {
		t.Fatal("claim")
	}
	if _, err := f.client.CancelFollowUp(ctx, req); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a running one: %v", err)
	}
	if err := f.st.DeferAssistantFollowUp(ctx, fu.ID, "lease_x", time.Now().Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.client.CancelFollowUp(ctx, req); err != nil {
		t.Fatal(err)
	}
	if list, _ := f.st.AssistantFollowUps(ctx, f.admin.ID, conv); len(list) != 0 {
		t.Fatal("still waiting")
	}
}

// A model that keeps coming back by itself is stopped: after followUpChain follow-up turns in a row with no message of the
// person between, no new follow-up is set; the person's next message starts the count again.
func TestFollowUpChainIsBounded(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	conv, _ := f.service.createConversation(ctx, f.admin.ID, time.Now())
	line := func(followUp bool) {
		body := messageBody{Text: "x"}
		if followUp {
			body.FollowUp = &adminv1.FollowUpTurn{Id: "afu_chain"}
		}
		if _, err := f.service.saveMessage(ctx, conv, f.admin.ID, "user", 0, body, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	line(false)
	for i := 0; i < followUpChain; i++ {
		line(true)
	}
	if got := setFollowUp(t, f, conv, followUpArgs(60, "again")); !got.output.err || !strings.Contains(got.output.text, "in a row") {
		t.Fatalf("a sixth time: %+v", got.output)
	}
	line(false)
	if got := setFollowUp(t, f, conv, followUpArgs(60, "again")); got.output.err {
		t.Fatalf("after the person wrote: %s", got.output.text)
	}
}
