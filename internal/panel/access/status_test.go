package access

import (
	"testing"
	"time"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestComputeStatusPrecedence(t *testing.T) {
	now := ts("2026-09-30T12:00:00Z")
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	cases := []struct {
		name     string
		disabled bool
		expires  time.Time
		quota    uint64
		used     uint64
		want     string
	}{
		{"plain", false, time.Time{}, 0, 999, StatusActive},
		{"unlimited quota ignores usage", false, time.Time{}, 0, 1 << 40, StatusActive},
		{"under quota", false, future, 100, 99, StatusActive},
		{"at quota is limited", false, time.Time{}, 100, 100, StatusLimited},
		{"over quota", false, future, 100, 500, StatusLimited},
		{"expired", false, past, 0, 0, StatusExpired},
		{"expires exactly now", false, now, 0, 0, StatusExpired},
		{"expired beats limited", false, past, 100, 500, StatusExpired},
		{"disabled beats expired", true, past, 0, 0, StatusDisabled},
		{"disabled beats limited", true, time.Time{}, 100, 500, StatusDisabled},
		{"disabled beats everything", true, past, 100, 500, StatusDisabled},
	}
	for _, c := range cases {
		if got := ComputeStatus(c.disabled, c.expires, c.quota, c.used, now); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestPeriods(t *testing.T) {
	wed := ts("2026-09-30T12:34:56Z") // a Wednesday
	if got := PeriodStart(ResetDay, wed); !got.Equal(ts("2026-09-30T00:00:00Z")) {
		t.Errorf("day start %v", got)
	}
	if got := PeriodStart(ResetWeek, wed); !got.Equal(ts("2026-09-28T00:00:00Z")) {
		t.Errorf("week start %v", got) // Monday
	}
	if got := PeriodStart(ResetWeek, ts("2026-09-27T10:00:00Z")); !got.Equal(ts("2026-09-21T00:00:00Z")) {
		t.Errorf("week start of a Sunday %v", got)
	}
	if got := PeriodStart(ResetMonth, wed); !got.Equal(ts("2026-09-01T00:00:00Z")) {
		t.Errorf("month start %v", got)
	}
	if got := PeriodStart(ResetRollingMonth, wed); !got.Equal(wed) {
		t.Errorf("rolling start %v", got)
	}

	if got := NextReset(ResetMonth, ts("2026-12-01T00:00:00Z")); !got.Equal(ts("2027-01-01T00:00:00Z")) {
		t.Errorf("december rolls over: %v", got)
	}
	if got := NextReset(ResetDay, ts("2026-09-30T00:00:00Z")); !got.Equal(ts("2026-10-01T00:00:00Z")) {
		t.Errorf("next day %v", got)
	}
	if got := NextReset(ResetWeek, ts("2026-09-28T00:00:00Z")); !got.Equal(ts("2026-10-05T00:00:00Z")) {
		t.Errorf("next week %v", got)
	}
	if got := NextReset(ResetRollingMonth, ts("2026-09-01T06:00:00Z")); !got.Equal(ts("2026-10-01T06:00:00Z")) {
		t.Errorf("rolling next %v", got)
	}
	if !NextReset(ResetNone, wed).IsZero() {
		t.Error("none must never reset")
	}

	// AdvancePeriod: running period stays, ended period moves, rolling keeps its anchor.
	start := ts("2026-09-01T00:00:00Z")
	if got, reset := AdvancePeriod(ResetMonth, start, ts("2026-09-30T23:59:59Z")); reset || !got.Equal(start) {
		t.Errorf("running month: %v %v", got, reset)
	}
	if got, reset := AdvancePeriod(ResetMonth, start, ts("2026-11-03T00:00:00Z")); !reset || !got.Equal(ts("2026-11-01T00:00:00Z")) {
		t.Errorf("month skipped: %v %v", got, reset)
	}
	roll := ts("2026-09-01T06:00:00Z")
	if got, reset := AdvancePeriod(ResetRollingMonth, roll, ts("2026-11-15T00:00:00Z")); !reset || !got.Equal(roll.Add(60*24*time.Hour)) {
		t.Errorf("rolling after 75 days: %v %v", got, reset)
	}
	if _, reset := AdvancePeriod(ResetNone, start, ts("2030-01-01T00:00:00Z")); reset {
		t.Error("none must not reset")
	}
}
