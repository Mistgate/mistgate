//go:build !js

package fleet

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// node_live.devices names each device with a session once, at the start of its newest session (hysteria2 opens one session per
// QUIC connection, so a device may have several), and leaves out what the panel cannot tell: a credential it does not know.
func TestBuildFleetLiveNamesTheDevicesWithASession(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	state := SessionState{Version: sessionStateVersion, NodeID: "nod_devices_live", OwnerGeneration: 1}
	stats := &agentv1.StatsBatch{IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Sessions: []*agentv1.Session{
			{CredId: "crd_a1", InboundId: "inb_1", ConnectedAtUnix: now.Add(-5 * time.Minute).Unix()},
			{CredId: "crd_a1", InboundId: "inb_1", ConnectedAtUnix: now.Add(-time.Minute).Unix()},
			{CredId: "crd_a2", InboundId: "inb_1", ConnectedAtUnix: now.Add(-2 * time.Minute).Unix()},
			{CredId: "crd_unknown", InboundId: "inb_1", ConnectedAtUnix: now.Add(-3 * time.Minute).Unix()},
		}}
	refs := map[string]store.FleetCredRef{
		"crd_a1": {UserID: "usr_alice", DeviceID: "dev_alice_phone", Protocol: "hysteria2"},
		"crd_a2": {UserID: "usr_alice", DeviceID: "dev_alice_laptop", Protocol: "hysteria2"},
	}
	live, _, tooLarge := buildFleetLive(state, stats, nil, now, refs)
	if tooLarge {
		t.Fatal("a small projection is too large")
	}
	var devices, users map[string]int64
	if err := json.Unmarshal([]byte(live.DevicesJSON), &devices); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(live.UsersJSON), &users); err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 || devices["dev_alice_phone"] != now.Add(-time.Minute).Unix() || devices["dev_alice_laptop"] != now.Add(-2*time.Minute).Unix() {
		t.Fatalf("devices = %v, want the phone at its newest session and the laptop", devices)
	}
	if len(users) != 1 {
		t.Fatalf("users = %v, want alice once", users)
	}
}

// The devices count toward the 1.5 MB valve of the projection: a batch of people that fits as users alone is too large once
// every one of their devices is listed too, and the previous row is kept (warnOversizedLiveProjection).
func TestBuildFleetLiveCountsTheDevicesInTheRowLimit(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	state := SessionState{Version: sessionStateVersion, NodeID: "nod_devices_size", OwnerGeneration: 1}
	const sessions = 20_000
	stats := &agentv1.StatsBatch{IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix()}
	refs := make(map[string]store.FleetCredRef, sessions)
	for i := range sessions {
		cred := fmt.Sprintf("crd_%026d", i)
		stats.Sessions = append(stats.Sessions, &agentv1.Session{CredId: cred, InboundId: "inb_1", ConnectedAtUnix: now.Add(-time.Minute).Unix()})
		refs[cred] = store.FleetCredRef{UserID: fmt.Sprintf("usr_%026d", i), DeviceID: fmt.Sprintf("dev_%026d", i), Protocol: "hysteria2"}
	}
	live, _, tooLarge := buildFleetLive(state, stats, nil, now, refs)
	if !tooLarge || live.Apply {
		t.Fatalf("a batch of %d people with a device each: tooLarge=%v apply=%v, want too large and not applied", sessions, tooLarge, live.Apply)
	}
	if len(live.UsersJSON)+len(live.LiveJSON) != 0 {
		t.Fatal("an oversized projection carries data")
	}
	// without the devices the same batch fits: the valve above is the devices' doing
	for cred, ref := range refs {
		ref.DeviceID = ""
		refs[cred] = ref
	}
	if live, _, tooLarge = buildFleetLive(state, stats, nil, now, refs); tooLarge || !live.Apply {
		t.Fatalf("the same batch without devices: tooLarge=%v apply=%v, want it to fit", tooLarge, live.Apply)
	}
}
