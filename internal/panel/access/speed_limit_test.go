package access

import (
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func ptr64(v uint64) *uint64 { return &v }

func limitsOf(t *testing.T, f *fixture, userID string) (device, person uint64, n int) {
	t.Helper()
	for _, in := range must(f.e.s.Desired(f.e.ctx, f.nodeID)) {
		for _, c := range in.Creds {
			if c.UserID != userID {
				continue
			}
			if n == 0 {
				device, person = c.RateLimitBps, c.UserRateLimitBps
			} else if device != c.RateLimitBps || person != c.UserRateLimitBps {
				t.Fatalf("two credentials of one user disagree: %d/%d and %d/%d", device, person, c.RateLimitBps, c.UserRateLimitBps)
			}
			n++
		}
	}
	return device, person, n
}

// A user has two limits: the person limit (speed_limit_bps) and the device limit (device_speed_limit_bps). Both are
// editable, both reach the nodes, and a change of either tells the nodes.
func TestDeviceAndPersonSpeedLimits(t *testing.T) {
	f := newFixture(t)
	e := f.e
	u := e.user("quinn", f.group, func(m *adminv1.CreateUserRequest) { m.SpeedLimitBps = 8_000_000; m.DeviceSpeedLimitBps = 3_000_000 }).User
	if u.SpeedLimitBps != 8_000_000 || u.DeviceSpeedLimitBps != 3_000_000 {
		t.Fatalf("created: person %d device %d", u.SpeedLimitBps, u.DeviceSpeedLimitBps)
	}
	// A second device of the same person: each credential carries both limits.
	token := e.tokenOf(u.Id)
	fetchAs(e, token, phone(), "")
	fetchAs(e, token, tablet(), "")
	if device, person, n := limitsOf(t, f, u.Id); n != 2 || device != 3_000_000 || person != 8_000_000 {
		t.Fatalf("desired: %d credentials, device %d person %d, want 2 with 3000000 and 8000000", n, device, person)
	}

	// The device limit alone changes: the nodes are told.
	e.resetNotify()
	got := must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, DeviceSpeedLimitBps: ptr64(uint64(1_000_000))}))).Msg.User
	if got.DeviceSpeedLimitBps != 1_000_000 || got.SpeedLimitBps != 8_000_000 {
		t.Fatalf("after the device edit: person %d device %d", got.SpeedLimitBps, got.DeviceSpeedLimitBps)
	}
	if e.notify.n.Load() == 0 {
		t.Error("a new device limit did not notify the nodes")
	}
	if device, person, _ := limitsOf(t, f, u.Id); device != 1_000_000 || person != 8_000_000 {
		t.Fatalf("desired after the device edit: device %d person %d", device, person)
	}

	// The person limit alone changes: the nodes are told, the device limit stays.
	e.resetNotify()
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, SpeedLimitBps: ptr64(uint64(2_000_000))})))
	if e.notify.n.Load() == 0 {
		t.Error("a new person limit did not notify the nodes")
	}
	if device, person, _ := limitsOf(t, f, u.Id); device != 1_000_000 || person != 2_000_000 {
		t.Fatalf("desired after the person edit: device %d person %d", device, person)
	}

	// Removing both: 0 = no limit, and an edit that changes neither stays quiet.
	e.resetNotify()
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, SpeedLimitBps: ptr64(uint64(0)), DeviceSpeedLimitBps: ptr64(uint64(0))})))
	if device, person, _ := limitsOf(t, f, u.Id); device != 0 || person != 0 {
		t.Fatalf("desired after removing both: device %d person %d", device, person)
	}
	e.resetNotify()
	must(e.s.UpdateUser(e.ctx, req(&adminv1.UpdateUserRequest{UserId: u.Id, DeviceSpeedLimitBps: ptr64(uint64(0))})))
	if e.notify.n.Load() != 0 {
		t.Error("an unchanged limit notified the nodes")
	}
}
