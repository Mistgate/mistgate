package agent

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseExitFacts(t *testing.T) {
	for _, c := range []struct {
		name               string
		show, unit, kernel string
		want               exitFacts
	}{
		{"nothing readable", "", "", "", exitFacts{Restarts: -1}},
		{"killed by the OOM killer, systemd's word", "NRestarts=4\nResult=oom-kill\n",
			"Main process exited, code=killed, status=9/KILL\nFailed with result 'oom-kill'.\n", "",
			exitFacts{Restarts: 4, Result: "oom-kill", Code: "killed", Status: "9", Signal: "KILL", OOM: true}},
		{"killed, the kernel's line says memory", "NRestarts=1\nResult=signal\n",
			"mistgate-node.service: Main process exited, code=killed, status=9/KILL\n", "Out of memory: Killed process 4242 (mistgate-node) total-vm:1kB\n",
			exitFacts{Restarts: 1, Result: "signal", Code: "killed", Status: "9", Signal: "KILL", OOM: true}},
		{"another process of the host killed for memory is not ours", "NRestarts=0\n", "", "Out of memory: Killed process 77 (postgres)\n",
			exitFacts{Restarts: 0}},
		{"exit code 1, the newest lines win", "NRestarts=2\nResult=exit-code\n",
			"Main process exited, code=killed, status=11/SEGV\nStarted.\nMain process exited, code=exited, status=1/FAILURE\nFailed with result 'exit-code'.\n", "",
			exitFacts{Restarts: 2, Result: "exit-code", Code: "exited", Status: "1"}},
		{"a crash with a core", "Result=core-dump\n", "Main process exited, code=dumped, status=11/SEGV\n", "",
			exitFacts{Restarts: -1, Result: "core-dump", Code: "dumped", Status: "11", Signal: "SEGV"}},
		{"a clean systemd result is no news", "NRestarts=0\nResult=success\n", "", "", exitFacts{Restarts: 0}},
	} {
		if got := parseExitFacts(c.show, c.unit, c.kernel); got != c.want {
			t.Errorf("%s:\n got  %+v\n want %+v", c.name, got, c.want)
		}
	}
}

// startWithProbe starts a fresh agent process on the harness's state directory with a fake systemd reader and waits for its
// agent_started event, returning the params.
func (h *harness) startWithProbe(probe exitProbe) map[string]string {
	h.t.Helper()
	before := len(h.panel.eventsByCode("agent_started"))
	h.a = h.newAgent()
	h.a.exitProbe = probe
	h.start()
	eventually(h.t, func() bool { return len(h.panel.eventsByCode("agent_started")) > before }, "agent_started")
	return h.panel.eventsByCode("agent_started")[before].Params
}

func TestAgentStartedSaysHowThePreviousRunEnded(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) { c.Version = "0.1.0-a" }})
	probeCalls := 0
	oom := func(context.Context) exitFacts {
		probeCalls++
		return exitFacts{Restarts: 4, Result: "oom-kill", Code: "killed", Status: "9", Signal: "KILL", OOM: true}
	}
	marker := filepath.Join(h.dir, fileCleanExit)

	// The first start in a directory has no previous run.
	p := h.startWithProbe(oom)
	if p["prev_exit"] != "unknown" || p["crash"] != "" || p["crash_loop"] != "" || probeCalls != 0 {
		t.Fatalf("first start: %v (probe %d)", p, probeCalls)
	}
	// A stop writes the marker; the next start finds it, consumes it and is no crash.
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("no clean marker after a stop: %v", err)
	}
	p = h.startWithProbe(oom)
	if p["prev_exit"] != "clean" || p["crash"] != "" || probeCalls != 0 {
		t.Fatalf("after a clean stop: %v (probe %d)", p, probeCalls)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the clean marker was not consumed: a crash of this run would read as clean")
	}

	// Three runs that end without the marker (killed): crash restarts, counted in a loop, with what systemd says.
	for i := 1; i <= 3; i++ {
		h.cancelWithoutMarker()
		p = h.startWithProbe(oom)
		if p["prev_exit"] != "unclean" || p["crash"] != "1" || p["crash_loop"] != string(rune('0'+i)) || p["reason"] != reasonRestart {
			t.Fatalf("crash %d: %v", i, p)
		}
		if p["exit_signal"] != "KILL" || p["exit_result"] != "oom-kill" || p["oom"] != "1" || p["systemd_restarts"] != "4" || p["exit_code"] != "" {
			t.Fatalf("crash %d facts: %v", i, p)
		}
	}
	if probeCalls != 3 {
		t.Errorf("probe ran %d times, want only for crashes", probeCalls)
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, fileCrashLog)); strings.Count(string(b), "\n") != 3 {
		t.Errorf("crash log = %q", b)
	}
}

// cancelWithoutMarker ends the running agent and removes the marker it wrote, as if the process had been killed.
func (h *harness) cancelWithoutMarker() {
	h.t.Helper()
	if err := h.stop(); err != nil {
		h.t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(h.dir, fileCleanExit)); err != nil {
		h.t.Fatal(err)
	}
}

func TestPreviousExitBootUpdateAndWindow(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	a := h.a
	ctx := context.Background()
	a.exitProbe = func(context.Context) exitFacts { return exitFacts{Restarts: 2} }

	// No marker, a previous version, the host just booted: power loss or a reset, not an agent crash.
	p := a.previousExit(ctx, true, false, reasonBoot)
	if p["prev_exit"] != "unclean" || p["crash"] != "" || p["crash_loop"] != "" {
		t.Errorf("boot: %v", p)
	}
	// An update that replaced the process on purpose is clean even without the marker (the old build wrote none).
	if p := a.previousExit(ctx, true, true, reasonUpdate); p["prev_exit"] != "clean" || p["crash"] != "" {
		t.Errorf("update: %v", p)
	}
	// Crashes older than the window drop out of the count; the probe's NRestarts is passed along.
	old := a.now().Add(-crashLoopWindow - time.Minute).Unix()
	if err := os.WriteFile(filepath.Join(h.dir, fileCrashLog), []byte(strconv.FormatInt(old, 10)+"\n"+strconv.FormatInt(a.now().Add(-time.Minute).Unix(), 10)+"\nnot a time\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p = a.previousExit(ctx, true, false, reasonRestart)
	if p["crash_loop"] != "2" || p["systemd_restarts"] != "2" || p["exit_code"] != "" || p["oom"] != "" {
		t.Errorf("window: %v", p)
	}
}
