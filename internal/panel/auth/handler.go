package auth

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Handler returns the Connect path prefix (e.g. "/mistgate.admin.v1.AuthService/") and
// the handler. It does not check sessions itself: mount it behind RequireSession, which
// also covers every other service.
func (s *Service) Handler() (string, http.Handler) {
	return adminv1connect.NewAuthServiceHandler(s, connect.WithReadMaxBytes(64<<10))
}

func sessionToken(h http.Header) string {
	c, err := (&http.Request{Header: h}).Cookie(CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

var errUnauthenticated = connect.NewError(connect.CodeUnauthenticated, errors.New("not signed in"))

var authBucket = securitylimit.Bucket{Name: "auth", Burst: 10, Refill: 3 * time.Second}

// clientIP is the address of the client: the TCP peer, or the address a trusted proxy reports.
func (s *Service) clientIP(req connect.AnyRequest) netip.Addr {
	return s.trust.ClientIP(req.Peer().Addr, req.Header())
}

func (s *Service) rateLimited(ctx context.Context, req connect.AnyRequest) error {
	decision, err := s.lim.Take(ctx, authBucket, SourceKey(s.clientIP(req)))
	if err != nil || !decision.Allowed {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many attempts, try again later"))
	}
	return nil
}

func ceremonyPutError(err error) error {
	if errors.Is(err, store.ErrAuthCeremonyLimit) {
		return connect.NewError(connect.CodeResourceExhausted, err)
	}
	return errInternal(err)
}

// waUser adapts an admin and its passkeys to webauthn.User.
type waUser struct {
	admin store.Admin
	creds []webauthn.Credential
}

func (u *waUser) WebAuthnID() []byte                         { return u.admin.UserHandle }
func (u *waUser) WebAuthnName() string                       { return u.admin.DisplayName }
func (u *waUser) WebAuthnDisplayName() string                { return u.admin.DisplayName }
func (u *waUser) WebAuthnCredentials() []webauthn.Credential { return u.creds }

func toCredential(p store.Passkey) webauthn.Credential {
	tr := make([]protocol.AuthenticatorTransport, len(p.Transports))
	for i, t := range p.Transports {
		tr[i] = protocol.AuthenticatorTransport(t)
	}
	return webauthn.Credential{
		ID:              p.CredentialID,
		PublicKey:       p.PublicKey,
		AttestationType: p.AttestationType,
		Transport:       tr,
		Flags:           webauthn.NewCredentialFlags(protocol.AuthenticatorFlags(p.Flags)),
		Authenticator:   webauthn.Authenticator{AAGUID: p.AAGUID, SignCount: p.SignCount},
	}
}

// Discoverable credentials and user verification are mandatory: sign-in has no username
// field, and a stolen security key alone must not open the panel. The server enforces
// the UV flag on the response because the ceremony records VerificationRequired.
var authSelection = protocol.AuthenticatorSelection{
	RequireResidentKey: protocol.ResidentKeyRequired(),
	ResidentKey:        protocol.ResidentKeyRequirementRequired,
	UserVerification:   protocol.VerificationRequired,
}

func toProtoAdmin(a store.Admin) *adminv1.Admin {
	role := adminv1.Role_ROLE_UNSPECIFIED
	switch a.Role {
	case store.RoleOwner:
		role = adminv1.Role_ROLE_OWNER
	case store.RoleHelper:
		role = adminv1.Role_ROLE_HELPER
	case store.RoleReadonly:
		role = adminv1.Role_ROLE_READONLY
	}
	return &adminv1.Admin{Id: a.ID, DisplayName: a.DisplayName, Role: role}
}

func errInternal(err error) error {
	return connect.NewError(connect.CodeInternal, errors.New("internal error")) // details went to slog
}

// GetLoginInfo is public: the brand, accent and language for the sign-in page and
// whether setup or the password form should be offered.
func (s *Service) GetLoginInfo(ctx context.Context, _ *connect.Request[adminv1.GetLoginInfoRequest]) (*connect.Response[adminv1.GetLoginInfoResponse], error) {
	set, err := instance.Load(ctx, s.st)
	if err != nil {
		s.log.Error("load instance settings", "err", err)
		return nil, errInternal(err)
	}
	n, err := s.st.AdminCount(ctx)
	if err != nil {
		s.log.Error("admin count", "err", err)
		return nil, errInternal(err)
	}
	pw, err := s.st.AnyPassword(ctx)
	if err != nil {
		s.log.Error("any password", "err", err)
		return nil, errInternal(err)
	}
	ts, err := loadTurnstile(ctx, s.st)
	if err != nil {
		s.log.Error("load turnstile settings", "err", err)
		return nil, errInternal(err)
	}
	resp := &adminv1.GetLoginInfoResponse{
		BrandHead: set.BrandHead, BrandTail: set.BrandTail, Accent: set.Accent, Language: set.Language,
		HasLogo: set.LogoSVG != "", SetupOpen: n == 0, PasswordLogin: pw && s.vault != nil,
	}
	if ts.Enabled {
		resp.TurnstileEnabled, resp.TurnstileSiteKey = true, ts.SiteKey
	}
	return connect.NewResponse(resp), nil
}

// BeginSetup starts creating the first admin: a passkey registration, or (method
// PASSWORD) a password + TOTP enrolment. options_json is the bare
// PublicKeyCredentialCreationOptionsJSON (no "publicKey" wrapper).
func (s *Service) BeginSetup(ctx context.Context, req *connect.Request[adminv1.BeginSetupRequest]) (*connect.Response[adminv1.BeginSetupResponse], error) {
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	if err := s.checkTurnstile(ctx, req.Msg.TurnstileToken, s.clientIP(req), "setup"); err != nil {
		return nil, err
	}
	hash := hashToken(req.Msg.SetupToken)
	if err := s.st.CheckSetupToken(ctx, hash, s.now()); err != nil {
		if errors.Is(err, store.ErrSetupClosed) {
			return nil, errBadSetupToken
		}
		s.log.Error("check setup token", "err", err)
		return nil, errInternal(err)
	}
	name := strings.TrimSpace(req.Msg.DisplayName)
	if name == "" {
		name = "Owner"
	}
	if len([]rune(name)) > 64 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("display name is too long"))
	}
	admin := store.Admin{ID: store.NewID("adm_"), DisplayName: name, Role: store.RoleOwner, UserHandle: []byte(randomToken(32))}
	src := SourceKey(s.clientIP(req))

	if req.Msg.Method == adminv1.SetupMethod_SETUP_METHOD_PASSWORD {
		return s.beginSetupPassword(ctx, req.Msg, admin, hash, src)
	}

	creation, data, err := s.wa.BeginRegistration(&waUser{admin: admin}, webauthn.WithAuthenticatorSelection(authSelection))
	if err != nil {
		s.log.Error("begin registration", "err", err)
		return nil, errInternal(err)
	}
	opts, err := json.Marshal(creation.Response)
	if err != nil {
		return nil, errInternal(err)
	}
	id, err := s.putCeremony(ctx, &ceremony{kind: ceremonySetup, src: src, data: *data, tokenHash: hash, admin: admin})
	if err != nil {
		return nil, ceremonyPutError(err)
	}
	return connect.NewResponse(&adminv1.BeginSetupResponse{CeremonyId: id, OptionsJson: string(opts)}), nil
}

var errBadSetupToken = connect.NewError(connect.CodePermissionDenied, errors.New("setup link is invalid or expired"))

func (s *Service) beginSetupPassword(ctx context.Context, m *adminv1.BeginSetupRequest, admin store.Admin, tokenHash []byte, src string) (*connect.Response[adminv1.BeginSetupResponse], error) {
	if s.vault == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("password sign-in is not available on this server"))
	}
	login, err := normLogin(m.Login)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := checkPasswordPolicy(m.Password); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	var pwHash string
	if err := s.hashed(ctx, func() { pwHash = hashPassword(m.Password) }); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("server is busy, try again"))
	}
	secret := newTOTPSecret()
	id, err := s.putCeremony(ctx, &ceremony{kind: ceremonySetupPassword, src: src, tokenHash: tokenHash, admin: admin, login: login, pwHash: pwHash, totp: secret})
	if err != nil {
		return nil, ceremonyPutError(err)
	}
	return connect.NewResponse(&adminv1.BeginSetupResponse{
		CeremonyId: id, TotpUri: totpURI(s.brandName(ctx), login, secret), TotpSecret: b32.EncodeToString(secret),
	}), nil
}

// FinishSetup verifies the new credential (passkey, or the TOTP code for a password
// admin), consumes the setup token, creates the owner and signs them in.
func (s *Service) FinishSetup(ctx context.Context, req *connect.Request[adminv1.FinishSetupRequest]) (*connect.Response[adminv1.FinishSetupResponse], error) {
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	c, ok, err := s.getCeremony(ctx, req.Msg.CeremonyId, ceremonySetup, ceremonySetupPassword)
	if err != nil {
		return nil, errInternal(err)
	}
	expired := func() error {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("registration expired, start again"))
	}
	consume := func() error {
		won, err := s.consumeCeremony(ctx, req.Msg.CeremonyId, c)
		if err != nil {
			return errInternal(err)
		}
		if !won {
			return expired()
		}
		return nil
	}
	if !ok {
		return nil, expired()
	}
	hash := hashToken(req.Msg.SetupToken)
	if subtle.ConstantTimeCompare(hash, c.tokenHash) != 1 {
		if err := consume(); err != nil {
			return nil, err
		}
		return nil, errBadSetupToken
	}
	now := s.now()
	ip := s.clientIP(req)
	method := "passkey"
	var create func() error
	if c.kind == ceremonySetupPassword {
		method = "password"
		step, ok := matchTOTP(c.totp, req.Msg.TotpCode, now)
		if !ok {
			if _, err := s.failCeremony(ctx, req.Msg.CeremonyId, c); err != nil {
				return nil, errInternal(err)
			}
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid code"))
		}
		sealed := s.vault.Seal(c.totp, totpAAD(c.admin.ID))
		create = func() error {
			return s.st.CreateFirstAdminPassword(ctx, hash, now, c.admin,
				store.PasswordCred{Login: c.login, Hash: c.pwHash, TOTPSecret: sealed, TOTPStep: step})
		}
	} else {
		parsed, err := protocol.ParseCredentialCreationResponseBytes([]byte(req.Msg.CredentialJson))
		if err != nil {
			s.log.Info("setup: bad credential json", "err", err)
			if consumeErr := consume(); consumeErr != nil {
				return nil, consumeErr
			}
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid credential"))
		}
		cred, err := s.wa.CreateCredential(&waUser{admin: c.admin}, c.data, parsed)
		if err != nil {
			s.log.Info("setup: credential rejected", "err", err)
			if consumeErr := consume(); consumeErr != nil {
				return nil, consumeErr
			}
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("credential rejected"))
		}
		pk := passkeyFromCredential(c.admin.ID, "First passkey", cred)
		create = func() error { return s.st.CreateFirstAdmin(ctx, hash, now, c.admin, pk) }
	}
	if err := consume(); err != nil {
		return nil, err
	}
	if err := create(); err != nil {
		if errors.Is(err, store.ErrSetupClosed) {
			s.audit(ctx, "anonymous", "setup", "rejected", ip, nil)
			return nil, errBadSetupToken
		}
		s.log.Error("create first admin", "err", err)
		return nil, errInternal(err)
	}
	cookie, err := s.newSession(ctx, c.admin.ID, ip.String(), req.Header().Get("User-Agent"))
	if err != nil {
		s.log.Error("create session", "err", err)
		return nil, errInternal(err)
	}
	s.audit(ctx, c.admin.ID, "setup", "ok", ip, map[string]any{"method": method})
	s.emit(Event{Kind: EventSetup, AdminID: c.admin.ID, Method: method, IP: ip.String()})
	resp := connect.NewResponse(&adminv1.FinishSetupResponse{Admin: toProtoAdmin(c.admin)})
	resp.Header().Add("Set-Cookie", cookie.String())
	return resp, nil
}

func totpAAD(adminID string) string { return adminID + "/totp" }

func passkeyFromCredential(adminID, name string, cred *webauthn.Credential) store.Passkey {
	tr := make([]string, len(cred.Transport))
	for i, t := range cred.Transport {
		tr[i] = string(t)
	}
	return store.Passkey{
		ID: store.NewID("pk_"), AdminID: adminID, CredentialID: cred.ID, PublicKey: cred.PublicKey,
		AttestationType: cred.AttestationType, SignCount: cred.Authenticator.SignCount,
		AAGUID: cred.Authenticator.AAGUID, Transports: tr, Flags: byte(cred.Flags.ProtocolValue()),
		Name: name,
	}
}

// BeginLogin starts a discoverable-credential assertion.
// options_json is the bare PublicKeyCredentialRequestOptionsJSON (no "publicKey" wrapper).
func (s *Service) BeginLogin(ctx context.Context, req *connect.Request[adminv1.BeginLoginRequest]) (*connect.Response[adminv1.BeginLoginResponse], error) {
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	if err := s.checkTurnstile(ctx, req.Msg.TurnstileToken, s.clientIP(req), "login"); err != nil {
		return nil, err
	}
	assertion, data, err := s.wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		s.log.Error("begin login", "err", err)
		return nil, errInternal(err)
	}
	opts, err := json.Marshal(assertion.Response)
	if err != nil {
		return nil, errInternal(err)
	}
	id, err := s.putCeremony(ctx, &ceremony{kind: ceremonyLogin, src: SourceKey(s.clientIP(req)), data: *data})
	if err != nil {
		return nil, ceremonyPutError(err)
	}
	return connect.NewResponse(&adminv1.BeginLoginResponse{CeremonyId: id, OptionsJson: string(opts)}), nil
}

var errSignInFailed = connect.NewError(connect.CodeUnauthenticated, errors.New("sign-in failed"))

// FinishLogin verifies the assertion and opens a session.
func (s *Service) FinishLogin(ctx context.Context, req *connect.Request[adminv1.FinishLoginRequest]) (*connect.Response[adminv1.FinishLoginResponse], error) {
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	ip := s.clientIP(req)
	fail := func(reason string, err error) error {
		s.log.Info("login failed", "reason", reason, "err", err, "ip", ip)
		s.audit(ctx, "anonymous", "login", "fail", ip, nil)
		s.emit(Event{Kind: EventSignInFailed, Method: "passkey", IP: ip.String()})
		return errSignInFailed
	}
	c, ok, err := s.getCeremony(ctx, req.Msg.CeremonyId, ceremonyLogin)
	if err != nil {
		return nil, errInternal(err)
	}
	if !ok {
		return nil, fail("unknown or expired ceremony", nil)
	}
	consumeFailure := func() error {
		_, err := s.consumeCeremony(ctx, req.Msg.CeremonyId, c)
		return err
	}
	parsed, err := protocol.ParseCredentialRequestResponseBytes([]byte(req.Msg.CredentialJson))
	if err != nil {
		if err := consumeFailure(); err != nil {
			return nil, errInternal(err)
		}
		return nil, fail("bad credential json", err)
	}
	lookup := func(rawID, userHandle []byte) (webauthn.User, error) {
		pk, err := s.st.PasskeyByCredentialID(ctx, rawID)
		if err != nil {
			return nil, err
		}
		admin, err := s.st.Admin(ctx, pk.AdminID)
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(admin.UserHandle, userHandle) {
			return nil, errors.New("user handle mismatch")
		}
		pks, err := s.st.PasskeysByAdmin(ctx, admin.ID)
		if err != nil {
			return nil, err
		}
		u := &waUser{admin: admin}
		for _, p := range pks {
			u.creds = append(u.creds, toCredential(p))
		}
		return u, nil
	}
	user, cred, err := s.wa.ValidatePasskeyLogin(lookup, c.data, parsed)
	if err != nil {
		if err := consumeFailure(); err != nil {
			return nil, errInternal(err)
		}
		return nil, fail("assertion rejected", err)
	}
	if cred.Authenticator.CloneWarning {
		if err := consumeFailure(); err != nil {
			return nil, errInternal(err)
		}
		return nil, fail("sign counter went backwards (cloned authenticator?)", nil)
	}
	admin := user.(*waUser).admin
	consumed, err := s.consumeCeremony(ctx, req.Msg.CeremonyId, c)
	if err != nil {
		return nil, errInternal(err)
	}
	if !consumed {
		return nil, fail("unknown or expired ceremony", nil)
	}
	if err := s.st.TouchPasskey(ctx, cred.ID, cred.Authenticator.SignCount, byte(cred.Flags.ProtocolValue()), s.now()); err != nil {
		s.log.Error("touch passkey", "err", err)
		return nil, errInternal(err)
	}
	cookie, err := s.newSession(ctx, admin.ID, ip.String(), req.Header().Get("User-Agent"))
	if err != nil {
		s.log.Error("create session", "err", err)
		return nil, errInternal(err)
	}
	s.audit(ctx, admin.ID, "login", "ok", ip, map[string]any{"method": "passkey"})
	s.emit(Event{Kind: EventSignIn, AdminID: admin.ID, Method: "passkey", IP: ip.String()})
	resp := connect.NewResponse(&adminv1.FinishLoginResponse{Admin: toProtoAdmin(admin)})
	resp.Header().Add("Set-Cookie", cookie.String())
	return resp, nil
}

// signInFailure is the UNAUTHENTICATED error of a failed password sign-in, carrying the
// attempts left or the unlock time.
func signInFailure(f store.LoginFailure, now time.Time) error {
	d := &adminv1.SignInFailure{AttemptsLeft: int32(max(0, MaxSignInFailures-f.Failures))}
	if f.Locked(now) {
		d.AttemptsLeft, d.LockedUntilUnix = 0, f.LockedUntil.Unix()
	}
	e := connect.NewError(connect.CodeUnauthenticated, errors.New("sign-in failed"))
	if detail, err := connect.NewErrorDetail(d); err == nil {
		e.AddDetail(detail)
	}
	return e
}

// PasswordLogin signs an admin in with login + password + authenticator code. Every
// failure looks the same whatever was wrong and whether the login exists; five failures
// lock the login for LockDuration, and a locked login is refused without looking at the
// credentials. A code is single-use.
func (s *Service) PasswordLogin(ctx context.Context, req *connect.Request[adminv1.PasswordLoginRequest]) (*connect.Response[adminv1.PasswordLoginResponse], error) {
	if err := s.rateLimited(ctx, req); err != nil {
		return nil, err
	}
	ip := s.clientIP(req)
	if err := s.checkTurnstile(ctx, req.Msg.TurnstileToken, ip, "login_password"); err != nil {
		return nil, err
	}
	now := s.now()
	login, err := normLogin(req.Msg.Login)
	if err != nil || len(req.Msg.Password) > maxPasswordBytes {
		return nil, signInFailure(store.LoginFailure{}, now) // cannot be a real login: nothing to count against
	}
	prior, err := s.st.LoginFailures(ctx, login, now, FailureWindow)
	if err != nil {
		s.log.Error("login failures", "err", err)
		return nil, errInternal(err)
	}
	// The address is locked too (whatever login it tried): guessing many logins from one place, or one
	// login from a place that has already failed a lot, gets nowhere.
	priorSrc, err := s.st.LoginFailures(ctx, sourceLockPrefix+SourceKey(ip), now, FailureWindow)
	if err != nil {
		s.log.Error("source failures", "err", err)
		return nil, errInternal(err)
	}
	if prior.Locked(now) || priorSrc.Locked(now) {
		s.audit(ctx, "anonymous", "login_password", "locked", ip, nil)
		if priorSrc.Locked(now) {
			prior = priorSrc
		}
		return nil, signInFailure(prior, now)
	}

	cred, err := s.st.PasswordByLogin(ctx, login)
	found := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("password lookup", "err", err)
		return nil, errInternal(err)
	}
	ok, step := false, int64(0)
	hash := spoofHash()
	if found {
		hash = cred.Hash
	}
	var pwOK bool
	if err := s.hashed(ctx, func() { pwOK = verifyPassword(hash, req.Msg.Password) }); err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("server is busy, try again"))
	}
	if found && s.vault != nil {
		if secret, err := s.vault.Open(cred.TOTPSecret, totpAAD(cred.AdminID)); err != nil {
			s.log.Error("cannot open TOTP secret (wrong master key?)", "admin", cred.AdminID)
		} else {
			var codeOK bool
			step, codeOK = matchTOTP(secret, req.Msg.TotpCode, now)
			ok = pwOK && codeOK
		}
	}
	if ok { // a code that was already used (or an older one) is a replay
		if ok, err = s.st.AdvanceTOTPStep(ctx, cred.AdminID, step); err != nil {
			s.log.Error("advance totp step", "err", err)
			return nil, errInternal(err)
		}
	}
	if !ok {
		return nil, s.passwordFailed(ctx, login, cred, found, ip, now)
	}

	if err := s.st.ClearLoginFailures(ctx, login); err != nil {
		s.log.Warn("clear login failures", "err", err)
	}
	admin, err := s.st.Admin(ctx, cred.AdminID)
	if err != nil {
		s.log.Error("admin of password login", "err", err)
		return nil, errInternal(err)
	}
	cookie, err := s.newSession(ctx, admin.ID, ip.String(), req.Header().Get("User-Agent"), cred)
	if errors.Is(err, store.ErrNotFound) { // the password or the code was replaced while this sign-in was checked
		return nil, s.passwordFailed(ctx, login, cred, found, ip, now)
	}
	if err != nil {
		s.log.Error("create session", "err", err)
		return nil, errInternal(err)
	}
	s.audit(ctx, admin.ID, "login", "ok", ip, map[string]any{"method": "password"})
	s.emit(Event{Kind: EventSignIn, AdminID: admin.ID, Login: login, Method: "password", IP: ip.String()})
	resp := connect.NewResponse(&adminv1.PasswordLoginResponse{Admin: toProtoAdmin(admin)})
	resp.Header().Add("Set-Cookie", cookie.String())
	return resp, nil
}

// passwordFailed counts a failed password sign-in and builds the error. Only real
// logins are named in the audit log and events: a mistyped password in the login field
// must not end up there.
func (s *Service) passwordFailed(ctx context.Context, login string, cred store.PasswordCred, found bool, ip netip.Addr, now time.Time) error {
	f, err := s.st.RecordLoginFailure(ctx, login, now, MaxSignInFailures, FailureWindow, LockDuration)
	if err != nil {
		s.log.Error("record login failure", "err", err)
		return errInternal(err)
	}
	fs, err := s.st.RecordLoginFailure(ctx, sourceLockPrefix+SourceKey(ip), now, MaxSourceFailures, FailureWindow, LockDuration)
	if err != nil {
		s.log.Error("record source failure", "err", err)
		return errInternal(err)
	}
	if fs.Locked(now) && !f.Locked(now) {
		f = fs // report the lock that applies to this client
		s.audit(ctx, "anonymous", "lockout", "locked", ip, map[string]any{"source": SourceKey(ip), "until": fs.LockedUntil.Unix()})
	}
	actor, named := "anonymous", ""
	if found {
		actor, named = cred.AdminID, login
	}
	s.log.Info("password login failed", "ip", ip, "failures", f.Failures)
	s.audit(ctx, actor, "login", "fail", ip, map[string]any{"method": "password", "login": nonEmpty(named, "?")})
	s.emit(Event{Kind: EventSignInFailed, AdminID: cred.AdminID, Login: named, Method: "password", IP: ip.String()})
	if f.Locked(now) {
		s.audit(ctx, actor, "lockout", "locked", ip, map[string]any{"login": nonEmpty(named, "?"), "until": f.LockedUntil.Unix()})
		s.emit(Event{Kind: EventLockout, AdminID: cred.AdminID, Login: named, Method: "password", IP: ip.String(), Until: f.LockedUntil})
	}
	return signInFailure(f, now)
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Logout deletes the session (if any) and clears the cookie.
func (s *Service) Logout(ctx context.Context, req *connect.Request[adminv1.LogoutRequest]) (*connect.Response[adminv1.LogoutResponse], error) {
	actor := "anonymous"
	if tok := sessionToken(req.Header()); tok != "" {
		if admin, err := s.resolve(ctx, tok); err == nil {
			actor = admin.ID
		}
		if err := s.st.DeleteSession(ctx, hashToken(tok)); err != nil {
			s.log.Error("delete session", "err", err)
			return nil, errInternal(err)
		}
	}
	s.audit(ctx, actor, "logout", "ok", s.clientIP(req), nil)
	resp := connect.NewResponse(&adminv1.LogoutResponse{})
	resp.Header().Add("Set-Cookie", clearedCookie().String())
	return resp, nil
}

// Me returns the signed-in admin.
func (s *Service) Me(ctx context.Context, _ *connect.Request[adminv1.MeRequest]) (*connect.Response[adminv1.MeResponse], error) {
	admin, ok := AdminFrom(ctx)
	if !ok {
		return nil, errUnauthenticated
	}
	resp := &adminv1.MeResponse{Admin: toProtoAdmin(admin), Version: buildinfo.Version, SourceUrl: s.sourceURL}
	if sess, ok := sessionFrom(ctx); ok {
		if until := s.stepUpUntil(sess); !until.IsZero() {
			resp.StepUpUntilUnix = until.Unix()
		}
	}
	return connect.NewResponse(resp), nil
}
