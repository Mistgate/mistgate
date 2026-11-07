//go:build !js

package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/encoding/protojson"
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

// Every admin who may use the assistant sees the connected providers for the picker, the panel's own first, without
// keys; a provider whose budget is spent is marked.
func TestAssistantAccessListsConnectedProviders(t *testing.T) {
	f := newAssistantFixture(t)
	ctx := context.Background()
	sealed := f.vault.Seal([]byte("second-key-never-return"), ProviderKeyAAD(adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC))
	if _, err := f.st.W.ExecContext(ctx, `UPDATE assistant_provider SET key_enc=?,key_hint='anth',model='',budget_usd=0 WHERE provider=?`, sealed, int32(adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC)); err != nil {
		t.Fatal(err)
	}
	helper := store.Admin{ID: "adm_helper", DisplayName: "Helper", Role: store.RoleHelper}
	if _, err := f.st.W.ExecContext(ctx, `INSERT INTO admin(id,display_name,role,user_handle,created_at) VALUES(?,?,?,?,?)`, helper.ID, helper.DisplayName, helper.Role, []byte("helper"), time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	resp, err := f.service.GetAssistantAccess(auth.WithAdmin(ctx, helper), connect.NewRequest(&adminv1.GetAssistantAccessRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	got := resp.Msg.Choices
	if len(got) != 2 || got[0].Provider != adminv1.AssistantProvider_ASSISTANT_PROVIDER_DEEPSEEK || got[0].Model != "deepseek-flash" || got[0].BudgetReached {
		t.Fatalf("the panel's provider is not first and usable: %+v", got)
	}
	if got[1].Provider != adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC || got[1].Model != defaultModel(adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC) || !got[1].BudgetReached {
		t.Fatalf("a connected provider without a budget is missing or not marked: %+v", got[1])
	}
	if resp.Msg.Effort != adminv1.AssistantEffort_ASSISTANT_EFFORT_NORMAL {
		t.Fatalf("effort = %v", resp.Msg.Effort)
	}
	encoded, _ := protojson.Marshal(resp.Msg)
	if strings.Contains(string(encoded), "never-return") || strings.Contains(string(encoded), "anth") {
		t.Fatalf("the access answer carries key material: %s", encoded)
	}
}
