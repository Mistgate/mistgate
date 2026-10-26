package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func seedProfile(e *testEnv, id string, inbounds ...*adminv1.Inbound) {
	e.t.Helper()
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	e.w.profiles[id] = &worldProfile{
		summary: &adminv1.ProfileSummary{
			Id: id, Name: "Main", Protocol: "hysteria2", Summary: "UDP 443", NodeCount: uint32(len(inbounds)),
			UserCount: 4, Version: 7,
			Warnings: []*adminv1.StatusReason{{Code: "inbound_failed", Params: map[string]string{"node": "de1"}}},
			Formats: []*adminv1.ProfileFormat{
				{Format: adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, Ok: true},
				{Format: adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON, Note: "self_signed"},
			},
		},
		settings: `{"port":443,"tls":{"server_name":"example.com","password":"` + canaryTK + `"}}`,
		inbounds: cloneInbounds(inbounds),
	}
}

func seedGroupProfile(e *testEnv, groupID, profileID string) {
	e.t.Helper()
	e.w.mu.Lock()
	defer e.w.mu.Unlock()
	g := e.w.groups[groupID]
	if g == nil {
		e.t.Fatalf("fixture group %q is missing", groupID)
	}
	if !slices.Contains(g.GetProfileIds(), profileID) {
		g.ProfileIds = append(g.ProfileIds, profileID)
	}
}

func fixtureInbound(id, profileID, nodeID, nodeName string) *adminv1.Inbound {
	return &adminv1.Inbound{
		Id: id, ProfileId: profileID, NodeId: nodeID, NodeName: nodeName,
		Port: 443, TlsServerName: "effective.example", PortOverride: 8443, TlsServerNameOverride: "edge.example", Enabled: true,
		State: adminv1.InboundState_INBOUND_STATE_ACTIVE,
	}
}

func mustPlanApproval(t *testing.T, e *testEnv, p PlanOut, approve bool) {
	t.Helper()
	if err := e.plans.decide(p.PlanID, approve); err != nil {
		t.Fatal(err)
	}
}

func TestProfileToolsRegisteredAndScoped(t *testing.T) {
	e := newTestEnv(t)
	_, readonly := e.token(ProfileReadonly)
	readNames := toolNames(t, e.session(readonly))
	for _, name := range []string{"protocols_list", "profiles_list", "profile_get"} {
		if !slices.Contains(readNames, name) {
			t.Errorf("readonly token cannot see %s", name)
		}
	}
	for _, name := range []string{"profile_create_plan", "profile_create_apply", "profile_update_plan", "profile_update_apply", "profile_deploy_plan", "profile_deploy_apply"} {
		if slices.Contains(readNames, name) {
			t.Errorf("readonly token can see admin tool %s", name)
		}
	}

	_, admin := e.token(ProfileAdmin)
	s := e.session(admin)
	for _, name := range []string{"profile_create_plan", "profile_create_apply", "profile_update_plan", "profile_update_apply", "profile_deploy_plan", "profile_deploy_apply"} {
		if !slices.Contains(toolNames(t, s), name) {
			t.Errorf("admin token cannot see %s", name)
		}
	}
	for tool, err := range s.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(tool.Name, "_apply") || !strings.HasPrefix(tool.Name, "profile_") {
			continue
		}
		props, _ := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
		if len(props) != 1 || props["confirm_token"] == nil {
			t.Errorf("%s apply arguments are not limited to confirm_token: %v", tool.Name, props)
		}
	}
}

func TestProfileReadToolsMaskSecretsAndExposePlacement(t *testing.T) {
	e := newTestEnv(t)
	seedProfile(e, "prf_test", fixtureInbound("inb_test", "prf_test", nodeA, "de1"))
	seedGroupProfile(e, "grp_1", "prf_test")
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)

	protocolsText := mustOK(t, s, "protocols_list", map[string]any{})
	noCanary(t, "protocols_list", protocolsText)
	protocols := decode[protocolsListV](t, protocolsText)
	if len(protocols.Protocols) != 1 || protocols.Protocols[0].ID != "hysteria2" || !slices.Contains(protocols.Protocols[0].Apps, "happ") {
		t.Fatalf("protocol list: %+v", protocols)
	}
	var schemaPort, schemaMode, schemaSecret map[string]any
	for _, field := range protocols.Protocols[0].SettingsSchema {
		switch field["path"] {
		case "/port":
			schemaPort = field
		case "/mode":
			schemaMode = field
		case "/tls/password":
			schemaSecret = field
		}
	}
	if schemaPort == nil || schemaPort["type"] != "integer" || schemaPort["min"] != float64(1) || schemaPort["max"] != float64(65535) ||
		schemaPort["default"] != float64(443) || schemaPort["x-critical"] != true || schemaPort["x-secret"] != false {
		t.Errorf("compact numeric schema: %+v", schemaPort)
	}
	if schemaMode == nil || schemaMode["enum"] == nil || schemaMode["default"] != "none" {
		t.Errorf("compact enum schema: %+v", schemaMode)
	}
	if schemaSecret == nil || schemaSecret["type"] != "string" || schemaSecret["x-secret"] != true || schemaSecret["default"] != profileSecretMask {
		t.Errorf("compact secret schema: %+v", schemaSecret)
	}
	var defaults map[string]any
	if err := json.Unmarshal(protocols.Protocols[0].DefaultSettings, &defaults); err != nil {
		t.Fatal(err)
	}
	if got := defaults["tls"].(map[string]any)["password"]; got != profileSecretMask {
		t.Errorf("protocol secret default = %v", got)
	}

	profilesText := mustOK(t, s, "profiles_list", map[string]any{})
	noCanary(t, "profiles_list", profilesText)
	profiles := decode[profileListV](t, profilesText)
	if len(profiles.Profiles) != 1 || profiles.Profiles[0].ID != "prf_test" || profiles.Profiles[0].Version != 7 || !slices.Contains(profiles.Profiles[0].GroupIDs, "grp_1") {
		t.Errorf("profile list projection: %+v", profiles)
	}
	if len(profiles.Profiles[0].Warnings) != 1 || profiles.Profiles[0].Warnings[0].Code != "inbound_failed" || len(profiles.Profiles[0].Formats) != 2 {
		t.Errorf("warnings/formats were omitted: %+v", profiles.Profiles[0])
	}

	profileText := mustOK(t, s, "profile_get", map[string]any{"profile_id": "prf_test"})
	noCanary(t, "profile_get", profileText)
	profile := decode[profileGetV](t, profileText)
	var settings map[string]any
	if err := json.Unmarshal(profile.Settings, &settings); err != nil {
		t.Fatal(err)
	}
	if got := settings["tls"].(map[string]any)["password"]; got != profileSecretMask {
		t.Errorf("profile secret = %v", got)
	}
	if len(profile.Inbounds) != 1 {
		t.Fatalf("inbounds: %+v", profile.Inbounds)
	}
	inbound := profile.Inbounds[0]
	if inbound.NodeID != nodeA || inbound.NodeName != "de1" || inbound.Port != 443 || inbound.PortOverride != 8443 ||
		inbound.TLSServerName != "effective.example" || inbound.TLSServerNameOverride != "edge.example" || !inbound.Enabled || inbound.State != "active" || !slices.Contains(profile.GroupIDs, "grp_1") {
		t.Errorf("inbound projection: %+v; groups=%v", inbound, profile.GroupIDs)
	}
}

func TestProfileCreatePlanApplyAndRollback(t *testing.T) {
	t.Run("lossy port needs owner approval", func(t *testing.T) {
		e := newTestEnv(t)
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_create", map[string]any{
			"protocol": "hysteria2", "name": "Lossy", "node_ids": []string{nodeA}, "allow_lossy_port": true,
		})
		if !p.NeedsApproval || len(p.Danger) == 0 {
			t.Fatalf("lossy port plan did not require approval: %+v", p)
		}
	})

	t.Run("create and place profile", func(t *testing.T) {
		e := newTestEnv(t)
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_create", map[string]any{
			"protocol": "hysteria2", "name": "New edge",
			"settings": map[string]any{"tls": map[string]any{"password": profileSecretMask}},
			"node_ids": []string{nodeA}, "group_ids": []string{"grp_1"},
			"port_overrides": map[string]any{nodeA: 8443},
		})
		if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != dangerFleet {
			t.Fatalf("create did not require approval: %+v", p)
		}
		if factOf(t, p, "port").Value != "8443" {
			t.Errorf("plan did not show the override port: %+v", p.Facts)
		}
		if len(e.w.profiles) != 0 || e.w.inboundCreateCalls != 0 {
			t.Fatal("create plan persisted a profile or inbound")
		}
		mustPlanApproval(t, e, p, true)
		applied := applyOf(t, s, "profile_create", p.ConfirmToken)
		if applied.Status != StatusApplied {
			t.Fatalf("apply: %+v", applied)
		}
		noCanary(t, "create plan and audit", storedPlanText(t, e, p.PlanID))
		audits := auditText(t, e)
		noCanary(t, "create audits", audits)
		if !strings.Contains(audits, `"settings"`) || !strings.Contains(audits, profileSecretMask) {
			t.Errorf("create audit omitted the masked settings: %s", audits)
		}
		e.w.mu.Lock()
		defer e.w.mu.Unlock()
		created := e.w.profiles["prf_created_1"]
		if created == nil || len(created.inbounds) != 1 || created.inbounds[0].GetPort() != 8443 || created.inbounds[0].GetPortOverride() != 8443 {
			t.Fatalf("created profile/inbound: %+v", created)
		}
		if !slices.Contains(e.w.groups["grp_1"].GetProfileIds(), "prf_created_1") {
			t.Errorf("profile was not added to group: %v", e.w.groups["grp_1"].GetProfileIds())
		}
	})

	t.Run("settings omitted", func(t *testing.T) {
		e := newTestEnv(t)
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_create", map[string]any{"protocol": "hysteria2", "name": "Plain"})
		if !p.NeedsApproval {
			t.Fatal("profile creation without settings did not require approval")
		}
		mustPlanApproval(t, e, p, true)
		if got := applyOf(t, s, "profile_create", p.ConfirmToken); got.Status != StatusApplied {
			t.Fatalf("apply: %+v", got)
		}
		var settings map[string]any
		if err := json.Unmarshal([]byte(e.w.profiles["prf_created_1"].settings), &settings); err != nil || settings == nil {
			t.Errorf("settings for empty create are not an object: %s (%v)", e.w.profiles["prf_created_1"].settings, err)
		}
	})

	t.Run("group user changes are preserved", func(t *testing.T) {
		e := newTestEnv(t)
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_create", map[string]any{
			"protocol": "hysteria2", "name": "Busy group", "group_ids": []string{"grp_1"},
		})
		e.w.mu.Lock()
		e.w.groups["grp_1"].UserCount++
		e.w.groups["grp_1"].ProfileIds = append(e.w.groups["grp_1"].ProfileIds, "prf_concurrent")
		e.w.mu.Unlock()
		mustPlanApproval(t, e, p, true)
		if out := applyOf(t, s, "profile_create", p.ConfirmToken); out.Status != StatusApplied {
			t.Fatalf("create after group change: %+v", out)
		}
		g := e.w.groups["grp_1"]
		if g.GetUserCount() != 4 || !slices.Contains(g.GetProfileIds(), "prf_concurrent") || !slices.Contains(g.GetProfileIds(), "prf_created_1") {
			t.Errorf("group update lost current state: %+v", g)
		}
	})

	t.Run("free port refusal", func(t *testing.T) {
		e := newTestEnv(t)
		e.w.refuseInboundNode = nodeB
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		out := mustFail(t, s, "profile_create_plan", map[string]any{"protocol": "hysteria2", "name": "Blocked", "node_ids": []string{nodeB}})
		noCanary(t, "free port refusal", out)
		if !strings.Contains(out, "8443") || !strings.Contains(strings.ToLower(out), "port") {
			t.Errorf("refusal did not include the free port: %s", out)
		}
		if len(e.w.profiles) != 0 || len(e.plans.byID) != 0 {
			t.Fatal("failed validation left a profile or plan")
		}
	})

	t.Run("second inbound failure rolls back", func(t *testing.T) {
		e := newTestEnv(t)
		e.w.failInboundCreateAt = 2
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_create", map[string]any{
			"protocol": "hysteria2", "name": "Rollback", "settings": map[string]any{"tls": map[string]any{"password": "$generate"}},
			"node_ids": []string{nodeA, nodeB}, "group_ids": []string{"grp_1"},
		})
		mustPlanApproval(t, e, p, true)
		out := applyError(t, s, "profile_create", p.ConfirmToken)
		if !strings.Contains(out, "second inbound failed") {
			t.Errorf("unexpected apply failure: %s", out)
		}
		e.w.mu.Lock()
		defer e.w.mu.Unlock()
		if len(e.w.profiles) != 0 || slices.Contains(e.w.groups["grp_1"].GetProfileIds(), "prf_created_1") {
			t.Errorf("rollback left profile/group state: profiles=%v group=%v", e.w.profiles, e.w.groups["grp_1"].GetProfileIds())
		}
	})
}

func TestProfileUpdateAndDeployLossyPortNeedApproval(t *testing.T) {
	t.Run("update", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test")
		_, secret := e.token(ProfileAdmin)
		p := planOf(t, e.session(secret), "profile_update", map[string]any{
			"profile_id": "prf_test", "name": "Renamed", "allow_lossy_port": true,
		})
		if !p.NeedsApproval || len(p.Danger) == 0 {
			t.Fatalf("lossy profile update did not require approval: %+v", p)
		}
	})

	t.Run("deploy", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test")
		_, secret := e.token(ProfileAdmin)
		p := planOf(t, e.session(secret), "profile_deploy", map[string]any{
			"profile_id": "prf_test", "add_node_ids": []string{nodeB}, "allow_lossy_port": true,
		})
		if !p.NeedsApproval || len(p.Danger) == 0 {
			t.Fatalf("lossy deployment did not require approval: %+v", p)
		}
	})
}

func TestProfileUpdateImpactStaleVersionAndMergePatch(t *testing.T) {
	t.Run("merge patch preserves omitted settings and secret", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test")
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_update", map[string]any{
			"profile_id": "prf_test", "settings_patch": map[string]any{"tls": map[string]any{"server_name": "edge.example"}},
		})
		if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != dangerFleet {
			t.Fatalf("profile update did not require approval: %+v", p)
		}
		if factOf(t, p, "setting:/tls/server_name").Value == "" {
			t.Fatalf("field change missing from plan: %+v", p.Facts)
		}
		mustPlanApproval(t, e, p, true)
		applyOf(t, s, "profile_update", p.ConfirmToken)
		stored := storedPlanText(t, e, p.PlanID)
		audits := auditText(t, e)
		noCanary(t, "update plan and audit", stored+audits)
		if !strings.Contains(audits, `"settings_patch"`) {
			t.Errorf("update audit omitted the masked patch: %s", audits)
		}
		e.w.mu.Lock()
		got := e.w.profiles["prf_test"].settings
		e.w.mu.Unlock()
		var settings map[string]any
		if err := json.Unmarshal([]byte(got), &settings); err != nil {
			t.Fatal(err)
		}
		tls := settings["tls"].(map[string]any)
		if tls["server_name"] != "edge.example" || tls["password"] != canaryTK || settings["port"] != float64(443) {
			t.Errorf("merge patch lost existing values: %s", got)
		}
	})

	t.Run("null patch is refused before a plan is stored", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test")
		_, secret := e.token(ProfileAdmin)
		out := mustFail(t, e.session(secret), "profile_update_plan", map[string]any{
			"profile_id": "prf_test", "settings_patch": map[string]any{"tls": map[string]any{"password": nil}},
		})
		if !strings.Contains(out, "settings_patch must not contain null") {
			t.Fatalf("null patch was not refused: %s", out)
		}
		e.plans.mu.Lock()
		plans := len(e.plans.byID)
		e.plans.mu.Unlock()
		if plans != 0 {
			t.Fatal("null patch entered a stored plan")
		}
	})

	t.Run("impact requires approval and stale version is reported", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test")
		e.w.profileUpdateImpact = &adminv1.ProfileImpact{
			InboundsRestarted: 1, UsersOnline: 2, DevicesNeedReissue: 3, CriticalFields: []string{"/port"},
		}
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_update", map[string]any{
			"profile_id": "prf_test", "settings_patch": map[string]any{"port": 8443},
		})
		if !p.NeedsApproval || len(p.Danger) == 0 || factOf(t, p, "inbounds_restarted").Value != "1" || factOf(t, p, "users_online").Value != "2" ||
			factOf(t, p, "devices_need_reissue").Value != "3" || factOf(t, p, "critical_field").Value != "/port" {
			t.Fatalf("impact/danger missing from plan: %+v", p)
		}
		mustPlanApproval(t, e, p, true)
		e.w.mu.Lock()
		e.w.profiles["prf_test"].summary.Version++
		e.w.mu.Unlock()
		out := applyError(t, s, "profile_update", p.ConfirmToken)
		if !strings.Contains(out, "changed after the plan") {
			t.Errorf("stale version was not reported: %s", out)
		}
		if stored := storedPlan(t, e, p.PlanID); stored.Status != StatusFailed || stored.OutcomeCode != "changed_since_plan" {
			t.Errorf("stored outcome: %+v", stored)
		}
	})

	t.Run("literal secret is rejected without echo", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test")
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		literal := "never-store-this-secret"
		out := mustFail(t, s, "profile_update_plan", map[string]any{
			"profile_id": "prf_test", "settings_patch": map[string]any{"tls": map[string]any{"password": literal}},
		})
		if strings.Contains(out, literal) {
			t.Errorf("secret was echoed in error: %s", out)
		}
		e.plans.mu.Lock()
		planCount := len(e.plans.byID)
		e.plans.mu.Unlock()
		if planCount != 0 {
			t.Fatal("rejected secret entered a stored plan")
		}
	})
}

func TestProfileUpdateRejectsCaseInsensitiveNameCollision(t *testing.T) {
	e := newTestEnv(t)
	seedProfile(e, "prf_test")
	seedProfile(e, "prf_other")
	e.w.mu.Lock()
	e.w.profiles["prf_other"].summary.Name = "Edge"
	e.w.mu.Unlock()
	_, secret := e.token(ProfileAdmin)
	out := mustFail(t, e.session(secret), "profile_update_plan", map[string]any{
		"profile_id": "prf_test", "name": "eDgE",
	})
	if !strings.Contains(out, "a profile with this name already exists") {
		t.Fatalf("case-insensitive name collision was not rejected: %s", out)
	}
	e.plans.mu.Lock()
	plans := len(e.plans.byID)
	e.plans.mu.Unlock()
	if plans != 0 {
		t.Fatal("colliding profile name entered a stored plan")
	}
}

func TestFakeProfileSettingsNullPreservesStoredValue(t *testing.T) {
	got := profileSettingsForTest(`{"port":443,"mode":"none"}`, `{"port":null}`, false)
	var settings map[string]any
	if err := json.Unmarshal([]byte(got), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["port"] != float64(443) {
		t.Fatalf("null overlay deleted stored value: %s", got)
	}
}

func TestProfileDeployAddsRemovalsAndDetectsGroupChanges(t *testing.T) {
	t.Run("add node and group", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test")
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_deploy", map[string]any{
			"profile_id": "prf_test", "add_node_ids": []string{nodeB}, "add_group_ids": []string{"grp_1"},
		})
		if !p.NeedsApproval || len(p.Danger) != 1 || p.Danger[0] != dangerFleet {
			t.Fatalf("profile deploy did not require approval: %+v", p)
		}
		mustPlanApproval(t, e, p, true)
		applyOf(t, s, "profile_deploy", p.ConfirmToken)
		if len(e.w.profiles["prf_test"].inbounds) != 1 || !slices.Contains(e.w.groups["grp_1"].GetProfileIds(), "prf_test") {
			t.Fatalf("add did not apply: profile=%+v group=%v", e.w.profiles["prf_test"], e.w.groups["grp_1"].GetProfileIds())
		}
	})

	t.Run("removal impact needs approval", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test", fixtureInbound("inb_test", "prf_test", nodeA, "de1"))
		seedGroupProfile(e, "grp_1", "prf_test")
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_deploy", map[string]any{
			"profile_id": "prf_test", "remove_node_ids": []string{nodeA}, "remove_group_ids": []string{"grp_1"},
		})
		if !p.NeedsApproval || factOf(t, p, "user_count").Value != "4" || factOf(t, p, "users_losing_server").Value != "3" {
			t.Fatalf("removal impact/danger missing: %+v", p)
		}
	})

	t.Run("group changes are last-write-wins and keep concurrent links", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test")
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_deploy", map[string]any{
			"profile_id": "prf_test", "add_node_ids": []string{nodeB}, "add_group_ids": []string{"grp_1"},
		})
		e.w.mu.Lock()
		e.w.groups["grp_1"].ProfileIds = append(e.w.groups["grp_1"].ProfileIds, "prf_concurrent")
		e.w.mu.Unlock()
		mustPlanApproval(t, e, p, true)
		out := applyOf(t, s, "profile_deploy", p.ConfirmToken)
		if !strings.Contains(out.Result, "Applied the requested profile placements") || strings.Contains(out.Result, "family") {
			t.Errorf("apply result: %s", out.Result)
		}
		if stored := storedPlan(t, e, p.PlanID); stored.Status != StatusApplied {
			t.Errorf("stored outcome: %+v", stored)
		}
		if len(e.w.profiles["prf_test"].inbounds) != 1 {
			t.Errorf("completed node add was not retained: %+v", e.w.profiles["prf_test"].inbounds)
		}
		if !slices.Contains(e.w.groups["grp_1"].GetProfileIds(), "prf_concurrent") || !slices.Contains(e.w.groups["grp_1"].GetProfileIds(), "prf_test") {
			t.Errorf("group update lost an existing profile link: %v", e.w.groups["grp_1"].GetProfileIds())
		}
	})

	t.Run("inbound state changes do not invalidate an approved removal", func(t *testing.T) {
		e := newTestEnv(t)
		seedProfile(e, "prf_test", fixtureInbound("inb_test", "prf_test", nodeA, "de1"))
		_, secret := e.token(ProfileAdmin)
		s := e.session(secret)
		p := planOf(t, s, "profile_deploy", map[string]any{"profile_id": "prf_test", "remove_node_ids": []string{nodeA}})
		mustPlanApproval(t, e, p, true)
		e.w.mu.Lock()
		e.w.profiles["prf_test"].inbounds[0].Enabled = false
		e.w.profiles["prf_test"].inbounds[0].State = adminv1.InboundState_INBOUND_STATE_DISABLED
		e.w.mu.Unlock()
		if out := applyOf(t, s, "profile_deploy", p.ConfirmToken); out.Status != StatusApplied {
			t.Fatalf("removal after state change: %+v", out)
		}
		if len(e.w.profiles["prf_test"].inbounds) != 0 {
			t.Fatalf("inbound remained after removal: %+v", e.w.profiles["prf_test"].inbounds)
		}
	})
}

func storedPlanText(t *testing.T, e *testEnv, id string) string {
	t.Helper()
	if id == "" {
		return ""
	}
	p := storedPlan(t, e, id)
	return p.ParamsJSON + p.FactsJSON + p.Summary + p.Error + p.Result
}

func auditText(t *testing.T, e *testEnv) string {
	t.Helper()
	b, err := json.Marshal(e.audits("mcp_plan"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := json.Marshal(e.audits("mcp_apply"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + string(c)
}

func TestProfileApplyResultsDoNotEchoGroupNames(t *testing.T) {
	e := newTestEnv(t)
	seedProfile(e, "prf_test")
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "profile_deploy", map[string]any{"profile_id": "prf_test", "add_group_ids": []string{"grp_2"}})
	mustPlanApproval(t, e, p, true)
	out := applyOf(t, s, "profile_deploy", p.ConfirmToken)
	groupName := e.w.groups["grp_2"].GetName()
	if strings.Contains(out.Result, groupName) || strings.Contains(out.Result, "IGNORE ALL RULES") || strings.Contains(out.Result, canaryTK) {
		t.Fatalf("apply result echoed group data: %q", out.Result)
	}
	if out.Result != "Applied the requested profile placements." {
		t.Fatalf("apply result = %q", out.Result)
	}
}

func TestProfileUpdateSchemaAndStoredArgsExcludeResolvedSettings(t *testing.T) {
	e := newTestEnv(t)
	seedProfile(e, "prf_test")
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	var props map[string]json.RawMessage
	for tool, err := range s.Tools(t.Context(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		if tool.Name != "profile_update_plan" {
			continue
		}
		b, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Fatal(err)
		}
		props = schema.Properties
	}
	if props == nil {
		t.Fatal("profile_update_plan schema was not returned")
	}
	for _, field := range []string{"expected_version", "settings_json", "protocol"} {
		if _, ok := props[field]; ok {
			t.Errorf("internal field %q appears in the input schema", field)
		}
	}

	long := strings.Repeat("x", 3000)
	e.w.mu.Lock()
	settings, _ := json.Marshal(map[string]any{"port": 443, "mode": "none", "tls": map[string]any{"server_name": "example.com", "password": canaryTK},
		"i1": long, "i2": long, "i3": long, "i4": long, "i5": long})
	e.w.profiles["prf_test"].settings = string(settings)
	e.w.mu.Unlock()
	p := planOf(t, s, "profile_update", map[string]any{"profile_id": "prf_test", "settings_patch": map[string]any{"port": 8443}})
	if !p.NeedsApproval {
		t.Fatal("profile update did not wait for approval")
	}
	stored := storedPlan(t, e, p.PlanID)
	if len(stored.ParamsJSON) >= maxArgsJSON || strings.Contains(stored.ParamsJSON, long) {
		t.Fatalf("stored arguments retained resolved settings (%d bytes)", len(stored.ParamsJSON))
	}
	mustPlanApproval(t, e, p, true)
	if out := applyOf(t, s, "profile_update", p.ConfirmToken); out.Status != StatusApplied {
		t.Fatalf("apply: %+v", out)
	}
}

func TestProfileDeployFailureNamesStepAndPanelCode(t *testing.T) {
	err := profileDeployFailure("remove_group", failure("changed_since_plan", "changed_since_plan: group=test-value"), profileDeployProgress{AddedNodes: 1, AddedGroups: 1})
	for _, want := range []string{"remove_group", "panel code changed_since_plan", "2 completed steps"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("deployment failure %q omits %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "test-value") {
		t.Fatalf("deployment failure exposed backend detail: %v", err)
	}
	if out := outcomeOfError(err); out.Code != "changed_since_plan" || out.Params["step"] != "remove_group" || out.Params["completed_steps"] != "2" {
		t.Fatalf("deployment failure outcome: %+v", out)
	}
}

func TestProfileDeployTimeoutKeepsCompletedStepCount(t *testing.T) {
	e := newTestEnv(t)
	seedProfile(e, "prf_test")
	id, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "profile_deploy", map[string]any{"profile_id": "prf_test", "add_node_ids": []string{nodeB}})
	mustPlanApproval(t, e, p, true)
	core := &env{cfg: Config{Plans: e.plans, Auth: e.auth}, now: e.now}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	c := &call{e: core, ctx: ctx, tokenID: id}
	_, err := core.apply(c, "profile_deploy", p.ConfirmToken, func(*call, Plan) (done, error) {
		return done{}, profileDeployFailure("add_node", apiError(context.DeadlineExceeded), profileDeployProgress{AddedNodes: 1})
	})
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("apply error = %v", err)
	}
	stored := storedPlan(t, e, p.PlanID)
	if stored.Status != StatusFailed || stored.OutcomeCode != "timeout" || !strings.Contains(stored.OutcomeParams, `"completed_steps":"1"`) {
		t.Fatalf("timeout outcome lost progress: %+v", stored)
	}
}
