//go:build !js

package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The edge has no scheduled or manual backup yet: the RPCs that make or schedule one (and list them) are refused with the
// coded reason before anything is read or saved, and they work on the VPS. Reading the settings keeps working on both.
func TestEdgeRefusesBackupRPCs(t *testing.T) {
	s, api, _ := newTestService(t)
	s.stepUp = func(context.Context) error { return nil }
	ctx := auth.WithAdmin(context.Background(), store.Admin{ID: "adm_o", Role: store.RoleOwner})
	r := rpc{s}
	before, err := s.st.PanelBackupSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	calls := map[string]func() error{
		"CreateBackup": func() error {
			_, err := r.CreateBackup(ctx, connect.NewRequest(&adminv1.CreateBackupRequest{}))
			return err
		},
		"UpdateBackupSettings": func() error {
			_, err := r.UpdateBackupSettings(ctx, connect.NewRequest(&adminv1.UpdateBackupSettingsRequest{
				AccountId: before.AccountID, Bucket: before.Bucket, AccessKeyId: before.AccessKeyID, AgeRecipient: before.AgeRecipient,
				Enabled: true, IntervalHours: 6, RetentionDays: 30,
			}))
			return err
		},
		"TestBackupStorage": func() error {
			_, err := r.TestBackupStorage(ctx, connect.NewRequest(&adminv1.TestBackupStorageRequest{}))
			return err
		},
		"ListBackups": func() error {
			_, err := r.ListBackups(ctx, connect.NewRequest(&adminv1.ListBackupsRequest{}))
			return err
		},
	}

	s.edge = true
	for name, call := range calls {
		err := call()
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "unavailable_in_edition: feature=backups") {
			t.Errorf("edge %s = %v, want the coded refusal", name, err)
		}
	}
	if after, err := s.st.PanelBackupSettings(ctx); err != nil || after.IntervalHours != before.IntervalHours || api.putCalls != 0 || s.running.Load() {
		t.Errorf("a refused call changed something: interval %d, uploads %d, running %v, %v", after.IntervalHours, api.putCalls, s.running.Load(), err)
	}
	if _, err := r.GetBackupSettings(ctx, connect.NewRequest(&adminv1.GetBackupSettingsRequest{})); err != nil {
		t.Errorf("edge GetBackupSettings = %v, reads must keep working", err)
	}

	s.edge = false
	for _, name := range []string{"UpdateBackupSettings", "TestBackupStorage", "ListBackups", "CreateBackup"} { // the backup goroutine last: it shares the fake bucket
		if err := calls[name](); err != nil {
			t.Errorf("VPS %s = %v", name, err)
		}
	}
	for deadline := time.Now().Add(10 * time.Second); s.running.Load() && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
	}
}
