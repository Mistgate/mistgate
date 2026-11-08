package doctor

import (
	"context"
	"fmt"
	"math"
	"strconv"

	"github.com/mistgate/mistgate/internal/sysstat"
)

// Load checks: what the machine is short of under the node's own load, as opposed to what is broken. None has a fix: the
// way out is fewer people on the node, another host, or a limit the owner raises himself.

// ---------------------------------------------------------------------------------------------------
// cpu_steal: the hypervisor gives this guest's CPU time to other guests. A cheap VPS on an oversold host shows it as slow
// handshakes and jitter that nothing on the node explains. The way out is another host or provider.

func checkCPUSteal(_ context.Context, e *Env) Result {
	s := e.Samples(softirqWindow)
	if len(s) < softirqMinSamples {
		return skip(CodeStealCollecting, fmt.Sprintf("collecting samples: %d of %d", len(s), softirqMinSamples),
			"samples", strconv.Itoa(len(s)), "need", strconv.Itoa(softirqMinSamples))
	}
	var sum, peak float64
	for _, x := range s {
		sum += x.Steal
		peak = max(peak, x.Steal)
	}
	mean := sum / float64(len(s))
	r := Result{Status: OK, Code: CodeStealLoad,
		Params: p("steal_pct", strconv.Itoa(int(math.Round(mean))), "steal_max_pct", strconv.Itoa(int(math.Round(peak))), "samples", strconv.Itoa(len(s))),
		Detail: fmt.Sprintf("steal %.0f%% of CPU time (mean of %d samples, peak %.0f%%)", mean, len(s), peak)}
	switch {
	case mean >= stealFail:
		r.Status = Fail
	case mean >= stealWarn:
		r.Status = Warn
	}
	return r
}

// ---------------------------------------------------------------------------------------------------
// conntrack_fill: the kernel's connection-tracking table. When it is full the kernel drops every new connection
// ("nf_conntrack: table full, dropping packet"), which looks to people like a node that stopped answering while the
// ones already connected carry on. Nothing to check where conntrack is not loaded. It owns the verdict on the fill;
// net_baseline only reports it next to the table's size.

func checkConntrackFill(_ context.Context, e *Env) Result {
	ct, dropsOK := readConntrackSnapshot(e) // the same reading net_baseline reports in its params
	if !ct.hasCountAndMax || ct.max == 0 {
		return skip(CodeConntrackNotLoaded, "connection tracking is not loaded")
	}
	count, limit := ct.count, ct.max
	pct := int(min(count/limit*100+count%limit*100/limit, 100)) // count*100 could overflow a bogus count
	r := Result{Status: OK, Code: CodeConntrackFill,
		Params: p("count", strconv.FormatUint(count, 10), "max", strconv.FormatUint(limit, 10), "fill_pct", strconv.Itoa(pct)),
		Detail: fmt.Sprintf("connection tracking %d of %d entries (%d%%)", count, limit, pct)}
	if dropsOK { // since boot; kernels without CONFIG_NF_CONNTRACK_PROCFS (Ubuntu's) do not count them
		r.Params["drops"] = strconv.FormatUint(ct.drops, 10)
	}
	switch {
	case pct >= conntrackFailPct:
		r.Status = Fail
	case pct >= conntrackWarnPct:
		r.Status = Warn
	}
	return r
}

// ---------------------------------------------------------------------------------------------------
// agent_fds: every connection of every engine is a descriptor of the agent process. At the soft limit new connections
// fail (EMFILE) while the node still looks alive. The unit sets a high limit; a manual run under a shell's 1024 does not.

func checkAgentFDs(_ context.Context, e *Env) Result {
	pid := "self"
	if e.SelfPID > 0 {
		pid = strconv.Itoa(e.SelfPID)
	}
	lim, err := e.read("/proc/" + pid + "/limits")
	limit := sysstat.ParseOpenFilesLimit(lim)
	ents, derr := e.readDir("/proc/" + pid + "/fd")
	if err != nil || derr != nil || limit == 0 {
		return skip(CodeFDsUnknown, "cannot read the agent's open files or their limit")
	}
	fds := uint64(len(ents))
	pct := int(min(fds*100/limit, 100))
	r := Result{Status: OK, Code: CodeFDsUsage,
		Params: p("fds", strconv.FormatUint(fds, 10), "limit", strconv.FormatUint(limit, 10), "used_pct", strconv.Itoa(pct)),
		Detail: fmt.Sprintf("%d of %d open files (%d%%)", fds, limit, pct)}
	switch {
	case pct >= fdsFailPct:
		r.Status = Fail
	case pct >= fdsWarnPct:
		r.Status = Warn
	}
	return r
}
