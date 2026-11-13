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
	"path"
	"strings"
)

const (
	// pinnedModuleTag and pinnedModuleCommit are the release of amneziawg-linux-kernel-module the Debian source build
	// checks out. The commit is verified after the clone: a moved tag is not trusted.
	pinnedModuleTag    = "v3.1.20260906"
	pinnedModuleCommit = "4569c4c"
	moduleRepo         = "https://github.com/amnezia-vpn/amneziawg-linux-kernel-module"
	modulesLoadFile    = "/etc/modules-load.d/amneziawg.conf"
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
	CodeBuildFailed    = "module_build_failed" // the module does not compile for this kernel (an API it has no answer for yet)
	CodeInterrupted    = "interrupted"
	CodeLaunchFailed   = "launch_failed"
	CodeBusy           = "busy"
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
	NodePath   string            // the installed node binary, for the Ubuntu apt hook
	StateDir   string            // the agent's state directory, for the Ubuntu apt hook
	CPUs       int
	Container  string // virtualisation of a container kind ("lxc", "openvz", "docker", ...), "" = none
	SecureBoot bool
	Systemd    bool // systemd-run is there and systemd is the init (the automatic run needs it)
	// Leftover are the module's packages an earlier run left half-installed (dpkg unpacked or half-configured): apt
	// refuses every install on the host until they are configured or gone.
	Leftover []string
}

// FileOp is one file change in a plan. Remove deletes a file if it exists; otherwise Run writes Body atomically with
// mode 0644, creating its parent directory with mode 0755.
type FileOp struct {
	Path   string
	Body   string
	Remove bool
}

// Step is one command or a set of file changes in the plan. Dir is optional. Check, when set, runs after the command
// and fails the step with its error (the pinned commit of the clone).
type Step struct {
	Desc  string
	Cmd   []string
	Files []FileOp
	Dir   string
	Check func(out string) error
	// Prep runs before the command, with the source directory ({src} replaced): the Debian recipe makes the module source
	// right here (FixCompat) before the compiler sees it. An error fails the step.
	Prep func(src string, w io.Writer) error
	// Build marks the step that compiles the module: its failure is module_build_failed. An apt step is one too when its
	// output shows the DKMS build of the module failing.
	Build bool
	// Undo runs when the step fails because the module did not build. A DKMS package whose build failed stays
	// half-configured, and apt then refuses every later install on the host; Undo removes it again.
	Undo []string
	// RunKernel is the release of the running kernel the plan was made for. Undo only removes the module when the failed build
	// was for that kernel: a build that fails for another (the new kernel's headers configured on the side) leaves the module
	// that works for the running kernel alone. "" = not known, which keeps the removal.
	RunKernel string
}

// aptInstall is `apt-get install` that never asks, waits for the dpkg lock and does not pull recommended packages (dkms
// recommends linux-headers-generic, which is neither the running kernel nor small).
func aptInstall(pkgs ...string) []string {
	return append([]string{"apt-get", "-o", fmt.Sprintf("DPkg::Lock::Timeout=%d", aptLockTimeoutSeconds), "install", "-y", "--no-install-recommends"}, pkgs...)
}

// modulePackages are what the Ubuntu recipe installs for the module: amneziawg pulls amneziawg-dkms.
var modulePackages = []string{"amneziawg", "amneziawg-dkms"}

// aptPurge removes packages with their configuration; a package that is not installed is not an error.
func aptPurge(pkgs ...string) []string {
	return append([]string{"apt-get", "-o", fmt.Sprintf("DPkg::Lock::Timeout=%d", aptLockTimeoutSeconds), "purge", "-y"}, pkgs...)
}

// aptUpdate sets the same timeout for any dpkg lock apt may acquire while refreshing its package index. The explicit
// WaitAptLock check also covers the apt lists and archive locks, which DPkg::Lock::Timeout does not cover.
func aptUpdate() []string {
	return []string{"apt-get", "-o", fmt.Sprintf("DPkg::Lock::Timeout=%d", aptLockTimeoutSeconds), "update"}
}

// Plan returns the steps for this machine, or an *Unsupported that says why the module cannot be installed here.
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
		ppaFiles, err := ubuntuPPAFiles(e.OSRelease)
		if err != nil {
			return nil, nil, err
		}
		// An install that leaves the module's DKMS package unbuilt (it does not compile for this kernel) is undone, and
		// so are the leftovers of an earlier run: the host's apt must never stay broken because of this recipe.
		undo := aptPurge(modulePackages...)
		if len(e.Leftover) > 0 {
			steps = append(steps, Step{Desc: "remove the module packages an earlier run left half-installed (" + strings.Join(e.Leftover, ", ") + ")", Cmd: undo})
		}
		hook, err := ubuntuKmodHook(e)
		steps = append(steps,
			Step{Desc: "refresh the package index", Cmd: aptUpdate()},
		)
		if err != nil {
			notes = append(notes, "the apt hook for automatic AmneziaWG module rebuilds was skipped: "+err.Error())
		} else {
			steps = append(steps, Step{Desc: "install the apt hook for AmneziaWG module rebuilds", Files: []FileOp{{Path: KmodHookPath, Body: hook}}})
		}
		steps = append(steps,
			Step{Desc: "install the headers of the running kernel", Cmd: aptInstall(headers), Undo: undo, RunKernel: e.Kernel},
			Step{Desc: "add the Amnezia PPA", Files: ppaFiles},
			Step{Desc: "refresh the package index from the Amnezia PPA", Cmd: aptUpdate()},
			Step{Desc: "install amneziawg", Cmd: aptInstall("amneziawg"), Undo: undo, RunKernel: e.Kernel},
		)
		notes = append(notes, "DKMS rebuilds the module after a kernel upgrade; the build takes about a minute on a small VPS and also rebuilds the initramfs",
			"if the module does not build for the running kernel, its packages are removed again (apt stays usable) and the node stays on userspace; a build that fails for another kernel (headers configured on the side) removes nothing")
	case id == "debian" || (strings.Contains(like, "debian") && id != ""):
		cpus := max(e.CPUs, 1)
		steps = []Step{
			{Desc: "refresh the package index", Cmd: aptUpdate()},
			{Desc: "install the build tools and the headers of the running kernel", Cmd: aptInstall("git", "make", "gcc", headers)},
			{Desc: "fetch the pinned source " + pinnedModuleTag, Cmd: []string{"git", "clone", "--depth", "1", "--branch", pinnedModuleTag, moduleRepo, "{src}"}},
			{Desc: "check the commit of the clone", Cmd: []string{"git", "-C", "{src}", "rev-parse", "HEAD"}, Check: func(out string) error {
				if !strings.HasPrefix(strings.TrimSpace(out), pinnedModuleCommit) {
					return &Failure{Code: CodeSourceMoved, Detail: fmt.Sprintf("the clone is at %q, the pinned commit is %s: the tag moved, nothing was built", strings.TrimSpace(out), pinnedModuleCommit)}
				}
				return nil
			}},
			{Desc: "build the module against the installed headers", Cmd: []string{"make", "-C", "{src}/src", "KERNELDIR=/lib/modules/" + e.Kernel + "/build", fmt.Sprintf("-j%d", cpus)}, Build: true,
				Prep: func(src string, w io.Writer) error {
					return CompatErr(FixCompat(RealSys(), []string{path.Join(src, "src")}, false, w))
				}},
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

// leftoverPackages reads `dpkg-query -W -f '${Package} ${db:Status-Abbrev}\n'`: the packages dpkg left half-installed
// (H), unpacked but not configured (U) or half-configured (F).
func leftoverPackages(out string) []string {
	var left []string
	for _, line := range strings.Split(out, "\n") {
		name, abbrev, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok && len(abbrev) >= 2 && strings.ContainsRune("HUF", rune(abbrev[1])) {
			left = append(left, name)
		}
	}
	return left
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
