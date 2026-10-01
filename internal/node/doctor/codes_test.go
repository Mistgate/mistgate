package doctor

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// code asserts the detail code of a result and, as key, value pairs, the params its sentence is written from.
func code(t *testing.T, r Result, want string, kv ...string) {
	t.Helper()
	if r.Code != want {
		t.Errorf("%s: code = %q, want %q (%s)", r.ID, r.Code, want, r.Detail)
	}
	for i := 0; i+1 < len(kv); i += 2 {
		param(t, r, kv[i], kv[i+1])
	}
}

// The registry has no duplicates and every code belongs to a real check or is a skip.* one.
func TestCodeRegistry(t *testing.T) {
	seen := map[string]bool{}
	ids := CheckIDs()
	for _, c := range Codes {
		if seen[c] {
			t.Errorf("code %s is listed twice", c)
		}
		seen[c] = true
		id, _, ok := strings.Cut(c, ".")
		if !ok || id != "skip" && !slices.Contains(ids, id) {
			t.Errorf("code %s belongs to no check", c)
		}
	}
}

// Every code has wording in the admin UI (web/src/i18n/health.ts, both languages), so a new code cannot ship as
// raw English by accident. The test is skipped outside a full checkout.
func TestEveryCodeHasUIWording(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "i18n", "health.ts"))
	if err != nil {
		t.Skip("no web/ checkout:", err)
	}
	src := string(b)
	for _, c := range Codes {
		key := `"doctor.detail.` + c + `"`
		if n := strings.Count(src, key); n != 2 {
			t.Errorf("%s is in health.ts %d time(s), want 2 (en and ru)", key, n)
		}
	}
	// ... and the other way round: no wording for a code the agent never sends. The panel writes a few codes of its own
	// when it re-reads a report (internal/panel/health/doctor.go reassess); they are worded too.
	panelCodes := []string{"ipv6.none_warp_ipv4"}
	for _, m := range regexp.MustCompile(`"doctor\.detail\.([a-z0-9_]+\.[a-z0-9_]+)"`).FindAllStringSubmatch(src, -1) {
		if !slices.Contains(Codes, m[1]) && !slices.Contains(panelCodes, m[1]) {
			t.Errorf("health.ts words an unknown code %s", m[1])
		}
	}
}

// Skips that do not come from a check itself.
func TestRunnerSkipCodes(t *testing.T) {
	f := newFake(t)
	rep, _ := f.doctor(func(e *Env) { e.Unsupported = "not a Linux host" }).Run(context.Background(), []string{CheckDiskSpace})
	code(t, rep.Results[0], CodeSkipUnsupported, "reason", "not a Linux host")
	rep, _ = f.doctor().Run(context.Background(), []string{"nope"})
	code(t, rep.Results[0], CodeSkipUnknown)
}

// Codes and params of the checks that have no richer scenario elsewhere.
func TestDetailCodesAndParams(t *testing.T) {
	t.Run("disk_space", func(t *testing.T) {
		f := newFake(t)
		f.stat["/"] = statOf(100000, 9000, 1000, 900)
		code(t, run(t, f.doctor(), CheckDiskSpace), CodeDiskUsage, "mount", "/", "used_pct", "91", "free_mb", "9000", "inode_pct", "10")
		delete(f.stat, "/")
		code(t, run(t, f.doctor(), CheckDiskSpace), CodeDiskUnreadable)
	})
	t.Run("journald_size", func(t *testing.T) {
		f := newFake(t)
		code(t, run(t, f.doctor(), CheckJournaldSize), CodeJournalNone)
		f.putSized("/var/log/journal/m/a.journal", 400<<20)
		code(t, run(t, f.doctor(), CheckJournaldSize), CodeJournalSize, "journal_mb", "400", "cap_mb", "200")
	})
	t.Run("time_sync", func(t *testing.T) {
		f := newFake(t)
		f.bin("timedatectl")
		f.cmd("timedatectl show -p NTPSynchronized --value", "no\n", nil)
		code(t, run(t, f.doctor(func(e *Env) { e.Offset = func() int64 { return -1 } }), CheckTimeSync), CodeTimeOffset, "offset_s", "-1", "ntp_synced", "no")
	})
	t.Run("memory_pressure", func(t *testing.T) {
		f := newFake(t)
		code(t, run(t, f.doctor(), CheckMemoryPressure), CodeMemNoInfo)
		f.put("/proc/meminfo", "MemTotal: 1024000 kB\nMemAvailable: 512000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
		code(t, run(t, f.doctor(), CheckMemoryPressure), CodeMemUsage, "avail_pct", "50", "swap_pct", "0", "oom_kills", "unknown")
	})
	t.Run("dstate_tasks", func(t *testing.T) {
		f := newFake(t)
		code(t, run(t, f.doctor(), CheckDstateTasks), CodeDstateNoProc)
		f.put("/proc/1/stat", procStatLine(1, "systemd", 'S', 0, 0, 1))
		code(t, run(t, f.doctor(), CheckDstateTasks), CodeDstateNone, "stuck", "0")
		f.put("/proc/812/stat", procStatLine(812, "apt-get", 'D', 1, 0, 9000))
		code(t, run(t, f.doctor(), CheckDstateTasks), CodeDstateStuck, "stuck", "1", "tasks", "apt-get(812)")
	})
	t.Run("cpu_softirq", func(t *testing.T) {
		f := newFake(t)
		code(t, run(t, f.doctor(), CheckCPUSoftirq), CodeCPUCollecting, "samples", "0", "need", "30")
	})
	t.Run("resolver", func(t *testing.T) {
		f := newFake(t)
		code(t, run(t, f.doctor(lookups(nil, nil)), CheckResolver), CodeResolverOK, "domains", "3", "failed_count", "0")
		code(t, run(t, f.doctor(lookups([]string{"www.google.com"}, nil)), CheckResolver), CodeResolverFailed,
			"domains", "3", "failed_count", "1", "failed", "www.google.com")
	})
	t.Run("ipv6", func(t *testing.T) {
		f := newFake(t)
		warp := func(e *Env) {
			e.Inbounds = func() []Inbound { return []Inbound{{ID: "w", Enabled: true, Egress: "warp"}} }
		}
		code(t, run(t, f.doctor(), CheckIPv6), CodeIPv6None)
		code(t, run(t, f.doctor(warp), CheckIPv6), CodeIPv6NoneWarp, "warp_inbounds", "w")
	})
}
