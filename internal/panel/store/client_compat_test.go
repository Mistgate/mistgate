//go:build !js

package store

import (
	"context"
	"testing"
)

// ClientCompatDevices: live link devices holding a VLESS credential, nothing else.
func TestClientCompatDevicesAreLiveLinkDevicesWithAVlessCredential(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	rewriteAccessGroup(t, s, "grp_link", "Link devices")
	rewriteAccessUser(t, s, "usr_alice", "alice", "grp_link")
	rewriteAccessUser(t, s, "usr_bob", "bob", "grp_link")
	setLinkMode(t, s, LinkModeEnforce)
	a := s.Access()
	for _, d := range []struct{ user, tag, hwid string }{{"usr_alice", "a1", "aaaaaaaaaaaaaaaa"}, {"usr_alice", "a2", "bbbbbbbbbbbbbbbb"}, {"usr_alice", "a3", "cccccccccccccccc"}, {"usr_bob", "b1", "dddddddddddddddd"}} {
		if _, err := a.EnsureLinkDevice(ctx, linkDevice(d.user, d.tag, HWIDHash(d.user, d.hwid)), 10, linkNow, linkCreds(d.tag, d.user)); err != nil {
			t.Fatal(err)
		}
	}
	// a2 lost its VLESS credential (the person's group no longer holds one), a3 is blocked
	if _, err := s.W.ExecContext(ctx, `UPDATE device_credential SET revoked_at = 1 WHERE device_id = 'dev_a2' AND protocol = 'vless'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.W.ExecContext(ctx, `UPDATE device SET blocked_at = 1 WHERE id = 'dev_a3'`); err != nil {
		t.Fatal(err)
	}
	all, err := a.ClientCompatDevices(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "dev_a1" || all[1].ID != "dev_b1" {
		t.Fatalf("devices = %+v", all)
	}
	if d := all[0]; d.UserID != "usr_alice" || d.Platform != "ios" || d.Client != "Happ/3.4.2" || d.Shared || d.LastSeenAt.IsZero() {
		t.Errorf("device = %+v", d)
	}
	if one, err := a.ClientCompatDevices(ctx, "usr_bob"); err != nil || len(one) != 1 || one[0].ID != "dev_b1" {
		t.Errorf("one person: %+v, %v", one, err)
	}
	if none, err := a.ClientCompatDevices(ctx, "usr_nobody"); err != nil || len(none) != 0 {
		t.Errorf("unknown person: %+v, %v", none, err)
	}
	// a revoked device is gone
	if _, err := a.RevokeDevice(ctx, "dev_b1", linkNow); err != nil {
		t.Fatal(err)
	}
	if all, _ := a.ClientCompatDevices(ctx, ""); len(all) != 1 {
		t.Errorf("after a revoke: %+v", all)
	}
}
