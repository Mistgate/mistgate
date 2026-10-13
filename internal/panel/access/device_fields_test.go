package access

import (
	"encoding/hex"
	"slices"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// GetUser describes each device by itself: its kind, what it said about itself, what it carried, whether it has a session and
// on which nodes (two: the same keys in use on two servers). The page says online per device too, from node_live.devices.
func TestGetUserShowsEachDeviceWithItsOwnOnlineAndTraffic(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	r := e.userBare("alice", f.group, nil)
	token := e.tokenOf(r.User.Id)
	fetchAs(e, token, phone(), "")
	fetchAs(e, token, tablet(), "")
	fetchAs(e, token, DeviceHint{}, "")
	var phoneID, tabletID string
	for hint, dest := range map[string]*string{hwidPhone: &phoneID, hwidTablet: &tabletID} {
		if err := e.st.R.QueryRow(`SELECT id FROM device WHERE user_id = ? AND hwid_hash = ?`, r.User.Id,
			storeHash(r.User.Id, DeviceHint{HWID: CanonHWID(hint)})).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	sharedID := sharedDeviceID(e, r.User.Id)
	e.sql(`UPDATE device SET used_bytes = 12345 WHERE id = ?`, phoneID)

	e.online[r.User.Id] = "nod_de1" // the person is online ...
	e.online[phoneID] = "nod_de1,nod_nl1"
	e.online[sharedID] = "nod_de1"
	got := map[string]*adminv1.Device{}
	for _, d := range must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: r.User.Id}))).Msg.Devices {
		got[d.Id] = d
	}
	if len(got) != 3 {
		t.Fatalf("devices = %d, want 3", len(got))
	}
	p := got[phoneID]
	hash := storeHash(r.User.Id, phone())
	if p.Kind != adminv1.DeviceKind_DEVICE_KIND_LINK || !p.Online || !slices.Equal(p.OnlineNodeIds, []string{"nod_de1", "nod_nl1"}) ||
		p.OsVersion != "18.1" || p.Client != "Happ/3.4.2" || p.UsedBytes != 12345 || p.Fingerprint != hex.EncodeToString(hash[:3]) {
		t.Errorf("phone = %+v", p)
	}
	// ... but only the devices with a session are: the tablet is not, though its person is
	if tb := got[tabletID]; tb.Online || len(tb.OnlineNodeIds) != 0 || tb.Kind != adminv1.DeviceKind_DEVICE_KIND_LINK || tb.Fingerprint == "" || tb.UsedBytes != 0 {
		t.Errorf("tablet = %+v", tb)
	}
	if sh := got[sharedID]; sh.Kind != adminv1.DeviceKind_DEVICE_KIND_LINK_SHARED || !sh.Online || sh.Fingerprint != "" {
		t.Errorf("shared device = %+v", sh)
	}
	delete(e.online, sharedID)
	delete(e.online, phoneID)
	for _, d := range must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: r.User.Id}))).Msg.Devices {
		if d.Online {
			t.Errorf("%s is online with no session of its own, only because the person is: %+v", d.Id, d)
		}
	}

	// the person's page: online per device, from what the nodes reported (node_live.devices of a connected node)
	e.sql(`UPDATE node SET last_seen_at = ?, liveness_timeout_s = 90 WHERE id IN ('nod_de1', 'nod_nl1')`, e.clock.Unix())
	e.sql(`INSERT INTO node_live (node_id, session, devices) VALUES ('nod_de1', 1, ?)`, `{"`+tabletID+`":1}`)
	e.sql(`INSERT INTO node_live (node_id, session, devices) VALUES ('nod_nl1', 1, ?)`, `{"`+tabletID+`":1,"`+phoneID+`":1}`)
	e.sql(`UPDATE node SET state = 'retired' WHERE id = 'nod_nl1'`) // its sessions say nothing now
	v := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{NoTouch: true}))
	online := map[string]bool{}
	for _, d := range v.Devices {
		online[d.ID] = d.Online
	}
	if len(online) != 3 || !online[tabletID] || online[phoneID] || online[sharedID] {
		t.Errorf("page devices online = %v, want only the tablet", online)
	}
}
