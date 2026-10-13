package health

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func addDeviceLimitEvent(e *env, userID string, at time.Time, used, limit int, refused bool) {
	e.t.Helper()
	flag := "0"
	if refused {
		flag = "1"
	}
	e.exec(`INSERT INTO event (ts, severity, code, source, user_id, params_json) VALUES (?, 1, 'device_limit_reached', 'panel', ?, ?)`,
		at.Unix(), userID, fmt.Sprintf(`{"used":"%d","limit":"%d","refused":%q}`, used, limit, flag))
}

// The alert is about refusals only: the sightings TRACK writes stay history, so an upgrade does not alert everyone who is
// already over their limit. It opens on the first refusal, stays one alert while refusals go on (one Telegram message), closes
// after a day without one, and is not part of the badge.
func TestDeviceLimitAlertOpensOnRefusalsAndClosesAfterAQuietDay(t *testing.T) {
	var seen []Transition
	e := newEnv(t, func(c *Config) { c.OnTransition = func(tr Transition) { seen = append(seen, tr) } })
	alice := addHealthUser(e, "alice", "active", time.Time{}, time.Time{}, time.Time{})
	bob := addHealthUser(e, "bob", "active", time.Time{}, time.Time{}, time.Time{})
	disabled := addHealthUser(e, "carol", "disabled", time.Time{}, time.Time{}, time.Time{})
	now := e.clock.Now()

	addDeviceLimitEvent(e, bob, now.Add(-time.Minute), 7, 5, false)     // TRACK: a sighting
	addDeviceLimitEvent(e, disabled, now.Add(-time.Minute), 5, 5, true) // not an active user
	addDeviceLimitEvent(e, "usr_gone", now.Add(-time.Minute), 5, 5, true)
	addDeviceLimitEvent(e, alice, now.Add(-26*time.Hour), 5, 5, true) // older than a day
	e.evaluate()
	if len(e.active()) != 0 {
		t.Fatalf("alerts before a refusal = %+v", e.active())
	}

	addDeviceLimitEvent(e, alice, now.Add(-time.Minute), 5, 5, true)
	e.evaluate()
	alert, ok := e.active()["device_limit//"+alice]
	if !ok || len(e.active()) != 1 {
		t.Fatalf("device-limit alert did not open alone: %+v", e.active())
	}
	if alert.Severity != sevWarning || alert.WhyKey != "health.alert.device_limit.why" || alert.TitleKey != "health.alert.device_limit.title" || alert.NodeID != "" {
		t.Fatalf("alert = %+v", alert)
	}
	want := map[string]string{"user_id": alice, "user_name": "alice", "used": "5", "limit": "5", "count": "1",
		"last_unix": strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)}
	for k, v := range want {
		if alert.Params[k] != v {
			t.Errorf("param %s = %q, want %q", k, alert.Params[k], v)
		}
	}
	if len(alert.Params) != len(want) {
		t.Errorf("params = %v", alert.Params)
	}
	if n, _ := e.s.AlertCounts(e.ctx); n != 0 {
		t.Errorf("a device-limit alert is counted in the badge: %d", n)
	}
	if actions := alertMsg(alert, map[string]string{}, func(store.HealthAlert) string { return "" }).Actions; len(actions) != 2 || actions[0] != "open_user" || actions[1] != "mute" {
		t.Errorf("actions = %v, want open_user and mute", actions)
	}

	// refusals every ten minutes for a day and a half: still the one alert, one open transition
	for i := 0; i < 36*6; i++ {
		e.clock.Advance(10 * time.Minute)
		addDeviceLimitEvent(e, alice, e.clock.Now(), 6, 5, true)
		e.evaluate()
		if got := e.active()["device_limit//"+alice]; got.ID != alert.ID {
			t.Fatalf("after %d refusals the alert is %+v, want the same id %s", i+1, got, alert.ID)
		}
	}
	if len(seen) != 1 || seen[0].Resolved {
		t.Fatalf("transitions while refusals continue = %+v, want a single open", seen)
	}
	if got := e.active()["device_limit//"+alice]; got.Params["used"] != "6" {
		t.Errorf("params were not refreshed: %v", got.Params)
	}

	e.clock.Advance(24*time.Hour - time.Minute)
	e.evaluate()
	if _, ok := e.active()["device_limit//"+alice]; !ok {
		t.Fatal("alert closed before a quiet day")
	}
	e.clock.Advance(2 * time.Minute)
	e.evaluate()
	if _, ok := e.active()["device_limit//"+alice]; ok {
		t.Fatal("alert stayed open after a quiet day")
	}
	if len(seen) != 2 || !seen[1].Resolved || seen[1].Alert.Resolution != "cleared" {
		t.Fatalf("transitions after the quiet day = %+v", seen)
	}
}
