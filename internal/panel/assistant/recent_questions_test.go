package assistant

import (
	"context"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The message box finishes a question from what the admin asked before: their own one-line questions of every conversation,
// newest first, each once; not a follow-up's turn, not a scheduled task's, not several lines.
func TestRecentQuestionsAreTheAdminsOwnOneLiners(t *testing.T) {
	f := newAssistantFixture(t, modelResponse{Text: "All quiet."}, modelResponse{Text: `{"title":"Nodes","summary":"Nodes."}`})
	ctx := context.Background()
	stream, err := f.client.Chat(ctx, connect.NewRequest(&adminv1.ChatRequest{Text: "Which nodes had problems today?"}))
	if err != nil {
		t.Fatal(err)
	}
	var conv string
	for _, ev := range drainChat(t, stream) {
		if s := ev.GetStarted(); s != nil {
			conv = s.ConversationId
		}
	}
	now := time.Now()
	task, err := f.service.createTaskConversation(ctx, f.admin.ID, "task_1", "Nightly", now)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range []struct {
		conv string
		body messageBody
	}{
		{conv, messageBody{Text: "Why can alice not connect?"}},
		{conv, messageBody{Text: "which nodes had problems today?"}},
		{conv, messageBody{Text: "first line\nsecond line"}},
		{conv, messageBody{Text: "read the result", FollowUp: &adminv1.FollowUpTurn{Id: "fu_1"}}},
		{task, messageBody{Text: "Check the fleet"}},
	} {
		if _, err = f.service.saveMessage(ctx, m.conv, f.admin.ID, "user", 0, m.body, now.Add(time.Duration(i+1)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.client.RecentQuestions(ctx, connect.NewRequest(&adminv1.RecentQuestionsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"which nodes had problems today?", "Why can alice not connect?"}; !slices.Equal(got.Msg.Questions, want) {
		t.Fatalf("questions = %q, want %q", got.Msg.Questions, want)
	}
}
