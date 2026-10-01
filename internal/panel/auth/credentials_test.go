package auth

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// signedInPassword signs the password admin in (a step after the last code used) and returns the session cookie.
func signedInPassword(t *testing.T, s *Service, clock *time.Time, loginName, password string, secret []byte) string {
	t.Helper()
	*clock = clock.Add(totpPeriod * time.Second)
	r, err := login(s, loginName, password, totpCode(secret, totpStep(*clock)))
	if err != nil {
		t.Fatalf("sign in: %v", err)
	}
	return sessionCookie(t, r.Header())
}

func wantCoded(t *testing.T, err error, code connect.Code, msg string) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != code || ce.Message() != msg {
		t.Fatalf("want %v %q, got %v", code, msg, err)
	}
}

func changePassword(s *Service, cookie, current, next string) (*connect.Response[adminv1.ChangePasswordResponse], error) {
	return s.ChangePassword(authedCtx(s, cookie), cookieReq(&adminv1.ChangePasswordRequest{CurrentPassword: current, NewPassword: next}, cookie))
}

// authedCtx is authed without a *testing.T, for the helpers that build calls.
func authedCtx(s *Service, cookie string) context.Context {
	sess, admin, err := s.resolveSession(context.Background(), cookie)
	if err != nil {
		return context.Background()
	}
	return context.WithValue(context.WithValue(context.Background(), adminKey{}, admin), sessionKey{}, sess)
}

func TestGetPasswordLogin(t *testing.T) {
	s, st, clock := newTestService(t)
	_, secret := passwordAdmin(t, s, st, "Ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, secret)
	r, err := s.GetPasswordLogin(authed(t, s, cookie), connect.NewRequest(&adminv1.GetPasswordLoginRequest{}))
	if err != nil || !r.Msg.Enabled || r.Msg.Login != "ada" || !r.Msg.Available {
		t.Fatalf("password admin: %+v %v", r, err)
	}

	s2, st2, _ := newTestService(t)
	_, pkCookie, _ := passkeyAdmin(t, s2, st2)
	r, err = s2.GetPasswordLogin(authed(t, s2, pkCookie), connect.NewRequest(&adminv1.GetPasswordLoginRequest{}))
	if err != nil || r.Msg.Enabled || r.Msg.Login != "" || !r.Msg.Available {
		t.Fatalf("passkey admin: %+v %v", r, err)
	}
	s2.vault = nil
	if r, _ := s2.GetPasswordLogin(authed(t, s2, pkCookie), connect.NewRequest(&adminv1.GetPasswordLoginRequest{})); r.Msg.Available {
		t.Error("a server without a master key says it can keep a password login")
	}
	if _, err := s2.GetPasswordLogin(context.Background(), connect.NewRequest(&adminv1.GetPasswordLoginRequest{})); codeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("without a session: %v", err)
	}
}

func TestChangePassword(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, secret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, secret)
	other, _ := s.newSession(context.Background(), adminID, "198.51.100.9", "other browser")
	const next = "a whole new passphrase here"

	// A stale session asks for a step-up first, before the password is even looked at.
	*clock = clock.Add(StepUpWindow + time.Second)
	if need := stepUpRequired(t, errOf(changePassword(s, cookie, testPassword, next))); !need.Totp {
		t.Errorf("StepUpRequired = %+v", need)
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := s.FinishStepUp(authed(t, s, cookie), cookieReq(&adminv1.FinishStepUpRequest{TotpCode: totpCode(secret, totpStep(*clock))}, cookie)); err != nil {
		t.Fatalf("step-up: %v", err)
	}

	// The rules of a new password; nothing is spent on them.
	if _, err := changePassword(s, cookie, testPassword, "short"); codeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("short new password: %v", err)
	}
	// A wrong current password is refused, audited and counted against the step-up lock.
	wantCoded(t, errOf(changePassword(s, cookie, "not the password at all", next)), connect.CodeInvalidArgument, "wrong_password")
	if f, _ := st.LoginFailures(context.Background(), stepUpLockKey+adminID, *clock, FailureWindow); f.Failures != 1 {
		t.Errorf("failures counted: %+v", f)
	}
	if n := countAuditResult(t, st, "password_change", "fail"); n != 1 {
		t.Errorf("failed change audited %d times", n)
	}
	// The right one: the hash changes, the other sessions end (setup's and the other browser's), this one stays, the
	// failures are forgotten.
	r, err := changePassword(s, cookie, testPassword, next)
	if err != nil || r.Msg.EndedSessions != 2 {
		t.Fatalf("change: %+v %v", r, err)
	}
	if _, err := s.resolve(context.Background(), other.Value); err == nil {
		t.Error("the other session survived a password change")
	}
	if _, err := s.resolve(context.Background(), cookie); err != nil {
		t.Errorf("the current session ended: %v", err)
	}
	if f, _ := st.LoginFailures(context.Background(), stepUpLockKey+adminID, *clock, FailureWindow); f.Failures != 0 {
		t.Errorf("failures not cleared: %+v", f)
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := login(s, "ada", testPassword, totpCode(secret, totpStep(*clock))); err == nil {
		t.Error("the old password still signs in")
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := login(s, "ada", next, totpCode(secret, totpStep(*clock))); err != nil {
		t.Errorf("the new password does not sign in: %v", err)
	}
	// The audit rows never hold a password.
	rows, _ := st.ListAudit(context.Background(), "", 0, 100)
	for _, row := range rows {
		if strings.Contains(row.Params, testPassword) || strings.Contains(row.Params, next) {
			t.Errorf("a password in the audit log: %+v", row)
		}
	}

	// Five wrong current passwords lock it (and step-up with it), even for the right one afterwards.
	cookie = signedInPassword(t, s, clock, "ada", next, secret)
	for range MaxSignInFailures {
		changePassword(s, cookie, "wrong wrong wrong wrong", "yet another passphrase")
	}
	if _, err := changePassword(s, cookie, next, "yet another passphrase"); codeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("after five wrong passwords: %v", err)
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := s.FinishStepUp(authed(t, s, cookie), cookieReq(&adminv1.FinishStepUpRequest{TotpCode: totpCode(secret, totpStep(*clock))}, cookie)); codeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("step-up while locked: %v", err)
	}

	// A passkey admin has no password to change.
	s2, st2, _ := newTestService(t)
	_, pkCookie, _ := passkeyAdmin(t, s2, st2)
	wantCoded(t, errOf(changePassword(s2, pkCookie, "x", next)), connect.CodeFailedPrecondition, "no_password_login")
}

func countAuditResult(t *testing.T, st *store.Store, action, result string) int {
	t.Helper()
	var n int
	if err := st.R.QueryRow(`SELECT count(*) FROM audit WHERE action = ? AND result = ?`, action, result).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func beginTOTP(s *Service, cookie, loginName, password string) (*connect.Response[adminv1.BeginTotpEnrollmentResponse], error) {
	return s.BeginTotpEnrollment(authedCtx(s, cookie), cookieReq(&adminv1.BeginTotpEnrollmentRequest{Login: loginName, Password: password}, cookie))
}

func finishTOTP(s *Service, cookie, ceremony, code string) (*connect.Response[adminv1.FinishTotpEnrollmentResponse], error) {
	return s.FinishTotpEnrollment(authedCtx(s, cookie), cookieReq(&adminv1.FinishTotpEnrollmentRequest{CeremonyId: ceremony, TotpCode: code}, cookie))
}

func secretOf(t *testing.T, b32text string) []byte {
	t.Helper()
	sec, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(b32text)
	if err != nil || len(sec) != 20 {
		t.Fatalf("secret %q: %v", b32text, err)
	}
	return sec
}

func TestRebindAuthenticator(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, oldSecret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, oldSecret)
	other, _ := s.newSession(context.Background(), adminID, "", "other")

	// Begin needs a step-up; a re-bind takes no login or password.
	*clock = clock.Add(StepUpWindow + time.Second)
	stepUpRequired(t, errOf(beginTOTP(s, cookie, "", "")))
	*clock = clock.Add(31 * time.Second)
	if _, err := s.FinishStepUp(authed(t, s, cookie), cookieReq(&adminv1.FinishStepUpRequest{TotpCode: totpCode(oldSecret, totpStep(*clock))}, cookie)); err != nil {
		t.Fatal(err)
	}
	wantCoded(t, errOf(beginTOTP(s, cookie, "ada", testPassword)), connect.CodeInvalidArgument, "password_login_exists")
	b, err := beginTOTP(s, cookie, "", "")
	if err != nil || !strings.HasPrefix(b.Msg.TotpUri, "otpauth://totp/") || !strings.Contains(b.Msg.TotpUri, "ada") {
		t.Fatalf("begin: %+v %v", b, err)
	}
	newSecret := secretOf(t, b.Msg.TotpSecret)
	if bytes.Equal(newSecret, oldSecret) {
		t.Fatal("the same secret again")
	}
	// Nothing is stored before the code comes back: the old app still signs in.
	stored, _ := st.PasswordByAdmin(context.Background(), adminID)
	if opened, _ := s.vault.Open(stored.TOTPSecret, totpAAD(adminID)); !bytes.Equal(opened, oldSecret) {
		t.Fatal("the secret changed before it was confirmed")
	}

	// Another session of the same admin cannot finish it (the attempt spends the ceremony: begin again).
	wantCoded(t, errOf(finishTOTP(s, other.Value, b.Msg.CeremonyId, totpCode(newSecret, totpStep(*clock)))), connect.CodeInvalidArgument, "enrollment_expired")
	b, _ = beginTOTP(s, cookie, "", "")
	newSecret = secretOf(t, b.Msg.TotpSecret)
	// A code of the old app, a typo: refused, and the same ceremony can still be finished.
	wantCoded(t, errOf(finishTOTP(s, cookie, b.Msg.CeremonyId, totpCode(oldSecret, totpStep(*clock)))), connect.CodeInvalidArgument, "invalid_code")
	wantCoded(t, errOf(finishTOTP(s, cookie, b.Msg.CeremonyId, "000000")), connect.CodeInvalidArgument, "invalid_code")
	code := totpCode(newSecret, totpStep(*clock))
	f, err := finishTOTP(s, cookie, b.Msg.CeremonyId, code)
	if err != nil || f.Msg.Login != "ada" || f.Msg.EndedSessions != 2 { // setup's session and the other browser's
		t.Fatalf("finish: %+v %v", f, err)
	}
	if _, err := s.resolve(context.Background(), other.Value); err == nil {
		t.Error("the other session survived a re-bind")
	}
	// Single use: the ceremony is gone.
	wantCoded(t, errOf(finishTOTP(s, cookie, b.Msg.CeremonyId, code)), connect.CodeInvalidArgument, "enrollment_expired")
	// The code that confirmed the app cannot sign in (it is spent), the old app's codes never again, the next one can.
	if _, err := login(s, "ada", testPassword, code); err == nil {
		t.Error("the confirming code was replayed at sign-in")
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := login(s, "ada", testPassword, totpCode(oldSecret, totpStep(*clock))); err == nil {
		t.Error("the old app still signs in")
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := login(s, "ada", testPassword, totpCode(newSecret, totpStep(*clock))); err != nil {
		t.Errorf("the new app does not sign in: %v", err)
	}
	if countAuditResult(t, st, "totp_rebind", "ok") != 1 {
		t.Error("the re-bind is not audited")
	}
	rows, _ := st.ListAudit(context.Background(), "", 0, 100)
	for _, row := range rows {
		if strings.Contains(row.Params, b.Msg.TotpSecret) {
			t.Errorf("the secret in the audit log: %+v", row)
		}
	}
}

func TestEnrollmentCeremonyLimits(t *testing.T) {
	s, st, clock := newTestService(t)
	_, secret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, secret)
	b, err := beginTOTP(s, cookie, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Five wrong codes and the ceremony is gone, even for the right code.
	for range maxSetupCodeTries {
		finishTOTP(s, cookie, b.Msg.CeremonyId, "000000")
	}
	wantCoded(t, errOf(finishTOTP(s, cookie, b.Msg.CeremonyId, totpCode(secretOf(t, b.Msg.TotpSecret), totpStep(*clock)))), connect.CodeInvalidArgument, "enrollment_expired")
	// Five minutes and it expires.
	b, _ = beginTOTP(s, cookie, "", "")
	*clock = clock.Add(CeremonyTTL + time.Second)
	wantCoded(t, errOf(finishTOTP(s, cookie, b.Msg.CeremonyId, totpCode(secretOf(t, b.Msg.TotpSecret), totpStep(*clock)))), connect.CodeInvalidArgument, "enrollment_expired")
	// Another admin's ceremony is unknown to this one.
	*clock = clock.Add(31 * time.Second)
	cookie = signedInPassword(t, s, clock, "ada", testPassword, secret)
	b, _ = beginTOTP(s, cookie, "", "")
	helper := insertAdmin(t, st, store.RoleHelper)
	hc, _ := s.newSession(context.Background(), helper.ID, "", "")
	wantCoded(t, errOf(finishTOTP(s, hc.Value, b.Msg.CeremonyId, totpCode(secretOf(t, b.Msg.TotpSecret), totpStep(*clock)))), connect.CodeInvalidArgument, "enrollment_expired")
	// No master key: nothing to keep a secret with.
	s.vault = nil
	wantCoded(t, errOf(beginTOTP(s, cookie, "", "")), connect.CodeFailedPrecondition, "no_master_key")
}

func TestPasskeyAdminAddsPasswordLogin(t *testing.T) {
	s, st, clock := newTestService(t)
	admin, cookie, _ := passkeyAdmin(t, s, st)
	if li, _ := s.GetLoginInfo(context.Background(), connect.NewRequest(&adminv1.GetLoginInfoRequest{})); li.Msg.PasswordLogin {
		t.Fatal("password sign-in offered before anyone has a password")
	}
	// The rules of a login and a password; a login another admin has.
	other := insertAdmin(t, st, store.RoleHelper)
	if err := st.AddPassword(context.Background(), store.PasswordCred{AdminID: other.ID, Login: "taken", Hash: "x", TOTPSecret: []byte{1}}, *clock); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string][2]string{"empty login": {"", testPassword}, "bad login": {"a b", testPassword}, "short password": {"ada", "short"}} {
		if _, err := beginTOTP(s, cookie, tc[0], tc[1]); codeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	wantCoded(t, errOf(beginTOTP(s, cookie, "Taken", testPassword)), connect.CodeAlreadyExists, "login_taken")

	b, err := beginTOTP(s, cookie, " Ada ", testPassword)
	if err != nil {
		t.Fatal(err)
	}
	sec := secretOf(t, b.Msg.TotpSecret)
	if _, err := st.PasswordByAdmin(context.Background(), admin.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("the login exists before its code was confirmed")
	}
	f, err := finishTOTP(s, cookie, b.Msg.CeremonyId, totpCode(sec, totpStep(*clock)))
	if err != nil || f.Msg.Login != "ada" || f.Msg.EndedSessions != 0 {
		t.Fatalf("finish: %+v %v", f, err)
	}
	cred, err := st.PasswordByAdmin(context.Background(), admin.ID)
	if err != nil || cred.Login != "ada" || !strings.HasPrefix(cred.Hash, "$argon2id$") || bytes.Contains(cred.TOTPSecret, sec) {
		t.Fatalf("stored: %+v %v", cred, err)
	}
	if li, _ := s.GetLoginInfo(context.Background(), connect.NewRequest(&adminv1.GetLoginInfoRequest{})); !li.Msg.PasswordLogin {
		t.Error("the sign-in page does not offer the password form now")
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := login(s, "ada", testPassword, totpCode(sec, totpStep(*clock))); err != nil {
		t.Errorf("the new password login does not sign in: %v", err)
	}
	// Step-up now offers the code as well as the passkey; a second add is a re-bind.
	*clock = clock.Add(StepUpWindow + time.Second)
	if need := stepUpRequired(t, errOf(beginTOTP(s, cookie, "", ""))); !need.Totp || !need.Passkey {
		t.Errorf("StepUpRequired = %+v", need)
	}
	if countAuditResult(t, st, "password_add", "ok") != 1 {
		t.Error("the new login is not audited")
	}
}

// --- the operator's reset ---

func TestResetLogin(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	adminID, oldSecret := passwordAdmin(t, s, st, "ada")
	cookie := signedInPassword(t, s, clock, "ada", testPassword, oldSecret)
	// Locked out: five wrong passwords, and a step-up lock too.
	for range MaxSignInFailures {
		login(s, "ada", "wrong wrong wrong", "000000")
	}
	for range MaxSignInFailures {
		s.FinishStepUp(authed(t, s, cookie), cookieReq(&adminv1.FinishStepUpRequest{TotpCode: "000000"}, cookie))
	}

	r, err := ResetLogin(ctx, st, s.vault, ResetLoginInput{Login: "ADA"}, *clock)
	if err != nil || r.Created || r.Login != "ada" || r.Admin.ID != adminID || r.SessionsEnded != 2 || len(r.Password) != 24 { // setup's and the sign-in's
		t.Fatalf("reset: %+v %v", r, err)
	}
	newSecret := secretOf(t, r.TOTPSecret)
	if _, err := s.resolve(ctx, cookie); err == nil {
		t.Error("a session survived the reset (a lost phone may hold one)")
	}
	if f, _ := st.LoginFailures(ctx, "ada", *clock, FailureWindow); f.Locked(*clock) {
		t.Error("the login is still locked")
	}
	if f, _ := st.LoginFailures(ctx, stepUpLockKey+adminID, *clock, FailureWindow); f.Locked(*clock) {
		t.Error("step-up is still locked")
	}
	// The old password and the old app are dead, the new pair signs in.
	*clock = clock.Add(31 * time.Second)
	if _, err := login(s, "ada", testPassword, totpCode(newSecret, totpStep(*clock))); err == nil {
		t.Error("the old password still signs in")
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := login(s, "ada", r.Password, totpCode(oldSecret, totpStep(*clock))); err == nil {
		t.Error("the old app still signs in")
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := login(s, "ada", r.Password, totpCode(newSecret, totpStep(*clock))); err != nil {
		t.Fatalf("the new pair does not sign in: %v", err)
	}
	// One audit row, the command line as the actor, no secret in it.
	rows, _ := st.ListAudit(ctx, "", 0, 100)
	n := 0
	for _, row := range rows {
		if row.Action == "reset_login" {
			n++
			if row.Actor != "cli" || !strings.Contains(row.Params, `"login":"ada"`) || strings.Contains(row.Params, r.Password) || strings.Contains(row.Params, r.TOTPSecret) {
				t.Errorf("audit row: %+v", row)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d reset rows", n)
	}
	// A typo of the only admin's login says which one it is.
	var te *ResetTargetError
	if _, err := ResetLogin(ctx, st, s.vault, ResetLoginInput{Login: "adda"}, *clock); !errors.As(err, &te) || !strings.Contains(err.Error(), `"ada"`) {
		t.Errorf("a typo: %v", err)
	}
	// Without a master key there is nothing to seal the secret with.
	if _, err := ResetLogin(ctx, st, nil, ResetLoginInput{Login: "ada"}, *clock); err == nil {
		t.Error("reset without a master key")
	}
	if _, err := ResetLogin(ctx, st, s.vault, ResetLoginInput{Login: "a b"}, *clock); err == nil {
		t.Error("a login that cannot exist")
	}
}

func TestResetLoginGivesAPasskeyAdminAPasswordLogin(t *testing.T) {
	s, st, clock := newTestService(t)
	ctx := context.Background()
	admin, cookie, _ := passkeyAdmin(t, s, st)

	// The only admin: no --admin needed.
	r, err := ResetLogin(ctx, st, s.vault, ResetLoginInput{Login: "owner"}, *clock)
	if err != nil || !r.Created || r.Admin.ID != admin.ID || r.SessionsEnded != 1 {
		t.Fatalf("reset: %+v %v", r, err)
	}
	if _, err := s.resolve(ctx, cookie); err == nil {
		t.Error("the session survived")
	}
	if pks, _ := st.PasskeysByAdmin(ctx, admin.ID); len(pks) != 1 {
		t.Error("the passkeys must stay")
	}
	*clock = clock.Add(31 * time.Second)
	if _, err := login(s, "owner", r.Password, totpCode(secretOf(t, r.TOTPSecret), totpStep(*clock))); err != nil {
		t.Fatalf("the new login does not sign in: %v", err)
	}

	// With several admins and an unknown login, the operator must say whose it is.
	helper := insertAdmin(t, st, store.RoleHelper)
	var te *ResetTargetError
	if _, err := ResetLogin(ctx, st, s.vault, ResetLoginInput{Login: "helper"}, *clock); !errors.As(err, &te) || !strings.Contains(err.Error(), "--admin") {
		t.Errorf("several admins, no --admin: %v", err)
	}
	if _, err := ResetLogin(ctx, st, s.vault, ResetLoginInput{Login: "helper", AdminID: "adm_nope"}, *clock); !errors.As(err, &te) {
		t.Errorf("an unknown admin: %v", err)
	}
	// A login that belongs to someone else is not moved to another admin.
	if _, err := ResetLogin(ctx, st, s.vault, ResetLoginInput{Login: "owner", AdminID: helper.ID}, *clock); !errors.As(err, &te) {
		t.Errorf("another admin's login: %v", err)
	}
	// An admin who already signs in under another name is pointed at it.
	if _, err := ResetLogin(ctx, st, s.vault, ResetLoginInput{Login: "boss", AdminID: admin.ID}, *clock); !errors.As(err, &te) || !strings.Contains(err.Error(), `"owner"`) {
		t.Errorf("a second login for one admin: %v", err)
	}
	r, err = ResetLogin(ctx, st, s.vault, ResetLoginInput{Login: "helper", AdminID: helper.ID}, *clock)
	if err != nil || !r.Created || r.Admin.ID != helper.ID {
		t.Fatalf("helper: %+v %v", r, err)
	}
	// The secret is sealed for that admin only.
	cred, _ := st.PasswordByAdmin(ctx, helper.ID)
	other, _ := vault.New(bytes.Repeat([]byte{7}, vault.KeySize))
	if _, err := other.Open(cred.TOTPSecret, totpAAD(helper.ID)); err == nil {
		t.Error("the secret opens with another master key")
	}
	if _, err := s.vault.Open(cred.TOTPSecret, totpAAD(admin.ID)); err == nil {
		t.Error("the secret opens as another admin's")
	}
}

// The audit page's "kind" chips: sign-ins and security, what people changed, what failed.
func TestListAuditByKind(t *testing.T) {
	s, st, _ := newTestService(t)
	octx, _ := ownerCtx(t, s, st)
	for _, r := range [][2]string{
		{"login", "fail"}, {"password_change", "ok"}, {"page_unlock_lockout", "locked"}, {"user_delete", "ok"}, {"inbound_add", "ok"},
		{"preset_default", "ok"}, {"node.awg_prepare", "ok"}, {"call", "ok"}, {"mcp_plan", "ok"}, {"security_update", "rejected"},
	} {
		if err := st.Audit(context.Background(), s.now(), store.AuditEntry{Actor: "a", Action: r[0], Result: r[1]}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(kind adminv1.AuditKind) string {
		t.Helper()
		r, err := s.ListAudit(octx, connect.NewRequest(&adminv1.ListAuditRequest{Kind: kind}))
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for i := len(r.Msg.Entries) - 1; i >= 0; i-- {
			out = append(out, r.Msg.Entries[i].Action)
		}
		return strings.Join(out, " ")
	}
	if got := list(adminv1.AuditKind_AUDIT_KIND_SIGN_IN); got != "login password_change page_unlock_lockout" {
		t.Errorf("sign-in: %s", got)
	}
	if got := list(adminv1.AuditKind_AUDIT_KIND_CHANGES); got != "user_delete inbound_add preset_default node.awg_prepare security_update" {
		t.Errorf("changes: %s", got)
	}
	if got := list(adminv1.AuditKind_AUDIT_KIND_FAILURES); got != "login page_unlock_lockout security_update" {
		t.Errorf("failures: %s", got)
	}
	if got := list(adminv1.AuditKind_AUDIT_KIND_UNSPECIFIED); !strings.Contains(got, "call") || !strings.Contains(got, "mcp_plan") {
		t.Errorf("all: %s", got)
	}
	if _, err := s.ListAudit(octx, connect.NewRequest(&adminv1.ListAuditRequest{Kind: adminv1.AuditKind(42)})); codeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("unknown kind: %v", err)
	}
}

func TestRandomPassword(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		p := randomPassword()
		if len(p) != 24 || strings.Count(p, "-") != 4 || checkPasswordPolicy(p) != nil || strings.ContainsAny(p, "01ilo") {
			t.Fatalf("password %q", p)
		}
		if seen[p] {
			t.Fatalf("repeated %q", p)
		}
		seen[p] = true
	}
}
