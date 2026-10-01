package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Cloudflare Turnstile. The owner switches it on in the security settings; while it is on,
// every public call that starts a sign-in or the setup (BeginSetup, BeginLogin, PasswordLogin) must carry a
// token that Cloudflare's siteverify accepts. The check fails closed: no token, a bad one, an unreachable
// Cloudflare or an unreadable secret all refuse the call. The way out when Cloudflare is down or the keys are
// wrong is `mistgate auth turnstile off` on the panel's server (DisableTurnstile); the checks read the settings
// on every call, so it takes effect at once, whether the panel runs or not.
//
// Finish* calls are not checked again: a ceremony id can only come from a Begin that passed the check, it is
// single-use, expires after CeremonyTTL and is capped per source, so one solved challenge per sign-in is enough.

// SiteverifyURL is Cloudflare's verification endpoint.
const SiteverifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

const (
	settingTurnstileEnabled = "turnstile_enabled" // "1" or "0"
	settingTurnstileSiteKey = "turnstile_site_key"
	settingTurnstileSecret  = "turnstile_secret" // base64 of the vault-sealed secret key
	turnstileSecretAAD      = "setting/turnstile_secret"

	turnstileTimeout  = 5 * time.Second
	maxTurnstileToken = 2048 // Cloudflare: tokens are at most 2048 characters
	maxSiteverifyBody = 64 << 10
)

var siteKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type turnstileSettings struct {
	Enabled   bool
	SiteKey   string
	SecretSet bool
	sealed    []byte
}

func loadTurnstile(ctx context.Context, st *store.Store) (turnstileSettings, error) {
	var t turnstileSettings
	get := func(k string) (string, error) {
		v, err := st.Setting(ctx, k)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return v, err
	}
	en, err := get(settingTurnstileEnabled)
	if err != nil {
		return t, err
	}
	t.Enabled = en == "1"
	if t.SiteKey, err = get(settingTurnstileSiteKey); err != nil {
		return t, err
	}
	sec, err := get(settingTurnstileSecret)
	if err != nil {
		return t, err
	}
	if sec != "" {
		if t.sealed, err = base64.StdEncoding.DecodeString(sec); err == nil {
			t.SecretSet = true
		}
	}
	return t, nil
}

// TurnstileEnabled tells the admin HTTP layer whether the captcha is on, so that the admin pages allow
// Cloudflare's script and frame in their CSP only then. An unreadable setting counts as off: the CSP stays
// strict, and the RPCs themselves fail closed.
func (s *Service) TurnstileEnabled(ctx context.Context) bool {
	ts, err := loadTurnstile(ctx, s.st)
	return err == nil && ts.Enabled
}

// DisableTurnstile switches the captcha off in the database (the CLI kill switch). It keeps the keys. It works
// with the panel stopped or running: the panel reads the setting on every request.
func DisableTurnstile(ctx context.Context, st *store.Store, now time.Time) error {
	if err := st.SetSettings(ctx, map[string]string{settingTurnstileEnabled: "0"}); err != nil {
		return err
	}
	return st.Audit(ctx, now, store.AuditEntry{Actor: "cli", Action: "turnstile_off", Result: "ok"})
}

var (
	errCaptchaRequired = connect.NewError(connect.CodePermissionDenied, errors.New("captcha required"))
	errCaptchaFailed   = connect.NewError(connect.CodePermissionDenied, errors.New("captcha failed"))
)

// checkTurnstile verifies the token of a public Begin / PasswordLogin call when the captcha is on. The token
// is single-use on Cloudflare's side; ip is passed as remoteip.
func (s *Service) checkTurnstile(ctx context.Context, token string, ip netip.Addr, action string) error {
	ts, err := loadTurnstile(ctx, s.st)
	if err != nil {
		s.log.Error("load turnstile settings", "err", err)
		return errInternal(err)
	}
	if !ts.Enabled {
		return nil
	}
	if token == "" {
		return errCaptchaRequired
	}
	fail := func(reason string, err error) error {
		s.log.Info("captcha refused", "action", action, "reason", reason, "err", err, "ip", ip)
		s.audit(ctx, "anonymous", "captcha", "fail", ip, map[string]any{"for": action, "reason": reason})
		return errCaptchaFailed
	}
	if len(token) > maxTurnstileToken {
		return fail("token too long", nil)
	}
	if s.vault == nil || !ts.SecretSet {
		return fail("no secret key", nil)
	}
	secret, err := s.vault.Open(ts.sealed, turnstileSecretAAD)
	if err != nil {
		return fail("cannot open the secret key (wrong master key?)", err)
	}
	ok, _, err := s.siteverify(ctx, string(secret), token, ip)
	if err != nil {
		return fail("siteverify unavailable", err)
	}
	if !ok {
		return fail("token rejected", nil)
	}
	return nil
}

// siteverify asks Cloudflare whether the token is valid (5 s timeout, bounded response). codes are Cloudflare's
// error-codes of a refusal ("invalid-input-secret", "invalid-input-response", "timeout-or-duplicate", ...).
func (s *Service) siteverify(ctx context.Context, secret, token string, ip netip.Addr) (ok bool, codes []string, err error) {
	ctx, cancel := context.WithTimeout(ctx, turnstileTimeout)
	defer cancel()
	form := url.Values{"secret": {secret}, "response": {token}}
	if ip.IsValid() {
		form.Set("remoteip", ip.String())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tsURL, strings.NewReader(form.Encode()))
	if err != nil {
		return false, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.tsClient.Do(req)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil, errors.New("siteverify answered " + resp.Status)
	}
	var out struct {
		Success bool     `json:"success"`
		Codes   []string `json:"error-codes"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSiteverifyBody)).Decode(&out); err != nil {
		return false, nil, err
	}
	return out.Success, out.Codes, nil
}

// proveTurnstile is the check before keys are saved with the captcha on: a token the widget made with the new site key
// must pass siteverify with the new secret, or nothing is saved (the owner would lock everybody out, themselves too).
func (s *Service) proveTurnstile(ctx context.Context, secret, token string, ip netip.Addr) error {
	if token == "" {
		return codedErr(connect.CodeFailedPrecondition, "turnstile_test_required")
	}
	if len(token) > maxTurnstileToken {
		return codedErr(connect.CodeFailedPrecondition, "turnstile_token_rejected")
	}
	ok, codes, err := s.siteverify(ctx, secret, token, ip)
	if err != nil {
		s.log.Info("turnstile test: siteverify unavailable", "err", err)
		return codedErr(connect.CodeUnavailable, "turnstile_unreachable")
	}
	switch {
	case ok:
		return nil
	case slices.Contains(codes, "invalid-input-secret") || slices.Contains(codes, "missing-input-secret"):
		return codedErr(connect.CodeFailedPrecondition, "turnstile_secret_rejected")
	}
	return codedErr(connect.CodeFailedPrecondition, "turnstile_token_rejected")
}

// --- owner-only settings RPCs ---

func requireOwner(ctx context.Context) (store.Admin, error) {
	admin, err := signedIn(ctx)
	if err != nil {
		return admin, err
	}
	if admin.Role != store.RoleOwner {
		return admin, connect.NewError(connect.CodePermissionDenied, errors.New("only the owner can do this"))
	}
	return admin, nil
}

func securityResponse(t turnstileSettings) (bool, string, bool) {
	return t.Enabled, t.SiteKey, t.SecretSet
}

// GetSecuritySettings returns the captcha settings; the secret key is reported only as set or not.
func (s *Service) GetSecuritySettings(ctx context.Context, _ *connect.Request[adminv1.GetSecuritySettingsRequest]) (*connect.Response[adminv1.GetSecuritySettingsResponse], error) {
	if _, err := requireOwner(ctx); err != nil {
		return nil, err
	}
	t, err := loadTurnstile(ctx, s.st)
	if err != nil {
		s.log.Error("load turnstile settings", "err", err)
		return nil, errInternal(err)
	}
	en, key, set := securityResponse(t)
	return connect.NewResponse(&adminv1.GetSecuritySettingsResponse{TurnstileEnabled: en, TurnstileSiteKey: key, TurnstileSecretSet: set}), nil
}

// UpdateSecuritySettings changes the captcha settings (absent fields stay). Turning the captcha on needs a
// site key and a secret key; removing the secret key switches it off. A call that leaves the captcha on with keys it
// was not proven with (switching it on, new keys while it is on) needs turnstile_test_token: see proveTurnstile.
func (s *Service) UpdateSecuritySettings(ctx context.Context, req *connect.Request[adminv1.UpdateSecuritySettingsRequest]) (*connect.Response[adminv1.UpdateSecuritySettingsResponse], error) {
	admin, err := requireOwner(ctx)
	if err != nil {
		return nil, err
	}
	// the captcha guards the login form: an old stolen owner cookie must not be enough to switch it off
	if err := s.RequireStepUp(ctx); err != nil {
		return nil, err
	}
	m := req.Msg
	t, err := loadTurnstile(ctx, s.st)
	if err != nil {
		s.log.Error("load turnstile settings", "err", err)
		return nil, errInternal(err)
	}
	before := t
	kv := map[string]string{}
	secretChanged, newSecret := false, ""
	if m.TurnstileSiteKey != nil {
		if *m.TurnstileSiteKey != "" && !siteKeyRe.MatchString(*m.TurnstileSiteKey) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("site key: letters, digits, - and _, at most 128 characters"))
		}
		t.SiteKey = *m.TurnstileSiteKey
		kv[settingTurnstileSiteKey] = t.SiteKey
	}
	if m.TurnstileSecretKey != nil {
		sec := *m.TurnstileSecretKey
		switch {
		case sec == "":
			t.SecretSet, t.Enabled = false, false // no secret, no captcha: nothing could verify a token
			kv[settingTurnstileSecret] = ""
		case len(sec) > 256 || strings.IndexFunc(sec, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0:
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("secret key: at most 256 characters, no spaces"))
		case s.vault == nil:
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the secret key cannot be stored on this server (no master key)"))
		default:
			t.SecretSet, newSecret = true, sec
			kv[settingTurnstileSecret] = base64.StdEncoding.EncodeToString(s.vault.Seal([]byte(sec), turnstileSecretAAD))
		}
		secretChanged = true
	}
	if m.TurnstileEnabled != nil {
		t.Enabled = *m.TurnstileEnabled
	}
	if t.Enabled && (t.SiteKey == "" || !t.SecretSet) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the captcha needs a site key and a secret key"))
	}
	if t.Enabled && (!before.Enabled || t.SiteKey != before.SiteKey || secretChanged) {
		secret := newSecret
		if secret == "" { // the stored one: it was proven before, but not with these settings
			if s.vault == nil {
				return nil, codedErr(connect.CodeFailedPrecondition, "turnstile_secret_rejected")
			}
			pt, err := s.vault.Open(t.sealed, turnstileSecretAAD)
			if err != nil {
				return nil, codedErr(connect.CodeFailedPrecondition, "turnstile_secret_rejected")
			}
			secret = string(pt)
		}
		if err := s.proveTurnstile(ctx, secret, m.TurnstileTestToken, s.clientIP(req)); err != nil {
			reason := ""
			if ce := new(connect.Error); errors.As(err, &ce) {
				reason = ce.Message()
			}
			s.audit(ctx, admin.ID, "security_update", "rejected", s.clientIP(req), map[string]any{"turnstile_enabled": true, "reason": reason})
			return nil, err
		}
	}
	kv[settingTurnstileEnabled] = "0"
	if t.Enabled {
		kv[settingTurnstileEnabled] = "1"
	}
	if err := s.st.SetSettings(ctx, kv); err != nil {
		s.log.Error("save security settings", "err", err)
		return nil, errInternal(err)
	}
	s.audit(ctx, admin.ID, "security_update", "ok", s.clientIP(req), map[string]any{
		"turnstile_enabled": t.Enabled, "site_key_changed": m.TurnstileSiteKey != nil, "secret_changed": secretChanged})
	en, key, set := securityResponse(t)
	return connect.NewResponse(&adminv1.UpdateSecuritySettingsResponse{TurnstileEnabled: en, TurnstileSiteKey: key, TurnstileSecretSet: set}), nil
}
