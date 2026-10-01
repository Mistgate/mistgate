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

// UpdateService is mounted behind the session: everybody reads, only the owner changes anything and only with a fresh
// step-up (a session older than the step-up window is turned away by the handler, not by the role check).
func TestUpdateServiceWiring(t *testing.T) {
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
	p, err := newPanel(st, vlt, authSvc, panelOpts{in: in, panelAddr: agentAddress("", "", in.PublicURL), title: "Test", dataDir: dir}, log)
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	cookies := map[string]string{}
	for role, age := range map[string]time.Duration{"owner": 0, "helper": 0, "readonly": 0, "staleowner": time.Hour} {
		dbRole := strings.TrimPrefix(role, "stale")
		id, token := "adm_"+role, "session-token-"+role+"-0123456789abcdef"
		if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, ?)`, id, role, dbRole, []byte(id), now.Unix()); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(token))
		// CreatedAt is the last step-up of a fresh session: an hour ago it is long over
		if err := st.CreateSession(ctx, store.Session{TokenHash: sum[:], AdminID: id, CreatedAt: now.Add(-age), LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		cookies[role] = token
	}
	admin := httptest.NewServer(p.srv.AdminListener())
	defer admin.Close()
	call := func(role, method, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("POST", admin.URL+"/api/mistgate.admin.v1.UpdateService/"+method, strings.NewReader(body))
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

	for _, role := range []string{"owner", "helper", "readonly", "staleowner"} {
		code, body := call(role, "GetUpdates", `{}`)
		if code != 200 {
			t.Fatalf("GetUpdates as %s: %d %s", role, code, body)
		}
		// an unsigned test build: no release key, and an empty directory
		if !strings.Contains(body, "BUNDLE_STATUS_MISSING") || strings.Contains(body, `"hasReleaseKey":true`) {
			t.Errorf("GetUpdates as %s: %s", role, body)
		}
	}

	// the owner with a fresh step-up gets to the handler (400 and 404 are its answers); the others are turned away
	for _, c := range []struct {
		method, body string
		owner        int
	}{
		{"StartRollout", `{}`, 400},                    // no release key in this build
		{"PauseRollout", `{}`, 400},                    // rollout is not active
		{"ResumeRollout", `{}`, 400},                   // rollout is not active
		{"CancelRollout", `{}`, 400},                   // rollout is not active
		{"RollbackNode", `{"nodeId":"nod_nope"}`, 404}, // no such node
		{"RescanBundle", `{}`, 200},
	} {
		if code, body := call("owner", c.method, c.body); code != c.owner {
			t.Errorf("%s as owner: %d %s, want %d", c.method, code, body, c.owner)
		}
		for _, role := range []string{"helper", "readonly"} {
			if code, body := call(role, c.method, c.body); code != 403 || !strings.Contains(body, "your role cannot do this") {
				t.Errorf("%s as %s: %d %s, want the role check to refuse it", c.method, role, code, body)
			}
		}
		if code, body := call("staleowner", c.method, c.body); code != 403 || !strings.Contains(body, "step-up required") {
			t.Errorf("%s as an owner without a fresh step-up: %d %s", c.method, code, body)
		}
	}
	if code, body := call("owner", "StartRollout", `{}`); !strings.Contains(body, "no release key in this build") {
		t.Errorf("StartRollout: %d %s", code, body)
	}
	var audited int
	st.R.QueryRowContext(ctx, `SELECT count(*) FROM audit WHERE action = 'update_rescan'`).Scan(&audited)
	if audited != 1 {
		t.Errorf("update_rescan audit rows: %d", audited)
	}
}
