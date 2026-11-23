package retention

import (
	"testing"
	"time"
)

func TestDefaultsAreWhatThePanelAlwaysDid(t *testing.T) {
	p := Default()
	want := map[Key]time.Duration{
		JournalDays: 7 * 24 * time.Hour, EventsInfoDays: 90 * 24 * time.Hour, EventsWarnDays: 400 * 24 * time.Hour,
		AuditDays: 400 * 24 * time.Hour, AlertsDays: 90 * 24 * time.Hour, CheckSamplesHours: 25 * time.Hour,
		CheckDailyDays: 90 * 24 * time.Hour, HostHistoryDays: 90 * 24 * time.Hour,
		TrafficBucketDays: 400 * 24 * time.Hour, NodeTrafficHourDays: 400 * 24 * time.Hour,
		NetworksDays: 7 * 24 * time.Hour,
	}
	if len(Fields) != len(want) {
		t.Fatalf("%d fields, want %d", len(Fields), len(want))
	}
	for k, d := range want {
		if got := p.Duration(k); got != d {
			t.Errorf("%s: %v, want %v", Fields[k].Key, got, d)
		}
	}
	if len(Keys()) != len(Fields) || Keys()[0] != "retention.journal_days" {
		t.Errorf("keys: %v", Keys())
	}
}

// Every Key lands on its own field: an unset slot of Fields would have an empty name and a zero window.
func TestEveryKeyHasAWindow(t *testing.T) {
	seen := map[string]bool{}
	for k, f := range Fields {
		if f.Key == "" || f.Unit == 0 || f.Min < 1 || seen[f.Key] {
			t.Errorf("key %d: unset or duplicate field %+v", k, f)
		}
		seen[f.Key] = true
		if Default().Duration(Key(k)) <= 0 {
			t.Errorf("%s: no window by default", f.Key)
		}
	}
}

func TestParseKeepsOnlyValidValues(t *testing.T) {
	p := Parse(map[string]string{
		"retention.events_info_days": "30", // fine
		"retention.audit_days":       "5",  // below the minimum of 30
		"retention.journal_days":     "99", // above the maximum of 7
		"retention.alerts_days":      "ten",
		"retention.events_warn_days": "",
		"retention.networks_days":    "8", // above the maximum of seven
	})
	checks := map[string]int{"events_info_days": 30, "audit_days": 400, "journal_days": 7, "alerts_days": 90, "events_warn_days": 400, "networks_days": 7}
	for k, want := range checks {
		if got, ok := p.Get(k); !ok || got != want {
			t.Errorf("%s = %d, want %d", k, got, want)
		}
	}
	if _, ok := p.Get("nope"); ok {
		t.Error("unknown name found")
	}
}
