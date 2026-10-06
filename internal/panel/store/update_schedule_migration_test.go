//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 00041: the update time zone default became UTC. An existing installation (it has an admin) keeps entering schedules
// in UTC+3, the old default; a new one starts at UTC; a stored choice is never changed.
func TestUpdateTimezoneMigrationKeepsTheOldDefaultOfExistingInstallations(t *testing.T) {
	ctx := context.Background()
	const key = "update_schedule_timezone_offset_minutes"
	for name, tc := range map[string]struct {
		admin        bool
		stored, want string
	}{
		"a new installation":            {},
		"an existing installation":      {admin: true, want: "180"},
		"an existing one with a choice": {admin: true, stored: "60", want: "60"},
	} {
		t.Run(name, func(t *testing.T) {
			s, p := openProvider(t)
			if _, err := p.UpTo(ctx, 40); err != nil {
				t.Fatal(err)
			}
			if tc.admin {
				execT(t, s, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_1', 'Alice', 'owner', x'01', 1)`)
			}
			if tc.stored != "" {
				execT(t, s, `INSERT INTO setting (k, v) VALUES (?, ?)`, key, tc.stored)
			}
			if _, err := p.UpTo(ctx, 41); err != nil {
				t.Fatal(err)
			}
			got, err := s.Setting(ctx, key)
			if tc.want == "" {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("a new installation got %q, %v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("offset = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

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
