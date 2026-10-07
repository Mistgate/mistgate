package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Step-up re-authentication. A stolen session cookie must not be enough to plant a passkey of the thief's
// own, remove the owner's, or throw the owner's other browsers out: those calls (and revealing a server
// password) need the admin to have proved a sign-in factor, a passkey assertion or a fresh authenticator code,
// within StepUpWindow. Signing in counts (the session starts with StepUpAt = its creation), so the usual
// "sign in, then add a passkey" needs no extra prompt.

// StepUpWindow is how long a proof of a sign-in factor keeps step-up protected calls open.
const StepUpWindow = 5 * time.Minute

const (
	ceremonyStepUp = "stepup"
	stepUpLockKey  = "stepup:" // login_failure key prefix: failed proofs per admin
)

func sessionFrom(ctx context.Context) (store.Session, bool) {
	s, ok := ctx.Value(sessionKey{}).(store.Session)
	return s, ok
}

// stepUpUntil is when the session's last proof runs out; zero if it already has.
func (s *Service) stepUpUntil(sess store.Session) time.Time {
	if until := sess.StepUpAt.Add(StepUpWindow); until.After(s.now()) {
		return until
	}
	return time.Time{}
}

// RequireStepUp returns nil when the session behind ctx (set by RequireSession) proved a sign-in factor within
// StepUpWindow; else PERMISSION_DENIED with a StepUpRequired detail that tells the UI what to ask for. It is the
// one helper every step-up protected call goes through, in this package and in the modules that follow. For an API
// token it is always PERMISSION_DENIED, except inside an approved plan (WithApprovedStepUp).
func (s *Service) RequireStepUp(ctx context.Context) error {
	if p := PrincipalFrom(ctx); p.Token {
		// An API token never proves a sign-in factor. The one exception is the MCP layer applying a plan the owner
		// approved with a step-up of their own: the grant is read from the database now and names this procedure.
		if s.grantAllows(ctx, p.TokenID, procedureFrom(ctx)) {
			return nil
		}
		return connect.NewError(connect.CodePermissionDenied, errors.New("step-up is not available to API tokens; changes that need it go through the owner's approval"))
	}
	admin, err := signedIn(ctx)
	if err != nil {
		return err
	}
	sess, ok := sessionFrom(ctx)
	if !ok {
		return errUnauthenticated
	}
	if !s.stepUpUntil(sess).IsZero() {
		return nil
	}
	d := &adminv1.StepUpRequired{}
	if pks, err := s.st.PasskeysByAdmin(ctx, admin.ID); err == nil {
		d.Passkey = len(pks) > 0
	}
	if ok, err := s.st.AdminHasPassword(ctx, admin.ID); err == nil {
		d.Totp = ok && s.vault != nil
	}
	e := connect.NewError(connect.CodePermissionDenied, errors.New("confirm it is you first (step-up required)"))
	if detail, err := connect.NewErrorDetail(d); err == nil {
		e.AddDetail(detail)
	}
	return e
}

// BeginStepUp starts a passkey assertion limited to the admin's own passkeys.
func (s *Service) BeginStepUp(ctx context.Context, req *connect.Request[adminv1.BeginStepUpRequest]) (*connect.Response[adminv1.BeginStepUpResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	pks, err := s.st.PasskeysByAdmin(ctx, admin.ID)
	if err != nil {
		s.log.Error("list passkeys", "err", err)
		return nil, errInternal(err)
	}
	if len(pks) == 0 {
		return connect.NewResponse(&adminv1.BeginStepUpResponse{}), nil // a password admin: FinishStepUp with totp_code
	}
	u := &waUser{admin: admin}
	for _, p := range pks {
		u.creds = append(u.creds, toCredential(p))
	}
	assertion, data, err := s.wa.BeginLogin(u, webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.log.Error("begin step-up", "err", err)
		return nil, errInternal(err)
	}
	opts, err := json.Marshal(assertion.Response)
	if err != nil {
		return nil, errInternal(err)
	}
	id, err := s.putCeremony(ctx, &ceremony{kind: ceremonyStepUp, src: SourceKey(s.clientIP(req)), data: *data, admin: admin, tokenHash: currentHash(req)})
	if err != nil {
		return nil, ceremonyPutError(err)
	}
	return connect.NewResponse(&adminv1.BeginStepUpResponse{CeremonyId: id, OptionsJson: string(opts)}), nil
}

// FinishStepUp verifies a passkey assertion (ceremony_id + credential_json) or a fresh authenticator code
// (totp_code) and opens the step-up window on this session. Wrong proofs are counted per admin: five lock
// step-up for LockDuration.
func (s *Service) FinishStepUp(ctx context.Context, req *connect.Request[adminv1.FinishStepUpRequest]) (*connect.Response[adminv1.FinishStepUpResponse], error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	now, ip := s.now(), s.clientIP(req)
	key := stepUpLockKey + admin.ID
	prior, err := s.st.LoginFailures(ctx, key, now, FailureWindow)
	if err != nil {
		s.log.Error("step-up failures", "err", err)
		return nil, errInternal(err)
	}
	if prior.Locked(now) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many wrong attempts, try again later"))
	}
	refuse := func(reason string, err error) error {
		s.log.Info("step-up failed", "reason", reason, "err", err, "admin", admin.ID, "ip", ip)
		s.audit(ctx, admin.ID, "stepup", "fail", ip, map[string]any{"reason": reason})
		if _, rerr := s.st.RecordLoginFailure(ctx, key, now, MaxSignInFailures, FailureWindow, LockDuration); rerr != nil {
			s.log.Error("record step-up failure", "err", rerr)
		}
		return connect.NewError(connect.CodeUnauthenticated, errors.New("step-up failed"))
	}

	m := req.Msg
	method := "passkey"
	if m.CeremonyId != "" || m.CredentialJson != "" {
		c, ok, err := s.getCeremony(ctx, m.CeremonyId, ceremonyStepUp)
		if err != nil {
			return nil, errInternal(err)
		}
		if !ok {
			return nil, refuse("unknown or foreign ceremony", nil)
		}
		if c.admin.ID != admin.ID || !bytes.Equal(c.tokenHash, currentHash(req)) {
			_, err := s.consumeCeremony(ctx, m.CeremonyId)
			return nil, refuse("unknown or foreign ceremony", err)
		}
		consumed, err := s.consumeCeremony(ctx, m.CeremonyId)
		if err != nil {
			return nil, refuse("could not consume ceremony", err)
		}
		if !consumed {
			return nil, refuse("unknown or foreign ceremony", nil)
		}
		parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(m.CredentialJson))
		if err != nil {
			return nil, refuse("bad credential json", err)
		}
		pks, err := s.st.PasskeysByAdmin(ctx, admin.ID)
		if err != nil {
			s.log.Error("list passkeys", "err", err)
			return nil, errInternal(err)
		}
		u := &waUser{admin: admin}
		for _, p := range pks {
			u.creds = append(u.creds, toCredential(p))
		}
		cred, err := s.wa.ValidateLogin(u, c.data, parsed)
		if err != nil {
			return nil, refuse("assertion rejected", err)
		}
		if cred.Authenticator.CloneWarning {
			return nil, refuse("sign counter went backwards (cloned authenticator?)", nil)
		}
		if err := s.st.TouchPasskey(ctx, cred.ID, cred.Authenticator.SignCount, byte(cred.Flags.ProtocolValue()), now); err != nil {
			s.log.Error("touch passkey", "err", err)
			return nil, errInternal(err)
		}
	} else {
		method = "totp"
		pw, err := s.st.PasswordByAdmin(ctx, admin.ID)
		if errors.Is(err, store.ErrNotFound) || s.vault == nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("no authenticator code is set up for this admin"))
		} else if err != nil {
			s.log.Error("password credential", "err", err)
			return nil, errInternal(err)
		}
		secret, err := s.vault.Open(pw.TOTPSecret, totpAAD(admin.ID))
		if err != nil {
			s.log.Error("cannot open TOTP secret (wrong master key?)", "admin", admin.ID)
			return nil, errInternal(err)
		}
		step, ok := matchTOTP(secret, m.TotpCode, now)
		if ok { // single use, and newer than the code that signed in: a replayed or older one is refused
			if ok, err = s.st.AdvanceTOTPStep(ctx, admin.ID, step); err != nil {
				s.log.Error("advance totp step", "err", err)
				return nil, errInternal(err)
			}
		}
		if !ok {
			return nil, refuse("wrong or used code", nil)
		}
	}

	if err := s.st.SessionStepUp(ctx, currentHash(req), now); err != nil {
		s.log.Error("record step-up", "err", err)
		return nil, errInternal(err)
	}
	if err := s.st.ClearLoginFailures(ctx, key); err != nil {
		s.log.Warn("clear step-up failures", "err", err)
	}
	s.audit(ctx, admin.ID, "stepup", "ok", ip, map[string]any{"method": method})
	return connect.NewResponse(&adminv1.FinishStepUpResponse{StepUpUntilUnix: now.Add(StepUpWindow).Unix()}), nil
}
