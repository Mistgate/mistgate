package auth

import (
	"runtime/debug"
	"sync"
	"time"
)

// An argon2id hash takes a 19 MiB block array. Go frees it at the next garbage collection, but an idle panel
// allocates so little that the next collection can be minutes away, and even after it the runtime's background
// scavenger hands the pages back to the OS only slowly: pages used by one sign-in can stay resident long after
// the hash finishes.
//
// scheduleMemoryRelease runs debug.FreeOSMemory (a collection plus an immediate return of free pages to the OS)
// once, releaseDelay after the last hash finished. The timer is pushed back by every hash that ends before it
// fires, so a burst of sign-in attempts causes one collection, not one per attempt: the rate limit is built in.
var release struct {
	mu    sync.Mutex
	timer *time.Timer
	delay time.Duration // releaseDelay; tests shorten it
}

const releaseDelay = 2 * time.Second

func scheduleMemoryRelease() {
	release.mu.Lock()
	defer release.mu.Unlock()
	d := release.delay
	if d == 0 {
		d = releaseDelay
	}
	if release.timer == nil {
		release.timer = time.AfterFunc(d, debug.FreeOSMemory)
		return
	}
	release.timer.Reset(d)
}
