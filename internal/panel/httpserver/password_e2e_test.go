package httpserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
)

// totpAt is an authenticator app: the 6-digit code for the secret (base32) at time t.
func totpAt(t *testing.T, secretB32 string, at time.Time) string {
	t.Helper()
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secretB32)
	if err != nil {
		t.Fatal(err)
	}
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, secret)
	mac.Write(c[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0xf
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[off:off+4])&0x7fffffff)%1_000_000)
}

func signInFailure(t *testing.T, err error) *adminv1.SignInFailure {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeUnauthenticated {
		t.Fatalf("want UNAUTHENTICATED, got %v", err)
	}
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if f, ok := v.(*adminv1.SignInFailure); ok {
				return f
			}
		}
	}
	t.Fatalf("no SignInFailure detail: %v", err)
	return nil
}

func (b *browser) passwordLogin(login, password, code string) (*connect.Response[adminv1.PasswordLoginResponse], error) {
	return b.api.PasswordLogin(context.Background(), connect.NewRequest(&adminv1.PasswordLoginRequest{Login: login, Password: password, TotpCode: code}))
}

func TestPasswordAndTOTPEndToEnd(t *testing.T) {
	// Behind a trusted proxy, every request from its own address: this test makes more calls
	// than one source may within the rate limit, which has its own tests.
	e := newTestEnvAuth(t, func(c *auth.Config) { c.TrustedProxies = loopback() })
	ctx := context.Background()
	b := newBrowser(t, e.admin.URL+"/", "")
	n := 0
	b.jar.xff = func() string { n++; return fmt.Sprintf("198.51.100.%d", n) }
	const password = "correct horse battery staple"

	info, err := b.api.GetLoginInfo(ctx, connect.NewRequest(&adminv1.GetLoginInfoRequest{}))
	if err != nil || !info.Msg.SetupOpen || info.Msg.PasswordLogin || info.Msg.BrandHead != "Mist" || info.Msg.BrandTail != "gate" ||
		info.Msg.Accent != "#b8acf2" || info.Msg.Language != "en" || info.Msg.HasLogo {
		t.Fatalf("login info before setup: %+v %v", info, err)
	}

	tok := e.setupToken(t, time.Now())
	begin := func(login, pw string) (*connect.Response[adminv1.BeginSetupResponse], error) {
		return b.api.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
			SetupToken: tok, DisplayName: "Ada", Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD, Login: login, Password: pw}))
	}
	if _, err := begin("a b", password); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad login: %v", err)
	}
	resp, err := begin("Ada", password)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.OptionsJson != "" || resp.Msg.CeremonyId == "" {
		t.Fatalf("password setup begin: %+v", resp.Msg)
	}
	uri, err := url.Parse(resp.Msg.TotpUri)
	if err != nil || uri.Scheme != "otpauth" || uri.Host != "totp" || uri.Path != "/Mistgate:ada" ||
		uri.Query().Get("secret") != resp.Msg.TotpSecret || uri.Query().Get("issuer") != "Mistgate" {
		t.Fatalf("otpauth uri %q: %v", resp.Msg.TotpUri, err)
	}
	secret := resp.Msg.TotpSecret
	finish := func(code string) (*connect.Response[adminv1.FinishSetupResponse], error) {
		return b.api.FinishSetup(ctx, connect.NewRequest(&adminv1.FinishSetupRequest{SetupToken: tok, CeremonyId: resp.Msg.CeremonyId, TotpCode: code}))
	}
	if _, err := finish("000000"); code(err) != connect.CodeInvalidArgument {
		t.Fatalf("wrong setup code: %v", err)
	}
	now := time.Now()
	fin, err := finish(totpAt(t, secret, now))
	if err != nil || fin.Msg.Admin.DisplayName != "Ada" || fin.Msg.Admin.Role != adminv1.Role_ROLE_OWNER {
		t.Fatalf("finish setup: %v", err)
	}
	if me, err := b.me(); err != nil || me.Admin.Id != fin.Msg.Admin.Id {
		t.Fatalf("Me after password setup: %v", err)
	}
	info, _ = b.api.GetLoginInfo(ctx, connect.NewRequest(&adminv1.GetLoginInfoRequest{}))
	if info.Msg.SetupOpen || !info.Msg.PasswordLogin {
		t.Fatalf("login info after setup: %+v", info.Msg)
	}
	if err := b.logout(); err != nil {
		t.Fatal(err)
	}

	// The code that finished setup is spent (and the failed try counts toward the lockout).
	if _, err := b.passwordLogin("ada", password, totpAt(t, secret, now)); signInFailure(t, err).AttemptsLeft != 4 {
		t.Fatalf("replay of the setup code: %v", err)
	}
	// The next step's code works, for any spelling of the login.
	next := now.Add(30 * time.Second)
	code1 := totpAt(t, secret, next)
	lr, err := b.passwordLogin(" ADA ", password, code1)
	if err != nil || lr.Msg.Admin.Id != fin.Msg.Admin.Id {
		t.Fatalf("password login: %v", err)
	}
	if me, err := b.me(); err != nil || me.Admin.DisplayName != "Ada" {
		t.Fatalf("Me after password login: %v", err)
	}
	b.logout()
	if _, err := b.passwordLogin("ada", password, code1); err == nil {
		t.Fatal("a TOTP code worked twice")
	}

	// Five wrong attempts lock the login, with the unlock time in the error; then even the
	// right credentials are refused.
	var f *adminv1.SignInFailure
	for i := 0; i < 4; i++ { // one failure (the replay above) is already counted
		_, err = b.passwordLogin("ada", "nope nope nope nope", "000000")
		f = signInFailure(t, err)
	}
	wantUntil := time.Now().Add(15 * time.Minute)
	if f.AttemptsLeft != 0 || f.LockedUntilUnix == 0 || f.LockedUntilUnix < wantUntil.Add(-10*time.Second).Unix() || f.LockedUntilUnix > wantUntil.Add(10*time.Second).Unix() {
		t.Fatalf("lockout: %+v, want until about %d", f, wantUntil.Unix())
	}
	right := totpAt(t, secret, next.Add(30*time.Second))
	if _, err := b.passwordLogin("ada", password, right); signInFailure(t, err).LockedUntilUnix != f.LockedUntilUnix {
		t.Fatalf("right credentials during lockout: %v", err)
	}
	if _, err := b.me(); code(err) != connect.CodeUnauthenticated {
		t.Fatal("session opened during lockout")
	}

	// The audit log (owner only) has the failures and the lockout, no secrets, with the source.
	rows, err := e.st.ListAudit(ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var fails, locks, oks int
	for _, r := range rows {
		if strings.Contains(r.Params, password) || strings.Contains(r.Params, secret) || strings.Contains(r.Params, code1) {
			t.Errorf("secret in audit row %+v", r)
		}
		switch {
		case r.Action == "login" && r.Result == "fail":
			fails++
		case r.Action == "lockout":
			locks++
		case r.Action == "login" && r.Result == "ok":
			oks++
		}
		if r.Source != "panel" {
			t.Errorf("source %q", r.Source)
		}
	}
	if fails != 6 || locks != 1 || oks != 1 {
		t.Errorf("audit: %d failures, %d lockouts, %d logins", fails, locks, oks)
	}
}
