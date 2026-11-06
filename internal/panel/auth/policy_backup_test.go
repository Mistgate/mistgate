package auth

import (
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestBackupDownloadPolicyIsOwnerOnlyAndNeedsStepUp(t *testing.T) {
	const path = BackupDownloadProcedure
	if got := ProcedureRole(path); got != store.RoleOwner {
		t.Errorf("backup download role = %q, want owner", got)
	}
	if !NeedsStepUp(path) {
		t.Error("backup download must require a fresh step-up")
	}
	if _, ok := TokenAccess(path); ok {
		t.Error("backup download must not be available to API tokens")
	}
}
