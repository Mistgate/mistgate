//go:build js

package backup

import (
	"net/http"

	"connectrpc.com/connect"
	adminv1connect "github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	r := rpc{s}
	options := append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)
	handlerOptions := connect.WithHandlerOptions(options...)
	getSettings := connect.NewUnaryHandler(adminv1connect.BackupServiceGetBackupSettingsProcedure, r.GetBackupSettings, handlerOptions)
	updateSettings := connect.NewUnaryHandler(adminv1connect.BackupServiceUpdateBackupSettingsProcedure, r.UpdateBackupSettings, handlerOptions)
	testStorage := connect.NewUnaryHandler(adminv1connect.BackupServiceTestBackupStorageProcedure, r.TestBackupStorage, handlerOptions)
	createBackup := connect.NewUnaryHandler(adminv1connect.BackupServiceCreateBackupProcedure, r.CreateBackup, handlerOptions)
	listBackups := connect.NewUnaryHandler(adminv1connect.BackupServiceListBackupsProcedure, r.ListBackups, handlerOptions)
	return "/" + adminv1connect.BackupServiceName + "/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case adminv1connect.BackupServiceGetBackupSettingsProcedure:
			getSettings.ServeHTTP(w, r)
		case adminv1connect.BackupServiceUpdateBackupSettingsProcedure:
			updateSettings.ServeHTTP(w, r)
		case adminv1connect.BackupServiceTestBackupStorageProcedure:
			testStorage.ServeHTTP(w, r)
		case adminv1connect.BackupServiceCreateBackupProcedure:
			createBackup.ServeHTTP(w, r)
		case adminv1connect.BackupServiceListBackupsProcedure:
			listBackups.ServeHTTP(w, r)
		case adminv1connect.BackupServicePrepareRestoreProcedure,
			adminv1connect.BackupServiceConfirmRestoreProcedure,
			adminv1connect.BackupServiceListKeptDataProcedure,
			adminv1connect.BackupServiceDeleteKeptDataProcedure,
			adminv1connect.BackupServiceGetRestoreResultProcedure:
			writeRestoreConnectError(w, r, connect.CodeUnimplemented, restoreUnavailableMessage)
		default:
			http.NotFound(w, r)
		}
	})
}
