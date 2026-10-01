// Package awgprep puts the AmneziaWG kernel module on a node.
//
// Two callers share it. `mistgate-node awg prepare-kernel` is the explicit, root-only command: it prints the plan and
// runs it with --yes. The agent (Controller, ctl.go) starts the same command, in the background, when the panel asks for
// it ("awg-prepare/1", agent.proto "AWG AND WARP"): the agent never installs a package inside its own hardened unit; it
// starts a separate transient systemd unit that runs the command, and follows it through a status file (status.go).
//
// The userspace backend (amneziawg-go inside the agent) is the default and needs none of this; the module is for nodes
// where its much smaller per-peer memory matters (1.8 KiB against 26 KiB).
package awgprep

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

const (
	// pinnedModuleTag and pinnedModuleCommit are the release of amneziawg-linux-kernel-module the Debian source build
	// checks out. The commit is verified after the clone: a moved tag is not trusted.
	pinnedModuleTag    = "v3.1.20260906"
	pinnedModuleCommit = "4569c4c"
	moduleRepo         = "https://github.com/amnezia-vpn/amneziawg-linux-kernel-module"
	modulesLoadFile    = "/etc/modules-load.d/amneziawg.conf"

	// aptLockWait is how long apt may wait for another package manager (unattended-upgrades on a fresh VPS) before the
	// install gives up; the job also waits for the lock before it starts (job.go) instead of failing at once.
	aptLockWait = "300"
)

// Codes of Unsupported.Code and Failure.Code: a stable vocabulary (agent.proto "AWG AND WARP"), the UI words them.
const (
	CodeContainer     = "container"
	CodeDistro        = "distro"
	CodeUnknownKernel = "unknown_kernel"
	CodeSecureBoot    = "secure_boot"
	CodeNoSystemd     = "no_systemd"

	CodeTimeout        = "timeout"
	CodeAptLock        = "apt_lock"
	CodeStepFailed     = "step_failed"
	CodeSourceMoved    = "source_moved"
	CodeModuleNotLoad  = "module_not_loaded"
	CodeModuleUnusable = "module_unusable"
	CodeInterrupted    = "interrupted"
	CodeLaunchFailed   = "launch_failed"
)

// Unsupported says that the module cannot be put on this machine, and why. It is a fact about the host, not a failure.
type Unsupported struct {
	Code string // CodeContainer, CodeDistro, CodeUnknownKernel, CodeSecureBoot or CodeNoSystemd
	Msg  string
}

func (u *Unsupported) Error() string { return u.Msg }

// Env is what the plan depends on; the Linux code reads it from the machine (real_linux.go), a test builds it by hand.
type Env struct {
	OSRelease  map[string]string // /etc/os-release
	Kernel     string            // uname -r
	CPUs       int
	Container  string // virtualisation of a container kind ("lxc", "openvz", "docker", ...), "" = none
	SecureBoot bool
	Systemd    bool // systemd-run is there and systemd is the init (the automatic run needs it)
}

// Step is one command of the plan. Dir is optional. Check, when set, runs after the command and fails the step with its
// error (the pinned commit of the clone).
type Step struct {
	Desc  string
	Cmd   []string
	Dir   string
	Check func(out string) error
}

// aptInstall is `apt-get install` that never asks, waits for the dpkg lock and does not pull recommended packages (dkms
// recommends linux-headers-generic, which is neither the running kernel nor small).
func aptInstall(pkgs ...string) []string {
	return append([]string{"apt-get", "-o", "DPkg::Lock::Timeout=" + aptLockWait, "install", "-y", "--no-install-recommends"}, pkgs...)
}

// Plan returns the commands for this machine, or an *Unsupported that says why the module cannot be installed here.
// notes are facts the owner should read before confirming.
func Plan(e Env) (steps []Step, notes []string, err error) {
	if e.Container != "" {
		return nil, nil, &Unsupported{CodeContainer, fmt.Sprintf("this is a %s container: a container cannot load kernel modules, use the userspace backend (it needs /dev/net/tun)", e.Container)}
	}
	if e.Kernel == "" || strings.ContainsAny(e.Kernel, " /\n'\"$") {
		return nil, nil, &Unsupported{CodeUnknownKernel, fmt.Sprintf("cannot tell the kernel release (%q)", e.Kernel)}
	}
	if e.SecureBoot {
		notes = append(notes, "Secure Boot is on: the module is unsigned, modprobe will be refused (\"Key was rejected by service\") unless you enroll a key (MOK) or turn Secure Boot off; the userspace backend needs neither")
	}
	id, like := strings.ToLower(e.OSRelease["ID"]), strings.ToLower(e.OSRelease["ID_LIKE"])
	headers := "linux-headers-" + e.Kernel
	switch {
	case id == "ubuntu":
		steps = []Step{
			{Desc: "refresh the package index", Cmd: []string{"apt-get", "update"}},
			{Desc: "install the PPA tooling and the headers of the running kernel (the DKMS package does not depend on them)",
				Cmd: aptInstall("software-properties-common", "python3-launchpadlib", "gnupg2", headers)},
			{Desc: "add the Amnezia PPA", Cmd: []string{"add-apt-repository", "-y", "ppa:amnezia/ppa"}},
			{Desc: "install amneziawg (the DKMS source of the module, rebuilt for every new kernel, and the awg tools)",
				Cmd: aptInstall("amneziawg")},
		}
		notes = append(notes, "DKMS rebuilds the module after a kernel upgrade; the build takes about a minute on a small VPS and also rebuilds the initramfs")
	case id == "debian" || (strings.Contains(like, "debian") && id != ""):
		cpus := max(e.CPUs, 1)
		steps = []Step{
			{Desc: "refresh the package index", Cmd: []string{"apt-get", "update"}},
			{Desc: "install the build tools and the headers of the running kernel", Cmd: aptInstall("git", "make", "gcc", headers)},
			{Desc: "fetch the pinned source " + pinnedModuleTag, Cmd: []string{"git", "clone", "--depth", "1", "--branch", pinnedModuleTag, moduleRepo, "{src}"}},
			{Desc: "check the commit of the clone", Cmd: []string{"git", "-C", "{src}", "rev-parse", "HEAD"}, Check: func(out string) error {
				if !strings.HasPrefix(strings.TrimSpace(out), pinnedModuleCommit) {
					return &Failure{Code: CodeSourceMoved, Detail: fmt.Sprintf("the clone is at %q, the pinned commit is %s: the tag moved, nothing was built", strings.TrimSpace(out), pinnedModuleCommit)}
				}
				return nil
			}},
			{Desc: "build the module against the installed headers", Cmd: []string{"make", "-C", "{src}/src", "KERNELDIR=/lib/modules/" + e.Kernel + "/build", fmt.Sprintf("-j%d", cpus)}},
			{Desc: "install the module for the running kernel", Cmd: []string{"install", "-D", "-m", "0644", "{src}/src/amneziawg.ko", "/lib/modules/" + e.Kernel + "/updates/amneziawg.ko"}},
			{Desc: "update the module dependency index", Cmd: []string{"depmod", "-a", e.Kernel}},
		}
		notes = append(notes, "this is a plain build without DKMS: after a kernel upgrade run this command again (the agent falls back to userspace until you do, and the doctor says so)")
	default:
		return nil, nil, &Unsupported{CodeDistro, fmt.Sprintf("no recipe for %q: Ubuntu (the Amnezia PPA) and Debian (a source build) are supported; for anything else build the module by hand "+
			"(https://docs.amnezia.org/documentation/instructions/install-amneziawg-kernel-module-linux) and run this command's checks with --verify-only", e.OSRelease["PRETTY_NAME"])}
	}
	steps = append(steps, Step{Desc: "load the module", Cmd: []string{"modprobe", "amneziawg"}})
	return steps, notes, nil
}

// PlanAuto is Plan for the run the panel asks for: it also refuses what only a person can fix halfway through. Secure
// Boot makes modprobe refuse the unsigned module after hundreds of MB were installed, and the automatic run is started
// by systemd-run.
func PlanAuto(e Env) ([]Step, []string, error) {
	if e.Container == "" && e.SecureBoot {
		return nil, nil, &Unsupported{CodeSecureBoot, "Secure Boot is on: the unsigned module would be refused by the kernel (enroll a key (MOK) or turn Secure Boot off, then run the command by hand)"}
	}
	if e.Container == "" && !e.Systemd {
		return nil, nil, &Unsupported{CodeNoSystemd, "systemd-run is not available: the build runs as a separate systemd unit, run the command by hand"}
	}
	return Plan(e)
}

// ParseOSRelease reads KEY=value lines (values may be quoted).
func ParseOSRelease(r io.Reader) map[string]string {
	m := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		m[k] = strings.Trim(v, `"'`)
	}
	return m
}
