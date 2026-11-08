package store

import (
	"context"
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

// SetSettingAndScheduledTaskNexts changes a setting and enabled tasks' next runs in one batch.
func (s *Store) SetSettingAndScheduledTaskNexts(ctx context.Context, key, value string, nexts []ScheduledTaskNext) error {
	stmts := make([]Stmt, 0, len(nexts)+1)
	stmts = append(stmts, Stmt{Query: `INSERT INTO setting (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, Args: []any{key, value}})
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
