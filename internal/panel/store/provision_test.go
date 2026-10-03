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

	if err := st.UpdateNodeProvisionJobWithEvent(ctx, job.ID, "completed", "completed", "", []byte{}, "agent_connected", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.NodeProvisionJob(ctx, job.ID)
	if err != nil || loaded.State != "completed" || len(loaded.Secret) != 0 {
		t.Fatalf("terminal job still has a secret: %+v, err %v", loaded, err)
	}
	terminalEvents, _, err := st.NodeProvisionEvents(ctx, job.ID, next, 10)
	if err != nil || len(terminalEvents) != 1 || terminalEvents[0].Code != "agent_connected" {
		t.Fatalf("terminal event = %+v, err %v", terminalEvents, err)
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
	if err := st.SetPendingNodeServerPassword(ctx, job.NodeID, []byte("encrypted-new"), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.NodeServerAccess(ctx, job.NodeID)
	if err != nil || string(loaded.Password) != "encrypted-old" || string(loaded.PendingPassword) != "encrypted-new" {
		t.Fatalf("pending access = %+v, err %v", loaded, err)
	}
	if err := st.CommitPendingNodeServerPassword(ctx, job.NodeID, []byte("encrypted-new-current"), now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	loaded, err = st.NodeServerAccess(ctx, job.NodeID)
	if err != nil || string(loaded.Password) != "encrypted-new-current" || loaded.PendingPassword != nil {
		t.Fatalf("committed access = %+v, err %v", loaded, err)
	}
	if err := st.RetireNode(ctx, job.NodeID, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.NodeServerAccess(ctx, job.NodeID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retired node retained SSH access: %v", err)
	}
}
