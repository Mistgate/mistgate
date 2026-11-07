package sysstat

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestCPUDelta(t *testing.T) {
	// user nice system idle iowait irq softirq steal guest guest_nice
	a, ok := ParseCPU("cpu  100 0 50 800 40 5 5 0 0 0\ncpu0 1 1 1 1 1 1 1 1\n")
	if !ok || a.Total != 1000 || a.Idle != 800 || a.IOWait != 40 || a.Softirq != 5 || a.Steal != 0 {
		t.Fatalf("parse: %+v %v", a, ok)
	}
	b, _ := ParseCPU("cpu  200 0 100 1500 100 10 50 40 7 0\n")
	// dt = 2000-1000 = 1000; idle 700 + iowait 60 = 760 -> busy 24; softirq 45 -> 4.5; steal 40 -> 4; iowait 6.
	s := CPUDelta(a, b)
	if !near(s.Busy, 24) || !near(s.Softirq, 4.5) || !near(s.Steal, 4) || !near(s.IOWait, 6) {
		t.Fatalf("delta: %+v", s)
	}
	if z := CPUDelta(b, a); z != (CPUShare{}) {
		t.Fatalf("counters went backwards: %+v", z)
	}
	// An old kernel's line with 7 values (no steal) still parses.
	if c, ok := ParseCPU("cpu 1 2 3 4 5 6 7\n"); !ok || c.Total != 28 || c.Steal != 0 {
		t.Fatalf("short line: %+v %v", c, ok)
	}
	if _, ok := ParseCPU("cpu x 0 0 0 0 0 0 0\n"); ok {
		t.Fatal("garbage parsed")
	}
}

func TestMeminfo(t *testing.T) {
	m := ParseMeminfo("MemTotal:        1000 kB\nMemFree: 1 kB\nMemAvailable:     250 kB\nSwapTotal: 400 kB\nSwapFree: 100 kB\n")
	if m.Total != 1000*1024 || m.Avail != 250*1024 || m.Used() != 750*1024 || m.SwapUsed() != 300*1024 {
		t.Fatalf("%+v", m)
	}
	if (Mem{Total: 1, Avail: 2}).Used() != 0 || (Mem{SwapTotal: 1, SwapFree: 2}).SwapUsed() != 0 {
		t.Fatal("underflow")
	}
}

func TestSockstat(t *testing.T) {
	v4 := "sockets: used 812\nTCP: inuse 120 orphan 3 tw 40 alloc 130 mem 25\nUDP: inuse 9 mem 4\nUDPLITE: inuse 0\nRAW: inuse 0\nFRAG: inuse 0 memory 0\n"
	v6 := "TCP6: inuse 30\nUDP6: inuse 2\nUDPLITE6: inuse 0\n"
	s := ParseSockstat(v4).Add(ParseSockstat(v6))
	if s.TCP != 150 || s.TCPOrphan != 3 || s.TCPTimeWait != 40 || s.UDP != 11 {
		t.Fatalf("%+v", s)
	}
}

func TestProcStat(t *testing.T) {
	// The command name holds a space and a ")".
	line := "4242 (mistgate) node) S 1 4242 4242 0 -1 4194560 1000 0 0 0 350 150 0 0 20 0 17 0 12345 999999 2500 18446744073709551615"
	p, ok := ParseProcStat(line)
	if !ok || p.CPUTicks != 500 || p.Threads != 17 {
		t.Fatalf("%+v %v", p, ok)
	}
	if _, ok := ParseProcStat("4242 (x) S 1"); ok {
		t.Fatal("short line parsed")
	}
	if ParseStatusRSS("Name:\tx\nVmRSS:\t  20480 kB\n") != 20480*1024 {
		t.Fatal("rss")
	}
	limits := "Limit                     Soft Limit           Hard Limit           Units\nMax open files            65536                524288               files\n"
	if ParseOpenFilesLimit(limits) != 65536 {
		t.Fatal("limit")
	}
	if ParseOpenFilesLimit("Max open files            unlimited unlimited files\n") != 0 {
		t.Fatal("unlimited")
	}
}

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSamplers(t *testing.T) {
	proc, sys := t.TempDir(), t.TempDir()
	r := Reader{Proc: proc, Sys: sys}
	write(t, proc, "stat", "cpu  100 0 50 800 40 5 5 0 0 0\n")
	write(t, proc, "loadavg", "0.52 0.40 0.30 1/200 999\n")
	write(t, proc, "meminfo", "MemTotal: 2048 kB\nMemAvailable: 1024 kB\n")
	write(t, proc, "sys/net/netfilter/nf_conntrack_count", "900\n")
	write(t, proc, "sys/net/netfilter/nf_conntrack_max", "1000\n")
	write(t, proc, "net/sockstat", "TCP: inuse 5 orphan 0 tw 1 alloc 6 mem 1\nUDP: inuse 2 mem 1\n")
	write(t, proc, "77/stat", "77 (mistgate-node) S 1 77 77 0 -1 0 0 0 0 0 10 10 0 0 20 0 8 0 1 1 1 1")
	write(t, proc, "77/status", "VmRSS:\t4096 kB\n")
	write(t, proc, "77/limits", "Max open files            1024                4096                 files\n")
	write(t, proc, "77/fd/0", "")
	write(t, proc, "77/fd/1", "")
	write(t, sys, "class/net/eth0/speed", "1000\n")
	write(t, sys, "class/net/ens3/speed", "-1\n")

	h := HostSampler{R: r}
	first := h.Sample()
	if first.CPU != (CPUShare{}) || first.Load1 != 0.52 || first.Mem.Used() != 1024*1024 || first.ConntrackCount != 900 || first.ConntrackMax != 1000 || first.Sockets.TCP != 5 || first.Sockets.UDP != 2 {
		t.Fatalf("first host sample: %+v", first)
	}
	p := ProcSampler{R: r, PID: "77"}
	if s := p.Sample(); s.CPUPct != 0 || s.RSS != 4096*1024 || s.Threads != 8 || s.FDs != 2 || s.FDLimit != 1024 {
		t.Fatalf("first proc sample: %+v", s)
	}

	write(t, proc, "stat", "cpu  200 0 100 1500 100 10 50 40 0 0\n")
	write(t, proc, "77/stat", "77 (mistgate-node) S 1 77 77 0 -1 0 0 0 0 0 60 60 0 0 20 0 9 0 1 1 1 1")
	if s := h.Sample(); !near(s.CPU.Busy, 24) || !near(s.CPU.Steal, 4) {
		t.Fatalf("second host sample: %+v", s.CPU)
	}
	// The process used 100 ticks of the machine's 1000.
	if s := p.Sample(); !near(s.CPUPct, 10) || s.Threads != 9 {
		t.Fatalf("second proc sample: %+v", s)
	}

	if r.NICSpeedMbps("eth0") != 1000 || r.NICSpeedMbps("ens3") != 0 || r.NICSpeedMbps("missing") != 0 || r.NICSpeedMbps("../x") != 0 {
		t.Fatal("nic speed")
	}

	// Nothing there: zeros, no panic.
	empty := Reader{Proc: t.TempDir(), Sys: t.TempDir()}
	if s := (&HostSampler{R: empty}).Sample(); s != (Host{}) {
		t.Fatalf("empty host: %+v", s)
	}
	if s := (&ProcSampler{R: empty}).Sample(); s != (Process{}) {
		t.Fatalf("empty proc: %+v", s)
	}
}

func TestGoRuntime(t *testing.T) {
	g := ReadGoRuntime()
	if g.Goroutines == 0 || g.TotalBytes == 0 || g.HeapBytes == 0 || g.HeapBytes > g.TotalBytes {
		t.Fatalf("%+v", g)
	}
}
