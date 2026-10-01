package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Bearer authentication with API tokens. It is a second way into
// RequireSession, not a second chain: the policy, the handlers and their audit rows are the ones the cookie
// session uses.

const (
	// TokenSecretPrefix starts every token secret. Neutral on purpose: no product name travels in a header.
	TokenSecretPrefix = "tk1_"

	tokenBurst      = 30          // most requests a token may make at once
	gateEvery       = time.Minute // successful reads of a token are audited, and last_used_* written, this often
	maxGateEntries  = 4096        // size at which the gates forget what is older than gateEvery
	maxPlanningBody = 1 << 20     // the dry-run check reads at most this much of an ApplyFix request
	auditPathLen    = 200         // audit rows clip the procedure path (it comes from the caller)
)

// secretRe is the exact shape of a token secret: 4 + 43 characters. Anything else is refused before the database
// is asked.
var secretRe = regexp.MustCompile(`^tk1_[A-Za-z0-9_-]{43}$`)

// NewTokenSecret returns a fresh token secret: the prefix and 256 random bits, URL-safe.
func NewTokenSecret() string {
	b := make([]byte, 32)
	rand.Read(b) // never fails on supported platforms
	return TokenSecretPrefix + base64.RawURLEncoding.EncodeToString(b)
}

// bearerFailure is a refused token request.
type bearerFailure struct {
	status int
	body   string
	retry  time.Duration // 429 only
	reason string        // for the log; never carries the secret
}

func failure(status int, code, msg, reason string) *bearerFailure {
	b, _ := json.Marshal(map[string]string{"code": code, "message": msg})
	return &bearerFailure{status: status, body: string(b), reason: reason}
}

var errBearerBad = failure(http.StatusUnauthorized, "unauthenticated", "not signed in", "malformed or unknown token")

// hasBearer reports whether the request carries an Authorization header of the Bearer scheme: then it is a token
// request and a cookie is never looked at. Another scheme (a proxy's basic auth in front of the panel) is
// ignored, so the cookie session works behind it.
func hasBearer(h http.Header) bool {
	for _, v := range h.Values("Authorization") {
		if scheme, _, _ := strings.Cut(strings.TrimSpace(v), " "); strings.EqualFold(scheme, "Bearer") {
			return true
		}
	}
	return false
}

// authenticateBearer checks the first three steps of token authentication: one Authorization header of the Bearer
// scheme with a well-formed secret, a live token, and, when limit is set, a token left in the token's bucket.
// The token is returned with a failure whenever it was found (so a refusal can be audited under it).
func (s *Service) authenticateBearer(r *http.Request, limit bool) (store.APIToken, *bearerFailure) {
	vals := r.Header.Values("Authorization")
	if len(vals) != 1 {
		return store.APIToken{}, errBearerBad
	}
	scheme, secret, ok := strings.Cut(vals[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || !secretRe.MatchString(secret) {
		return store.APIToken{}, errBearerBad
	}
	tok, err := s.st.APITokenBySecretHash(r.Context(), hashToken(secret))
	if err == store.ErrNotFound {
		return store.APIToken{}, errBearerBad
	}
	if err != nil {
		s.log.Error("look up api token", "err", err)
		return store.APIToken{}, failure(http.StatusInternalServerError, "internal", "internal error", "database error")
	}
	switch now := s.now(); {
	case tok.Revoked():
		return tok, failure(http.StatusUnauthorized, "unauthenticated", "token revoked", "token revoked")
	case tok.Expired(now):
		return tok, failure(http.StatusUnauthorized, "unauthenticated", "token expired", "token expired")
	}
	if limit {
		refill := time.Minute / time.Duration(max(tok.RatePerMin, 1))
		if ok, wait := s.tokLim.allowRate(tok.ID, float64(min(tokenBurst, max(tok.RatePerMin, 1))), refill); !ok {
			f := failure(http.StatusTooManyRequests, "resource_exhausted", "too many requests for this token, slow down", "rate limit")
			f.retry = wait
			return tok, f
		}
	}
	return tok, nil
}

func writeBearerFailure(w http.ResponseWriter, f *bearerFailure) {
	if f.status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int((f.retry+time.Second-1)/time.Second))))
	}
	writeJSONError(w, f.status, f.body)
}

// gate reports whether the keyed action may happen now: at most once per gateEvery for each key.
func (s *Service) gate(m map[string]time.Time, key string) bool {
	now := s.now()
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	if last, ok := m[key]; ok && now.Sub(last) < gateEvery {
		return false
	}
	if len(m) >= maxGateEntries {
		for k, t := range m {
			if now.Sub(t) >= gateEvery {
				delete(m, k)
			}
		}
	}
	m[key] = now
	return true
}

// touchToken records a use of the token, at most once a minute per token.
func (s *Service) touchToken(ctx context.Context, tok store.APIToken, ip netip.Addr, ch Channel) {
	if !s.gate(s.tokTouched, tok.ID) {
		return
	}
	addr := ""
	if ip.IsValid() {
		addr = ip.String()
	}
	if err := s.st.TouchAPIToken(context.WithoutCancel(ctx), tok.ID, s.now(), addr, string(ch)); err != nil {
		s.log.Warn("touch api token", "err", err)
	}
}

func principalOf(tok store.APIToken, ch Channel) Principal {
	return Principal{Token: true, TokenID: tok.ID, Profile: tok.Profile, Channel: ch, Name: tok.Name, ExpiresAt: tok.ExpiresAt}
}

// auditCall writes the "call" row of a token request (section 8): who, over which channel, which procedure,
// which HTTP status. gateKey, when set, limits the row to once a minute per token and key (successful reads, and
// the 429 of a token that keeps hammering).
func (s *Service) auditCall(ctx context.Context, p Principal, path string, status int, ip netip.Addr, gateKey string) {
	if gateKey != "" && !s.gate(s.tokAudited, p.TokenID+"|"+gateKey) {
		return
	}
	result := "ok"
	switch {
	case status == http.StatusForbidden:
		result = "denied"
	case status == http.StatusTooManyRequests:
		result = "limited"
	case status >= 400:
		result = "error"
	}
	params, _ := json.Marshal(map[string]any{"procedure": store.Clip(path, auditPathLen), "status": status})
	e := store.AuditEntry{Actor: p.ActorID(), Action: "call", Params: string(params), Result: result, Source: store.AuditAPI}
	if p.Channel == ChannelMCP {
		e.Source = store.AuditMCP
	}
	if ip.IsValid() {
		e.IP = ip.String()
	}
	if err := s.st.Audit(context.WithoutCancel(ctx), s.now(), e); err != nil {
		s.log.Error("audit write failed", "action", "call", "err", err)
	}
}

// statusWriter remembers the status the handler answered with.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 && code >= 200 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() { http.NewResponseController(w.ResponseWriter).Flush() }

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

const (
	msgNotForTokens = "this call is not available to API tokens"
	msgProfile      = "this token's profile cannot do this"
	msgApproval     = "this needs the owner's approval; it is not available over the API"
)

// tokenDenied is step 4: the allow-list, the role of the profile against the procedure's level, and, for a
// procedure that needs the owner's approval, a grant. "" means allowed.
func (s *Service) tokenDenied(r *http.Request, tok store.APIToken, path string) string {
	access, listed := tokenProcedures[path]
	if !listed {
		return msgNotForTokens
	}
	if !roleAllows(profileRole(tok.Profile), levelOf(path)) {
		return msgProfile
	}
	if access != TokenAccessApproved {
		return ""
	}
	ctx := context.WithValue(r.Context(), procedureKey{}, path)
	if s.grantAllows(ctx, tok.ID, path) {
		return ""
	}
	// The dry run of a node fix changes nothing: a planning grant opens ApplyFix, but only when the request
	// itself says dry_run, so a mistake in the MCP layer cannot perform a fix nobody approved.
	if path == adminv1connect.HealthServiceApplyFixProcedure && planningAllows(ctx) && planningDryRun(r) {
		return ""
	}
	return msgApproval
}

// planningDryRun reports whether r is an ApplyFix request with dry_run set, and puts the body back for the
// handler. Anything it cannot read as such (compressed, another content type, too large) is not a dry run.
func planningDryRun(r *http.Request) bool {
	if enc := r.Header.Get("Content-Encoding"); enc != "" && enc != "identity" {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxPlanningBody+1))
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil || len(body) > maxPlanningBody {
		return false
	}
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var m adminv1.ApplyFixRequest
	switch mt {
	case "application/proto":
		err = proto.Unmarshal(body, &m)
	case "application/json":
		err = protojson.UnmarshalOptions{DiscardUnknown: true}.Unmarshal(body, &m)
	default:
		return false
	}
	return err == nil && m.DryRun
}

// serveToken is RequireSession for a request with a Bearer header.
func (s *Service) serveToken(w http.ResponseWriter, r *http.Request, next http.Handler) {
	ch := ChannelAPI
	if channelFrom(r.Context()) == ChannelMCP {
		ch = ChannelMCP // the MCP layer's own call: the /mcp request already paid the rate limit
	}
	ip := ClientIPFrom(r.Context())
	tok, fail := s.authenticateBearer(r, ch == ChannelAPI)
	if fail != nil {
		s.log.Warn("bearer refused", "reason", fail.reason, "ip", ip, "path", store.Clip(r.URL.Path, auditPathLen))
		if fail.status == http.StatusTooManyRequests {
			s.auditCall(r.Context(), principalOf(tok, ch), r.URL.Path, fail.status, ip, "429")
		}
		writeBearerFailure(w, fail)
		return
	}
	p := principalOf(tok, ch)
	s.touchToken(r.Context(), tok, ip, ch)
	path := r.URL.Path
	if msg := s.tokenDenied(r, tok, path); msg != "" {
		s.log.Info("token call refused", "token", tok.ID, "path", store.Clip(path, auditPathLen), "why", msg)
		s.auditCall(r.Context(), p, path, http.StatusForbidden, ip, "")
		b, _ := json.Marshal(map[string]string{"code": "permission_denied", "message": msg})
		writeJSONError(w, http.StatusForbidden, string(b))
		return
	}
	admin := store.Admin{ID: p.ActorID(), DisplayName: tok.Name, Role: profileRole(tok.Profile)}
	ctx := context.WithValue(r.Context(), adminKey{}, admin)
	ctx = context.WithValue(ctx, principalKey{}, p)
	ctx = context.WithValue(ctx, procedureKey{}, path)
	source := store.AuditAPI
	if ch == ChannelMCP {
		source = store.AuditMCP
	}
	ctx = store.WithAuditSource(ctx, source)

	sw := &statusWriter{ResponseWriter: w}
	defer func() {
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		gateKey := ""
		if levelOf(path) == levelRead && status < 400 {
			gateKey = path // a polling script's successful reads: once a minute per procedure
		}
		s.auditCall(r.Context(), p, path, status, ip, gateKey)
	}()
	next.ServeHTTP(sw, r.WithContext(ctx))
}

// RequireBearer wraps an endpoint that is not an admin procedure (the MCP endpoint) with token authentication:
// the same checks as the API (a well-formed secret, a live token, the token's rate limit), no cookie. It puts
// the Principal into the context (PrincipalFrom) and records the use; it writes no "call" row itself, the
// procedures the endpoint then calls do. The channel names how the token is used and what last_used_via shows.
func (s *Service) RequireBearer(next http.Handler, c Channel) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = s.WithClientIP(r)
		ip := ClientIPFrom(r.Context())
		tok, fail := s.authenticateBearer(r, true)
		if fail != nil {
			s.log.Warn("bearer refused", "reason", fail.reason, "ip", ip, "path", store.Clip(r.URL.Path, auditPathLen))
			if fail.status == http.StatusTooManyRequests {
				s.auditCall(r.Context(), principalOf(tok, c), r.URL.Path, fail.status, ip, "429")
			}
			writeBearerFailure(w, fail)
			return
		}
		s.touchToken(r.Context(), tok, ip, c)
		ctx := context.WithValue(r.Context(), principalKey{}, principalOf(tok, c))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
