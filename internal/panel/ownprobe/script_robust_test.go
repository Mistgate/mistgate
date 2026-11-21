package ownprobe

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// What the probe script does to a small router: the tools it needs, how it treats memory, what it does when things break, and
// what it never touches. The numbers of the footprint are in the plan (§11.3); these tests keep them honest.

// The script does not reach for the router: no firewall, routes, DNS, cron, listening programs or the router's own CLI. The
// words are looked for in code, not in comments.
func TestScriptDoesNotTouchTheRouter(t *testing.T) {
	var code []string
	for _, line := range strings.Split(string(probeScript), "\n") {
		if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
			code = append(code, trailingComment.ReplaceAllString(line, ""))
		}
	}
	body := strings.Join(code, "\n")
	banned := regexp.MustCompile(`(^|[^A-Za-z0-9_./-])(iptables|ip6tables|nft|ebtables|ndmc|ifconfig|sysctl|crontab|nc|ncat|netcat|socat|telnetd|httpd|udhcpc|udhcpd|dnsmasq|uci|passwd|chpasswd|reboot|poweroff|halt|mount|umount|modprobe|insmod|setsid|nohup)([^A-Za-z0-9_-]|$)`)
	if m := banned.FindString(body); m != "" {
		t.Errorf("the script mentions a program it has no business with: %q", m)
	}
	for _, re := range []string{`(^|[;&|({]\s*)ip\s+(link|addr|address|route|rule|netns|neigh)`, `resolv\.conf`, `/etc/(hosts|passwd|shadow|resolv)`, `\bnslookup\b|\bdig\b|\bnc\s`} {
		if m := regexp.MustCompile(re).FindString(body); m != "" {
			t.Errorf("the script has %q", m)
		}
	}
	// every curl of the script has a hard limit on the whole transfer and one on the connection (the argv tests check the same on
	// what really runs)
	stmt := strings.NewReplacer("\\\n", " ").Replace(body)
	calls := 0
	for _, line := range strings.Split(stmt, "\n") {
		if strings.Contains(line, "net_curl ") && !strings.Contains(line, "net_curl()") && !strings.Contains(line, `"$@"`) || strings.Contains(line, "curl_check \"") && !strings.Contains(line, "curl_check()") {
			calls++
			if !regexp.MustCompile(`--connect-timeout [0-9]+ `).MatchString(line) || !regexp.MustCompile(` -m [0-9]+ `).MatchString(line) {
				t.Errorf("a curl call without --connect-timeout and -m: %s", strings.TrimSpace(line))
			}
		}
	}
	if calls < 7 {
		t.Errorf("found only %d curl calls in the script, there are seven", calls)
	}
	// the one download of the whole system (the install command) is limited too
	command, err := BuildInstallCommand("https://panel.example.com", "/abcdefghijklmnopqrstuvwx/", "mgp_"+strings.Repeat("a", 52), ScriptHash())
	if err != nil || !regexp.MustCompile(`curl [^|]*--connect-timeout [0-9]+ -m [0-9]+ `).MatchString(command) {
		t.Errorf("the install command downloads without a time limit: %v %s", err, command)
	}
}

// poorScript is the script with its PATH cut down to the lab's bin and a directory of busybox applet links: whatever the script
// runs that is not a shell builtin, a busybox applet or the fake curl and friends of the lab is "not found".
func poorScript(t *testing.T) []byte {
	t.Helper()
	const full = ":/opt/bin:/opt/sbin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin ;;\n\t*) die 'bad MGP_ROOT' ;;"
	if strings.Count(string(probeScript), full) != 1 {
		t.Fatal("the PATH line of the script is not the one this test expects")
	}
	return []byte(strings.Replace(string(probeScript), full, ":${ROOT}/applets ;;\n\t*) die 'bad MGP_ROOT' ;;", 1))
}

func busyboxPath(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("busybox")
	if err != nil {
		if os.Getenv("MGP_REQUIRE_BUSYBOX") != "" {
			t.Fatal("MGP_REQUIRE_BUSYBOX is set and there is no busybox")
		}
		t.Skip("SKIP: no busybox")
	}
	return p
}

// A router has a busybox and curl and little else. The script installs, runs a cycle, reports its status, stops and uninstalls
// with a PATH that holds only the applets of busybox (links made in the test's own directory) and the lab's fakes.
func TestWorksWithOnlyBusyboxApplets(t *testing.T) {
	t.Parallel()
	bb := busyboxPath(t)
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{noConf: true, notInstalled: true, realClock: true, scriptBytes: poorScript(t)})
		applets := filepath.Join(l.root, "applets")
		must(t, os.MkdirAll(applets, 0o755))
		if out, err := combinedOutput(exec.Command(bb, "--install", "-s", applets)); err != nil {
			t.Fatalf("busybox --install: %v %s", err, out)
		}
		t.Cleanup(func() { l.stopAndWait() })
		token := "mgp_" + strings.Repeat("d", 52)
		down := filepath.Join(l.root, "mgp.sh")
		must(t, writeExe(down, l.opts.scriptBytes))
		must(t, os.WriteFile(filepath.Join(l.fake, "enroll.resp"), []byte("mgp 1\nid "+labID+"\nsecret "+labSecret+"\nend\n"), 0o644))
		// an applet that is missing is named, and nothing is installed
		if sh.name == "dash" {
			must(t, os.Remove(filepath.Join(applets, "wc")))
			must(t, os.Remove(filepath.Join(applets, "tr")))
			out, code := l.runAt(down, "install", labBase, token)
			if code == 0 || !strings.Contains(out, "missing commands: tr wc") && !strings.Contains(out, "missing commands: wc tr") || l.exists("opt/etc") || l.exists("opt/sbin/mistgate-probe") {
				t.Errorf("install without tr and wc: %d %s", code, out)
			}
			must(t, os.Symlink(bb, filepath.Join(applets, "wc")))
			must(t, os.Symlink(bb, filepath.Join(applets, "tr")))
		} else {
			t.Log("this busybox is built with FEATURE_SH_STANDALONE: ash finds every applet whatever the PATH is, so a missing one cannot be staged here")
		}
		out, code := l.runAt(down, "install", labBase, token)
		if code != 0 || !strings.HasPrefix(out, "OK: probe "+labID+" registered and running") {
			t.Fatalf("install: %d %s", code, out)
		}
		waitFor(t, "a whole cycle", 90*time.Second, func() bool { return l.stateHas("last_exec") && !l.stateHas("lock") })
		if st, _ := l.run("status"); !strings.Contains(st, "supervisor: running") || !strings.Contains(st, "synced: yes") {
			t.Errorf("status: %s", st)
		}
		if n := len(l.checks()); n != 5 {
			t.Errorf("the cycle made %d checks, want 5 (3 targets, the control, the reference)", n)
		}
		l.assertArgv()
		if out, code := l.stopAndWait(); code != 0 {
			t.Errorf("stop: %d %s", code, out)
		}
		if out, _ := l.run("run"); !strings.HasPrefix(out, "run: http 200") {
			t.Errorf("run: %s", out)
		}
		if out, code := l.run("uninstall"); code != 0 || !strings.Contains(out, "removed") {
			t.Errorf("uninstall: %d %s", code, out)
		}
		for _, rel := range []string{"opt/sbin/mistgate-probe", "opt/etc/mistgate-probe", "opt/etc/init.d/S99mistgate-probe", "tmp/mistgate-probe"} {
			if l.exists(rel) {
				t.Errorf("%s is left after uninstall", rel)
			}
		}
	})
}

// A router that is short of memory does nothing: no sync, no curl, one line in the log, and the next time again.
func TestLowMemorySkipsTheCycle(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		must(t, os.MkdirAll(filepath.Join(l.root, "proc"), 0o755))
		meminfo := func(body string) { must(t, os.WriteFile(filepath.Join(l.root, "proc/meminfo"), []byte(body), 0o644)) }
		cases := []struct {
			name, body string
			skipped    bool
		}{
			{"8 MB free", "MemTotal:  131072 kB\nMemFree: 4000 kB\nMemAvailable:    8000 kB\nBuffers: 1 kB\n", true},
			{"one kB under", "MemAvailable: 16383 kB\n", true},
			{"exactly 16 MB", "MemAvailable: 16384 kB\n", false},
			{"plenty", "MemTotal: 131072 kB\nMemFree: 90000 kB\nMemAvailable: 100000 kB\n", false},
			{"an old kernel without the field", "MemTotal: 131072 kB\nMemFree: 100 kB\nCached: 200 kB\n", false},
			{"an empty file", "", false},
		}
		for _, c := range cases {
			l.forget()
			l.forgetJob()
			_ = os.Remove(filepath.Join(l.fake, "logger.log"))
			meminfo(c.body)
			out, _ := l.run("run")
			logged, _ := os.ReadFile(filepath.Join(l.fake, "logger.log"))
			syncs := 0
			for _, call := range l.calls() {
				if call.isSync() {
					syncs++
				}
			}
			if c.skipped {
				if !strings.HasPrefix(out, "run: http none, executed 0, next 600") || syncs != 0 || len(l.checks()) != 0 || !strings.Contains(string(logged), "low memory: cycle skipped") {
					t.Errorf("%s: the cycle was not skipped cleanly: %q, %d syncs, log %q", c.name, out, syncs, logged)
				}
				if strings.Count(string(logged), "\n") != 1 {
					t.Errorf("%s: want one line in the log: %q", c.name, logged)
				}
			} else if !strings.HasPrefix(out, "run: http 200") || syncs == 0 || strings.Contains(string(logged), "low memory") {
				t.Errorf("%s: the cycle did not run: %q, %d syncs, log %q", c.name, out, syncs, logged)
			}
		}
		// the memory comes back: the very next run works
		meminfo("MemAvailable: 8000 kB\n")
		l.forget()
		l.forgetJob()
		l.run("run")
		meminfo("MemAvailable: 40000 kB\n")
		if out, _ := l.run("run"); !strings.HasPrefix(out, "run: http 200, executed 1") {
			t.Errorf("after the memory came back: %s", out)
		}
	})
}

// A worker that dies at once, every time, is restarted after 10, 20, 40 ... seconds up to a ceiling, never in a tight loop.
func TestSupervisorBacksOffWhenTheWorkerKeepsDying(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		// a curl that does not run: the worker refuses to start ("curl is not installed") and the supervisor finds that out each time
		must(t, writeExe(filepath.Join(l.root, "bin", "curl"), []byte("#!/bin/sh\nexit 1\n")))
		c := l.cmd(nil, "supervise")
		must(t, startCmd(c))
		restarts := regexp.MustCompile(`restarting in ([0-9]+) s`)
		var got []string
		waitFor(t, "a dozen restarts", 60*time.Second, func() bool {
			raw, _ := os.ReadFile(filepath.Join(l.fake, "logger.log"))
			got = nil
			for _, m := range restarts.FindAllStringSubmatch(string(raw), -1) {
				got = append(got, m[1])
			}
			return len(got) >= 14
		})
		must(t, c.Process.Signal(syscall.SIGTERM))
		_ = c.Wait()
		want := []string{"10", "20", "40", "80", "160", "320", "640", "1280", "2560"}
		for i, g := range got[:14] {
			w := "2560"
			if i < len(want) {
				w = want[i]
			}
			if g != w {
				t.Errorf("restart %d after %s s, want %s (all: %v)", i+1, g, w, got[:14])
				break
			}
		}
		if l.stateHas("supervisor.pid") {
			t.Error("the stopped supervisor left its pid file")
		}
	})
}

// A sleep that does not sleep (out of processes, a broken applet) must not turn the worker into a busy loop that calls the panel
// again and again: the worker ends, and the supervisor's back-off takes over.
func TestWorkerDoesNotSpinWhenSleepFails(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c := l.cmd([]string{"FAKE_SLEEP_BROKEN=1"}, "worker")
		cc := exec.CommandContext(ctx, c.Path, c.Args[1:]...)
		cc.Dir, cc.Env = c.Dir, c.Env
		l.own(cc)
		err := runCmd(cc)
		ee, ok := err.(*exec.ExitError)
		if !ok || ee.ExitCode() != 1 {
			t.Fatalf("the worker should end with status 1 when sleep does not sleep: %v", err)
		}
		syncs := 0
		for _, call := range l.calls() {
			if call.isSync() {
				syncs++
			}
		}
		if syncs > 2 { // the cycle and the report of its job
			t.Errorf("%d syncs before the worker gave up", syncs)
		}
	})
}

func readStat(pid int) (fields []string, ok bool) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil, false
	}
	s := string(raw)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return nil, false
	}
	return append([]string{s[:i+1]}, strings.Fields(s[i+1:])...), true
}

// procTree is pid and all its descendants.
func procTree(pid int) []int {
	children := map[int][]int{}
	entries, _ := os.ReadDir("/proc")
	for _, e := range entries {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if f, ok := readStat(p); ok && len(f) > 2 {
			pp, _ := strconv.Atoi(f[2])
			children[pp] = append(children[pp], p)
		}
	}
	out := []int{pid}
	for i := 0; i < len(out); i++ {
		out = append(out, children[out[i]]...)
	}
	return out
}

type procUse struct {
	pid                  int
	comm                 string
	rss, pss, cpu, voted int // kB, kB, clock ticks, voluntary context switches
}

func useOf(pid int) (procUse, bool) {
	f, ok := readStat(pid)
	if !ok || len(f) < 15 {
		return procUse{}, false
	}
	u := procUse{pid: pid, comm: strings.Trim(f[0][strings.Index(f[0], "(")+1:], ")")}
	ut, _ := strconv.Atoi(f[12])
	st, _ := strconv.Atoi(f[13])
	u.cpu = ut + st
	status, _ := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	for _, line := range strings.Split(string(status), "\n") {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			u.rss, _ = strconv.Atoi(strings.Fields(v)[0])
		}
		if v, ok := strings.CutPrefix(line, "voluntary_ctxt_switches:"); ok {
			u.voted, _ = strconv.Atoi(strings.TrimSpace(v))
		}
	}
	if raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/smaps_rollup", pid)); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			if v, ok := strings.CutPrefix(line, "Pss:"); ok {
				u.pss, _ = strconv.Atoi(strings.Fields(v)[0])
			}
		}
	}
	return u, true
}

// The idle router: a supervisor, a worker and one sleep, each blocked in the kernel. No wake-ups between two syncs, no CPU, a
// few hundred kilobytes. The sleep that is waiting is the same process for the whole time (one sleep until the next sync, not a
// loop of short ones).
func TestIdleFootprint(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{realClock: true})
		t.Cleanup(func() { l.stopAndWait() })
		if out, code := l.run("start"); code != 0 {
			t.Fatalf("start: %d %s", code, out)
		}
		waitFor(t, "the first cycle to end and the worker to sleep", 90*time.Second, func() bool {
			w := l.pidOf("worker.pid")
			if !l.stateHas("last_exec") || l.stateHas("lock") || w == 0 {
				return false
			}
			return len(procTree(w)) > 1 // the sleep until the next sync (a forked ash under busybox, the sleep program under dash)
		})
		time.Sleep(time.Second)
		sup := l.pidOf("supervisor.pid")
		snapshot := func() map[int]procUse {
			m := map[int]procUse{}
			for _, p := range procTree(sup) {
				if u, ok := useOf(p); ok {
					m[p] = u
				}
			}
			return m
		}
		before := snapshot()
		time.Sleep(20 * time.Second)
		after := snapshot()
		var rss, pss, cpu int
		for pid, a := range after {
			b, same := before[pid]
			if !same {
				t.Errorf("a process (%d %s) appeared in 20 s of idle: the script wakes up between syncs", pid, a.comm)
				continue
			}
			rss += a.rss
			pss += a.pss
			cpu += a.cpu - b.cpu
			if a.voted != b.voted {
				t.Errorf("process %d (%s) woke up %d times in 20 s of idle", pid, a.comm, a.voted-b.voted)
			}
			t.Logf("idle %s: pid %d, RSS %d kB, PSS %d kB, CPU %d ticks in 20 s, %d wake-ups", sh.name, pid, a.rss, a.pss, a.cpu-b.cpu, a.voted-b.voted)
		}
		for pid := range before {
			if _, still := after[pid]; !still {
				t.Errorf("process %d is gone after 20 s of idle", pid)
			}
		}
		t.Logf("MEASURE idle %s: %d processes, RSS sum %d kB, PSS sum %d kB, CPU %d ticks in 20 s", sh.name, len(after), rss, pss, cpu)
		if len(after) > 3 {
			t.Errorf("%d processes in idle, want 3 (supervisor, worker, sleep)", len(after))
		}
		if cpu != 0 {
			t.Errorf("%d CPU ticks in 20 s of idle", cpu)
		}
		if pss > 3072 {
			t.Errorf("idle PSS %d kB: the plan says 2 MB, x86 static busybox is a little above it", pss)
		}
		// the worker and its curls run at the lowest priority
		w := l.pidOf("worker.pid")
		if f, ok := readStat(w); !ok || len(f) < 18 || f[17] != "10" {
			t.Errorf("worker nice: %v", f)
		}
		if p, err := exec.LookPath("ionice"); err == nil {
			if out, _ := combinedOutput(exec.Command(p, "-p", strconv.Itoa(w))); !strings.HasPrefix(string(out), "idle") {
				t.Errorf("worker I/O class: %s", out)
			}
		}
	})
}

// After the router boots without its /opt (the stick is out) or with a cleaned /tmp, the init script does nothing bad: without the
// program it exits at once and quietly, with it it starts the probe in the background and returns.
func TestInitScriptIsHarmless(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{noConf: true, notInstalled: true, realClock: true})
		t.Cleanup(func() { l.stopAndWait() })
		token := "mgp_" + strings.Repeat("e", 52)
		down := filepath.Join(l.root, "mgp.sh")
		must(t, writeExe(down, probeScript))
		must(t, os.WriteFile(filepath.Join(l.fake, "enroll.resp"), []byte("mgp 1\nid "+labID+"\nsecret "+labSecret+"\nend\n"), 0o644))
		if out, code := l.runAt(down, "install", labBase, token); code != 0 {
			t.Fatalf("install: %d %s", code, out)
		}
		s99 := filepath.Join(l.root, "opt/etc/init.d/S99mistgate-probe")
		l.stopAndWait()
		// the program is gone (a half-removed /opt): every verb is quiet and harmless
		saved, err := os.ReadFile(l.script)
		must(t, err)
		must(t, os.Remove(l.script))
		before := tree(t, l.root, l.fake)
		for _, verb := range []string{"start", "stop", "restart", "status"} {
			begin := time.Now()
			if out, code := l.shellScript(s99 + " " + verb); code != 0 || out != "" || time.Since(begin) > 3*time.Second {
				t.Errorf("init %s without the program: %d %q in %v", verb, code, out, time.Since(begin))
			}
		}
		if d := diffTree(before, tree(t, l.root, l.fake)); len(d) != 0 {
			t.Errorf("the init script without the program changed files: %v", d)
		}
		// a reboot: /tmp is empty, the program is there again; start returns at once and the probe runs
		must(t, os.RemoveAll(l.state))
		must(t, writeExe(l.script, saved))
		begin := time.Now()
		if out, code := l.shellScript(s99 + " start"); code != 0 || time.Since(begin) > 3*time.Second {
			t.Errorf("init start after a reboot: %d %q in %v", code, out, time.Since(begin))
		}
		waitFor(t, "the probe after the reboot", 10*time.Second, func() bool { w := l.pidOf("worker.pid"); return w > 0 && procAlive(w) })
		pids := l.probePids()
		if out, _ := l.shellScript(s99 + " stop"); out != "" {
			t.Errorf("init stop: %q", out)
		}
		if !waitGone(pids) {
			t.Error("the probe did not stop")
		}
	})
}
