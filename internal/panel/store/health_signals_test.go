//go:build !js

package store

import (
	"context"
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
