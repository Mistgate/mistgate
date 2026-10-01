package hostctl

import (
	"bufio"
	"strconv"
	"strings"
)

// Pure parsers for /proc files. They are not behind the Linux build tag so they are tested everywhere.

type cpuSample struct{ total, idle, softirq uint64 }

// parseCPUStat reads the aggregate "cpu" line of /proc/stat:
// user nice system idle iowait irq softirq steal [guest guest_nice]. guest time is already inside user.
func parseCPUStat(s string) (cpuSample, bool) {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 8 || f[0] != "cpu" {
			continue
		}
		var v [8]uint64
		var total uint64
		for i := range v {
			n, err := strconv.ParseUint(f[i+1], 10, 64)
			if err != nil {
				return cpuSample{}, false
			}
			v[i] = n
			total += n
		}
		return cpuSample{total: total, idle: v[3] + v[4], softirq: v[6]}, true
	}
	return cpuSample{}, false
}

// cpuPct turns two samples into (busy %, softirq %).
func cpuPct(prev, cur cpuSample) (busy, soft float64) {
	if cur.total <= prev.total || cur.idle < prev.idle || cur.softirq < prev.softirq {
		return 0, 0
	}
	dt := float64(cur.total - prev.total)
	busy = (dt - float64(cur.idle-prev.idle)) * 100 / dt
	soft = float64(cur.softirq-prev.softirq) * 100 / dt
	return clamp100(busy), clamp100(soft)
}

func clamp100(f float64) float64 {
	return min(max(f, 0), 100)
}

// parseMeminfo returns MemTotal and MemAvailable in bytes (0 if missing).
func parseMeminfo(s string) (total, avail uint64) {
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
			total = n * 1024
		case "MemAvailable:":
			avail = n * 1024
		}
	}
	return
}

// parseNetDev returns the rx/tx byte counters of one interface from /proc/net/dev.
func parseNetDev(s, iface string) (rx, tx uint64, ok bool) {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		name, rest, found := strings.Cut(sc.Text(), ":")
		if !found || strings.TrimSpace(name) != iface {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			return 0, 0, false
		}
		r, e1 := strconv.ParseUint(f[0], 10, 64)
		t, e2 := strconv.ParseUint(f[8], 10, 64)
		return r, t, e1 == nil && e2 == nil
	}
	return 0, 0, false
}

// defaultIface returns the interface of the IPv4 default route from /proc/net/route ("" if none).
func defaultIface(route string) string {
	for _, line := range strings.Split(route, "\n") {
		f := strings.Fields(line)
		// Iface Destination Gateway Flags ...; the header line has "Destination" in column 2.
		if len(f) >= 4 && f[1] == "00000000" {
			return f[0]
		}
	}
	return ""
}

func parseFirstFloat(s string) float64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	return v
}

// parseBtime reads "btime <unix>" from /proc/stat.
func parseBtime(s string) int64 {
	for _, line := range strings.Split(s, "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "btime" {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			return n
		}
	}
	return 0
}

// osPrettyName reads PRETTY_NAME from /etc/os-release.
func osPrettyName(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}
