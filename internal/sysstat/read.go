package sysstat

import (
	"os"
	"path/filepath"
	"runtime/metrics"
	"sync"
)

// Reader reads under a /proc and a /sys root. The zero value reads the real ones.
type Reader struct {
	Proc, Sys string
}

func (r Reader) proc(rel string) string {
	root := r.Proc
	if root == "" {
		root = "/proc"
	}
	b, _ := os.ReadFile(filepath.Join(root, rel))
	return string(b)
}

func (r Reader) sys(rel string) string {
	root := r.Sys
	if root == "" {
		root = "/sys"
	}
	b, _ := os.ReadFile(filepath.Join(root, rel))
	return string(b)
}

// Conntrack returns the connection-tracking table's entries and its limit (0, 0 where netfilter's conntrack is not loaded).
// When the table is full the kernel drops new connections ("nf_conntrack: table full, dropping packet"), which looks to
// people like a node that stopped answering.
func (r Reader) Conntrack() (count, max uint64) {
	count, _ = ParseUint(r.proc("sys/net/netfilter/nf_conntrack_count"))
	max, _ = ParseUint(r.proc("sys/net/netfilter/nf_conntrack_max"))
	return count, max
}

// Sockets returns the IPv4 and IPv6 socket counts added together.
func (r Reader) Sockets() Sockets {
	return ParseSockstat(r.proc("net/sockstat")).Add(ParseSockstat(r.proc("net/sockstat6")))
}

// NICSpeedMbps is the link speed of an interface in Mbit/s, 0 when the driver does not say (most virtual NICs).
func (r Reader) NICSpeedMbps(iface string) uint32 {
	if iface == "" || filepath.Base(iface) != iface {
		return 0
	}
	n, ok := ParseUint(r.sys(filepath.Join("class/net", iface, "speed")))
	if !ok || n > 1_000_000 {
		return 0
	}
	return uint32(n)
}

// Host is one sample of the machine.
type Host struct {
	CPU   CPUShare
	Load1 float64
	Mem   Mem
	// Connection tracking: entries and limit (0, 0 = not loaded).
	ConntrackCount, ConntrackMax uint64
	Sockets                      Sockets
}

// HostSampler takes Host samples; CPU shares are over the time since the previous Sample (zero on the first). Safe for
// concurrent use.
type HostSampler struct {
	R    Reader
	mu   sync.Mutex
	prev CPUTimes
}

// Sample reads the machine now.
func (h *HostSampler) Sample() Host {
	var s Host
	if cur, ok := ParseCPU(h.R.proc("stat")); ok {
		h.mu.Lock()
		if h.prev.Total != 0 {
			s.CPU = CPUDelta(h.prev, cur)
		}
		h.prev = cur
		h.mu.Unlock()
	}
	s.Load1 = firstFloat(h.R.proc("loadavg"))
	s.Mem = ParseMeminfo(h.R.proc("meminfo"))
	s.ConntrackCount, s.ConntrackMax = h.R.Conntrack()
	s.Sockets = h.R.Sockets()
	return s
}

// Process is one sample of a process.
type Process struct {
	// CPUPct is the process's CPU time over the interval in percent of the whole machine (all CPUs), so it can be set
	// against the host's Busy: 25 on a 4-CPU machine is one CPU fully used.
	CPUPct  float64
	RSS     uint64
	Threads uint32
	// Open file descriptors (sockets are among them) and the soft limit (0 = not known).
	FDs, FDLimit uint64
}

// ProcSampler samples one process (PID "self" when empty). Safe for concurrent use.
type ProcSampler struct {
	R   Reader
	PID string

	mu                  sync.Mutex
	prevProc, prevTotal uint64
}

// Sample reads the process now; CPUPct is over the time since the previous Sample (zero on the first).
func (p *ProcSampler) Sample() Process {
	pid := p.PID
	if pid == "" {
		pid = "self"
	}
	var out Process
	st, okP := ParseProcStat(p.R.proc(filepath.Join(pid, "stat")))
	cpu, okC := ParseCPU(p.R.proc("stat"))
	if okP {
		out.Threads = st.Threads
	}
	if okP && okC {
		p.mu.Lock()
		if p.prevTotal != 0 && cpu.Total > p.prevTotal && st.CPUTicks >= p.prevProc {
			out.CPUPct = Clamp100(float64(st.CPUTicks-p.prevProc) * 100 / float64(cpu.Total-p.prevTotal))
		}
		p.prevProc, p.prevTotal = st.CPUTicks, cpu.Total
		p.mu.Unlock()
	}
	out.RSS = ParseStatusRSS(p.R.proc(filepath.Join(pid, "status")))
	out.FDLimit = ParseOpenFilesLimit(p.R.proc(filepath.Join(pid, "limits")))
	root := p.R.Proc
	if root == "" {
		root = "/proc"
	}
	if ents, err := os.ReadDir(filepath.Join(root, pid, "fd")); err == nil {
		out.FDs = uint64(len(ents))
	}
	return out
}

// GoRuntime is what the Go runtime of this process holds.
type GoRuntime struct {
	Goroutines uint64
	// HeapBytes is memory in live and not yet swept heap objects; TotalBytes is all memory the runtime mapped from the OS
	// (heap, stacks, its own metadata), which is close to what the process costs beyond its binary.
	HeapBytes, TotalBytes uint64
}

// ReadGoRuntime reads the runtime's own counters without stopping the world.
func ReadGoRuntime() GoRuntime {
	s := []metrics.Sample{
		{Name: "/sched/goroutines:goroutines"},
		{Name: "/memory/classes/heap/objects:bytes"},
		{Name: "/memory/classes/total:bytes"},
	}
	metrics.Read(s)
	u := func(i int) uint64 {
		if s[i].Value.Kind() == metrics.KindUint64 {
			return s[i].Value.Uint64()
		}
		return 0
	}
	return GoRuntime{Goroutines: u(0), HeapBytes: u(1), TotalBytes: u(2)}
}

func firstFloat(s string) float64 {
	v, _ := parseFirstFloat(s)
	return v
}
