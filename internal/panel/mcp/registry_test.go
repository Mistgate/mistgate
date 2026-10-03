package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
)

// Every tool's minimum profile is the strictest role among the procedures it calls: a tool cannot claim to be safer than
// its procedures (auth.ProcedureRole is the panel's own policy). Every procedure must be one tokens may reach, and one that
// needs the owner's approval or a step-up must belong to a dangerous tool, whose apply waits for the owner.
func TestRegistryMatchesPolicy(t *testing.T) {
	for _, td := range registry {
		want := 1
		for _, p := range td.procs {
			want = max(want, roleRank(auth.ProcedureRole(p)))
			access, ok := auth.TokenAccess(p)
			if !ok {
				t.Errorf("%s: %s is not on the token allow-list", td.name, p)
			}
			if (auth.NeedsStepUp(p) || access == auth.TokenAccessApproved) && !td.danger {
				t.Errorf("%s: calls %s, which needs the owner's approval, but is not marked dangerous", td.name, p)
			}
		}
		if got := roleRank(profileRole(td.min)); got != want {
			t.Errorf("%s: minimum profile %s is role rank %d, its procedures need %d", td.name, td.min, got, want)
		}
	}
}

// The link and the password of a user's page (GetSubscriptionLink) are the user's credentials: no tool reaches them, and
// the one that creates a user shows neither (the owner copies both in the admin panel).
func TestNoToolReachesTheLinkOrThePagePassword(t *testing.T) {
	for _, td := range registry {
		if slices.Contains(td.procs, adminv1connect.UserServiceGetSubscriptionLinkProcedure) {
			t.Errorf("%s calls GetSubscriptionLink, which returns the link and the page password", td.name)
		}
	}
	if _, ok := auth.TokenAccess(adminv1connect.UserServiceGetSubscriptionLinkProcedure); ok {
		t.Error("API tokens may call GetSubscriptionLink")
	}
}

// roleRank orders the roles the way the policy does.
func roleRank(role string) int {
	switch role {
	case "readonly":
		return 1
	case "helper":
		return 2
	case "owner":
		return 3
	}
	return 0
}

// profileRole is the role a token profile maps to.
func profileRole(p Profile) string {
	switch p {
	case ProfileReadonly:
		return "readonly"
	case ProfileOperator:
		return "helper"
	}
	return "owner"
}

func TestRegistryShape(t *testing.T) {
	seen := map[string]Profile{}
	for _, td := range registry {
		if td.add == nil || td.desc == "" || len(td.procs) == 0 {
			t.Errorf("%s: incomplete entry", td.name)
		}
		if prev, dup := seen[td.name+"/"+string(td.min)]; dup {
			t.Errorf("%s registered twice for %s", td.name, prev)
		}
		seen[td.name+"/"+string(td.min)] = td.min
		d := td.description()
		if strings.HasSuffix(td.name, "_plan") && !strings.Contains(d, planNotice) {
			t.Errorf("%s lacks the plan notice", td.name)
		}
		isRead := !strings.HasSuffix(td.name, "_apply")
		if isRead && !td.free {
			t.Errorf("%s returns text from data but is not marked free-text", td.name)
		}
		if td.free && !strings.HasSuffix(d, untrustedNotice) {
			t.Errorf("%s lacks the untrusted-data notice", td.name)
		}
		if strings.HasSuffix(td.name, "_apply") && td.free {
			t.Errorf("%s: apply results are panel text only", td.name)
		}
	}
}

func TestToolsPerProfile(t *testing.T) {
	e := newTestEnv(t)
	want := map[Profile]int{ProfileReadonly: 14, ProfileOperator: 28, ProfileAdmin: 46}
	got := map[Profile][]string{}
	for p, n := range want {
		_, secret := e.token(p)
		names := toolNames(t, e.session(secret))
		got[p] = names
		if len(names) != n {
			t.Errorf("%s sees %d tools, want %d: %v", p, len(names), n, names)
		}
		if len(toolsFor(p)) != n {
			t.Errorf("%s: registry says %d tools, want %d", p, len(toolsFor(p)), n)
		}
	}
	for _, n := range got[ProfileReadonly] {
		if strings.HasSuffix(n, "_plan") || strings.HasSuffix(n, "_apply") {
			t.Errorf("readonly sees a change tool: %s", n)
		}
	}
	for _, n := range got[ProfileOperator] {
		if slices.Contains([]string{"audit_search", "node_fix_plan", "rollout_start_plan", "node_rollback_apply"}, n) {
			t.Errorf("operator sees an admin tool: %s", n)
		}
	}
	for _, n := range []string{"node_fix_plan", "node_fix_apply", "rollout_start_plan", "rollout_pause_plan", "rollout_resume_plan", "rollout_cancel_plan", "node_rollback_plan", "audit_search"} {
		if !slices.Contains(got[ProfileAdmin], n) {
			t.Errorf("admin lacks %s", n)
		}
	}
}

func TestProvisioningToolsKeepSecretsOutOfPlans(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	properties := map[string]map[string]any{}
	for tool, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name == "node_install_plan" || tool.Name == "node_install_apply" ||
			tool.Name == "node_server_password_rotate_plan" || tool.Name == "node_server_password_rotate_apply" {
			props, _ := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
			properties[tool.Name] = props
		}
	}
	if properties["node_install_plan"]["password"] != nil || properties["node_server_password_rotate_plan"]["new_password"] != nil {
		t.Fatal("password appeared in an MCP plan input")
	}
	if properties["node_install_apply"]["password"] == nil || properties["node_server_password_rotate_apply"]["new_password"] == nil {
		t.Fatal("apply tools must accept their one-call secret inputs")
	}
}

// The doctor's refresh argument exists only for operators and admins.
func TestNodeDoctorRefreshOnlyForOperators(t *testing.T) {
	e := newTestEnv(t)
	has := func(p Profile) bool {
		_, secret := e.token(p)
		s := e.session(secret)
		for tool, err := range s.Tools(context.Background(), nil) {
			if err != nil {
				t.Fatal(err)
			}
			if tool.Name != "node_doctor" {
				continue
			}
			b, _ := tool.InputSchema.(map[string]any)["properties"].(map[string]any)["refresh"]
			return b != nil
		}
		t.Fatal("no node_doctor")
		return false
	}
	if has(ProfileReadonly) {
		t.Error("readonly's node_doctor has refresh")
	}
	if !has(ProfileOperator) || !has(ProfileAdmin) {
		t.Error("operator or admin lacks refresh")
	}
}

// A tool the profile does not see is unknown, whatever its name.
func TestHiddenToolIsUnknown(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	for _, name := range []string{"user_disable_plan", "node_fix_apply", "audit_search"} {
		_, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil {
			t.Errorf("%s: a readonly token could call it", name)
		}
	}
	if n := len(e.w.log); n != 0 {
		t.Errorf("hidden tools reached the API %d times", n)
	}
}
