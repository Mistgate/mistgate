package health

import (
	"testing"
	"time"
)

// The owner's retention.* settings replace the defaults, a value out of range is ignored, and the hourly traffic tables are pruned
// (400 days by default).
func TestSweepAppliesTheOwnersRetention(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	now := e.clock.Now()
	day := 24 * time.Hour
	add := func(sev int, age time.Duration) {
		e.exec(`INSERT INTO event (ts, severity, code, source, params_json) VALUES (?, ?, 'ret', 'panel', '{}')`, now.Add(-age).Unix(), sev)
	}
	add(1, 20*day) // info, kept 90 days by default
	add(1, 2*day)
	add(3, 40*day) // error, kept 400 days by default
	e.exec(`INSERT INTO audit (ts, actor, action, params, result, source) VALUES (?, 'a', 'ret_old', '{}', 'ok', 'panel')`, now.Add(-100*day).Unix())
	e.exec(`INSERT INTO audit (ts, actor, action, params, result, source) VALUES (?, 'a', 'ret_new', '{}', 'ok', 'panel')`, now.Add(-10*day).Unix())
	e.exec(`INSERT INTO user_group (id, name, created_at) VALUES ('grp_r', 'g', 1)`)
	e.exec(`INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_r', 'u', 'grp_r', 1, x'01', x'02', 1)`)
	hour := now.Unix() / 3600 * 3600
	for _, age := range []time.Duration{401 * day, 399 * day, 100 * day} {
		at := hour - int64(age/time.Second)
		e.exec(`INSERT INTO traffic_bucket (user_id, node_id, protocol, hour_start, bytes_up, bytes_down) VALUES ('usr_r', 'de1', 'hysteria2', ?, 1, 1)`, at)
		e.exec(`INSERT INTO node_traffic_hour (node_id, protocol, hour_start, bytes_up, bytes_down) VALUES ('de1', 'hysteria2', ?, 1, 1)`, at)
	}
	count := func(q string) int {
		var n int
		if err := e.st.R.QueryRowContext(e.ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// defaults: nothing here is old enough but the traffic of 401 days ago
	e.s.sweep(e.ctx)
	if got := count(`SELECT count(*) FROM event WHERE code = 'ret'`); got != 3 {
		t.Fatalf("events after the default sweep: %d", got)
	}
	if got := count(`SELECT count(*) FROM audit WHERE action LIKE 'ret_%'`); got != 2 {
		t.Fatalf("audit after the default sweep: %d", got)
	}
	if b, h := count(`SELECT count(*) FROM traffic_bucket`), count(`SELECT count(*) FROM node_traffic_hour`); b != 2 || h != 2 {
		t.Fatalf("traffic rows after the default sweep: buckets %d, node hours %d (the 401-day rows go, the others stay)", b, h)
	}

	// the owner's values: info events 7 days, errors 30, traffic 90; audit 5 is below its limit (30 days), so the default applies
	e.exec(`INSERT INTO setting (k, v) VALUES ('retention.events_info_days', '7'), ('retention.events_warn_days', '30'), ('retention.audit_days', '5'),
		('retention.traffic_bucket_days', '90'), ('retention.node_traffic_hour_days', '90')`)
	e.s.sweep(e.ctx)
	if got := count(`SELECT count(*) FROM event WHERE code = 'ret'`); got != 1 {
		t.Fatalf("events after the owner's sweep: %d, want only the info event of 2 days", got)
	}
	if got := count(`SELECT count(*) FROM audit WHERE action LIKE 'ret_%'`); got != 2 {
		t.Fatalf("audit with an out-of-range value: %d, want both (the default 400 days applies)", got)
	}
	if b, h := count(`SELECT count(*) FROM traffic_bucket`), count(`SELECT count(*) FROM node_traffic_hour`); b != 0 || h != 0 {
		t.Fatalf("traffic rows left after 90 days: buckets %d, node hours %d", b, h)
	}
}

func TestSweepSkipsPolicyPrunesWhenSettingsReadFails(t *testing.T) {
	e := newEnv(t)
	now := e.clock.Now()
	day := 24 * time.Hour
	e.exec(`INSERT INTO setting (k, v) VALUES ('retention.audit_days', '1095')`)
	e.exec(`INSERT INTO audit (ts, actor, action, params, result, source) VALUES (?, 'a', 'retained', '{}', 'ok', 'panel')`, now.Add(-500*day).Unix())
	e.exec(`INSERT INTO country_check (node_id, country, family, at, verdict) VALUES ('de1', 'DE', 4, ?, 'unknown')`, now.Add(-8*day).Unix())
	if _, err := e.st.W.ExecContext(e.ctx, `DROP TABLE setting`); err != nil {
		t.Fatal(err)
	}

	e.s.sweep(e.ctx)

	var audits, countries int
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT count(*) FROM audit WHERE action = 'retained'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT count(*) FROM country_check WHERE node_id = 'de1'`).Scan(&countries); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("500-day audit row after unreadable settings: %d, want it retained", audits)
	}
	if countries != 0 {
		t.Fatalf("fixed seven-day country retention left %d rows, want it to run", countries)
	}
}
