package statehash

import (
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
)

// The person limit joins a credential's line only when it is set: every state without one hashes as before the field
// existed (goldenCreds), and a state with one hashes differently for each value.
func TestPersonRateLimitJoinsTheHashOnlyWhenSet(t *testing.T) {
	in := goldenInbound()
	if got := Creds(in.Creds); got != goldenCreds {
		t.Fatalf("Creds = %s, want the golden %s", got, goldenCreds)
	}
	with := append([]plugin.UserCred(nil), in.Creds...)
	with[0].UserRateLimitBps = 5_000_000
	other := append([]plugin.UserCred(nil), in.Creds...)
	other[0].UserRateLimitBps = 6_000_000
	a, b := Creds(with), Creds(other)
	if a == goldenCreds || b == goldenCreds || a == b {
		t.Fatalf("the person limit must change the hash: none %s, 5M %s, 6M %s", goldenCreds, a, b)
	}
	// The device limit is a different field: swapping the two values must not collide.
	swapped := append([]plugin.UserCred(nil), in.Creds...)
	swapped[0].RateLimitBps, swapped[0].UserRateLimitBps = 5_000_000, 0
	with[0].RateLimitBps = 0
	if Creds(swapped) == Creds(with) {
		t.Fatal("a device limit and a person limit of the same value hash alike")
	}
}
