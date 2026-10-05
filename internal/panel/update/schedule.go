package update

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	updateScheduleTimezoneKey = "update_schedule_timezone_offset_minutes"
	// defaultScheduleTimezoneOffsetMin is UTC. Installations made while the default was UTC+3 got that value stored
	// by migration 00040, so their schedules are entered as before.
	defaultScheduleTimezoneOffsetMin = 0
	minimumScheduleLead              = time.Minute
	maximumScheduleLead              = 365 * 24 * time.Hour
	// scheduleMissAfter: a schedule that has not started this long after its time is marked missed and never starts
	// by itself (an update meant for a quiet hour must not run whenever the node comes back).
	scheduleMissAfter = 2 * time.Hour
)

func validScheduleTimezoneOffset(offset int32) bool {
	return offset >= -12*60 && offset <= 14*60 && offset%15 == 0
}

func (s *Service) scheduleTimezoneOffset(ctx context.Context) (int32, error) {
	raw, err := s.st.Setting(ctx, updateScheduleTimezoneKey)
	if errors.Is(err, store.ErrNotFound) {
		return defaultScheduleTimezoneOffsetMin, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || !validScheduleTimezoneOffset(int32(n)) {
		return defaultScheduleTimezoneOffsetMin, nil
	}
	return int32(n), nil
}

func (s *Service) setScheduleTimezone(ctx context.Context, offset int32) error {
	if !validScheduleTimezoneOffset(offset) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("timezone offset must be between UTC-12:00 and UTC+14:00 in 15-minute steps"))
	}
	if err := s.st.SetSettings(ctx, map[string]string{updateScheduleTimezoneKey: strconv.FormatInt(int64(offset), 10)}); err != nil {
		return s.internal("save update timezone", err)
	}
	s.audit(ctx, "update_schedule_timezone", map[string]string{"offset_minutes": strconv.FormatInt(int64(offset), 10)})
	return nil
}

func timezoneName(offset int32) string {
	sign := "+"
	n := int(offset)
	if n < 0 {
		sign = "-"
		n = -n
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, n/60, n%60)
}

func parseScheduleLocalTime(local string, offset int32) (time.Time, error) {
	if !validScheduleTimezoneOffset(offset) || len(local) != len("2006-01-02T15:04") {
		return time.Time{}, errors.New("date and time must use YYYY-MM-DDTHH:mm")
	}
	zone := time.FixedZone(timezoneName(offset), int(offset)*60)
	t, err := time.ParseInLocation("2006-01-02T15:04", local, zone)
	if err != nil || t.Format("2006-01-02T15:04") != local {
		return time.Time{}, errors.New("date and time are invalid")
	}
	return t, nil
}

func (s *Service) scheduleNodeUpdate(ctx context.Context, nodeID, localDateTime string, requestedOffset int32, expectedVersion string, expectedBuilt int64) (store.NodeUpdateScheduleRow, error) {
	if s.cfg.Key == nil {
		return store.NodeUpdateScheduleRow{}, precondition("no release key in this build")
	}
	if nodeID == "" {
		return store.NodeUpdateScheduleRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("node_id is required"))
	}
	offset, err := s.scheduleTimezoneOffset(ctx)
	if err != nil {
		return store.NodeUpdateScheduleRow{}, s.internal("load update timezone", err)
	}
	if !validScheduleTimezoneOffset(requestedOffset) {
		return store.NodeUpdateScheduleRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("timezone offset is invalid"))
	}
	if requestedOffset != offset {
		return store.NodeUpdateScheduleRow{}, precondition("the schedule timezone changed; review the date and time")
	}
	scheduledAt, err := parseScheduleLocalTime(localDateTime, offset)
	if err != nil {
		return store.NodeUpdateScheduleRow{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	now := s.now()
	if scheduledAt.Before(now.Add(minimumScheduleLead)) {
		return store.NodeUpdateScheduleRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("scheduled time must be at least one minute in the future"))
	}
	if scheduledAt.After(now.Add(maximumScheduleLead)) {
		return store.NodeUpdateScheduleRow{}, connect.NewError(connect.CodeInvalidArgument, errors.New("scheduled time cannot be more than one year in the future"))
	}
	b := s.current()
	if b == nil || !b.trusted {
		return store.NodeUpdateScheduleRow{}, precondition("no trusted bundle")
	}
	if expectedVersion == "" || expectedBuilt <= 0 || expectedVersion != b.manifest.Version || expectedBuilt != b.manifest.Built {
		return store.NodeUpdateScheduleRow{}, precondition("the update bundle changed; review the new version")
	}
	if scheduledAt.Unix() > b.manifest.Expires {
		return store.NodeUpdateScheduleRow{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("the signed bundle expires at %s, before the scheduled time",
			time.Unix(b.manifest.Expires, 0).In(scheduledAt.Location()).Format("2006-01-02 15:04 MST")))
	}
	views, err := s.nodes(ctx, b, nil)
	if err != nil {
		return store.NodeUpdateScheduleRow{}, s.internal("list nodes to schedule update", err)
	}
	i := -1
	for n := range views {
		if views[n].row.ID == nodeID {
			i = n
			break
		}
	}
	if i < 0 {
		return store.NodeUpdateScheduleRow{}, notFound("node")
	}
	n := views[i]
	if n.row.AgentBuilt >= b.manifest.Built || !updatableWhenOffline(n) {
		return store.NodeUpdateScheduleRow{}, precondition("no node to update")
	}
	r := store.NodeUpdateScheduleRow{
		NodeID: nodeID, ToVersion: b.manifest.Version, ToBuilt: b.manifest.Built,
		ScheduledAt: scheduledAt.Unix(), TimezoneOffsetMinutes: offset, CreatedBy: s.cfg.Actor(ctx), CreatedAt: now,
	}
	if err := s.st.SetNodeUpdateSchedule(ctx, r); err != nil {
		return store.NodeUpdateScheduleRow{}, s.internal("save node update schedule", err)
	}
	s.audit(ctx, "node_update_schedule", map[string]string{"node_id": nodeID, "version": r.ToVersion, "built": strconv.FormatInt(r.ToBuilt, 10), "scheduled_unix": strconv.FormatInt(r.ScheduledAt, 10)})
	s.kick()
	return r, nil
}

func updatableWhenOffline(n nodeView) bool {
	return n.row.State == "active" && slices.Contains(n.row.AgentCaps, capUpdate)
}

func (s *Service) processScheduledNodeUpdates(ctx context.Context) {
	if s.cfg.Key == nil {
		return
	}
	if n, err := s.st.MarkNodeUpdateSchedulesMissed(ctx, s.now().Add(-scheduleMissAfter), s.now()); err != nil {
		s.log.Warn("update: mark missed node update schedules", "err", err)
	} else if n > 0 {
		s.log.Info("update: node update schedules missed their window", "count", n)
	}
	s.mu.Lock()
	blocked := s.bundleSyncing || s.panelBusy()
	s.mu.Unlock()
	if blocked {
		return
	}
	if _, err := s.st.ActiveRollout(ctx); err == nil {
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("update: check active rollout before scheduled update", "err", err)
		return
	}
	b := s.current()
	if b == nil || !b.trusted {
		return
	}
	schedules, err := s.st.DueNodeUpdateSchedules(ctx, s.now())
	if err != nil {
		s.log.Warn("update: load due node updates", "err", err)
		return
	}
	for _, schedule := range schedules {
		// Keep a schedule visible if its signed release has been replaced. Never silently update to a different build.
		if schedule.ToBuilt != b.manifest.Built || schedule.ToVersion != b.manifest.Version {
			continue
		}
		views, err := s.nodes(ctx, b, nil)
		if err != nil {
			s.log.Warn("update: list nodes for scheduled update", "err", err)
			return
		}
		var node *nodeView
		for i := range views {
			if views[i].row.ID == schedule.NodeID {
				node = &views[i]
				break
			}
		}
		if node == nil || node.row.AgentBuilt >= schedule.ToBuilt {
			if _, err := s.st.DeleteNodeUpdateScheduleIfMatches(ctx, schedule); err != nil {
				s.log.Warn("update: remove obsolete node update schedule", "node", schedule.NodeID, "err", err)
			}
			continue
		}
		if !node.connected || !updatable(node.state) {
			continue
		}
		actor := schedule.CreatedBy
		if actor == "" {
			actor = "system:schedule"
		}
		ro, err := s.startWithActorAndSchedule(ctx, []string{schedule.NodeID}, 0, actor, nil, &schedule)
		if err != nil {
			if connect.CodeOf(err) == connect.CodeFailedPrecondition || ctx.Err() != nil {
				return
			}
			s.log.Warn("update: start scheduled node update", "node", schedule.NodeID, "err", err)
			continue
		}
		s.log.Info("update: scheduled node update started", "rollout", ro.ID, "node", schedule.NodeID, "version", ro.ToVersion)
		return // one active rollout at a time
	}
}
