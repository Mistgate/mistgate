//go:build js && wasm

package store

import (
	"bytes"
	"context"
	"embed"
	"syscall/js"
	"testing"
	"time"
)

//go:embed testdata/schema.golden
var d1SchemaGolden embed.FS

func TestD1MigrationParity(t *testing.T) {
	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	st, err := OpenD1(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	got, err := normalizedSchema(context.Background(), st.R)
	if err != nil {
		t.Fatal(err)
	}
	want, err := d1SchemaGolden.ReadFile("testdata/schema.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Fatalf("D1 schema differs from the SQLite golden")
	}

	wantMigrations, err := d1Migrations()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.R.QueryContext(context.Background(), `SELECT version_id FROM goose_db_version
		WHERE is_applied = TRUE AND version_id > 0 ORDER BY version_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotVersions []int64
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		gotVersions = append(gotVersions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(gotVersions) != len(wantMigrations) {
		t.Fatalf("applied %d migration versions, want %d", len(gotVersions), len(wantMigrations))
	}
	for i, migration := range wantMigrations {
		if gotVersions[i] != migration.version {
			t.Errorf("migration version %d = %d, want %d", i, gotVersions[i], migration.version)
		}
	}
}

func TestD1StoreSmoke(t *testing.T) {
	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	st, err := OpenD1(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	// This smoke covers Setting, SetSettings, Audit, and ListAudit. Store methods
	// that read inside a transaction remain deferred to phase 1, step 2, and currently
	// return d1driver.ErrReadInTx. That list includes AddPassword, ResetPasswordLogin,
	// RecordLoginFailure, CreateFirstAdmin, CreateFirstAdminPassword, DeletePasskey,
	// CreateEnrollment, Enroll, NodeHello, RetireNode, IngestStats, IngestEvent,
	// CreateAPIToken, RevokeAPIToken, CreateMCPPlan, DecideMCPPlan,
	// ApproveMCPPlanWithSecret, BeginApply, TakeMCPPlanOwnerSecret, OpenAlert,
	// InsertProbeCredIdx, SetTelegramBot, BindTelegramChat, RequeueNodeProvisionJobs,
	// RequestCancelNodeProvisionJob, ClaimNodeProvisionJob, AwgPrepareStarted,
	// AwgPrepareFinish, Access.UpdateProfile, Access.DeleteProfile, Access.DeleteInbound,
	// Access.CreateGroup, Access.UpdateGroup, Access.DeleteGroup, Access.RevokeDevice,
	// Access.AddAWGDevice, Access.RotateAWGDevice, and Access.EnsureImplicitAWGCreds.
	if err := st.SetSettings(ctx, map[string]string{"edge.smoke": "ready"}); err != nil {
		t.Fatal(err)
	}
	if value, err := st.Setting(ctx, "edge.smoke"); err != nil || value != "ready" {
		t.Fatalf("Setting() = %q, %v", value, err)
	}
	if err := st.Audit(ctx, time.Unix(123, 0).UTC(), AuditEntry{Actor: "anonymous", Action: "edge.smoke", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListAudit(ctx, "", 0, 10)
	if err != nil || len(rows) != 1 || rows[0].Action != "edge.smoke" {
		t.Fatalf("ListAudit() = %+v, %v", rows, err)
	}
}
