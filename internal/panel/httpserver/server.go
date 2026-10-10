// Package httpserver wires the panel's HTTP surface: the public listener (decoy site,
// the admin UI/API when it is reached by secret host or secret path prefix, the secret
// public mounts such as subscriptions, and the agent endpoint selected by a secret TLS
// SNI name) and an optional separate admin listener.
package httpserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/httpserver/ratelimit"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/web"
)

// AdminHandler is one more Connect service of the admin API. Path is the prefix the
// generated constructor returns ("/mistgate.admin.v1.NodeService/"). It is served at
// /api<Path> behind the session middleware, so handlers can rely on auth.AdminFrom.
type AdminHandler struct {
	Path    string
	Handler http.Handler
}

// AdminPage is a server-rendered page on the authenticated admin surface. Path must be
// an exact path without a trailing slash; both forms are served without redirects so a
// secret admin prefix is preserved.
type AdminPage struct {
	Path    string
	Handler http.Handler
}

// Config selects how the admin UI and API are reachable and what else the panel serves.
type Config struct {
	// AdminHost, if set, serves the admin on the public listener for requests whose Host
	// is exactly this name (mode a). Everything else on that host gets the decoy.
	AdminHost string
	// AdminPrefix, if set ("/" + secret + "/"), serves the admin on the public listener
	// under that path prefix (mode b).
	AdminPrefix string
	// DecoyDir holds the decoy site; empty means the built-in "coming soon" page.
	DecoyDir string
	// TrustedOrigins are browser origins allowed to make state-changing requests to the
	// admin in addition to same-origin (the WebAuthn origins; in dev the Vite server).
	TrustedOrigins []string
	// Dist is the SPA build; nil means the embedded web/dist.
	Dist fs.FS
	Log  *slog.Logger

	// AdminHandlers are further admin API services (nodes, users, profiles ...), mounted
	// with AuthService and InstanceService behind the session middleware.
	AdminHandlers []AdminHandler
	// AdminPages are Go-rendered pages mounted on the signed-in admin surface. They use
	// the same owner-only session policy as unknown admin procedures and do not accept API tokens.
	AdminPages []AdminPage
	// PublicMounts maps secret prefixes ("/" + secret + "/", like AdminPrefix) on the
	// public listener to public handlers, e.g. the subscription endpoint. The handler sees
	// the path with the prefix (minus its trailing slash) stripped. Everything outside the
	// admin and these prefixes is the decoy.
	PublicMounts map[string]http.Handler

	// Limits are the per-client request limits of the public listener, the admin surface and
	// the agent endpoint; zero values mean the defaults.
	Limits Limits

	// AgentSNI, AgentTLS and AgentHandler serve the node agents on the public TLS
	// listener. A TLS handshake whose server name is AgentSNI gets the tls.Config AgentTLS
	// returns for that ClientHello (server certificate from the panel's own CA, client
	// certificates, ...), and the requests on such a connection go to AgentHandler. The
	// listener's read/write deadlines are lifted only for a stream opened with a verified
	// client certificate (agent streams are long-lived); the unauthenticated Enroll call, and
	// anything else without a certificate, keeps them and a small body limit.
	// Requests that did not arrive on such a connection never reach AgentHandler. The
	// endpoint exists only when AgentTLS and AgentHandler are both set (AgentSNI alone is
	// accepted, so the name can be carried before the fleet module is wired in).
	AgentSNI     string
	AgentTLS     func(*tls.ClientHelloInfo) (*tls.Config, error)
	AgentHandler http.Handler

	// AgentLinkPrefix and AgentLinkHandler serve the signed WebSocket transport on the public listener.
	// The prefix is a stored secret path; unrelated paths continue to the decoy and public mounts.
	AgentLinkPrefix  string
	AgentLinkHandler http.Handler

	// UserPagePreview, if set, serves GET <admin>/preview/user-page/<user id>: the public user page as that user
	// would see it, for the admin to show in a frame. The server checks the session and the right to see the user's
	// link (GetSubscriptionLink: helper or owner, never a token) and validates the id; the function writes the
	// response (its own CSP with frame-ancestors 'self').
	UserPagePreview func(w http.ResponseWriter, r *http.Request, userID string)

	// AdminURL and SubscriptionBase are where the admin is reached and the base of the subscription links, as `mistgate
	// setup` stored them: InstanceService shows them to the owner (Settings -> Domains), read-only.
	AdminURL, SubscriptionBase string

	// MCP, if set, builds the handler of the MCP endpoint from the in-process admin API: the services
	// behind the session middleware, request paths without the "/api" prefix. The result is served at <admin>/mcp, and only
	// on the admin surface (behind the secret prefix, host or separate listener): never on the decoy side, the public mounts
	// or the agent endpoint. The handler authenticates its own requests (a bearer token, never a cookie). nil: no endpoint.
	MCP func(api http.Handler) http.Handler
}

// mcpWriteWindow is how long an MCP call may take to answer: longer than the listener's write timeout, because a plan that
// applies a fix or a doctor refresh waits for a node.
const mcpWriteWindow = 3 * time.Minute

// Rate is a token bucket per client: Burst requests at once, then PerSecond. The zero value means the
// default; a negative PerSecond switches the limit off.
type Rate struct {
	PerSecond float64
	Burst     int
}

// Limits are the request limits per client network (IPv4 address, IPv6 /64). Over the limit a request is
// answered with a 429 that looks like a web server's own, with Retry-After. A long-lived stream counts
// once, when it is opened.
type Limits struct {
	Public Rate // decoy site, robots.txt and the public mounts (subscriptions)
	Admin  Rate // the admin UI and API, whichever way it is reached
	Agent  Rate // the node agent endpoint
	// MaxKeys bounds the clients tracked per limit (memory); the least recently seen is forgotten first.
	MaxKeys int
}

// limiter builds the limiter for r, or for def when r is the zero value.
func (l Limits) limiter(r, def Rate) *ratelimit.Limiter {
	if r == (Rate{}) {
		r = def
	}
	keys := l.MaxKeys
	if keys <= 0 {
		keys = 10000
	}
	return ratelimit.New(r.PerSecond, r.Burst, keys)
}

// agentUnauthBody caps the body of a request on the agent SNI that has no certificate (Enroll carries
// a token and a CSR: a few hundred bytes).
const agentUnauthBody = 32 << 10

// Server builds the HTTP handlers.
type Server struct {
	cfg   Config
	auth  *auth.Service
	st    *store.Store
	decoy *decoy
	dist  fs.FS
	cop   *http.CrossOriginProtection

	api       apiRouter
	pages     map[string]http.Handler
	logo      http.Handler
	mounts    []mount
	agentSNI  string // normalised
	agentLink http.Handler

	preview http.Handler // GET /preview/user-page/<id> behind the session, nil when not configured
	mcp     http.Handler // POST <admin>/mcp, nil when not configured

	hostAdmin   http.Handler // prefix "/"
	prefixAdmin http.Handler // with AdminPrefix stripped

	pubLim, adminLim, agentLim *ratelimit.Limiter
}

// withPublicContext adds both public-listener values before cloning the request once.
func withPublicContext(r *http.Request, a *auth.Service) *http.Request {
	ctx := a.ContextWithClientIP(r.Context(), r)
	ctx = context.WithValue(ctx, startKey{}, time.Now())
	return r.WithContext(ctx)
}

type mount struct {
	prefix  string
	handler http.Handler // prefix stripped
}

// apiRouter dispatches /api/... (after "/api" is stripped) to the admin services by path
// prefix. It is deliberately not a ServeMux: a mux answers "/x" with a redirect to "/x/",
// and behind a secret prefix that redirect would drop the prefix.
type apiRouter []AdminHandler

func (a apiRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	for _, h := range a {
		if strings.HasPrefix(r.URL.Path, h.Path) {
			if isStream(r) {
				liftDeadlines(w)
			}
			h.Handler.ServeHTTP(w, r)
			return
		}
	}
	http.NotFound(w, r)
}

// isStream reports a Connect or gRPC streaming-capable request: its response may last
// longer than the listener's write timeout.
func isStream(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.HasPrefix(ct, "application/connect+") || strings.HasPrefix(ct, "application/grpc")
}

// liftDeadlines removes the server's read and write deadlines for this request. An error
// only means the ResponseWriter does not support deadlines (a test recorder): nothing to lift.
func liftDeadlines(w http.ResponseWriter) {
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Time{})
	_ = rc.SetWriteDeadline(time.Time{})
}

func validSecretPrefix(p string) bool {
	return len(p) >= 4 && strings.HasPrefix(p, "/") && strings.HasSuffix(p, "/")
}

// New validates cfg and builds the server. st is the panel database (instance settings,
// logo).
func New(cfg Config, a *auth.Service, st *store.Store) (*Server, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.AdminPrefix != "" && !validSecretPrefix(cfg.AdminPrefix) {
		return nil, fmt.Errorf("admin prefix %q must look like /secret/", cfg.AdminPrefix)
	}
	if (cfg.AgentLinkPrefix == "") != (cfg.AgentLinkHandler == nil) {
		return nil, errors.New("AgentLinkPrefix and AgentLinkHandler go together")
	}
	if cfg.AgentLinkPrefix != "" && (!validSecretPrefix(cfg.AgentLinkPrefix) || len(strings.Trim(cfg.AgentLinkPrefix, "/")) < 16 ||
		cfg.AgentLinkPrefix == cfg.AdminPrefix || (cfg.AdminPrefix != "" &&
		(strings.HasPrefix(cfg.AgentLinkPrefix, cfg.AdminPrefix) || strings.HasPrefix(cfg.AdminPrefix, cfg.AgentLinkPrefix)))) {
		return nil, errors.New("AgentLinkPrefix must be a separate secret path prefix of at least 16 characters")
	}
	cfg.AdminHost = strings.ToLower(strings.TrimSuffix(cfg.AdminHost, "."))
	s := &Server{cfg: cfg, auth: a, st: st, decoy: newDecoy(cfg.DecoyDir), dist: cfg.Dist, cop: http.NewCrossOriginProtection()}
	s.pubLim = cfg.Limits.limiter(cfg.Limits.Public, Rate{PerSecond: 10, Burst: 60})
	s.adminLim = cfg.Limits.limiter(cfg.Limits.Admin, Rate{PerSecond: 30, Burst: 200}) // a page load and its polling
	s.agentLim = cfg.Limits.limiter(cfg.Limits.Agent, Rate{PerSecond: 5, Burst: 30})
	if s.dist == nil {
		s.dist = web.Dist()
	}
	for _, o := range cfg.TrustedOrigins {
		if err := s.cop.AddTrustedOrigin(o); err != nil {
			return nil, fmt.Errorf("trusted origin %q: %w", o, err)
		}
	}

	authPath, authHandler := a.Handler()
	inst := &instanceAPI{st: st, log: cfg.Log, adminURL: cfg.AdminURL, subBase: cfg.SubscriptionBase}
	instPath, instHandler := inst.handler()
	s.api = apiRouter{{authPath, authHandler}, {instPath, instHandler}}
	seen := map[string]bool{authPath: true, instPath: true}
	for _, h := range cfg.AdminHandlers {
		if !validSecretPrefix(h.Path) || h.Handler == nil || seen[h.Path] {
			return nil, fmt.Errorf("admin handler path %q: must look like /pkg.Service/, be unique and have a handler", h.Path)
		}
		seen[h.Path] = true
		s.api = append(s.api, h)
	}
	seenPages := make(map[string]bool, len(cfg.AdminPages))
	s.pages = make(map[string]http.Handler, len(cfg.AdminPages)*2)
	for _, page := range cfg.AdminPages {
		if !validAdminPagePath(page.Path) || page.Handler == nil || seenPages[page.Path] {
			return nil, fmt.Errorf("admin page path %q: must be a unique absolute route without a trailing slash and have a handler", page.Path)
		}
		seenPages[page.Path] = true
		protected := a.RequireSession(page.Handler)
		s.pages[page.Path] = protected
		s.pages[page.Path+"/"] = protected
	}
	s.logo = NewLogoHandler(st, cfg.Log)
	if cfg.MCP != nil {
		s.mcp = cfg.MCP(a.RequireSession(s.api))
	}

	for prefix, h := range cfg.PublicMounts {
		if !validSecretPrefix(prefix) || h == nil || prefix == cfg.AdminPrefix ||
			(cfg.AdminPrefix != "" && (strings.HasPrefix(prefix, cfg.AdminPrefix) || strings.HasPrefix(cfg.AdminPrefix, prefix))) {
			return nil, fmt.Errorf("public mount %q: must look like /secret/, have a handler and not overlap the admin prefix", prefix)
		}
		for _, m := range s.mounts {
			if strings.HasPrefix(prefix, m.prefix) || strings.HasPrefix(m.prefix, prefix) {
				return nil, fmt.Errorf("public mounts %q and %q overlap", prefix, m.prefix)
			}
		}
		if cfg.AgentLinkPrefix != "" && (strings.HasPrefix(prefix, cfg.AgentLinkPrefix) || strings.HasPrefix(cfg.AgentLinkPrefix, prefix)) {
			return nil, fmt.Errorf("public mount %q overlaps the agent link prefix", prefix)
		}
		s.mounts = append(s.mounts, mount{prefix, http.StripPrefix(strings.TrimSuffix(prefix, "/"), s.decoy.swapErrors(h))})
	}
	sort.Slice(s.mounts, func(i, j int) bool { return s.mounts[i].prefix < s.mounts[j].prefix })
	if cfg.AgentLinkHandler != nil {
		s.agentLink = http.StripPrefix(strings.TrimSuffix(cfg.AgentLinkPrefix, "/"), s.decoy.swapErrors(cfg.AgentLinkHandler))
	}

	if (cfg.AgentTLS == nil) != (cfg.AgentHandler == nil) || (cfg.AgentTLS != nil && cfg.AgentSNI == "") {
		return nil, errors.New("AgentTLS and AgentHandler go together and need AgentSNI")
	}
	if cfg.AgentSNI != "" {
		s.agentSNI = normSNI(cfg.AgentSNI)
		if strings.ContainsAny(s.agentSNI, "/: ") || !strings.Contains(s.agentSNI, ".") {
			return nil, fmt.Errorf("agent SNI %q must be a host name like abc123.invalid", cfg.AgentSNI)
		}
	}

	if cfg.UserPagePreview != nil {
		s.preview = s.requireLinkAccess(http.HandlerFunc(s.servePreview))
	}
	if cfg.AdminHost != "" {
		s.hostAdmin = s.newAdmin("/")
	}
	if cfg.AdminPrefix != "" {
		s.prefixAdmin = http.StripPrefix(strings.TrimSuffix(cfg.AdminPrefix, "/"), s.newAdmin(cfg.AdminPrefix))
	}
	return s, nil
}

func validAdminPagePath(route string) bool {
	if route == "" || route == "/" || !strings.HasPrefix(route, "/") || strings.HasSuffix(route, "/") ||
		strings.ContainsAny(route, "?#\\") || strings.Contains(route, "//") || route != path.Clean(route) {
		return false
	}
	for _, reserved := range []string{"/api", "/mcp", "/brand", "/assets", "/preview"} {
		if route == reserved || strings.HasPrefix(route, reserved+"/") {
			return false
		}
	}
	return true
}

// agentRequest reports whether r arrived on a TLS connection negotiated for the agent SNI.
func (s *Server) agentRequest(r *http.Request) bool {
	return s.agentHooks() && r.TLS != nil && ctEqual(normSNI(r.TLS.ServerName), s.agentSNI)
}

// serveAgent runs the agent handler. Only a stream opened with a verified client certificate (the
// TLS config asks for one without requiring it, so Enroll can come without) loses the listener's
// deadlines; the unauthenticated Enroll RPC and anything else without a certificate keeps the
// read timeouts and gets a small body limit, so a client cannot park connections on the endpoint
// (security review F2). The certificate is verified by crypto/tls and the fleet's VerifyConnection
// before any request is served on the connection.
func (s *Server) serveAgent(w http.ResponseWriter, r *http.Request) {
	if !s.allow(s.agentLim, w, r) {
		return
	}
	if agentStream(r) {
		liftDeadlines(w)
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, agentUnauthBody)
	}
	s.cfg.AgentHandler.ServeHTTP(w, r)
}

// agentStream reports a long-lived call by an authenticated agent: a streaming content type, a client
// certificate, and not the Enroll procedure.
func agentStream(r *http.Request) bool {
	return r.TLS != nil && len(r.TLS.VerifiedChains) > 0 &&
		r.URL.Path != agentv1connect.EnrollmentServiceEnrollProcedure && isStream(r)
}

// allow takes a token of l for the client of r; when there is none it writes the 429 and returns false.
func (s *Server) allow(l *ratelimit.Limiter, w http.ResponseWriter, r *http.Request) bool {
	ok, retry := l.Allow(auth.SourceKey(s.auth.ClientIP(r)))
	if !ok {
		s.decoy.tooMany(w, r, retry)
	}
	return ok
}

// Public is the handler for the main listener. Agent connections go to the agent
// handler; non-canonical paths, unknown hosts, wrong or missing prefixes and unknown
// paths all get the decoy.
func (s *Server) Public() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = withPublicContext(r, s.auth) // auth.ClientIPFrom for the mounts and the agent endpoint
		if s.agentRequest(r) {
			s.serveAgent(w, r) // its own limit
			return
		}
		canonical := canonicalPath(r)
		if canonical && s.agentLinkRequest(r) {
			if !s.allow(s.agentLim, w, r) {
				return
			}
			if isWebSocketUpgrade(r) {
				liftDeadlines(w)
			}
			s.agentLink.ServeHTTP(w, r)
			return
		}
		if canonical && s.hostAdmin != nil && ctEqual(hostOnly(r.Host), s.cfg.AdminHost) {
			if s.allow(s.adminLim, w, r) {
				s.hostAdmin.ServeHTTP(w, r)
			}
			return
		}
		if canonical && s.prefixAdmin != nil && hasSecretPrefix(r.URL.Path, s.cfg.AdminPrefix) {
			if s.allow(s.adminLim, w, r) {
				s.prefixAdmin.ServeHTTP(w, r)
			}
			return
		}
		if !s.allow(s.pubLim, w, r) {
			return
		}
		if !canonical {
			s.decoy.notFound(w, r)
			return
		}
		for _, m := range s.mounts {
			if hasSecretPrefix(r.URL.Path, m.prefix) {
				m.handler.ServeHTTP(w, r)
				return
			}
		}
		s.decoy.ServeHTTP(w, r)
	})
}

func (s *Server) agentLinkRequest(r *http.Request) bool {
	if s.agentLink == nil || !hasSecretPrefix(r.URL.Path, s.cfg.AgentLinkPrefix) {
		return false
	}
	rel := strings.TrimPrefix(r.URL.Path, strings.TrimSuffix(s.cfg.AgentLinkPrefix, "/"))
	if rel == agentv1connect.EnrollmentServiceEnrollProcedure { // link-only agents enrol here; nothing else but /link/<id>
		return true
	}
	return strings.HasPrefix(rel, "/link/") && len(strings.TrimPrefix(rel, "/link/")) > 0 &&
		!strings.Contains(strings.TrimPrefix(rel, "/link/"), "/")
}

func isWebSocketUpgrade(r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, token := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
			return true
		}
	}
	return false
}

// hasSecretPrefix compares the start of path with prefix in constant time.
func hasSecretPrefix(path, prefix string) bool {
	if len(path) > len(prefix) {
		path = path[:len(prefix)]
	}
	return ctEqual(path, prefix)
}

// AdminListener is the handler for a separate admin listener (mode c): prefix "/".
func (s *Server) AdminListener() http.Handler {
	admin := s.newAdmin("/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = withStart(r)
		if !s.allow(s.adminLim, w, r) {
			return
		}
		if !canonicalPath(r) {
			s.decoy.notFound(w, r)
			return
		}
		admin.ServeHTTP(w, r)
	})
}

// newAdmin builds the admin handler. base is the URL prefix the browser sees ("/" or
// "/secret/"); it only matters for the <base href> rewrite. Requests arrive with the
// prefix already stripped. Routing is by explicit path tests, never a ServeMux, so the
// admin never answers with a redirect (which would lose the secret prefix).
func (s *Server) newAdmin(base string) http.Handler {
	api := http.StripPrefix("/api", s.auth.RequireSession(s.api))
	spa := s.spa(base)
	return s.securityHeaders(s.cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case strings.HasPrefix(p, "/api/"): // "/api" itself is a 404 in the SPA handler
			api.ServeHTTP(w, r)
		case p == "/mcp" && s.mcp != nil:
			http.NewResponseController(w).SetWriteDeadline(time.Now().Add(mcpWriteWindow)) // the error: a writer without deadlines
			s.mcp.ServeHTTP(w, r)
		case p == "/brand/logo.svg":
			s.logo.ServeHTTP(w, r)
		case s.preview != nil && strings.HasPrefix(p, previewPrefix):
			s.preview.ServeHTTP(w, r)
		case s.pages[p] != nil:
			s.pages[p].ServeHTTP(w, r)
		default:
			spa.ServeHTTP(w, r)
		}
	})))
}

// cloudflare is the one third-party origin the admin pages may use: while the captcha is on, and on the page where the
// owner proves new keys before switching it on (securityPage; the SPA loads that document afresh for it).
const cloudflare = "https://challenges.cloudflare.com"

// securityPage is Settings -> Security, the SPA route where the Turnstile keys are tried.
const securityPage = "/settings/security"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		script, frame := "script-src 'self'; ", ""
		// Only the page itself needs the exception; the API and the hashed assets skip the settings read.
		if p := r.URL.Path; p == securityPage || !strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/assets/") && p != "/mcp" && s.auth.TurnstileEnabled(r.Context()) {
			// 'self' stays: the admin frames its own user-page preview
			script, frame = "script-src 'self' "+cloudflare+"; ", "frame-src 'self' "+cloudflare+"; "
		}
		h.Set("Content-Security-Policy", "default-src 'self'; "+script+frame+"style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; "+
			"base-uri 'self'; form-action 'self'; frame-ancestors 'none'")
		h["X-Content-Type-Options"] = headerNoSniff[:1:1]
		h["X-Frame-Options"] = headerFrameDeny[:1:1]
		h["Referrer-Policy"] = headerNoReferrer[:1:1]
		h["Cross-Origin-Opener-Policy"] = headerCOOPSameOrigin[:1:1]
		h["X-Robots-Tag"] = headerRobotsNoIndex[:1:1]
		h["Cache-Control"] = headerCacheNoStore[:1:1]
		if r.TLS != nil {
			h["Strict-Transport-Security"] = headerHSTS[:1:1]
		}
		next.ServeHTTP(w, r)
	})
}

// ServeOptions are the listener settings for Serve.
type ServeOptions struct {
	Public      string // address of the main listener (decoy, and admin in modes a/b)
	Admin       string // optional separate admin listener address
	AgentListen string // optional separate TLS listener for the agent endpoint (dev; needs the agent hooks)
	TLSCert     string // optional; applies to the public listener
	TLSKey      string
	// ACMEDomains get Let's Encrypt certificates on the public listener (TLS-ALPN-01, so it
	// must be reachable on port 443), with the cache in ACMEDir. ACMEHTTP, if set (":80"), is
	// a listener for HTTP-01 and the http -> https redirect.
	ACMEDomains []string
	ACMEEmail   string
	ACMEDir     string
	ACMEHTTP    string
}

// serverLog is net/http's error log. A failed TLS handshake is what scanners and bots probing a public port cause,
// dozens an hour, so it is debug; anything else stays a warning.
type serverLog struct{ l *slog.Logger }

func (w serverLog) Write(p []byte) (int, error) {
	msg := strings.TrimSuffix(string(p), "\n")
	if strings.HasPrefix(msg, "http: TLS handshake error") {
		w.l.Debug(msg)
	} else {
		w.l.Warn(msg)
	}
	return len(p), nil
}

// Serve runs the listeners until ctx is cancelled, then shuts them down gracefully.
func (s *Server) Serve(ctx context.Context, o ServeOptions) error {
	type listener struct {
		name string
		srv  *http.Server
		tls  bool
	}
	mk := func(addr string, h http.Handler, tlsCfg *tls.Config) *http.Server {
		return &http.Server{
			Addr:              addr,
			Handler:           h,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       2 * time.Minute,
			MaxHeaderBytes:    16 << 10,
			TLSConfig:         tlsCfg,
			ErrorLog:          log.New(serverLog{s.cfg.Log}, "", 0),
		}
	}
	tlsCfg, mgr, err := s.publicTLS(o)
	if err != nil {
		return err
	}
	ls := []listener{{"public", mk(o.Public, s.Public(), tlsCfg), tlsCfg != nil}}
	if o.Admin != "" {
		ls = append(ls, listener{"admin", mk(o.Admin, s.AdminListener(), nil), false})
	}
	if o.AgentListen != "" {
		if !s.agentHooks() {
			return errors.New("an agent listener needs the agent hooks (fleet) to be configured")
		}
		srv := mk(o.AgentListen, http.HandlerFunc(s.serveAgent), &tls.Config{MinVersion: tls.VersionTLS12, GetConfigForClient: s.agentConfig})
		ls = append(ls, listener{"agent", srv, true})
	}
	if mgr != nil && o.ACMEHTTP != "" {
		_, port, _ := net.SplitHostPort(o.Public)
		ls = append(ls, listener{"acme-http", mk(o.ACMEHTTP, s.acmeHTTP(mgr, o.ACMEDomains, port), nil), false})
	}

	errc := make(chan error, len(ls))
	for _, l := range ls {
		ln, err := net.Listen("tcp", l.srv.Addr)
		if err != nil {
			return err
		}
		s.cfg.Log.Info("listening", "listener", l.name, "addr", ln.Addr().String(), "tls", l.tls)
		go func() {
			if l.tls {
				errc <- l.srv.ServeTLS(ln, "", "") // certificates come from TLSConfig
			} else {
				errc <- l.srv.Serve(decoyErrors(ln, s.decoy))
			}
		}()
	}
	var first error
	select {
	case first = <-errc:
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, l := range ls {
		l.srv.Shutdown(sctx)
	}
	if errors.Is(first, http.ErrServerClosed) {
		return nil
	}
	return first
}
