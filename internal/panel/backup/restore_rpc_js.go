//go:build js

package backup

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

var _ adminv1connect.BackupServiceHandler = rpc{}

const restoreUnavailableMessage = "backup restore is available on the VPS edition"

func restoreUnavailable() error {
	return connect.NewError(connect.CodeUnimplemented, errors.New(restoreUnavailableMessage))
}

func writeRestoreConnectError(w http.ResponseWriter, r *http.Request, code connect.Code, message string) {
	_ = connect.NewErrorWriter().Write(w, r, connect.NewError(code, errors.New(message)))
}

func (s *Service) upload(w http.ResponseWriter, r *http.Request) {
	writeRestoreConnectError(w, r, connect.CodeUnimplemented, restoreUnavailableMessage)
}

func (r rpc) PrepareRestore(context.Context, *connect.Request[adminv1.PrepareRestoreRequest]) (*connect.Response[adminv1.PrepareRestoreResponse], error) {
	return nil, restoreUnavailable()
}

func (r rpc) ConfirmRestore(context.Context, *connect.Request[adminv1.ConfirmRestoreRequest]) (*connect.Response[adminv1.ConfirmRestoreResponse], error) {
	return nil, restoreUnavailable()
}

func (r rpc) ListKeptData(context.Context, *connect.Request[adminv1.ListKeptDataRequest]) (*connect.Response[adminv1.ListKeptDataResponse], error) {
	return nil, restoreUnavailable()
}

func (r rpc) DeleteKeptData(context.Context, *connect.Request[adminv1.DeleteKeptDataRequest]) (*connect.Response[adminv1.DeleteKeptDataResponse], error) {
	return nil, restoreUnavailable()
}

func (r rpc) GetRestoreResult(context.Context, *connect.Request[adminv1.GetRestoreResultRequest]) (*connect.Response[adminv1.GetRestoreResultResponse], error) {
	return nil, restoreUnavailable()
}
