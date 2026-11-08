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
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The fallback provider of the scheduled tasks is another connected provider, or none.
func TestFallbackProviderSetting(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	update := func(fb adminv1.AssistantProvider) error {
		_, err := f.client.UpdateAssistantSettings(ctx, connect.NewRequest(&adminv1.UpdateAssistantSettingsRequest{Settings: &adminv1.AssistantSettings{
			Enabled: true, Provider: adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK, Effort: adminv1.AssistantEffort_ASSISTANT_EFFORT_NORMAL,
			Audience: adminv1.AssistantAudience_ASSISTANT_AUDIENCE_OWNER, RetentionDays: 30, FallbackProvider: fb}}))
		return err
	}
	if err := update(adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("the main provider as its own fallback: %v", err)
	}
	if err := update(adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a fallback without a key: %v", err)
	}
	sealed := f.vault.Seal([]byte("zai-key-never-return"), ProviderKeyAAD(adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI))
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_provider SET key_enc=? WHERE provider=?`, sealed, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI)); err != nil {
		t.Fatal(err)
	}
	if err := update(adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI); err != nil {
		t.Fatal(err)
	}
	got, err := f.client.GetAssistantSettings(ctx, connect.NewRequest(&adminv1.GetAssistantSettingsRequest{}))
	if err != nil || got.Msg.Settings.FallbackProvider != adminv1.AssistantProvider_ASSISTANT_PROVIDER_ZAI {
		t.Fatalf("settings: %+v %v", got, err)
	}
}

// A plan a scheduled run prepared is applied, at the owner's click, with the identity that made it: the operator profile.
func TestScheduledPlanAppliesWithTheScheduledIdentity(t *testing.T) {
	f := newAssistantFixture(t)
	session, err := f.service.assistantSession(context.Background(), f.admin, store.ScheduledTokenID(f.admin.ID))
	if err != nil {
		t.Fatal(err)
	}
	_ = session.Close()
	f.runtime.mu.Lock()
	id := f.runtime.identities[len(f.runtime.identities)-1]
	f.runtime.mu.Unlock()
	if !id.Scheduled || id.Profile != store.ProfileOperator || id.TokenID != store.ScheduledTokenID(f.admin.ID) {
		t.Fatalf("identity: %+v", id)
	}
}

// flakyAdapter fails its first `fails` calls with code, then answers like the scripted one.
type flakyAdapter struct {
	*scriptedAdapter
	mu    sync.Mutex
	fails int
	code  string
	calls int
}

func (a *flakyAdapter) Stream(ctx context.Context, key string, req modelRequest, onText func(string) error) (modelResponse, error) {
	a.mu.Lock()
	a.calls++
	fail := a.calls <= a.fails
	a.mu.Unlock()
	if fail {
		return modelResponse{InputTokens: 3, UsageReported: true}, apiError(a.code)
	}
	return a.scriptedAdapter.Stream(ctx, key, req, onText)
}

func verdictCall(problem bool, summary string) modelToolCall {
	b, _ := json.Marshal(map[string]any{"problem": problem, "summary": summary})
	return scriptedTool(verdictTool, "call_verdict", string(b))
}

// A scheduled run reads with the operator identity of the owner's scheduled tasks, writes its answer into the task's own
// conversation (a new part per run, so a run's model sees that run only), and reports the verdict.
func TestScheduledRunAnswersInTheTasksConversation(t *testing.T) {
	f := newAssistantFixture(t,
		modelResponse{Text: "Looking at de1.", Tools: []modelToolCall{scriptedTool("read_node", "call_read", `{"node":"de1"}`)}},
		modelResponse{Text: "de1 does not answer its checks.", Tools: []modelToolCall{verdictCall(true, "de1 fails its checks since 06:10")}},
		// the second run
		modelResponse{Text: "All quiet.", Tools: []modelToolCall{verdictCall(false, "nothing to report")}},
	)
	f.service.retryWaits = []time.Duration{0}
	ctx := context.Background()
	run := ScheduledRun{TaskID: "tsk_morning", TaskName: "Morning check", AdminID: f.admin.ID, Prompt: "Check the fleet and tell me what is wrong."}
	out := f.service.RunScheduled(ctx, run)
	if out.Status != "problem" || out.Summary != "de1 fails its checks since 06:10" || out.ConversationID == "" || out.Provider != "deepseek" || out.USD <= 0 || out.FellBack {
		t.Fatalf("first run: %+v", out)
	}
	f.runtime.mu.Lock()
	identity := f.runtime.identities[0]
	calls := append([]string(nil), f.runtime.calls...)
	f.runtime.mu.Unlock()
	if !identity.Scheduled || identity.Profile != store.ProfileOperator || identity.TokenID != store.ScheduledTokenID(f.admin.ID) || identity.AdminID != f.admin.ID {
		t.Fatalf("the run did not act with the scheduled operator identity: %+v", identity)
	}
	if strings.Join(calls, ",") != "read_node" {
		t.Fatalf("tool calls: %v", calls)
	}
	reqs := f.adapter.requests()
	for _, tool := range reqs[0].Tools {
		if strings.HasSuffix(tool.Name, "_apply") || tool.Name == "memory_save" || tool.Name == "ui_open" || tool.Name == "ui_secret_form" ||
			tool.Name == "conversation_search" || tool.Name == "conversation_read" {
			t.Errorf("a scheduled run was offered %s", tool.Name)
		}
	}
	if !strings.Contains(reqs[0].System, "scheduled run") || !strings.Contains(reqs[0].System, "Morning check") {
		t.Fatalf("system prompt lacks the scheduled note: %q", reqs[0].System)
	}
	summary, _, err := f.service.conversation(ctx, f.admin.ID, out.ConversationID)
	if err != nil || summary.TaskId != "tsk_morning" || summary.Title != "Morning check" || summary.Messages != 2 {
		t.Fatalf("conversation: %+v %v", summary, err)
	}

	run.ConversationID = out.ConversationID
	second := f.service.RunScheduled(ctx, run)
	if second.Status != "ok" || second.ConversationID != out.ConversationID || second.Summary != "nothing to report" {
		t.Fatalf("second run: %+v", second)
	}
	reqs = f.adapter.requests()
	last := reqs[len(reqs)-1]
	if len(last.History) != 1 || strings.Contains(string(last.History[0]), "de1") {
		t.Fatalf("the second run's model saw the first run: %d messages", len(last.History))
	}
	if summary, _, _ = f.service.conversation(ctx, f.admin.ID, out.ConversationID); summary.Messages != 4 {
		t.Fatalf("the conversation holds %d messages, want both runs", summary.Messages)
	}
}

func TestScheduledRunDoesNotSendAdminWideContext(t *testing.T) {
	f := newAssistantFixture(t, modelResponse{Tools: []modelToolCall{verdictCall(false, "all clear")}})
	ctx := context.Background()
	adminID := f.admin.ID
	privatePanelNote := "admin panel context marker"
	sealedNotes := f.vault.Seal([]byte(privatePanelNote), PanelNotesAAD())
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_settings SET panel_notes_enc=? WHERE id=1`, sealedNotes); err != nil {
		t.Fatal(err)
	}
	privateMemory := "administrator memory marker"
	memoryID := "mem_scheduled_context"
	if err := f.st.InsertAssistantMemory(ctx, memoryID, adminID, f.vault.Seal([]byte(privateMemory), MemoryAAD(memoryID)), false, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	otherConversation, err := f.service.createConversation(ctx, adminID, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	privateTitle := "other conversation title marker"
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_conversation SET title_enc=? WHERE id=?`, f.vault.Seal([]byte(privateTitle), ConversationTitleAAD(otherConversation)), otherConversation); err != nil {
		t.Fatal(err)
	}

	out := f.service.RunScheduled(ctx, ScheduledRun{TaskID: "tsk_context", TaskName: "Context check", AdminID: adminID, Prompt: "Check the fleet."})
	if out.Status != "ok" {
		t.Fatalf("scheduled run: %+v", out)
	}
	requests := f.adapter.requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want 1", len(requests))
	}
	request := requests[0]
	for _, marker := range []string{privatePanelNote, privateMemory, privateTitle} {
		if strings.Contains(request.System, marker) {
			t.Errorf("scheduled system prompt included admin-wide context %q", marker)
		}
	}
	for _, tool := range request.Tools {
		if tool.Name == "conversation_search" || tool.Name == "conversation_read" {
			t.Errorf("scheduled run was offered %s", tool.Name)
		}
	}
}

// Before anything is spent: the assistant off, no provider key, a spent budget are skips with their reason.
func TestScheduledRunSkips(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	run := ScheduledRun{TaskID: "tsk_skip", TaskName: "Skip", AdminID: f.admin.ID, Prompt: "Check"}
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_provider SET budget_usd=0 WHERE provider=?`, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK)); err != nil {
		t.Fatal(err)
	}
	if out := f.service.RunScheduled(ctx, run); out.Status != "skipped" || out.Reason != "budget_reached" {
		t.Fatalf("spent budget: %+v", out)
	}
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_settings SET enabled=0 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if out := f.service.RunScheduled(ctx, run); out.Status != "skipped" || out.Reason != "assistant_off" {
		t.Fatalf("assistant off: %+v", out)
	}
	if len(f.adapter.requests()) != 0 {
		t.Fatal("a skipped run reached the provider")
	}
}

// A provider that fails is tried again with a wait; one that keeps failing hands the run to the fallback provider.
func TestScheduledRunRetriesThenFallsBack(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	f.service.retryWaits = []time.Duration{0}
	answer := modelResponse{Text: "Fine.", Tools: []modelToolCall{verdictCall(false, "all good")}}

	// two overloaded answers, then one that works: three attempts of the same call
	flaky := &flakyAdapter{scriptedAdapter: &scriptedAdapter{queue: []modelResponse{answer}}, fails: 2, code: "provider_overloaded"}
	f.service.providers[adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK] = flaky
	out := f.service.RunScheduled(ctx, ScheduledRun{TaskID: "tsk_r", TaskName: "Retry", AdminID: f.admin.ID, Prompt: "Check"})
	if out.Status != "ok" || out.FellBack || flaky.calls != 3 {
		t.Fatalf("retry: %+v after %d calls", out, flaky.calls)
	}

	// the main provider never answers; the owner chose Ollama Cloud as the fallback
	down := &flakyAdapter{scriptedAdapter: &scriptedAdapter{}, fails: 100, code: "provider_unreachable"}
	backup := &scriptedAdapter{queue: []modelResponse{{Text: "Checked.", Tools: []modelToolCall{verdictCall(true, "nl1 is down")}}}}
	f.service.providers[adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK] = down
	f.service.providers[adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD] = backup
	sealed := f.vault.Seal([]byte("fallback-key-never-return"), ProviderKeyAAD(adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD))
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_provider SET key_enc=?,budget_usd=5 WHERE provider=?`, sealed, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD)); err != nil {
		t.Fatal(err)
	}
	if out = f.service.RunScheduled(ctx, ScheduledRun{TaskID: "tsk_f", TaskName: "Fallback", AdminID: f.admin.ID, Prompt: "Check"}); out.Status != "failed" || out.Reason != "provider_unreachable" {
		t.Fatalf("without a fallback: %+v", out)
	}
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_settings SET fallback_provider=? WHERE id=1`, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_OLLAMA_CLOUD)); err != nil {
		t.Fatal(err)
	}
	out = f.service.RunScheduled(ctx, ScheduledRun{TaskID: "tsk_f", TaskName: "Fallback", AdminID: f.admin.ID, Prompt: "Check"})
	if out.Status != "problem" || !out.FellBack || out.Provider != "ollama_cloud" || out.Summary != "nl1 is down" {
		t.Fatalf("fallback: %+v", out)
	}
	if down.calls != 2*scheduledAttempts {
		t.Fatalf("the main provider was tried %d times, want %d", down.calls, 2*scheduledAttempts)
	}
	var code string
	_ = f.st.R.QueryRowContext(ctx, `SELECT last_error_code FROM assistant_provider WHERE provider=?`, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK)).Scan(&code)
	if code != "provider_unreachable" {
		t.Fatalf("the failing provider's card does not say so: %q", code)
	}
}

// A run whose model never gives a verdict, even when asked once more, failed: nobody can tell whether it found something.
func TestScheduledRunWithoutVerdictFails(t *testing.T) {
	f := newAssistantFixture(t, modelResponse{Text: "Everything looks fine."}, modelResponse{Text: "Still fine."})
	out := f.service.RunScheduled(context.Background(), ScheduledRun{TaskID: "tsk_v", TaskName: "Verdict", AdminID: f.admin.ID, Prompt: "Check"})
	if out.Status != "failed" || out.Reason != "no_verdict" {
		t.Fatalf("%+v", out)
	}
	reqs := f.adapter.requests()
	if len(reqs) != 2 || len(reqs[1].Tools) != 1 || reqs[1].Tools[0].Name != verdictTool {
		t.Fatalf("the second round did not ask for the verdict alone: %d requests", len(reqs))
	}
}

// A secret pasted into a task's prompt never reaches the provider.
func TestScheduledRunScrubsThePrompt(t *testing.T) {
	f := newAssistantFixture(t, modelResponse{Tools: []modelToolCall{verdictCall(false, "ok")}})
	secret := "sk-ant-api03-" + strings.Repeat("A1b2", 10)
	out := f.service.RunScheduled(context.Background(), ScheduledRun{TaskID: "tsk_s", TaskName: "Secret", AdminID: f.admin.ID, Prompt: "Use key " + secret + " to check"})
	if out.Status != "ok" {
		t.Fatalf("%+v", out)
	}
	for _, req := range f.adapter.requests() {
		for _, raw := range req.History {
			if strings.Contains(string(raw), secret) {
				t.Fatal("the secret reached the provider")
			}
		}
	}
}
