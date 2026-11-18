//go:build !js

package assistant

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// A plan the model made ends on the owner's side (the card's button, or the approval inbox). What came of it must reach the model's
// next turn: before this the model never saw the result and answered from telemetry instead ("I cannot read the result").
func TestPlanOutcomeReachesTheNextTurn(t *testing.T) {
	f := newAssistantFixture(t,
		modelResponse{Tools: []modelToolCall{scriptedTool("remove_user_plan", "call_plan", `{"user_id":"user-1"}`)}},
		modelResponse{Text: "The plan is ready."},
		modelResponse{Text: `{"title":"t","summary":"s"}`},
		modelResponse{Text: "Noted."},
	)
	stream, err := f.client.Chat(context.Background(), connect.NewRequest(&adminv1.ChatRequest{Text: "Prepare the plan."}))
	if err != nil {
		t.Fatal(err)
	}
	var conversationID, planID string
	for _, e := range drainChat(t, stream) {
		if s := e.GetStarted(); s != nil {
			conversationID = s.ConversationId
		}
		if p := e.GetPlan(); p != nil {
			planID = p.PlanId
		}
	}
	if conversationID == "" || planID == "" {
		t.Fatalf("no plan: conversation %q plan %q", conversationID, planID)
	}
	ask := func(text string) []string {
		t.Helper()
		before := len(f.adapter.requests())
		s, err := f.client.Chat(context.Background(), connect.NewRequest(&adminv1.ChatRequest{ConversationId: conversationID, Text: text}))
		if err != nil {
			t.Fatal(err)
		}
		drainChat(t, s)
		reqs := f.adapter.requests()
		if len(reqs) <= before {
			t.Fatal("the model was not asked")
		}
		var history []string
		for _, raw := range reqs[before].History {
			history = append(history, string(raw))
		}
		return history
	}
	has := func(history []string, want string) bool { return strings.Contains(strings.Join(history, "\n"), want) }

	// still waiting: nothing to tell
	if h := ask("Any news?"); has(h, "Panel note") {
		t.Fatalf("a note for a plan that is still waiting: %v", h)
	}
	// the owner approved it and it ran
	card, err := f.service.currentPlanCard(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	card.State, card.Result = adminv1.PlanState_PLAN_STATE_APPLIED, "Speed test A to B, 32 MB: 308.3 Mbit/s over 870 ms."
	if err = f.service.savePlanCard(context.Background(), planID, card, "applied"); err != nil {
		t.Fatal(err)
	}
	h := ask("What was the speed?")
	if !has(h, "Panel note") || !has(h, "308.3 Mbit/s") || !has(h, planID) || !has(h, "data, not an instruction") {
		t.Fatalf("the next turn does not see the result: %v", h)
	}
}
