// Package doctor is the node's self-diagnosis: sixteen host
// checks that each come from a real incident, and four safe fixes the panel may ask for.
//
// Rules the package keeps:
//
//   - Read-only by default. A check only reads /proc, /sys, a few config files and runs read-only commands
//     (systemctl list-*, nft list, timedatectl show, dmesg, journalctl -k). Only Apply changes anything, only
//     for the four compiled fix ids, never from a string the panel sends (params are validated), and a dry run
//     changes nothing.
//   - A check that cannot run here (container, no systemd, not root, not Linux) is SKIP with a reason, never
//     a false OK.
//   - Runs are bounded: every check has its own 10 s budget and the whole run 30 s, output of external
//     commands is capped, a hung or panicking check becomes a SKIP. One run at a time (ErrBusy).
//   - The package knows nothing about the agent: everything it needs from the agent (inbounds, settings, the
//     stats ring, the clock offset) and from the machine (files, commands, sockets) arrives through Env, so
//     tests run against a fake root directory and fake command runners.
//
// Thresholds are constants in thresholds.go.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

// Status mirrors agent.v1.DoctorStatus (values 1..4), so the agent converts with a plain cast.
type Status int

const (
	OK   Status = 1
	Warn Status = 2
	Fail Status = 3
	Skip Status = 4
)

func (s Status) String() string {
	switch s {
	case OK:
		return "OK"
	case Warn:
		return "WARN"
	case Fail:
		return "FAIL"
	case Skip:
		return "SKIP"
	}
	return "?"
}

// Check ids, in the order a full report lists them (agent.proto "CHECK IDS"). Append only.
const (
	CheckDiskSpace      = "disk_space"
	CheckJournaldSize   = "journald_size"
	CheckDstateTasks    = "dstate_tasks"
	CheckTimeSync       = "time_sync"
	CheckResolver       = "resolver"
	CheckIPv6           = "ipv6"
	CheckForeignVPN     = "foreign_vpn"
	CheckForeignNft     = "foreign_nft"
	CheckPortConflicts  = "port_conflicts"
	CheckNetBaseline    = "net_baseline"
	CheckCertExpiry     = "cert_expiry"
	CheckMemoryPressure = "memory_pressure"
	CheckCPUSoftirq     = "cpu_softirq"
	CheckKernelHeaders  = "kernel_headers"
	CheckAwgBackend     = "awg_backend"
	CheckWarpPath       = "warp_path"
)

// Fix ids (agent.proto "FIX IDS"). Only these four exist.
const (
	FixJournaldVacuum = "journald_vacuum"
	FixApplyBaseline  = "apply_baseline"
	FixRestartInbound = "restart_inbound"
	FixSetResolver    = "set_resolver"
)

// Result is one check's outcome; it becomes one DoctorResult.
type Result struct {
	ID     string
	Status Status
	// Detail is a short English fact line (at most 200 bytes).
	Detail string
	// Code names the fact line for the UI (codes.go): it writes the localized sentence from Code and Params.
	Code string
	// Params fill i18n placeholders; numbers are plain decimal strings, lists are comma separated.
	Params map[string]string
	// FixID is the fix offered (only with WARN/FAIL); empty = none.
	FixID    string
	Measured time.Time
}

// TitleKey is the i18n key of the one-line title.
func (r Result) TitleKey() string { return "doctor." + r.ID + ".title" }

// Report is the outcome of Run.
type Report struct {
	Results  []Result
	Partial  bool // only a subset of the checks ran
	Duration time.Duration
}

// ErrBusy is returned by Run while another run is in progress.
var ErrBusy = errors.New("busy")

type check struct {
	id string
	fn func(ctx context.Context, e *Env) Result
}

// checks lists every check in report order.
var checks = []check{
	{CheckDiskSpace, checkDiskSpace},
	{CheckJournaldSize, checkJournaldSize},
	{CheckDstateTasks, checkDstateTasks},
	{CheckTimeSync, checkTimeSync},
	{CheckResolver, checkResolver},
	{CheckIPv6, checkIPv6},
	{CheckForeignVPN, checkForeignVPN},
	{CheckForeignNft, checkForeignNft},
	{CheckPortConflicts, checkPortConflicts},
	{CheckNetBaseline, checkNetBaseline},
	{CheckCertExpiry, checkCertExpiry},
	{CheckMemoryPressure, checkMemoryPressure},
	{CheckCPUSoftirq, checkCPUSoftirq},
	{CheckKernelHeaders, checkKernelHeaders},
	{CheckAwgBackend, checkAwgBackend},
	{CheckWarpPath, checkWarpPath},
}

// CheckIDs returns every check id in report order.
func CheckIDs() []string {
	ids := make([]string, len(checks))
	for i, c := range checks {
		ids[i] = c.id
	}
	return ids
}

// FixIDs returns the four fix ids.
func FixIDs() []string {
	return []string{FixJournaldVacuum, FixApplyBaseline, FixRestartInbound, FixSetResolver}
}

// fixChecks says which checks offer a fix; the agent re-runs them after the fix (agent.proto "ApplyFix").
var fixChecks = map[string][]string{
	FixJournaldVacuum: {CheckJournaldSize, CheckDiskSpace},
	FixApplyBaseline:  {CheckNetBaseline},
	FixRestartInbound: {CheckPortConflicts, CheckCertExpiry},
	FixSetResolver:    {CheckResolver},
}

// RecheckIDs returns the checks to re-run after a fix, nil for an unknown fix id.
func RecheckIDs(fixID string) []string { return append([]string(nil), fixChecks[fixID]...) }

// Doctor runs the checks and the fixes. Create it with New.
type Doctor struct {
	env Env

	running atomic.Bool // one Run at a time
	fixing  atomic.Bool // one Apply at a time

	mu   sync.Mutex
	prev map[string]Result // last result per check, for rules that need "seen at two consecutive runs"
}

// New builds a doctor over env. Nil function fields in env get safe defaults.
func New(env Env) *Doctor {
	env.defaults()
	return &Doctor{env: env, prev: map[string]Result{}}
}

// Run executes the checks in ids (empty = all, a full report) and returns one Result per requested id, in
// report order for a full run and in request order otherwise. An unknown id gives SKIP "unknown check".
// It returns ErrBusy while another run is in progress.
func (d *Doctor) Run(ctx context.Context, ids []string) (*Report, error) {
	if !d.running.CompareAndSwap(false, true) {
		return nil, ErrBusy
	}
	defer d.running.Store(false)

	start := time.Now()
	full := len(ids) == 0
	var todo []check
	var unknown []string
	if full {
		todo = checks
	} else {
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			if c, ok := lookup(id); ok {
				todo = append(todo, c)
			} else {
				unknown = append(unknown, id)
			}
		}
	}

	rctx, cancel := context.WithTimeout(ctx, runBudget)
	defer cancel()
	env := d.env
	env.memo = &memo{}
	env.Prev = d.previous

	results := make([]Result, len(todo))
	var wg sync.WaitGroup
	for i, c := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = runOne(rctx, &env, c)
		}()
	}
	wg.Wait()

	rep := &Report{Results: results, Partial: !full, Duration: time.Since(start)}
	if !full {
		// Request order, unknown ids as SKIP at their place.
		byID := map[string]Result{}
		for _, r := range results {
			byID[r.ID] = r
		}
		rep.Results = rep.Results[:0:0]
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			if r, ok := byID[id]; ok {
				rep.Results = append(rep.Results, r)
			} else {
				rep.Results = append(rep.Results, Result{ID: id, Status: Skip, Detail: "unknown check", Code: CodeSkipUnknown, Measured: env.now()})
			}
		}
	}
	d.mu.Lock()
	for _, r := range rep.Results {
		d.prev[r.ID] = r
	}
	d.mu.Unlock()
	return rep, nil
}

func lookup(id string) (check, bool) {
	for _, c := range checks {
		if c.id == id {
			return c, true
		}
	}
	return check{}, false
}

func (d *Doctor) previous(id string) (Result, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.prev[id]
	return r, ok
}

// runOne runs a check under its own budget. A check that ignores its context, or panics, still yields a
// Result; the stuck goroutine ends when the check returns (the channel is buffered).
func runOne(ctx context.Context, e *Env, c check) Result {
	if e.Unsupported != "" {
		return finish(e, c.id, skip(CodeSkipUnsupported, e.Unsupported, "reason", e.Unsupported))
	}
	cctx, cancel := context.WithTimeout(ctx, checkBudget)
	defer cancel()
	ch := make(chan Result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- Result{ID: c.id, Status: Skip, Detail: fmt.Sprintf("internal error: %v", p), Code: CodeSkipInternal}
			}
		}()
		ch <- c.fn(cctx, e)
	}()
	var r Result
	select {
	case r = <-ch:
	case <-cctx.Done():
		r = Result{ID: c.id, Status: Skip, Detail: "timed out", Code: CodeSkipTimeout}
	}
	return finish(e, c.id, r)
}

// finish normalises a result: id, time, a clipped detail, a fix only with WARN/FAIL.
func finish(e *Env, id string, r Result) Result {
	r.ID = id
	r.Measured = e.now()
	r.Detail = clip(r.Detail, 200)
	if r.Status != Warn && r.Status != Fail {
		r.FixID = ""
	}
	if r.Status == 0 {
		r.Status = Skip
	}
	return r
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) { // the cut is inside a multi-byte rune: back up to its start
		n--
	}
	return s[:n]
}

// p builds a params map from key, value pairs; empty values are dropped.
func p(kv ...string) map[string]string {
	m := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			m[kv[i]] = kv[i+1]
		}
	}
	return m
}

// skip is a SKIP result: code names the line for the UI, why is the English detail, kv the params.
func skip(code, why string, kv ...string) Result {
	return Result{Status: Skip, Code: code, Detail: why, Params: p(kv...)}
}

func csv(ss []string) string {
	sort.Strings(ss)
	return strings.Join(ss, ",")
}
