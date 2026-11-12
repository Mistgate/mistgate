package subs_test

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The Mistgate app switch of a person (AppToggles.mistgate). On by default for new people and for everybody who existed before it
// (the column is "off", default 0). Off: a new enrolment is refused (409 app_disabled) and the devices already enrolled are told
// the status and the reason, with no server; their keys are kept, the nodes stop taking them, and everything comes back when it is
// switched on.

func (e *syncEnv) setApps(userID string, apps *adminv1.AppToggles) {
	e.t.Helper()
	if _, err := e.svc.UpdateUser(e.ctx, connect.NewRequest(&adminv1.UpdateUserRequest{UserId: userID, Apps: apps})); err != nil {
		e.t.Fatal(err)
	}
}

// credsOnNode is how many credentials of the person's devices the node would be told about.
func (e *syncEnv) credsOnNode(userID string) int {
	e.t.Helper()
	n := 0
	for _, in := range must(e.svc.Desired(e.ctx, "nod_1")) {
		for _, c := range in.Creds {
			if c.UserID == userID {
				n++
			}
		}
	}
	return n
}

func TestMistgateAppIsOnByDefault(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", nil)
	users := must(e.svc.ListUsers(e.ctx, connect.NewRequest(&adminv1.ListUsersRequest{}))).Msg.Users
	if len(users) != 1 || users[0].GetApps().Mistgate == nil || !users[0].GetApps().GetMistgate() {
		t.Fatalf("a new person's apps = %v, want the Mistgate app on", users[0].GetApps())
	}
	a := e.enrolDevice(tok, hwidA)
	if b := e.mustSync(a.Key); b.Status != "active" || len(b.Servers) == 0 || b.Reason != "" {
		t.Fatalf("sync of a person with the app on: %s", b.raw)
	}
	// an update that does not name the switch (an older admin page) leaves it as it is: on, and off if it was off
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: true})
	if got := e.userApps(uid); !got.GetMistgate() {
		t.Fatalf("an update without the switch turned it off: %v", got)
	}
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: true, Mistgate: new(false)})
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: false})
	if got := e.userApps(uid); got.GetMistgate() || got.GetAmnezia() {
		t.Fatalf("an update without the switch changed it: %v", got)
	}
}

func (e *syncEnv) userApps(userID string) *adminv1.AppToggles {
	e.t.Helper()
	for _, u := range must(e.svc.ListUsers(e.ctx, connect.NewRequest(&adminv1.ListUsersRequest{}))).Msg.Users {
		if u.Id == userID {
			return u.Apps
		}
	}
	e.t.Fatalf("no user %s", userID)
	return nil
}

func TestMistgateAppOffRefusesEnrolment(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Happ: true, Amnezia: true, Mistgate: new(false)}
	})
	for range 2 { // the refusal gives the unit of the budget back: it can be asked again
		rec := e.post(tok, enrolBody(hwidA))
		if rec.Code != 409 || errorCode(t, rec) != "app_disabled" {
			t.Fatalf("enrol with the app off = %d %q, want 409 app_disabled", rec.Code, rec.Body.String())
		}
	}
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ?`, uid); n != 0 {
		t.Fatalf("a device was made for a person with the app off: %d", n)
	}
	// switched on, the same request goes through
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: true, Mistgate: new(true)})
	e.enrolOK(tok, hwidA)
	// the subscription link itself is a different app and is not touched by this switch
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: true, Mistgate: new(false)})
	if rec := fetch(e.h, "/"+tok, happUA, "X-HWID", hwidB); rec.Code != 200 {
		t.Fatalf("the plain link of a person with the Mistgate app off = %d", rec.Code)
	}
}

// The app needs the subscription link: with the link app off the enrolment is refused whatever the switch says.
func TestMistgateAppNeedsTheSubscriptionLink(t *testing.T) {
	e := newSyncEnv(t, nil)
	_, tok := e.person("bob", func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Happ: false, Amnezia: true, Mistgate: new(true)}
	})
	if rec := e.post(tok, enrolBody(hwidA)); rec.Code != 409 || errorCode(t, rec) != "app_disabled" {
		t.Fatalf("enrol with the link off = %d %q, want 409 app_disabled", rec.Code, rec.Body.String())
	}
}

func TestMistgateAppOffLeavesEnrolledDevicesWithoutServers(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	on := e.mustSync(a.Key)
	if len(on.Servers) == 0 || len(on.paths("awg")) != 1 {
		t.Fatalf("sync with the app on: %s", on.raw)
	}
	creds := e.liveCredIDs(a.DeviceID)
	if e.credsOnNode(uid) == 0 {
		t.Fatal("the node is told nothing about a device of a person with the app on")
	}

	notified := e.state.n.Load()
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: true, Mistgate: new(false)})
	if e.state.n.Load() == notified {
		t.Error("the nodes were not told that the app is off")
	}
	rec := e.sync(a.Key)
	if rec.Code != 200 {
		t.Fatalf("sync with the app off = %d %s", rec.Code, rec.Body.String())
	}
	off := decodeSync(t, rec)
	if off.Status != "active" || off.Reason != "app_disabled" || off.Servers == nil || len(off.Servers) != 0 || off.DNS != nil || off.Plan == nil {
		t.Errorf("sync with the app off: %s", off.raw)
	}
	if off.etg == on.etg {
		t.Error("the ETag did not change with the switch")
	}
	// kept, not revoked: the device stays, its credentials stay, and the node no longer takes them
	if e.liveCredIDs(a.DeviceID) != creds {
		t.Error("switching the app off changed the device's credentials")
	}
	if n := e.count(`SELECT count(*) FROM device WHERE id = ? AND revoked_at IS NULL`, a.DeviceID); n != 1 {
		t.Error("the device was removed")
	}
	if n := e.credsOnNode(uid); n != 0 {
		t.Errorf("the node is told about %d credentials of a person with the app off", n)
	}
	// the owner's global switch is not the person's: closing it changes nothing for a connected device, as before
	e.switchAPI(false)
	if rec := e.sync(a.Key); rec.Code != 200 {
		t.Fatalf("sync with the global switch off = %d", rec.Code)
	}
	e.switchAPI(true)

	// back on: the same device, the same credentials, the servers again
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: true, Mistgate: new(true)})
	back := e.mustSync(a.Key)
	if back.Reason != "" || back.raw != on.raw || e.liveCredIDs(a.DeviceID) != creds {
		t.Errorf("sync after switching back on: %s", back.raw)
	}
	if e.credsOnNode(uid) == 0 {
		t.Error("the node did not take the credentials back")
	}
}

// A person that is not active gets the status and no reason: the status is what explains it.
func TestMistgateAppOffReasonIsOnlyForAnActivePerson(t *testing.T) {
	e := newSyncEnv(t, nil)
	uid, tok := e.person("alice", nil)
	a := e.enrolDevice(tok, hwidA)
	e.setApps(uid, &adminv1.AppToggles{Happ: true, Amnezia: true, Mistgate: new(false)})
	if _, err := e.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, uid); err != nil {
		t.Fatal(err)
	}
	if b := e.mustSync(a.Key); b.Status != "disabled" || b.Reason != "" || len(b.Servers) != 0 {
		t.Errorf("sync of a disabled person with the app off: %s", b.raw)
	}
}
