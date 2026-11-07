//go:build js

package app

import (
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// The panel's log ring, retention settings, export and metrics are the VPS edition's (proto/mistgate/ops/v1): the Workers keep
// none of their code, only this stub that answers every procedure of the service with UNIMPLEMENTED. It names the service by
// its path so no generated code of it is linked.
const opsServicePath = "/mistgate.ops.v1.OpsService/"

func (k *core) addOps(cfg *httpserver.Config) {
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: opsServicePath, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := connect.NewError(connect.CodeUnimplemented, errors.New("not available on this edition"))
		_ = connect.NewErrorWriter().Write(w, r, err)
	})})
}
