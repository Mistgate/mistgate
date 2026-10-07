package auth

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const testPassword = "correct horse battery staple"

func TestSetupWrongTOTPUpdatesExistingCeremony(t *testing.T) {
	s, st, _ := newTestService(t)
	ctx := context.Background()
	tok, err := IssueSetupToken(ctx, st, s.now())
	if err != nil {
		t.Fatal(err)
	}
	begin, err := s.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
		SetupToken: tok, Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD, Login: "ada", Password: testPassword,
	}))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(begin.Msg.TotpSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `CREATE TRIGGER reject_ceremony_restore BEFORE INSERT ON auth_ceremony
		BEGIN SELECT RAISE(ABORT, 'ceremony restore forbidden'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishSetup(ctx, connect.NewRequest(&adminv1.FinishSetupRequest{
		SetupToken: tok, CeremonyId: begin.Msg.CeremonyId, TotpCode: "invalid",
	})); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "invalid code") {
		t.Fatalf("wrong code = %v; want recoverable invalid-code response", err)
	}
	if _, err := st.W.ExecContext(ctx, `DROP TRIGGER reject_ceremony_restore`); err != nil {
		t.Fatal(err)
	}
	var tries int
	var sealed []byte
	if err := st.R.QueryRowContext(ctx, `SELECT tries, totp_enc FROM auth_ceremony WHERE id = ?`, begin.Msg.CeremonyId).Scan(&tries, &sealed); err != nil || tries != 1 {
		t.Fatalf("ceremony after typo = tries %d, err %v; want same live ceremony at try 1", tries, err)
	}
	opened, err := s.vault.Open(sealed, ceremonyTOTPRecordID(begin.Msg.CeremonyId))
	if err != nil || string(opened) != string(secret) || begin.Msg.TotpUri == "" {
		t.Fatalf("ceremony QR secret changed after typo: err %v", err)
	}
}

func TestLateSetupTypoCannotRestoreConsumedCeremony(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	tok, err := IssueSetupToken(ctx, st, s.now())
	if err != nil {
		t.Fatal(err)
	}
	begin, err := s.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
		SetupToken: tok, Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD, Login: "ada", Password: testPassword,
	}))
	if err != nil {
		t.Fatal(err)
	}
	stale, ok, err := s.getCeremony(ctx, begin.Msg.CeremonyId, ceremonySetupPassword)
	if err != nil || !ok {
		t.Fatalf("read ceremony for typo: ok=%v err=%v", ok, err)
	}
	if _, valid := matchTOTP(stale.totp, "invalid", s.now()); valid {
		t.Fatal("test typo unexpectedly matched the TOTP secret")
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(begin.Msg.TotpSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishSetup(ctx, connect.NewRequest(&adminv1.FinishSetupRequest{
		SetupToken: tok, CeremonyId: begin.Msg.CeremonyId, TotpCode: totpCode(secret, totpStep(*clock)),
	})); err != nil {
		t.Fatalf("correct finish: %v", err)
	}
	if changed, err := s.failCeremony(ctx, begin.Msg.CeremonyId); err != nil || changed {
		t.Fatalf("late typo failure = changed %v, err %v; want a deleted ceremony", changed, err)
	}
	if _, err := s.st.GetAuthCeremony(ctx, begin.Msg.CeremonyId, s.now()); !errors.Is(err, store.ErrAuthCeremonyNotFound) {
		t.Fatalf("late typo restored consumed ceremony: %v", err)
	}
}

func failureOf(t *testing.T, err error) *adminv1.SignInFailure {
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
	t.Fatalf("no SignInFailure detail in %v", err)
	return nil
}

// passwordAdmin runs the setup flow for a password admin through the handlers and
// returns the admin id and the TOTP secret the "authenticator app" got.
func passwordAdmin(t *testing.T, s *Service, st *store.Store, login string) (string, []byte) {
	t.Helper()
	ctx := context.Background()
	tok, err := IssueSetupToken(ctx, st, s.now())
	if err != nil {
		t.Fatal(err)
	}
	begin, err := s.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
		SetupToken: tok, DisplayName: "Ada", Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD, Login: login, Password: testPassword,
	}))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(begin.Msg.TotpSecret)
	if err != nil || begin.Msg.OptionsJson != "" {
		t.Fatalf("secret %q: %v", begin.Msg.TotpSecret, err)
	}
	fin, err := s.FinishSetup(ctx, connect.NewRequest(&adminv1.FinishSetupRequest{
		SetupToken: tok, CeremonyId: begin.Msg.CeremonyId, TotpCode: totpCode(secret, totpStep(s.now())),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fin.Header().Get("Set-Cookie"), CookieName+"=") {
		t.Error("setup did not open a session")
	}
	return fin.Msg.Admin.Id, secret
}

func login(s *Service, loginName, password, code string) (*connect.Response[adminv1.PasswordLoginResponse], error) {
	return s.PasswordLogin(context.Background(), connect.NewRequest(&adminv1.PasswordLoginRequest{Login: loginName, Password: password, TotpCode: code}))
}

func TestPasswordSetupAndLogin(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	adminID, secret := passwordAdmin(t, s, st, "  Ada.L ")

	// Stored: lower-case login, an argon2id hash, the secret only sealed.
	cred, err := st.PasswordByLogin(ctx, "ada.l")
	if err != nil || cred.AdminID != adminID || !strings.HasPrefix(cred.Hash, "$argon2id$") || strings.Contains(cred.Hash, testPassword) {
		t.Fatalf("stored credential: %+v %v", cred, err)
	}
	if strings.Contains(string(cred.TOTPSecret), string(secret)) {
		t.Error("TOTP secret stored in the clear")
	}
	if opened, err := s.vault.Open(cred.TOTPSecret, totpAAD(adminID)); err != nil || string(opened) != string(secret) {
		t.Errorf("sealed secret: %v", err)
	}
	if _, err := s.vault.Open(cred.TOTPSecret, totpAAD("adm_other")); err == nil {
		t.Error("sealed secret opens for another admin")
	}
	if n, _ := st.AdminCount(ctx); n != 1 {
		t.Fatal("admin not created")
	}
	if ok, _ := st.AnyPassword(ctx); !ok {
		t.Error("AnyPassword")
	}

	// The code used at setup is spent; the next step's code works once; login is case-insensitive.
	if _, err := login(s, "ada.l", testPassword, totpCode(secret, totpStep(*clock))); failureOf(t, err).AttemptsLeft != MaxSignInFailures-1 {
		t.Fatalf("replay of the setup code: %v", err)
	}
	*clock = clock.Add(totpPeriod * time.Second)
	code := totpCode(secret, totpStep(*clock))
	resp, err := login(s, "ADA.L", testPassword, code)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.Msg.Admin.Id != adminID || !strings.Contains(resp.Header().Get("Set-Cookie"), "HttpOnly") {
		t.Errorf("login response: %+v", resp.Msg)
	}
	if _, err := login(s, "ada.l", testPassword, code); err == nil {
		t.Fatal("the same code was accepted twice")
	}
	// A success cleared the failure counter (the replay above counted once, this is the second failure).
	*clock = clock.Add(totpPeriod * time.Second)
	if _, err := login(s, "ada.l", testPassword, totpCode(secret, totpStep(*clock))); err != nil {
		t.Fatalf("second login: %v", err)
	}
	if f, _ := st.LoginFailures(ctx, "ada.l", *clock, FailureWindow); f.Failures != 0 {
		t.Errorf("failures not cleared: %+v", f)
	}
	// Wrong password with a right code, wrong code with a right password: both fail alike.
	*clock = clock.Add(totpPeriod * time.Second)
	code = totpCode(secret, totpStep(*clock))
	if _, err := login(s, "ada.l", "wrong wrong wrong", code); err == nil {
		t.Error("wrong password accepted")
	}
	if _, err := login(s, "ada.l", testPassword, "000000"); err == nil {
		t.Error("wrong code accepted")
	}
	// The code that accompanied a wrong password was not burnt.
	if _, err := login(s, "ada.l", testPassword, code); err != nil {
		t.Errorf("code burnt by a failed attempt: %v", err)
	}
	// Audit rows carry the method and never the secrets.
	rows, _ := st.ListAudit(ctx, "", 0, 100)
	for _, r := range rows {
		if strings.Contains(r.Params, testPassword) || strings.Contains(r.Params, code) {
			t.Errorf("secret in audit row %+v", r)
		}
	}
	if rows[0].Action != "login" || rows[0].Result != "ok" || !strings.Contains(rows[0].Params, `"method":"password"`) {
		t.Errorf("last audit row: %+v", rows[0])
	}
}

func TestConcurrentWrongAndCorrectSetupCodes(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	token, err := IssueSetupToken(ctx, st, s.now())
	if err != nil {
		t.Fatal(err)
	}
	begin, err := s.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
		SetupToken: token, DisplayName: "Ada", Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD,
		Login: "ada", Password: testPassword,
	}))
	if err != nil {
		t.Fatal(err)
	}
	secret := secretOf(t, begin.Msg.TotpSecret)
	correct := totpCode(secret, totpStep(*clock))
	wrong := "000000"
	if wrong == correct {
		wrong = "000001"
	}
	finish := func(code string) error {
		_, err := s.FinishSetup(ctx, connect.NewRequest(&adminv1.FinishSetupRequest{
			SetupToken: token, CeremonyId: begin.Msg.CeremonyId, TotpCode: code,
		}))
		return err
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() { <-start; results <- finish(correct) }()
	go func() { <-start; results <- finish(wrong) }()
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if codeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("losing finish: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("two finishes succeeded %d times, want once", successes)
	}
	if n, err := st.AdminCount(ctx); err != nil || n != 1 {
		t.Fatalf("created admins = %d, err=%v; want one", n, err)
	}
}

func TestConcurrentWrongSetupCodesRespectLimit(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	token, err := IssueSetupToken(ctx, st, s.now())
	if err != nil {
		t.Fatal(err)
	}
	begin, err := s.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
		SetupToken: token, DisplayName: "Ada", Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD,
		Login: "ada", Password: testPassword,
	}))
	if err != nil {
		t.Fatal(err)
	}
	wrong := "000000"
	if wrong == totpCode(secretOf(t, begin.Msg.TotpSecret), totpStep(*clock)) {
		wrong = "000001"
	}
	const attempts = 100
	start := make(chan struct{})
	results := make(chan error, attempts)
	for range attempts {
		go func() {
			<-start
			_, err := s.FinishSetup(ctx, connect.NewRequest(&adminv1.FinishSetupRequest{
				SetupToken: token, CeremonyId: begin.Msg.CeremonyId, TotpCode: wrong,
			}))
			results <- err
		}()
	}
	close(start)
	invalidCodes := 0
	for range attempts {
		err := <-results
		if err == nil || codeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("wrong code result: %v", err)
		}
		if strings.Contains(err.Error(), "invalid code") {
			invalidCodes++
		}
	}
	if invalidCodes != maxSetupCodeTries {
		t.Fatalf("concurrent invalid-code answers = %d, want %d", invalidCodes, maxSetupCodeTries)
	}
	if _, ok, err := s.getCeremony(ctx, begin.Msg.CeremonyId, ceremonySetupPassword); err != nil || ok {
		t.Fatalf("ceremony after wrong codes: ok=%v err=%v; want it deleted", ok, err)
	}
	if n, err := st.AdminCount(ctx); err != nil || n != 0 {
		t.Fatalf("created admins = %d, err=%v; want none", n, err)
	}
}

func TestPasswordLockout(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	adminID, secret := passwordAdmin(t, s, st, "ada")
	*clock = clock.Add(time.Minute)

	var mu sync.Mutex
	var events []Event
	s.SetEventHook(func(e Event) { mu.Lock(); events = append(events, e); mu.Unlock() })

	// Four failures count down; the fifth locks.
	for want := MaxSignInFailures - 1; want >= 1; want-- {
		f := failureOf(t, errOf(login(s, "ada", "nope nope nope nope", "000000")))
		if int(f.AttemptsLeft) != want || f.LockedUntilUnix != 0 {
			t.Fatalf("attempts left %d (locked %d), want %d", f.AttemptsLeft, f.LockedUntilUnix, want)
		}
	}
	f := failureOf(t, errOf(login(s, "ada", "nope nope nope nope", "000000")))
	wantUntil := clock.Add(LockDuration).Unix()
	if f.AttemptsLeft != 0 || f.LockedUntilUnix != wantUntil {
		t.Fatalf("lockout: %+v, want until %d", f, wantUntil)
	}

	// Locked: the right credentials are refused too, and do not extend the lock.
	*clock = clock.Add(5 * time.Minute)
	f = failureOf(t, errOf(login(s, "ada", testPassword, totpCode(secret, totpStep(*clock)))))
	if f.LockedUntilUnix != wantUntil || f.AttemptsLeft != 0 {
		t.Fatalf("while locked: %+v", f)
	}
	// Unlocked after LockDuration: a fresh count, and the right credentials work.
	*clock = time.Unix(wantUntil, 0).Add(time.Second)
	if _, err := login(s, "ada", testPassword, totpCode(secret, totpStep(*clock))); err != nil {
		t.Fatalf("after the lock: %v", err)
	}
	if f := failureOf(t, errOf(login(s, "ada", "nope nope nope nope", "000000"))); f.AttemptsLeft != MaxSignInFailures-1 {
		t.Errorf("counter not reset after the lock: %+v", f)
	}

	// The bot hook heard about it: failures, then one lockout naming the admin and the end of the lock.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(events)
		mu.Unlock()
		if n >= 8 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	locks := 0
	for _, e := range events {
		if e.Kind == EventLockout {
			locks++
			if e.AdminID != adminID || e.Login != "ada" || e.Until.Unix() != wantUntil || e.Method != "password" {
				t.Errorf("lockout event: %+v", e)
			}
		}
	}
	if locks != 1 {
		t.Errorf("%d lockout events in %+v", locks, events)
	}
	rows, _ := st.ListAudit(ctx, "", 0, 100)
	var sawLock bool
	for _, r := range rows {
		sawLock = sawLock || (r.Action == "lockout" && r.Result == "locked")
	}
	if !sawLock {
		t.Error("no lockout audit row")
	}
}

// errOf drops the response so a call can be passed to failureOf.
func errOf[T any](_ T, err error) error { return err }

// An unknown login fails and locks exactly like a known one, so the responses do not
// reveal which logins exist, and only real logins reach the audit log.
func TestUnknownLoginIsIndistinguishable(t *testing.T) {
	s, st, _ := newTestService(t)
	ctx := context.Background()
	passwordAdmin(t, s, st, "ada")
	for _, name := range []string{"ada", "nobody"} {
		for want := MaxSignInFailures - 1; want >= 0; want-- {
			err := errOf(login(s, name, "wrong wrong wrong", "123456"))
			f := failureOf(t, err)
			if int(f.AttemptsLeft) != want || (want == 0) != (f.LockedUntilUnix != 0) {
				t.Fatalf("%s: attempts left %d locked %d, want %d", name, f.AttemptsLeft, f.LockedUntilUnix, want)
			}
			if err.Error() != errSignInFailed.Error() {
				t.Errorf("%s: message %q", name, err)
			}
		}
	}
	rows, _ := st.ListAudit(ctx, "", 0, 100)
	for _, r := range rows {
		if strings.Contains(r.Params, "nobody") {
			t.Errorf("made-up login reached the audit log: %+v", r)
		}
	}
	// A login that cannot exist (bad characters, absurd length) fails without touching the table.
	for _, bad := range []string{"", "a b", strings.Repeat("x", 500)} {
		if f := failureOf(t, errOf(login(s, bad, "x", "000000"))); f.AttemptsLeft != MaxSignInFailures {
			t.Errorf("%q: %+v", bad, f)
		}
	}
	if _, err := login(s, "ada", strings.Repeat("p", maxPasswordBytes+1), "000000"); err == nil {
		t.Error("oversized password reached the hash")
	}
}

func TestPasswordSetupValidation(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	tok, _ := IssueSetupToken(ctx, st, s.now())
	begin := func(login, pw string) error {
		_, err := s.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
			SetupToken: tok, Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD, Login: login, Password: pw,
		}))
		return err
	}
	for name, tc := range map[string][2]string{"short password": {"ada", "short"}, "bad login": {"a b", testPassword}, "empty login": {"", testPassword}} {
		if err := begin(tc[0], tc[1]); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Without a vault there is nowhere safe to keep the TOTP secret.
	v := s.vault
	s.vault = nil
	if err := begin("ada", testPassword); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("no vault: %v", err)
	}
	s.vault = v

	// A mistyped code may be retried on the same ceremony, five times in all.
	resp, err := s.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
		SetupToken: tok, Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD, Login: "ada", Password: testPassword,
	}))
	if err != nil {
		t.Fatal(err)
	}
	finish := func(code string) error {
		_, err := s.FinishSetup(ctx, connect.NewRequest(&adminv1.FinishSetupRequest{SetupToken: tok, CeremonyId: resp.Msg.CeremonyId, TotpCode: code}))
		return err
	}
	secret, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(resp.Msg.TotpSecret)
	good := totpCode(secret, totpStep(*clock))
	for i := range maxSetupCodeTries - 1 {
		if err := finish("000000"); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "invalid code") {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	if err := finish("000000"); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("last try: %v", err)
	}
	if err := finish(good); err == nil || strings.Contains(err.Error(), "invalid code") {
		t.Fatalf("ceremony survived %d wrong codes: %v", maxSetupCodeTries, err)
	}
	if n, _ := st.AdminCount(ctx); n != 0 {
		t.Fatal("admin created without a valid code")
	}
	// A typo followed by the right code succeeds.
	resp, _ = s.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
		SetupToken: tok, Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD, Login: "ada", Password: testPassword,
	}))
	secret, _ = base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(resp.Msg.TotpSecret)
	if err := finish("000000"); err == nil {
		t.Fatal("wrong code accepted")
	}
	if err := finish(totpCode(secret, totpStep(*clock))); err != nil {
		t.Fatalf("right code after a typo: %v", err)
	}
	// The token is spent: a second setup cannot start.
	if err := begin("eve", testPassword); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("second setup: %v", err)
	}
}

func TestCeremonyCapPerSource(t *testing.T) {
	s, _, _ := newTestService(t)
	ctx := context.Background()
	var finished string
	for i := range maxCeremoniesPerSource {
		id, err := s.putCeremony(ctx, &ceremony{kind: ceremonyLogin, src: "203.0.113.5"})
		if err != nil {
			t.Fatalf("ceremony %d refused: %v", i, err)
		}
		if i == 0 {
			finished = id
		}
	}
	if _, err := s.putCeremony(ctx, &ceremony{kind: ceremonyLogin, src: "203.0.113.5"}); err == nil {
		t.Fatal("source exceeded its cap")
	}
	if _, err := s.putCeremony(ctx, &ceremony{kind: ceremonyLogin, src: "203.0.113.6"}); err != nil {
		t.Fatalf("another source was affected: %v", err)
	}
	// Finished and expired ceremonies free the slot.
	_, ok, err := s.getCeremony(ctx, finished, ceremonyLogin)
	if err != nil || !ok {
		t.Fatalf("read ceremony to verify the source slot: ok=%v err=%v", ok, err)
	}
	if consumed, err := s.st.ConsumeAuthCeremony(ctx, finished); err != nil || !consumed {
		t.Fatalf("consume ceremony to free source slot: consumed=%v err=%v", consumed, err)
	}
	if _, err := s.putCeremony(ctx, &ceremony{kind: ceremonyLogin, src: "203.0.113.5"}); err != nil {
		t.Fatalf("slot not freed: %v", err)
	}
	// The global cap holds however many sources there are.
	s2, _, _ := newTestService(t)
	for i := range maxCeremonies {
		if _, err := s2.putCeremony(ctx, &ceremony{kind: ceremonyLogin, src: string(rune('A'+i%50)) + string(rune('a'+i/50))}); err != nil {
			t.Fatalf("ceremony %d: %v", i, err)
		}
	}
	if _, err := s2.putCeremony(ctx, &ceremony{kind: ceremonyLogin, src: "fresh"}); err == nil {
		t.Fatal("global cap not enforced")
	}
}
