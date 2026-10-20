//go:build js && wasm

package bridge

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall/js"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestRecoveryBridgeResetsOnceRejectsWrongTokensAndLimitsAttempts(t *testing.T) {
	stateMu.Lock()
	previous := state
	state = nil
	stateMu.Unlock()
	t.Cleanup(func() {
		stateMu.Lock()
		current := state
		state = previous
		stateMu.Unlock()
		if current != nil && current != previous {
			_ = current.store.Close()
		}
	})

	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	seed, err := store.OpenD1(context.Background(), binding)
	if err != nil {
		t.Fatal("open D1 for recovery test")
	}
	for _, query := range []string{
		`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_alice', 'Alice', 'owner', x'01', 1)`,
		`INSERT INTO passkey (id, admin_id, credential_id, public_key, created_at) VALUES ('pk_alice', 'adm_alice', x'02', x'03', 1)`,
		`INSERT INTO session (token_hash, admin_id, created_at, last_seen_at, expires_at) VALUES (x'04', 'adm_alice', 1, 1, 9999999999)`,
	} {
		if _, err := seed.W.ExecContext(context.Background(), query); err != nil {
			_ = seed.Close()
			t.Fatal("seed D1 recovery account")
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal("close D1 seed connection")
	}

	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sum := sha256.Sum256([]byte(token))
	ask := js.FuncOf(func(js.Value, []js.Value) any { return nil })
	closeNode := js.FuncOf(func(js.Value, []js.Value) any { return nil })
	poke := js.FuncOf(func(js.Value, []js.Value) any { return nil })
	defer ask.Release()
	defer closeNode.Release()
	defer poke.Release()
	nodeLink := js.Global().Get("Object").New()
	nodeLink.Set("ask", ask)
	nodeLink.Set("close", closeNode)
	nodeLink.Set("poke", poke)

	attempts := 0
	limit := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 && args[0].Get("operation").String() == "take" {
			attempts++
		}
		decision := js.Global().Get("Object").New()
		decision.Set("ok", attempts <= 4)
		decision.Set("retryAfterMs", 30000)
		remaining := 4 - attempts
		if remaining < 0 {
			remaining = 0
		}
		decision.Set("remaining", remaining)
		decision.Set("first", attempts == 1)
		return js.Global().Get("Promise").Call("resolve", decision)
	})
	defer limit.Release()

	options := js.Global().Get("Object").New()
	options.Set("d1", binding)
	options.Set("masterKey", strings.Repeat("ab", 32))
	options.Set("publicURL", "https://example.com")
	options.Set("adminPrefix", "/test-admin/")
	options.Set("recoveryTokenSHA256", hex.EncodeToString(sum[:]))
	options.Set("nodeLink", nodeLink)
	options.Set("limit", limit)
	logs := captureConsoleLog(t)
	if err := initPanel(options); err != nil {
		t.Fatal("initialize edge panel for recovery")
	}
	for _, line := range *logs {
		if strings.Contains(line, token) || strings.Contains(line, hex.EncodeToString(sum[:])) || strings.Contains(line, "/test-admin/") {
			t.Fatal("recovery offer log exposed recovery data")
		}
	}

	post := func(recoveryToken string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"recoveryToken": recoveryToken, "login": "alice"})
		req := httptest.NewRequest(http.MethodPost, "https://example.com/test-admin/api/recover-login", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.7:443"
		response := httptest.NewRecorder()
		stateMu.RLock()
		current := state
		stateMu.RUnlock()
		current.handler.ServeHTTP(response, req)
		return response
	}

	recovered := post(token)
	if recovered.Code != http.StatusOK {
		t.Fatalf("recovery request status = %d, want 200", recovered.Code)
	}
	var result struct {
		Login         string `json:"login"`
		Password      string `json:"password"`
		TOTPURI       string `json:"totpUri"`
		TOTPSecret    string `json:"totpSecret"`
		SessionsEnded int    `json:"sessionsEnded"`
	}
	if err := json.Unmarshal(recovered.Body.Bytes(), &result); err != nil || result.Login != "alice" || result.Password == "" ||
		!strings.HasPrefix(result.TOTPURI, "otpauth://totp/") || result.TOTPSecret == "" || result.SessionsEnded != 1 {
		t.Fatal("recovery response did not return the one-time reset details")
	}

	used := post(token)
	wrong := post(strings.Repeat("b", 43))
	if used.Code != http.StatusForbidden || wrong.Code != http.StatusForbidden || !bytes.Equal(used.Body.Bytes(), wrong.Body.Bytes()) {
		t.Fatal("used and wrong recovery tokens did not receive the same refusal")
	}
	if got := post(strings.Repeat("b", 43)); got.Code != http.StatusForbidden {
		t.Fatalf("wrong-token retry status = %d, want 403", got.Code)
	}
	if got := post(strings.Repeat("b", 43)); got.Code != http.StatusTooManyRequests {
		t.Fatalf("limited wrong-token status = %d, want 429", got.Code)
	}

	current := state
	var actor, action string
	if err := current.store.R.QueryRowContext(context.Background(), `SELECT actor, action FROM audit WHERE action = 'reset_login'`).Scan(&actor, &action); err != nil || actor != "edge_recovery" || action != "reset_login" {
		t.Fatal("recovery did not write the reset audit row")
	}
	var passkeys, sessions int
	if err := current.store.R.QueryRowContext(context.Background(), `SELECT count(*) FROM passkey WHERE admin_id = 'adm_alice'`).Scan(&passkeys); err != nil || passkeys != 1 {
		t.Fatal("recovery did not preserve the owner's passkey")
	}
	if err := current.store.R.QueryRowContext(context.Background(), `SELECT count(*) FROM session WHERE admin_id = 'adm_alice'`).Scan(&sessions); err != nil || sessions != 0 {
		t.Fatal("recovery did not end the owner's sessions")
	}
	usedLogs := captureConsoleLog(t)
	if err := offerRecovery(context.Background(), current.store, initOptions{recoveryTokenHash: sum[:]}, time.Now()); err != nil {
		t.Fatalf("re-offering a used recovery token: %v", err)
	}
	if len(*usedLogs) != 0 {
		t.Fatalf("used recovery token logged a warning: %v", *usedLogs)
	}
}

func TestRecoveryOfferFailureDoesNotFailAdminInitialization(t *testing.T) {
	stateMu.Lock()
	previous := state
	state = nil
	stateMu.Unlock()
	t.Cleanup(func() {
		stateMu.Lock()
		current := state
		state = previous
		stateMu.Unlock()
		if current != nil && current != previous {
			_ = current.store.Close()
		}
	})

	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	seed, err := store.OpenD1(context.Background(), binding)
	if err != nil {
		t.Fatal("open D1 for recovery-offer failure test")
	}
	for _, query := range []string{
		`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_alice', 'Alice', 'owner', x'01', 1)`,
		`INSERT INTO passkey (id, admin_id, credential_id, public_key, created_at) VALUES ('pk_alice', 'adm_alice', x'02', x'03', 1)`,
		`CREATE TRIGGER reject_recovery_offer BEFORE INSERT ON recovery_token BEGIN SELECT RAISE(ABORT, 'injected recovery offer failure'); END`,
	} {
		if _, err := seed.W.ExecContext(context.Background(), query); err != nil {
			_ = seed.Close()
			t.Fatalf("seed recovery-offer failure: %v", err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal("close D1 seed connection")
	}

	tokenHash := sha256.Sum256([]byte("failed-recovery-offer"))
	ask := js.FuncOf(func(js.Value, []js.Value) any { return nil })
	closeNode := js.FuncOf(func(js.Value, []js.Value) any { return nil })
	poke := js.FuncOf(func(js.Value, []js.Value) any { return nil })
	defer ask.Release()
	defer closeNode.Release()
	defer poke.Release()
	nodeLink := js.Global().Get("Object").New()
	nodeLink.Set("ask", ask)
	nodeLink.Set("close", closeNode)
	nodeLink.Set("poke", poke)
	options := js.Global().Get("Object").New()
	options.Set("d1", binding)
	options.Set("masterKey", strings.Repeat("ab", 32))
	options.Set("publicURL", "https://example.com")
	options.Set("adminPrefix", "/test-admin/")
	options.Set("recoveryTokenSHA256", hex.EncodeToString(tokenHash[:]))
	options.Set("nodeLink", nodeLink)
	limit := js.FuncOf(func(js.Value, []js.Value) any {
		decision := js.Global().Get("Object").New()
		decision.Set("ok", true)
		decision.Set("retryAfterMs", 0)
		decision.Set("remaining", 4)
		decision.Set("first", true)
		return js.Global().Get("Promise").Call("resolve", decision)
	})
	defer limit.Release()
	options.Set("limit", limit)

	errorsLogged := captureConsoleError(t)
	if err := initPanel(options); err != nil {
		t.Fatalf("recovery offer failure stopped admin initialization: %v", err)
	}
	stateMu.RLock()
	current := state
	stateMu.RUnlock()
	if current == nil || current.handler == nil {
		t.Fatal("admin handler was unavailable after recovery offer failed")
	}
	if len(*errorsLogged) == 0 || !strings.Contains(strings.Join(*errorsLogged, "\n"), "could not offer recovery token") {
		t.Fatalf("recovery offer failure was not logged: %v", *errorsLogged)
	}
}

func captureConsoleError(t *testing.T) *[]string {
	t.Helper()
	console := js.Global().Get("console")
	original := console.Get("error")
	var lines []string
	capture := js.FuncOf(func(_ js.Value, args []js.Value) any {
		parts := make([]string, len(args))
		for i, arg := range args {
			parts[i] = arg.String()
		}
		lines = append(lines, strings.Join(parts, " "))
		return nil
	})
	console.Set("error", capture)
	t.Cleanup(func() {
		console.Set("error", original)
		capture.Release()
	})
	return &lines
}

func TestParseRecoveryTokenHash(t *testing.T) {
	valid := sha256.Sum256([]byte("recovery-token"))
	if got, err := parseRecoveryTokenHash(hex.EncodeToString(valid[:])); err != nil || !bytes.Equal(got, valid[:]) {
		t.Fatal("valid recovery hash was rejected")
	}
	bad := []string{"abc", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64), strings.ToUpper(hex.EncodeToString(valid[:]))}
	for i, raw := range bad {
		if _, err := parseRecoveryTokenHash(raw); err == nil {
			t.Errorf("invalid recovery hash case %d was accepted", i)
		}
	}
}
