//go:build !js

package health

import (
	"context"
	"testing"
	"time"
)

// A waiter for the probe key of an inbound gives up with its context, and the key is free again once the holder lets go.
func TestAWGProbeLockWaiterLeavesWithItsContext(t *testing.T) {
	s := &Service{}
	unlock, ok := s.lockAWGProbe(context.Background(), "inb_a")
	if !ok {
		t.Fatal("the first lock was refused")
	}
	// Another inbound has its own key and is not held up.
	if other, ok := s.lockAWGProbe(context.Background(), "inb_b"); !ok {
		t.Fatal("the lock of another inbound was refused")
	} else {
		other()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, ok := s.lockAWGProbe(ctx, "inb_a"); ok || time.Since(start) > time.Second {
		t.Fatalf("a waiter with a spent context got the lock (%v) or waited %v", ok, time.Since(start))
	}
	unlock()
	again, ok := s.lockAWGProbe(context.Background(), "inb_a")
	if !ok {
		t.Fatal("the lock was not released")
	}
	again()
}
