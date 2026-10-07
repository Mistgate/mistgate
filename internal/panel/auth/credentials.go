package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// Settings -> Security -> "Password and code": the signed-in admin's own password + authenticator-code sign-in, and the
// operator's way back in from the panel server (ResetLogin, `mistgate auth reset-login`).
//
// The rules that keep this from being a back door:
//   - every change needs a fresh step-up (a passkey or a code of the current app), like adding a passkey: a stolen
//     session cookie alone changes nothing;
//   - a new password also needs the current one, and a wrong one counts against the step-up lock (five wrong proofs lock
//     step-up for LockDuration);
//   - a new authenticator secret is stored only after a code of it has been typed back (the setup wizard's rule), on
//     a ceremony bound to the admin and to this session, single-use, 5 minutes;
//   - replacing a credential (password, app) ends the admin's other sessions; the operator's reset ends all of them;
//   - everything is in the audit log, the secrets never are.

const ceremonyTOTPEnroll = "totp-enroll"

// codedErr is a connect error whose message is a stable code the SPA words itself (web/src/lib/errors.ts).
func codedErr(c connect.Code, code string) error { return connect.NewError(c, errors.New(code)) }

// GetPasswordLogin says whether the signed-in admin has a password login, and under which login.
func (s *Service) GetPasswordLogin(ctx context.Context, _ *connect.Request[adminv1.GetPasswordLoginRequest]) (*connect.Response[adminv1.GetPasswordLoginResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	resp := &adminv1.GetPasswordLoginResponse{Available: s.vault != nil}
	switch c, err := s.st.PasswordByAdmin(ctx, admin.ID); {
	case err == nil:
		resp.Enabled, resp.Login = true, c.Login
	case !errors.Is(err, store.ErrNotFound):
		s.log.Error("password credential", "err", err)
		return nil, errInternal(err)
	}
	return connect.NewResponse(resp), nil
}

// ChangePassword sets a new password for the signed-in admin's password login: step-up, then the current password.
func (s *Service) ChangePassword(ctx context.Context, req *connect.Request[adminv1.ChangePasswordRequest]) (*connect.Response[adminv1.ChangePasswordResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	m, now, ip := req.Msg, s.now(), s.clientIP(req)
	cred, err := s.st.PasswordByAdmin(ctx, admin.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, codedErr(connect.CodeFailedPrecondition, "no_password_login")
	} else if err != nil {
		s.log.Error("password credential", "err", err)
		return nil, errInternal(err)
	}
	if err := checkPasswordPolicy(m.NewPassword); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	key := stepUpLockKey + admin.ID // a wrong current password is a wrong proof, like a wrong step-up code
	if prior, err := s.st.LoginFailures(ctx, key, now, FailureWindow); err != nil {
		s.log.Error("step-up failures", "err", err)
		return nil, errInternal(err)
	} else if prior.Locked(now) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many wrong attempts, try again later"))
	}
	if len(m.CurrentPassword) > maxPasswordBytes {
		m.CurrentPassword = "" // never matches; the hash is not asked to chew on a megabyte
	}
	var ok bool
	if err := s.hashed(ctx, func() { ok = verifyPassword(cred.Hash, m.CurrentPassword) }); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("server is busy, try again"))
	}
	if !ok {
		if _, err := s.st.RecordLoginFailure(ctx, key, now, MaxSignInFailures, FailureWindow, LockDuration); err != nil {
			s.log.Error("record step-up failure", "err", err)
		}
		s.audit(ctx, admin.ID, "password_change", "fail", ip, map[string]any{"reason": "wrong current password"})
		return nil, codedErr(connect.CodeInvalidArgument, "wrong_password")
	}
	var hash string
	if err := s.hashed(ctx, func() { hash = hashPassword(m.NewPassword) }); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("server is busy, try again"))
	}
	if err := s.st.SetPasswordHash(ctx, admin.ID, hash); err != nil {
		s.log.Error("set password", "err", err)
		return nil, errInternal(err)
	}
	ended, err := s.st.DeleteOtherSessions(ctx, admin.ID, currentHash(req))
	if err != nil {
		s.log.Error("end other sessions", "err", err)
		return nil, errInternal(err)
	}
	if err := s.st.ClearLoginFailures(ctx, key); err != nil {
		s.log.Warn("clear step-up failures", "err", err)
	}
	s.audit(ctx, admin.ID, "password_change", "ok", ip, map[string]any{"login": cred.Login, "sessions_ended": ended})
	s.emit(Event{Kind: EventPasswordChanged, AdminID: admin.ID, Login: cred.Login, Method: "password", IP: ip.String()})
	return connect.NewResponse(&adminv1.ChangePasswordResponse{EndedSessions: uint32(ended)}), nil
}

// BeginTotpEnrollment makes a new authenticator secret for the signed-in admin: a re-bind of the app for an admin with
// a password login, or (login + password given) a new password login for a passkey admin. Nothing is stored until
// FinishTotpEnrollment gets a code of the new secret.
func (s *Service) BeginTotpEnrollment(ctx context.Context, req *connect.Request[adminv1.BeginTotpEnrollmentRequest]) (*connect.Response[adminv1.BeginTotpEnrollmentResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	// Step-up guards the Begin: Finish needs the ceremony that only a Begin of this session hands out.
	if err := s.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	if s.vault == nil {
		return nil, codedErr(connect.CodeFailedPrecondition, "no_master_key")
	}
	m := req.Msg
	c := &ceremony{kind: ceremonyTOTPEnroll, src: SourceKey(s.clientIP(req)), admin: admin, tokenHash: currentHash(req)}
	switch cred, err := s.st.PasswordByAdmin(ctx, admin.ID); {
	case err == nil: // re-bind: the login and the password stay as they are
		if m.Login != "" || m.Password != "" {
			return nil, codedErr(connect.CodeInvalidArgument, "password_login_exists")
		}
		c.login = cred.Login
	case errors.Is(err, store.ErrNotFound): // a passkey admin adds the password login
		if c.login, err = normLogin(m.Login); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if err := checkPasswordPolicy(m.Password); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if _, err := s.st.PasswordByLogin(ctx, c.login); err == nil {
			return nil, codedErr(connect.CodeAlreadyExists, "login_taken")
		} else if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("password lookup", "err", err)
			return nil, errInternal(err)
		}
		if err := s.hashed(ctx, func() { c.pwHash = hashPassword(m.Password) }); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("server is busy, try again"))
		}
	default:
		s.log.Error("password credential", "err", err)
		return nil, errInternal(err)
	}
	c.totp = newTOTPSecret()
	id, err := s.putCeremony(ctx, c)
	if err != nil {
		return nil, ceremonyPutError(err)
	}
	return connect.NewResponse(&adminv1.BeginTotpEnrollmentResponse{
		CeremonyId: id, TotpUri: totpURI(s.brandName(ctx), c.login, c.totp), TotpSecret: b32.EncodeToString(c.totp),
	}), nil
}

// FinishTotpEnrollment checks a code of the new secret and stores it: the re-bound app, or the new password login.
func (s *Service) FinishTotpEnrollment(ctx context.Context, req *connect.Request[adminv1.FinishTotpEnrollmentRequest]) (*connect.Response[adminv1.FinishTotpEnrollmentResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	m, now, ip := req.Msg, s.now(), s.clientIP(req)
	c, ok, err := s.getCeremony(ctx, m.CeremonyId, ceremonyTOTPEnroll)
	if err != nil {
		return nil, errInternal(err)
	}
	expired := func() error { return codedErr(connect.CodeInvalidArgument, "enrollment_expired") }
	consume := func() error {
		won, err := s.consumeCeremony(ctx, m.CeremonyId, c)
		if err != nil {
			return errInternal(err)
		}
		if !won {
			return expired()
		}
		return nil
	}
	// another admin's ceremony, or one begun in another session, is as good as unknown
	if !ok || c.admin.ID != admin.ID || !bytes.Equal(c.tokenHash, currentHash(req)) {
		if ok {
			if err := consume(); err != nil {
				return nil, err
			}
		}
		return nil, expired()
	}
	step, ok := matchTOTP(c.totp, m.TotpCode, now)
	if !ok {
		if _, err := s.failCeremony(ctx, m.CeremonyId, c); err != nil {
			return nil, errInternal(err)
		}
		return nil, codedErr(connect.CodeInvalidArgument, "invalid_code")
	}
	if s.vault == nil {
		if err := consume(); err != nil {
			return nil, err
		}
		return nil, codedErr(connect.CodeFailedPrecondition, "no_master_key")
	}
	if err := consume(); err != nil {
		return nil, err
	}
	sealed := s.vault.Seal(c.totp, totpAAD(admin.ID))
	resp := &adminv1.FinishTotpEnrollmentResponse{Login: c.login}
	if c.pwHash == "" { // re-bind
		switch err := s.st.SetTOTPSecret(ctx, admin.ID, sealed, step); {
		case errors.Is(err, store.ErrNotFound):
			return nil, codedErr(connect.CodeFailedPrecondition, "no_password_login")
		case err != nil:
			s.log.Error("set totp secret", "err", err)
			return nil, errInternal(err)
		}
		ended, err := s.st.DeleteOtherSessions(ctx, admin.ID, currentHash(req))
		if err != nil {
			s.log.Error("end other sessions", "err", err)
			return nil, errInternal(err)
		}
		resp.EndedSessions = uint32(ended)
		s.audit(ctx, admin.ID, "totp_rebind", "ok", ip, map[string]any{"login": c.login, "sessions_ended": ended})
		s.emit(Event{Kind: EventTOTPRebound, AdminID: admin.ID, Login: c.login, Method: "password", IP: ip.String()})
		return connect.NewResponse(resp), nil
	}
	switch err := s.st.AddPassword(ctx, store.PasswordCred{AdminID: admin.ID, Login: c.login, Hash: c.pwHash, TOTPSecret: sealed, TOTPStep: step}, now); {
	case errors.Is(err, store.ErrLoginTaken):
		return nil, codedErr(connect.CodeAlreadyExists, "login_taken")
	case errors.Is(err, store.ErrAccessExists):
		return nil, codedErr(connect.CodeInvalidArgument, "password_login_exists")
	case err != nil:
		s.log.Error("add password login", "err", err)
		return nil, errInternal(err)
	}
	s.audit(ctx, admin.ID, "password_add", "ok", ip, map[string]any{"login": c.login})
	s.emit(Event{Kind: EventPasswordAdded, AdminID: admin.ID, Login: c.login, Method: "password", IP: ip.String()})
	return connect.NewResponse(resp), nil
}

// --- the operator's way back in ---

// ResetLoginInput says whose login `mistgate auth reset-login` resets.
type ResetLoginInput struct {
	// Login is the password login to reset, or (when no admin has it yet) the login to give a passkey-only admin.
	Login string
	// AdminID picks that passkey-only admin; empty is fine when the panel has exactly one admin.
	AdminID string
}

// ResetLoginResult is what the operator is shown once: the new password and the new authenticator secret.
type ResetLoginResult struct {
	Admin         store.Admin
	Login         string
	Password      string
	TOTPURI       string
	TOTPSecret    string // base32, for typing into the app
	Created       bool   // the admin had no password login before
	SessionsEnded int
}

// ResetTargetError is a reset-login that does not name one admin clearly; the message says what to do.
type ResetTargetError struct{ msg string }

func (e *ResetTargetError) Error() string { return e.msg }

// ResetLogin is the way back in for an admin who lost the phone: run on the panel's own server (the database and the
// master key are files only root, or the panel's own user, can read, so whoever can run it owns the panel already). It
// gives the admin a fresh random password and a fresh authenticator secret, under the existing login or a new one for a
// passkey-only admin, lifts the lockouts of the login and of the admin's step-up, and ends every session of the admin
// (a lost phone may still hold one). Passkeys stay: the admin removes a lost one in Settings -> Security. One audit row,
// actor "cli", without the secrets.
func ResetLogin(ctx context.Context, st *store.Store, v *vault.Vault, in ResetLoginInput, now time.Time) (ResetLoginResult, error) {
	var out ResetLoginResult
	if v == nil {
		return out, errors.New("no master key: password sign-in needs it to keep the authenticator secret")
	}
	login, err := normLogin(in.Login)
	if err != nil {
		return out, err
	}
	admins, err := st.AdminLogins(ctx)
	if err != nil {
		return out, err
	}
	var target *store.AdminLogin
	for i, a := range admins {
		if a.Login == login {
			target = &admins[i]
		}
	}
	switch {
	case target != nil:
		if in.AdminID != "" && in.AdminID != target.ID {
			return out, &ResetTargetError{fmt.Sprintf("the login %q belongs to %s (%s), not to %s", login, target.DisplayName, target.ID, in.AdminID)}
		}
	case in.AdminID != "":
		for i, a := range admins {
			if a.ID == in.AdminID {
				target = &admins[i]
			}
		}
		if target == nil {
			return out, &ResetTargetError{fmt.Sprintf("no admin %s", in.AdminID)}
		}
	case len(admins) == 1:
		target = &admins[0]
	case len(admins) == 0:
		return out, &ResetTargetError{"no admin yet: run `mistgate setup` and open its link"}
	default:
		return out, &ResetTargetError{fmt.Sprintf("no admin signs in as %q; to give one a password login, add --admin <id>", login)}
	}
	out.Created = target.Login == ""
	if !out.Created && target.Login != login {
		return out, &ResetTargetError{fmt.Sprintf("%s signs in as %q: run reset-login %s", target.DisplayName, target.Login, target.Login)}
	}

	out.Admin, out.Login, out.Password = target.Admin, login, randomPassword()
	secret := newTOTPSecret()
	hash := hashPassword(out.Password)
	brand := instance.DefaultBrandHead + instance.DefaultBrandTail
	if set, err := instance.Load(ctx, st); err == nil {
		brand = set.BrandName()
	}
	out.TOTPURI, out.TOTPSecret = totpURI(brand, login, secret), b32.EncodeToString(secret)
	cred := store.PasswordCred{AdminID: target.ID, Login: login, Hash: hash, TOTPSecret: v.Seal(secret, totpAAD(target.ID))}
	params, _ := json.Marshal(map[string]any{"admin": target.ID, "login": login, "created": out.Created})
	out.SessionsEnded, err = st.ResetPasswordLogin(ctx, cred, out.Created, []string{login, stepUpLockKey + target.ID}, now,
		store.AuditEntry{Actor: "cli", Action: "reset_login", Result: "ok", Params: string(params)})
	if errors.Is(err, store.ErrLoginTaken) {
		return out, &ResetTargetError{fmt.Sprintf("the login %q is taken", login)}
	}
	return out, err
}

// randomPassword is 20 characters from an alphabet without look-alikes, in groups of four: about 99 bits.
func randomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	var b strings.Builder
	for i := range 20 {
		if i > 0 && i%4 == 0 {
			b.WriteByte('-')
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			panic(err) // crypto/rand does not fail on supported platforms
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String()
}
