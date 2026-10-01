package doctor

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------------------------------
// dstate_tasks: a QXL video driver hang blocks apt and SSH logins on some hosters' VMs (load 1.00, D-state, TTM).
// The fix is a reboot from the hoster panel, so there is no fix id.

var ttmHintRe = regexp.MustCompile(`(?i)(ttm|qxl)`)
var ttmBadRe = regexp.MustCompile(`(?i)(error|timeout|timed out|hung|blocked|fail|stuck)`)

func checkDstateTasks(ctx context.Context, e *Env) Result {
	if len(pidList(e)) == 0 {
		return skip(CodeDstateNoProc, "cannot read /proc")
	}
	cur := sampleD(e)
	for i := 1; i < dstateSamples && len(cur) > 0; i++ {
		e.Sleep(ctx, dstateGap)
		if ctx.Err() != nil {
			return skip(CodeDstateCut, "interrupted")
		}
		next := sampleD(e)
		for k := range cur {
			if _, ok := next[k]; !ok {
				delete(cur, k) // it left D state between samples: not stuck
			}
		}
	}

	r := Result{Status: OK, Params: p("stuck", strconv.Itoa(len(cur))), Code: CodeDstateNone}
	if n := len(cur); n > 0 {
		r.Code = CodeDstateStuck
		names := make([]string, 0, n)
		for _, t := range cur {
			names = append(names, t.comm+"("+strconv.Itoa(t.pid)+")")
		}
		names = firstN(sorted(names), dstateMaxListed)
		r.Params["tasks"] = strings.Join(names, ",")
		r.Status = Warn
		if prev, ok := e.Prev(CheckDstateTasks); ok {
			if n, _ := strconv.Atoi(prev.Params["stuck"]); n > 0 {
				r.Status = Fail // seen at two consecutive runs: minutes, not a blip
			}
		}
		if k := kernelLog(ctx, e); hasTTMHint(k.lines) {
			r.Params["hint"] = "qxl_ttm"
			r.Status = Fail
		}
		r.Detail = fmt.Sprintf("%d task(s) stuck in D state: %s", n, strings.Join(names, ", "))
	} else {
		r.Detail = "no task stuck in D state"
	}
	if idleLoaded(e) {
		r.Status = Fail
		r.Params["load_idle"] = "1"
		r.Code = CodeDstateLoadIdle
		if len(cur) > 0 {
			r.Code = CodeDstateStuckLoad
		}
		r.Detail += "; load1 at the CPU count while CPU is idle for 10 min"
	}
	return r
}

func sampleD(e *Env) map[string]procStat {
	m := map[string]procStat{}
	for _, pid := range pidList(e) {
		s, err := e.read("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			continue
		}
		if ps, ok := parseProcStat(s); ok && ps.state == 'D' && !ps.kernelThread() {
			m[strconv.Itoa(ps.pid)+":"+strconv.FormatUint(ps.starts, 10)] = ps
		}
	}
	return m
}

func hasTTMHint(lines []string) bool {
	for _, l := range lines {
		if ttmHintRe.MatchString(l) && ttmBadRe.MatchString(l) {
			return true
		}
	}
	return false
}

// idleLoaded: for the whole last 10 minutes load1 sat at about the CPU count while the CPU was idle. That
// is the signature of tasks stuck in D state even when the stuck list itself came back empty.
func idleLoaded(e *Env) bool {
	s := e.Samples(dstateLoadSpan)
	if len(s) < dstateLoadMin || e.now().Sub(s[0].At) < dstateLoadSpan*9/10 {
		return false
	}
	want := dstateLoadFrac * float64(e.CPUs)
	for _, x := range s {
		if x.Load1 < want || x.CPU >= dstateIdleCPU {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------------------------------
// time_sync: TLS and WireGuard handshakes need a sane clock. No fix in this phase.

func checkTimeSync(ctx context.Context, e *Env) Result {
	off := e.Offset()
	abs := int64(math.Abs(float64(off)))
	synced := "unknown"
	if !e.container() && e.has("timedatectl") { // a container's clock is the host's business
		if out, err := e.Run(ctx, "timedatectl", "show", "-p", "NTPSynchronized", "--value"); err == nil {
			synced = strings.TrimSpace(string(out))
		}
	}
	r := Result{Status: OK, Code: CodeTimeOffset, Params: p("offset_s", strconv.FormatInt(off, 10), "ntp_synced", synced),
		Detail: fmt.Sprintf("offset %ds against the panel, NTP synchronized: %s", off, synced)}
	switch {
	case abs > clockFailS:
		r.Status = Fail
	case abs > clockWarnS || synced == "no":
		r.Status = Warn
	}
	return r
}

// ---------------------------------------------------------------------------------------------------
// memory_pressure: no dedicated incident; the risk is RAM on small nodes (numbers unmeasured).

var oomKillRe = regexp.MustCompile(`Killed process \d+ \(([^)]+)\)`)
var oomAnyRe = regexp.MustCompile(`(?i)out of memory|oom-kill`)
var engineCommRe = regexp.MustCompile(`(?i)mistgate|hysteria|xray|sing-box|amnezia|awg|wireguard`)

func checkMemoryPressure(ctx context.Context, e *Env) Result {
	mi, err := e.read("/proc/meminfo")
	if err != nil {
		return skip(CodeMemNoInfo, "cannot read /proc/meminfo")
	}
	f := meminfo(mi)
	total, avail := f["MemTotal"], f["MemAvailable"]
	if total == 0 {
		return skip(CodeMemNoInfo, "MemTotal missing in /proc/meminfo")
	}
	availPct := int(avail * 100 / total)
	swapPct := 0
	if st := f["SwapTotal"]; st > 0 {
		swapPct = int((st - min(f["SwapFree"], st)) * 100 / st)
	}
	some, full, havePSI := -1.0, -1.0, false
	if s, err := e.read("/proc/pressure/memory"); err == nil {
		some, full = psiAvg60(s, "some"), psiAvg60(s, "full")
		havePSI = some >= 0
	}
	kills, victims, oomKnown := oomKills(kernelLog(ctx, e))
	agentKilled := false
	for _, v := range victims {
		if engineCommRe.MatchString(v) {
			agentKilled = true
		}
	}

	st := OK
	swapPressure := swapPct > swapWarnPct && (havePSI && some >= psiWarnSome || !havePSI && availPct < 25)
	if availPct < memWarnAvailPct || swapPressure || kills > 0 {
		st = Warn
	}
	if availPct < memFailAvailPct || havePSI && full >= psiFailFull || agentKilled {
		st = Fail
	}
	oom := strconv.Itoa(kills)
	if !oomKnown {
		oom = "unknown"
	}
	r := Result{Status: st, Code: CodeMemUsage, Params: p("avail_pct", strconv.Itoa(availPct), "swap_pct", strconv.Itoa(swapPct), "oom_kills", oom)}
	if havePSI {
		r.Params["psi_some"] = strconv.FormatFloat(some, 'f', 2, 64)
		r.Params["psi_full"] = strconv.FormatFloat(full, 'f', 2, 64)
	}
	if len(victims) > 0 {
		r.Params["oom_victims"] = strings.Join(firstN(sorted(uniq(victims)), 5), ",")
	}
	r.Detail = fmt.Sprintf("%d%% of RAM available, swap %d%% used, OOM kills in 24 h: %s", availPct, swapPct, oom)
	return r
}

// meminfo parses /proc/meminfo into bytes by field name (no colon).
func meminfo(s string) map[string]uint64 {
	m := map[string]uint64{}
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
			if len(f) >= 3 && f[2] == "kB" {
				n *= 1024
			}
			m[strings.TrimSuffix(f[0], ":")] = n
		}
	}
	return m
}

// psiAvg60 reads avg60 of the "some" or "full" line of /proc/pressure/*; -1 when absent.
func psiAvg60(s, kind string) float64 {
	for _, line := range strings.Split(s, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[0] != kind {
			continue
		}
		for _, kv := range f[1:] {
			if v, ok := strings.CutPrefix(kv, "avg60="); ok {
				if x, err := strconv.ParseFloat(v, 64); err == nil {
					return x
				}
			}
		}
	}
	return -1
}

// oomKills counts OOM-killer victims in the kernel log. known is false when the log could not be read.
func oomKills(k klogInfo) (n int, victims []string, known bool) {
	if k.err != nil {
		return 0, nil, false
	}
	anyOOM := 0
	for _, l := range k.lines {
		if m := oomKillRe.FindStringSubmatch(l); m != nil {
			n++
			victims = append(victims, m[1])
		} else if oomAnyRe.MatchString(l) {
			anyOOM++
		}
	}
	if n == 0 && anyOOM > 0 {
		n = 1 // an OOM report without a "Killed process" line: count the event once, victim unknown
	}
	return n, victims, true
}

// ---------------------------------------------------------------------------------------------------
// cpu_softirq: sustained softirq or CPU load. No fix.

func checkCPUSoftirq(_ context.Context, e *Env) Result {
	s := e.Samples(softirqWindow)
	if len(s) < softirqMinSamples {
		return skip(CodeCPUCollecting, fmt.Sprintf("collecting samples: %d of %d", len(s), softirqMinSamples),
			"samples", strconv.Itoa(len(s)), "need", strconv.Itoa(softirqMinSamples))
	}
	var soft, cpu float64
	for _, x := range s {
		soft += x.Softirq
		cpu += x.CPU
	}
	soft /= float64(len(s))
	cpu /= float64(len(s))
	r := Result{Status: OK, Code: CodeCPULoad, Params: p("softirq_pct", strconv.Itoa(int(math.Round(soft))), "cpu_pct", strconv.Itoa(int(math.Round(cpu))), "samples", strconv.Itoa(len(s))),
		Detail: fmt.Sprintf("softirq %.0f%%, CPU %.0f%% (mean of %d samples)", soft, cpu, len(s))}
	switch {
	case soft >= softirqFail || cpu >= cpuFail:
		r.Status = Fail
	case soft >= softirqWarn:
		r.Status = Warn
	}
	return r
}

// ---------------------------------------------------------------------------------------------------
// kernel_headers: the AmneziaWG kernel module is built by DKMS. No fix: installing packages is not in
// the safe set (the owner runs `mistgate-node awg prepare-kernel`). Nothing to say until the node serves AmneziaWG: a
// permanent warning nobody can act on is noise. The module is optional: auto and userspace mode run the userspace
// backend by design, so missing build tools are only a fact there (OK). It is a WARN only when the owner asked for
// kernel mode and the module is not running, because then the missing tools are what stands between the node and
// the mode it was told to use.

func checkKernelHeaders(_ context.Context, e *Env) Result {
	if !hasAWGInbound(e) {
		return skip(CodeHeadersNoAwg, "no AmneziaWG inbound on this node")
	}
	if e.container() {
		return skip(CodeHeadersContainer, "container host ("+e.Virt+"): userspace AmneziaWG only", "virt", e.Virt)
	}
	var missing []string
	rel, _ := e.read("/proc/sys/kernel/osrelease")
	rel = strings.TrimSpace(rel)
	if !headersPresent(e, rel) {
		missing = append(missing, "headers")
	}
	for _, tool := range []string{"dkms", "make"} {
		if !e.has(tool) {
			missing = append(missing, tool)
		}
	}
	if !e.has("gcc") && !e.has("cc") {
		missing = append(missing, "gcc")
	}
	if len(missing) == 0 {
		return Result{Status: OK, Code: CodeHeadersReady, Params: p("kernel", rel), Detail: "headers for " + rel + ", dkms, make and gcc are installed"}
	}
	mode, running := "auto", ""
	if e.AwgBackend != nil {
		b := e.AwgBackend()
		mode = orStr(b.Mode, "auto")
		if b.Available {
			running = b.Name
		}
	}
	r := Result{Status: OK, Params: p("kernel", rel, "missing", strings.Join(missing, ","), "mode", mode, "backend", running)}
	list := strings.Join(missing, ", ")
	switch {
	case mode == "kernel" && running != "kernel":
		r.Status, r.Code = Warn, CodeHeadersMissing
		r.Detail = "kernel " + rel + ": missing " + list
	case running == "userspace":
		r.Code = CodeHeadersUserspace
		r.Detail = "AmneziaWG runs in userspace; kernel mode needs " + list + " (kernel " + rel + ")"
	default:
		r.Code = CodeHeadersOptional
		r.Detail = "kernel mode of AmneziaWG needs " + list + " (kernel " + rel + ")"
	}
	return r
}

// The AmneziaWG protocol id is matched loosely, the way panel/health/eval.go does.
func hasAWGInbound(e *Env) bool {
	if e.Inbounds == nil {
		return false
	}
	for _, in := range e.Inbounds() {
		if in.Enabled && (strings.Contains(in.Protocol, "awg") || strings.Contains(in.Protocol, "amnezia")) {
			return true
		}
	}
	return false
}

func headersPresent(e *Env, release string) bool {
	if release == "" {
		return false
	}
	for _, d := range []string{"/lib/modules/" + release + "/build", "/usr/src/linux-headers-" + release} {
		if _, err := e.stat(d); err == nil {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------------------------------
// small helpers

func sorted(s []string) []string { sort.Strings(s); return s }

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func uniq(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}
