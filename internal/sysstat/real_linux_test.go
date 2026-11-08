package sysstat

import (
	"testing"
	"time"
)

// On a real Linux /proc the readers find the machine and this test process.
func TestRealProc(t *testing.T) {
	var h HostSampler
	var p ProcSampler
	h.Sample()
	p.Sample()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) { // burn some CPU so the process has ticks to show
	}
	s := h.Sample()
	if s.Mem.Total == 0 || s.Mem.Avail == 0 || s.Load1 < 0 {
		t.Fatalf("host = %+v", s)
	}
	me := p.Sample()
	if me.RSS == 0 || me.Threads == 0 || me.FDs == 0 || me.FDLimit == 0 || me.CPUPct <= 0 {
		t.Fatalf("self = %+v", me)
	}
	if total, used := DiskUsage("/"); total == 0 || used > total {
		t.Fatalf("disk / = %d, %d", total, used)
	}
}
