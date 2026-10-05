package store

import (
	"context"
	"time"
)

// NodeUpdateScheduleRow is an owner-scheduled rollout of one signed agent bundle to one node.
// ScheduledAt is an absolute UTC Unix time; TimezoneOffsetMinutes preserves the wall-clock context.
type NodeUpdateScheduleRow struct {
	NodeID, ToVersion, CreatedBy string
	ToBuilt, ScheduledAt         int64
	TimezoneOffsetMinutes        int32
	CreatedAt                    time.Time
	// MissedAt is when the schedule was marked missed (Unix seconds), 0 while it may still start.
	MissedAt int64
}

// SetNodeUpdateSchedule creates or replaces the pending schedule for a node (a replaced schedule is no longer missed).
func (s *Store) SetNodeUpdateSchedule(ctx context.Context, r NodeUpdateScheduleRow) error {
	_, err := s.W.ExecContext(ctx, `
		INSERT INTO node_update_schedule (node_id, to_version, to_built, scheduled_at, timezone_offset_minutes, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET to_version = excluded.to_version, to_built = excluded.to_built,
			scheduled_at = excluded.scheduled_at, timezone_offset_minutes = excluded.timezone_offset_minutes,
			created_by = excluded.created_by, created_at = excluded.created_at, missed_at = 0`,
		r.NodeID, r.ToVersion, r.ToBuilt, r.ScheduledAt, r.TimezoneOffsetMinutes, r.CreatedBy, unix(r.CreatedAt))
	return err
}

// NodeUpdateSchedules returns all pending schedules indexed by node id, missed ones included.
func (s *Store) NodeUpdateSchedules(ctx context.Context) (map[string]NodeUpdateScheduleRow, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT node_id, to_version, to_built, scheduled_at, timezone_offset_minutes, created_by, created_at, missed_at
		FROM node_update_schedule ORDER BY scheduled_at, node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]NodeUpdateScheduleRow)
	for rows.Next() {
		var r NodeUpdateScheduleRow
		var created int64
		if err := rows.Scan(&r.NodeID, &r.ToVersion, &r.ToBuilt, &r.ScheduledAt, &r.TimezoneOffsetMinutes, &r.CreatedBy, &created, &r.MissedAt); err != nil {
			return nil, err
		}
		r.CreatedAt = fromUnix(created)
		out[r.NodeID] = r
	}
	return out, rows.Err()
}

// MarkNodeUpdateSchedulesMissed marks the schedules whose time was before cutoff and that have not started: they
// never start by themselves afterwards. It returns how many it marked.
func (s *Store) MarkNodeUpdateSchedulesMissed(ctx context.Context, cutoff, now time.Time) (int64, error) {
	res, err := s.W.ExecContext(ctx, `UPDATE node_update_schedule SET missed_at = ? WHERE missed_at = 0 AND scheduled_at < ?`, unix(now), unix(cutoff))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DueNodeUpdateSchedules returns the earliest schedules whose requested time has arrived and that were not missed.
func (s *Store) DueNodeUpdateSchedules(ctx context.Context, now time.Time) ([]NodeUpdateScheduleRow, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT node_id, to_version, to_built, scheduled_at, timezone_offset_minutes, created_by, created_at
		FROM node_update_schedule WHERE scheduled_at <= ? AND missed_at = 0 ORDER BY scheduled_at, node_id LIMIT 100`, unix(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeUpdateScheduleRow
	for rows.Next() {
		var r NodeUpdateScheduleRow
		var created int64
		if err := rows.Scan(&r.NodeID, &r.ToVersion, &r.ToBuilt, &r.ScheduledAt, &r.TimezoneOffsetMinutes, &r.CreatedBy, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = fromUnix(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteNodeUpdateSchedule removes a node's schedule and reports whether one existed.
func (s *Store) DeleteNodeUpdateSchedule(ctx context.Context, nodeID string) (bool, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM node_update_schedule WHERE node_id = ?`, nodeID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeleteNodeUpdateScheduleIfMatches removes only the schedule observed by a worker. A concurrent reschedule must survive.
func (s *Store) DeleteNodeUpdateScheduleIfMatches(ctx context.Context, r NodeUpdateScheduleRow) (bool, error) {
	res, err := s.W.ExecContext(ctx, `DELETE FROM node_update_schedule
		WHERE node_id = ? AND to_version = ? AND to_built = ? AND scheduled_at = ? AND timezone_offset_minutes = ?`,
		r.NodeID, r.ToVersion, r.ToBuilt, r.ScheduledAt, r.TimezoneOffsetMinutes)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
