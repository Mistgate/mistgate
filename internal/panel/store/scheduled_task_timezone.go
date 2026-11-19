package store

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// ScheduledTaskSchedule is the part of an enabled task needed to recalculate its next run.
type ScheduledTaskSchedule struct {
	ID           string
	Kind         string
	EveryMinutes int
	MinuteOfDay  int
	Weekday      int
}

// ScheduledTaskNext is the next run time to write when the panel's schedule offset changes.
type ScheduledTaskNext struct {
	ID        string
	NextRunAt time.Time
}

// SettingPanelTimezone is the panel's time zone: minutes east of UTC, the one of the update schedules and of the scheduled
// tasks. Migration 00041 writes it too. Read it through PanelTimezone.
const SettingPanelTimezone = "update_schedule_timezone_offset_minutes"

// ValidTimezoneOffset reports whether minutes is a zone offset the panel accepts: UTC-12:00 to UTC+14:00 in 15-minute steps.
func ValidTimezoneOffset(minutes int) bool {
	return minutes >= -12*60 && minutes <= 14*60 && minutes%15 == 0
}

// PanelTimezone is the panel's offset east of UTC in minutes; a missing or unusable value is UTC. Only a failed read is an error.
func (s *Store) PanelTimezone(ctx context.Context) (int, error) {
	raw, err := s.Setting(ctx, SettingPanelTimezone)
	if errors.Is(err, ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(raw)
	if err != nil || !ValidTimezoneOffset(n) {
		return 0, nil
	}
	return n, nil
}

// SetPanelTimezone changes the panel's time zone and enabled tasks' next runs in one batch.
func (s *Store) SetPanelTimezone(ctx context.Context, minutes int, nexts []ScheduledTaskNext) error {
	stmts := make([]Stmt, 0, len(nexts)+1)
	stmts = append(stmts, Stmt{Query: `INSERT INTO setting (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, Args: []any{SettingPanelTimezone, strconv.Itoa(minutes)}})
	for _, next := range nexts {
		at := int64(0)
		if !next.NextRunAt.IsZero() {
			at = next.NextRunAt.Unix()
		}
		stmts = append(stmts, Stmt{Query: `UPDATE scheduled_task SET next_run_at = ? WHERE id = ? AND enabled = 1`, Args: []any{at, next.ID}})
	}
	_, err := s.batch(ctx, stmts...)
	return err
}
