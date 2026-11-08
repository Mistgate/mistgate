package store

import "context"

// EnabledScheduledTaskSchedules returns no schedules in the edge edition, which does not run scheduled tasks.
func (*Store) EnabledScheduledTaskSchedules(context.Context) ([]ScheduledTaskSchedule, error) {
	return nil, nil
}
