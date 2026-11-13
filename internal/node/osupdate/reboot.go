package osupdate

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// rebootStep is the last step of a job. It reboots only when the request asked for it, a reboot is needed and every check
// below passes; a refusal is a failed job with reboot = held, and nothing was rebooted.
func (r *run) rebootStep(ctx context.Context) error {
	spec := r.j.Spec
	if spec.Reboot == RebootNever {
		_ = r.write(func(s *Status) { s.Reboot = RebootNotAsked })
		return nil
	}
	held := func(f *Failure, kernel string) error {
		r.unlock()
		_ = r.write(func(s *Status) { s.Reboot, s.HeldReason, s.KernelTo = RebootHeld, f.Code, kernel })
		return f
	}
	r.phase(PhaseChecking)
	running := r.host.KernelRunning()
	var ki kernelInfo
	if r.container == "" {
		var f *Failure
		if ki, f = r.bootKernel(ctx); f != nil {
			if cf := ctxFailure(ctx); cf != nil {
				return cf
			}
			return held(f, "")
		}
	}
	switched := ki.Newest != "" && ki.Newest != running && !ki.Older
	required := r.host.exists("/run/reboot-required") || switched
	if spec.Reboot == RebootIfRequired && !required {
		_ = r.write(func(s *Status) { s.Reboot = RebootNotRequired })
		return nil
	}
	if f := r.rebootChecks(ctx, ki, switched); f != nil {
		if f.Code == CodeTimeout || f.Code == CodeCancelled {
			r.unlock()
			return f
		}
		return held(f, ki.Newest)
	}
	if f := ctxFailure(ctx); f != nil {
		r.unlock()
		return f
	}

	// The point of no return: the status says "rebooting" and is on disk before the command, so a host that comes back
	// finds it. Until the command runs, the job can still be cancelled. The dpkg lock is held from the checks to the end.
	to := ki.Newest
	if to == "" {
		to = running
	}
	r.phase(PhaseRebooting)
	_ = r.write(func(s *Status) { s.KernelTo, s.Reboot, s.AwgRequired = to, "", s.AwgRequired || r.awgNeeded })
	grace := r.j.RebootGrace
	if grace <= 0 {
		grace = RebootGrace
	}
	r.host.Sleep(ctx, grace)
	if f := ctxFailure(ctx); f != nil {
		r.unlock()
		return f
	}
	// The reboot is counted before the command, on disk: a reboot that happens must never be missing from the count.
	if err := r.recordReboot(); err != nil {
		r.unlock()
		_ = r.write(func(s *Status) { s.Reboot, s.HeldReason = RebootHeld, CodeRebootLimit })
		return fail(CodeRebootLimit, "the reboot could not be recorded in the reboot history, so it is not done: %v", err)
	}
	if _, err := r.cmdKeep(ctx, "systemctl", "reboot"); err != nil {
		r.unlock()
		_ = r.write(func(s *Status) { s.Reboot, s.HeldReason = RebootHeld, CodeRebootFailed })
		return fail(CodeRebootFailed, "the reboot command was refused")
	}
	wait := r.j.RebootWait
	if wait <= 0 {
		wait = RebootWait
	}
	deadline := r.host.Now().Add(wait)
	for r.host.Now().Before(deadline) {
		r.host.Sleep(ctx, HeartbeatEvery)
		if ctx.Err() != nil {
			// Shutdown stops this unit: that is the reboot happening, not a failure. The file keeps saying "rebooting".
			return errRebooting
		}
	}
	r.unlock()
	_ = r.write(func(s *Status) { s.Reboot, s.HeldReason = RebootHeld, CodeRebootFailed })
	return fail(CodeRebootFailed, "the host did not go down after the reboot command")
}

// rebootChecks refuses the reboot with every reason that applies: the first is the failure's code. The checks look at the
// kernel the next boot starts, whether or not it is a new one. When they pass, the dpkg lock is held (r.release) until the
// reboot, so that no other package manager starts in the pause before it.
func (r *run) rebootChecks(ctx context.Context, ki kernelInfo, switched bool) *Failure {
	var codes, parts []string
	var pkg string
	add := func(code, format string, a ...any) {
		codes = append(codes, code)
		parts = append(parts, fmt.Sprintf(format, a...))
	}
	if up, ok := r.host.Uptime(); !ok {
		add(CodeTooSoon, "the uptime of the host could not be read")
	} else if up < MinUptime {
		add(CodeTooSoon, "the host booted %d minutes ago; a reboot is done after %d minutes of uptime at the earliest", int(up/time.Minute), int(MinUptime/time.Minute))
	}
	if msg := r.rebootLimitProblem(); msg != "" {
		add(CodeRebootLimit, "%s", msg)
	}
	locked := false
	if r.waitLock(ctx, time.Minute, false) != nil {
		if f := ctxFailure(ctx); f != nil {
			return f
		}
		add(CodeAptLock, "another package manager holds the dpkg lock")
	} else if release, err := r.host.HoldAptLock(); err != nil {
		add(CodeAptLock, "the dpkg lock could not be taken")
	} else {
		r.release, locked = release, true
	}
	if _, half, err := r.dpkgTable(ctx); err != nil {
		add(CodeDpkgInterrupted, "the package database could not be read")
	} else if len(half) > 0 {
		pkg = half[0]
		add(CodeDpkgInterrupted, "interrupted package installs: %s", strings.Join(firstN(half, 5), ", "))
	}
	if r.container == "" {
		// Every kernel the boot menu can end up on is checked, not only the newest: after a failed boot GRUB falls back to the
		// next one, and a fallback without its boot files or without the module is a node that does not come back.
		needModule := r.awgNeeded || r.host.ModuleLoaded()
		var noModule, noBoot []string
		for _, c := range ki.Candidates {
			if needModule && !r.moduleBuilt(ctx, c) {
				noModule = append(noModule, c)
			}
			if !r.bootFilesOK(c) {
				noBoot = append(noBoot, c)
			}
		}
		if len(noModule) > 0 {
			add(CodeAwgModuleMissing, "the AmneziaWG module is not built for kernel %s and AmneziaWG uses the kernel module", strings.Join(firstN(noModule, 3), ", "))
		}
		if ki.Older {
			add(CodeKernelOlder, "the next boot would start kernel %s, which is older than the running one", ki.Newest)
		}
		if len(noBoot) > 0 {
			add(CodeInitramfsMissing, "the boot files of kernel %s are missing or incomplete", strings.Join(firstN(noBoot, 3), ", "))
		}
		// No grub.cfg at all is boot_default_custom's to say (the boot loader may not be GRUB); here the file exists and lacks the entry.
		if r.host.exists("/boot/grub/grub.cfg") && !grubNames(r.host.text("/boot/grub/grub.cfg"), ki.Newest) {
			add(CodeBootEntryMissing, "/boot/grub/grub.cfg has no menu entry for kernel %s, which the next boot starts", ki.Newest)
		}
		if free, ok := r.host.FreeMB("/boot"); switched && ok && free < minBootFreeMB {
			add(CodeBootFull, "/boot has only %d MB free", free)
		}
		if msg := r.grubProblem(ctx); msg != "" {
			add(CodeBootDefaultCustom, "%s", msg)
		}
	}
	if len(codes) == 0 {
		return nil
	}
	if locked {
		r.unlock()
	}
	f := fail(codes[0], "reboot refused: %s", strings.Join(parts, "; "))
	f.Package, f.Reasons = pkg, codes
	return f
}

func (r *run) bootFilesOK(k string) bool {
	if size, ok := r.host.Size("/boot/vmlinuz-" + k); !ok || size == 0 {
		return false
	}
	// A /boot that filled up leaves a truncated initramfs behind; a real one is several MB.
	size, ok := r.host.Size("/boot/initrd.img-" + k)
	return ok && size >= minInitramfsBytes
}

func firstN(in []string, n int) []string {
	if len(in) > n {
		return in[:n]
	}
	return in
}
