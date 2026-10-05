package main

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// TelegramService is mounted behind the session with its roles: only the owner sets the bot, every admin manages their
// own chat, and a call that needs a step-up is refused without one (a session of this test has none).
func TestTelegramWiring(t *testing.T) {
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
	cookies := map[string]string{}
	for _, role := range []string{"owner", "helper", "readonly"} {
		id, token := "adm_"+role, "session-token-"+role+"-0123456789abcdef"
		if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, ?)`, id, role, role, []byte(id), now.Unix()); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(token))
		if err := st.CreateSession(ctx, store.Session{TokenHash: sum[:], AdminID: id, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		cookies[role] = token
	}
	admin := httptest.NewServer(p.srv.AdminListener())
	defer admin.Close()
	call := func(role, method, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", admin.URL+"/api/mistgate.admin.v1.TelegramService/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookies[role]})
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	for _, role := range []string{"owner", "helper", "readonly"} {
		if code, body := call(role, "GetTelegram", `{}`); code != 200 {
			t.Errorf("GetTelegram as %s: %d %s", role, code, body)
		}
	}
	// the owner gets past the role check (and meets the step-up), the others are refused as roles
	_, ownerBody := call("owner", "SetTelegramBot", `{"clear":true}`)
	for _, role := range []string{"helper", "readonly"} {
		code, body := call(role, "SetTelegramBot", `{"clear":true}`)
		if code != 403 || body == ownerBody {
			t.Errorf("SetTelegramBot as %s: %d %s", role, code, body)
		}
	}
	if strings.Contains(ownerBody, "owner access required") {
		t.Errorf("the owner was refused as a role: %s", ownerBody)
	}
	// every role may manage their own chat; none of these is a role refusal
	for _, role := range []string{"owner", "helper", "readonly"} {
		for _, m := range []string{"BeginTelegramLink", "UnlinkTelegram", "SetTelegramAlerts", "SendTelegramTest"} {
			if code, body := call(role, m, `{}`); code == 401 || (code == 403 && strings.Contains(body, "access required")) {
				t.Errorf("%s as %s: %d %s", m, role, code, body)
			}
		}
	}
	// no step-up in this session: linking is refused, nothing is stored
	if code, body := call("owner", "BeginTelegramLink", `{}`); code == 200 {
		t.Errorf("BeginTelegramLink without a step-up: %d %s", code, body)
	}
}
