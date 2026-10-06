//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNodeProvisionJobRecoversAndKeepsEventsRedacted(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	job := NodeProvisionJob{
		ID: "prv_test", NodeID: "nod_test", Name: "edge-1", Address: "edge.example.com",
		CountryCode: "EE", SSHHost: "198.51.100.22", SSHPort: 2222, HostFingerprint: "SHA256:pin",
		Secret: []byte("sealed-credential"), CreatedBy: "adm_test", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	duplicate := job
	duplicate.ID, duplicate.NodeID = "prv_duplicate", "nod_duplicate"
	if err := st.CreateNodeProvisionJob(ctx, duplicate); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate node name error = %v", err)
	}
	loaded, err := st.NodeProvisionJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != "queued" || string(loaded.Secret) != string(job.Secret) || loaded.SSHPort != job.SSHPort {
		t.Fatalf("persisted job = %+v", loaded)
	}

	claimed, ok, err := st.ClaimNodeProvisionJob(ctx, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim job = %v, %v", ok, err)
	}
	if claimed.State != "running" || claimed.Phase != "connecting" {
		t.Fatalf("claimed job = %+v", claimed)
	}
	if err := st.RequeueNodeProvisionJobs(ctx, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.NodeProvisionJob(ctx, job.ID)
	if err != nil || loaded.State != "queued" || loaded.Phase != "queued" {
		t.Fatalf("recovered job = %+v, err %v", loaded, err)
	}

	events, next, err := st.NodeProvisionEvents(ctx, job.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[2].Code != "resumed_after_restart" || next != events[2].ID {
		t.Fatalf("recovery events = %+v, cursor %d", events, next)
	}
	if events[0].Phase != "queued" || events[0].Code != "created" || events[1].Code != "started" {
		t.Fatalf("initial events = %+v", events[:2])
	}

	if _, ok, err := st.ClaimNodeProvisionJob(ctx, now.Add(3*time.Minute)); err != nil || !ok {
		t.Fatalf("claim recovered job = %v, %v", ok, err)
	}
	if err := st.UpdateRunningNodeProvisionJobWithEvent(ctx, job.ID, "completed", "completed", "", []byte{}, "agent_connected", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.NodeProvisionJob(ctx, job.ID)
	if err != nil || loaded.State != "completed" || len(loaded.Secret) != 0 {
		t.Fatalf("terminal job still has a secret: %+v, err %v", loaded, err)
	}
	terminalEvents, _, err := st.NodeProvisionEvents(ctx, job.ID, next, 10)
	if err != nil || len(terminalEvents) != 2 || terminalEvents[1].Code != "agent_connected" {
		t.Fatalf("terminal event = %+v, err %v", terminalEvents, err)
	}
}

// A retry changes the job only while it is still in the state the caller read: a double submit must not requeue a
// job the worker already claimed (the watcher would then cancel the live SSH command).
func TestRetryNodeProvisionJobOnlyFromTheStateRead(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	job := NodeProvisionJob{ID: "prv_retry", NodeID: "nod_retry", Name: "edge-retry", Address: "edge.example.com",
		SSHHost: "203.0.113.8", SSHPort: 22, HostFingerprint: "SHA256:pin", Secret: []byte("sealed"),
		CreatedBy: "adm_test", CreatedAt: now, UpdatedAt: now}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ClaimNodeProvisionJob(ctx, now); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if err := st.UpdateRunningNodeProvisionJobWithEvent(ctx, job.ID, "failed", "failed", "ssh_authentication_failed", []byte{}, "ssh_authentication_failed", now); err != nil {
		t.Fatal(err)
	}
	// two submits read "failed"; the first requeues, the worker claims it, the second must not touch it
	if err := st.RetryNodeProvisionJob(ctx, job.ID, "failed", []byte("sealed-1"), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ClaimNodeProvisionJob(ctx, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("claim the retry: %v %v", ok, err)
	}
	if err := st.RetryNodeProvisionJob(ctx, job.ID, "failed", []byte("sealed-2"), now.Add(time.Minute)); !errors.Is(err, ErrConflict) {
		t.Fatalf("second submit = %v, want ErrConflict", err)
	}
	loaded, err := st.NodeProvisionJob(ctx, job.ID)
	if err != nil || loaded.State != "running" || string(loaded.Secret) != "sealed-1" {
		t.Fatalf("running job was changed by a stale retry: %+v, err %v", loaded, err)
	}

	// a cancelled job can be retried, unless its name went to a live node meanwhile
	if _, _, err := st.RequestCancelNodeProvisionJob(ctx, job.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishCancelledNodeProvisionJob(ctx, job.ID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_other', 'edge-retry', 'x.example.com', 'pending', ?)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := st.RetryNodeProvisionJob(ctx, job.ID, "cancelled", []byte("sealed-3"), now.Add(3*time.Minute)); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("retry onto a taken name = %v, want ErrNameTaken", err)
	}
	if _, err := st.W.ExecContext(ctx, `DELETE FROM node WHERE id = 'nod_other'`); err != nil {
		t.Fatal(err)
	}
	if err := st.RetryNodeProvisionJob(ctx, job.ID, "cancelled", []byte("sealed-3"), now.Add(3*time.Minute)); err != nil {
		t.Fatalf("retry of a cancelled job = %v", err)
	}
	if loaded, _ := st.NodeProvisionJob(ctx, job.ID); loaded.State != "queued" {
		t.Fatalf("retried cancelled job = %+v", loaded)
	}
}

func TestCancelQueuedProvisionClearsCredentialsAndCannotBeClaimed(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	job := NodeProvisionJob{
		ID: "prv_cancel_queued", NodeID: "nod_cancel_queued", Name: "cancel-queued", Address: "edge.example.com",
		SSHHost: "198.51.100.9", SSHPort: 22, HostFingerprint: "SHA256:pin",
		Secret: []byte("sealed-password"), CreatedBy: "adm_test", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	state, changed, err := st.RequestCancelNodeProvisionJob(ctx, job.ID, now.Add(time.Minute))
	if err != nil || state != "cancelled" || !changed {
		t.Fatalf("cancel queued = %q, %v, %v", state, changed, err)
	}
	loaded, err := st.NodeProvisionJob(ctx, job.ID)
	if err != nil || loaded.State != "cancelled" || loaded.ErrorCode != "cancelled_before_start" || len(loaded.Secret) != 0 {
		t.Fatalf("cancelled queued job = %+v, err %v", loaded, err)
	}
	if _, ok, err := st.ClaimNodeProvisionJob(ctx, now.Add(2*time.Minute)); err != nil || ok {
		t.Fatalf("cancelled job was claimed: ok=%v err=%v", ok, err)
	}
	if _, changed, err := st.RequestCancelNodeProvisionJob(ctx, job.ID, now.Add(3*time.Minute)); err != nil || changed {
		t.Fatalf("repeated cancel = changed %v, err %v", changed, err)
	}
	events, _, err := st.NodeProvisionEvents(ctx, job.ID, 0, 10)
	if err != nil || len(events) != 2 || events[1].Code != "cancelled_before_start" {
		t.Fatalf("cancellation journal = %+v, err %v", events, err)
	}
}

func TestCancelRunningProvisionIsTerminalAndSurvivesRestart(t *testing.T) {
	for _, finishByRestart := range []bool{false, true} {
		name := "worker finalizes"
		if finishByRestart {
			name = "restart finalizes"
		}
		t.Run(name, func(t *testing.T) {
			st := openTemp(t)
			ctx := context.Background()
			now := time.Unix(1_800_000_000, 0).UTC()
			job := NodeProvisionJob{
				ID: "prv_cancel_running", NodeID: "nod_cancel_running", Name: "cancel-running", Address: "edge.example.com",
				SSHHost: "198.51.100.10", SSHPort: 22, HostFingerprint: "SHA256:pin",
				Secret: []byte("sealed-password"), CreatedBy: "adm_test", CreatedAt: now, UpdatedAt: now,
			}
			if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := st.ClaimNodeProvisionJob(ctx, now); err != nil || !ok {
				t.Fatalf("claim running job: ok=%v err=%v", ok, err)
			}
			state, changed, err := st.RequestCancelNodeProvisionJob(ctx, job.ID, now.Add(time.Minute))
			if err != nil || state != "cancel_requested" || !changed {
				t.Fatalf("request cancellation = %q, %v, %v", state, changed, err)
			}
			if err := st.UpdateRunningNodeProvisionJob(ctx, job.ID, "running", "transfer", "", job.Secret, now.Add(2*time.Minute)); !errors.Is(err, ErrConflict) {
				t.Fatalf("active worker reverted cancellation: %v", err)
			}
			if finishByRestart {
				if err := st.RequeueNodeProvisionJobs(ctx, now.Add(3*time.Minute)); err != nil {
					t.Fatal(err)
				}
			} else if changed, err := st.FinishCancelledNodeProvisionJob(ctx, job.ID, now.Add(3*time.Minute)); err != nil || !changed {
				t.Fatalf("worker finalizes cancellation: changed=%v err=%v", changed, err)
			}
			loaded, err := st.NodeProvisionJob(ctx, job.ID)
			if err != nil || loaded.State != "cancelled" || loaded.ErrorCode != "remote_outcome_unknown" || len(loaded.Secret) != 0 {
				t.Fatalf("terminal cancelled job = %+v, err %v", loaded, err)
			}
			events, _, err := st.NodeProvisionEvents(ctx, job.ID, 0, 10)
			if err != nil || len(events) != 4 || events[3].Code != "remote_outcome_unknown" {
				t.Fatalf("cancellation journal = %+v, err %v", events, err)
			}
			if err := st.CompleteNodeProvisionJob(ctx, job.ID, NodeServerAccess{NodeID: job.NodeID}, now.Add(4*time.Minute)); !errors.Is(err, ErrNotFound) {
				t.Fatalf("cancelled job was completed: %v", err)
			}
		})
	}
}

func TestCompleteProvisionRetainsAccessAndPasswordRotationIsRecoverable(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	job := NodeProvisionJob{ID: "prv_access", NodeID: "nod_access", Name: "edge-access", Address: "edge.example.com",
		SSHHost: "203.0.113.7", SSHPort: 22, HostFingerprint: "SHA256:pin", Secret: []byte("temporary"),
		CreatedBy: "adm_test", CreatedAt: now, UpdatedAt: now}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'pending', ?)`,
		job.NodeID, job.Name, job.Address, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.ClaimNodeProvisionJob(ctx, now); err != nil || !ok {
		t.Fatalf("claim job: ok=%v err=%v", ok, err)
	}
	access := NodeServerAccess{NodeID: job.NodeID, NodeName: job.Name, SSHHost: job.SSHHost, SSHPort: job.SSHPort,
		SSHUser: "root", HostFingerprint: job.HostFingerprint, Password: []byte("encrypted-old")}
	if err := st.CompleteNodeProvisionJob(ctx, job.ID, access, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	loadedJob, err := st.NodeProvisionJob(ctx, job.ID)
	if err != nil || loadedJob.State != "completed" || len(loadedJob.Secret) != 0 {
		t.Fatalf("completed job = %+v, err %v", loadedJob, err)
	}
	loaded, err := st.NodeServerAccess(ctx, job.NodeID)
	if err != nil || string(loaded.Password) != "encrypted-old" || loaded.PendingPassword != nil {
		t.Fatalf("saved access = %+v, err %v", loaded, err)
	}
	if loaded.PasswordGenerated || loaded.NodeRetired || loaded.NodeName != job.Name {
		t.Fatalf("fresh access flags = %+v", loaded)
	}
	// a generated rotation marks the access at once: the server may use the new password before the commit
	if err := st.SetPendingNodeServerPassword(ctx, job.NodeID, []byte("encrypted-new"), true, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.NodeServerAccess(ctx, job.NodeID)
	if err != nil || string(loaded.Password) != "encrypted-old" || string(loaded.PendingPassword) != "encrypted-new" || !loaded.PasswordGenerated {
		t.Fatalf("pending access = %+v, err %v", loaded, err)
	}
	if err := st.CommitPendingNodeServerPassword(ctx, job.NodeID, []byte("encrypted-new-current"), false, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.NodeServerAccess(ctx, job.NodeID)
	if err != nil || string(loaded.Password) != "encrypted-new-current" || loaded.PendingPassword != nil || !loaded.PasswordGenerated {
		t.Fatalf("committed access = %+v, err %v", loaded, err)
	}

	// the name comes from the node, so a rename shows at once (the stored copy is never updated)
	if _, err := st.W.ExecContext(ctx, `UPDATE node SET name = 'edge-renamed' WHERE id = ?`, job.NodeID); err != nil {
		t.Fatal(err)
	}
	if loaded, _ = st.NodeServerAccess(ctx, job.NodeID); loaded.NodeName != "edge-renamed" {
		t.Fatalf("access after rename = %q", loaded.NodeName)
	}
	if err := st.ForgetNodeServerAccess(ctx, job.NodeID); !errors.Is(err, ErrConflict) {
		t.Fatalf("forgetting a live node's access = %v, want ErrConflict", err)
	}

	// retiring keeps the access, sealed password included: it may be the only copy
	if err := st.RetireNode(ctx, job.NodeID, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.NodeServerAccess(ctx, job.NodeID)
	if err != nil || string(loaded.Password) != "encrypted-new-current" || !loaded.NodeRetired {
		t.Fatalf("retired node's access = %+v, err %v", loaded, err)
	}
	if err := st.ForgetNodeServerAccess(ctx, job.NodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.NodeServerAccess(ctx, job.NodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgotten access is still there: %v", err)
	}
	if err := st.ForgetNodeServerAccess(ctx, job.NodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgetting twice = %v, want ErrNotFound", err)
	}
}
