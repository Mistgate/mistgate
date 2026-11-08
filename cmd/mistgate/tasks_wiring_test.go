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

// A scheduled check task on a real panel (loopback only): an admin-profile token plans it, the owner approves it, an operator
// token runs it now, and the run goes through the real MCP server and the real auth with the hidden operator identity of the
// owner's scheduled tasks.
func TestScheduledTaskEndToEnd(t *testing.T) {
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
	const ownerCookie = "session-token-owner-tasks-0123456789abcdef"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_owner', 'Owner', 'owner', ?, ?)`, []byte("adm_owner"), now.Unix()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(ownerCookie))
	if err := st.CreateSession(ctx, store.Session{TokenHash: sum[:], AdminID: "adm_owner", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(p.srv.AdminListener())
	defer admin.Close()

	mkToken := func(name, profile string) string {
		t.Helper()
		secret := auth.NewTokenSecret()
		h := sha256.Sum256([]byte(secret))
		if err := st.CreateAPIToken(ctx, store.APIToken{ID: store.NewID("tok_"), Name: name, Profile: profile, Hint: secret[len(secret)-4:], RatePerMin: 600,
			CreatedBy: "adm_owner", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, h[:]); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	session := func(secret string) *sdk.ClientSession {
		t.Helper()
		c := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil)
		s, err := c.Connect(ctx, &sdk.StreamableClientTransport{
			Endpoint: admin.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerClient{secret}}, DisableStandaloneSSE: true, MaxRetries: -1,
		}, nil)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	}
	call := func(s *sdk.ClientSession, name string, args map[string]any) string {
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
		if r.IsError {
			t.Fatalf("%s: %s", name, sb.String())
		}
		return sb.String()
	}
	ad, op := session(mkToken("boss", store.ProfileAdmin)), session(mkToken("ops", store.ProfileOperator))

	var plan mcp.PlanOut
	if err := json.Unmarshal([]byte(call(ad, "task_create_plan", map[string]any{"name": "Morning check", "kind": "check", "schedule": "daily 07:30",
		"steps": []any{map[string]any{"tool": "fleet_status", "conditions": []any{map[string]any{"path": "nodes_total", "op": "gte", "value": "0"}}}}})), &plan); err != nil {
		t.Fatal(err)
	}
	if !plan.NeedsApproval {
		t.Fatalf("the plan does not wait for the owner: %+v", plan)
	}
	req, _ := http.NewRequest("POST", admin.URL+"/api/mistgate.admin.v1.ApprovalService/Approve", strings.NewReader(`{"id":"`+plan.PlanID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: ownerCookie})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("Approve: %d %s", resp.StatusCode, body)
	}
	call(ad, "task_create_apply", map[string]any{"confirm_token": plan.ConfirmToken})

	var list mcp.TasksV
	_ = json.Unmarshal([]byte(call(op, "tasks_list", map[string]any{})), &list)
	if len(list.Tasks) != 1 || list.Tasks[0].Schedule != "daily 07:30" || list.Tasks[0].NextRunUnix <= now.Unix() {
		t.Fatalf("tasks: %+v", list)
	}
	id := list.Tasks[0].ID
	call(op, "task_run_now", map[string]any{"task_id": id})
	var runs mcp.TaskRunsV
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_ = json.Unmarshal([]byte(call(op, "task_runs", map[string]any{"task_id": id})), &runs)
		if len(runs.Runs) == 1 && runs.Runs[0].Status != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(runs.Runs) != 1 || runs.Runs[0].Status != "problem" || !strings.Contains(runs.Runs[0].Summary, "nodes_total = 0") || runs.Runs[0].Actor != "ops" {
		t.Fatalf("runs: %+v", runs)
	}
	// the run acted as the owner's scheduled tasks: the hidden operator identity, not a listed token
	tok, err := st.GetAssistantToken(ctx, store.ScheduledTokenID("adm_owner"))
	if err != nil || tok.Profile != store.ProfileOperator || tok.LastUsedVia != "" && tok.LastUsedVia != "mcp" {
		t.Fatalf("scheduled identity: %+v %v", tok, err)
	}
	listed, _ := st.ListAPITokens(ctx)
	for _, l := range listed {
		if l.ID == tok.ID {
			t.Fatal("the scheduled identity is listed among the API tokens")
		}
	}
}
