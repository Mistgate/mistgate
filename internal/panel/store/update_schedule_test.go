//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNodeUpdateScheduleStorageAndRolloutConsumption(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "scheduled")
	now := time.Unix(1_800_000_000, 0)
	schedule := NodeUpdateScheduleRow{
		NodeID: nodeID, ToVersion: "v1.2.3", ToBuilt: 1_800_000_200, ScheduledAt: now.Add(time.Hour).Unix(),
		TimezoneOffsetMinutes: 180, CreatedBy: "adm_owner", CreatedAt: now,
	}
	if err := s.SetNodeUpdateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	got, err := s.NodeUpdateSchedules(ctx)
	if err != nil || len(got) != 1 || got[nodeID].ToVersion != schedule.ToVersion || got[nodeID].ScheduledAt != schedule.ScheduledAt || got[nodeID].TimezoneOffsetMinutes != 180 {
		t.Fatalf("saved schedule: %+v, %v", got, err)
	}
	if due, err := s.DueNodeUpdateSchedules(ctx, now); err != nil || len(due) != 0 {
		t.Fatalf("future schedule due too early: %+v, %v", due, err)
	}
	if due, err := s.DueNodeUpdateSchedules(ctx, now.Add(time.Hour)); err != nil || len(due) != 1 || due[0].NodeID != nodeID {
		t.Fatalf("due schedule: %+v, %v", due, err)
	}

	rollout := RolloutRow{ID: "rol_schedule", Status: RolloutRunning, ToVersion: schedule.ToVersion, ToBuilt: schedule.ToBuilt,
		Manifest: []byte("manifest"), Signature: []byte("signature"), BatchSize: 1, CreatedBy: "adm_owner", CreatedAt: now}
	step := StepRow{NodeID: nodeID, NodeName: "scheduled", State: StepPending}
	if err := s.CreateScheduledRollout(ctx, rollout, []StepRow{step}, schedule); err != nil {
		t.Fatal(err)
	}
	got, err = s.NodeUpdateSchedules(ctx)
	if err != nil || len(got) != 0 {
		t.Fatalf("started schedule was not consumed: %+v, %v", got, err)
	}
	if active, err := s.ActiveRollout(ctx); err != nil || active.ID != rollout.ID {
		t.Fatalf("scheduled rollout: %+v, %v", active, err)
	}

	if ok, err := s.SetRolloutStatus(ctx, rollout.ID, []string{RolloutRunning}, RolloutDone, "", nil, now.Add(time.Minute)); err != nil || !ok {
		t.Fatalf("finish scheduled rollout: %v %v", ok, err)
	}
	if err := s.SetNodeUpdateSchedule(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	stale := schedule
	stale.ScheduledAt--
	second := rollout
	second.ID, second.CreatedAt = "rol_stale_schedule", now.Add(2*time.Minute)
	if err := s.CreateScheduledRollout(ctx, second, []StepRow{step}, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale schedule started a rollout: %v", err)
	}
	if _, err := s.Rollout(ctx, second.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial rollout survived failed schedule consumption: %v", err)
	}
	got, err = s.NodeUpdateSchedules(ctx)
	if err != nil || len(got) != 1 || got[nodeID].ScheduledAt != schedule.ScheduledAt {
		t.Fatalf("concurrent schedule was lost: %+v, %v", got, err)
	}
}

func TestDeleteNodeUpdateScheduleIfMatchesPreservesReschedule(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "rescheduled")
	first := NodeUpdateScheduleRow{NodeID: nodeID, ToVersion: "v1", ToBuilt: 10, ScheduledAt: 20, TimezoneOffsetMinutes: 180, CreatedAt: time.Unix(1, 0)}
	if err := s.SetNodeUpdateSchedule(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ScheduledAt = 30
	second.ToVersion = "v2"
	if err := s.SetNodeUpdateSchedule(ctx, second); err != nil {
		t.Fatal(err)
	}
	if deleted, err := s.DeleteNodeUpdateScheduleIfMatches(ctx, first); err != nil || deleted {
		t.Fatalf("old worker deleted a reschedule: %v %v", deleted, err)
	}
	got, err := s.NodeUpdateSchedules(ctx)
	if err != nil || len(got) != 1 || got[nodeID].ToVersion != "v2" {
		t.Fatalf("new schedule did not survive: %+v, %v", got, err)
	}
}
