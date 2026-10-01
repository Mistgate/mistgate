package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/descope/virtualwebauthn"
	"google.golang.org/protobuf/encoding/prototext"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	cfSecret  = "0x4AAAAAAA-test-secret-key"
	cfSiteKey = "0x4AAAAAAA-test_site-key"
)

// fakeCF is a stand-in for Cloudflare's siteverify: a token that starts with "ok-" is valid exactly once.
type fakeCF struct {
	srv *httptest.Server

	mu     sync.Mutex
	calls  []url.Values
	used   map[string]bool
	status int           // 0 = 200
	body   string        // "" = the normal answer
	delay  time.Duration // answer after this long
}

func newFakeCF(t *testing.T) *fakeCF {
	t.Helper()
	f := &fakeCF{used: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		r.ParseForm()
		f.mu.Lock()
		f.calls = append(f.calls, r.PostForm)
		status, body, delay := f.status, f.body, f.delay
		tok := r.PostForm.Get("response")
		codes := []string{} // what Cloudflare says about a refusal
		switch {
		case r.PostForm.Get("secret") != cfSecret:
			codes = append(codes, "invalid-input-secret")
		case !strings.HasPrefix(tok, "ok-"):
			codes = append(codes, "invalid-input-response")
		case f.used[tok]:
			codes = append(codes, "timeout-or-duplicate")
		}
		f.used[tok] = true
		f.mu.Unlock()
		time.Sleep(delay)
		if status != 0 {
			w.WriteHeader(status)
		}
		if body != "" {
			w.Write([]byte(body))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": len(codes) == 0, "error-codes": codes})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCF) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// turnstileOn enables the captcha through the owner's RPC, the way the UI does: with a token the widget made with the
// new site key (s.tsURL must point at a fakeCF).
func turnstileOn(t *testing.T, s *Service, ownerCtx context.Context) {
	t.Helper()
	en, key, sec := true, cfSiteKey, cfSecret
	if _, err := s.UpdateSecuritySettings(ownerCtx, connect.NewRequest(&adminv1.UpdateSecuritySettingsRequest{
		TurnstileEnabled: &en, TurnstileSiteKey: &key, TurnstileSecretKey: &sec, TurnstileTestToken: "ok-enable-" + randomToken(8)})); err != nil {
		t.Fatalf("enable turnstile: %v", err)
	}
}

func ownerCtx(t *testing.T, s *Service, st *store.Store) (context.Context, string) {
	t.Helper()
	a := makeAdmin(t, s, st)
	c, err := s.newSession(context.Background(), a.ID, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return authed(t, s, c.Value), c.Value
}

// The captcha guards the login form: a stolen owner cookie whose step-up window is over cannot switch it off.
func TestTurnstileSettingsNeedAFreshStepUp(t *testing.T) {
	s, st, clock := newTestService(t)
	octx, _ := ownerCtx(t, s, st)
	*clock = clock.Add(StepUpWindow + time.Second)
	empty := ""
	_, err := s.UpdateSecuritySettings(octx, connect.NewRequest(&adminv1.UpdateSecuritySettingsRequest{TurnstileSecretKey: &empty}))
	if codeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an old owner session removed the captcha secret: %v", err)
	}
}

func TestTurnstileSettingsAreOwnerOnlyAndNeverShowTheSecret(t *testing.T) {
	s, st, _ := newTestService(t)
	octx, _ := ownerCtx(t, s, st)
	cf := newFakeCF(t)
	s.tsURL = cf.srv.URL
	helper := insertAdmin(t, st, store.RoleHelper)
	hc, _ := s.newSession(context.Background(), helper.ID, "", "")
	hctx := authed(t, s, hc.Value)
	get := func(ctx context.Context) (*adminv1.GetSecuritySettingsResponse, error) {
		r, err := s.GetSecuritySettings(ctx, connect.NewRequest(&adminv1.GetSecuritySettingsRequest{}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	update := func(ctx context.Context, m *adminv1.UpdateSecuritySettingsRequest) (*adminv1.UpdateSecuritySettingsResponse, error) {
		r, err := s.UpdateSecuritySettings(ctx, connect.NewRequest(m))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	yes, no := true, false
	key, sec, empty, badKey, badSec := cfSiteKey, cfSecret, "", "key with spaces", "secret with spaces"

	if _, err := get(hctx); codeOf(err) != connect.CodePermissionDenied {
		t.Errorf("get as a helper: %v", err)
	}
	if _, err := update(hctx, &adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes}); codeOf(err) != connect.CodePermissionDenied {
		t.Errorf("update as a helper: %v", err)
	}
	if g, err := get(octx); err != nil || g.TurnstileEnabled || g.TurnstileSiteKey != "" || g.TurnstileSecretSet {
		t.Fatalf("defaults: %+v %v", g, err)
	}

	// On needs both keys.
	if _, err := update(octx, &adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes}); codeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("enable without keys: %v", err)
	}
	if _, err := update(octx, &adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileSiteKey: &key}); codeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("enable without a secret: %v", err)
	}
	if _, err := update(octx, &adminv1.UpdateSecuritySettingsRequest{TurnstileSiteKey: &badKey}); codeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad site key: %v", err)
	}
	if _, err := update(octx, &adminv1.UpdateSecuritySettingsRequest{TurnstileSecretKey: &badSec}); codeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad secret: %v", err)
	}
	if g, _ := get(octx); g.TurnstileEnabled || g.TurnstileSecretSet {
		t.Errorf("a refused update changed something: %+v", g)
	}

	resp, err := update(octx, &adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileSiteKey: &key, TurnstileSecretKey: &sec, TurnstileTestToken: "ok-1"})
	if err != nil || !resp.TurnstileEnabled || resp.TurnstileSiteKey != cfSiteKey || !resp.TurnstileSecretSet {
		t.Fatalf("enable: %+v %v", resp, err)
	}
	// The secret is nowhere in any response, and only sealed in the database.
	g, _ := get(octx)
	for _, m := range []string{prototext.Format(g), prototext.Format(resp)} {
		if strings.Contains(m, cfSecret) {
			t.Errorf("the secret key is in a response: %s", m)
		}
	}
	raw, err := st.Setting(context.Background(), settingTurnstileSecret)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := base64.StdEncoding.DecodeString(raw)
	if raw == "" || strings.Contains(raw, cfSecret) || bytes.Contains(sealed, []byte(cfSecret)) {
		t.Errorf("stored secret is not sealed: %q", raw)
	}
	// Bound to its record: it cannot be opened as another record.
	if _, err := s.vault.Open(sealed, "some-other-record"); err == nil {
		t.Error("the sealed secret opens under another record id")
	}

	// Absent fields stay: changing the site key keeps the secret and the switch (a new key while on is proven first).
	key2 := "another-site-key"
	if r, err := update(octx, &adminv1.UpdateSecuritySettingsRequest{TurnstileSiteKey: &key2, TurnstileTestToken: "ok-2"}); err != nil || !r.TurnstileEnabled || !r.TurnstileSecretSet || r.TurnstileSiteKey != key2 {
		t.Errorf("site key change: %+v %v", r, err)
	}
	// Off keeps the keys; removing the secret switches it off as well.
	if r, err := update(octx, &adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &no}); err != nil || r.TurnstileEnabled || !r.TurnstileSecretSet {
		t.Errorf("off: %+v %v", r, err)
	}
	if _, err := update(octx, &adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileTestToken: "ok-3"}); err != nil {
		t.Errorf("on again with the stored keys: %v", err)
	}
	if r, err := update(octx, &adminv1.UpdateSecuritySettingsRequest{TurnstileSecretKey: &empty}); err != nil || r.TurnstileEnabled || r.TurnstileSecretSet {
		t.Errorf("removing the secret: %+v %v", r, err)
	}
	if countAudit(t, st, "security_update") < 4 {
		t.Error("settings changes are not audited")
	}
	rows, _ := st.R.Query(`SELECT params FROM audit WHERE action = 'security_update'`)
	for rows.Next() {
		var p string
		rows.Scan(&p)
		if strings.Contains(p, cfSecret) || strings.Contains(p, key2) {
			t.Errorf("audit row carries a key: %s", p)
		}
	}
	rows.Close()
}

func TestTurnstileGatesBeginCallsOverHTTP(t *testing.T) {
	s, st, _ := newTestService(t)
	octx, _ := ownerCtx(t, s, st)
	cf := newFakeCF(t)
	s.tsURL = cf.srv.URL
	s.trust = NewProxyTrust(mustProxies(t, "127.0.0.0/8"))

	mux := http.NewServeMux()
	p, h := s.Handler()
	mux.Handle(p, s.RequireSession(h))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cli := adminv1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	from := func(r interface{ Header() http.Header }) { r.Header().Set("X-Forwarded-For", "203.0.113.7") }

	// Off: nothing is asked of the browser and Cloudflare is never called.
	li, err := cli.GetLoginInfo(context.Background(), connect.NewRequest(&adminv1.GetLoginInfoRequest{}))
	if err != nil || li.Msg.TurnstileEnabled || li.Msg.TurnstileSiteKey != "" {
		t.Fatalf("login info while off: %+v %v", li, err)
	}
	if _, err := cli.BeginLogin(context.Background(), connect.NewRequest(&adminv1.BeginLoginRequest{})); err != nil {
		t.Fatalf("BeginLogin with the captcha off: %v", err)
	}
	if cf.callCount() != 0 {
		t.Fatal("Cloudflare was called while the captcha is off")
	}

	turnstileOn(t, s, octx)
	li, _ = cli.GetLoginInfo(context.Background(), connect.NewRequest(&adminv1.GetLoginInfoRequest{}))
	if !li.Msg.TurnstileEnabled || li.Msg.TurnstileSiteKey != cfSiteKey {
		t.Errorf("login info while on: %+v", li.Msg)
	}
	if strings.Contains(prototext.Format(li.Msg), cfSecret) {
		t.Error("GetLoginInfo leaks the secret")
	}

	begin := func(token string) error {
		r := connect.NewRequest(&adminv1.BeginLoginRequest{TurnstileToken: token})
		from(r)
		_, err := cli.BeginLogin(context.Background(), r)
		return err
	}
	calls := cf.callCount() // switching it on asked Cloudflare once, about the test token
	if err := begin(""); codeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "captcha required") {
		t.Errorf("no token: %v", err)
	}
	if cf.callCount() != calls {
		t.Error("Cloudflare was asked about an empty token")
	}
	if err := begin("bad-token"); codeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "captcha failed") {
		t.Errorf("rejected token: %v", err)
	}
	if err := begin("ok-1"); err != nil {
		t.Fatalf("valid token: %v", err)
	}
	// What Cloudflare was sent: the secret, the token and the client address (from the trusted proxy).
	cf.mu.Lock()
	last := cf.calls[len(cf.calls)-1]
	cf.mu.Unlock()
	if last.Get("secret") != cfSecret || last.Get("response") != "ok-1" || last.Get("remoteip") != "203.0.113.7" {
		t.Errorf("siteverify form: %v", last)
	}
	// Single use (Cloudflare's rule, relayed): the same token again is refused.
	if err := begin("ok-1"); codeOf(err) != connect.CodePermissionDenied {
		t.Errorf("a used token: %v", err)
	}
	// An absurd token is refused without a call.
	n := cf.callCount()
	if err := begin(strings.Repeat("x", maxTurnstileToken+1)); codeOf(err) != connect.CodePermissionDenied || cf.callCount() != n {
		t.Errorf("huge token: %v (calls %d -> %d)", err, n, cf.callCount())
	}

	// The password form and the setup are gated as well.
	pw := func(token string) error {
		r := connect.NewRequest(&adminv1.PasswordLoginRequest{Login: "nobody", Password: "x", TotpCode: "000000", TurnstileToken: token})
		from(r)
		_, err := cli.PasswordLogin(context.Background(), r)
		return err
	}
	if err := pw(""); codeOf(err) != connect.CodePermissionDenied {
		t.Errorf("PasswordLogin without a token: %v", err)
	}
	if err := pw("ok-2"); codeOf(err) != connect.CodeUnauthenticated { // the captcha passed; the credentials are wrong
		t.Errorf("PasswordLogin with a token and wrong credentials: %v", err)
	}
	if err := pw("ok-2"); codeOf(err) != connect.CodePermissionDenied { // each attempt needs a fresh token
		t.Errorf("PasswordLogin reusing a token: %v", err)
	}
	if f, _ := st.LoginFailures(context.Background(), "nobody", time.Now(), FailureWindow); f.Failures != 1 {
		t.Errorf("failures counted for the login: %d, want 1 (a refused captcha is not a wrong password)", f.Failures)
	}
	bs := func(token string) error {
		r := connect.NewRequest(&adminv1.BeginSetupRequest{SetupToken: "whatever", TurnstileToken: token})
		from(r)
		_, err := cli.BeginSetup(context.Background(), r)
		return err
	}
	if err := bs(""); codeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "captcha") {
		t.Errorf("BeginSetup without a token: %v", err)
	}
	if err := bs("ok-3"); codeOf(err) != connect.CodePermissionDenied || strings.Contains(err.Error(), "captcha") { // passed the captcha, then the setup token is wrong
		t.Errorf("BeginSetup with a token: %v", err)
	}
}

// Wrong keys never lock the owner out: switching the check on, or new keys while it is on, is saved only after a token
// of the widget passes siteverify with the new secret; Cloudflare out of reach saves nothing either.
func TestTurnstileKeysAreProvenBeforeTheCheckGoesOn(t *testing.T) {
	s, st, _ := newTestService(t)
	octx, _ := ownerCtx(t, s, st)
	cf := newFakeCF(t)
	s.tsURL = cf.srv.URL
	yes, no := true, false
	key, sec, wrongSec := cfSiteKey, cfSecret, "0x4AAAAAAA-some-other-secret"
	update := func(m *adminv1.UpdateSecuritySettingsRequest) error {
		_, err := s.UpdateSecuritySettings(octx, connect.NewRequest(m))
		return err
	}
	stored := func() *adminv1.GetSecuritySettingsResponse {
		g, err := s.GetSecuritySettings(octx, connect.NewRequest(&adminv1.GetSecuritySettingsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return g.Msg
	}
	nothingSaved := func(what string) {
		t.Helper()
		if g := stored(); g.TurnstileEnabled || g.TurnstileSiteKey != "" || g.TurnstileSecretSet {
			t.Errorf("%s saved something: %+v", what, g)
		}
	}

	wantCoded(t, update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileSiteKey: &key, TurnstileSecretKey: &sec}),
		connect.CodeFailedPrecondition, "turnstile_test_required")
	nothingSaved("no token")
	wantCoded(t, update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileSiteKey: &key, TurnstileSecretKey: &wrongSec, TurnstileTestToken: "ok-a"}),
		connect.CodeFailedPrecondition, "turnstile_secret_rejected")
	nothingSaved("a secret Cloudflare does not know")
	wantCoded(t, update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileSiteKey: &key, TurnstileSecretKey: &sec, TurnstileTestToken: "made-up"}),
		connect.CodeFailedPrecondition, "turnstile_token_rejected")
	nothingSaved("a token Cloudflare refuses")
	wantCoded(t, update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileSiteKey: &key, TurnstileSecretKey: &sec, TurnstileTestToken: strings.Repeat("x", maxTurnstileToken+1)}),
		connect.CodeFailedPrecondition, "turnstile_token_rejected")
	down := newFakeCF(t)
	s.tsURL = down.srv.URL
	down.srv.Close()
	wantCoded(t, update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileSiteKey: &key, TurnstileSecretKey: &sec, TurnstileTestToken: "ok-b"}),
		connect.CodeUnavailable, "turnstile_unreachable")
	nothingSaved("Cloudflare out of reach")
	s.tsURL = cf.srv.URL
	if countAuditResult(t, st, "security_update", "rejected") != 5 {
		t.Errorf("rejected attempts audited %d times", countAuditResult(t, st, "security_update", "rejected"))
	}

	// The proof goes to Cloudflare with the new secret; then it is saved and on.
	if err := update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileSiteKey: &key, TurnstileSecretKey: &sec, TurnstileTestToken: "ok-c"}); err != nil {
		t.Fatalf("a proven pair: %v", err)
	}
	cf.mu.Lock()
	last := cf.calls[len(cf.calls)-1]
	cf.mu.Unlock()
	if last.Get("secret") != cfSecret || last.Get("response") != "ok-c" {
		t.Errorf("siteverify form: %v", last)
	}
	if g := stored(); !g.TurnstileEnabled || g.TurnstileSiteKey != cfSiteKey || !g.TurnstileSecretSet {
		t.Fatalf("after the proof: %+v", g)
	}
	// While on: a new secret or site key is proven too, and a refused one leaves the working pair in place.
	wantCoded(t, update(&adminv1.UpdateSecuritySettingsRequest{TurnstileSecretKey: &wrongSec, TurnstileTestToken: "ok-d"}), connect.CodeFailedPrecondition, "turnstile_secret_rejected")
	key2 := "another-site-key"
	wantCoded(t, update(&adminv1.UpdateSecuritySettingsRequest{TurnstileSiteKey: &key2}), connect.CodeFailedPrecondition, "turnstile_test_required")
	if g := stored(); !g.TurnstileEnabled || g.TurnstileSiteKey != cfSiteKey {
		t.Errorf("a refused change touched the working pair: %+v", g)
	}
	// Off, removing the secret and keys changed while off need no proof; on again does (with the stored secret).
	n := cf.callCount()
	if err := update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &no}); err != nil {
		t.Fatal(err)
	}
	if err := update(&adminv1.UpdateSecuritySettingsRequest{TurnstileSiteKey: &key2}); err != nil {
		t.Fatal(err)
	}
	if cf.callCount() != n {
		t.Error("Cloudflare was asked while the check stays off")
	}
	wantCoded(t, update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes}), connect.CodeFailedPrecondition, "turnstile_test_required")
	if err := update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes, TurnstileTestToken: "ok-e"}); err != nil {
		t.Errorf("on again with the stored secret: %v", err)
	}
	cf.mu.Lock()
	last = cf.calls[len(cf.calls)-1]
	cf.mu.Unlock()
	if last.Get("secret") != cfSecret {
		t.Errorf("the stored secret was not the one tried: %v", last)
	}
	// The kill switch, then on again: the same rule.
	if err := DisableTurnstile(context.Background(), st, s.now()); err != nil {
		t.Fatal(err)
	}
	wantCoded(t, update(&adminv1.UpdateSecuritySettingsRequest{TurnstileEnabled: &yes}), connect.CodeFailedPrecondition, "turnstile_test_required")
	empty := ""
	if err := update(&adminv1.UpdateSecuritySettingsRequest{TurnstileSecretKey: &empty}); err != nil {
		t.Errorf("removing the secret needs no proof: %v", err)
	}
	// No audit row carries a key or a token.
	rows, _ := st.R.Query(`SELECT params FROM audit WHERE action = 'security_update'`)
	for rows.Next() {
		var p string
		rows.Scan(&p)
		if strings.Contains(p, cfSecret) || strings.Contains(p, wrongSec) || strings.Contains(p, "ok-") {
			t.Errorf("audit row carries a key or a token: %s", p)
		}
	}
	rows.Close()
}

// One solved challenge per sign-in: Begin consumed the token, Finish carries none and still works.
func TestTurnstileFinishNeedsNoSecondToken(t *testing.T) {
	s, st, _ := newTestService(t)
	admin, cookie, k := passkeyAdmin(t, s, st)
	_ = admin
	cf := newFakeCF(t)
	s.tsURL = cf.srv.URL
	turnstileOn(t, s, authed(t, s, cookie))

	ctx := context.Background()
	if _, err := s.BeginLogin(ctx, connect.NewRequest(&adminv1.BeginLoginRequest{})); codeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("BeginLogin without a token: %v", err)
	}
	begin, err := s.BeginLogin(ctx, connect.NewRequest(&adminv1.BeginLoginRequest{TurnstileToken: "ok-login"}))
	if err != nil {
		t.Fatal(err)
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(begin.Msg.OptionsJson)
	if err != nil {
		t.Fatal(err)
	}
	cj := virtualwebauthn.CreateAssertionResponse(k.rp, k.dev, k.cred, *opts)
	n := cf.callCount()
	fin, err := s.FinishLogin(ctx, connect.NewRequest(&adminv1.FinishLoginRequest{CeremonyId: begin.Msg.CeremonyId, CredentialJson: cj}))
	if err != nil || fin.Msg.Admin == nil {
		t.Fatalf("FinishLogin without a token: %v", err)
	}
	if cf.callCount() != n {
		t.Error("FinishLogin called Cloudflare again")
	}
	// A ceremony id cannot be made up: without a passing Begin there is nothing to finish.
	if _, err := s.FinishLogin(ctx, connect.NewRequest(&adminv1.FinishLoginRequest{CeremonyId: "forged", CredentialJson: cj})); codeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("FinishLogin with a forged ceremony: %v", err)
	}
}

// Fail closed, whatever is wrong; and the kill switch frees the sign-in without touching the keys.
func TestTurnstileFailsClosedAndKillSwitch(t *testing.T) {
	s, st, _ := newTestService(t)
	octx, _ := ownerCtx(t, s, st)
	cf := newFakeCF(t)
	s.tsURL = cf.srv.URL
	turnstileOn(t, s, octx)
	begin := func(tok string) error {
		_, err := s.BeginLogin(context.Background(), connect.NewRequest(&adminv1.BeginLoginRequest{TurnstileToken: tok}))
		return err
	}
	denied := func(what, tok string) {
		t.Helper()
		if err := begin(tok); codeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: %v, want PERMISSION_DENIED", what, err)
		}
	}

	cf.mu.Lock()
	cf.status = http.StatusInternalServerError
	cf.mu.Unlock()
	denied("siteverify answers 500", "ok-a")
	cf.mu.Lock()
	cf.status, cf.body = 0, `{"success":true`
	cf.mu.Unlock()
	denied("siteverify answers broken JSON", "ok-b")
	cf.mu.Lock()
	cf.body = `<html>captive portal</html>`
	cf.mu.Unlock()
	denied("siteverify answers HTML", "ok-c")
	cf.mu.Lock()
	cf.body, cf.status = `{"success":true}`, http.StatusBadGateway
	cf.mu.Unlock()
	denied("siteverify answers 502 with a success body", "ok-d")

	cf.mu.Lock()
	cf.status, cf.body, cf.delay = 0, "", 600*time.Millisecond
	cf.mu.Unlock()
	s.tsClient = &http.Client{Timeout: 150 * time.Millisecond}
	start := time.Now()
	denied("siteverify too slow", "ok-e")
	if time.Since(start) > 2*time.Second {
		t.Error("the timeout did not cut the call short")
	}
	cf.mu.Lock()
	cf.delay = 0
	cf.mu.Unlock()
	s.tsClient = &http.Client{Timeout: turnstileTimeout}

	url := cf.srv.URL
	cf.srv.Close()
	denied("Cloudflare unreachable", "ok-f")
	s.tsURL = url // (closed server; keep pointing at it for the kill switch check below)

	// The secret cannot be opened (master key changed): closed as well.
	other, _ := vault.New(bytes.Repeat([]byte{9}, vault.KeySize))
	good := newFakeCF(t)
	s.tsURL = good.srv.URL
	if err := begin("ok-g"); err != nil {
		t.Fatalf("a healthy setup must pass: %v", err)
	}
	realVault := s.vault
	s.vault = other
	denied("secret sealed with another master key", "ok-h")
	s.vault = nil
	denied("no vault", "ok-i")
	s.vault = realVault

	// Kill switch: with Cloudflare gone and the panel "running" (same service), off frees the sign-in at once.
	s.tsURL = url
	denied("still on, Cloudflare gone", "ok-j")
	if err := DisableTurnstile(context.Background(), st, s.now()); err != nil {
		t.Fatal(err)
	}
	if err := begin(""); err != nil {
		t.Errorf("after the kill switch: %v", err)
	}
	g, _ := s.GetSecuritySettings(octx, connect.NewRequest(&adminv1.GetSecuritySettingsRequest{}))
	if g.Msg.TurnstileEnabled || g.Msg.TurnstileSiteKey != cfSiteKey || !g.Msg.TurnstileSecretSet {
		t.Errorf("the kill switch should only switch off: %+v", g.Msg)
	}
	li, _ := s.GetLoginInfo(context.Background(), connect.NewRequest(&adminv1.GetLoginInfoRequest{}))
	if li.Msg.TurnstileEnabled {
		t.Error("the login page still asks for a captcha")
	}
	if countAudit(t, st, "turnstile_off") != 1 {
		t.Error("the kill switch is not in the audit log")
	}
	// Idempotent, also on a fresh database that never had the setting.
	if err := DisableTurnstile(context.Background(), st, s.now()); err != nil {
		t.Errorf("second run: %v", err)
	}
}
