package httpserver

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/descope/virtualwebauthn"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
)

// browser is a software authenticator plus a Connect client with its own cookie jar.
type browser struct {
	t    *testing.T
	api  adminv1connect.AuthServiceClient
	jar  *cookieJar
	rp   virtualwebauthn.RelyingParty
	dev  virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

// newBrowser talks to the admin API at adminBase (ending in "/"), optionally with a Host override.
func newBrowser(t *testing.T, adminBase, host string) *browser {
	jar := &cookieJar{host: host}
	return &browser{
		t:    t,
		jar:  jar,
		api:  adminv1connect.NewAuthServiceClient(&http.Client{Transport: jar}, adminBase+"api", connect.WithProtoJSON()),
		rp:   virtualwebauthn.RelyingParty{Name: "Mistgate", ID: testRPID, Origin: testOrigin},
		dev:  virtualwebauthn.NewAuthenticator(),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

func code(err error) connect.Code {
	if err == nil {
		return 0
	}
	return connect.CodeOf(err)
}

func (b *browser) beginSetup(token, name string) (string, *virtualwebauthn.AttestationOptions, error) {
	b.t.Helper()
	resp, err := b.api.BeginSetup(context.Background(), connect.NewRequest(&adminv1.BeginSetupRequest{SetupToken: token, DisplayName: name}))
	if err != nil {
		return "", nil, err
	}
	// The options must be the bare PublicKeyCredentialCreationOptionsJSON.
	if strings.Contains(resp.Msg.OptionsJson[:20], "publicKey") {
		b.t.Fatalf("options_json is wrapped: %.60s", resp.Msg.OptionsJson)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(resp.Msg.OptionsJson)
	if err != nil {
		b.t.Fatalf("options_json not parseable: %v", err)
	}
	b.dev.Options.UserHandle = []byte(opts.UserID)
	return resp.Msg.CeremonyId, opts, nil
}

func (b *browser) finishSetup(token, ceremony string, opts *virtualwebauthn.AttestationOptions) (*connect.Response[adminv1.FinishSetupResponse], error) {
	cj := virtualwebauthn.CreateAttestationResponse(b.rp, b.dev, b.cred, *opts)
	return b.api.FinishSetup(context.Background(), connect.NewRequest(&adminv1.FinishSetupRequest{SetupToken: token, CeremonyId: ceremony, CredentialJson: cj}))
}

func (b *browser) register(token, name string) error {
	b.t.Helper()
	cer, opts, err := b.beginSetup(token, name)
	if err != nil {
		return err
	}
	if _, err := b.finishSetup(token, cer, opts); err != nil {
		return err
	}
	b.dev.AddCredential(b.cred)
	return nil
}

func (b *browser) beginLogin() (string, *virtualwebauthn.AssertionOptions, error) {
	resp, err := b.api.BeginLogin(context.Background(), connect.NewRequest(&adminv1.BeginLoginRequest{}))
	if err != nil {
		return "", nil, err
	}
	if strings.Contains(resp.Msg.OptionsJson[:20], "publicKey") {
		b.t.Fatalf("options_json is wrapped: %.60s", resp.Msg.OptionsJson)
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(resp.Msg.OptionsJson)
	if err != nil {
		b.t.Fatalf("options_json not parseable: %v", err)
	}
	if len(opts.AllowCredentials) != 0 {
		b.t.Error("discoverable login must not list allowCredentials")
	}
	return resp.Msg.CeremonyId, opts, nil
}

func (b *browser) finishLogin(cer string, opts *virtualwebauthn.AssertionOptions) (*connect.Response[adminv1.FinishLoginResponse], error) {
	cj := virtualwebauthn.CreateAssertionResponse(b.rp, b.dev, b.cred, *opts)
	return b.api.FinishLogin(context.Background(), connect.NewRequest(&adminv1.FinishLoginRequest{CeremonyId: cer, CredentialJson: cj}))
}

func (b *browser) login() error {
	b.t.Helper()
	cer, opts, err := b.beginLogin()
	if err != nil {
		return err
	}
	_, err = b.finishLogin(cer, opts)
	return err
}

func (b *browser) me() (*adminv1.MeResponse, error) {
	resp, err := b.api.Me(context.Background(), connect.NewRequest(&adminv1.MeRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (b *browser) logout() error {
	_, err := b.api.Logout(context.Background(), connect.NewRequest(&adminv1.LogoutRequest{}))
	return err
}

func (e *testEnv) setupToken(t *testing.T, at time.Time) string {
	t.Helper()
	tok, err := auth.IssueSetupToken(context.Background(), e.st, at)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// fullFlow: setup -> Me -> logout -> login -> Me, plus the negative checks that follow.
func fullFlow(t *testing.T, e *testEnv, adminBase, host string) {
	t.Helper()
	b := newBrowser(t, adminBase, host)

	if _, err := b.me(); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("Me without a session: %v", err)
	}

	token := e.setupToken(t, time.Now())
	if err := b.register(token, "Ada"); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Session cookie attributes.
	var sess *http.Cookie
	for _, c := range b.jar.last {
		if c.Name == auth.CookieName {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || !sess.Secure || sess.SameSite != http.SameSiteStrictMode || sess.Path != "/" || sess.Domain != "" || len(sess.Value) < 40 {
		t.Fatalf("bad session cookie: %+v", sess)
	}

	me, err := b.me()
	if err != nil || me.Admin.DisplayName != "Ada" || me.Admin.Role != adminv1.Role_ROLE_OWNER || me.Version == "" {
		t.Fatalf("Me after setup: %+v %v", me, err)
	}
	if !strings.HasPrefix(me.Admin.Id, "adm_") {
		t.Errorf("admin id %q", me.Admin.Id)
	}

	// The token is single use, and no second admin can be created.
	if _, _, err := newBrowser(t, adminBase, host).beginSetup(token, "Mallory"); code(err) != connect.CodePermissionDenied {
		t.Fatalf("setup token reuse: %v", err)
	}

	// Logout kills the session server-side: replaying the old cookie no longer works.
	if err := b.logout(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.me(); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("Me after logout: %v", err)
	}
	replay := newBrowser(t, adminBase, host)
	replay.jar.cookie = auth.CookieName + "=" + sess.Value
	if _, err := replay.me(); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("old session cookie still valid after logout: %v", err)
	}

	// Discoverable login, twice (sign counter must advance), then a replayed assertion fails.
	if err := b.login(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if me, err := b.me(); err != nil || me.Admin.Id == "" {
		t.Fatalf("Me after login: %v", err)
	}
	b.logout()
	cer, opts, err := b.beginLogin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.finishLogin(cer, opts); err != nil {
		t.Fatalf("second login: %v", err)
	}
	if _, err := b.finishLogin(cer, opts); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("replayed ceremony accepted: %v", err)
	}

	// A login from an authenticator the server has never seen fails.
	stranger := newBrowser(t, adminBase, host)
	stranger.dev.Options.UserHandle = []byte("whoever")
	stranger.dev.AddCredential(stranger.cred)
	if err := stranger.login(); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("unknown credential logged in: %v", err)
	}
	if _, err := stranger.me(); code(err) != connect.CodeUnauthenticated {
		t.Fatal("stranger has a session")
	}

	// Audit trail: setup, logins, failures and logouts are recorded, secrets are not.
	rows, err := e.st.R.Query(`SELECT action, result, params FROM audit ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]int{}
	for rows.Next() {
		var action, result, params string
		rows.Scan(&action, &result, &params)
		seen[action+"/"+result]++
		if strings.Contains(params, token) || strings.Contains(params, sess.Value) {
			t.Errorf("secret in audit params: %s", params)
		}
	}
	for _, want := range []string{"setup/ok", "login/ok", "login/fail", "logout/ok"} {
		if seen[want] == 0 {
			t.Errorf("no audit row %s (have %v)", want, seen)
		}
	}
}

func TestAuthFlowThroughSecretPrefix(t *testing.T) {
	e := newTestEnv(t)
	fullFlow(t, e, e.public.URL+testPrefix, "")
}

func TestAuthFlowThroughSecretHost(t *testing.T) {
	e := newTestEnv(t)
	fullFlow(t, e, e.public.URL+"/", testAdminHst)
}

func TestAuthFlowThroughAdminListener(t *testing.T) {
	e := newTestEnv(t)
	fullFlow(t, e, e.admin.URL+"/", "")
}

func TestSetupTokenExpired(t *testing.T) {
	e := newTestEnv(t)
	tok := e.setupToken(t, time.Now().Add(-auth.SetupTokenTTL-time.Minute))
	b := newBrowser(t, e.admin.URL+"/", "")
	if _, _, err := b.beginSetup(tok, "Ada"); code(err) != connect.CodePermissionDenied {
		t.Fatalf("expired token: %v", err)
	}
	if _, _, err := b.beginSetup("not-a-token", "Ada"); code(err) != connect.CodePermissionDenied {
		t.Fatalf("garbage token: %v", err)
	}
}

func TestSetupTokenIsBoundToCeremonyAndSurvivesFailures(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	tok := e.setupToken(t, time.Now())
	b := newBrowser(t, e.admin.URL+"/", "")

	// A ceremony begun with one token cannot be finished with another.
	cer, opts, err := b.beginSetup(tok, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	other := e.setupToken(t, time.Now()) // also invalidates tok
	if _, err := b.finishSetup(other, cer, opts); code(err) != connect.CodePermissionDenied {
		t.Fatalf("token swap: %v", err)
	}

	// A credential for the wrong origin is rejected and does not burn the token.
	b2 := newBrowser(t, e.admin.URL+"/", "")
	b2.rp.Origin = "https://evil.example"
	cer, opts, err = b2.beginSetup(other, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b2.finishSetup(other, cer, opts); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("wrong origin accepted: %v", err)
	}
	if n, _ := e.st.AdminCount(ctx); n != 0 {
		t.Fatal("admin created from a rejected credential")
	}
	b3 := newBrowser(t, e.admin.URL+"/", "")
	if err := b3.register(other, ""); err != nil {
		t.Fatalf("token burnt by a failed attempt: %v", err)
	}
	if me, _ := b3.me(); me == nil || me.Admin.DisplayName != "Owner" {
		t.Fatalf("default display name: %+v", me)
	}
}

func TestSessionRequiresValidCookie(t *testing.T) {
	e := newTestEnv(t)
	b := newBrowser(t, e.admin.URL+"/", "")
	for _, cookie := range []string{"", auth.CookieName + "=", auth.CookieName + "=forged", "sid=abc", auth.CookieName + "=" + strings.Repeat("A", 43)} {
		b.jar.cookie = cookie
		if _, err := b.me(); code(err) != connect.CodeUnauthenticated {
			t.Errorf("cookie %q: %v", cookie, err)
		}
	}
}

func TestRateLimitOnCeremonies(t *testing.T) {
	e := newTestEnv(t)
	b := newBrowser(t, e.admin.URL+"/", "")
	var limited int
	for range 25 {
		if _, _, err := b.beginLogin(); code(err) == connect.CodeResourceExhausted {
			limited++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if limited < 10 || limited > 20 {
		t.Fatalf("rate limit: %d of 25 requests limited, want the burst (10) to pass and the rest to fail", limited)
	}
	// Me is not a ceremony endpoint and stays responsive.
	if _, err := b.me(); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("Me: %v", err)
	}
}
