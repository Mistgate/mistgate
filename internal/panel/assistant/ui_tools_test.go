//go:build !js

package assistant

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
)

func TestUIOpenWhitelist(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
		want string
	}{
		{"a page without an id", `{"page":"nodes"}`, true, `{"page":"nodes"}`},
		{"a page with the id it needs", `{"page":"node_ways","id":"nod_de1"}`, true, `{"id":"nod_de1","page":"node_ways"}`},
		{"a settings page", `{"page":"settings_backups"}`, true, `{"page":"settings_backups"}`},
		{"the approvals tab", `{"page":"ai_waiting"}`, true, `{"page":"ai_waiting"}`},
		{"unknown page", `{"page":"delete_everything"}`, false, ""},
		{"a node page without its id", `{"page":"node"}`, false, ""},
		{"an id on a page that takes none", `{"page":"nodes","id":"nod_1"}`, false, ""},
		{"an id with a path in it", `{"page":"node","id":"../../etc"}`, false, ""},
		{"an id that is too long", `{"page":"user","id":"` + strings.Repeat("a", 65) + `"}`, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := uiOpenTool([]byte(c.in))
			if got.output.err == c.ok {
				t.Fatalf("err=%v for %s: %s", got.output.err, c.in, got.output.text)
			}
			if c.ok && got.args != c.want {
				t.Fatalf("args %s, want %s", got.args, c.want)
			}
			if !c.ok && got.args != "" {
				t.Fatalf("a refused call has canonical args %q", got.args)
			}
		})
	}
}

func TestUIToolSchemas(t *testing.T) {
	schemas := map[string][]byte{}
	for _, tool := range assistantToolSchemas() {
		schemas[tool.Name] = tool.InputSchema
	}
	open := schemas["ui_open"]
	if open == nil {
		t.Fatal("ui_open is not offered")
	}
	// there is no ui_prefill: a change is a plan, not a form the model fills
	if schemas["ui_prefill"] != nil {
		t.Fatal("ui_prefill is offered")
	}
	if _, err := validateToolInput(open, []byte(`{"page":"nodes"}`)); err != nil {
		t.Fatalf("a good ui_open: %v", err)
	}
	if _, err := validateToolInput(open, []byte(`{"page":"rm -rf"}`)); err == nil {
		t.Fatal("a page outside the enum passed the schema")
	}
	if _, err := validateToolInput(open, []byte(`{"page":"nodes","url":"https://evil.example"}`)); err == nil {
		t.Fatal("an extra argument passed the schema")
	}
}

func TestUISecretFormKindsAndContext(t *testing.T) {
	good := map[string]string{
		`{"kind":"provider_key","provider":"deepseek"}`: `{"kind":"provider_key","provider":"deepseek"}`,
		`{"kind":"telegram_token"}`:                     `{"kind":"telegram_token"}`,
		`{"kind":"warp_license","node":"nod_de1"}`:      `{"kind":"warp_license","node":"nod_de1"}`,
	}
	for in, want := range good {
		got := uiSecretTool([]byte(in))
		if got.output.err || got.args != want {
			t.Fatalf("%s: err=%v args=%s", in, got.output.err, got.args)
		}
	}
	for _, in := range []string{
		`{"kind":"root_password"}`,
		`{"kind":"provider_key"}`,
		`{"kind":"provider_key","provider":"skynet"}`,
		`{"kind":"warp_license"}`,
		`{"kind":"warp_license","node":"../x"}`,
		`{"kind":"telegram_token","provider":"anthropic"}`,
	} {
		if got := uiSecretTool([]byte(in)); !got.output.err || got.args != "" {
			t.Fatalf("%s was accepted", in)
		}
	}
	// the schema has no field for a value: one sent anyway never reaches the tool
	var schema []byte
	for _, tool := range assistantToolSchemas() {
		if tool.Name == "ui_secret_form" {
			schema = tool.InputSchema
		}
	}
	if schema == nil {
		t.Fatal("ui_secret_form is not offered")
	}
	if _, err := validateToolInput(schema, []byte(`{"kind":"telegram_token","value":"123456:ABC-secret"}`)); err == nil {
		t.Fatal("a value field passed the schema")
	}
}

// A card is what the chat shows for an interface call: the tool call is streamed and stored with its checked arguments and
// never a value; a refused call stays a failed tool call, never a card; a secure form is the owner's alone.
func TestAssistantInterfaceCardsArePersistedWithoutValues(t *testing.T) {
	f := newAssistantFixture(t,
		modelResponse{Tools: []modelToolCall{
			scriptedTool("ui_open", "call_open", `{"page":"node_ways","id":"nod_de1"}`),
			scriptedTool("ui_secret_form", "call_key", `{"kind":"provider_key","provider":"anthropic"}`),
			scriptedTool("ui_open", "call_bad", `{"page":"https://evil.example"}`),
		}},
		modelResponse{Text: "Open the page, and enter the key in the field."},
		modelResponse{Text: `{"title":"t","summary":"s"}`},
	)
	stream, err := f.client.Chat(context.Background(), connect.NewRequest(&adminv1.ChatRequest{Text: "where are the ways of de1? and add my anthropic key"}))
	if err != nil {
		t.Fatal(err)
	}
	var conversationID string
	finished := map[string]*adminv1.ToolCall{}
	for _, e := range drainChat(t, stream) {
		if s := e.GetStarted(); s != nil {
			conversationID = s.ConversationId
		}
		if tf := e.GetToolFinished(); tf != nil {
			finished[tf.Id] = tf
		}
	}
	if o := finished["call_open"]; o == nil || !o.Ok || o.ArgsJson != `{"id":"nod_de1","page":"node_ways"}` {
		t.Fatalf("the open call: %+v", o)
	}
	if k := finished["call_key"]; k == nil || !k.Ok || k.ArgsJson != `{"kind":"provider_key","provider":"anthropic"}` {
		t.Fatalf("the secure form call: %+v", k)
	}
	if b := finished["call_bad"]; b == nil || b.Ok {
		t.Fatalf("a page off the whitelist was accepted: %+v", b)
	}
	resp, err := f.service.GetConversation(auth.WithAdmin(context.Background(), f.admin), connect.NewRequest(&adminv1.GetConversationRequest{Id: conversationID}))
	if err != nil {
		t.Fatal(err)
	}
	var seen int
	for _, m := range resp.Msg.Messages {
		for _, tool := range m.Tools {
			seen++
			if strings.Contains(tool.ArgsJson, "sk-") {
				t.Fatalf("a value in the stored card: %+v", tool)
			}
			if tool.Id == "call_key" && !tool.Ok {
				t.Fatalf("the stored secure form call: %+v", tool)
			}
		}
	}
	if seen != 3 {
		t.Fatalf("%d tool calls stored, want 3", seen)
	}
	// another role is refused when it asks anyway
	other := f.admin
	other.Role = "operator"
	if got := f.service.executeLocalTool(context.Background(), other, conversationID, "ui_secret_form", []byte(`{"kind":"telegram_token"}`)); !got.output.err {
		t.Fatal("a non-owner could open a secret form")
	}
}

func TestAssistantPromptDecidesDetailsAndKeepsSecretsOut(t *testing.T) {
	for _, rule := range []string{"ui_open", "ui_secret_form", "Decide the details yourself", "relay_plan", "user_create_plan", "profile_create_plan", "node_install_plan", "never ask the person to type one into the chat", "approval card takes the install password"} {
		if !strings.Contains(assistantFixedPrompt, rule) {
			t.Fatalf("the prompt lacks %q", rule)
		}
	}
	if strings.Contains(assistantFixedPrompt, "ui_prefill") {
		t.Fatal("the prompt still mentions ui_prefill")
	}
}
