package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestBackupTransferPolicyIsOwnerOnlyAndNeedsStepUp(t *testing.T) {
	for _, path := range []string{BackupDownloadProcedure, BackupUploadProcedure} {
		if got := ProcedureRole(path); got != store.RoleOwner {
			t.Errorf("%s role = %q, want owner", path, got)
		}
		if !NeedsStepUp(path) {
			t.Errorf("%s must require a fresh step-up", path)
		}
		if _, ok := TokenAccess(path); ok {
			t.Errorf("%s must not be available to API tokens", path)
		}
	}
}

func TestBackupRestorePolicyIsOwnerOnlyAndStepUpWhereItChangesData(t *testing.T) {
	for _, path := range []string{
		adminv1connect.BackupServicePrepareRestoreProcedure,
		adminv1connect.BackupServiceConfirmRestoreProcedure,
		adminv1connect.BackupServiceDeleteKeptDataProcedure,
	} {
		if got := ProcedureRole(path); got != store.RoleOwner {
			t.Errorf("%s role = %q, want owner", path, got)
		}
		if !NeedsStepUp(path) {
			t.Errorf("%s must require a fresh step-up", path)
		}
		if _, ok := TokenAccess(path); ok {
			t.Errorf("%s must not be available to API tokens", path)
		}
	}
	for _, path := range []string{adminv1connect.BackupServiceListKeptDataProcedure, adminv1connect.BackupServiceGetRestoreResultProcedure} {
		if got := ProcedureRole(path); got != store.RoleOwner {
			t.Errorf("%s role = %q, want owner", path, got)
		}
		if _, ok := TokenAccess(path); ok {
			t.Errorf("%s must not be available to API tokens", path)
		}
	}
}
