//go:build !js

package assistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// A scheduled assistant task (internal/panel/tasks, mistgate.tasks.v1): the owner's prompt, run on a schedule by the assistant
// with its tools, without a person in the loop. It acts with the hidden identity of the admin's scheduled tasks
// (store.ScheduledTokenID), the operator profile whatever the admin's role: it reads, runs probes and may prepare plans, never
// applies one (the _apply tools are not given to it, as in a chat) and never reaches an admin tool. Its answer goes into the
// task's own conversation, a new part per run (the model of a run sees that run only; the owner sees them all and may go on
// from the last one in the chat). It ends with a verdict, the local tool task_verdict, which says whether it found a problem.
//
// A provider error is retried with a growing wait; when the provider still fails, the run starts again on the fallback
// provider of the settings, if the owner chose one. The monthly budget of each provider holds as for a chat: a run that would
// pass it does not start (skipped, budget_reached).

// ScheduledRun is what a scheduled task asks of the assistant.
type ScheduledRun struct {
	TaskID, TaskName string
	// AdminID is the task's owner: the conversation is theirs, and so is the cost.
	AdminID string
	Prompt  string
	// ConversationID is the task's conversation; empty, or deleted meanwhile, starts a new one.
	ConversationID string
}

// ScheduledOutcome is what came of a run.
type ScheduledOutcome struct {
	Status         string // "ok", "problem", "failed" or "skipped"
	Reason         string // a code, for failed and skipped
	Summary        string // the verdict's line, cleaned
	ConversationID string
	Provider       string // the provider's slug ("anthropic" ...)
	Model          string
	USD            float64
	FellBack       bool
}

const (
	scheduledToolCalls = 24
	scheduledTurn      = 10 * time.Minute
	// scheduledAttempts is how often one model call is tried on the same provider before the run gives up on it.
	scheduledAttempts = 3
	verdictTool       = "task_verdict"
)

// ScheduledRunLimit bounds one RunScheduled: a turn on each of two providers, with the waits between attempts.
const ScheduledRunLimit = 2*scheduledTurn + 2*time.Minute

const scheduledNote = `This turn is a scheduled run of a task the owner set up. Nobody reads it while it runs: the owner reads the answer later, in this conversation.
- Do what the task asks with the read and run tools. Do not ask questions: decide from what the tools return, and say what you could not check.
- You may prepare a plan (a *_plan tool) when a change is clearly needed; the owner reviews it later. You cannot apply it.
- End by calling task_verdict exactly once: problem=true when you found something the owner should look at or act on, false when all is well; summary is one plain line of at most 200 characters that says what (node, group or protocol names are fine), never a secret.`

const verdictNote = "Call task_verdict now with your verdict on this run, and nothing else."

func verdictSchema() mcp.AssistantTool {
	return mcp.AssistantTool{Name: verdictTool,
		Description: "Your verdict on this scheduled run, called once at the end: problem is true when the owner should look at or act on something; summary is one line (at most 200 characters) saying what.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"problem":{"type":"boolean"},"summary":{"type":"string","maxLength":300}},"required":["problem","summary"],"additionalProperties":false}`)}
}

type scheduledVerdict struct {
	Problem bool   `json:"problem"`
	Summary string `json:"summary"`
}

// retryWait is the wait before attempt n+2 of a model call: 5 s, then 30 s (tests set Service.retryWaits).
func (s *Service) retryWait(n int) time.Duration {
	waits := s.retryWaits
	if waits == nil {
		waits = []time.Duration{5 * time.Second, 30 * time.Second}
	}
	if len(waits) == 0 {
		return 0
	}
	return waits[min(n, len(waits)-1)]
}

// transientCode: a provider answer worth another attempt on the same provider.
func transientCode(code string) bool {
	return code == "provider_unreachable" || code == "provider_overloaded" || code == "rate_limited"
}

// RunScheduled runs one scheduled assistant task and reports the outcome. It never returns an error: everything that went wrong
// is a status and a reason code, recorded with the run.
func (s *Service) RunScheduled(ctx context.Context, run ScheduledRun) ScheduledOutcome {
	out := ScheduledOutcome{ConversationID: run.ConversationID}
	fail := func(status, reason string) ScheduledOutcome {
		out.Status, out.Reason = status, reason
		return out
	}
	if s.st == nil || s.vault == nil {
		return fail("failed", "internal")
	}
	if err := s.ensureRows(ctx); err != nil {
		return fail("failed", "internal")
	}
	settings, err := s.readSettings(ctx)
	if err != nil {
		return fail("failed", "internal")
	}
	if !settings.enabled {
		return fail("skipped", "assistant_off")
	}
	if settings.provider == adminv1.AssistantProvider_ASSISTANT_PROVIDER_UNSPECIFIED {
		return fail("skipped", "no_provider")
	}
	admin, err := s.st.Admin(ctx, run.AdminID)
	if err != nil || admin.Role != store.RoleOwner && admin.Role != store.RoleHelper {
		return fail("skipped", "not_allowed")
	}
	s.mu.RLock()
	runtime := s.tools
	s.mu.RUnlock()
	if runtime == nil {
		return fail("failed", "tools_unavailable")
	}
	providers := []adminv1.AssistantProvider{settings.provider}
	if fb := settings.fallback; fb != adminv1.AssistantProvider_ASSISTANT_PROVIDER_UNSPECIFIED && fb != settings.provider && validProvider(fb) {
		providers = append(providers, fb)
	}

	// The conversation: the lock first (an owner going on from the last run in the chat keeps the turn), then a new part.
	conversationID, err := s.scheduledConversation(ctx, admin, run)
	if err != nil {
		return fail("failed", "internal")
	}
	out.ConversationID = conversationID
	leaseID := store.NewID("assturn_")
	started := s.now()
	locked, err := s.st.AcquireAssistantTurn(ctx, conversationID, admin.ID, leaseID, started, started.Add(ScheduledRunLimit))
	if err != nil {
		return fail("failed", "internal")
	}
	if !locked {
		return fail("skipped", "conversation_busy")
	}
	defer func() { _ = s.st.ReleaseAssistantTurn(context.WithoutCancel(ctx), conversationID, leaseID) }()
	segment, err := s.nextSegment(ctx, admin.ID, conversationID)
	if err != nil {
		return fail("failed", "internal")
	}

	taskContext := &adminv1.PageContext{Kind: "task", Id: cleanContextText(run.TaskID, 100), Label: cleanContextText(run.TaskName, 120)}
	prompt := strings.TrimSpace(mcp.ScrubText(run.Prompt))
	userTranscript, _ := json.Marshal(map[string]any{"role": "user", "content": prompt})
	if _, err = s.saveMessage(context.WithoutCancel(ctx), conversationID, admin.ID, "user", segment, messageBody{Text: prompt, Context: taskContext, Transcript: []json.RawMessage{userTranscript}}, s.now()); err != nil {
		return fail("failed", "internal")
	}

	identity, err := s.st.EnsureScheduledToken(ctx, admin.ID, s.now())
	if err != nil {
		return fail("failed", "internal")
	}
	session, err := runtime.OpenAssistant(ctx, mcp.AssistantIdentity{TokenID: identity.ID, Profile: store.ProfileOperator, AdminID: admin.ID, AdminName: admin.DisplayName, Scheduled: true})
	if err != nil {
		return fail("failed", "tools_unavailable")
	}
	defer session.Close()
	registry, err := session.ListTools(ctx)
	if err != nil {
		return fail("failed", "tools_unavailable")
	}
	tools := make([]chatTool, 0, len(registry)+1)
	for _, t := range registry {
		if t.Name == "" || strings.HasSuffix(t.Name, "_apply") {
			continue
		}
		tools = append(tools, chatTool{AssistantTool: t})
	}
	tools = append(tools, chatTool{AssistantTool: verdictSchema(), local: true})

	note := scheduledNote + "\nThe task is named " + quoteUntrusted(cleanContextText(run.TaskName, 120)) + "."
	system := strings.TrimSpace(note)

	lastCode, attemptedAny := "", false
	for i, provider := range providers {
		turn := s.scheduledTurn(ctx, admin, identity.ID, conversationID, provider, settings.effort, system, tools, session, userTranscript)
		out.USD += turn.usd
		attemptedAny = attemptedAny || turn.attempted
		if turn.code != "" {
			lastCode = turn.code
			if ctx.Err() != nil {
				break
			}
			continue
		}
		out.Provider, out.Model, out.FellBack = providerSlug(provider), turn.model, i > 0
		body := messageBody{Text: turn.text, Context: taskContext, Tools: turn.tools, Plans: turn.plans, End: "end", Transcript: turn.transcript}
		if turn.verdict == nil {
			body.End = "error"
		} else if strings.TrimSpace(body.Text) == "" {
			body.Text = cleanContextText(turn.verdict.Summary, 300)
		}
		if _, err = s.saveMessage(context.WithoutCancel(ctx), conversationID, admin.ID, "assistant", segment, body, s.now()); err != nil {
			s.log.Warn("scheduled assistant answer save failed", "code", "scheduled_answer_unsaved", "task_id", run.TaskID, "err", err)
		}
		if turn.verdict == nil {
			return fail("failed", "no_verdict")
		}
		out.Summary = cleanContextText(turn.verdict.Summary, 300)
		out.Status = "ok"
		if turn.verdict.Problem {
			out.Status = "problem"
		}
		return out
	}
	// every provider failed: the conversation says so
	_, _ = s.saveMessage(context.WithoutCancel(ctx), conversationID, admin.ID, "assistant", segment, messageBody{Context: taskContext, End: "error"}, s.now())
	if lastCode == "" {
		lastCode = "provider_unreachable"
	}
	if !attemptedAny && (lastCode == "budget_reached" || lastCode == "no_provider") {
		return fail("skipped", lastCode)
	}
	return fail("failed", lastCode)
}

// quoteUntrusted puts a name the owner typed in quotes for the system prompt.
func quoteUntrusted(s string) string { return "“" + strings.ReplaceAll(s, "”", "'") + "”" }

// scheduledConversation is the task's conversation, or a new one (the first run, or the owner deleted it).
func (s *Service) scheduledConversation(ctx context.Context, admin store.Admin, run ScheduledRun) (string, error) {
	if run.ConversationID != "" {
		var id string
		err := s.st.R.QueryRowContext(ctx, `SELECT id FROM assistant_conversation WHERE id=? AND admin_id=? AND task_id=?`, run.ConversationID, admin.ID, run.TaskID).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return s.createTaskConversation(ctx, admin.ID, run.TaskID, cleanContextText(run.TaskName, 120), s.now())
}

// nextSegment starts a part of the conversation for one run: what the model of this run reads (activeHistory) is this run only.
// A new conversation starts at part 0 with no messages.
func (s *Service) nextSegment(ctx context.Context, adminID, conversationID string) (int, error) {
	var segment, messages int
	if err := s.st.R.QueryRowContext(ctx, `SELECT segment,(SELECT count(*) FROM assistant_message WHERE conversation_id=c.id) FROM assistant_conversation c WHERE id=? AND admin_id=?`, conversationID, adminID).Scan(&segment, &messages); err != nil {
		return 0, err
	}
	if messages == 0 {
		return segment, nil
	}
	segment++
	_, err := s.st.W.ExecContext(ctx, `UPDATE assistant_conversation SET segment=?,updated_at=? WHERE id=? AND admin_id=?`, segment, s.now().Unix(), conversationID, adminID)
	return segment, err
}

// scheduledTurnResult is one provider's attempt at a run.
type scheduledTurnResult struct {
	code       string // empty: the turn ended with an answer
	attempted  bool   // a model call reached the provider
	model      string
	usd        float64
	text       string
	tools      []*adminv1.ToolCall
	plans      []*adminv1.PlanCard
	transcript []json.RawMessage
	verdict    *scheduledVerdict
}

// scheduledTurn is the agent loop of one run on one provider: as a chat turn, without a stream, a person or the tools that need one.
func (s *Service) scheduledTurn(ctx context.Context, admin store.Admin, tokenID, conversationID string, provider adminv1.AssistantProvider, effort adminv1.AssistantEffort,
	system string, tools []chatTool, session mcp.AssistantSession, userTranscript json.RawMessage) scheduledTurnResult {
	var res scheduledTurnResult
	row, err := s.readProviderRow(ctx, provider)
	if err != nil || len(row.sealed) == 0 {
		res.code = "no_provider"
		return res
	}
	res.model = row.model
	if res.model == "" {
		res.model = defaultModel(provider)
	}
	if row.budget <= 0 {
		res.code = "budget_reached"
		return res
	}
	if spent, err := s.providerSpend(ctx, provider, monthKey(s.now())); err != nil {
		res.code = "internal"
		return res
	} else if spent >= row.budget {
		res.code = "budget_reached"
		return res
	}
	keyBytes, err := s.vault.Open(row.sealed, ProviderKeyAAD(provider))
	if err != nil {
		res.code = "no_provider"
		return res
	}
	defer zero(keyBytes)
	adapter := s.providers[provider]
	if adapter == nil {
		res.code = "provider_unreachable"
		return res
	}
	byName := make(map[string]chatTool, len(tools))
	all := make([]mcp.AssistantTool, 0, len(tools))
	for _, t := range tools {
		byName[t.Name] = t
		all = append(all, t.AssistantTool)
	}
	turnCtx, cancel := context.WithTimeout(ctx, scheduledTurn)
	defer cancel()
	history := []json.RawMessage{userTranscript}
	var text strings.Builder
	counted := false
	used := 0
	call := func(modelTools []mcp.AssistantTool, note string) (modelResponse, string) {
		roundSystem := system
		if note != "" {
			roundSystem = system + "\n\n" + note
		}
		req := modelRequest{Model: res.model, System: roundSystem, Stable: assistantFixedPrompt, Effort: effort, MaxOutputTokens: defaultMaxOutputTokens, History: history, Tools: modelTools}
		response, code := s.scheduledCall(turnCtx, admin.ID, provider, res.model, row.budget, req, string(keyBytes), adapter, &counted, &res)
		if code != "" {
			if errors.Is(turnCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
				code = "timeout"
			}
			return response, code
		}
		for _, raw := range response.Transcript {
			history = append(history, raw)
			res.transcript = append(res.transcript, raw)
		}
		if response.Text != "" {
			text.WriteString(paragraphBreak(text.String(), response.Text) + response.Text)
		}
		return response, ""
	}
	for {
		finalRound := used >= scheduledToolCalls
		modelTools, note := all, ""
		if finalRound {
			modelTools, note = []mcp.AssistantTool{verdictSchema()}, toolLimitNote+"\n"+verdictNote
		}
		response, code := call(modelTools, note)
		if code != "" {
			res.code = code
			return res
		}
		if response.Stop == "refusal" {
			res.code = "refused"
			return res
		}
		if len(response.Tools) == 0 {
			break
		}
		outputs := make([]toolOutput, 0, len(response.Tools))
		for _, tc := range response.Tools {
			output := s.scheduledTool(turnCtx, session, admin, tokenID, conversationID, tc, byName, &used, finalRound, &res)
			outputs = append(outputs, output)
		}
		for _, raw := range adapter.ToolResults(response.Tools, outputs) {
			history = append(history, raw)
			res.transcript = append(res.transcript, raw)
		}
		if res.verdict != nil || finalRound {
			break
		}
	}
	if res.verdict == nil && turnCtx.Err() == nil {
		// one short round that may only give the verdict
		response, code := call([]mcp.AssistantTool{verdictSchema()}, verdictNote)
		if code == "" {
			var outputs []toolOutput
			for _, tc := range response.Tools {
				outputs = append(outputs, s.scheduledTool(turnCtx, session, admin, tokenID, conversationID, tc, map[string]chatTool{verdictTool: {AssistantTool: verdictSchema(), local: true}}, &used, true, &res))
			}
			for _, raw := range adapter.ToolResults(response.Tools, outputs) {
				res.transcript = append(res.transcript, raw)
			}
		}
	}
	res.text = text.String()
	return res
}

// scheduledTool runs one tool call of a scheduled turn: the verdict, a local tool or a panel tool through MCP.
func (s *Service) scheduledTool(ctx context.Context, session mcp.AssistantSession, admin store.Admin, tokenID, conversationID string, tc modelToolCall, byName map[string]chatTool, used *int, noMore bool, res *scheduledTurnResult) toolOutput {
	tool, exists := byName[tc.name]
	if !exists {
		return toolOutput{err: true, text: "unknown tool; this tool is not available to you"}
	}
	input, err := validateToolInput(tool.InputSchema, tc.input)
	if err != nil {
		return toolOutput{err: true, text: err.Error()}
	}
	if tc.name == verdictTool {
		var v scheduledVerdict
		if json.Unmarshal(input, &v) != nil {
			return toolOutput{err: true, text: "verdict must have problem and summary"}
		}
		if res.verdict == nil {
			res.verdict = &v
		}
		return toolOutput{text: `{"recorded":true}`}
	}
	if noMore || *used >= scheduledToolCalls {
		return toolOutput{err: true, text: "tool call limit reached; no tool was run"}
	}
	*used++
	ui := &adminv1.ToolCall{Id: cleanContextText(tc.id, 100), Tool: cleanContextText(tc.name, 100), ArgsJson: clipped(safeToolArgs(tc.input), 1200), Done: true}
	var execution toolExecution
	if tool.local {
		execution = s.executeLocalTool(ctx, admin, conversationID, tc.name, input)
	} else {
		execution = s.executeMCPTool(ctx, session, admin, conversationID, tokenID, tc.name, input)
	}
	ui.Ok = !execution.output.err
	if execution.output.err {
		ui.Error = clipped(cleanContextText(execution.output.text, 240), 240)
	} else {
		ui.Count = resultCount(execution.output.text)
	}
	res.tools = append(res.tools, ui)
	if execution.card != nil {
		res.plans = append(res.plans, execution.card)
	}
	return execution.output
}

// scheduledCall is one model call of a scheduled turn, tried up to scheduledAttempts times on a transient failure with a growing
// wait between. Every attempt reserves its budget first and settles what the provider reported.
func (s *Service) scheduledCall(ctx context.Context, adminID string, provider adminv1.AssistantProvider, model string, budget float64, req modelRequest, key string,
	a adapter, counted *bool, res *scheduledTurnResult) (modelResponse, string) {
	for attempt := 0; ; attempt++ {
		reservation, err := s.reserveProviderCall(ctx, adminID, provider, model, budget, req)
		if err != nil {
			if code := assistantErrorCode(err); code == "budget_reached" {
				return modelResponse{}, code
			}
			return modelResponse{}, "internal"
		}
		res.attempted = true
		callCtx, cancel := context.WithTimeout(ctx, providerTimeout)
		response, callErr := a.Stream(callCtx, key, req, nil)
		cancel()
		res.usd += s.reconcileProviderCall(ctx, reservation, adminID, provider, model, response.InputTokens, response.OutputTokens, response.UsageReported, !*counted)
		*counted = true
		if callErr == nil {
			s.clearProviderError(ctx, provider)
			return response, ""
		}
		code := providerCode(callErr)
		if code == "provider_unreachable" || code == "provider_overloaded" || code == "rate_limited" || code == "bad_key" || code == "no_balance" {
			s.setProviderError(ctx, provider, code)
		}
		if !transientCode(code) || attempt+1 >= scheduledAttempts || ctx.Err() != nil {
			return modelResponse{}, code
		}
		wait := s.retryWait(attempt)
		select {
		case <-ctx.Done():
			return modelResponse{}, code
		case <-time.After(wait):
		}
	}
}
