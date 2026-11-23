//go:build !js

package store

import (
	"context"
	"testing"
	"time"
)

// A schedule past its window is marked missed once, is no longer due, and saving it again clears the mark.
func TestMarkNodeUpdateSchedulesMissed(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	nodeID, _ := fixtureInbound(t, s, "a")
	now := time.Unix(1_000_000, 0)
	row := NodeUpdateScheduleRow{NodeID: nodeID, ToVersion: "v1", ToBuilt: 10, ScheduledAt: now.Add(-3 * time.Hour).Unix(), CreatedAt: now}
	if err := s.SetNodeUpdateSchedule(ctx, row); err != nil {
		t.Fatal(err)
	}
	if n, err := s.MarkNodeUpdateSchedulesMissed(ctx, now.Add(-4*time.Hour), now); err != nil || n != 0 {
		t.Fatalf("inside the window: %d %v", n, err)
	}
	if n, err := s.MarkNodeUpdateSchedulesMissed(ctx, now.Add(-2*time.Hour), now); err != nil || n != 1 {
		t.Fatalf("after the window: %d %v", n, err)
	}
	if n, err := s.MarkNodeUpdateSchedulesMissed(ctx, now.Add(-2*time.Hour), now.Add(time.Minute)); err != nil || n != 0 {
		t.Fatalf("marked twice: %d %v", n, err)
	}
	if due, err := s.DueNodeUpdateSchedules(ctx, now); err != nil || len(due) != 0 {
		t.Fatalf("a missed schedule is due: %+v %v", due, err)
	}
	if got, _ := s.NodeUpdateSchedules(ctx); got[nodeID].MissedAt != now.Unix() {
		t.Fatalf("missed schedule: %+v", got[nodeID])
	}
	row.ScheduledAt = now.Add(time.Hour).Unix()
	if err := s.SetNodeUpdateSchedule(ctx, row); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.NodeUpdateSchedules(ctx); got[nodeID].MissedAt != 0 {
		t.Fatalf("a schedule saved again stays missed: %+v", got[nodeID])
	}
}
