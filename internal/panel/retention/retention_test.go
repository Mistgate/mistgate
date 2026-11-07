package retention

import (
	"testing"
	"time"
)

func TestDefaultsAreWhatThePanelAlwaysDid(t *testing.T) {
	p := Default()
	want := map[string]time.Duration{
		"journal_days": 7 * 24 * time.Hour, "events_info_days": 90 * 24 * time.Hour, "events_warn_days": 400 * 24 * time.Hour,
		"audit_days": 400 * 24 * time.Hour, "alerts_days": 90 * 24 * time.Hour, "check_samples_hours": 25 * time.Hour,
		"check_daily_days": 90 * 24 * time.Hour, "host_history_days": 90 * 24 * time.Hour,
		"traffic_bucket_days": 400 * 24 * time.Hour, "node_traffic_hour_days": 400 * 24 * time.Hour,
	}
	if len(Fields) != len(want) {
		t.Fatalf("%d fields, want %d", len(Fields), len(want))
	}
	for k, d := range want {
		if got := p.Duration(k); got != d {
			t.Errorf("%s: %v, want %v", k, got, d)
		}
	}
	if len(Keys()) != len(Fields) || Keys()[0] != "retention.journal_days" {
		t.Errorf("keys: %v", Keys())
	}
	if p.Duration("nope") != 0 {
		t.Error("an unknown name has a duration")
	}
}

func TestParseKeepsOnlyValidValues(t *testing.T) {
	p := Parse(map[string]string{
		"retention.events_info_days": "30", // fine
		"retention.audit_days":       "5",  // below the minimum of 30
		"retention.journal_days":     "99", // above the maximum of 7
		"retention.alerts_days":      "ten",
		"retention.events_warn_days": "",
	})
	checks := map[string]int{"events_info_days": 30, "audit_days": 400, "journal_days": 7, "alerts_days": 90, "events_warn_days": 400}
	for k, want := range checks {
		if got, ok := p.Get(k); !ok || got != want {
			t.Errorf("%s = %d, want %d", k, got, want)
		}
	}
	if _, ok := p.Get("nope"); ok {
		t.Error("unknown name found")
	}
}
