//go:build !js

package assistant

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The composer's picker: a turn may run on another connected provider and at another depth than the panel's settings;
// a provider without a key, or a value out of range, is refused before anything is spent.
func TestAssistantTurnPicksProviderAndEffort(t *testing.T) {
	f := newAssistantFixture(t, modelResponse{Text: "Fine."}, modelResponse{Text: `{"title":"t","summary":"s"}`})
	stream, err := f.client.Chat(context.Background(), connect.NewRequest(&adminv1.ChatRequest{
		Text: "How are the nodes?", Provider: adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK, Effort: adminv1.AssistantEffort_ASSISTANT_EFFORT_THOROUGH,
	}))
	if err != nil {
		t.Fatal(err)
	}
	drainChat(t, stream)
	reqs := f.adapter.requests()
	if len(reqs) == 0 || reqs[0].Effort != adminv1.AssistantEffort_ASSISTANT_EFFORT_THOROUGH {
		t.Fatalf("the turn did not run at the picked depth: %+v", reqs)
	}

	refused := func(req *adminv1.ChatRequest, want connect.Code) {
		t.Helper()
		before := len(f.adapter.requests())
		stream, err := f.client.Chat(context.Background(), connect.NewRequest(req))
		if err == nil {
			for stream.Receive() {
			}
			err = stream.Err()
		}
		var ce *connect.Error
		if !errors.As(err, &ce) || ce.Code() != want {
			t.Fatalf("%+v: got %v, want %v", req, err, want)
		}
		if len(f.adapter.requests()) != before {
			t.Fatalf("%+v reached the provider", req)
		}
	}
	// Anthropic has no key on this panel
	refused(&adminv1.ChatRequest{Text: "hi", Provider: adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC}, connect.CodeFailedPrecondition)
	refused(&adminv1.ChatRequest{Text: "hi", Provider: adminv1.AssistantProvider(99)}, connect.CodeInvalidArgument)
	refused(&adminv1.ChatRequest{Text: "hi", Effort: adminv1.AssistantEffort(99)}, connect.CodeInvalidArgument)
}
