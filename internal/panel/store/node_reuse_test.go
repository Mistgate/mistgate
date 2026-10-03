package store

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
)

func TestRetiredNodeAndFinishedProvisionNamesCanBeReused(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "panel.db")
	w, err := openDB(path, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	r, err := openDB(path, 4, true)
	if err != nil {
		w.Close()
		t.Fatal(err)
	}
	st := &Store{W: w, R: r}
	t.Cleanup(func() { _ = st.Close() })
	migrations, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, st.W, migrations)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 32); err != nil {
		t.Fatalf("prepare pre-fix schema: %v", err)
	}

	if _, err := st.W.ExecContext(ctx, `
		INSERT INTO node (id, name, address, state, created_at, retired_at)
		VALUES ('nod_old', 'de1', 'de1.example.com', 'retired', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `
		INSERT INTO node_provision_job (
			id, node_id, name, address, ssh_host, ssh_port, host_fingerprint, secret,
			state, phase, created_by, created_at, updated_at
		) VALUES ('prv_old', 'nod_old', 'de1', 'de1.example.com', '192.0.2.1', 22,
			'SHA256:old', x'01', 'completed', 'complete', 'adm_old', 1, 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `
		INSERT INTO node_provision_event (job_id, phase, code, created_at)
		VALUES ('prv_old', 'complete', 'installed', 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("apply name reuse migration: %v", err)
	}

	var oldState string
	if err := st.W.QueryRowContext(ctx, `SELECT state FROM node WHERE id = 'nod_old'`).Scan(&oldState); err != nil || oldState != "retired" {
		t.Fatalf("retired node history = %q, err %v", oldState, err)
	}
	if got := countT(t, st, `SELECT count(*) FROM node_provision_event WHERE job_id = 'prv_old' AND code = 'installed'`); got != 1 {
		t.Fatalf("finished install event count = %d, want 1", got)
	}
	var foreignKeyProblems int
	rows, err := st.W.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		foreignKeyProblems++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if foreignKeyProblems != 0 {
		t.Fatalf("migration left %d foreign-key violations", foreignKeyProblems)
	}

	now := time.Unix(1_800_000_000, 0).UTC()
	node, err := st.CreateEnrollment(ctx, &NodeRow{ID: "nod_reused", Name: "DE1", Address: "new.example.com"}, "", []byte{1}, "adm_test", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("reuse a retired node name: %v", err)
	}
	makeJob := func(id string) NodeProvisionJob {
		return NodeProvisionJob{
			ID: id, NodeID: "nod_" + id, Name: "de1", Address: "new.example.com",
			SSHHost: "192.0.2.2", SSHPort: 22, HostFingerprint: "SHA256:new", Secret: []byte("encrypted"),
			CreatedBy: "adm_test", CreatedAt: now, UpdatedAt: now,
		}
	}
	if err := st.CreateNodeProvisionJob(ctx, makeJob("prv_blocked")); !errors.Is(err, ErrConflict) {
		t.Fatalf("SSH install overlapped live node %q: %v", node.Name, err)
	}
	if err := st.RetireNode(ctx, node.ID, now); err != nil {
		t.Fatalf("retire reused node: %v", err)
	}
	if err := st.CreateNodeProvisionJob(ctx, makeJob("prv_new")); err != nil {
		t.Fatalf("reuse a retired name for SSH install: %v", err)
	}
	provisionNode, err := st.CreateEnrollment(ctx,
		&NodeRow{ID: "nod_prv_new", Name: "de1", Address: "new.example.com"}, "", []byte{2}, "adm_test", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("allow the active install job to enroll its own node: %v", err)
	}
	if err := st.CreateNodeProvisionJob(ctx, makeJob("prv_duplicate")); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate active SSH name = %v, want conflict", err)
	}
	if _, ok, err := st.ClaimNodeProvisionJob(ctx, now); err != nil || !ok {
		t.Fatalf("claim the install job: ok=%v err=%v", ok, err)
	}
	access := NodeServerAccess{NodeID: provisionNode.ID, NodeName: provisionNode.Name, SSHHost: "192.0.2.2", SSHPort: 22,
		SSHUser: "root", HostFingerprint: "SHA256:new", Password: []byte("encrypted")}
	if err := st.CompleteNodeProvisionJob(ctx, "prv_new", access, now.Add(time.Minute)); err != nil {
		t.Fatalf("complete install after the node enrolled: %v", err)
	}
	if err := st.RetireNode(ctx, provisionNode.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("retire completed install: %v", err)
	}
	if err := st.CreateNodeProvisionJob(ctx, makeJob("prv_retry")); err != nil {
		t.Fatalf("reuse a finished SSH name: %v", err)
	}
}
