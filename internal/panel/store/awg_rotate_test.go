//go:build !js

package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Each rotation leaves the device's previous peer quarantined at the same index; the next rotation must still be able to
// reuse the index (the quarantine keeps it from other devices, not from its own).
func TestRotateAWGDeviceTwiceWithinQuarantine(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	profile, _ := awgNet(t, s, "rotate_twice")
	execT(t, s, `INSERT INTO user_group (id, name, created_at) VALUES ('grp_rt', 'g', 1)`)
	execT(t, s, `INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at) VALUES ('usr_rt', 'u', 'grp_rt', 1, x'03', x'04', 1)`)
	now := time.Unix(1000, 0)
	n := 0
	issue := func(int) (AccessCred, string, error) {
		n++
		id := fmt.Sprintf("crd_rt_%d", n)
		return AccessCred{ID: id, Protocol: "awg", SecretEnc: []byte{1}, DataJSON: `{}`}, "pub_" + id, nil
	}
	idx, err := s.Access().AddAWGDevice(ctx, AWGDeviceAdd{Device: AccessDevice{ID: "dev_rt", UserID: "usr_rt"}, ProfileID: profile, MaxIdx: 8, Limit: 5}, now, issue)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		if _, err := s.Access().RotateAWGDevice(ctx, "dev_rt", now.Add(time.Duration(i)*time.Minute), issue); err != nil {
			t.Fatalf("rotation %d: %v", i, err)
		}
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE profile_id = ? AND idx = ? AND released_at = 0`, profile, idx); got != 1 {
		t.Fatalf("live peers at the device's index %d = %d, want 1", idx, got)
	}
	if got := countT(t, s, `SELECT count(*) FROM awg_peer WHERE profile_id = ? AND idx = ? AND released_at > 0`, profile, idx); got != 3 {
		t.Fatalf("quarantined peers of the device's earlier keys = %d, want 3", got)
	}
}
