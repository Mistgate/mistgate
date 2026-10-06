// Package auth signs panel admins in — with passkeys (WebAuthn) or, as the fallback
// chosen at setup, with password + authenticator code — and manages their sessions.
// It implements the admin AuthService over Connect-RPC and provides the HTTP middleware
// that protects everything else under the admin API.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	// CookieName is the session cookie. The __Host- prefix forces Secure, Path=/ and no Domain.
	CookieName = "__Host-sid"

	SetupTokenTTL    = 30 * time.Minute
	CeremonyTTL      = 5 * time.Minute
	SessionIdleTTL   = 12 * time.Hour
	SessionMaxAge    = 30 * 24 * time.Hour
	touchGranularity = time.Minute // last_seen_at is written at most this often per session

	// MaxSignInFailures wrong password sign-ins within FailureWindow lock the login for LockDuration.
	MaxSignInFailures = 5
	FailureWindow     = time.Hour
	LockDuration      = 15 * time.Minute
	// MaxSourceFailures failed password sign-ins from one address (IPv4, or an IPv6 /64) within FailureWindow, for
	// any logins, lock that address for LockDuration. Higher than the per-login limit: one address can be a whole
	// office behind a NAT. The lock row is keyed "ip:<SourceKey>"; a login cannot contain a colon, so the two never meet.
	MaxSourceFailures = 10
	sourceLockPrefix  = "ip:"

	maxCeremonies          = 2048
	maxCeremoniesPerSource = 8  // pending ceremonies one client (IPv4 address or IPv6 /64) may hold
	maxSetupCodeTries      = 5  // wrong TOTP codes tolerated on one password-setup ceremony
	maxConcurrentHashes    = 4  // argon2 runs at once: each takes 64 MiB
	maxPasskeyName         = 64 // characters
)

// Config is the relying-party configuration and what the service needs from its host.
type Config struct {
	RPID    string   // e.g. "localhost" or "example.com"
	RPName  string   // shown by the authenticator; defaults to "Mistgate"
	Origins []string // allowed browser origins, e.g. "https://k7q2x9.example.com"
	// Vault seals the TOTP secrets at rest. Without it password sign-in is unavailable.
	Vault *vault.Vault
	// TrustedProxies are the reverse proxies whose X-Forwarded-For / Forwarded headers are
	// believed when working out the client address (rate limit, audit log, sessions).
	TrustedProxies []netip.Prefix
	// TurnstileURL is the Cloudflare siteverify endpoint; empty means SiteverifyURL. Tests point it at a fake.
	TurnstileURL string
	// HTTPClient makes the siteverify call; nil means a client with a 5 s timeout.
	HTTPClient *http.Client
	// Limiter is the shared backend for security-sensitive limits. Nil uses bounded in-memory state.
	Limiter securitylimit.Limiter
	// SourceURL is where the panel's source code is published; Me hands it to the admin, which links it next to the
	// version. Empty = no link.
	SourceURL string
}

// Service implements the admin AuthService.
type Service struct {
	st         *store.Store
	wa         *webauthn.WebAuthn
	vault      *vault.Vault
	log        *slog.Logger
	lim        securitylimit.Limiter
	authBurst  float64
	authRefill time.Duration
	trust      ProxyTrust
	now        func() time.Time

	sourceURL string // where the source code is published (Config.SourceURL)

	tsURL    string       // Turnstile siteverify endpoint
	tsClient *http.Client // and the client that calls it
	hashSem  chan struct{}
	hook     atomic.Pointer[func(Event)]

	// API tokens (bearer.go): the per-token request limiter, and the once-a-minute gates that keep a polling
	// script from rewriting last_used_* or filling the audit log with reads.
	tokMu      sync.Mutex
	tokTouched map[string]time.Time // token id -> last last_used_* write
	tokAudited map[string]time.Time // "token id|key" -> last audited successful read (or refused burst)
}

// ceremony is the server-side half of a begin/finish pair.
type ceremony struct {
	kind      string // ceremonySetup, ceremonySetupPassword, ceremonyLogin or ceremonyAddPasskey
	src       string // who started it (SourceKey), for the per-source cap
	data      webauthn.SessionData
	tokenHash []byte      // setup: the token the ceremony was started with
	admin     store.Admin // setup: the admin that will be created; add-passkey: the signed-in admin
	name      string      // add-passkey: the label
	login     string      // setup-password
	pwHash    string      // setup-password: argon2id of the password (the password itself is never kept)
	totp      []byte      // setup-password: the secret shown to the admin
	tries     int
	expires   time.Time
}

const (
	ceremonySetup         = "setup"
	ceremonySetupPassword = "setup-password"
	ceremonyLogin         = "login"
	ceremonyAddPasskey    = "add-passkey"
)

// New builds the service. log may be nil.
func New(st *store.Store, cfg Config, log *slog.Logger) (*Service, error) {
	if log == nil {
		log = slog.Default()
	}
	if cfg.RPName == "" {
		cfg.RPName = "Mistgate"
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          cfg.RPID,
		RPDisplayName: cfg.RPName,
		RPOrigins:     cfg.Origins,
	})
	if err != nil {
		return nil, fmt.Errorf("webauthn config: %w", err)
	}
	if cfg.TurnstileURL == "" {
		cfg.TurnstileURL = SiteverifyURL
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: turnstileTimeout}
	}
	if cfg.Limiter == nil {
		cfg.Limiter = securitylimit.NewMemory()
	}
	s := &Service{
		sourceURL:  cfg.SourceURL,
		tsURL:      cfg.TurnstileURL,
		tsClient:   cfg.HTTPClient,
		st:         st,
		wa:         wa,
		vault:      cfg.Vault,
		log:        log,
		lim:        cfg.Limiter,
		authBurst:  10,
		authRefill: 3 * time.Second,
		trust:      NewProxyTrust(cfg.TrustedProxies),
		now:        time.Now,
		hashSem:    make(chan struct{}, maxConcurrentHashes),
		tokTouched: map[string]time.Time{},
		tokAudited: map[string]time.Time{},
	}
	return s, nil
}

func hashToken(tok string) []byte {
	h := sha256.Sum256([]byte(tok))
	return h[:]
}

func randomToken(n int) string {
	b := make([]byte, n)
	rand.Read(b) // never fails on supported platforms
	return base64.RawURLEncoding.EncodeToString(b)
}

// IssueSetupToken creates a one-time setup token (32 random bytes, stored as SHA-256,
// valid SetupTokenTTL from now) and invalidates earlier unused ones. It returns the
// plaintext token exactly once. It fails if an admin already exists.
func IssueSetupToken(ctx context.Context, st *store.Store, now time.Time) (string, error) {
	if n, err := st.AdminCount(ctx); err != nil {
		return "", err
	} else if n > 0 {
		return "", ErrAdminExists
	}
	tok := randomToken(32)
	if err := st.PutSetupToken(ctx, hashToken(tok), now.Add(SetupTokenTTL)); err != nil {
		return "", err
	}
	return tok, nil
}

// ErrAdminExists is returned by IssueSetupToken once the first admin has been created.
var ErrAdminExists = errors.New("auth: an admin already exists")

// putCeremony stores c and returns its unguessable id. Expired rows are pruned and
// both pending-ceremony caps are enforced by the store.
func (s *Service) putCeremony(ctx context.Context, c *ceremony) (string, error) {
	now := s.now()
	c.expires = now.Add(CeremonyTTL)
	id := randomToken(16)
	row, err := s.ceremonyRow(id, c)
	if err != nil {
		return "", err
	}
	if err := s.st.PutAuthCeremony(ctx, row, now, maxCeremoniesPerSource, maxCeremonies); err != nil {
		return "", err
	}
	return id, nil
}

// takeCeremony atomically removes a live ceremony. A wrong-kind finish still consumes
// it, matching the previous map behavior.
func (s *Service) takeCeremony(ctx context.Context, id string, kinds ...string) (*ceremony, bool, error) {
	row, err := s.st.TakeAuthCeremony(ctx, id, s.now())
	if errors.Is(err, store.ErrAuthCeremonyNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	c, err := s.ceremonyFromRow(row)
	if err != nil {
		return nil, false, err
	}
	for _, kind := range kinds {
		if c.kind == kind {
			return c, true, nil
		}
	}
	return nil, false, nil
}

// restoreCeremony puts a recoverable ceremony back under the same id and expiry.
func (s *Service) restoreCeremony(ctx context.Context, id string, c *ceremony) error {
	row, err := s.ceremonyRow(id, c)
	if err != nil {
		return err
	}
	return s.st.RestoreAuthCeremony(ctx, row)
}

func ceremonyTOTPRecordID(id string) string { return "auth_ceremony.totp:" + id }

func (s *Service) ceremonyRow(id string, c *ceremony) (store.AuthCeremony, error) {
	data, err := json.Marshal(c.data)
	if err != nil {
		return store.AuthCeremony{}, err
	}
	admin, err := json.Marshal(c.admin)
	if err != nil {
		return store.AuthCeremony{}, err
	}
	var sealedTOTP []byte
	if len(c.totp) > 0 {
		if s.vault == nil {
			return store.AuthCeremony{}, errors.New("auth: cannot persist setup ceremony without a vault")
		}
		sealedTOTP = s.vault.Seal(c.totp, ceremonyTOTPRecordID(id))
	}
	return store.AuthCeremony{
		ID: id, Kind: c.kind, Source: c.src, SessionData: string(data), TokenHash: c.tokenHash,
		AdminJSON: string(admin), Name: c.name, Login: c.login, PasswordHash: c.pwHash,
		TOTPEncrypted: sealedTOTP, Tries: c.tries, ExpiresAt: c.expires,
	}, nil
}

func (s *Service) ceremonyFromRow(row store.AuthCeremony) (*ceremony, error) {
	c := &ceremony{
		kind: row.Kind, src: row.Source, tokenHash: row.TokenHash, name: row.Name, login: row.Login,
		pwHash: row.PasswordHash, tries: row.Tries, expires: row.ExpiresAt,
	}
	if err := json.Unmarshal([]byte(row.SessionData), &c.data); err != nil {
		return nil, fmt.Errorf("auth: decode ceremony session: %w", err)
	}
	if err := json.Unmarshal([]byte(row.AdminJSON), &c.admin); err != nil {
		return nil, fmt.Errorf("auth: decode ceremony admin: %w", err)
	}
	if len(row.TOTPEncrypted) > 0 {
		if s.vault == nil {
			return nil, errors.New("auth: cannot open setup ceremony without a vault")
		}
		var err error
		if c.totp, err = s.vault.Open(row.TOTPEncrypted, ceremonyTOTPRecordID(row.ID)); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// newSession creates a session for admin and returns the cookie to set.
// checked: the password credential a password sign-in verified; the session is made only while it is still current
// (store.CreatePasswordSession, ErrNotFound otherwise). Left out for a passkey sign-in.
func (s *Service) newSession(ctx context.Context, adminID, ip, ua string, checked ...store.PasswordCred) (*http.Cookie, error) {
	now := s.now()
	tok := randomToken(32)   // 256 bits
	ua = store.Clip(ua, 256) // a header can hold any bytes; an invalid string would break ListSessions
	sess := store.Session{
		TokenHash: hashToken(tok), AdminID: adminID,
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(SessionMaxAge),
		IP: ip, UserAgent: ua,
	}
	var err error
	if len(checked) > 0 {
		err = s.st.CreatePasswordSession(ctx, sess, checked[0])
	} else {
		err = s.st.CreateSession(ctx, sess)
	}
	if err != nil {
		return nil, err
	}
	// Opportunistic cleanup keeps the table small without a background goroutine.
	if err := s.st.PurgeSessions(ctx, now, SessionIdleTTL); err != nil {
		s.log.Warn("purge sessions", "err", err)
	}
	return &http.Cookie{
		Name: CookieName, Value: tok, Path: "/",
		MaxAge: int(SessionMaxAge / time.Second), HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	}, nil
}

func clearedCookie() *http.Cookie {
	return &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode}
}

// resolveSession returns the session and its admin for a session token, enforcing the idle and absolute limits.
func (s *Service) resolveSession(ctx context.Context, tok string) (store.Session, store.Admin, error) {
	if tok == "" {
		return store.Session{}, store.Admin{}, store.ErrNotFound
	}
	h := hashToken(tok)
	sess, admin, err := s.st.SessionWithAdmin(ctx, h)
	if err != nil {
		return sess, admin, err
	}
	now := s.now()
	if !now.Before(sess.ExpiresAt) || now.Sub(sess.LastSeenAt) >= SessionIdleTTL {
		if err := s.st.DeleteSession(ctx, h); err != nil {
			s.log.Warn("delete expired session", "err", err)
		}
		return sess, admin, store.ErrNotFound
	}
	if now.Sub(sess.LastSeenAt) >= touchGranularity {
		if err := s.st.TouchSession(ctx, h, now); err != nil {
			s.log.Warn("touch session", "err", err)
		}
	}
	return sess, admin, nil
}

// audit writes one audit-log row. params must not contain secrets.
func (s *Service) audit(ctx context.Context, actor, action, result string, ip netip.Addr, params map[string]any) {
	p := ""
	if len(params) > 0 {
		b, _ := json.Marshal(params)
		p = string(b)
	}
	e := store.AuditEntry{Actor: actor, Action: action, Params: p, Result: result, Source: store.AuditPanel}
	if ip.IsValid() {
		e.IP = ip.String()
	}
	if err := s.st.Audit(ctx, s.now(), e); err != nil {
		s.log.Error("audit write failed", "action", action, "err", err)
	}
}

// brandName is the issuer label of authenticator-app entries; the installation's brand.
func (s *Service) brandName(ctx context.Context) string {
	set, err := instance.Load(ctx, s.st)
	if err != nil {
		return instance.DefaultBrandHead + instance.DefaultBrandTail
	}
	return set.BrandName()
}

// hashed runs fn (an argon2 computation) while holding one of maxConcurrentHashes slots.
func (s *Service) hashed(ctx context.Context, fn func()) error {
	select {
	case s.hashSem <- struct{}{}:
		defer func() { <-s.hashSem }()
		defer scheduleMemoryRelease() // the 64 MiB buffer is garbage now; hand it back once things are quiet
		fn()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// resolve is resolveSession for callers that only need the admin.
func (s *Service) resolve(ctx context.Context, tok string) (store.Admin, error) {
	_, admin, err := s.resolveSession(ctx, tok)
	return admin, err
}
