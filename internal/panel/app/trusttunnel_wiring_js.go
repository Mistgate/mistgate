//go:build js

package app

import (
	"net/http"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// TrustTunnel is not offered in the Cloudflare edition's first version: the Workers keep none of its code (proto/mistgate/
// trusttunnel/v1), only this stub, which names the service by its path so no generated code is linked, and refuses every
// procedure the way the admin words a feature the edition lacks.
const trustTunnelServicePath = "/mistgate.trusttunnel.v1.TrustTunnelService/"

func (k *core) addTrustTunnel(cfg *httpserver.Config) {
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: trustTunnelServicePath, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = connect.NewErrorWriter().Write(w, r, edition.Refuse(edition.TrustTunnel))
	})})
}
