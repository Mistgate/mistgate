package ratelimit

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock turns a wait into a jump of virtual time, so a test measures how long the traffic would have taken
// without sleeping a real second.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0)} }

func (c *fakeClock) clock() clock {
	return clock{
		now: func() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t },
		sleep: func(ctx context.Context, d time.Duration) bool {
			if ctx.Err() != nil {
				return false
			}
			c.mu.Lock()
			c.t = c.t.Add(d)
			c.mu.Unlock()
			return true
		},
	}
}

func (c *fakeClock) since(t0 time.Time) time.Duration { return c.clock().now().Sub(t0) }

const mbps8 = 8_000_000 // 1 MB/s in bits per second; the burst is max(1 MB, 64 KiB) = 1 MB

// approx fails when got is not within 2% of want.
func approx(t *testing.T, what string, got, want time.Duration) {
	t.Helper()
	d := got - want
	if d < 0 {
		d = -d
	}
	if d > want/50+time.Millisecond {
		t.Fatalf("%s: took %v, want about %v", what, got, want)
	}
}

// push sends total bytes in chunks through the pair and returns the virtual time it took.
func push(t *testing.T, fc *fakeClock, device, person *Limiter, total, chunk uint64) time.Duration {
	t.Helper()
	t0 := fc.clock().now()
	for sent := uint64(0); sent < total; sent += chunk {
		if !WaitUp(context.Background(), device, person, chunk) {
			t.Fatal("wait ended")
		}
	}
	return fc.since(t0)
}

func TestDeviceLimitAlone(t *testing.T) {
	fc := newFakeClock()
	dev := newWithClock(mbps8, fc.clock())
	// 5 MB at 1 MB/s with 1 MB of burst: 4 s.
	approx(t, "device alone", push(t, fc, dev, nil, 5_000_000, 10_000), 4*time.Second)
}

func TestPersonLimitIsSharedByTwoDevices(t *testing.T) {
	fc := newFakeClock()
	reg := newRegistry(fc.clock())
	shared := reg.Sync("in1", []Holder{{"c1", "alice", mbps8}, {"c2", "alice", mbps8}})
	if shared["c1"] == nil || shared["c1"] != shared["c2"] {
		t.Fatalf("two devices of one person must get one bucket: %v", shared)
	}
	// Two devices alternate, 3 MB each, 6 MB in all through a 1 MB/s person bucket with 1 MB of burst: 5 s,
	// the same as one device sending all 6 MB, not 2.5 s as if each device had its own bucket.
	t0 := fc.clock().now()
	for sent := 0; sent < 3_000_000; sent += 10_000 {
		for _, c := range []string{"c1", "c2"} {
			if !WaitUp(context.Background(), nil, shared[c], 10_000) {
				t.Fatal("wait ended")
			}
		}
	}
	approx(t, "two devices of one person", fc.since(t0), 5*time.Second)
}

func TestDevicesOfDifferentPeopleDoNotShare(t *testing.T) {
	fc := newFakeClock()
	reg := newRegistry(fc.clock())
	shared := reg.Sync("in1", []Holder{{"c1", "alice", mbps8}, {"c2", "bob", mbps8}})
	if shared["c1"] == shared["c2"] || shared["c1"] == nil || shared["c2"] == nil {
		t.Fatalf("two people must get two buckets: %v", shared)
	}
	// Alice drains her bucket (1 MB burst) and then has to wait; Bob has a full bucket and passes at once.
	if d := push(t, fc, nil, shared["c1"], 1_000_000, 10_000); d != 0 {
		t.Fatalf("the burst should pass without a wait, took %v", d)
	}
	if d := push(t, fc, nil, shared["c1"], 1_000_000, 10_000); d < 900*time.Millisecond {
		t.Fatalf("alice past her burst must wait, took %v", d)
	}
	if d := push(t, fc, nil, shared["c2"], 1_000_000, 10_000); d != 0 {
		t.Fatalf("bob bucket must be untouched by alice, took %v", d)
	}
}

func TestByteWaitsForBothBuckets(t *testing.T) {
	for _, tc := range []struct {
		name           string
		device, person uint64
		want           time.Duration
	}{
		// 3 MB with a 1 MB burst leaves 2 MB to wait for: the slower bucket decides.
		{"person is the slower", 2 * mbps8, mbps8, 2 * time.Second},
		{"device is the slower", mbps8, 2 * mbps8, 2 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFakeClock()
			reg := newRegistry(fc.clock())
			person := reg.Sync("in1", []Holder{{"c1", "alice", tc.person}})["c1"]
			dev := newWithClock(tc.device, fc.clock())
			got := push(t, fc, dev, person, 3_000_000, 10_000)
			// Two buckets in sequence cost a little more than the slower one alone (the faster one is waited on first),
			// never less.
			if got < tc.want-tc.want/50 || got > tc.want+tc.want/4 {
				t.Fatalf("took %v, want about %v", got, tc.want)
			}
		})
	}
}

func TestChunkBiggerThanTheBurstIsChargedInFull(t *testing.T) {
	fc := newFakeClock()
	l := newWithClock(8*minBurst, fc.clock()) // 64 KiB/s, burst 64 KiB
	t0 := fc.clock().now()
	if !l.WaitUp(context.Background(), 10*minBurst) {
		t.Fatal("an oversized chunk must be charged in parts, not refused")
	}
	// the first 64 KiB are the full bucket, the other 576 KiB take 9 s at 64 KiB/s
	if got := fc.since(t0); got < 9*time.Second-time.Millisecond {
		t.Fatalf("640 KiB at 64 KiB/s passed in %v, want about 9 s", got)
	}
}

func TestCancelledWaitFails(t *testing.T) {
	fc := newFakeClock()
	l := newWithClock(8*minBurst, fc.clock())
	l.WaitUp(context.Background(), minBurst) // empty the bucket
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l.WaitUp(ctx, minBurst) {
		t.Fatal("a cancelled context must end the wait")
	}
}

func TestChangingThePersonLimitReachesTheLiveBucket(t *testing.T) {
	fc := newFakeClock()
	reg := newRegistry(fc.clock())
	person := reg.Sync("in1", []Holder{{"c1", "alice", mbps8}})["c1"]
	push(t, fc, nil, person, 1_000_000, 10_000) // drain the burst
	approx(t, "at 1 MB/s", push(t, fc, nil, person, 2_000_000, 10_000), 2*time.Second)

	again := reg.Sync("in1", []Holder{{"c1", "alice", 2 * mbps8}})["c1"]
	if again != person {
		t.Fatal("a change of the value must keep the bucket, the engines hold the pointer")
	}
	if person.BPS() != 2*mbps8 {
		t.Fatalf("bps = %d", person.BPS())
	}
	push(t, fc, nil, person, 2_000_000, 10_000) // drain what the larger burst refilled
	approx(t, "at 2 MB/s", push(t, fc, nil, person, 4_000_000, 10_000), 2*time.Second)
}

func TestRegistryLifecycle(t *testing.T) {
	reg := NewRegistry()
	if reg.Limiter("alice") != nil {
		t.Fatal("no bucket before a limited credential arrives")
	}
	// A credential without a person limit holds nothing.
	if got := reg.Sync("in1", []Holder{{"c1", "alice", 0}}); len(got) != 0 || reg.Limiter("alice") != nil {
		t.Fatalf("an unlimited credential got a bucket: %v", got)
	}
	l := reg.Sync("in1", []Holder{{"c1", "alice", mbps8}})["c1"]
	// The same person on another inbound (another engine) uses the same bucket.
	if other := reg.Sync("in2", []Holder{{"c9", "alice", mbps8}})["c9"]; other != l {
		t.Fatal("the same person on a second inbound must share the bucket")
	}
	// The first inbound lets go: the second still holds it.
	reg.Sync("in1", nil)
	if reg.Limiter("alice") != l {
		t.Fatal("the bucket must live while a credential holds it")
	}
	// A credential that leaves an inbound while the inbound stays lets go of its share only.
	reg.Sync("in2", []Holder{{"c8", "bob", mbps8}})
	if reg.Limiter("alice") != nil {
		t.Fatal("alice last credential is gone: her bucket must be dropped")
	}
	if reg.Limiter("bob") == nil {
		t.Fatal("bob bucket is missing")
	}
	reg.Sync("in2", nil)
	if reg.Limiter("bob") != nil || len(reg.users) != 0 || len(reg.scopes) != 0 {
		t.Fatalf("a released registry must be empty: users=%d scopes=%d", len(reg.users), len(reg.scopes))
	}
	var nilReg *Registry
	if nilReg.Sync("x", []Holder{{"c", "u", 1}}) != nil || nilReg.Limiter("u") != nil {
		t.Fatal("a nil registry means no person limits")
	}
}

func TestDisagreeingCredentialsTakeTheSmallestLimit(t *testing.T) {
	// Whatever order the inbounds arrive in, the bucket ends at the smallest non-zero value.
	for _, order := range [][2]uint64{{mbps8, 4 * mbps8}, {4 * mbps8, mbps8}} {
		reg := NewRegistry()
		l := reg.Sync("in1", []Holder{{"c1", "alice", order[0]}})["c1"]
		reg.Sync("in2", []Holder{{"c2", "alice", order[1]}})
		if l.BPS() != mbps8 {
			t.Fatalf("order %v: bps = %d, want the smallest %d", order, l.BPS(), mbps8)
		}
		// The small one leaves: the rest is the value of whoever remains.
		if order[0] == mbps8 {
			reg.Sync("in1", nil)
		} else {
			reg.Sync("in2", nil)
		}
		if l.BPS() != 4*mbps8 {
			t.Fatalf("order %v: bps = %d after the smaller left, want %d", order, l.BPS(), 4*mbps8)
		}
	}
}
