package osupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/secupdates"
)

const (
	// JobTimeout bounds the waits and quick steps of a job. An apt that has started is never cut by it: dpkg killed in the
	// middle of a transaction leaves the host half configured. Only the unit's RuntimeMaxSec stops a hung one.
	JobTimeout = 60 * time.Minute
	// LockWait is how long the job waits for another package manager before it starts.
	LockWait = 15 * time.Minute
	// UpdateTimeout cuts "apt-get update" (it is not a dpkg transaction, so cutting it is safe).
	UpdateTimeout = 5 * time.Minute
	// RebootGrace is the pause between writing the "rebooting" status and the reboot command: it lets the agent see the
	// phase and tell the panel before the host goes down.
	RebootGrace = 10 * time.Second
	// RebootWait is how long the job waits for the host to go down after the reboot command was accepted.
	RebootWait = 10 * time.Minute
	// MinUptime is the shortest time a host must have been up before a job reboots it: a panel that asks again and again
	// cannot keep a node in a reboot loop.
	MinUptime = 10 * time.Minute
	// kernelListTimeout bounds the two linux-version calls (they are quick; the reader's 3 s is too tight for a busy host).
	kernelListTimeout = 30 * time.Second

	minVarFreeMB      = 500
	minBootFreeForKer = 200
	minBootFreeMB     = 50
	minInitramfsBytes = 1 << 20
	maxKept           = 256 << 10 // what the job keeps of the end of one command's output to classify a failure
	maxQuery          = 8 << 20   // what it keeps of a read-only command (the package table is the largest)

	// RuntimeMaxSec is the unit's backstop behind JobTimeout.
	RuntimeMaxSec = 4500
)

var (
	aptLockOption = fmt.Sprintf("DPkg::Lock::Timeout=%d", int(LockWait/time.Second))
	dpkgConfOpts  = []string{"--force-confdef", "--force-confold"}
)

// Failure is why a job did not end well. Code is one of the Code* constants; Detail is one short English sentence (what the
// status file, the event and the UI carry, never package-manager output); Package is the package at fault, when one is.
type Failure struct {
	Code, Detail, Package string
	// Reasons are all codes that blocked a reboot, the first being Code.
	Reasons []string
}

func (f *Failure) Error() string { return f.Detail }

func fail(code, format string, a ...any) *Failure {
	return &Failure{Code: code, Detail: clip(fmt.Sprintf(format, a...), 300)}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// errRebooting ends Do without a final status: the host is going down and the file must keep saying "rebooting".
var errRebooting = errors.New("the host is rebooting")

// Job is one run of an OS job (what `mistgate-node host os-update --yes` does): preflight, lock wait, repair, refresh,
// install, the AmneziaWG module for a new kernel, the reboot checks and, as the very last step, the reboot. It writes
// StatusPath, which the agent follows.
type Job struct {
	Spec       Spec
	StatusPath string // "" = no status file
	Timeout    time.Duration
	LockWait   time.Duration
	Out        io.Writer
	Run        awgprep.Runner
	Host       Host
	Heartbeat  time.Duration
	// RebootLogPath is where the reboots of panel jobs (osj_) are counted; "" = os-reboots.json next to StatusPath.
	RebootLogPath string
	// RebootGrace and RebootWait are zero in production (the constants above); tests shorten them.
	RebootGrace time.Duration
	RebootWait  time.Duration
}

type run struct {
	j     Job
	host  Host
	out   *journal
	run   awgprep.Runner
	mu    sync.Mutex
	st    Status
	table map[string]string // package -> version, before the job
	// awgNeeded: the AmneziaWG kernel module is in use or said to be (Spec.AwgRequired), so no reboot may leave it behind.
	awgNeeded bool
	container string
	distro    string
	// unknownSource is the start of the sha256 of a module source Mistgate does not know (found by fixSource), "" = none.
	unknownSource string
	// release lets go of the dpkg lock held for the reboot.
	release func()
}

// Do runs the job. The error is a *Failure for a job that did not end well, and nil for one that did and for one whose
// reboot started: that one's process is stopped by the shutdown, so its status file keeps saying "rebooting". A status file
// is left behind either way.
func (j Job) Do(ctx context.Context) error {
	host := j.Host.defaults()
	if _, ref := ParseSpec(j.Spec.Action, j.Spec.Mode, j.Spec.Reboot, j.Spec.JobID); ref != nil || j.Spec.JobID == "" {
		return fail(CodeBadRequest, "the job is not valid")
	}
	limit := j.Timeout
	if limit <= 0 {
		limit = JobTimeout
	}
	beat := j.Heartbeat
	if beat <= 0 {
		beat = HeartbeatEvery
	}
	runner := j.Run
	if runner == nil {
		runner = awgprep.ExecRunner
	}
	r := &run{j: j, host: host, out: newJournal(j.Out), run: runner}
	now := host.Now().Unix()
	bootID := host.BootID()

	// A job of another id that is alive owns the file: this one must not overwrite it. The agent writes "running" for its
	// own job id before it launches the unit, so that id passes.
	if j.StatusPath != "" {
		if prev, ok, _ := ReadStatus(j.StatusPath); ok && prev.State == StateRunning && prev.JobID != j.Spec.JobID &&
			now-prev.Updated <= int64(Stale/time.Second) && (prev.BootID == "" || prev.BootID == bootID) {
			return fail(CodeBusy, "another OS job is running")
		}
	}
	r.st = Status{
		JobID: j.Spec.JobID, Action: j.Spec.Action, Mode: j.Spec.Mode, RebootPolicy: j.Spec.Reboot,
		State: StateRunning, Phase: PhasePreflight, Started: now, Updated: now, BootID: bootID, AwgRequired: j.Spec.AwgRequired,
	}
	if err := r.write(nil); err != nil {
		return fail(CodeLaunchFailed, "the job status could not be written")
	}

	ctx, cancel := context.WithTimeoutCause(ctx, limit, context.DeadlineExceeded)
	defer cancel()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(beat)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				_ = r.write(nil)
			}
		}
	}()

	err := r.steps(ctx)
	close(stop)
	wg.Wait()
	r.out.flush()
	if errors.Is(err, errRebooting) {
		return nil
	}
	r.unlock()
	if err != nil {
		var f *Failure
		if !errors.As(err, &f) {
			f = fail(CodeInstallFailed, "the job failed")
		}
		_ = r.write(func(s *Status) {
			s.State, s.Code, s.Reason, s.Package, s.Reasons, s.Finished = StateFailed, f.Code, f.Detail, f.Package, f.Reasons, r.host.Now().Unix()
		})
		return f
	}
	_ = r.write(func(s *Status) { s.State, s.Finished = StateDone, r.host.Now().Unix() })
	return nil
}

func (r *run) unlock() {
	if r.release != nil {
		r.release()
		r.release = nil
	}
}

func (r *run) write(change func(*Status)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if change != nil {
		change(&r.st)
	}
	r.st.Updated = r.host.Now().Unix()
	if r.j.StatusPath == "" {
		return nil
	}
	return WriteStatus(r.j.StatusPath, r.st)
}

func (r *run) phase(p string) {
	fmt.Fprintf(r.out, "== %s\n", p)
	_ = r.write(func(s *Status) { s.Phase = p })
}

// ctxFailure is the Failure of a job whose context ended, or nil while it is alive.
func ctxFailure(ctx context.Context) *Failure {
	switch {
	case ctx.Err() == nil:
		return nil
	case errors.Is(context.Cause(ctx), context.DeadlineExceeded):
		return fail(CodeTimeout, "the job did not finish in time and was stopped")
	}
	return fail(CodeCancelled, "the job was cancelled before it changed anything past its point of no return")
}

// cmd runs a command that may be cut with the job; cmdKeep runs one that must not be (a dpkg transaction): it still only
// starts while the job is alive. When the job is stopped (systemctl stop sends SIGTERM to this process only: the unit's
// KillMode is mixed) a running cmdKeep is waited for, up to the unit's TimeoutStopSec.
func (r *run) cmd(ctx context.Context, args ...string) (string, error) {
	return r.exec(ctx, args)
}

func (r *run) cmdKeep(ctx context.Context, args ...string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return r.exec(context.WithoutCancel(ctx), args)
}

// query runs a read-only command whose output is data for the job (the package table, a version list), not log: it is not
// written to the journal of the unit.
func (r *run) query(ctx context.Context, args ...string) (string, error) {
	out, err := r.run(ctx, io.Discard, "", args...)
	if len(out) > maxQuery {
		out = out[:maxQuery]
	}
	return out, err
}

func (r *run) exec(ctx context.Context, args []string) (string, error) {
	fmt.Fprintf(r.out, "$ %s\n", strings.Join(args, " "))
	out, err := r.run(ctx, r.out, "", args...)
	r.out.flush()
	if len(out) > maxKept { // an error is at the end of the output
		out = out[len(out)-maxKept:]
	}
	return out, err
}

func (r *run) steps(ctx context.Context) error {
	spec := r.j.Spec
	if f := r.preflight(ctx); f != nil {
		return f
	}
	if f := ctxFailure(ctx); f != nil {
		return f
	}
	if spec.Action == ActionReboot {
		return r.rebootStep(ctx)
	}
	if f := r.waitLock(ctx, r.lockWait(), true); f != nil {
		return f
	}
	if spec.Action == ActionUpgrade {
		if f := r.repairIfNeeded(ctx); f != nil {
			return f
		}
	}
	if f := r.refresh(ctx); f != nil {
		return f
	}
	if spec.Action == ActionRefresh {
		_ = r.write(func(s *Status) { s.Reboot = RebootNotAsked })
		return nil
	}
	if f := r.install(ctx); f != nil {
		return f
	}
	if f := r.awgModule(ctx); f != nil {
		return f
	}
	if f := ctxFailure(ctx); f != nil {
		return f
	}
	return r.rebootStep(ctx)
}

func (r *run) lockWait() time.Duration {
	if r.j.LockWait > 0 {
		return r.j.LockWait
	}
	return LockWait
}

// supported lists the systems a job runs on: Ubuntu 22.04, 24.04, 26.04 and Debian 12, 13.
var supported = map[string][]string{"ubuntu": {"22.04", "24.04", "26.04"}, "debian": {"12", "13"}}

func supportedSystem(id, version string) bool {
	for _, v := range supported[id] {
		if v == version {
			return true
		}
	}
	return false
}

// checkSystem is the part of the preflight that needs no command: a system the job runs on, with systemd as PID 1. The agent
// asks it before it launches a job and the job asks it again.
func checkSystem(h Host) (distro string, ref *Refusal) {
	osr := parseOSRelease(h.text("/etc/os-release"))
	distro = strings.ToLower(osr["ID"])
	if !supportedSystem(distro, osr["VERSION_ID"]) {
		return distro, &Refusal{Code: CodeUnsupportedOS, Reason: "OS updates run on Ubuntu 22.04, 24.04, 26.04 and Debian 12, 13 only"}
	}
	if h.text("/proc/1/comm") != "systemd" {
		return distro, &Refusal{Code: CodeNoSystemd, Reason: "systemd is not the init of this host"}
	}
	return distro, nil
}

func (r *run) preflight(ctx context.Context) *Failure {
	spec := r.j.Spec
	distro, ref := checkSystem(r.host)
	if ref != nil {
		return fail(ref.Code, "%s", ref.Reason)
	}
	r.distro = distro
	r.container = r.detectContainer(ctx)
	// The module is needed when the caller says so (the agent, from the backend its engine really uses) or when it is
	// loaded: a loaded module is what the node's tunnels run on, and a use count would not show it (WireGuard-style modules
	// hold no reference for their interfaces).
	r.awgNeeded = spec.AwgRequired || r.host.ModuleLoaded()
	_ = r.write(func(s *Status) { s.KernelFrom, s.AwgRequired = r.host.KernelRunning(), r.awgNeeded })
	if spec.Action != ActionReboot {
		if free, ok := r.host.FreeMB("/var"); ok && free < minVarFreeMB {
			return fail(CodeDiskFull, "/var has only %d MB free, %d MB are needed", free, minVarFreeMB)
		}
		if free, ok := r.host.FreeMB("/"); ok && free < minVarFreeMB {
			return fail(CodeDiskFull, "/ has only %d MB free, %d MB are needed", free, minVarFreeMB)
		}
	}
	if spec.Action == ActionUpgrade && spec.Mode == ModeSecurity {
		// unattended-upgrade reboots by itself when this is set, past every check of this job. "/b" makes apt-config answer
		// true or false whatever spelling the configuration used (on, with, enable, 1 ... checked on apt 2.7), and nothing at
		// all for a key that is not set (which is false). A failure to read it, or any other answer, is a refusal.
		out, err := r.query(ctx, "apt-config", "shell", "R", "Unattended-Upgrade::Automatic-Reboot/b")
		switch v := parseAssignments(out)["R"]; {
		case err != nil || (v != "true" && v != "false" && v != ""):
			return fail(CodeAutoReboot, "could not read the Automatic-Reboot setting of unattended-upgrades")
		case v == "true":
			return fail(CodeAutoReboot, "unattended-upgrades is set to reboot by itself (Automatic-Reboot); turn it off or use all updates")
		}
	}
	table, _, err := r.dpkgTable(ctx)
	if err != nil && spec.Action == ActionUpgrade {
		return fail(CodeDpkgBroken, "the package database could not be read")
	}
	r.table = table
	return nil
}

// waitLock waits for another package manager; the failure is apt_lock when it never lets go.
func (r *run) waitLock(ctx context.Context, maxWait time.Duration, setPhase bool) *Failure {
	if setPhase {
		r.phase(PhaseWaitingLock)
	}
	steps := []awgprep.Step{{Cmd: []string{"apt-get"}}}
	err := awgprep.WaitAptLock(ctx, r.out, steps, awgprep.Sys{AptBusy: r.host.AptBusy, Sleep: r.host.Sleep}, maxWait)
	if err == nil {
		return nil
	}
	if f := ctxFailure(ctx); f != nil {
		return f
	}
	return fail(CodeAptLock, "another package manager held the dpkg lock for more than %d minutes", max(int(maxWait/time.Minute), 1))
}

// dpkgTable reads "status<TAB>package<TAB>version" of every package: the versions (installed ones only) and the packages
// dpkg left half installed or half configured.
func (r *run) dpkgTable(ctx context.Context) (map[string]string, []string, error) {
	out, err := r.query(ctx, "dpkg-query", "-W", "-f=${db:Status-Abbrev}\t${Package}\t${Version}\n")
	if err != nil && out == "" {
		return nil, nil, err
	}
	versions, half := parseDpkgTable(out)
	return versions, half, nil
}

// repairIfNeeded finishes interrupted package installs (dpkg --configure -a, whose output goes to the journal and is read here).
// When the module's own DKMS build is what keeps dpkg from finishing, the job stops (awg_module_build_failed): it removes
// nothing and installs nothing more.
func (r *run) repairIfNeeded(ctx context.Context) *Failure {
	_, half, err := r.dpkgTable(ctx)
	if err != nil || len(half) == 0 {
		return nil
	}
	r.phase(PhaseRepairing)
	r.fixSource() // the configure below builds the module: a known-bad source is repaired before it does
	configure := func() (string, error) {
		out, err := r.cmdKeep(ctx, append(append([]string{"dpkg"}, dpkgConfOpts...), "--configure", "-a")...)
		if err == nil {
			out2, err2 := r.cmd(ctx, "apt-get", "-o", aptLockOption, "check")
			out, err = out+out2, err2
		}
		return out, err
	}
	out, err := configure()
	if err != nil && ownDKMSFailure(out) && r.fixSource() {
		out, err = configure() // repaired just now: once more
	}
	if err != nil && ownDKMSFailure(out) {
		return r.buildFailed(ctx, out, "")
	}
	_, half, rerr := r.dpkgTable(ctx)
	if rerr != nil || len(half) > 0 || err != nil {
		pkg := ""
		if len(half) > 0 {
			pkg = half[0]
		}
		if awgprep.IsAptLockOutput(out) {
			return fail(CodeAptLock, "another package manager prevented access to the package lock")
		}
		f := fail(CodeDpkgBroken, "interrupted package installs could not be repaired")
		f.Package = pkg
		return f
	}
	return nil
}

// fixSource repairs the module's source (compat.h) when it is a file Mistgate knows to break the build on some kernels, and
// says whether it changed one. What it found goes to the journal and the status file (awg_source); a file it does not know is
// left as it is and remembered for the reason of a failed build.
func (r *run) fixSource() bool {
	sys := awgprep.Sys{ReadFile: r.host.ReadFile, WriteFile: r.host.WriteFile}
	rs := awgprep.FixCompat(sys, r.host.Glob("/usr/src/amneziawg-*"), false, r.out)
	if sha := awgprep.CompatUnknownSHA(rs); sha != "" {
		r.unknownSource = sha
		_ = r.write(func(s *Status) { s.AwgSource = "unknown:" + sha })
	}
	if awgprep.CompatChanged(rs) {
		_ = r.write(func(s *Status) { s.AwgSource = "patched" })
		return true
	}
	return false
}

// buildFailed ends a job whose AmneziaWG module does not build (its own DKMS build failed). The job removes nothing (the module
// that is loaded keeps serving until the next boot, and an automatic removal would take it for good), installs nothing more and
// holds the reboot. The kernel comes from dkms's own words, then from the hint, then from the kernel of the next boot; the
// package named is the headers of that kernel, whose configuration runs the build.
func (r *run) buildFailed(ctx context.Context, out, hint string) *Failure {
	k := hint
	if named := awgprep.DKMSFailureKernel(out); named != "" {
		k = named
	}
	if !safeKernelVersion(k) {
		k = ""
		if ki, f := r.bootKernel(ctx); f == nil {
			k = ki.Newest
		}
	}
	f := &Failure{Code: CodeAwgBuildFailed}
	what := "a kernel"
	if safeKernelVersion(k) {
		what = "kernel " + k
		if validPackageName("linux-headers-" + k) {
			f.Package = "linux-headers-" + k
		}
	}
	f.Detail = fmt.Sprintf("the AmneziaWG module does not build for %s; nothing was removed and nothing more was installed, the loaded module serves "+
		"until the next boot; fix the build (its make.log is under /var/lib/dkms/amneziawg) before any reboot", what)
	if r.unknownSource != "" {
		f.Detail += "; " + awgprep.UnknownSourceNote(r.unknownSource)
	}
	if r.j.Spec.Reboot != RebootNever {
		_ = r.write(func(s *Status) { s.Reboot, s.HeldReason = RebootHeld, CodeAwgBuildFailed })
	}
	return f
}

func (r *run) refresh(ctx context.Context) *Failure {
	r.phase(PhaseRefreshing)
	cctx, cancel := context.WithTimeout(ctx, UpdateTimeout)
	defer cancel()
	out, err := r.cmd(cctx, "apt-get", "-o", aptLockOption, "update")
	security, other := classifyUpdate(out)
	if len(other) > 0 {
		_ = r.write(func(s *Status) { s.SourcesFailed = other })
	}
	if f := ctxFailure(ctx); f != nil {
		return f
	}
	switch {
	case len(security) > 0:
		return fail(CodeSecuritySource, "the security package source could not be refreshed: %s", strings.Join(security, ", "))
	case err != nil && len(other) == 0:
		if awgprep.IsAptLockOutput(out) {
			return fail(CodeAptLock, "another package manager prevented access to the package lock")
		}
		return fail(CodeRefreshFailed, "the package lists could not be refreshed")
	}
	return nil
}

func (r *run) install(ctx context.Context) *Failure {
	spec := r.j.Spec
	r.phase(PhaseInstalling)
	r.fixSource() // a new kernel's postinst builds the module: a known-bad source is repaired before it does
	if sim, err := r.query(ctx, append([]string{"apt-get"}, aptSimulationArgs...)...); err == nil {
		_, _, notable := parseSimulation(sim)
		if kernelPending(notable) {
			if free, ok := r.host.FreeMB("/boot"); ok && free < minBootFreeForKer {
				return fail(CodeDiskFull, "/boot has only %d MB free, a new kernel needs %d MB", free, minBootFreeForKer)
			}
		}
	}
	var out string
	var err error
	if spec.Mode == ModeSecurity {
		status, qerr := r.query(ctx, "dpkg-query", "-W", "-f=${Status}", "unattended-upgrades")
		if qerr != nil || strings.TrimSpace(status) != "install ok installed" {
			iout, ierr := r.cmdKeep(ctx, append([]string{"apt-get"}, secupdates.InstallArgs()...)...)
			if ierr != nil {
				if awgprep.IsAptLockOutput(iout) {
					return fail(CodeAptLock, "another package manager prevented access to the package lock")
				}
				return fail(CodeInstallFailed, "installing unattended-upgrades failed")
			}
		}
		out, err = r.cmdKeep(ctx, "unattended-upgrade", "-v")
	} else {
		args := []string{"apt-get", "-y", "-o", aptLockOption}
		for _, o := range dpkgConfOpts {
			args = append(args, "-o", "Dpkg::Options::="+o)
		}
		out, err = r.cmdKeep(ctx, append(args, "--with-new-pkgs", "upgrade")...)
	}
	if err != nil {
		if f := r.installFailed(ctx, out); f != nil {
			return f
		}
	}
	if after, _, derr := r.dpkgTable(ctx); derr == nil {
		_ = r.write(func(s *Status) { s.Upgraded = countChanged(r.table, after) })
	}
	return nil
}

// installFailed classifies a failed install.
func (r *run) installFailed(ctx context.Context, out string) *Failure {
	switch {
	case awgprep.IsAptLockOutput(out):
		return fail(CodeAptLock, "another package manager prevented access to the package lock")
	case ownDKMSFailure(out):
		// The new kernel's postinst ran the module's build and it failed. A source Mistgate knows to be bad is repaired and dpkg
		// finishes once more (a nil here: the job goes on); otherwise, or when that fails too, the job stops here.
		if r.fixSource() {
			return r.repairIfNeeded(ctx)
		}
		return r.buildFailed(ctx, out, "")
	}
	// unattended-upgrade writes the output of dpkg to its own log, not to this job's: when it fails, look at dpkg itself. Whatever
	// is interrupted is finished by the repair, whose output this job reads.
	if _, half, err := r.dpkgTable(ctx); err == nil && len(half) > 0 {
		if f := r.repairIfNeeded(ctx); f != nil {
			return f
		}
		f := fail(CodeInstallFailed, "the install was interrupted; dpkg was repaired, run the update again")
		f.Package = half[0]
		return f
	}
	f := fail(CodeInstallFailed, "installing the updates failed")
	f.Package = failedPackage(out)
	return f
}

func kernelPending(notable []string) bool {
	for _, n := range notable {
		if strings.HasPrefix(n, "linux-image") {
			return true
		}
	}
	return false
}

// kernelInfo is the kernel the next boot starts. Newest is the highest installed version; Older says the running kernel is
// higher than every installed one, i.e. a reboot would go back to an older kernel.
type kernelInfo struct {
	Newest string
	Older  bool
	// Candidates are the installed kernels the boot menu can end up on: the highest one (the default) and every lower one that is
	// not older than the running kernel (what GRUB falls back to). Each needs its boot files and, where the module is in
	// use, the module.
	Candidates []string
}

// bootKernel tells the kernel GRUB starts next (the highest installed version) and compares it with the running one. Outside a
// container a kernel that cannot be told is a failure (kernel_unknown): nothing can be checked about it.
func (r *run) bootKernel(ctx context.Context) (kernelInfo, *Failure) {
	unknown := func(why string) (kernelInfo, *Failure) {
		f := fail(CodeKernelUnknown, "the kernel the next boot starts could not be told (%s)", why)
		f.Reasons = []string{CodeKernelUnknown}
		return kernelInfo{}, f
	}
	cctx, cancel := context.WithTimeout(ctx, kernelListTimeout)
	defer cancel()
	list, err := r.query(cctx, "linux-version", "list")
	if err != nil {
		return unknown("linux-version list failed")
	}
	var versions []string
	for _, v := range strings.Fields(list) {
		if safeKernelVersion(v) {
			versions = append(versions, v)
		}
	}
	if len(versions) == 0 {
		return unknown("no installed kernel")
	}
	running := r.host.KernelRunning()
	all := append([]string(nil), versions...)
	if safeKernelVersion(running) && !contains(versions, running) {
		all = append(all, running)
	}
	newest := ""
	sorted, err := r.query(cctx, append([]string{"linux-version", "sort", "--reverse"}, all...)...)
	if err != nil {
		return unknown("linux-version sort failed")
	}
	// The sorted list is highest first. The first installed version in it is what GRUB starts; when the running kernel (not
	// among the installed ones) comes before it, the next boot goes down to an older kernel.
	newIdx, runIdx := -1, -1
	fields := strings.Fields(sorted)
	for i, v := range fields {
		if newIdx < 0 && contains(versions, v) {
			newIdx = i
			newest = v
		}
		if runIdx < 0 && v == running {
			runIdx = i
		}
	}
	if newIdx < 0 {
		return unknown("linux-version returned nothing usable")
	}
	var cands []string
	for i, v := range fields {
		if contains(versions, v) && (runIdx < 0 || i <= runIdx) {
			cands = append(cands, v)
		}
	}
	return kernelInfo{Newest: newest, Older: runIdx >= 0 && runIdx < newIdx, Candidates: cands}, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// moduleBuilt: the AmneziaWG module for kernel k exists. modinfo is asked first (it knows what depmod indexed, wherever the
// file is); the usual places (DKMS on Ubuntu, the plain build on Debian) are the fallback for a tree depmod has not been run on.
// The job runs as root outside the agent's sandbox, so it can see /lib/modules.
func (r *run) moduleBuilt(ctx context.Context, k string) bool {
	if out, err := r.query(ctx, "modinfo", "-k", k, "-F", "filename", "amneziawg"); err == nil && strings.TrimSpace(out) != "" {
		return true
	}
	base := "/lib/modules/" + k
	for _, p := range []string{base + "/updates/dkms/amneziawg.ko*", base + "/updates/amneziawg.ko*", base + "/extra/amneziawg.ko*"} {
		if len(r.host.Glob(p)) > 0 {
			return true
		}
	}
	return false
}

// awgModule asks DKMS to build the module for the kernel that was just installed (Ubuntu). It installs no package: the headers
// come with the kernel's own packages, and a node whose headers are missing is left with a module that is missing for that
// kernel, which holds the reboot. A build of the module that fails stops the job (awg_module_build_failed); nothing is removed.
func (r *run) awgModule(ctx context.Context) *Failure {
	if r.container != "" {
		return nil
	}
	ki, f := r.bootKernel(ctx)
	if f != nil || ki.Older || ki.Newest == r.host.KernelRunning() {
		return nil // nothing to build for; if a reboot is asked for, the checks before it say what is wrong
	}
	k := ki.Newest
	table, _, _ := r.dpkgTable(ctx)
	if !r.host.ModuleLoaded() && !installedStatus(table, "amneziawg-dkms") {
		return nil
	}
	r.phase(PhaseAwgModule)
	if r.distro != "ubuntu" {
		// Debian builds the module by hand, not with DKMS: it stays missing for the new kernel until prepare-kernel runs.
		fmt.Fprintln(r.out, "the AmneziaWG module of this host is a plain build: it is not rebuilt for the new kernel")
		return nil
	}
	dkmsOK := func() bool {
		out, err := r.query(ctx, "dkms", "status", "-m", "amneziawg", "-k", k)
		return err == nil && dkmsStatusInstalled(out, k)
	}
	if dkmsOK() {
		return nil
	}
	r.fixSource()
	out, err := r.cmdKeep(ctx, "dkms", "autoinstall", "-k", k)
	if err != nil && ownDKMSFailure(out) && r.fixSource() {
		// Repaired just now: finish dpkg (the failed build left the package half configured), then the build once more.
		if f := r.repairIfNeeded(ctx); f != nil {
			return f
		}
		if dkmsOK() {
			return nil
		}
		out, err = r.cmdKeep(ctx, "dkms", "autoinstall", "-k", k)
	}
	if err != nil && ownDKMSFailure(out) {
		return r.buildFailed(ctx, out, k)
	}
	if _, half, err := r.dpkgTable(ctx); err == nil {
		for _, p := range half {
			if strings.HasPrefix(p, "amneziawg") {
				return r.buildFailed(ctx, "", k) // the module's own package is left half configured by its build
			}
		}
	}
	return nil
}

// detectContainer says what kind of container the job runs in, "" on a machine of its own. systemd's own marker comes first, then
// systemd-detect-virt (which prints "none" and exits 1 outside one), then OpenVZ's /proc/vz without /proc/bc: the way the module
// preparation (awgprep) tells it, so that the two agree.
func (r *run) detectContainer(ctx context.Context) string {
	if c := safeValue(r.host.text("/run/systemd/container"), 48); c != "" {
		return c
	}
	out, _ := r.query(ctx, "systemd-detect-virt", "--container")
	if c := safeValue(strings.TrimSpace(out), 48); c != "" && c != "none" {
		return c
	}
	if r.host.exists("/proc/vz") && !r.host.exists("/proc/bc") {
		return "openvz"
	}
	return ""
}
