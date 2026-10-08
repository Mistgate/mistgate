package health

import (
	"strconv"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	cFail = adminv1.CheckStatus_CHECK_STATUS_FAILED
	cOK   = adminv1.CheckStatus_CHECK_STATUS_OK
	cDeg  = adminv1.CheckStatus_CHECK_STATUS_DEGRADED
)

// A synthetic alert opens only after two failed rounds, resolves by itself on the next passing round, and a
// failure that returns within an hour re-opens the same row.
func TestSyntheticAlertOpensAfterTwoRoundsAndReopens(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(a)
	e.pass(b)

	e.fail(a, "auth")
	want(t, e.active()) // one failed round is not an alert
	e.fail(a, "auth")
	got := e.active()
	want(t, got, "check_failed/de1/"+a)
	al := got["check_failed/de1/"+a]
	if al.Severity != sevWarning { // two hy2 inbounds on the node: not the only one of its protocol
		t.Fatalf("severity %d", al.Severity)
	}
	if al.WhyKey != "health.alert.check_failed.why.auth" || al.TitleKey != "health.alert.check_failed.title" || al.Params["inbound"] != a {
		t.Fatalf("alert texts: %+v", al)
	}
	first := al.ID

	e.pass(a)
	want(t, e.active())
	if h := e.history(); len(h) != 1 || h[0].Resolution != "cleared" || h[0].ID != first {
		t.Fatalf("history: %+v", h)
	}

	// the same failure within an hour: same row, same first_seen
	e.fail(a, "auth")
	e.fail(a, "auth")
	re := e.active()["check_failed/de1/"+a]
	if re.ID != first || !re.FirstSeen.Equal(al.FirstSeen) {
		t.Fatalf("re-opened alert: %+v, first was %+v", re, al)
	}

	// after more than an hour a new row
	e.pass(a)
	e.clock.Advance(61 * time.Minute)
	e.fail(a, "auth")
	e.fail(a, "auth")
	if fresh := e.active()["check_failed/de1/"+a]; fresh.ID == first {
		t.Fatalf("an alert that ended an hour ago was re-opened: %+v", fresh)
	}
}

func TestDegradedNeverOpensAnAlertAndBreaksTheStreak(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "exit_unreachable")
	e.round(a, cDeg, "http_status")
	e.fail(a, "exit_unreachable")
	want(t, e.active())
	e.round(a, cDeg, "http_status")
	e.round(a, cDeg, "http_status")
	want(t, e.active())
}

// Every probed inbound failing is one critical NO_TRAFFIC of the node (which supersedes CHECK_FAILED, and the
// node status reads it); when all of them only time out on 443 and 8443, the hoster cuts UDP as a whole.
func TestNoTrafficSupersedesCheckFailed(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "timeout")
	e.fail(a, "timeout")
	got := e.active()
	want(t, got, "check_failed/de1/"+a)
	if w := got["check_failed/de1/"+a].WhyKey; w != "health.alert.check_failed.why.udp_blocked" { // b answers on another port
		t.Fatalf("why = %s", w)
	}
	if nt, _, _, _ := e.s.NodeHealth("de1"); nt {
		t.Fatal("no_traffic with one inbound still passing")
	}

	e.fail(b, "timeout")
	e.fail(b, "timeout")
	got = e.active()
	want(t, got, "no_traffic/de1/")
	nt := got["no_traffic/de1/"]
	if nt.Severity != sevCritical || nt.WhyKey != "health.alert.no_traffic.why.udp_all_blocked" || nt.Params["failed"] != "2" || nt.Params["total"] != "2" ||
		nt.Params["ports"] != "443, 8443" {
		t.Fatalf("no_traffic: %+v", nt)
	}
	if noTraffic, failed, total, _ := e.s.NodeHealth("de1"); !noTraffic || failed != 2 || total != 2 {
		t.Fatalf("node health: %v %d %d", noTraffic, failed, total)
	}
	var superseded bool
	for _, h := range e.history() {
		superseded = superseded || (h.Kind == kCheckFailed && h.Resolution == "superseded")
	}
	if !superseded {
		t.Fatalf("check_failed was not superseded: %+v", e.history())
	}

	e.pass(a)
	e.pass(b)
	want(t, e.active())
	if nt, _, _, _ := e.s.NodeHealth("de1"); nt {
		t.Fatal("no_traffic after recovery")
	}
}

func TestMixedErrorsAndOnlyInboundOfProtocolIsCritical(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.fail(a, "tls")
	e.fail(a, "tls")
	e.fail(b, "timeout")
	e.fail(b, "timeout")
	if nt := e.active()["no_traffic/de1/"]; nt.WhyKey != "health.alert.no_traffic.why.mixed" {
		t.Fatalf("no_traffic: %+v", nt)
	}
}

func TestOnlyInboundOfItsProtocolMakesCheckFailedCritical(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "tls")
	e.fail(a, "tls")
	if al := e.active()["check_failed/de1/"+a]; al.Severity != sevWarning || al.WhyKey != "health.alert.check_failed.why.tls" {
		t.Fatalf("two hy2 inbounds: %+v", al)
	}
	e.exec(`UPDATE inbound SET enabled = 0 WHERE id = ?`, b)
	e.evaluate()
	// b is off: a is now the only hy2 inbound of the node, and it is probed alone, so this is NO_TRAFFIC
	want(t, e.active(), "no_traffic/de1/")
}

func TestUserAlertsOpenHoldRefreshAndResolve(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now().UTC().Truncate(time.Hour).Add(20 * time.Minute)
	e.clock.Advance(now.Sub(e.clock.Now()))

	expired := addHealthUser(e, "expired-user", "expired", now.Add(-5*24*time.Hour), now.Add(-10*time.Minute), now.Add(-3*24*time.Hour))
	quota := addHealthUser(e, "quota-user", "limited", now.Add(-2*24*time.Hour), now.Add(-15*time.Minute), now.Add(5*24*time.Hour))
	silent := addHealthUser(e, "silent-user", "active", now.Add(-30*time.Hour), now.Add(-2*time.Hour), time.Time{})
	never := addHealthUser(e, "never-user", "active", time.Time{}, time.Time{}, time.Time{})
	stale := addHealthUser(e, "stale-user", "active", time.Time{}, time.Time{}, time.Time{})
	profile := e.awgProfile("health-user-alerts", "")
	addHealthAWGDevice(e, never, profile, "dev_never", now.Add(-3*time.Hour), time.Time{})
	addHealthAWGDevice(e, stale, profile, "dev_stale", now.Add(-3*24*time.Hour), now.Add(-2*time.Hour))
	e.exec(`UPDATE profile SET critical_epoch = 1 WHERE id = ?`, profile)

	e.evaluate()
	active := e.active()
	want(t, active,
		"access_ended//"+expired, "access_ended//"+quota,
		"user_connection//dev_never", "user_connection//dev_stale", "user_connection//"+silent)
	if got := active["access_ended//"+expired]; got.Severity != sevWarning || got.WhyKey != "health.alert.access_ended.why.expired" ||
		got.Params["since"] != strconv.FormatInt(now.Add(-3*24*time.Hour).Unix(), 10) {
		t.Fatalf("expired alert: %+v", got)
	}
	if got := active["access_ended//"+quota]; got.WhyKey != "health.alert.access_ended.why.quota" {
		t.Fatalf("quota alert: %+v", got)
	} else if got.Params["since"] != strconv.FormatInt(now.Add(-2*24*time.Hour).Unix(), 10) {
		t.Fatalf("quota end time should be the last traffic time: %+v", got.Params)
	}
	for _, subject := range []string{"dev_never", "dev_stale", silent} {
		if got := active["user_connection//"+subject]; got.Severity != sevInfo {
			t.Errorf("user connection alert for %s: %+v", subject, got)
		}
	}
	firstSeen := map[string]time.Time{}
	for k, alert := range active {
		firstSeen[k] = alert.LastSeen
	}
	e.evaluate()
	for k, alert := range e.active() {
		if !alert.LastSeen.Equal(firstSeen[k]) {
			t.Errorf("unchanged %s alert refreshed immediately: %v", k, alert.LastSeen)
		}
	}
	e.clock.Advance(time.Minute)
	e.evaluate()
	for k, alert := range e.active() {
		if !alert.LastSeen.Equal(firstSeen[k]) {
			t.Errorf("unchanged %s alert refreshed before five minutes: %v", k, alert.LastSeen)
		}
	}
	e.clock.Advance(4 * time.Minute)
	e.evaluate()
	for k, alert := range e.active() {
		if !alert.LastSeen.After(firstSeen[k]) {
			t.Errorf("unchanged %s alert was not refreshed at five minutes: %v", k, alert.LastSeen)
		}
	}

	e.exec(`UPDATE device SET last_seen_at = ? WHERE user_id = ? AND hwid_hash IS NULL`,
		e.clock.Now().Add(-userAccessEndedFetchWindow-time.Second).Unix(), expired)
	e.evaluate()
	if _, ok := e.active()["access_ended//"+expired]; !ok {
		t.Fatal("expired access alert resolved while the implicit device was quiet for less than 48 hours")
	}
	if _, ok := e.active()["access_ended//"+quota]; !ok {
		t.Fatal("quota alert resolved while the implicit device was still active")
	}
	e.clock.Advance(42*time.Hour + time.Second)
	e.evaluate()
	if _, ok := e.active()["access_ended//"+expired]; ok {
		t.Fatal("expired access alert stayed open after the implicit device was quiet for 48 hours")
	}

	e.exec(`UPDATE user SET status = 'active', last_seen_at = ? WHERE id IN (?, ?)`, e.clock.Now().Unix(), expired, quota)
	e.exec(`UPDATE device SET last_seen_at = ? WHERE id = 'dev_never'`, e.clock.Now().Unix())
	e.exec(`UPDATE profile SET critical_epoch = 0 WHERE id = ?`, profile)
	e.exec(`UPDATE user SET last_seen_at = ? WHERE id = ?`, e.clock.Now().Unix(), silent)
	e.evaluate()
	want(t, e.active())
}

func TestAccessEndedRequiresPostExpiryFetchAndKnownRecentEnd(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	beforeEnd := addHealthUser(e, "before-end", "expired", time.Time{}, now.Add(-time.Minute), now.Add(-30*time.Minute))
	afterEnd := addHealthUser(e, "after-end", "expired", time.Time{}, now.Add(-time.Minute), now.Add(-30*time.Minute))
	addHealthUser(e, "no-end", "expired", time.Time{}, now.Add(-time.Minute), time.Time{})
	addHealthUser(e, "old-end", "expired", now.Add(-40*24*time.Hour), now.Add(-time.Minute), now.Add(-31*24*time.Hour))
	e.exec(`UPDATE device SET last_seen_at = ? WHERE user_id = ?`, now.Add(-45*time.Minute).Unix(), beforeEnd)

	e.evaluate()
	want(t, e.active(), "access_ended//"+afterEnd)
	if got := e.active()["access_ended//"+afterEnd].Params["since"]; got != strconv.FormatInt(now.Add(-30*time.Minute).Unix(), 10) {
		t.Fatalf("access end parameter = %q, want stable end time", got)
	}
}

func TestAccessEndedHoldsOvernightAndResolvesAfterFortyNineHours(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	user := addHealthUser(e, "overnight", "expired", time.Time{}, now, now.Add(-time.Minute))
	e.evaluate()
	opened := e.active()["access_ended//"+user]
	if opened.ID == "" {
		t.Fatal("access ended alert did not open after a post-expiry fetch")
	}

	e.clock.Advance(10 * time.Hour)
	e.evaluate()
	if got := e.active()["access_ended//"+user]; got.ID != opened.ID {
		t.Fatalf("access ended alert did not survive 10 hours without a fetch: %+v", got)
	}

	e.clock.Advance(39 * time.Hour)
	e.evaluate()
	if _, ok := e.active()["access_ended//"+user]; ok {
		t.Fatal("access ended alert stayed open after 49 hours without a fetch")
	}
	if history := e.history(); len(history) != 1 || history[0].Resolution != "cleared" {
		t.Fatalf("access ended history = %+v", history)
	}
}

func TestUserConnectionIdentityIgnoresLastNode(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "provider", true)
	e.node("nl1", "provider", true)
	now := e.clock.Now()
	user := addHealthUser(e, "silent-user", "active", now.Add(-30*time.Hour), now.Add(-time.Hour), time.Time{})
	e.exec(`UPDATE user SET last_node_id = 'de1' WHERE id = ?`, user)
	e.evaluate()
	first := e.active()["user_connection//"+user]
	if first.ID == "" {
		t.Fatal("user connection alert did not use an empty node key")
	}

	e.exec(`UPDATE user SET last_node_id = 'nl1' WHERE id = ?`, user)
	e.evaluate()
	if got := e.active()["user_connection//"+user]; got.ID != first.ID {
		t.Fatalf("changing last_node_id changed the alert: first %+v, now %+v", first, got)
	}
}

// A stale key is worth an alert only while the person uses it: not for a user whose access ended, not for a device
// quiet for weeks.
func TestStaleKeyNeedsAnActiveUserAndRecentUse(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	ended := addHealthUser(e, "ended-user", "expired", time.Time{}, time.Time{}, now.Add(-24*time.Hour))
	idle := addHealthUser(e, "idle-user", "active", time.Time{}, time.Time{}, time.Time{})
	profile := e.awgProfile("stale-key-limits", "")
	addHealthAWGDevice(e, ended, profile, "dev_ended", now.Add(-30*24*time.Hour), now.Add(-2*time.Hour))
	addHealthAWGDevice(e, idle, profile, "dev_idle", now.Add(-60*24*time.Hour), now.Add(-20*24*time.Hour))
	e.exec(`UPDATE profile SET critical_epoch = 1 WHERE id = ?`, profile)
	e.evaluate()
	want(t, e.active())
}

func TestUsersImpactedHysteresisAndHourBoundary(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "provider", true)
	e.inbound("de1", 443)
	hour := e.clock.Now().UTC().Truncate(time.Hour)
	setNodeConnectedAt(e, "de1", hour.Add(-time.Hour))
	seedImpactHours(e, "de1", "hysteria2", hour, 1, 5)
	e.clock.Advance(hour.Add(20 * time.Minute).Sub(e.clock.Now()))
	e.evaluate()
	alertKey := "users_impacted/de1/hysteria2"
	opened := e.active()[alertKey]
	if opened.Severity != sevWarning || opened.WhyKey != "health.alert.users_impacted.why.gone" ||
		opened.Params["now"] != "1" || opened.Params["usual"] != "5" || opened.Params["users"] != "4" {
		t.Fatalf("users impacted alert: %+v", opened)
	}

	setImpactPeak(e, "de1", "hysteria2", hour, 2)
	e.evaluate()
	if got := e.active()[alertKey]; got.ID != opened.ID {
		t.Fatalf("alert did not hold in the hysteresis band: %+v", got)
	}
	setImpactPeak(e, "de1", "hysteria2", hour, 3)
	e.evaluate()
	want(t, e.active())

	setImpactPeak(e, "de1", "hysteria2", hour, 1)
	e.evaluate()
	if got := e.active()[alertKey]; got.ID != opened.ID {
		t.Fatalf("recent users_impacted alert did not reopen: %+v", got)
	}

	nextHour := hour.Add(time.Hour)
	seedImpactHistory(e, "de1", "hysteria2", nextHour, 5)
	e.clock.Advance(nextHour.Add(10 * time.Minute).Sub(e.clock.Now()))
	e.evaluate()
	if got := e.active()[alertKey]; got.ID != opened.ID {
		t.Fatalf("alert did not hold across the hour boundary: %+v", got)
	}
	e.clock.Advance(10 * time.Minute)
	e.evaluate()
	if got := e.active()[alertKey]; got.ID != opened.ID {
		t.Fatalf("alert did not continue after the new hour warmed up: %+v", got)
	}
	setImpactPeak(e, "de1", "hysteria2", nextHour, 3)
	e.evaluate()
	want(t, e.active())

}

func TestUsersImpactedWaitsForMinuteTwentyAndUsualBaseline(t *testing.T) {
	for _, tc := range []struct {
		name    string
		minute  time.Duration
		usual   int
		opensAt time.Duration
	}{
		{name: "early", minute: 19 * time.Minute, usual: 5, opensAt: 20 * time.Minute},
		{name: "usual at minimum", minute: 20 * time.Minute, usual: 5, opensAt: 20 * time.Minute},
		{name: "usual below minimum", minute: 20 * time.Minute, usual: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.node("de1", "provider", true)
			e.inbound("de1", 443)
			hour := e.clock.Now().UTC().Truncate(time.Hour)
			setNodeConnectedAt(e, "de1", hour.Add(-time.Hour))
			seedImpactHours(e, "de1", "hysteria2", hour, 0, tc.usual)
			e.clock.Advance(hour.Add(tc.minute).Sub(e.clock.Now()))
			e.evaluate()
			if tc.opensAt == 0 {
				want(t, e.active())
				return
			}
			if tc.minute < tc.opensAt {
				want(t, e.active())
				e.clock.Advance(tc.opensAt - tc.minute)
				e.evaluate()
			}
			want(t, e.active(), "users_impacted/de1/hysteria2")
		})
	}
}

func TestUsersImpactedHoldsWhenMedianFallsBelowMinimum(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "provider", true)
	e.inbound("de1", 443)
	hour := e.clock.Now().UTC().Truncate(time.Hour)
	setNodeConnectedAt(e, "de1", hour.Add(-time.Hour))
	seedImpactHours(e, "de1", "hysteria2", hour, 1, 5)
	e.clock.Advance(hour.Add(20 * time.Minute).Sub(e.clock.Now()))
	e.evaluate()
	alertKey := "users_impacted/de1/hysteria2"
	opened := e.active()[alertKey]
	if opened.ID == "" {
		t.Fatal("users_impacted alert did not open")
	}

	nightHour := hour.Add(10 * time.Hour)
	seedImpactHours(e, "de1", "hysteria2", nightHour, 0, 2)
	e.clock.Advance(nightHour.Add(20 * time.Minute).Sub(e.clock.Now()))
	e.evaluate()
	if got := e.active()[alertKey]; got.ID != opened.ID {
		t.Fatalf("active users_impacted alert did not hold below the usual minimum: %+v", got)
	}
}

func TestUsersImpactedWaitsAfterNodeReconnect(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "provider", true)
	e.inbound("de1", 443)
	hour := e.clock.Now().UTC().Truncate(time.Hour)
	seedImpactHours(e, "de1", "hysteria2", hour, 1, 5)

	e.clock.Advance(hour.Add(20 * time.Minute).Sub(e.clock.Now()))
	e.evaluate()
	want(t, e.active())
	e.clock.Advance(time.Minute)
	e.evaluate()
	alertKey := "users_impacted/de1/hysteria2"
	opened := e.active()[alertKey]
	if opened.ID == "" {
		t.Fatal("users_impacted did not open after the node had been connected for 20 minutes")
	}

	e.exec(`UPDATE node SET last_connected_at = ? WHERE id = ?`, e.clock.Now().Unix(), "de1")
	e.evaluate()
	if got := e.active()[alertKey]; got.ID != opened.ID {
		t.Fatalf("users_impacted alert did not hold after the node reconnected: %+v", got)
	}
}

func TestUsersImpactedSkipsRedistributedTraffic(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "provider", true)
	e.node("nl1", "provider", true)
	e.inbound("de1", 443)
	e.inbound("nl1", 443)
	hour := e.clock.Now().UTC().Truncate(time.Hour)
	setNodeConnectedAt(e, "de1", hour.Add(-time.Hour))
	setNodeConnectedAt(e, "nl1", hour.Add(-time.Hour))
	seedImpactHours(e, "de1", "hysteria2", hour, 0, 10)
	seedImpactHours(e, "nl1", "hysteria2", hour, 10, 10)
	e.clock.Advance(hour.Add(20 * time.Minute).Sub(e.clock.Now()))
	e.evaluate()
	for key := range e.active() {
		if strings.HasPrefix(key, "users_impacted/") {
			t.Fatalf("traffic redistribution opened an alert: %s", key)
		}
	}
}

func TestUsersImpactedIsSuppressedByHeldActiveCheckFailed(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "provider", true)
	first, second := e.inbound("de1", 443), e.inbound("de1", 8443)
	hour := e.clock.Now().UTC().Truncate(time.Hour)
	setNodeConnectedAt(e, "de1", hour.Add(-time.Hour))
	seedImpactHours(e, "de1", "hysteria2", hour, 5, 5)
	e.pass(first)
	e.pass(second)
	e.fail(first, "timeout")
	e.fail(first, "timeout")
	if _, ok := e.active()["check_failed/de1/"+first]; !ok {
		t.Fatal("CHECK_FAILED did not open")
	}

	e.clock.Advance(hour.Add(40 * time.Minute).Sub(e.clock.Now()))
	setImpactPeak(e, "de1", "hysteria2", hour, 1)
	e.evaluate()
	active := e.active()
	if _, ok := active["check_failed/de1/"+first]; !ok {
		t.Fatal("stale CHECK_FAILED was not held")
	}
	if _, ok := active["users_impacted/de1/hysteria2"]; ok {
		t.Fatal("users_impacted opened while a held CHECK_FAILED explained the protocol")
	}

	sn, err := e.s.snapshot(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.DoctorResults(e.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	accepts, err := e.st.DoctorAccepts(e.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	signals, err := e.st.HealthSignals(e.ctx, e.clock.Now())
	if err != nil {
		t.Fatal(err)
	}
	d := e.s.derive(e.ctx, e.clock.Now(), sn, rows, acceptsByNode(accepts), signals, mustActiveAlerts(t, e))
	if !d.holdSynth["de1"] {
		t.Fatal("test did not leave CHECK_FAILED in the held state")
	}
	if _, ok := d.conds[key{kCheckFailed, "de1", first}]; ok {
		t.Fatal("held CHECK_FAILED was also a current condition")
	}
	if _, ok := d.conds[key{kUsersImpacted, "de1", "hysteria2"}]; ok {
		t.Fatal("held CHECK_FAILED did not suppress the gone condition")
	}
}

func TestUsersImpactedIsSupersededWhenProtocolIsDisabled(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "provider", true)
	inbound := e.inbound("de1", 443)
	hour := e.clock.Now().UTC().Truncate(time.Hour)
	setNodeConnectedAt(e, "de1", hour.Add(-time.Hour))
	seedImpactHours(e, "de1", "hysteria2", hour, 1, 5)
	e.clock.Advance(hour.Add(20 * time.Minute).Sub(e.clock.Now()))
	e.evaluate()
	if _, ok := e.active()["users_impacted/de1/hysteria2"]; !ok {
		t.Fatal("users_impacted did not open")
	}

	e.exec(`UPDATE inbound SET enabled = 0 WHERE id = ?`, inbound)
	e.evaluate()
	if _, ok := e.active()["users_impacted/de1/hysteria2"]; ok {
		t.Fatal("users_impacted stayed open after its protocol was disabled")
	}
	found := false
	for _, alert := range e.history() {
		found = found || (alert.Kind == kUsersImpacted && alert.Resolution == "superseded")
	}
	if !found {
		t.Fatalf("disabled protocol resolution was not superseded: %+v", e.history())
	}
}

func TestUsersImpactedIsSupersededByProtocolChecks(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inbounds  int
		checkFail bool
	}{
		{name: "no traffic", inbounds: 1},
		{name: "check failed", inbounds: 2, checkFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.node("de1", "provider", true)
			first := e.inbound("de1", 443)
			var second string
			if tc.inbounds == 2 {
				second = e.inbound("de1", 8443)
			}
			hour := e.clock.Now().UTC().Truncate(time.Hour)
			setNodeConnectedAt(e, "de1", hour.Add(-time.Hour))
			seedImpactHours(e, "de1", "hysteria2", hour, 1, 5)
			e.clock.Advance(hour.Add(20 * time.Minute).Sub(e.clock.Now()))
			e.evaluate()
			if _, ok := e.active()["users_impacted/de1/hysteria2"]; !ok {
				t.Fatal("users_impacted did not open before the check failure")
			}
			e.fail(first, "timeout")
			e.fail(first, "timeout")
			if tc.checkFail {
				e.pass(second)
				e.fail(first, "timeout")
				e.fail(first, "timeout")
			}
			active := e.active()
			if _, ok := active["users_impacted/de1/hysteria2"]; ok {
				t.Fatalf("users_impacted stayed open with a node check alert: %+v", active)
			}
			if tc.checkFail {
				if _, ok := active["check_failed/de1/"+first]; !ok {
					t.Fatalf("CHECK_FAILED was not raised: %+v", active)
				}
			} else if _, ok := active["no_traffic/de1/"]; !ok {
				t.Fatalf("NO_TRAFFIC was not raised: %+v", active)
			}
			found := false
			for _, history := range e.history() {
				found = found || (history.Kind == kUsersImpacted && history.Resolution == "superseded")
			}
			if !found {
				t.Fatalf("users_impacted resolution was not superseded: %+v", e.history())
			}
		})
	}
}

func addHealthUser(e *env, name, status string, lastTraffic, fetched, expires time.Time) string {
	e.t.Helper()
	group := must(e.acc.CreateGroup(e.ctx, req(&adminv1.CreateGroupRequest{Name: name + "-group"}))).Msg.Group.Id
	user := must(e.acc.CreateUser(e.ctx, req(&adminv1.CreateUserRequest{Name: name, GroupId: group}))).Msg.User
	trafficAt := int64(0)
	if !lastTraffic.IsZero() {
		trafficAt = lastTraffic.Unix()
	}
	var expiresAt any
	if !expires.IsZero() {
		expiresAt = expires.Unix()
	}
	e.exec(`UPDATE user SET status = ?, last_seen_at = ?, expires_at = ? WHERE id = ?`, status, trafficAt, expiresAt, user.Id)
	fetchedAt := int64(0)
	if !fetched.IsZero() {
		fetchedAt = fetched.Unix()
	}
	e.exec(`UPDATE device SET last_seen_at = ? WHERE user_id = ? AND hwid_hash IS NULL`, fetchedAt, user.Id)
	return user.Id
}

func addHealthAWGDevice(e *env, userID, profileID, deviceID string, createdAt, handshakeAt time.Time) {
	e.t.Helper()
	_, err := e.st.Access().AddAWGDevice(e.ctx, store.AWGDeviceAdd{
		Device: store.AccessDevice{ID: deviceID, UserID: userID}, ProfileID: profileID, MaxIdx: 100, Limit: 10,
	}, createdAt, func(idx int) (store.AccessCred, string, error) {
		return store.AccessCred{ID: "crd_" + deviceID, Protocol: "awg", SecretEnc: []byte{1}, DataJSON: "{}"},
			"pub_" + deviceID, nil
	})
	if err != nil {
		e.t.Fatalf("add AWG health device: %v", err)
	}
	if !handshakeAt.IsZero() {
		e.exec(`UPDATE device SET last_seen_at = ? WHERE id = ?`, handshakeAt.Unix(), deviceID)
	}
}

func seedImpactHours(e *env, nodeID, protocol string, hour time.Time, current, usual int) {
	e.t.Helper()
	seedImpactHistory(e, nodeID, protocol, hour, usual)
	setImpactPeak(e, nodeID, protocol, hour, current)
}

func seedImpactHistory(e *env, nodeID, protocol string, hour time.Time, usual int) {
	e.t.Helper()
	for day := 1; day <= 7; day++ {
		setImpactPeak(e, nodeID, protocol, hour.Add(-time.Duration(day)*24*time.Hour), usual)
	}
}

func setImpactPeak(e *env, nodeID, protocol string, hour time.Time, peak int) {
	e.t.Helper()
	e.exec(`INSERT INTO node_traffic_hour (node_id, protocol, hour_start, peak_users) VALUES (?, ?, ?, ?)
		ON CONFLICT(node_id, protocol, hour_start) DO UPDATE SET peak_users = excluded.peak_users`,
		nodeID, protocol, hour.UTC().Truncate(time.Hour).Unix(), peak)
}

func setNodeConnectedAt(e *env, nodeID string, at time.Time) {
	e.t.Helper()
	e.exec(`UPDATE node SET last_connected_at = ? WHERE id = ?`, at.Unix(), nodeID)
}

func mustActiveAlerts(t *testing.T, e *env) []store.HealthAlert {
	t.Helper()
	active, err := e.st.ActiveAlerts(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return active
}

func TestWhyVariantWarpPath(t *testing.T) {
	spec := func(port uint16, egress string) *target {
		t := &target{}
		t.spec.Listen.Port, t.spec.Egress, t.spec.Protocol = port, egress, "hysteria2"
		return t
	}
	failing := probed{spec(443, "warp"), cell{last: &Result{ErrorCode: "exit_unreachable"}, streak: 3}}
	direct := probed{spec(8443, "direct"), cell{last: &Result{}, streak: 0}}
	if got := whyVariant(failing, []probed{failing, direct}); got != "warp_path" {
		t.Fatalf("got %s", got)
	}
	if got := whyVariant(failing, []probed{failing}); got != "exit_unreachable" {
		t.Fatalf("no passing direct inbound: %s", got)
	}
	timeout := probed{spec(443, "direct"), cell{last: &Result{ErrorCode: "timeout"}, streak: 3}}
	if got := whyVariant(timeout, []probed{timeout, direct}); got != "udp_blocked" {
		t.Fatalf("got %s", got)
	}
	if got := whyVariant(timeout, []probed{timeout}); got != "timeout" {
		t.Fatalf("got %s", got)
	}
}

// A silence shorter than the blip window is no alert; a longer one opens NODE_DOWN, suppresses the node's other
// alerts without resolving them, and resolves as node_returned when the node is back. A panel that just started
// does not call the silent nodes down while their agents reconnect.
func TestNodeDownAndBlip(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a := e.inbound("de1", 443)
	e.report("de1", false, res("disk_space", dWarn, "journald_vacuum"))
	want(t, e.active(), "doctor_warn/de1/disk_space")
	_ = a

	e.fl.set("de1", liveState{up: false, caps: []string{capDoctor}})
	e.clock.Advance(5 * time.Minute) // a blip
	e.evaluate()
	want(t, e.active(), "doctor_warn/de1/disk_space")

	e.clock.Advance(6 * time.Minute) // 11 minutes of silence
	e.evaluate()
	got := e.active()
	want(t, got, "doctor_warn/de1/disk_space", "node_down/de1/")
	if nd := got["node_down/de1/"]; nd.Severity != sevCritical || nd.Params["minutes"] == "" {
		t.Fatalf("node_down: %+v", nd)
	}

	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor}})
	e.evaluate()
	want(t, e.active(), "doctor_warn/de1/disk_space")
	found := false
	for _, h := range e.history() {
		if h.Kind == kNodeDown {
			found = h.Resolution == "node_returned"
		}
	}
	if !found {
		t.Fatalf("history: %+v", e.history())
	}
}

func TestNodeDownWaitsForTheStartGrace(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.StartGrace = 5 * time.Minute })
	e.node("de1", "hetzner", false)
	old := e.clock.Now().Add(-time.Hour).Unix() // long silent before the panel started
	e.exec(`UPDATE node SET last_seen_at = ?, last_connected_at = ?`, old, old)
	e.evaluate()
	want(t, e.active())
	e.clock.Advance(6 * time.Minute)
	e.evaluate()
	want(t, e.active(), "node_down/de1/")
}

func TestHostBlipIsAResolvedHistoryRecord(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	now := e.clock.Now()
	e.s.NodeReturned(e.ctx, "de1", now.Add(-4*time.Minute), now)
	want(t, e.active())
	h := e.history()
	if len(h) != 1 || h[0].Kind != kHostBlip || h[0].Severity != sevInfo || h[0].Resolution != "node_returned" || h[0].Params["minutes"] != "4" ||
		!h[0].FirstSeen.Equal(now.Add(-4*time.Minute)) {
		t.Fatalf("history: %+v", h)
	}
	if n, _ := e.s.AlertCounts(e.ctx); n != 0 {
		t.Fatal("a blip record was counted in the badge")
	}
}

// Doctor rows become alerts as the report says (debounce is the agent's), WARN -> FAIL supersedes, the next OK
// resolves, and a resolve right after ApplyFix is "fix_applied".
func TestDoctorAlerts(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)

	e.report("de1", false, res("disk_space", dWarn, "journald_vacuum", "used_pct", "85"), res("resolver", dOK, ""), res("ipv6", dSkip, ""))
	got := e.active()
	want(t, got, "doctor_warn/de1/disk_space")
	al := got["doctor_warn/de1/disk_space"]
	if al.Severity != sevWarning || al.WhyKey != "health.doctor.disk_space.why" || al.Params["used_pct"] != "85" || al.Params["fix_id"] != "journald_vacuum" ||
		al.TitleKey != "health.alert.doctor_warn.title" || al.Subject != "disk_space" {
		t.Fatalf("alert: %+v", al)
	}

	e.report("de1", true, res("disk_space", dFail, "journald_vacuum", "used_pct", "95")) // a partial report merges
	got = e.active()
	want(t, got, "doctor_fail/de1/disk_space")
	if got["doctor_fail/de1/disk_space"].Severity != sevCritical {
		t.Fatal("FAIL is not critical")
	}
	if h := e.history(); len(h) != 1 || h[0].Kind != kDoctorWarn || h[0].Resolution != "superseded" {
		t.Fatalf("history: %+v", h)
	}

	// the admin applies the fix, then the re-check is OK
	e.s.fixMu.Lock()
	e.s.recentFix["de1/journald_vacuum"] = e.clock.Now()
	e.s.fixMu.Unlock()
	e.report("de1", true, res("disk_space", dOK, ""))
	want(t, e.active())
	if h := e.history(); h[0].Kind != kDoctorFail || h[0].Resolution != "fix_applied" {
		t.Fatalf("history: %+v", h)
	}
	if rows, _ := e.st.DoctorResults(e.ctx, "de1"); len(rows) != 3 {
		t.Fatalf("the partial report dropped the other checks: %d rows", len(rows))
	}

	// not after a fix: cleared
	e.report("de1", true, res("resolver", dFail, ""))
	e.report("de1", true, res("resolver", dOK, ""))
	if h := e.history(); h[0].Kind != kDoctorFail || h[0].Subject != "resolver" || h[0].Resolution != "cleared" {
		t.Fatalf("history: %+v", h)
	}
}

func TestCertExpiryFromDoctorNeverDuplicatesAsDoctorAlert(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("cert_expiry", dWarn, "", "days_left", "9"))
	want(t, e.active(), "cert_expiry/de1/cert")
	e.report("de1", false, res("cert_expiry", dFail, "", "days_left", "2", "inbound_id", "inb_x"))
	got := e.active()
	want(t, got, "cert_expiry/de1/inb_x")
	if got["cert_expiry/de1/inb_x"].Severity != sevCritical {
		t.Fatal("a failing certificate is critical")
	}
}

func TestKernelHeadersIsInfoWithoutAmneziaWG(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("kernel_headers", dWarn, ""))
	if al := e.active()["doctor_warn/de1/kernel_headers"]; al.Severity != sevInfo {
		t.Fatalf("kernel_headers: %+v", al)
	}
	if n, _ := e.s.AlertCounts(e.ctx); n != 0 {
		t.Fatalf("an INFO alert is in the badge: %d", n)
	}
}

func TestDoctorFixIdsAreTheFiveKnownOnes(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dWarn, "rm_rf_slash"), res("resolver", dWarn, "set_resolver"), res("ipv6", dOK, "set_resolver"), res("warp_path", dFail, "reconnect_warp"))
	rows, _ := e.st.DoctorResults(e.ctx, "de1")
	fixes := map[string]string{}
	for _, r := range rows {
		fixes[r.CheckID] = r.FixID
	}
	if fixes["disk_space"] != "" || fixes["resolver"] != "set_resolver" || fixes["ipv6"] != "" || fixes["warp_path"] != "reconnect_warp" {
		t.Fatalf("fix ids: %v", fixes)
	}
}

// A stale report says nothing about the present: its alerts stay as they are and no new ones open from it.
func TestStaleDoctorReportHoldsItsAlerts(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dWarn, ""))
	e.clock.Advance(30 * time.Minute)
	e.evaluate()
	want(t, e.active(), "doctor_warn/de1/disk_space")
	if _, _, _, fail := e.s.NodeHealth("de1"); fail != "" {
		t.Fatal("stale doctor state reaches the node status")
	}
	e.report("de1", false, res("disk_space", dOK, "")) // a fresh report decides again
	want(t, e.active())
}

func TestStateDriftFollowsTheFleet(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor}, drift: true})
	e.evaluate()
	want(t, e.active(), "state_drift/de1/")
	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor}})
	e.evaluate()
	want(t, e.active())
}

func TestRetiredNodeResolvesItsAlerts(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dWarn, ""))
	e.exec(`UPDATE node SET state = 'retired', retired_at = 1 WHERE id = 'de1'`)
	e.evaluate()
	want(t, e.active())
	if h := e.history(); len(h) != 1 || h[0].Resolution != "node_retired" {
		t.Fatalf("history: %+v", h)
	}
}

// A muted alert still resolves; the badge leaves it out while it is muted and takes it back after.
func TestMuteHidesFromBadgeNotFromResolving(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dFail, ""), res("resolver", dWarn, ""))
	if n, c := e.s.AlertCounts(e.ctx); n != 2 || c != 1 {
		t.Fatalf("badge %d/%d", n, c)
	}
	id := e.active()["doctor_fail/de1/disk_space"].ID
	if _, err := e.st.MuteAlert(e.ctx, id, e.clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, c := e.s.AlertCounts(e.ctx); n != 1 || c != 0 {
		t.Fatalf("badge while muted %d/%d", n, c)
	}
	e.evaluate() // evaluation goes on, the mute stays
	if al := e.active()["doctor_fail/de1/disk_space"]; al.MutedUntil.IsZero() {
		t.Fatal("mute lost")
	}
	e.clock.Advance(2 * time.Hour)
	e.report("de1", false, res("disk_space", dFail, ""), res("resolver", dWarn, ""))
	if n, _ := e.s.AlertCounts(e.ctx); n != 2 {
		t.Fatalf("badge after the mute ended: %d", n)
	}
	e.report("de1", false, res("disk_space", dOK, ""), res("resolver", dWarn, ""))
	var resolved bool
	for _, h := range e.history() {
		resolved = resolved || (h.ID == id && h.Resolution == "cleared")
	}
	if !resolved {
		t.Fatal("a muted alert did not resolve")
	}
}

// When most probed inbounds fail on nodes of three providers the panel's own network is suspect: one fleet
// alert, no per-node alerts, no NO_TRAFFIC status; it resolves when rounds pass again.
func TestBlindPanelGuard(t *testing.T) {
	e := newEnv(t)
	var ins []string
	for _, n := range []struct{ id, provider string }{{"n1", "hetzner"}, {"n2", "hostera"}, {"n3", "hosterb"}, {"n4", "hetzner"}} {
		e.node(n.id, n.provider, true)
		ins = append(ins, e.inbound(n.id, 443))
	}
	for _, in := range ins {
		e.fail(in, "timeout")
	}
	for _, in := range ins {
		e.fail(in, "timeout")
	}
	got := e.active()
	want(t, got, "check_failed//panel_egress")
	if got["check_failed//panel_egress"].WhyKey != "health.alert.check_failed.why.panel_egress" {
		t.Fatalf("guard alert: %+v", got)
	}
	if nt, _, _, _ := e.s.NodeHealth("n1"); nt {
		t.Fatal("NO_TRAFFIC status while the panel is blind")
	}
	for _, in := range ins {
		e.pass(in)
	}
	want(t, e.active())
}

func TestGuardNeedsThreeProviders(t *testing.T) {
	e := newEnv(t)
	var ins []string
	for _, n := range []struct{ id, provider string }{{"n1", "hetzner"}, {"n2", "hetzner"}, {"n3", "hostera"}} {
		e.node(n.id, n.provider, true)
		ins = append(ins, e.inbound(n.id, 443))
	}
	for range 2 {
		for _, in := range ins {
			e.fail(in, "timeout")
		}
	}
	got := e.active()
	want(t, got, "no_traffic/n1/", "no_traffic/n2/", "no_traffic/n3/")
}

// The agent's own view of a certificate is the fallback for agents without a doctor.
func TestPanelViewOfCertificates(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a := e.inbound("de1", 443)
	e.exec(`UPDATE inbound SET cert_not_after = ? WHERE id = ?`, e.clock.Now().Add(10*24*time.Hour).Unix(), a)
	e.evaluate()
	got := e.active()
	want(t, got, "cert_expiry/de1/"+a)
	if al := got["cert_expiry/de1/"+a]; al.Severity != sevWarning || al.Params["days_left"] != "10" {
		t.Fatalf("10 days: %+v", al)
	}
	e.exec(`UPDATE inbound SET cert_not_after = ? WHERE id = ?`, e.clock.Now().Add(-time.Hour).Unix(), a)
	e.evaluate()
	if al := e.active()["cert_expiry/de1/"+a]; al.Severity != sevCritical || al.WhyKey != "health.alert.cert_expiry.why.expired" {
		t.Fatalf("expired: %+v", al)
	}
	// with a doctor report that covers certificates, the doctor is the only source
	e.report("de1", false, res("cert_expiry", dOK, ""))
	want(t, e.active())
}

func TestTransitionHook(t *testing.T) {
	var seen []Transition
	e := newEnv(t, func(c *Config) { c.OnTransition = func(tr Transition) { seen = append(seen, tr) } })
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dWarn, ""))
	e.report("de1", false, res("disk_space", dOK, ""))
	if len(seen) != 2 || seen[0].Resolved || !seen[1].Resolved || seen[1].Alert.Resolution != "cleared" {
		t.Fatalf("transitions: %+v", seen)
	}
}

func TestReportsAreCleanedAndBounded(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	r := res("disk_space", dWarn, "")
	r.Detail = string(long)
	r.Params["k"] = string(long)
	bad := []*agentv1.DoctorResult{res("Bad Id!", dWarn, ""), {Id: "ok_one", Status: 9}}
	all := append([]*agentv1.DoctorResult{r}, bad...)
	e.s.DoctorReport(e.ctx, "de1", &agentv1.DoctorReport{Results: all})
	rows, _ := e.st.DoctorResults(e.ctx, "de1")
	if len(rows) != 1 || len(rows[0].Detail) > 256 || len(rows[0].Params["k"]) > 256 || rows[0].TitleKey != "doctor.disk_space.title" {
		t.Fatalf("rows: %+v", rows)
	}
	// a report that says it did not run changes nothing
	e.s.DoctorReport(e.ctx, "de1", &agentv1.DoctorReport{Error: "busy", Results: []*agentv1.DoctorResult{res("ipv6", dFail, "")}})
	if rows, _ := e.st.DoctorResults(e.ctx, "de1"); len(rows) != 1 {
		t.Fatalf("busy report stored: %+v", rows)
	}
}

// A result from before the node was away is not an answer of now: its alert stays until a fresh round decides, and
// no alert opens from it.
func TestStaleCheckResultsHoldTheirAlerts(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "tls")
	e.fail(a, "tls")
	want(t, e.active(), "check_failed/de1/"+a)

	e.clock.Advance(20 * time.Minute) // nobody probed for a while (the node was away)
	e.evaluate()
	want(t, e.active(), "check_failed/de1/"+a)
	if nt, _, _, _ := e.s.NodeHealth("de1"); nt {
		t.Fatal("stale results feed the node status")
	}
	e.pass(a)
	e.pass(b)
	want(t, e.active())
}

// Alerts that opened before the whole picture was visible (the first inbounds to fail twice) are superseded by the
// fleet alert once most probed inbounds on three providers fail.
func TestGuardSupersedesEarlierNodeAlerts(t *testing.T) {
	e := newEnv(t)
	var ins []string
	for _, n := range []struct{ id, provider string }{{"n1", "hetzner"}, {"n2", "hostera"}, {"n3", "hosterb"}} {
		e.node(n.id, n.provider, true)
		ins = append(ins, e.inbound(n.id, 443))
	}
	e.fail(ins[0], "timeout")
	e.fail(ins[0], "timeout")
	want(t, e.active(), "no_traffic/n1/")
	e.fail(ins[1], "timeout")
	e.fail(ins[2], "timeout")
	want(t, e.active(), "check_failed//panel_egress")
	var superseded bool
	for _, h := range e.history() {
		superseded = superseded || (h.Kind == kNoTraffic && h.NodeID == "n1" && h.Resolution == "superseded")
	}
	if !superseded {
		t.Fatalf("history: %+v", e.history())
	}
}

// The doctor list is shown in the order of agent.proto CHECK IDS, the L3 checks included; an id the panel does not
// know yet goes after them.
func TestDoctorRowsFollowTheCheckIDOrder(t *testing.T) {
	ids := []string{"warp_path", "aa_new", "awg_backend", "kernel_headers", "disk_space"}
	rows := make([]store.DoctorRow, len(ids))
	for i, id := range ids {
		rows[i].CheckID = id
	}
	sortDoctor(rows)
	var got []string
	for _, r := range rows {
		got = append(got, r.CheckID)
	}
	if want := "disk_space kernel_headers awg_backend warp_path aa_new"; strings.Join(got, " ") != want {
		t.Fatalf("order %v, want %s", got, want)
	}
}
