package awgprep

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recRun is the fake command runner: it records what ran and fails, blocks or answers on a command prefix.
type recRun struct {
	ran   []string
	fail  string            // a command prefix that fails ("exit status 100")
	block string            // a command prefix that never ends until the context does
	out   map[string]string // output per command prefix
}

func (r *recRun) run(ctx context.Context, _ io.Writer, _ string, cmd ...string) (string, error) {
	line := strings.Join(cmd, " ")
	r.ran = append(r.ran, line)
	if r.block != "" && strings.HasPrefix(line, r.block) {
		<-ctx.Done()
		return "", errors.New("signal: killed")
	}
	for k, v := range r.out {
		if strings.HasPrefix(line, k) {
			if r.fail != "" && strings.HasPrefix(line, r.fail) {
				return v, errors.New("exit status 100")
			}
			return v, nil
		}
	}
	if r.fail != "" && strings.HasPrefix(line, r.fail) {
		return "", errors.New("exit status 100")
	}
	return "", nil
}

func goodSys(written *string) Sys {
	return Sys{
		WriteFile: func(path, body string) error { *written = path + "\n" + body; return nil },
		Exists:    func(string) bool { return true },
		Probe:     func() (uint32, uint32, error) { return 3, 34, nil },
		Sleep:     func(context.Context, time.Duration) {},
	}
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("err = %v, want a *Failure", err)
	}
	return f.Code
}

func debianPlan(t *testing.T) []Step {
	t.Helper()
	steps, _, err := Plan(Env{OSRelease: osr("debian", ""), Kernel: "6.1.0-18-amd64", CPUs: 1})
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func ubuntuPlan(t *testing.T) []Step {
	t.Helper()
	steps, _, err := Plan(Env{OSRelease: osr("ubuntu", ""), Kernel: "6.8.0-142-generic", CPUs: 1})
	if err != nil {
		t.Fatal(err)
	}
	return steps
}

func TestRunRunsThePlanThenVerifies(t *testing.T) {
	r := &recRun{out: map[string]string{"git -C": pinnedModuleCommit + "abcdef0123456789\n"}}
	var wrote string
	var buf bytes.Buffer
	var seen [][2]int
	if err := Run(context.Background(), &buf, debianPlan(t), "/tmp/src", r.run, goodSys(&wrote), JobTimeout, func(i, n int) { seen = append(seen, [2]int{i, n}) }); err != nil {
		t.Fatal(err)
	}
	if r.ran[len(r.ran)-1] != "modprobe amneziawg" || !strings.Contains(strings.Join(r.ran, "\n"), "git clone --depth 1 --branch "+pinnedModuleTag) {
		t.Errorf("ran %v", r.ran)
	}
	if !strings.Contains(strings.Join(r.ran, " "), "-C /tmp/src/src") {
		t.Errorf("{src} was not replaced: %v", r.ran)
	}
	if !strings.HasPrefix(wrote, "/etc/modules-load.d/amneziawg.conf\n") || !strings.Contains(wrote, "\namneziawg\n") {
		t.Errorf("modules-load.d = %q", wrote)
	}
	if !strings.Contains(buf.String(), "OK: the amneziawg kernel module is loaded (genl v3, maxattr 34)") {
		t.Errorf("output:\n%s", buf.String())
	}
	if len(seen) != len(r.ran) || seen[0] != [2]int{1, len(r.ran)} {
		t.Errorf("progress %v for %d commands", seen, len(r.ran))
	}
}

// A moved tag stops the build before anything is compiled or installed.
func TestRunStopsOnAMovedTag(t *testing.T) {
	r := &recRun{out: map[string]string{"git -C": "deadbeef00000000\n"}}
	var wrote string
	err := Run(context.Background(), io.Discard, debianPlan(t), "/tmp/src", r.run, goodSys(&wrote), JobTimeout, nil)
	if err == nil || !strings.Contains(err.Error(), "tag moved") || codeOf(t, err) != CodeSourceMoved {
		t.Fatalf("err = %v", err)
	}
	for _, c := range r.ran {
		if strings.HasPrefix(c, "make") || strings.HasPrefix(c, "install") || strings.HasPrefix(c, "modprobe") {
			t.Errorf("ran %q after a failed commit check", c)
		}
	}
	if wrote != "" {
		t.Error("modules-load.d written for a module that was not built")
	}
}

// An apt failure: the run stops at that step, the short reason names the step and not the apt output.
func TestRunReportsAFailedStep(t *testing.T) {
	r := &recRun{fail: "add-apt-repository", out: map[string]string{"add-apt-repository": "E: some very long apt output\nmore lines\n"}}
	var wrote string
	err := Run(context.Background(), io.Discard, ubuntuPlan(t), "/tmp/src", r.run, goodSys(&wrote), JobTimeout, nil)
	if err == nil || !strings.Contains(err.Error(), "add-apt-repository") || !strings.Contains(err.Error(), "nothing is rolled back") {
		t.Fatalf("err = %v", err)
	}
	if code, detail := Short(err); code != CodeStepFailed || !strings.Contains(detail, "add the Amnezia PPA") || strings.Contains(detail, "apt output") {
		t.Errorf("short = %q %q", code, detail)
	}
	if len(r.ran) != 3 {
		t.Errorf("steps after the failed one ran: %v", r.ran)
	}
	if wrote != "" {
		t.Error("the module was verified after a failed step")
	}
}

func TestRunNamesALockedPackageManager(t *testing.T) {
	lockedInstall := "apt-get -o DPkg::Lock::Timeout=1800 install"
	r := &recRun{fail: lockedInstall, out: map[string]string{lockedInstall: "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 812 (unattended-upgr)\n"}}
	var wrote string
	err := Run(context.Background(), io.Discard, debianPlan(t), "/tmp/src", r.run, goodSys(&wrote), JobTimeout, nil)
	if codeOf(t, err) != CodeAptLock {
		t.Fatalf("err = %v", err)
	}
}

func TestRunDeadlineIsATimeout(t *testing.T) {
	r := &recRun{block: "apt-get -o"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var wrote string
	err := Run(ctx, io.Discard, debianPlan(t), "/tmp/src", r.run, goodSys(&wrote), 15*time.Minute, nil)
	if codeOf(t, err) != CodeTimeout || !strings.Contains(err.Error(), "15 minutes") {
		t.Fatalf("err = %v", err)
	}
	// stopped for another reason it is an interruption, not a timeout
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	err = Run(ctx2, io.Discard, debianPlan(t), "/tmp/src", (&recRun{}).run, goodSys(&wrote), time.Minute, nil)
	if codeOf(t, err) != CodeInterrupted {
		t.Fatalf("err = %v", err)
	}
}

func TestVerify(t *testing.T) {
	var wrote string
	ok := goodSys(&wrote)
	cases := map[string]struct {
		mut  func(*Sys)
		want string
		code string
	}{
		"not loaded":           {func(s *Sys) { s.Exists = func(string) bool { return false } }, "not loaded", CodeModuleNotLoad},
		"genl does not answer": {func(s *Sys) { s.Probe = func() (uint32, uint32, error) { return 0, 0, errors.New("no family") } }, "does not answer", CodeModuleUnusable},
		"genl v2":              {func(s *Sys) { s.Probe = func() (uint32, uint32, error) { return 2, 34, nil } }, "genl v2", CodeModuleUnusable},
		"3.0 without 3.1":      {func(s *Sys) { s.Probe = func() (uint32, uint32, error) { return 3, 32, nil } }, "maxattr 32", CodeModuleUnusable},
		"cannot persist":       {func(s *Sys) { s.WriteFile = func(string, string) error { return errors.New("read-only") } }, "load at boot", CodeModuleUnusable},
	}
	for name, c := range cases {
		s := ok
		c.mut(&s)
		err := Verify(io.Discard, s)
		if err == nil || !strings.Contains(err.Error(), c.want) || codeOf(t, err) != c.code {
			t.Errorf("%s: err = %v, want it to say %q with code %s", name, err, c.want, c.code)
		}
	}
	if err := Verify(io.Discard, ok); err != nil {
		t.Errorf("a good module: %v", err)
	}
}

func TestWaitAptLock(t *testing.T) {
	var slept int
	busy := 3
	sys := Sys{AptBusy: func() bool { busy--; return busy >= 0 }, Sleep: func(context.Context, time.Duration) { slept++ }}
	if err := WaitAptLock(context.Background(), io.Discard, debianPlan(t), sys, LockWait); err != nil || slept != 2 {
		t.Errorf("a lock that frees up: err = %v, slept %d", err, slept)
	}
	// never free: bounded, with the right code and a bounded number of polls
	slept = 0
	sys = Sys{AptBusy: func() bool { return true }, Sleep: func(context.Context, time.Duration) { slept++ }}
	err := WaitAptLock(context.Background(), io.Discard, debianPlan(t), sys, time.Minute)
	if codeOf(t, err) != CodeAptLock || slept != 20 {
		t.Errorf("a lock that never frees: err = %v, polls %d", err, slept)
	}
	// a plan without apt (the manual module recipe of another day) never waits
	if err := WaitAptLock(context.Background(), io.Discard, []Step{{Cmd: []string{"modprobe", "amneziawg"}}}, sys, time.Minute); err != nil {
		t.Errorf("no apt in the plan: %v", err)
	}
}

func jobFor(t *testing.T, env Env, r *recRun, wrote *string) (Job, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "awg-prepare.json")
	return Job{StatusPath: path, Out: io.Discard, Env: env, Auto: true, Run: r.run, Sys: goodSys(wrote), Src: "/tmp/src", Heartbeat: 5 * time.Millisecond}, path
}

var okEnv = Env{OSRelease: osr("debian", ""), Kernel: "6.1.0-18-amd64", CPUs: 1, Systemd: true}

func TestJobWritesAStatusFromRunningToDone(t *testing.T) {
	r := &recRun{out: map[string]string{"git -C": pinnedModuleCommit + "00\n"}}
	var wrote string
	job, path := jobFor(t, okEnv, r, &wrote)
	var states []string
	run := r.run
	job.Run = func(ctx context.Context, w io.Writer, dir string, cmd ...string) (string, error) {
		st, ok, _ := ReadStatus(path)
		if ok {
			states = append(states, st.State)
		}
		return run(ctx, w, dir, cmd...)
	}
	if err := job.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, ok, _ := ReadStatus(path)
	if !ok || st.State != StateDone || st.Kernel != "6.1.0-18-amd64" || st.Finished == 0 || st.Code != "" || st.Reported {
		t.Errorf("status = %+v", st)
	}
	if len(states) == 0 || states[0] != StateRunning {
		t.Errorf("the status must say running while the commands run: %v", states)
	}
	if wrote == "" {
		t.Error("the module was not verified")
	}
}

func TestJobAptFailureLeavesAShortFailedStatus(t *testing.T) {
	failedInstall := "apt-get -o DPkg::Lock::Timeout=1800 install"
	r := &recRun{fail: failedInstall, out: map[string]string{failedInstall: "E: Unable to locate package linux-headers-6.1.0-18-amd64\n" + strings.Repeat("noise\n", 100)}}
	var wrote string
	job, path := jobFor(t, okEnv, r, &wrote)
	err := job.Do(context.Background())
	if codeOf(t, err) != CodeStepFailed {
		t.Fatalf("err = %v", err)
	}
	st, _, _ := ReadStatus(path)
	if st.State != StateFailed || st.Code != CodeStepFailed || len(st.Reason) > 200 || strings.Contains(st.Reason, "noise") || st.Step != 2 {
		t.Errorf("status = %+v", st)
	}
	if wrote != "" {
		t.Error("verified after a failure")
	}
}

func TestJobTimeoutKillsTheRunAndSaysSo(t *testing.T) {
	r := &recRun{block: "git clone"}
	var wrote string
	job, path := jobFor(t, okEnv, r, &wrote)
	job.Timeout = 40 * time.Millisecond
	if err := job.Do(context.Background()); codeOf(t, err) != CodeTimeout {
		t.Fatalf("err = %v", err)
	}
	st, _, _ := ReadStatus(path)
	if st.State != StateFailed || st.Code != CodeTimeout || st.Finished == 0 {
		t.Errorf("status = %+v", st)
	}
	for _, c := range r.ran {
		if strings.HasPrefix(c, "make") || strings.HasPrefix(c, "modprobe") {
			t.Errorf("ran %q after the timeout", c)
		}
	}
}

// A host the plan refuses is a failed status with the host's code, written by the job itself.
func TestJobOnAContainerAndOnAnotherDistro(t *testing.T) {
	for name, c := range map[string]struct {
		mut  func(*Env)
		code string
	}{
		"container": {func(e *Env) { e.Container = "lxc" }, CodeContainer},
		"alpine":    {func(e *Env) { e.OSRelease = osr("alpine", "") }, CodeDistro},
	} {
		env := okEnv
		c.mut(&env)
		r := &recRun{}
		var wrote string
		job, path := jobFor(t, env, r, &wrote)
		err := job.Do(context.Background())
		if codeOf(t, err) != c.code {
			t.Errorf("%s: err = %v", name, err)
		}
		if st, _, _ := ReadStatus(path); st.State != StateFailed || st.Code != c.code {
			t.Errorf("%s: status = %+v", name, st)
		}
		if len(r.ran) != 0 {
			t.Errorf("%s: ran %v", name, r.ran)
		}
	}
}

func TestJobWaitsForAForeignAptLockBeforeTheFirstCommand(t *testing.T) {
	r := &recRun{out: map[string]string{"git -C": pinnedModuleCommit + "00\n"}}
	var wrote string
	job, _ := jobFor(t, okEnv, r, &wrote)
	busy := 2
	job.Sys.AptBusy = func() bool { busy--; return busy >= 0 }
	var ranWhenBusy int
	run := r.run
	job.Run = func(ctx context.Context, w io.Writer, dir string, cmd ...string) (string, error) {
		if busy >= 0 {
			ranWhenBusy++
		}
		return run(ctx, w, dir, cmd...)
	}
	if err := job.Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ranWhenBusy != 0 {
		t.Errorf("%d commands ran while another package manager held the lock", ranWhenBusy)
	}
}

func TestJobWaitsForAptLockLongerThanTheOldFiveMinuteLimit(t *testing.T) {
	r := &recRun{out: map[string]string{"git -C": pinnedModuleCommit + "00\n"}}
	var wrote string
	job, _ := jobFor(t, okEnv, r, &wrote)
	var checks int
	job.Sys.AptBusy = func() bool {
		checks++
		return checks <= 122 // more than five minutes at the three-second polling interval
	}
	job.Sys.Sleep = func(context.Context, time.Duration) {}
	if err := job.Do(context.Background()); err != nil {
		t.Fatalf("job failed while waiting for a lock that eventually clears: %v", err)
	}
	if len(r.ran) == 0 {
		t.Fatal("the plan did not run after the lock cleared")
	}
}

func TestStatusFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "s.json")
	if _, ok, err := ReadStatus(path); ok || err != nil {
		t.Errorf("no file: ok=%v err=%v", ok, err)
	}
	if err := WriteStatus(path, Status{State: StateRunning, Started: 5}); err != nil {
		t.Fatal(err)
	}
	if s, ok, _ := ReadStatus(path); !ok || s.State != StateRunning || s.Started != 5 {
		t.Errorf("round trip = %+v", s)
	}
	if err := WriteStatus(path, Status{State: "weird"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := ReadStatus(path); ok {
		t.Error("an unknown state counts as nothing")
	}
}
