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
