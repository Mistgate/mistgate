package auth

import (
	"context"
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

// retained is the memory the Go heap holds from the OS (what shows up as resident set size, give or take the
// binary and the stacks).
func retained() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapSys - m.HeapReleased
}

// A sign-in hashes with 64 MiB of argon2 memory. The panel must not stay that much bigger afterwards: the
// idle target is 80 MB resident.
func TestHashingReturnsItsMemoryToTheOS(t *testing.T) {
	saved := argon
	argon.time, argon.memKiB, argon.threads = 3, 64<<10, 2 // the real production cost
	release.mu.Lock()
	release.delay = 50 * time.Millisecond
	release.mu.Unlock()
	t.Cleanup(func() {
		argon = saved
		release.mu.Lock()
		release.delay = 0
		release.mu.Unlock()
	})

	runtime.GC()
	debug.FreeOSMemory()
	base := retained()

	hash, err := hashPassword(context.Background(), "a password of sufficient length") // not verified here: a second hash would allocate outside the measured window
	if err != nil {
		t.Fatal(err)
	}
	_ = hash
	peak := retained()
	if peak < base+48<<20 {
		t.Fatalf("the heap grew by only %d MiB during the hash: this test does not measure what it should", (peak-base)>>20)
	}

	// Once things are quiet the memory goes back: within the delay plus one collection.
	deadline := time.Now().Add(5 * time.Second)
	for retained() > base+16<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("after hashing the heap still holds %d MiB more than before (peak +%d MiB)", (retained()-base)>>20, (peak-base)>>20)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Logf("heap held from the OS: before %d MiB, at the peak %d MiB, after the release %d MiB", base>>20, peak>>20, retained()>>20)

	// A burst of hashes causes one release after the burst, not one per hash: the timer is pushed back.
	for i := 0; i < 3; i++ {
		if _, err := hashPassword(context.Background(), "another password of sufficient length"); err != nil {
			t.Fatal(err)
		}
	}
	deadline = time.Now().Add(5 * time.Second)
	for retained() > base+16<<20 {
		if time.Now().After(deadline) {
			t.Fatalf("after a burst the heap still holds %d MiB more than before", (retained()-base)>>20)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
