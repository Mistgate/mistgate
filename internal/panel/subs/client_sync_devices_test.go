package subs_test

import (
	"fmt"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// devices: {"used": N, "limit": M}, the person's own numbers: the count enrol answers device_limit by, and the limit (0 = none, as
// quota_bytes 0 is no quota). Nothing of any device.

const (
	hwidC3 = "3c4d5e6f-7a8b-4c9d-8e0f-1a2b3c4d5e6f"
	hwidD4 = "4d5e6f7a-8b9c-4d0e-8f1a-2b3c4d5e6f7a"
)

func devicesOf(t *testing.T, b syncBody) string {
	t.Helper()
	i := strings.Index(b.raw, `"devices":`)
	if i < 0 {
		t.Fatalf("no devices in the answer: %s", b.raw)
	}
	j := strings.Index(b.raw[i:], "}")
	return b.raw[i : i+j+1]
}

func TestSyncDevicesAreCountedAsEnrolCounts(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("alice", func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 2 })
	_, bobTok := e.person("bob", nil)
	e.enrolDevice(bobTok, hwidD4) // another person's device is nobody's business here

	a := e.enrolDevice(tok, hwidA)
	one := e.mustSync(a.Key)
	if got := devicesOf(t, one); got != `"devices":{"used":1,"limit":2}` {
		t.Errorf("one device of two: %s", got)
	}
	b := e.enrolDevice(tok, hwidB2)
	two := e.mustSync(a.Key)
	if got := devicesOf(t, two); got != `"devices":{"used":2,"limit":2}` {
		t.Errorf("two devices of two: %s", got)
	}
	if two.etg == one.etg {
		t.Error("the ETag did not follow a device being added")
	}

	// Full is what enrol says no by: the numbers and the answer of enrol agree.
	if rec := e.post(tok, enrolBody(hwidC3)); rec.Code != 409 || errorCode(t, rec) != "device_limit" {
		t.Fatalf("a third device = %d %s, want 409 device_limit", rec.Code, rec.Body.String())
	}
	// Nothing of the other device, nor of the other person's, is in the answer.
	for _, forbidden := range []string{b.DeviceID, b.Key, hwidB2, hwidD4, "devices\":[", "device_ids"} {
		if strings.Contains(two.raw, forbidden) {
			t.Errorf("the body holds %q", forbidden)
		}
	}

	// A device leaves: the place is free again and enrol takes one.
	if rec := e.leave(b.Key); rec.Code != 200 {
		t.Fatalf("leave = %d", rec.Code)
	}
	if got := devicesOf(t, e.mustSync(a.Key)); got != `"devices":{"used":1,"limit":2}` {
		t.Errorf("after a device left: %s", got)
	}
	e.enrolDevice(tok, hwidC3)
	if got := devicesOf(t, e.mustSync(a.Key)); got != `"devices":{"used":2,"limit":2}` {
		t.Errorf("after another came: %s", got)
	}
}

// The Telegram key is listed among a person's devices but fills no place.
func TestSyncDevicesTelegramKeyFillsNoPlace(t *testing.T) {
	e := newSyncEnv(t, nil)
	group, _ := e.extras()
	_, tok := e.personIn("alice", group, true, true) // Telegram on: the person holds a key device
	a := e.enrolDevice(tok, hwidA)
	if got := devicesOf(t, e.mustSync(a.Key)); got != `"devices":{"used":1,"limit":5}` {
		t.Errorf("a device and the Telegram key: %s", got)
	}
}

// The numbers are there wherever the plan is: for a person without access too, and with the app switched off.
func TestSyncDevicesWhereThePlanIs(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 3 })
	a := e.enrolDevice(tok, hwidA)
	want := fmt.Sprintf(`"devices":{"used":1,"limit":%d}`, 3)
	if _, err := e.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	if b := e.mustSync(a.Key); b.Status != "disabled" || devicesOf(t, b) != want {
		t.Errorf("disabled: %s", b.raw)
	}
	if _, err := e.st.W.Exec(`UPDATE user SET disabled = 0 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: true, Mistgate: new(false)})
	if b := e.mustSync(a.Key); b.Reason != "app_disabled" || devicesOf(t, b) != want {
		t.Errorf("app off: %s", b.raw)
	}
}
