package access

import "time"

// User statuses as stored in user.status.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusExpired  = "expired"
	StatusLimited  = "limited"
)

// Quota reset periods as stored in user.quota_reset.
const (
	ResetNone          = "none"
	ResetDay           = "day"
	ResetWeek          = "week"
	ResetMonth         = "month"
	ResetRollingMonth  = "rolling_month"
	rollingMonthLength = 30 * 24 * time.Hour
)

// ComputeStatus applies the precedence disabled > expired > limited > active. A zero expiresAt means the
// term never ends, quota 0 means unlimited; usage at or above the quota limits the user.
func ComputeStatus(disabled bool, expiresAt time.Time, quota, used uint64, now time.Time) string {
	switch {
	case disabled:
		return StatusDisabled
	case !expiresAt.IsZero() && !now.Before(expiresAt):
		return StatusExpired
	case quota > 0 && used >= quota:
		return StatusLimited
	}
	return StatusActive
}

func validReset(r string) bool {
	switch r {
	case ResetNone, ResetDay, ResetWeek, ResetMonth, ResetRollingMonth:
		return true
	}
	return false
}

// PeriodStart is the start of the quota period that contains t: 00:00 UTC of the day, of the Monday, of the
// 1st; for none and rolling_month periods have no calendar anchor, so it is t itself.
func PeriodStart(reset string, t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch reset {
	case ResetDay:
		return day
	case ResetWeek:
		return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7)) // Monday
	case ResetMonth:
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
	}
	return t
}

// NextReset is when the period that started at periodStart ends; zero when it never does.
func NextReset(reset string, periodStart time.Time) time.Time {
	p := periodStart.UTC()
	switch reset {
	case ResetDay:
		return p.AddDate(0, 0, 1)
	case ResetWeek:
		return p.AddDate(0, 0, 7)
	case ResetMonth:
		return time.Date(p.Year(), p.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	case ResetRollingMonth:
		return p.Add(rollingMonthLength)
	}
	return time.Time{}
}

// AdvancePeriod returns the period start valid at now (periodStart itself while the period is running)
// and whether a reset happened. Rolling periods move in whole 30-day steps so they keep their anchor.
func AdvancePeriod(reset string, periodStart, now time.Time) (time.Time, bool) {
	next := NextReset(reset, periodStart)
	if next.IsZero() || now.Before(next) {
		return periodStart, false
	}
	if reset == ResetRollingMonth {
		k := now.Sub(periodStart) / rollingMonthLength
		return periodStart.Add(k * rollingMonthLength), true
	}
	return PeriodStart(reset, now), true
}
