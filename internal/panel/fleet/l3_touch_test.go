package fleet

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// An AWG peer with a session moves the device's last_seen_at to its newest handshake; the page and the admin show it
// as "last handshake" and draw the online dot from it. Found by scripts/e2e-wsl.sh --awg: a device that carried
// traffic for minutes showed no handshake and was never online. Other protocols and older times change nothing.
func TestAwgSessionMovesTheDevicesLastSeenForward(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	e.exec(`UPDATE device_credential SET protocol = 'awg' WHERE id = 'crd_alice_wg'`)
	var awgDevice, hyDevice string
	e.st.R.QueryRow(`SELECT device_id FROM device_credential WHERE id = 'crd_alice_wg'`).Scan(&awgDevice)
	e.st.R.QueryRow(`SELECT device_id FROM device_credential WHERE id = 'crd_erin_hy'`).Scan(&hyDevice)
	if awgDevice == "" || hyDevice == "" || awgDevice == hyDevice {
		t.Fatalf("fixture: devices %q %q", awgDevice, hyDevice)
	}
	seen := func(dev string) (v int64) {
		e.st.R.QueryRow(`SELECT last_seen_at FROM device WHERE id = ?`, dev).Scan(&v)
		return
	}
	e.exec(`UPDATE device SET last_seen_at = 0`) // a device that never connected
	hyBefore := seen(hyDevice)
	c, _, _ := connectFull(a, "inst1")
	now := time.Now().Unix()
	send := func(seq uint64, awgAt int64) {
		c.send(seq, statsBatch(now-10, now, nil, []*agentv1.Session{
			{CredId: "crd_alice_wg", InboundId: ids.i2, ConnectedAtUnix: awgAt},
			{CredId: "crd_erin_hy", InboundId: ids.i1, ConnectedAtUnix: now - 30},
		}))
		c.ack()
	}
	send(1, now-60)
	if got := seen(awgDevice); got != now-60 {
		t.Errorf("the awg device's last_seen_at = %d, want the handshake time %d", got, now-60)
	}
	if got := seen(hyDevice); got != hyBefore {
		t.Errorf("a hysteria2 session moved its device's last_seen_at: %d -> %d", hyBefore, got)
	}
	send(2, now-200) // an older handshake (a batch resent late) never moves it back
	if got := seen(awgDevice); got != now-60 {
		t.Errorf("an older handshake moved last_seen_at back to %d", got)
	}
	send(3, now+3600) // a clock from the future is clamped to this clock
	if got := seen(awgDevice); got < now-60 || got > time.Now().Unix()+1 {
		t.Errorf("a handshake from the future was stored as %d", got)
	}
}

// A session of any protocol is a use of its device: it moves last_connected_at, which the idle rule reads, and leaves
// last_seen_at alone for a link device (that is its last subscription fetch; the page, the admin and the health check mean
// it so). An app that tunnels daily but never refetches its subscription therefore never goes idle.
func TestAnySessionMovesTheDevicesLastConnectedNotItsLastFetch(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	ids := e.fixture(a.nodeID)
	var hyDevice, hyUser string
	e.st.R.QueryRow(`SELECT device_id, user_id FROM device_credential WHERE id = 'crd_erin_hy'`).Scan(&hyDevice, &hyUser)
	if hyDevice == "" {
		t.Fatal("fixture: the hysteria2 credential has no device")
	}
	old := time.Now().Add(-40 * 24 * time.Hour).Unix()
	e.exec(`UPDATE device SET last_seen_at = ?, created_at = ? WHERE id = ?`, old, old, hyDevice)
	idle := func() bool {
		counts, err := e.st.Access().DeviceCounts(context.Background(), []string{hyUser}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return counts[hyUser].Idle > 0
	}
	if !idle() {
		t.Fatal("fixture: a device last fetched 40 days ago is not idle")
	}
	c, _, _ := connectFull(a, "inst1")
	now := time.Now().Unix()
	c.send(1, statsBatch(now-10, now, nil, []*agentv1.Session{{CredId: "crd_erin_hy", InboundId: ids.i1, ConnectedAtUnix: now - 30}}))
	c.ack()
	var seen, connected int64
	e.st.R.QueryRow(`SELECT last_seen_at, last_connected_at FROM device WHERE id = ?`, hyDevice).Scan(&seen, &connected)
	if seen != old || connected != now-30 {
		t.Errorf("after a hysteria2 session last_seen_at = %d (want the old fetch %d), last_connected_at = %d (want %d)", seen, old, connected, now-30)
	}
	if idle() {
		t.Error("a device with a session a moment ago is still idle")
	}
}
