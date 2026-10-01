package httpserver

import (
	"net/http"
	"strings"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// previewPrefix is the admin path of the user-page preview: <admin>/preview/user-page/<user id>.
const previewPrefix = "/preview/user-page/"

// requireLinkAccess lets a request through only for a caller who may fetch the user's subscription link: the
// preview renders the user page with the real link, and opening it may issue the user's missing credentials. It
// borrows that policy instead of repeating it: the session middleware judges the request as a call of
// UserService.GetSubscriptionLink (helper or owner, closed to API tokens), and the handler behind it sees the
// real request with the admin in its context.
func (s *Server) requireLinkAccess(next http.Handler) http.Handler {
	gate := func(orig *http.Request) http.Handler {
		return s.auth.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, probe *http.Request) {
			next.ServeHTTP(w, orig.WithContext(probe.Context()))
		}))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe := r.Clone(r.Context())
		probe.URL.Path = adminv1connect.UserServiceGetSubscriptionLinkProcedure
		gate(r).ServeHTTP(w, probe)
	})
}

// servePreview validates the user id and hands over to the configured preview function.
func (s *Server) servePreview(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, previewPrefix)
	if r.Method != http.MethodGet && r.Method != http.MethodHead || !validPreviewID(id) {
		http.NotFound(w, r)
		return
	}
	s.cfg.UserPagePreview(w, r, id)
}

// validPreviewID: the ids the store issues are a kind prefix and lower-case base32 ("usr_...").
func validPreviewID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
