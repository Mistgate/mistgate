package auth

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// publicProcedures are the only admin API paths reachable without a session. The list
// is an allow-list on purpose: a service or streaming RPC added later is protected until
// someone decides otherwise here.
var publicProcedures = map[string]bool{
	adminv1connect.AuthServiceGetLoginInfoProcedure:  true,
	adminv1connect.AuthServiceBeginSetupProcedure:    true,
	adminv1connect.AuthServiceFinishSetupProcedure:   true,
	adminv1connect.AuthServiceBeginLoginProcedure:    true,
	adminv1connect.AuthServiceFinishLoginProcedure:   true,
	adminv1connect.AuthServicePasswordLoginProcedure: true,
	adminv1connect.AuthServiceLogoutProcedure:        true, // tolerates a missing/expired session
}

type (
	adminKey    struct{}
	sessionKey  struct{}
	clientIPKey struct{}
)

// ClientIPFrom returns the client address RequireSession resolved for the request (the
// TCP peer, or what a trusted proxy reported): the one to put in audit rows. Invalid
// outside RequireSession.
func ClientIPFrom(ctx context.Context) netip.Addr {
	a, _ := ctx.Value(clientIPKey{}).(netip.Addr)
	return a
}

// ClientIP is the client address for a request under the trusted-proxy rules.
func (s *Service) ClientIP(r *http.Request) netip.Addr {
	return s.trust.ClientIP(r.RemoteAddr, r.Header)
}

// WithClientIP returns r with its ClientIP in the context, for ClientIPFrom. RequireSession
// does this for the admin API; the public listener does it for everything it dispatches
// (subscription mounts, the agent endpoint).
func (s *Service) WithClientIP(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), clientIPKey{}, s.ClientIP(r)))
}

// WithAdmin returns ctx carrying a as RequireSession would put it, for tests of modules that read AdminFrom. A request
// cannot reach it: only the middleware sets the admin of a real call.
func WithAdmin(ctx context.Context, a store.Admin) context.Context {
	return context.WithValue(ctx, adminKey{}, a)
}

// AdminFrom returns the signed-in admin placed in ctx by RequireSession.
func AdminFrom(ctx context.Context) (store.Admin, bool) {
	a, ok := ctx.Value(adminKey{}).(store.Admin)
	return a, ok
}

// ActorLabel names the caller of an admin RPC for an event the admin reads ("who pressed it"): the admin's display
// name, else the id (an API token is "token:<id>"), "" when the call has no admin.
func ActorLabel(ctx context.Context) string {
	a, ok := AdminFrom(ctx)
	if !ok {
		return ""
	}
	if a.DisplayName != "" {
		return a.DisplayName
	}
	return a.ID
}

// RequireSession wraps the admin API handler. It sees request paths with the "/api"
// prefix already stripped ("/mistgate.admin.v1.XService/Method"). Every request whose
// path is not exactly a public procedure needs a valid session cookie, whatever the
// service, the RPC kind (unary or streaming) or whether the path even exists; the
// signed-in admin is put into the request context for AdminFrom. A request with an
// "Authorization: Bearer" header is an API token call instead (bearer.go): the token is
// checked, the procedure must be on the token allow-list and within the profile's role,
// and AdminFrom then returns a synthetic admin "token:<id>" (PrincipalFrom says so).
func (s *Service) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = s.WithClientIP(r)
		if publicProcedures[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		if hasBearer(r.Header) { // an API token: the token path only, a bad token never falls back to a cookie
			s.serveToken(w, r, next)
			return
		}
		sess, admin, err := s.resolveSession(r.Context(), sessionToken(r.Header))
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, unauthenticatedBody)
			return
		}
		// The role policy: one level per procedure (policy.go), enforced here for every service.
		if !roleAllows(admin.Role, levelOf(r.URL.Path)) {
			writeJSONError(w, http.StatusForbidden, forbiddenBody)
			return
		}
		ctx := context.WithValue(r.Context(), adminKey{}, admin)
		ctx = context.WithValue(ctx, sessionKey{}, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func writeJSONError(w http.ResponseWriter, status int, body string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	w.Write([]byte(body))
}

// The Connect-protocol errors for an unauthenticated call and for one the admin's role does not allow.
const (
	unauthenticatedBody = `{"code":"unauthenticated","message":"not signed in"}`
	forbiddenBody       = `{"code":"permission_denied","message":"your role cannot do this"}`
)
