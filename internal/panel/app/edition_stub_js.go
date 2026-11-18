//go:build js

package app

import (
	"net/http"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// refuseInEdition mounts, at the path of a VPS-only service, a handler that answers every procedure with the admin's refusal for a
// feature the edition lacks (unavailable_in_edition: feature=<id>). One function for all those stubs.
func refuseInEdition(cfg *httpserver.Config, path, feature string) {
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: path, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = connect.NewErrorWriter().Write(w, r, edition.Refuse(feature))
	})})
}
