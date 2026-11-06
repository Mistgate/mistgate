//go:build !js

package backup

import (
	"net/http"

	"connectrpc.com/connect"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewBackupServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)...)
}
