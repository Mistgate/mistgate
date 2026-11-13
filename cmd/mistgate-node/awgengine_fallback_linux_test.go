//go:build linux

package main

import (
	"context"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// Needs a host with /dev/net/tun and no working amneziawg module (WSL, a CI container): mode "kernel" then runs on the
// userspace fallback. The module appearing does nothing by itself; only ReprobeKernel can move the engine, and only when a
// candidate really gets the kernel backend. A host that claims the module but cannot use it (here: moduleLoaded says yes,
// the genetlink family is absent) keeps the serving engine untouched.
func TestFallbackEngineMovesBackOnlyThroughTheFix(t *testing.T) {
	ctx := context.Background()
	mode := "kernel"
	w := newAwgEngine(awgEnv(), func() string { return mode })
	loaded := false
	w.moduleLoaded = func() bool { return loaded }
	if st := w.BackendStatus(); !st.Fallback {
		t.Skipf("this host does not give the fallback (needs /dev/net/tun and no module): %+v", st)
	}
	serving := w.peek()
	loaded = true // someone built and loaded the module
	for range 3 {
		if st := w.BackendStatus(); !st.Fallback || st.Name != "userspace" || w.peek() != serving {
			t.Fatalf("the engine moved by itself: %+v", st)
		}
	}
	// The module is claimed but the kernel backend is not usable: the candidate fails, the serving engine stays.
	if moved, err := w.ReprobeKernel(ctx); err != nil || moved {
		t.Fatalf("moved to an unusable kernel backend: moved=%v err=%v", moved, err)
	}
	if st := w.BackendStatus(); !st.Fallback || st.Name != "userspace" || !st.Available || w.peek() != serving {
		t.Fatalf("the fallback engine stopped serving: %+v", st)
	}
	// No module at all: nothing is even probed.
	loaded = false
	if moved, err := w.ReprobeKernel(ctx); err != nil || moved || w.peek() != serving {
		t.Fatalf("moved with no module: moved=%v err=%v", moved, err)
	}
	// An engine that is not on the fallback is left alone, module or not.
	mode = "userspace"
	if !w.NodeSettings(ctx, &pb.NodeSettings{AwgBackend: "userspace"}) || w.peek() != nil {
		t.Fatal("the changed setting did not drop the engine")
	}
	if st := w.BackendStatus(); st.Fallback || st.Name != "userspace" || w.peek() == nil {
		t.Fatalf("userspace mode: %+v", st)
	}
	serving, loaded = w.peek(), true
	if moved, err := w.ReprobeKernel(ctx); err != nil || moved || w.peek() != serving {
		t.Fatalf("the fix touched an engine that was not a fallback: moved=%v err=%v", moved, err)
	}
}
