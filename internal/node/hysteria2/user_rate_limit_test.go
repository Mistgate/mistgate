package hysteria2

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

func person(userID string, bps uint64) func(*plugin.UserCred) {
	return func(c *plugin.UserCred) { c.UserID, c.UserRateLimitBps = userID, bps }
}

func personBucket(t *testing.T, r *rig, credID string) any {
	t.Helper()
	in := r.e.(*eng).inbounds["inb_1"]
	cs := in.idx.Load().byID[credID]
	if cs == nil {
		t.Fatalf("no credential %s", credID)
	}
	if l := cs.person.Load(); l != nil {
		return l
	}
	return nil
}

// Two devices of one person draw on one bucket, devices of another person on another; the bucket follows the value, is
// dropped with the last credential of the person, and the inbound frees whatever it still holds.
func TestPersonRateLimitBucketLifecycle(t *testing.T) {
	r := newRig(t, "")
	if !r.e.Capabilities().RateLimitPerUser {
		t.Fatal("the capability must be advertised")
	}
	users := r.e.(*eng).users
	r.apply(cred("a1", person("usr_a", 800_000)), cred("a2", person("usr_a", 800_000)), cred("b1", person("usr_b", 800_000)), cred("c1"))
	a1, a2, b1, c1 := personBucket(t, r, "crd_a1"), personBucket(t, r, "crd_a2"), personBucket(t, r, "crd_b1"), personBucket(t, r, "crd_c1")
	if a1 == nil || a1 != a2 {
		t.Fatalf("two devices of one person must share a bucket: %v %v", a1, a2)
	}
	if b1 == nil || b1 == a1 {
		t.Fatalf("another person must have a bucket of their own: %v", b1)
	}
	if c1 != nil {
		t.Fatal("a credential without a person limit must not get one")
	}

	// A new value reaches the live bucket; the credentials keep the same one.
	r.apply(cred("a1", person("usr_a", 1_600_000)), cred("a2", person("usr_a", 1_600_000)), cred("b1", person("usr_b", 800_000)))
	if personBucket(t, r, "crd_a1") != a1 || users.Limiter("usr_a").BPS() != 1_600_000 {
		t.Fatalf("the bucket must follow the new value: bps %d", users.Limiter("usr_a").BPS())
	}
	// A device leaves: the person keeps the bucket. The last one leaves: it is dropped. The limit removed: the same.
	r.apply(cred("a1", person("usr_a", 1_600_000)), cred("b1"))
	if users.Limiter("usr_a") == nil || users.Limiter("usr_b") != nil || personBucket(t, r, "crd_b1") != nil {
		t.Fatalf("after b1 lost its limit: a=%v b=%v", users.Limiter("usr_a"), users.Limiter("usr_b"))
	}
	r.apply(cred("b1"))
	if users.Limiter("usr_a") != nil {
		t.Fatal("the last credential of the person is gone: the bucket must be dropped")
	}
	r.apply(cred("a1", person("usr_a", 800_000)))
	if users.Limiter("usr_a") == nil {
		t.Fatal("the bucket is missing")
	}
	if err := r.e.Remove(context.Background(), "inb_1"); err != nil {
		t.Fatal(err)
	}
	if users.Limiter("usr_a") != nil {
		t.Fatal("removing the inbound must free its buckets")
	}
}

// The traffic of two devices of one person is held to the person limit together (a lower bound: the shaping can only
// make it slower, so the check cannot flake on a slow machine).
func TestPersonRateLimitSharedByDevices(t *testing.T) {
	r := newRig(t, "")
	// 800 kbit/s = 100 kB/s per direction for the person, a burst of one second (100 kB).
	r.apply(cred("a1", person("usr_a", 800_000)), cred("a2", person("usr_a", 800_000)))
	const size = 150_000
	start := time.Now()
	var wg sync.WaitGroup
	for _, id := range []string{"a1", "a2"} {
		conn, err := r.mustDial(id).TCP(r.echo)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(60 * time.Second))
		wg.Add(1)
		go func() {
			defer wg.Done()
			go conn.Write(payload(size))
			if _, err := io.ReadFull(conn, make([]byte, size)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	// 300 kB through one 100 kB/s bucket with a 100 kB burst: 2 s. Two buckets of their own would need 0.5 s.
	if took := time.Since(start); took < 1500*time.Millisecond {
		t.Fatalf("two devices of one person moved %d bytes each in %v: the person limit is not shared", size, took)
	}
}
