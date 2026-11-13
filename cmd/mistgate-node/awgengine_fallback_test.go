package main

import (
	"context"
	"testing"
)

// The fix only acts on an engine that exists: asking before anything was built builds nothing and probes nothing.
func TestReprobeKernelDoesNotBuildTheEngine(t *testing.T) {
	w := newAwgEngine(awgEnv(), func() string { return "kernel" })
	w.moduleLoaded = func() bool { t.Fatal("the module was probed with no engine built"); return true }
	moved, err := w.ReprobeKernel(context.Background())
	if moved || err != nil || w.peek() != nil {
		t.Fatalf("moved=%v err=%v built=%v", moved, err, w.peek() != nil)
	}
}
