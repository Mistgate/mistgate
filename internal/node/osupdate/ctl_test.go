package osupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/unit"
)

type ctlRig struct {
	t        *testing.T
	m        *fakeMachine
	path     string
	now      time.Time
	launches []Spec
	launch   func(Spec) error
	excluded string
	settled  func() (bool, int)
	ended    []Status
}

func newRig(t *testing.T) *ctlRig {
	m := newMachine(t)
	return &ctlRig{t: t, m: m, path: statusPath(t), now: time.Unix(1_800_000_000, 0)}
}

// ctl builds a Controller. Building a second one on the same rig is an agent restart: nothing but the file is shared.
func (r *ctlRig) ctl() *Controller {
	return New(Config{
		StatusPath: r.path, Host: r.m.host(), Now: func() time.Time { return r.now },
		Launch: func(_ context.Context, s Spec) error {
			r.launches = append(r.launches, s)
			if r.launch != nil {
				return r.launch(s)
			}
			return nil
		},
		Excluded: func(context.Context) string { return r.excluded },
		Settled: func() (bool, int) {
			if r.settled == nil {
				return true, 0
			}
			return r.settled()
		},
	})
}

func (r *ctlRig) poll(c *Controller) { c.Poll(func(s Status) { r.ended = append(r.ended, s) }) }

func (r *ctlRig) advance(d time.Duration) { r.now = r.now.Add(d) }

func (r *ctlRig) status() Status { return mustStatus(r.t, r.path) }

var upgradeSpec = Spec{JobID: "osj_a1", Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}

func TestStartWritesTheStatusBeforeLaunchingAndAllowsOneJob(t *testing.T) {
	r := newRig(t)
	r.launch = func(Spec) error {
		if st := r.status(); st.State != StateRunning || st.JobID != "osj_a1" {
			t.Errorf("status before the launch = %+v", st)
		}
		return nil
	}
	c := r.ctl()
	a := c.Start(context.Background(), upgradeSpec)
	if a.State != AnswerStarted || a.JobID != "osj_a1" || len(r.launches) != 1 {
		t.Fatalf("answer = %+v launches = %d", a, len(r.launches))
	}
	again := c.Start(context.Background(), Spec{JobID: "osj_b2", Action: ActionRefresh, Reboot: RebootNever})
	if again.State != AnswerRunning || again.JobID != "osj_a1" || len(r.launches) != 1 {
		t.Fatalf("a second job while one runs: %+v launches = %d", again, len(r.launches))
	}
	if !c.Active() {
		t.Fatal("Active() = false for a running job")
	}
	// A restarted agent sees the same job.
	if again := r.ctl().Start(context.Background(), Spec{JobID: "osj_c3", Action: ActionRefresh, Reboot: RebootNever}); again.State != AnswerRunning {
		t.Fatalf("after an agent restart: %+v", again)
	}
}

func TestStartRefusals(t *testing.T) {
	r := newRig(t)
	c := r.ctl()
	for name, tc := range map[string]struct {
		spec Spec
		code string
	}{
		"unknown action":  {Spec{JobID: "osj_x", Action: "format", Reboot: RebootNever}, CodeBadRequest},
		"unknown mode":    {Spec{JobID: "osj_x", Action: ActionUpgrade, Mode: "dist-upgrade", Reboot: RebootNever}, CodeBadRequest},
		"bad reboot":      {Spec{JobID: "osj_x", Action: ActionUpgrade, Mode: ModeAll, Reboot: "now"}, CodeBadRequest},
		"reboot never":    {Spec{JobID: "osj_x", Action: ActionReboot, Reboot: RebootNever}, CodeBadRequest},
		"always upgrade":  {Spec{JobID: "osj_x", Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootAlways}, CodeBadRequest},
		"mode on refresh": {Spec{JobID: "osj_x", Action: ActionRefresh, Mode: ModeAll, Reboot: RebootNever}, CodeBadRequest},
		"hostile job id":  {Spec{JobID: "osj_x;reboot", Action: ActionRefresh, Reboot: RebootNever}, CodeBadRequest},
		"manual id":       {Spec{JobID: "manual-1", Action: ActionRefresh, Reboot: RebootNever}, ""},
	} {
		a := c.Start(context.Background(), tc.spec)
		if tc.code != "" && (a.State != AnswerUnsupported || a.Code != tc.code) {
			t.Errorf("%s: answer = %+v", name, a)
		}
	}
	if len(r.launches) != 1 { // only the valid manual id launched
		t.Fatalf("launches = %d: a refused request started a unit", len(r.launches))
	}
}

func TestStartRefusesAnUnsupportedHostAndOtherHostWork(t *testing.T) {
	r := newRig(t)
	r.m.files["/etc/os-release"] = "ID=fedora\nVERSION_ID=41\n"
	if a := r.ctl().Start(context.Background(), upgradeSpec); a.Code != CodeUnsupportedOS || len(r.launches) != 0 {
		t.Fatalf("answer = %+v", a)
	}
	r = newRig(t)
	r.m.files["/proc/1/comm"] = "init\n"
	if a := r.ctl().Start(context.Background(), upgradeSpec); a.Code != CodeNoSystemd {
		t.Fatalf("answer = %+v", a)
	}
	r = newRig(t)
	r.excluded = "an agent update is going"
	a := r.ctl().Start(context.Background(), upgradeSpec)
	if a.State != AnswerUnsupported || a.Code != CodeBusy || len(r.launches) != 0 {
		t.Fatalf("answer = %+v", a)
	}
	if _, ok, _ := ReadStatus(r.path); ok {
		t.Fatal("a refused request left a status file")
	}
}

func TestLaunchFailureIsReportedAtOnceAndLeavesNoRunningJob(t *testing.T) {
	r := newRig(t)
	r.launch = func(Spec) error { return errors.New("systemd-run: boom") }
	c := r.ctl()
	a := c.Start(context.Background(), upgradeSpec)
	if a.State != AnswerUnsupported || a.Code != CodeLaunchFailed || strings.Contains(a.Reason, "boom") {
		t.Fatalf("answer = %+v", a)
	}
	st := r.status()
	if st.State != StateFailed || !st.Reported {
		t.Fatalf("status = %+v", st)
	}
	r.poll(c)
	if len(r.ended) != 0 {
		t.Fatal("a launch failure is answered to the caller, not reported again")
	}
	// The same job id again is answered with that job's result, even when it failed to launch; a retry takes a new id.
	if a := c.Start(context.Background(), upgradeSpec); a.State != AnswerFinished || a.Result != StateFailed || a.Code != CodeLaunchFailed {
		t.Fatalf("answer for the same job id = %+v", a)
	}
	r.launch = func(Spec) error { return unit.ErrBusy }
	retry := upgradeSpec
	retry.JobID = "osj_a2"
	if a := c.Start(context.Background(), retry); a.Code != CodeBusy {
		t.Fatalf("answer for an existing unit = %+v", a)
	}
}

func TestEndOfAJobIsReportedOnceAndBlocksTheNextUntilThen(t *testing.T) {
	r := newRig(t)
	c := r.ctl()
	c.Start(context.Background(), upgradeSpec)
	st := r.status()
	st.State, st.Finished, st.Reboot = StateDone, r.now.Unix(), RebootNotRequired
	if err := WriteStatus(r.path, st); err != nil {
		t.Fatal(err)
	}
	if a := c.Start(context.Background(), Spec{JobID: "osj_n", Action: ActionRefresh, Reboot: RebootNever}); a.Code != CodeBusy {
		t.Fatalf("an unreported result must be reported before the next job: %+v", a)
	}
	r.poll(c)
	r.poll(c)
	r.poll(r.ctl())
	if len(r.ended) != 1 || r.ended[0].State != StateDone {
		t.Fatalf("ended = %+v", r.ended)
	}
	if _, ok := c.Current(); ok {
		t.Fatal("a reported job is no longer the current one")
	}
	if a := c.Start(context.Background(), Spec{JobID: "osj_n", Action: ActionRefresh, Reboot: RebootNever}); a.State != AnswerStarted {
		t.Fatalf("answer = %+v", a)
	}
}

func TestSilentJobIsInterrupted(t *testing.T) {
	r := newRig(t)
	c := r.ctl()
	c.Start(context.Background(), upgradeSpec)
	r.advance(Stale / 2)
	r.poll(c)
	if len(r.ended) != 0 {
		t.Fatal("a job within its heartbeat is alive")
	}
	r.advance(Stale)
	r.poll(c)
	if len(r.ended) != 1 || r.ended[0].State != StateFailed || r.ended[0].Code != CodeInterrupted {
		t.Fatalf("ended = %+v", r.ended)
	}
}

// rebooting plays a job that wrote "rebooting" and was killed by the shutdown.
func (r *ctlRig) rebooting(kernelTo string, awg bool) {
	st := Status{JobID: "osj_a1", Action: ActionUpgrade, Mode: ModeSecurity, RebootPolicy: RebootIfRequired, State: StateRunning,
		Phase: PhaseRebooting, Started: r.now.Unix() - 600, Updated: r.now.Unix(), KernelFrom: oldKernel, KernelTo: kernelTo,
		BootID: "boot-one", AwgRequired: awg}
	if err := WriteStatus(r.path, st); err != nil {
		r.t.Fatal(err)
	}
}

func (r *ctlRig) booted() {
	r.m.files["/proc/sys/kernel/random/boot_id"] = "boot-two\n"
	r.m.files["/proc/sys/kernel/osrelease"] = newKernel + "\n"
	r.advance(90 * time.Second)
}

func TestReturnOnTheExpectedKernelIsDone(t *testing.T) {
	r := newRig(t)
	r.rebooting(newKernel, true)
	r.m.files["/sys/module/amneziawg/version"] = "3.1"
	r.booted()
	r.poll(r.ctl()) // a freshly started agent: the heartbeat is not even stale yet
	if len(r.ended) != 1 {
		t.Fatalf("ended = %+v", r.ended)
	}
	got := r.ended[0]
	if got.State != StateDone || got.Reboot != RebootDone || got.KernelNow != newKernel || got.Code != "" || got.Finished == 0 {
		t.Fatalf("result = %+v", got)
	}
}

func TestReturnOnAnUnexpectedKernelOrWithoutTheModuleFails(t *testing.T) {
	r := newRig(t)
	r.rebooting(newKernel, false)
	r.booted()
	r.m.files["/proc/sys/kernel/osrelease"] = oldKernel + "\n" // GRUB booted the old one
	r.poll(r.ctl())
	if got := r.ended[0]; got.State != StateFailed || got.Code != CodeKernelNotSwitched || got.Reboot != RebootDone || got.KernelNow != oldKernel {
		t.Fatalf("result = %+v", got)
	}

	r = newRig(t)
	r.rebooting(newKernel, true) // AmneziaWG ran on the module; /sys/module/amneziawg is absent now
	r.booted()
	r.poll(r.ctl())
	if got := r.ended[0]; got.State != StateFailed || got.Code != CodeAwgNotLoaded {
		t.Fatalf("result = %+v", got)
	}
}

func TestReturnWaitsForTheEnginesThenReports(t *testing.T) {
	r := newRig(t)
	r.rebooting(newKernel, false)
	r.booted()
	settled := false
	failed := 0
	r.settled = func() (bool, int) { return settled, failed }
	c := r.ctl()
	r.poll(c)
	if len(r.ended) != 0 || r.status().State != StateRunning || r.status().Back == 0 {
		t.Fatalf("the engines are not up yet: ended=%v status=%+v", r.ended, r.status())
	}
	// An agent restart in this window continues from the file.
	r.advance(time.Minute)
	c = r.ctl()
	r.poll(c)
	if len(r.ended) != 0 {
		t.Fatal("reported before the engines settled")
	}
	settled, failed = true, 2
	r.poll(c)
	if len(r.ended) != 1 || r.ended[0].Code != CodeInboundFailed || r.ended[0].State != StateFailed {
		t.Fatalf("ended = %+v", r.ended)
	}
}

func TestReturnReportsAfterTheSettleLimitEvenIfNothingSettled(t *testing.T) {
	r := newRig(t)
	r.rebooting(newKernel, false)
	r.booted()
	r.settled = func() (bool, int) { return false, 0 }
	c := r.ctl()
	r.poll(c)
	r.advance(SettleMax + time.Second)
	r.poll(c)
	if len(r.ended) != 1 || r.ended[0].State != StateDone {
		t.Fatalf("ended = %+v", r.ended)
	}
}

func TestRebootThatNeverHappenedIsRebootFailed(t *testing.T) {
	r := newRig(t)
	r.rebooting(newKernel, false) // same boot id
	c := r.ctl()
	r.advance(Stale / 2)
	r.poll(c)
	if len(r.ended) != 0 {
		t.Fatal("the job is still beating")
	}
	r.advance(Stale)
	r.poll(c)
	if got := r.ended[0]; got.State != StateFailed || got.Code != CodeRebootFailed || got.Reboot != RebootHeld {
		t.Fatalf("result = %+v", got)
	}
}

func TestCurrentCarriesRunningAndUnreportedJobsOnly(t *testing.T) {
	r := newRig(t)
	c := r.ctl()
	if _, ok := c.Current(); ok {
		t.Fatal("no job yet")
	}
	c.Start(context.Background(), upgradeSpec)
	st, ok := c.Current()
	if !ok || st.State != StateRunning {
		t.Fatalf("current = %+v %v", st, ok)
	}
	j := st.Proto()
	if j.JobId != "osj_a1" || j.State != StateRunning || j.Phase != PhasePreflight || j.Action != ActionUpgrade || j.Mode != ModeSecurity {
		t.Fatalf("proto = %v", j)
	}
}

func TestLauncherCommandLineHasOnlyEnumeratedFields(t *testing.T) {
	var got []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return nil, nil
	}
	spec := Spec{JobID: "osj_ab-12", Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired, AwgRequired: true}
	if err := launchSystemdRun(context.Background(), "/usr/local/bin/mistgate-node", "/var/lib/mistgate-node/os-update.json", spec, run); err != nil {
		t.Fatal(err)
	}
	want := []string{"systemd-run", "--quiet", "--collect", "--no-ask-password", "--unit=mistgate-os-update"}
	if !slices.Equal(got[:5], want) {
		t.Fatalf("argv = %v", got)
	}
	i := slices.Index(got, "--")
	if i < 0 {
		t.Fatalf("no -- before the command: %v", got)
	}
	// A stop signals the job only (mixed) and waits for the apt it is in; the default would SIGTERM dpkg in mid-transaction.
	for _, p := range []string{"--property=KillMode=mixed", "--property=TimeoutStopSec=600", "--property=RuntimeMaxSec=4500"} {
		if !slices.Contains(got[:i], p) {
			t.Errorf("systemd-run does not get %s: %v", p, got[:i])
		}
	}
	tail := got[i+1:]
	wantTail := []string{"/usr/local/bin/mistgate-node", "host", "os-update", "--yes", "--action", "upgrade", "--mode", "security", "--reboot", "if_required",
		"--job-id", "osj_ab-12", "--status-file", "/var/lib/mistgate-node/os-update.json", "--timeout", "1h0m0s", "--awg-required"}
	if !slices.Equal(tail, wantTail) {
		t.Fatalf("command = %v", tail)
	}
	for _, a := range got {
		if strings.ContainsAny(a, " ;|&$`\n") && !strings.HasPrefix(a, "--description=") {
			t.Errorf("argument %q could be taken for shell syntax", a)
		}
	}
	// An invalid spec never reaches systemd-run.
	got = nil
	bad := Spec{JobID: "osj_a b", Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}
	if err := launchSystemdRun(context.Background(), "/x", "/y", bad, run); err == nil || got != nil {
		t.Fatalf("err = %v argv = %v", err, got)
	}
	if err := launchSystemdRun(context.Background(), "/x", "/y", spec, func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Failed to start transient service unit: Unit mistgate-os-update.service already exists."), errors.New("exit 1")
	}); !errors.Is(err, unit.ErrBusy) {
		t.Fatalf("err = %v, want unit.ErrBusy", err)
	}
}

// The same job id again is a repeated command (a panel that asks twice after a reconnect), not a new job: it must not start
// anything, least of all a second reboot.
func TestSameJobIdIsAnsweredWithTheJobsResultAndStartsNothing(t *testing.T) {
	r := newRig(t)
	c := r.ctl()
	reboot := Spec{JobID: "osj_rb", Action: ActionReboot, Reboot: RebootAlways}
	if a := c.Start(context.Background(), reboot); a.State != AnswerStarted {
		t.Fatalf("answer = %+v", a)
	}
	if a := c.Start(context.Background(), reboot); a.State != AnswerRunning || a.JobID != "osj_rb" {
		t.Fatalf("while it runs: %+v", a)
	}
	st := r.status()
	st.State, st.Finished, st.Reboot = StateDone, r.now.Unix(), RebootDone
	if err := WriteStatus(r.path, st); err != nil {
		t.Fatal(err)
	}
	for _, reported := range []bool{false, true} { // before and after the end was reported
		a := c.Start(context.Background(), reboot)
		if a.State != AnswerFinished || a.Result != StateDone || a.JobID != "osj_rb" {
			t.Fatalf("reported=%v: answer = %+v", reported, a)
		}
		r.poll(c)
	}
	if len(r.launches) != 1 {
		t.Fatalf("a repeated command launched %d jobs", len(r.launches))
	}
	// The same also across an agent restart.
	if a := r.ctl().Start(context.Background(), reboot); a.State != AnswerFinished || len(r.launches) != 1 {
		t.Fatalf("after a restart: %+v launches %d", a, len(r.launches))
	}
}

// A status file that cannot be written (/var full or read-only) must not make the end be reported every few seconds.
func TestEndIsReportedOnceEvenIfTheFileCannotBeMarked(t *testing.T) {
	r := newRig(t)
	c := r.ctl()
	c.Start(context.Background(), upgradeSpec)
	st := r.status()
	st.State, st.Finished = StateDone, r.now.Unix()
	if err := WriteStatus(r.path, st); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(r.path, 0o444); err != nil {
		t.Skip("cannot make the file read-only here")
	}
	// The rename in WriteStatus replaces a read-only file on Linux; to be sure the mark cannot be written, make the directory
	// read-only too (skipped where permissions do not bind, e.g. root).
	dir := filepath.Dir(r.path)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skip("cannot make the directory read-only here")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if f, err := os.Create(filepath.Join(dir, "probe")); err == nil {
		f.Close()
		t.Skip("permissions do not bind this user")
	}
	for range 5 {
		r.poll(c)
	}
	if len(r.ended) != 1 {
		t.Fatalf("the end was reported %d times", len(r.ended))
	}
}

// A job of an earlier boot that was not rebooting is over at once: the host restarted under it.
func TestJobOfAnEarlierBootIsInterruptedAtOnce(t *testing.T) {
	r := newRig(t)
	st := Status{JobID: "osj_old", Action: ActionUpgrade, Mode: ModeAll, RebootPolicy: RebootNever, State: StateRunning, Phase: PhaseInstalling,
		Started: r.now.Unix() - 60, Updated: r.now.Unix() - 5, BootID: "an-earlier-boot"}
	if err := WriteStatus(r.path, st); err != nil {
		t.Fatal(err)
	}
	r.poll(r.ctl()) // the heartbeat is five seconds old: the clock would say alive
	if len(r.ended) != 1 || r.ended[0].Code != CodeInterrupted || r.ended[0].State != StateFailed {
		t.Fatalf("ended = %+v", r.ended)
	}
}
