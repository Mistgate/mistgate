package httpserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// User verification (PIN or biometrics) is required for registration and sign-in, and the
// server enforces it on the response, not just in the options it hands the browser.
func TestUserVerificationIsRequired(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	base := e.admin.URL + "/"
	tok := e.setupToken(t, time.Now())

	// The options ask for it.
	raw, err := newBrowser(t, base, "").api.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{SetupToken: tok}))
	if err != nil || !strings.Contains(raw.Msg.OptionsJson, `"userVerification":"required"`) {
		t.Fatalf("registration options: %v %s", err, raw.Msg.OptionsJson)
	}
	rawLogin, err := newBrowser(t, base, "").api.BeginLogin(ctx, connect.NewRequest(&adminv1.BeginLoginRequest{}))
	if err != nil || !strings.Contains(rawLogin.Msg.OptionsJson, `"userVerification":"required"`) {
		t.Fatalf("login options: %v %s", err, rawLogin.Msg.OptionsJson)
	}

	// An authenticator that does not verify the user cannot register, and the failure does
	// not burn the setup token.
	b := newBrowser(t, base, "")
	b.dev.Options.UserNotVerified = true
	if err := b.register(tok, "Ada"); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("registration without user verification: %v", err)
	}
	if n, _ := e.st.AdminCount(ctx); n != 0 {
		t.Fatal("admin created from an unverified registration")
	}
	b.dev.Options.UserNotVerified = false
	b.cred = newBrowser(t, base, "").cred // a fresh credential: the first one never registered
	if err := b.register(tok, "Ada"); err != nil {
		t.Fatalf("verified registration: %v", err)
	}
	b.logout()

	// Sign-in: same.
	b.dev.Options.UserNotVerified = true
	if err := b.login(); code(err) != connect.CodeUnauthenticated {
		t.Fatalf("login without user verification: %v", err)
	}
	if _, err := b.me(); code(err) != connect.CodeUnauthenticated {
		t.Fatal("session after an unverified login")
	}
	b.dev.Options.UserNotVerified = false
	if err := b.login(); err != nil {
		t.Fatalf("verified login: %v", err)
	}
	if _, err := b.me(); err != nil {
		t.Fatal(err)
	}
}
