package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// The three profile tools on the real panel: real auth (grants, planning dry-run check), real access service.
func TestProfileToolsApprovalEndToEnd(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(ctx, dbPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key, err := vault.LoadKey(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	vlt, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	in := instance{
		PublicURL: "https://example.com", AdminPrefix: "/", AdminListen: "127.0.0.1:8081", RPID: "localhost",
		RPOrigins: []string{"http://localhost:8081"}, AgentSNI: newAgentSNI("example.com"), SubPrefix: newSecretPrefix(),
	}
	authSvc, err := auth.New(st, auth.Config{RPID: in.RPID, Origins: in.RPOrigins, Vault: vlt}, log)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newPanel(st, vlt, authSvc, panelOpts{in: in, panelAddr: agentAddress("", "", in.PublicURL), title: "Test", dataDir: dir, masterKey: key}, log)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ownerTok := "session-token-owner-0123456789abcdef"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_owner', 'owner', 'owner', ?, ?)`, []byte("adm_owner"), now.Unix()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(ownerTok))
	if err := st.CreateSession(ctx, store.Session{TokenHash: sum[:], AdminID: "adm_owner", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(p.srv.AdminListener())
	defer admin.Close()
	secret := auth.NewTokenSecret()
	tsum := sha256.Sum256([]byte(secret))
	if err := st.CreateAPIToken(ctx, store.APIToken{ID: store.NewID("tok_"), Name: "boss", Profile: store.ProfileAdmin, Hint: secret[len(secret)-4:], RatePerMin: 600,
		CreatedBy: "adm_owner", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, tsum[:]); err != nil {
		t.Fatal(err)
	}
	c := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	s, err := c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: admin.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerClient{secret}}, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tool := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		r, err := s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var sb strings.Builder
		for _, c := range r.Content {
			if tc, ok := c.(*sdk.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		return sb.String(), r.IsError
	}
	ok := func(name string, args map[string]any) string {
		t.Helper()
		out, isErr := tool(name, args)
		if isErr {
			t.Fatalf("%s: %s", name, out)
		}
		return out
	}
	rpc := func(cookie bool, sec, service, method, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", admin.URL+"/api/mistgate.admin.v1."+service+"/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: ownerTok})
		}
		if sec != "" {
			req.Header.Set("Authorization", "Bearer "+sec)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	approve := func(id string) {
		t.Helper()
		if code, body := rpc(true, "", "ApprovalService", "Approve", `{"id":"`+id+`"}`); code != 200 {
			t.Fatalf("Approve: %d %s", code, body)
		}
	}
	code, body := rpc(true, "", "GroupService", "CreateGroup", `{"name":"everyone"}`)
	if code != 200 {
		t.Fatalf("CreateGroup: %d %s", code, body)
	}
	groupID := decodeJSON[struct {
		Group struct{ ID string } `json:"group"`
	}](t, body).Group.ID
	for _, n := range [][2]string{{"nod_a", "de1"}, {"nod_b", "nl1"}} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', 1)`, n[0], n[1], n[1]+".example.com"); err != nil {
			t.Fatal(err)
		}
	}
	profiles := func() string {
		_, b := rpc(true, "", "ProfileService", "ListProfiles", `{}`)
		return b
	}

	// 1. create with a node and a group
	pl := decodeJSON[mcp.PlanOut](t, ok("profile_create_plan", map[string]any{"protocol": "hysteria2", "name": "Edge", "node_ids": []string{"nod_a"}, "group_ids": []string{groupID}}))
	if !pl.NeedsApproval || strings.Contains(profiles(), "Edge") {
		t.Fatalf("create plan: %+v; profiles %s", pl, profiles())
	}
	approve(pl.PlanID)
	ap := decodeJSON[mcp.ApplyOut](t, ok("profile_create_apply", map[string]any{"confirm_token": pl.ConfirmToken}))
	t.Logf("create apply: %+v", ap)
	var lp struct {
		Profiles []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			NodeCount int    `json:"nodeCount"`
		} `json:"profiles"`
	}
	_ = json.Unmarshal([]byte(profiles()), &lp)
	if len(lp.Profiles) != 1 || lp.Profiles[0].NodeCount != 1 {
		t.Fatalf("after create: %s", profiles())
	}
	prf := lp.Profiles[0].ID
	if _, b := rpc(true, "", "GroupService", "ListGroups", `{}`); !strings.Contains(b, prf) {
		t.Errorf("group lacks the profile: %s", b)
	}

	// 2. update the port
	pl = decodeJSON[mcp.PlanOut](t, ok("profile_update_plan", map[string]any{"profile_id": prf, "settings_patch": map[string]any{"port": 8443}}))
	if !pl.NeedsApproval {
		t.Fatalf("update plan: %+v", pl)
	}
	approve(pl.PlanID)
	ap = decodeJSON[mcp.ApplyOut](t, ok("profile_update_apply", map[string]any{"confirm_token": pl.ConfirmToken}))
	t.Logf("update apply: %+v", ap)
	if _, b := rpc(true, "", "ProfileService", "GetProfile", `{"profileId":"`+prf+`"}`); !strings.Contains(b, `8443`) {
		t.Errorf("port not updated: %s", b)
	}

	// 3. deploy: move to nod_b and leave the group
	pl = decodeJSON[mcp.PlanOut](t, ok("profile_deploy_plan", map[string]any{"profile_id": prf, "add_node_ids": []string{"nod_b"}, "remove_node_ids": []string{"nod_a"}, "remove_group_ids": []string{groupID}}))
	if !pl.NeedsApproval {
		t.Fatalf("deploy plan: %+v", pl)
	}
	approve(pl.PlanID)
	ap = decodeJSON[mcp.ApplyOut](t, ok("profile_deploy_apply", map[string]any{"confirm_token": pl.ConfirmToken}))
	t.Logf("deploy apply: %+v", ap)
	if _, b := rpc(true, "", "ProfileService", "GetProfile", `{"profileId":"`+prf+`"}`); !strings.Contains(b, `"nod_b"`) || strings.Contains(b, `"nod_a"`) {
		t.Errorf("deploy result: %s", b)
	}
	if _, b := rpc(true, "", "GroupService", "ListGroups", `{}`); strings.Contains(b, prf) {
		t.Errorf("group still has the profile: %s", b)
	}

	// 4. a create whose second node retires after the plan: the rollback runs under the grant and removes everything
	pl = decodeJSON[mcp.PlanOut](t, ok("profile_create_plan", map[string]any{"protocol": "hysteria2", "name": "Gone", "settings": map[string]any{"port": 9443}, "node_ids": []string{"nod_a", "nod_b"}, "group_ids": []string{groupID}}))
	approve(pl.PlanID)
	if _, err := st.W.ExecContext(ctx, `UPDATE node SET state = 'retired' WHERE id = 'nod_b'`); err != nil {
		t.Fatal(err)
	}
	out, isErr := tool("profile_create_apply", map[string]any{"confirm_token": pl.ConfirmToken})
	t.Logf("rollback apply: err=%v %s", isErr, out)
	if !isErr || strings.Contains(out, "rollback incomplete") || strings.Contains(profiles(), "Gone") {
		t.Errorf("rollback: %v %s; profiles %s", isErr, out, profiles())
	}
	if _, b := rpc(true, "", "GroupService", "ListGroups", `{}`); strings.Contains(b, "prf_") {
		t.Errorf("group after rollback: %s", b)
	}
}
