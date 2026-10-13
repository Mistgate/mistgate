//go:build !js

package store

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestHealthSignals(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Date(2026, 10, 7, 12, 40, 0, 0, time.UTC)
	seedHealthSignals(t, s, now)

	got, err := s.HealthSignals(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Users) != 3 || len(got.AWGDevices) != 1 || len(got.NodeHours) != 8 {
		t.Fatalf("signals = users %d, AWG devices %d, node hours %d", len(got.Users), len(got.AWGDevices), len(got.NodeHours))
	}
	users := map[string]HealthUserSignal{}
	for _, user := range got.Users {
		users[user.ID] = user
	}
	if user := users["usr_expired"]; user.Status != "expired" || !user.SubscriptionFetchAt.Equal(now.Add(-time.Hour)) || !user.LastTrafficAt.Equal(now.Add(-4*24*time.Hour)) {
		t.Fatalf("expired user signal = %+v", user)
	}
	if user := users["usr_limited"]; user.Status != "limited" || !user.SubscriptionFetchAt.Equal(now.Add(-10*time.Minute)) {
		t.Fatalf("limited user signal = %+v", user)
	}
	device := got.AWGDevices[0]
	if device.DeviceID != "dev_active_awg" || device.Status != "active" || device.ConfigEpoch != 1 || device.CriticalEpoch != 2 ||
		!device.CreatedAt.Equal(now.Add(-3*24*time.Hour)) || !device.LastHandshakeAt.Equal(now.Add(-2*time.Hour)) {
		t.Fatalf("AWG signal = %+v", device)
	}
	for _, device := range got.AWGDevices {
		if device.DeviceID == "dev_active_implicit" {
			t.Fatal("implicit device with an AWG credential appeared in AWG signals")
		}
	}
	foundCurrentHour := false
	for _, hour := range got.NodeHours {
		foundCurrentHour = foundCurrentHour || (hour.NodeID == "nod_signals" && hour.Protocol == "hysteria2" && hour.Hour == now.UTC().Truncate(time.Hour).Unix())
	}
	if !foundCurrentHour {
		t.Fatalf("current node hour was not returned: %+v", got.NodeHours)
	}
}

func TestHealthSignalsTorrentAttemptsPerUserAndNode(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Date(2026, 10, 7, 12, 40, 0, 0, time.UTC)
	seedHealthSignals(t, s, now)
	seedTorrentEvents(t, s, now)

	got, err := s.HealthSignals(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Torrents) != 1 {
		t.Fatalf("torrent signals = %+v, want only evidence-qualified usr_active event on nod_signals", got.Torrents)
	}
	byNode := map[string]HealthTorrentSignal{}
	for _, signal := range got.Torrents {
		byNode[signal.NodeID] = signal
	}
	signal := byNode["nod_signals"]
	slices.Sort(signal.DstPorts)
	if signal.UserID != "usr_active" || signal.Count != 2 || !signal.LastAt.Equal(now.Add(-time.Hour)) ||
		signal.Evidence != "tracker_connect" || !slices.Equal(signal.DstPorts, []string{"6881", "6969"}) {
		t.Fatalf("nod_signals torrent signal = %+v", signal)
	}
	if _, ok := byNode["nod_other"]; ok {
		t.Fatalf("events without evidence fed the people signal: %+v", byNode["nod_other"])
	}
}

func TestHealthSignalsTorrentQueryUsesTheSeverityTimeIndex(t *testing.T) {
	s := openTemp(t)
	rows, err := s.R.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+healthTorrentSQL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "; "
	}
	t.Log(plan)
	if !strings.Contains(plan, "USING INDEX event_retention") && !strings.Contains(plan, "USING COVERING INDEX event_retention") {
		t.Fatalf("torrent signal scans the whole event table: %s", plan)
	}
}

func TestHealthSignalsDeviceLimitQueryUsesItsIndex(t *testing.T) {
	s := openTemp(t)
	rows, err := s.R.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+healthDeviceLimitSQL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "; "
	}
	if !strings.Contains(plan, "event_device_limit") {
		t.Fatalf("device-limit signal does not use event_device_limit: %s", plan)
	}
}

// Only the events that turned an app away are signals; the day is a window, and the last refusal tells used and limit.
func TestHealthSignalsDeviceLimitCountsRefusalsOnly(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	add := func(user string, age time.Duration, params string) {
		t.Helper()
		if _, err := s.W.ExecContext(ctx, `INSERT INTO event (ts, severity, code, source, user_id, params_json) VALUES (?, 1, 'device_limit_reached', 'panel', ?, ?)`,
			now.Add(-age).Unix(), user, params); err != nil {
			t.Fatal(err)
		}
	}
	add("usr_alice", 5*time.Hour, `{"used":"5","limit":"5","refused":"1","mode":"enforce"}`)
	add("usr_alice", time.Hour, `{"used":"6","limit":"5","refused":"1","mode":"strict"}`)
	add("usr_alice", 30*time.Minute, `{"used":"7","limit":"5","refused":"0","mode":"track"}`)
	add("usr_bob", time.Hour, `{"used":"7","limit":"5","refused":"0","mode":"track"}`)
	add("usr_carol", time.Hour, `{"used":"3","limit":"3"}`)
	add("usr_erin", 25*time.Hour, `{"used":"3","limit":"3","refused":"1"}`)
	batch, err := s.HealthSignals(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []HealthDeviceLimitSignal{{UserID: "usr_alice", Count: 2, LastAt: now.Add(-time.Hour), Used: 6, Limit: 5}}
	if !reflect.DeepEqual(batch.DeviceLimits, want) {
		t.Fatalf("device limit signals = %+v, want %+v", batch.DeviceLimits, want)
	}
}
