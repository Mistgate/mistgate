//go:build !js

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAssistantFollowUpKeepsReadProfileAndCapsChanges(t *testing.T) {
	e := newTestEnv(t)
	server, err := NewAssistant(Config{Plans: e.plans, Auth: e.auth, API: e.w.api(e.auth), Now: e.now})
	if err != nil {
		t.Fatal(err)
	}

	owner, err := server.OpenAssistant(context.Background(), AssistantIdentity{
		TokenID: "tk_owner", Profile: string(ProfileAdmin), AdminID: "adm_owner", FollowUp: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	ownerTools, err := owner.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ownerNames := make(map[string]bool, len(ownerTools))
	for _, tool := range ownerTools {
		ownerNames[tool.Name] = true
	}
	if !ownerNames["node_debug_logs"] {
		t.Fatal("owner follow-up was not offered node_debug_logs")
	}
	if ownerNames["dns_preset_create_plan"] {
		t.Fatal("owner follow-up was offered an admin-level plan tool")
	}
	read, err := owner.CallTool(context.Background(), "node_debug_logs", json.RawMessage(`{}`))
	if err != nil || read.IsError {
		t.Fatalf("owner follow-up could not read node_debug_logs: %+v %v", read, err)
	}
	if !strings.Contains(read.Text, "captures") {
		t.Fatalf("node_debug_logs returned no capture list: %s", read.Text)
	}
	denied, err := owner.CallTool(context.Background(), "dns_preset_create_plan", json.RawMessage(`{}`))
	if err != nil || !denied.IsError || !strings.Contains(denied.Text, "live turn") {
		t.Fatalf("owner follow-up was not refused an admin plan tool: %+v %v", denied, err)
	}

	helper, err := server.OpenAssistant(context.Background(), AssistantIdentity{
		TokenID: "tk_helper", Profile: string(ProfileOperator), AdminID: "adm_helper", FollowUp: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Close() })
	helperTools, err := helper.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool, len(helperTools))
	for _, tool := range helperTools {
		got[tool.Name] = true
	}
	want := make(map[string]bool)
	for _, tool := range toolsFor(ProfileOperator) {
		if !strings.HasSuffix(tool.name, "_apply") {
			want[tool.name] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("helper follow-up tool count = %d, operator profile has %d", len(got), len(want))
	}
	for name := range got {
		if !want[name] {
			t.Errorf("helper follow-up was offered tool outside its profile: %s", name)
		}
	}
	if got["node_debug_logs"] || got["dns_preset_create_plan"] {
		t.Fatal("helper follow-up exceeded the helper's read or change profile")
	}
	helperRead, err := helper.CallTool(context.Background(), "node_debug_logs", json.RawMessage(`{}`))
	if err != nil || !helperRead.IsError {
		t.Fatalf("helper follow-up was allowed to call node_debug_logs: %+v %v", helperRead, err)
	}
}
