package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mistgate/mistgate/internal/logring"
	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// The panel's logs, level, retention and /metrics on a real panel: real auth, real services, the process logger's ring. What
// an owner, an admin token and a stranger can reach, what a request id does, and that nothing secret leaves.
func TestObservabilityOnARealPanel(t *testing.T) {
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
	log, ring, level := logring.New(io.Discard, "text", slog.LevelInfo, 500)
	in := instance{
		PublicURL: "https://example.com", AdminPrefix: "/", AdminListen: "127.0.0.1:8081", RPID: "localhost",
		RPOrigins: []string{"http://localhost:8081"}, AgentSNI: "k4m9x2q7.node.example.com", SubPrefix: newSecretPrefix(),
	}
	authSvc, err := auth.New(st, auth.Config{RPID: in.RPID, Origins: in.RPOrigins, Vault: vlt}, log)
	if err != nil {
		t.Fatal(err)
	}
	p, err := newPanel(st, vlt, authSvc, panelOpts{in: in, panelAddr: agentAddress("", "", in.PublicURL), title: "Test", dataDir: dir, masterKey: key,
		ops: &app.OpsConfig{Ring: ring, Level: level, DefaultLevel: slog.LevelInfo, Format: "text"}}, log)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_o', 'owner', 'owner', ?, ?)`, []byte("adm_o"), now.Unix()); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("session-token-owner-0123456789abcdef"))
	if err := st.CreateSession(ctx, store.Session{TokenHash: sum[:], AdminID: "adm_o", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	admin := httptest.NewServer(p.srv.AdminListener())
	defer admin.Close()

	mkToken := func(name, profile string) string {
		secret := auth.NewTokenSecret()
		s := sha256.Sum256([]byte(secret))
		if err := st.CreateAPIToken(ctx, store.APIToken{ID: store.NewID("tok_"), Name: name, Profile: profile, Hint: secret[len(secret)-4:], RatePerMin: 600,
			CreatedBy: "adm_o", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}, s[:]); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	adSecret, roSecret := mkToken("boss", store.ProfileAdmin), mkToken("looker", store.ProfileReadonly)
	call := func(cookie bool, secret, method, body string) (int, string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest("POST", admin.URL+"/api/mistgate.ops.v1.OpsService/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "session-token-owner-0123456789abcdef"})
		}
		if secret != "" {
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header
	}

	// the canaries go through the process logger
	log.Warn("fleet: dial failed to 203.0.113.77:443", "code", "dial_failed", "err", "lookup k4m9x2q7.node.example.com", "user", "canary-person")

	// the owner reads the logs; the response names the request; a stranger gets no header
	code, body, hdr := call(true, "", "GetPanelLogs", `{"minLevel":"warn"}`)
	rid := hdr.Get("X-Request-Id")
	if code != 200 || !regexp.MustCompile(`^[0-9a-f]{8}$`).MatchString(rid) {
		t.Fatalf("owner GetPanelLogs: %d %s, X-Request-Id %q", code, body, rid)
	}
	for _, leak := range []string{"203.0.113.77", "k4m9x2q7", "canary-person"} {
		if strings.Contains(body, leak) {
			t.Errorf("GetPanelLogs leaked %q: %s", leak, body)
		}
	}
	if !strings.Contains(body, "dial_failed") || !strings.Contains(body, "<ip>") {
		t.Errorf("GetPanelLogs: %s", body)
	}
	if code, _, hdr := call(false, "", "GetPanelLogs", `{}`); code == 200 || hdr.Get("X-Request-Id") != "" {
		t.Errorf("a stranger: %d, X-Request-Id %q", code, hdr.Get("X-Request-Id"))
	}
	// a readonly token reaches nothing of it; an admin token reads, may set the level, but not change retention or export
	for _, m := range []string{"GetPanelLogs", "GetRetention", "SetLogLevel", "ExportData", "GetMetricsSettings"} {
		if code, _, _ := call(false, roSecret, m, `{}`); code != 403 {
			t.Errorf("readonly token %s: %d", m, code)
		}
	}
	if code, _, _ := call(false, adSecret, "GetPanelLogs", `{}`); code != 200 {
		t.Errorf("admin token GetPanelLogs: %d", code)
	}
	for _, m := range []string{"SetRetention", "ExportData", "SetMetricsSettings", "GetMetricsSettings"} {
		if code, _, _ := call(false, adSecret, m, `{"values":{"audit_days":100}}`); code != 403 {
			t.Errorf("admin token %s over the API: %d", m, code)
		}
	}
	// the panel's resources and the fleet's load are reads for everybody, a readonly token included; the admin API calls made
	// above are counted by the middleware
	if code, body, _ := call(true, "", "GetPanelResources", `{}`); code != 200 || !strings.Contains(body, `"requests":"`) || !strings.Contains(body, `"pageCount":"`) {
		t.Errorf("owner GetPanelResources: %d %s", code, body)
	}
	for _, m := range []string{"GetPanelResources", "GetFleetLoad"} {
		if code, body, _ := call(false, roSecret, m, `{"hours":24}`); code != 200 || strings.Contains(body, dir) {
			t.Errorf("readonly token %s: %d %s", m, code, body)
		}
	}
	if code, _, _ := call(false, "", "GetFleetLoad", `{}`); code == 200 {
		t.Error("a stranger read the fleet's load")
	}

	// the request id is on the audit row of the change
	if code, body, hdr := call(true, "", "SetLogLevel", `{"level":"debug"}`); code != 200 {
		t.Fatalf("SetLogLevel: %d %s", code, body)
	} else if rid = hdr.Get("X-Request-Id"); rid == "" {
		t.Fatal("no request id on SetLogLevel")
	}
	rows, err := st.ListAudit(ctx, "", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range rows {
		if a.Action == "log_level_set" && strings.Contains(a.Params, `"req":"`+rid+`"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("no log_level_set audit row with req %s: %+v", rid, rows)
	}
	if code, body, _ := call(true, "", "SetLogLevel", `{"level":"info"}`); code != 200 {
		t.Fatalf("SetLogLevel back: %d %s", code, body)
	}

	// MCP: the admin profile reads the logs and plans a level change; retention changes need the owner
	c := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	s, err := c.Connect(ctx, &sdk.StreamableClientTransport{Endpoint: admin.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerClient{adSecret}},
		DisableStandaloneSSE: true, MaxRetries: -1}, nil)
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
	// the load reads reach the real services through MCP
	if out := mustTool(t, tool, "panel_resources", map[string]any{}); !strings.Contains(out, `"page_count":`) || !strings.Contains(out, `"admin_api":`) {
		t.Errorf("panel_resources: %s", out)
	}
	if out := mustTool(t, tool, "capacity_report", map[string]any{"hours": 24}); !strings.Contains(out, `"hours":24`) || !strings.Contains(out, `"summary":`) {
		t.Errorf("capacity_report: %s", out)
	}
	out, isErr := tool("panel_logs", map[string]any{"min_level": "warn", "code": "dial_failed"})
	if isErr || !strings.Contains(out, `"dial_failed"`) || strings.Contains(out, "203.0.113.77") || strings.Contains(out, "k4m9x2q7") || strings.Contains(out, "canary-person") {
		t.Errorf("panel_logs: %v %s", isErr, out)
	}
	plan := decodeJSON[mcp.PlanOut](t, mustTool(t, tool, "log_level_plan", map[string]any{"level": "debug"}))
	if ap := decodeJSON[mcp.ApplyOut](t, mustTool(t, tool, "log_level_apply", map[string]any{"confirm_token": plan.ConfirmToken})); ap.Status != "applied" {
		t.Errorf("log_level_apply: %+v", ap)
	}
	if level.Level() != slog.LevelDebug {
		t.Errorf("the level after the MCP apply: %v", level.Level())
	}
	level.Set(slog.LevelInfo)
	if out, isErr := tool("retention_plan", map[string]any{"values": map[string]any{"audit_days": 3}}); !isErr || !strings.Contains(out, "audit_days") {
		t.Errorf("retention_plan out of range: %v %s", isErr, out)
	}
	rp := decodeJSON[mcp.PlanOut](t, mustTool(t, tool, "retention_plan", map[string]any{"values": map[string]any{"audit_days": 100}}))
	if out, isErr := tool("retention_apply", map[string]any{"confirm_token": rp.ConfirmToken}); !isErr || !strings.Contains(out, "waiting for the owner") {
		t.Errorf("retention_apply before the owner approved: %v %s", isErr, out)
	}
	if out := mustTool(t, tool, "retention_status", map[string]any{}); !strings.Contains(out, `"audit_days"`) || !strings.Contains(out, `"default":400`) {
		t.Errorf("retention_status: %s", out)
	}

	// the owner changes retention in the UI (step-up is fresh: the session was just made)
	if code, body, _ := call(true, "", "SetRetention", `{"values":{"audit_days":100}}`); code != 200 || !strings.Contains(body, `"value":100`) {
		t.Fatalf("SetRetention: %d %s", code, body)
	}

	// The debug capture of a node: closed to a readonly token, an admin token may start and read, never stop, and a node
	// that is not connected refuses it cleanly. What a capture holds is cleaned again on the way out, over the API and MCP.
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, state, address, created_at) VALUES ('nod_dbg', 'de1', 'active', '203.0.113.10', 1)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{"StartDebugCapture", "StopDebugCapture", "ListDebugCaptures", "GetDebugCapture"} {
		if code, _, _ := call(false, roSecret, m, `{"nodeId":"nod_dbg","captureId":"req_x"}`); code != 403 {
			t.Errorf("readonly token %s: %d", m, code)
		}
	}
	if code, _, _ := call(false, adSecret, "StopDebugCapture", `{"captureId":"req_x"}`); code != 403 {
		t.Errorf("admin token StopDebugCapture over the API: %d (a stop is the owner's)", code)
	}
	if code, body, _ := call(false, adSecret, "StartDebugCapture", `{"nodeId":"nod_dbg","durationS":60}`); code == 403 || !strings.Contains(body, "node_offline") {
		t.Errorf("admin token StartDebugCapture: %d %s (allowed, and refused by the fleet: no agent)", code, body)
	}
	if code, body, _ := call(true, "", "StartDebugCapture", `{"nodeId":"nod_dbg","durationS":60}`); code != 400 && code != 412 || !strings.Contains(body, "node_offline") {
		t.Errorf("owner StartDebugCapture on a node with no agent: %d %s", code, body)
	}
	var running int
	if err := st.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM debug_capture`).Scan(&running); err != nil || running != 0 {
		t.Errorf("a refused start left %d captures (%v)", running, err)
	}
	if out, isErr := tool("node_debug_capture_plan", map[string]any{"node": "de1", "duration_s": 5000}); !isErr || !strings.Contains(out, "900") {
		t.Errorf("node_debug_capture_plan with 5000 s: %v %s", isErr, out)
	}
	dp := decodeJSON[mcp.PlanOut](t, mustTool(t, tool, "node_debug_capture_plan", map[string]any{"node": "de1", "sources": []string{"xray"}, "duration_s": 60, "reason": "tls errors"}))
	if dp.NeedsApproval || !strings.Contains(mustJSON(t, dp.Facts), "xray") {
		t.Errorf("plan = %+v", dp)
	}
	if out, isErr := tool("node_debug_capture_apply", map[string]any{"confirm_token": dp.ConfirmToken}); !isErr || !strings.Contains(out, "offline") {
		t.Errorf("node_debug_capture_apply with no agent: %v %s", isErr, out)
	}
	canaries := []string{"203.0.113.88", "k4m9x2q7", "canary-person", "Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyMTI"}
	for _, q := range []string{
		`INSERT INTO debug_capture (id, node_id, actor, level, sources, duration_s, started_at, ends_at, state, end_reason, records) VALUES ('req_dbg1', 'nod_dbg', 'adm_o', 'debug', 'xray', 60, 1, 61, 'done', 'expired', 2)`,
		`INSERT INTO debug_capture_line VALUES ('req_dbg1', 1, 1000, 0, 'xray', 'tls to 203.0.113.88 sni k4m9x2q7.node.example.com', '{"user":"canary-person","err":"key Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyMTI","code":"tls"}')`,
		`INSERT INTO debug_capture_line VALUES ('req_dbg1', 2, 1001, 1, 'agent', 'second', '{}')`,
	} {
		if _, err := st.W.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	list := mustTool(t, tool, "node_debug_logs", map[string]any{})
	if !strings.Contains(list, "req_dbg1") {
		t.Errorf("node_debug_logs list: %s", list)
	}
	logs := mustTool(t, tool, "node_debug_logs", map[string]any{"capture_id": "req_dbg1", "source": "xray"})
	if !strings.Contains(logs, `"tls"`) || strings.Contains(logs, "second") {
		t.Errorf("node_debug_logs: %s", logs)
	}
	for _, leak := range canaries {
		if strings.Contains(logs, leak) || strings.Contains(list, leak) {
			t.Errorf("node_debug_logs leaked %q: %s", leak, logs)
		}
	}
	if code, body, _ := call(false, adSecret, "GetDebugCapture", `{"captureId":"req_dbg1"}`); code != 200 || strings.Contains(body, "203.0.113.88") || strings.Contains(body, "k4m9x2q7") {
		t.Errorf("admin token GetDebugCapture: %d %s", code, body)
	}

	// /metrics: unknown to a stranger while off; the owner turns it on and gets a token once
	metrics := func(authz string) (int, string) {
		req, _ := http.NewRequest("GET", admin.URL+"/metrics", nil)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := metrics(""); strings.Contains(body, "mistgate_") || code == 401 {
		t.Errorf("/metrics while off: %d %.100s", code, body)
	}
	code, body, _ = call(true, "", "SetMetricsSettings", `{"enabled":true}`)
	token := decodeJSON[struct{ Token string }](t, body).Token
	if code != 200 || !strings.HasPrefix(token, "mgm_") {
		t.Fatalf("SetMetricsSettings: %d %s", code, body)
	}
	if code, body := metrics(""); code != 401 || strings.Contains(body, "mistgate_") {
		t.Errorf("/metrics without a token: %d", code)
	}
	if code, _ := metrics("Bearer " + adSecret); code != 401 {
		t.Errorf("/metrics with an API token: %d", code)
	}
	code, body = metrics("Bearer " + token)
	if code != 200 || !strings.Contains(body, "mistgate_nodes{status=\"online\"} 0") || !strings.Contains(body, "mistgate_alerts_active") || !strings.HasSuffix(body, "# EOF\n") {
		t.Errorf("/metrics: %d\n%s", code, body)
	}
	var j any
	if json.Unmarshal([]byte(body), &j) == nil {
		t.Error("metrics are JSON")
	}
}

func mustTool(t *testing.T, tool func(string, map[string]any) (string, bool), name string, args map[string]any) string {
	t.Helper()
	out, isErr := tool(name, args)
	if isErr {
		t.Fatalf("%s: %s", name, out)
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
