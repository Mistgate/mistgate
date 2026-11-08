// Package sysstat reads what a Linux host and the current process are using: CPU (with the parts lost to softirq, steal
// and I/O wait), memory and swap, connection tracking, sockets, the NIC's link speed, and the process's own CPU, memory,
// threads and file descriptors. The node agent reports it with every stats batch; the panel shows its own.
//
// The parsers here are pure and build everywhere, so they are tested on any OS. The readers (read.go) take a /proc and
// /sys root so tests can point them at a fixture; on a system without /proc they return zeros, which callers treat as
// "not known".
package sysstat

import (
	"bufio"
	"strconv"
	"strings"
)

// CPUTimes is the aggregate "cpu" line of /proc/stat, in USER_HZ ticks summed over all CPUs:
// user nice system idle iowait irq softirq steal [guest guest_nice]. guest time is already inside user, so it is not added
// to Total.
type CPUTimes struct {
	Total, Idle, IOWait, Softirq, Steal uint64
}

// ParseCPU reads the aggregate "cpu" line of /proc/stat.
func ParseCPU(stat string) (CPUTimes, bool) {
	for _, line := range strings.Split(stat, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[0] != "cpu" {
			continue
		}
		var v [8]uint64
		var total uint64
		for i := range v {
			if i+1 >= len(f) {
				break
			}
			n, err := strconv.ParseUint(f[i+1], 10, 64)
			if err != nil {
				return CPUTimes{}, false
			}
			v[i] = n
			total += n
		}
		return CPUTimes{Total: total, Idle: v[3], IOWait: v[4], Softirq: v[6], Steal: v[7]}, true
	}
	return CPUTimes{}, false
}

// CPUShare is how the whole machine's CPU time was spent between two samples, in percent of all CPUs.
type CPUShare struct {
	// Busy is everything but idle and I/O wait: user, system, irq, softirq and steal.
	Busy float64
	// Softirq is the part of Busy spent in softirq (packet processing, a kernel WireGuard/AmneziaWG).
	Softirq float64
	// Steal is the part of Busy the hypervisor gave to other guests: a busy neighbour on the same host.
	Steal float64
	// IOWait is time idle while waiting for disk; it is not in Busy.
	IOWait float64
}

// CPUDelta turns two samples into shares. Counters that went backwards (a reset) give zeros.
func CPUDelta(prev, cur CPUTimes) CPUShare {
	if cur.Total <= prev.Total || cur.Idle < prev.Idle || cur.IOWait < prev.IOWait || cur.Softirq < prev.Softirq || cur.Steal < prev.Steal {
		return CPUShare{}
	}
	dt := float64(cur.Total - prev.Total)
	idle := float64(cur.Idle-prev.Idle) + float64(cur.IOWait-prev.IOWait)
	return CPUShare{
		Busy:    Clamp100((dt - idle) * 100 / dt),
		Softirq: Clamp100(float64(cur.Softirq-prev.Softirq) * 100 / dt),
		Steal:   Clamp100(float64(cur.Steal-prev.Steal) * 100 / dt),
		IOWait:  Clamp100(float64(cur.IOWait-prev.IOWait) * 100 / dt),
	}
}

// Clamp100 keeps a percentage in 0..100.
func Clamp100(f float64) float64 { return min(max(f, 0), 100) }

// Mem is /proc/meminfo's totals in bytes (0 when missing).
type Mem struct {
	Total, Avail, SwapTotal, SwapFree uint64
}

// Used is Total minus Avail, the memory the system could not hand to a new program without swapping.
func (m Mem) Used() uint64 {
	if m.Total < m.Avail {
		return 0
	}
	return m.Total - m.Avail
}

// SwapUsed is SwapTotal minus SwapFree.
func (m Mem) SwapUsed() uint64 {
	if m.SwapTotal < m.SwapFree {
		return 0
	}
	return m.SwapTotal - m.SwapFree
}

// ParseMeminfo reads MemTotal, MemAvailable, SwapTotal and SwapFree.
func ParseMeminfo(s string) Mem {
	var m Mem
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			m.Total = n * 1024
		case "MemAvailable:":
			m.Avail = n * 1024
		case "SwapTotal:":
			m.SwapTotal = n * 1024
		case "SwapFree:":
			m.SwapFree = n * 1024
		}
	}
	return m
}

// Sockets are the counts of /proc/net/sockstat and sockstat6 added together.
type Sockets struct {
	// TCP sockets in use (established and listening), in TIME_WAIT, and orphaned (closed by the program, not yet by the peer).
	TCP, TCPTimeWait, TCPOrphan uint32
	UDP                         uint32
}

// ParseSockstat reads "TCP: inuse N orphan N tw N ..." and "UDP: inuse N ..." (sockstat), or "TCP6: inuse N" and
// "UDP6: inuse N" (sockstat6). Call it on each file and Add the results.
func ParseSockstat(s string) Sockets {
	var out Sockets
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		kv := map[string]uint32{}
		for i := 1; i+1 < len(f); i += 2 {
			if n, err := strconv.ParseUint(f[i+1], 10, 32); err == nil {
				kv[f[i]] = uint32(n)
			}
		}
		switch f[0] {
		case "TCP:", "TCP6:":
			out.TCP += kv["inuse"]
			out.TCPOrphan += kv["orphan"]
			out.TCPTimeWait += kv["tw"]
		case "UDP:", "UDP6:":
			out.UDP += kv["inuse"]
		}
	}
	return out
}

// Add sums two counts (IPv4 and IPv6).
func (s Sockets) Add(o Sockets) Sockets {
	return Sockets{TCP: s.TCP + o.TCP, TCPTimeWait: s.TCPTimeWait + o.TCPTimeWait, TCPOrphan: s.TCPOrphan + o.TCPOrphan, UDP: s.UDP + o.UDP}
}

// ParseUint reads a file holding one number, such as nf_conntrack_count. ok = false for an empty file or a negative
// number (a NIC's speed is -1 when the driver does not know it).
func ParseUint(s string) (uint64, bool) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	return n, err == nil
}

// ProcStat is what /proc/<pid>/stat says about a process.
type ProcStat struct {
	// CPUTicks is utime plus stime, in USER_HZ ticks: the same unit as CPUTimes, so the two can be divided.
	CPUTicks uint64
	Threads  uint32
}

// ParseProcStat reads /proc/<pid>/stat. The command name (field 2) is in parentheses and may hold spaces and ")", so the
// fields are counted after the last ")".
func ParseProcStat(s string) (ProcStat, bool) {
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return ProcStat{}, false
	}
	// After ")": state(3) ppid(4) ... utime(14) stime(15) ... num_threads(20). f[0] is field 3.
	f := strings.Fields(s[i+1:])
	if len(f) < 18 {
		return ProcStat{}, false
	}
	ut, e1 := strconv.ParseUint(f[11], 10, 64)
	st, e2 := strconv.ParseUint(f[12], 10, 64)
	th, e3 := strconv.ParseUint(f[17], 10, 32)
	if e1 != nil || e2 != nil || e3 != nil {
		return ProcStat{}, false
	}
	return ProcStat{CPUTicks: ut + st, Threads: uint32(th)}, true
}

// ParseStatusRSS reads VmRSS from /proc/<pid>/status, in bytes.
func ParseStatusRSS(s string) uint64 {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				n, _ := strconv.ParseUint(f[0], 10, 64)
				return n * 1024
			}
		}
	}
	return 0
}

// ParseOpenFilesLimit reads the soft "Max open files" from /proc/<pid>/limits (0 = unknown or unlimited).
func ParseOpenFilesLimit(s string) uint64 {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, "Max open files"); ok {
			f := strings.Fields(v)
			if len(f) > 0 {
				n, _ := strconv.ParseUint(f[0], 10, 64)
				return n
			}
		}
	}
	return 0
}

func parseFirstFloat(s string) (float64, bool) {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[0], 64)
	return v, err == nil
}
