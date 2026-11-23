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
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// backup_status on the real auth, the real backup service and the real store: an admin-profile token and the owner's assistant read
// the status of the backups and nothing that points at the storage; nothing else of the backups is open to a token.
func TestBackupStatusOnRealAuth(t *testing.T) {
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

	// what points at the storage, in the settings the panel keeps
	storage := []string{"canaryaccount", "canary-bucket", "CANARYACCESSKEYID", "age1canaryrecipient", "CANARYSECRETBYTES"}
	now := time.Now()
	if err := st.SavePanelBackupSettings(ctx, store.PanelBackupSettings{
		AccountID: "canaryaccount0123456789abcdef0123", Jurisdiction: "eu", Bucket: "canary-bucket", AccessKeyID: "CANARYACCESSKEYID",
		AgeRecipient: "age1canaryrecipientxxxxxxxxxxxxxxxxxxxxxxxxxxxx", SecretAccessKey: []byte("CANARYSECRETBYTES"),
		Enabled: true, IntervalHours: 12, RetentionDays: 9,
	}, now); err != nil {
		t.Fatal(err)
	}

	cookies := map[string]string{}
	for _, role := range []string{store.RoleOwner, store.RoleHelper} {
		id, cookie := "adm_"+role, "session-token-"+role+"-0123456789abcdef"
		if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, ?)`, id, role, role, []byte(id), now.Unix()); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(cookie))
		if err := st.CreateSession(ctx, store.Session{TokenHash: sum[:], AdminID: id, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		cookies[role] = cookie
	}
	admin := httptest.NewServer(p.srv.AdminListener())
	defer admin.Close()

	mkToken := func(name, profile string) string {
		t.Helper()
		secret := auth.NewTokenSecret()
		sum := sha256.Sum256([]byte(secret))
		if err := st.CreateAPIToken(ctx, store.APIToken{
			ID: store.NewID("tok_"), Name: name, Profile: profile, Hint: secret[len(secret)-4:], RatePerMin: 600, CreatedBy: "adm_owner",
			CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
		}, sum[:]); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	adSecret, opSecret, roSecret := mkToken("boss", store.ProfileAdmin), mkToken("ops", store.ProfileOperator), mkToken("looker", store.ProfileReadonly)
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
	post := func(path, cookie, secret string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", admin.URL+"/api"+path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
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
		return resp.StatusCode, string(b)
	}
	// the answer carries the status and nothing of the storage: a canary found anywhere in the body, key names included, fails
	statusOnly := func(what, body string) {
		t.Helper()
		for _, c := range storage {
			if strings.Contains(strings.ToLower(body), strings.ToLower(c)) {
				t.Errorf("%s: the answer carries %q: %s", what, c, body)
			}
		}
		for _, name := range []string{"account", "bucket", "access_key", "recipient", "has_secret", "jurisdiction", "secret", "endpoint", "url"} {
			if strings.Contains(strings.ToLower(body), name) {
				t.Errorf("%s: the answer names %q: %s", what, name, body)
			}
		}
	}

	// the admin-profile token reads it through the MCP
	for _, profile := range []string{store.ProfileOperator, store.ProfileReadonly} {
		s := session(map[string]string{store.ProfileOperator: opSecret, store.ProfileReadonly: roSecret}[profile])
		for tool, err := range s.Tools(ctx, nil) {
			if err != nil {
				t.Fatal(err)
			}
			if tool.Name == "backup_status" {
				t.Errorf("the %s profile sees backup_status", profile)
			}
		}
		if _, err := s.CallTool(ctx, &sdk.CallToolParams{Name: "backup_status", Arguments: map[string]any{}}); err == nil {
			t.Errorf("the %s profile called backup_status", profile)
		}
	}
	r, err := session(adSecret).CallTool(ctx, &sdk.CallToolParams{Name: "backup_status", Arguments: map[string]any{}})
	if err != nil || r.IsError {
		t.Fatalf("backup_status with an admin token: %v %+v", err, r)
	}
	out := r.Content[0].(*sdk.TextContent).Text
	var status struct {
		Enabled       bool   `json:"enabled"`
		Running       bool   `json:"running"`
		IntervalHours int32  `json:"interval_hours"`
		RetentionDays int32  `json:"retention_days"`
		LastError     string `json:"last_error_code"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil || !status.Enabled || status.Running || status.IntervalHours != 12 || status.RetentionDays != 9 {
		t.Fatalf("backup_status: %v %s", err, out)
	}
	statusOnly("backup_status", out)

	// the procedure under it, over /api: the status for the admin profile, nothing for the others
	code, body := post("/mistgate.admin.v1.BackupService/GetBackupSettings", "", adSecret)
	if code != 200 || !strings.Contains(body, `"intervalHours":12`) {
		t.Fatalf("GetBackupSettings with an admin token: %d %s", code, body)
	}
	statusOnly("GetBackupSettings with an admin token", body)
	for name, secret := range map[string]string{"operator": opSecret, "readonly": roSecret} {
		if code, body := post("/mistgate.admin.v1.BackupService/GetBackupSettings", "", secret); code != 403 {
			t.Errorf("GetBackupSettings with a %s token: %d %s", name, code, body)
		}
	}
	// the owner's own session still sees the settings of its storage
	if code, body := post("/mistgate.admin.v1.BackupService/GetBackupSettings", cookies[store.RoleOwner], ""); code != 200 || !strings.Contains(body, "canary-bucket") || !strings.Contains(body, "CANARYACCESSKEYID") {
		t.Errorf("GetBackupSettings with the owner's session: %d %s", code, body)
	}
	if code, body := post("/mistgate.admin.v1.BackupService/GetBackupSettings", cookies[store.RoleHelper], ""); code != 403 {
		t.Errorf("GetBackupSettings with a helper's session: %d %s", code, body)
	}

	// the owner's assistant: the in-process call of an assistant turn, with its hidden identity, on the same handler chain
	if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_settings(id,enabled,audience) VALUES(1,1,3) ON CONFLICT(id) DO UPDATE SET enabled=1, audience=3`); err != nil {
		t.Fatal(err)
	}
	assistant := func(adminID, profile string) (int, string) {
		t.Helper()
		hidden, err := st.EnsureAssistantToken(ctx, adminID, profile, now)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "http://panel.invalid/api/mistgate.admin.v1.BackupService/GetBackupSettings", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req = req.WithContext(auth.WithAssistant(req.Context(), hidden.ID, profile, adminID, adminID))
		rec := httptest.NewRecorder()
		p.srv.AdminListener().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	code, body = assistant("adm_"+store.RoleOwner, store.ProfileAdmin)
	if code != 200 || !strings.Contains(body, `"intervalHours":12`) {
		t.Fatalf("GetBackupSettings for the owner's assistant: %d %s", code, body)
	}
	statusOnly("GetBackupSettings for the owner's assistant", body)
	// a helper's assistant has the operator profile and no backup status
	if code, body := assistant("adm_"+store.RoleHelper, store.ProfileOperator); code != 403 {
		t.Errorf("GetBackupSettings for a helper's assistant: %d %s", code, body)
	}

	// nothing else of the backups is open to a token, whatever its profile
	for _, method := range []string{"UpdateBackupSettings", "TestBackupStorage", "CreateBackup", "ListBackups", "PrepareRestore", "ConfirmRestore", "ListKeptData", "DeleteKeptData", "GetRestoreResult"} {
		for name, secret := range map[string]string{"admin": adSecret, "operator": opSecret, "readonly": roSecret} {
			if code, body := post("/mistgate.admin.v1.BackupService/"+method, "", secret); code != 403 {
				t.Errorf("%s with a %s token: %d %s", method, name, code, body)
			}
		}
	}
	for _, path := range []string{auth.BackupDownloadProcedure, auth.BackupUploadProcedure} {
		if code, body := post(path, "", adSecret); code != 403 {
			t.Errorf("%s with an admin token: %d %s", path, code, body)
		}
	}
}
