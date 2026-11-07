package doctor

import (
	"strconv"
	"testing"
	"time"
)

func TestCPUSteal(t *testing.T) {
	mk := func(steal float64, n int) func(*Env) {
		return func(e *Env) {
			e.Samples = func(time.Duration) []Sample {
				s := make([]Sample, n)
				for i := range s {
					s[i] = Sample{At: t0.Add(-time.Duration(n-i) * 10 * time.Second), CPU: 40, Steal: steal}
				}
				return s
			}
		}
	}
	f := newFake(t)
	r := run(t, f.doctor(mk(60, 29)), CheckCPUSteal) // not enough samples yet: no verdict either way
	want(t, r, Skip, "")
	code(t, r, CodeStealCollecting, "samples", "29", "need", "30")
	want(t, run(t, f.doctor(mk(0, 60)), CheckCPUSteal), OK, "")
	want(t, run(t, f.doctor(mk(9.9, 60)), CheckCPUSteal), OK, "")
	want(t, run(t, f.doctor(mk(10, 60)), CheckCPUSteal), Warn, "")
	want(t, run(t, f.doctor(mk(24.9, 60)), CheckCPUSteal), Warn, "")
	r = run(t, f.doctor(mk(25, 60)), CheckCPUSteal)
	want(t, r, Fail, "")
	code(t, r, CodeStealLoad, "steal_pct", "25", "steal_max_pct", "25", "samples", "60")

	// The mean decides, not one spike: a minute of 90% in ten minutes of 2% is a neighbour's burst.
	spike := func(e *Env) {
		e.Samples = func(time.Duration) []Sample {
			s := make([]Sample, 60)
			for i := range s {
				s[i] = Sample{At: t0.Add(-time.Duration(60-i) * 10 * time.Second), Steal: 2}
				if i < 6 {
					s[i].Steal = 90
				}
			}
			return s
		}
	}
	r = run(t, f.doctor(spike), CheckCPUSteal)
	want(t, r, Warn, "") // (6*90 + 54*2) / 60 = 10.8
	param(t, r, "steal_pct", "11")
	param(t, r, "steal_max_pct", "90")
}

func TestConntrackFill(t *testing.T) {
	f := newFake(t)
	r := run(t, f.doctor(), CheckConntrackFill)
	want(t, r, Skip, "") // no nf_conntrack: nothing that can fill
	code(t, r, CodeConntrackNotLoaded)

	set := func(count, limit int) {
		f.put("/proc/sys/net/netfilter/nf_conntrack_count", strconv.Itoa(count)+"\n")
		f.put("/proc/sys/net/netfilter/nf_conntrack_max", strconv.Itoa(limit)+"\n")
	}
	set(1000, 262144)
	r = run(t, f.doctor(), CheckConntrackFill)
	want(t, r, OK, "")
	code(t, r, CodeConntrackFill, "count", "1000", "max", "262144", "fill_pct", "0")
	set(79, 100)
	want(t, run(t, f.doctor(), CheckConntrackFill), OK, "")
	set(80, 100)
	want(t, run(t, f.doctor(), CheckConntrackFill), Warn, "")
	set(95, 100)
	want(t, run(t, f.doctor(), CheckConntrackFill), Fail, "")
	set(130, 100) // the count can pass the limit for a moment; the percentage stops at 100
	r = run(t, f.doctor(), CheckConntrackFill)
	want(t, r, Fail, "")
	param(t, r, "fill_pct", "100")
	set(10, 0)
	want(t, run(t, f.doctor(), CheckConntrackFill), Skip, "")
	f.put("/proc/sys/net/netfilter/nf_conntrack_max", "garbage\n")
	want(t, run(t, f.doctor(), CheckConntrackFill), Skip, "")
}

func TestAgentFDs(t *testing.T) {
	f := newFake(t)
	r := run(t, f.doctor(), CheckAgentFDs)
	want(t, r, Skip, "")
	code(t, r, CodeFDsUnknown)

	limits := func(soft string) {
		f.put("/proc/100/limits", "Limit                     Soft Limit           Hard Limit           Units\n"+
			"Max open files            "+soft+"                4096                 files\n")
	}
	open := func(n int) {
		f.remove("/proc/100/fd")
		for i := range n {
			f.put("/proc/100/fd/"+strconv.Itoa(i), "")
		}
	}
	limits("100")
	open(10)
	r = run(t, f.doctor(), CheckAgentFDs)
	want(t, r, OK, "")
	code(t, r, CodeFDsUsage, "fds", "10", "limit", "100", "used_pct", "10")
	open(80)
	want(t, run(t, f.doctor(), CheckAgentFDs), Warn, "")
	open(95)
	want(t, run(t, f.doctor(), CheckAgentFDs), Fail, "")
	limits("unlimited") // not known: no verdict
	want(t, run(t, f.doctor(), CheckAgentFDs), Skip, "")

	// SelfPID 0 reads /proc/self.
	g := newFake(t)
	g.put("/proc/self/limits", "Max open files            1000                4096                 files\n")
	g.put("/proc/self/fd/0", "")
	r = run(t, g.doctor(func(e *Env) { e.SelfPID = 0 }), CheckAgentFDs)
	want(t, r, OK, "")
	param(t, r, "fds", "1")
}
