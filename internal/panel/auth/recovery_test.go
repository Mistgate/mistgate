package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/securitylimit"
)

type recoveryTestLimiter struct {
	mu       sync.Mutex
	attempts int
	allowed  int
}

func (l *recoveryTestLimiter) Take(context.Context, securitylimit.Bucket, string) (securitylimit.Decision, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts++
	return securitylimit.Decision{Allowed: l.attempts <= l.allowed, RetryAfter: 30 * time.Second}, nil
}

func (*recoveryTestLimiter) Peek(context.Context, securitylimit.Window, string) (securitylimit.Decision, error) {
	return securitylimit.Decision{Allowed: true}, nil
}

func (*recoveryTestLimiter) Record(context.Context, securitylimit.Window, string) (securitylimit.Decision, error) {
	return securitylimit.Decision{Allowed: true}, nil
}

func (*recoveryTestLimiter) Reset(context.Context, securitylimit.Window, string) error { return nil }

func TestRecoveryHTTPResetsOnceAndLimitsGenericRefusals(t *testing.T) {
	limiter := &recoveryTestLimiter{allowed: 3}
	clock := time.Now().UTC()
	s, st, now := newTestServiceWithLimiter(t, limiter, &clock)
	s.SetEdge(true)
	makeAdmin(t, s, st)
	const token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := st.OfferRecoveryToken(context.Background(), hashToken(token), now.Add(RecoveryTokenTTL)); err != nil {
		t.Fatal("offer recovery token")
	}

	events := make(chan Event, 1)
	s.SetEventHook(func(e Event) { events <- e })
	post := func(recoveryToken, login string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(recoveryRequest{RecoveryToken: recoveryToken, Login: login})
		req := httptest.NewRequest(http.MethodPost, "https://example.com/test-admin/api/recover-login", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.7:443"
		response := httptest.NewRecorder()
		s.RecoveryHandler().ServeHTTP(response, req)
		return response
	}

	recovered := post(token, "alice")
	var details recoveryResponse
	if recovered.Code != http.StatusOK || json.Unmarshal(recovered.Body.Bytes(), &details) != nil || details.Login != "alice" || details.Password == "" ||
		!strings.HasPrefix(details.TOTPURI, "otpauth://totp/") || details.TOTPSecret == "" {
		t.Fatal("valid recovery token did not return new sign-in details")
	}
	used := post(token, "alice")
	wrong := post(strings.Repeat("b", 43), "alice")
	if used.Code != http.StatusForbidden || wrong.Code != http.StatusForbidden || used.Body.String() != wrong.Body.String() {
		t.Fatal("used and wrong recovery tokens did not receive the same refusal")
	}
	if limited := post(strings.Repeat("b", 43), "alice"); limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited recovery status = %d, want 429", limited.Code)
	}
	var actor, action, ip string
	if err := st.R.QueryRowContext(context.Background(), `SELECT actor, action, ip FROM audit WHERE action = 'reset_login'`).Scan(&actor, &action, &ip); err != nil || actor != "edge_recovery" || action != "reset_login" || ip != "203.0.113.7" {
		t.Fatal("recovery did not write a reset audit row")
	}
	select {
	case event := <-events:
		if event.Kind != EventPasswordChanged || event.AdminID == "" || event.Login != "alice" || event.Method != "password" || event.IP != "203.0.113.7" {
			t.Errorf("recovery event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery did not emit the password-changed event")
	}
}

func TestRecoveryWrongLoginDoesNotConsumeToken(t *testing.T) {
	limiter := &recoveryTestLimiter{allowed: 10}
	clock := time.Now().UTC()
	s, st, now := newTestServiceWithLimiter(t, limiter, &clock)
	s.SetEdge(true)
	passwordAdmin(t, s, st, "alice")
	const token = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := st.OfferRecoveryToken(context.Background(), hashToken(token), now.Add(RecoveryTokenTTL)); err != nil {
		t.Fatal("offer recovery token")
	}
	post := func(login string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(recoveryRequest{RecoveryToken: token, Login: login})
		req := httptest.NewRequest(http.MethodPost, "https://example.com/test-admin/api/recover-login", strings.NewReader(string(body)))
		req.RemoteAddr = "203.0.113.7:443"
		w := httptest.NewRecorder()
		s.RecoveryHandler().ServeHTTP(w, req)
		return w
	}
	wrong := post("admin")
	var refusal map[string]string
	if wrong.Code != http.StatusBadRequest || json.Unmarshal(wrong.Body.Bytes(), &refusal) != nil || refusal["code"] != "invalid_argument" {
		t.Fatalf("wrong login response = %d %s; want 400 invalid_argument", wrong.Code, wrong.Body.String())
	}
	if live, err := st.RecoveryTokenLive(context.Background(), hashToken(token), *now); err != nil || !live {
		t.Fatalf("wrong login spent recovery token: live=%v err=%v", live, err)
	}
	if right := post("alice"); right.Code != http.StatusOK {
		t.Fatalf("right login after wrong login = %d %s; want 200", right.Code, right.Body.String())
	}
}

func TestRecoveryEmptyLoginSelectsSinglePasskeyAdmin(t *testing.T) {
	limiter := &recoveryTestLimiter{allowed: 10}
	clock := time.Now().UTC()
	s, st, now := newTestServiceWithLimiter(t, limiter, &clock)
	s.SetEdge(true)
	makeAdmin(t, s, st)
	const token = "ccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := st.OfferRecoveryToken(context.Background(), hashToken(token), now.Add(RecoveryTokenTTL)); err != nil {
		t.Fatal("offer recovery token")
	}
	body, _ := json.Marshal(recoveryRequest{RecoveryToken: token})
	req := httptest.NewRequest(http.MethodPost, "https://example.com/test-admin/api/recover-login", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:443"
	w := httptest.NewRecorder()
	s.RecoveryHandler().ServeHTTP(w, req)
	var result recoveryResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Login != "admin" || !result.Created {
		t.Fatalf("empty-login recovery = %d %s; want one passkey admin's new default login", w.Code, w.Body.String())
	}
}

func TestRecoveryEmptyLoginUsesTheSingleAdminsCurrentLogin(t *testing.T) {
	limiter := &recoveryTestLimiter{allowed: 10}
	clock := time.Now().UTC()
	s, st, now := newTestServiceWithLimiter(t, limiter, &clock)
	s.SetEdge(true)
	passwordAdmin(t, s, st, "alice")
	const token = "fffffffffffffffffffffffffffffffffffffffffff"
	if _, err := st.OfferRecoveryToken(context.Background(), hashToken(token), now.Add(RecoveryTokenTTL)); err != nil {
		t.Fatal("offer recovery token")
	}
	body, _ := json.Marshal(recoveryRequest{RecoveryToken: token})
	req := httptest.NewRequest(http.MethodPost, "https://example.com/test-admin/api/recover-login", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:443"
	w := httptest.NewRecorder()
	s.RecoveryHandler().ServeHTTP(w, req)
	var result recoveryResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Login != "alice" || result.Created {
		t.Fatalf("empty-login recovery = %d %s; want current login alice", w.Code, w.Body.String())
	}
}

func TestRecoverySelectsUniquePasskeyOnlyOwnerAmongSeveralAdmins(t *testing.T) {
	limiter := &recoveryTestLimiter{allowed: 10}
	clock := time.Now().UTC()
	s, st, now := newTestServiceWithLimiter(t, limiter, &clock)
	s.SetEdge(true)
	owner := makeAdmin(t, s, st)
	insertAdmin(t, st, "helper")
	const token = "ggggggggggggggggggggggggggggggggggggggggggg"
	if _, err := st.OfferRecoveryToken(context.Background(), hashToken(token), now.Add(RecoveryTokenTTL)); err != nil {
		t.Fatal("offer recovery token")
	}
	body, _ := json.Marshal(recoveryRequest{RecoveryToken: token})
	req := httptest.NewRequest(http.MethodPost, "https://example.com/test-admin/api/recover-login", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:443"
	w := httptest.NewRecorder()
	s.RecoveryHandler().ServeHTTP(w, req)
	var result recoveryResponse
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Login != "admin" || !result.Created {
		t.Fatalf("empty-login recovery = %d %s; want unique owner's new default login", w.Code, w.Body.String())
	}
	cred, err := st.PasswordByLogin(context.Background(), "admin")
	if err != nil || cred.AdminID != owner.ID {
		t.Fatalf("recovered admin = %q, %v; want owner %q", cred.AdminID, err, owner.ID)
	}
}

func TestRecoveryExpiredTokenThroughHandlerIsForbidden(t *testing.T) {
	limiter := &recoveryTestLimiter{allowed: 10}
	clock := time.Now().UTC()
	s, st, now := newTestServiceWithLimiter(t, limiter, &clock)
	s.SetEdge(true)
	makeAdmin(t, s, st)
	const token = "ddddddddddddddddddddddddddddddddddddddddddd"
	if _, err := st.OfferRecoveryToken(context.Background(), hashToken(token), now.Add(-time.Second)); err != nil {
		t.Fatal("offer expired recovery token")
	}
	body, _ := json.Marshal(recoveryRequest{RecoveryToken: token, Login: "alice"})
	req := httptest.NewRequest(http.MethodPost, "https://example.com/test-admin/api/recover-login", strings.NewReader(string(body)))
	req.RemoteAddr = "203.0.113.7:443"
	w := httptest.NewRecorder()
	s.RecoveryHandler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expired-token response = %d %s; want 403", w.Code, w.Body.String())
	}
}

func TestRecoveryConcurrentPostsOnlyOneWins(t *testing.T) {
	limiter := &recoveryTestLimiter{allowed: 10}
	clock := time.Now().UTC()
	s, st, now := newTestServiceWithLimiter(t, limiter, &clock)
	s.SetEdge(true)
	makeAdmin(t, s, st)
	const token = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if _, err := st.OfferRecoveryToken(context.Background(), hashToken(token), now.Add(RecoveryTokenTTL)); err != nil {
		t.Fatal("offer recovery token")
	}
	start := make(chan struct{})
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			body, _ := json.Marshal(recoveryRequest{RecoveryToken: token, Login: "alice"})
			req := httptest.NewRequest(http.MethodPost, "https://example.com/test-admin/api/recover-login", strings.NewReader(string(body)))
			req.RemoteAddr = "203.0.113.7:443"
			w := httptest.NewRecorder()
			s.RecoveryHandler().ServeHTTP(w, req)
			statuses <- w.Code
		}()
	}
	close(start)
	wg.Wait()
	close(statuses)
	wins, refusals := 0, 0
	for status := range statuses {
		switch status {
		case http.StatusOK:
			wins++
		case http.StatusForbidden:
			refusals++
		default:
			t.Errorf("concurrent recovery status = %d; want 200 or 403", status)
		}
	}
	if wins != 1 || refusals != 1 {
		t.Fatalf("concurrent recovery results = %d winners, %d refusals; want one each", wins, refusals)
	}
}
