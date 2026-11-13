//go:build linux

package main

import (
	"context"
	"testing"
)

// Needs a host with /dev/net/tun and no amneziawg module (WSL, a CI container): mode "kernel" then runs on the userspace
// fallback. The module appearing does nothing by itself; only the repair drops the engine, and a second one finds nothing.
func TestFallbackEngineMovesBackOnlyThroughTheFix(t *testing.T) {
	mode := "kernel"
	w := newAwgEngine(awgEnv(), func() string { return mode })
	loaded := false
	w.moduleLoaded = func() bool { return loaded }
	if st := w.BackendStatus(); !st.Fallback {
		t.Skipf("this host does not give the fallback (needs /dev/net/tun and no module): %+v", st)
	}
	loaded = true // someone built and loaded the module
	for range 3 {
		if st := w.BackendStatus(); !st.Fallback || st.Name != "userspace" || w.peek() == nil {
			t.Fatalf("the engine moved by itself: %+v", st)
		}
	}
	moved, err := w.ReprobeKernel(context.Background())
	if err != nil || !moved || w.peek() != nil {
		t.Fatalf("the fix did not drop the engine: moved=%v err=%v built=%v", moved, err, w.peek() != nil)
	}
	if moved, _ := w.ReprobeKernel(context.Background()); moved {
		t.Fatal("a second fix found an engine to drop")
	}
	// An engine that is not on the fallback is left alone, module or not.
	mode = "userspace"
	if st := w.BackendStatus(); st.Fallback || w.peek() == nil {
		t.Fatalf("userspace mode: %+v", st)
	}
	if moved, _ := w.ReprobeKernel(context.Background()); moved || w.peek() == nil {
		t.Fatal("the fix dropped an engine that was not a fallback")
	}
}
