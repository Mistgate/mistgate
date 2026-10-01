package doctor

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Readers for /proc and the kernel log, shared by several checks. They only read.

// ---- journal size ----

type journalInfo struct {
	bytes int64
	found bool // at least one journal directory exists
}

// journal sums the size of the persistent and the volatile journal.
func journal(e *Env) journalInfo {
	return e.memo.journal.get(func() journalInfo {
		var ji journalInfo
		for _, dir := range []string{"/var/log/journal", "/run/log/journal"} {
			if _, err := e.stat(dir); err != nil {
				continue
			}
			ji.found = true
			ji.bytes += dirSize(e, dir)
		}
		return ji
	})
}

// dirSize adds up the regular files below dir (one level of machine-id directories, but any depth works).
func dirSize(e *Env, dir string) int64 {
	var n int64
	ents, err := e.readDir(dir)
	if err != nil {
		return 0
	}
	for _, ent := range ents {
		sub := dir + "/" + ent.Name()
		if ent.IsDir() {
			n += dirSize(e, sub)
		} else if fi, err := ent.Info(); err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
	}
	return n
}

// ---- kernel log ----

type klogInfo struct {
	lines []string // last 24 h, warning and above
	err   error
}

// kernelLog returns the recent kernel messages, from the journal if there is one, else from dmesg.
func kernelLog(ctx context.Context, e *Env) klogInfo {
	return e.memo.klog.get(func() klogInfo {
		if e.has("journalctl") {
			out, err := e.Run(ctx, "journalctl", "-k", "--no-pager", "-q", "-p", "warning", "-o", "cat", "--since", "-24h", "-n", "5000")
			if err == nil {
				return klogInfo{lines: splitLines(string(out))}
			}
		}
		if e.has("dmesg") {
			out, err := e.Run(ctx, "dmesg", "--time-format=iso", "--level=emerg,alert,crit,err,warn")
			if err == nil {
				return klogInfo{lines: recentDmesg(splitLines(string(out)), e.now().Add(-24*time.Hour))}
			}
			return klogInfo{err: err}
		}
		return klogInfo{err: errors.New("no journalctl or dmesg")}
	})
}

func splitLines(s string) []string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// recentDmesg keeps lines whose iso timestamp ("2026-09-30T12:00:00,123456+00:00 text") is after since, and
// lines without a parsable timestamp.
func recentDmesg(lines []string, since time.Time) []string {
	out := lines[:0:0]
	for _, l := range lines {
		head, _, ok := strings.Cut(l, " ")
		if ok {
			if t, err := time.Parse(time.RFC3339Nano, strings.Replace(head, ",", ".", 1)); err == nil && t.Before(since) {
				continue
			}
		}
		out = append(out, l)
	}
	return out
}

// ---- processes ----

// pidList returns the numeric directories of /proc.
func pidList(e *Env) []int {
	ents, err := e.readDir("/proc")
	if err != nil {
		return nil
	}
	var pids []int
	for _, ent := range ents {
		if n, err := strconv.Atoi(ent.Name()); err == nil && n > 0 {
			pids = append(pids, n)
		}
	}
	return pids
}

// procComms maps every pid to its comm (the 15-byte process name).
func procComms(e *Env) map[int]string {
	return e.memo.procs.get(func() map[int]string {
		m := map[int]string{}
		for _, pid := range pidList(e) {
			if s, err := e.read("/proc/" + strconv.Itoa(pid) + "/comm"); err == nil {
				m[pid] = strings.TrimSpace(s)
			}
		}
		return m
	})
}

// procStat is what dstate_tasks needs from /proc/<pid>/stat.
type procStat struct {
	pid    int
	comm   string
	state  byte
	ppid   int
	flags  uint64
	starts uint64 // start time in clock ticks since boot: pid + starts identify a task across samples
}

const pfKthread = 0x00200000

// parseProcStat parses "pid (comm) S ppid pgrp session tty tpgid flags ... starttime ..." where comm may
// itself contain spaces and parentheses, hence the last ')'.
func parseProcStat(s string) (procStat, bool) {
	open, cl := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || cl < open {
		return procStat{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		return procStat{}, false
	}
	f := strings.Fields(s[cl+1:])
	if len(f) < 20 { // field 3 is f[0], starttime (field 22) is f[19]
		return procStat{}, false
	}
	ppid, e1 := strconv.Atoi(f[1])
	flags, e2 := strconv.ParseUint(f[6], 10, 64)
	st, e3 := strconv.ParseUint(f[19], 10, 64)
	if e1 != nil || e2 != nil || e3 != nil || len(f[0]) != 1 {
		return procStat{}, false
	}
	return procStat{pid: pid, comm: s[open+1 : cl], state: f[0][0], ppid: ppid, flags: flags, starts: st}, true
}

func (p procStat) kernelThread() bool { return p.pid == 2 || p.ppid == 2 || p.flags&pfKthread != 0 }

// ---- sockets ----

type sock struct {
	network string // "udp" | "tcp"
	port    int
	inode   uint64
}

// listenSockets returns the TCP sockets in LISTEN state and the UDP sockets that are bound but not
// connected, from /proc/net/{tcp,tcp6,udp,udp6}. A connected UDP socket is a client, not a listener.
func listenSockets(e *Env) []sock {
	var out []sock
	for _, t := range []struct{ file, network string }{
		{"/proc/net/tcp", "tcp"}, {"/proc/net/tcp6", "tcp"}, {"/proc/net/udp", "udp"}, {"/proc/net/udp6", "udp"},
	} {
		s, err := e.read(t.file)
		if err != nil {
			continue
		}
		out = append(out, parseNetSockets(s, t.network)...)
	}
	return out
}

func parseNetSockets(s, network string) []sock {
	var out []sock
	for i, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if i == 0 || len(f) < 10 {
			continue
		}
		_, portHex, ok := cutLast(f[1], ':')
		if !ok {
			continue
		}
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil || port == 0 {
			continue
		}
		if network == "tcp" && f[3] != "0A" {
			continue
		}
		if network == "udp" && !allZeroAddr(f[2]) {
			continue
		}
		inode, err := strconv.ParseUint(f[9], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, sock{network: network, port: int(port), inode: inode})
	}
	return out
}

func cutLast(s string, sep byte) (before, after string, ok bool) {
	i := strings.LastIndexByte(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+1:], true
}

// allZeroAddr: a remote address of 0.0.0.0:0 or [::]:0 in /proc/net notation.
func allZeroAddr(a string) bool {
	host, port, ok := cutLast(a, ':')
	return ok && strings.Trim(host, "0") == "" && strings.Trim(port, "0") == ""
}

var socketLinkRe = regexp.MustCompile(`^socket:\[(\d+)\]$`)

// socketOwners finds which pid holds each wanted socket inode by reading /proc/<pid>/fd. Nothing is cached
// beyond the wanted set, so memory stays flat however many descriptors the host has.
func socketOwners(e *Env, want map[uint64]bool) map[uint64]int {
	owners := map[uint64]int{}
	if len(want) == 0 {
		return owners
	}
	for _, pid := range pidList(e) {
		dir := "/proc/" + strconv.Itoa(pid) + "/fd"
		ents, err := e.readDir(dir)
		if err != nil {
			continue
		}
		for _, ent := range ents {
			link, err := e.readlink(dir + "/" + ent.Name())
			if err != nil {
				continue
			}
			if m := socketLinkRe.FindStringSubmatch(link); m != nil {
				if ino, _ := strconv.ParseUint(m[1], 10, 64); want[ino] {
					owners[ino] = pid
				}
			}
		}
	}
	return owners
}
