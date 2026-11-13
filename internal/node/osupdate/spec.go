package osupdate

import (
	"fmt"
	"regexp"
	"strings"
)

// CapabilityUpdate is listed in Hello.capabilities by an agent that can run OS jobs (agent.proto "OS UPDATES"). The panel
// sends UpdateOs only to a stream that lists it.
const CapabilityUpdate = "os-update/1"

// The enumerated vocabulary of a job. Nothing else is ever accepted from the wire or the command line.
const (
	ActionRefresh = "refresh"
	ActionUpgrade = "upgrade"
	ActionReboot  = "reboot"

	ModeSecurity = "security"
	ModeAll      = "all"

	RebootNever      = "never"
	RebootIfRequired = "if_required"
	RebootAlways     = "always"
)

// Codes of a refusal or a failed job (OsJob.code). The vocabulary is part of agent.proto "OS UPDATES".
const (
	CodeUnsupportedOS     = "unsupported_os"
	CodeNoSystemd         = "no_systemd"
	CodeBadRequest        = "bad_request"
	CodeBusy              = "busy"
	CodeDiskFull          = "disk_full"
	CodeAptLock           = "apt_lock"
	CodeDpkgBroken        = "dpkg_broken"
	CodeSecuritySource    = "security_source_failed"
	CodeRefreshFailed     = "refresh_failed"
	CodeInstallFailed     = "install_failed"
	CodeAutoReboot        = "auto_reboot_conflict"
	CodeDpkgInterrupted   = "dpkg_interrupted"
	CodeAwgModuleMissing  = "awg_module_missing"
	CodeBootFull          = "boot_full"
	CodeInitramfsMissing  = "initramfs_missing"
	CodeRebootFailed      = "reboot_failed"
	CodeTimeout           = "timeout"
	CodeInterrupted       = "interrupted"
	CodeCancelled         = "cancelled"
	CodeLaunchFailed      = "launch_failed"
	CodeKernelNotSwitched = "kernel_not_switched"
	CodeAwgNotLoaded      = "awg_module_not_loaded"
	CodeInboundFailed     = "inbound_failed"
	// CodeKernelUnknown: the kernel the next boot starts could not be told, so no check of it is possible.
	CodeKernelUnknown = "kernel_unknown"
	// CodeKernelOlder: the next boot would start an older kernel than the running one.
	CodeKernelOlder = "kernel_older"
	// CodeAwgBuildFailed: the AmneziaWG module's own DKMS build failed. The job stops; nothing is removed or installed after it.
	CodeAwgBuildFailed = "awg_module_build_failed"
	// CodeBootDefaultCustom: what GRUB starts is not "the newest kernel": a custom default, a saved or next entry, or not GRUB.
	CodeBootDefaultCustom = "boot_default_custom"
	// CodeBootEntryMissing: /boot/grub/grub.cfg does not name the image of the kernel the next boot starts.
	CodeBootEntryMissing = "boot_entry_missing"
	// CodeRebootLimit: the node reboots at most once an hour and three times a day for jobs of the panel.
	CodeRebootLimit = "reboot_limit"
	// CodeTooSoon: the host booted less than MinUptime ago; a second reboot so soon is not done.
	CodeTooSoon = "too_soon"
)

// NoteSecurityLeftoverFailed is a non-fatal result note: unattended-upgrade completed, but its named security leftovers did not install.
const NoteSecurityLeftoverFailed = "security_leftover_failed"

// Codes lists every code a job or a refusal can carry; the panel checks an incoming job against it.
var Codes = []string{CodeUnsupportedOS, CodeNoSystemd, CodeBadRequest, CodeBusy, CodeDiskFull, CodeAptLock, CodeDpkgBroken, CodeSecuritySource,
	CodeRefreshFailed, CodeInstallFailed, CodeAutoReboot, CodeDpkgInterrupted, CodeAwgModuleMissing, CodeBootFull, CodeInitramfsMissing,
	CodeRebootFailed, CodeTimeout, CodeInterrupted, CodeCancelled, CodeLaunchFailed, CodeKernelNotSwitched, CodeAwgNotLoaded, CodeInboundFailed,
	CodeKernelUnknown, CodeKernelOlder, CodeAwgBuildFailed, CodeTooSoon, CodeBootDefaultCustom, CodeBootEntryMissing, CodeRebootLimit}

// RebootBlockers are the codes of Codes that the reader's reboot preview (OsStatus.reboot_blockers) can carry.
var RebootBlockers = []string{CodeInitramfsMissing, CodeBootEntryMissing, CodeAwgModuleMissing, CodeDpkgInterrupted, CodeKernelOlder, CodeBootDefaultCustom}

// Phases lists the phases of a running job.
var Phases = []string{PhasePreflight, PhaseWaitingLock, PhaseRepairing, PhaseRefreshing, PhaseInstalling, PhaseAwgModule, PhaseChecking, PhaseRebooting}

// Job phases (OsJob.phase).
const (
	PhasePreflight   = "preflight"
	PhaseWaitingLock = "waiting_lock"
	PhaseRepairing   = "repairing"
	PhaseRefreshing  = "refreshing"
	PhaseInstalling  = "installing"
	PhaseAwgModule   = "awg_module"
	PhaseChecking    = "checking"
	PhaseRebooting   = "rebooting"
)

// Job states (OsJob.state) and the reboot results (OsJob.reboot).
const (
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"

	RebootDone        = "done"
	RebootNotRequired = "not_required"
	RebootHeld        = "held"
	RebootNotAsked    = "not_asked"
)

// Spec is one job as the node understands it. Every field is checked by ParseSpec; a Spec never carries free text.
type Spec struct {
	JobID  string
	Action string
	Mode   string
	Reboot string
	// AwgRequired: AmneziaWG inbounds run on the kernel module, so a reboot into a kernel without the module is refused.
	// The agent sets it from the backend the engine really uses; the job also looks at the module's use count itself.
	AwgRequired bool
}

// Refusal is a request the node will not run, with the code the caller gets back.
type Refusal struct {
	Code, Reason string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Reason }

var jobIDPattern = regexp.MustCompile(`^(osj_|manual-)[a-z0-9_-]{1,48}$`)

// ValidJobID: an id made by the panel (osj_...) or by a hand-started run (manual-<unix>).
func ValidJobID(s string) bool { return jobIDPattern.MatchString(s) }

// ParseSpec checks the enumerated fields of UpdateOs (or of the command line). Allowed combinations:
// refresh + never; upgrade + (security | all) + (never | if_required); reboot + (if_required | always). The reboot value
// of the command line may be spelled if-required. An empty jobID is accepted: the caller makes one.
func ParseSpec(action, mode, reboot, jobID string) (Spec, *Refusal) {
	reboot = strings.ReplaceAll(reboot, "-", "_")
	bad := func(format string, a ...any) (Spec, *Refusal) {
		return Spec{}, &Refusal{Code: CodeBadRequest, Reason: fmt.Sprintf(format, a...)}
	}
	if jobID != "" && !ValidJobID(jobID) {
		return bad("the job id is not valid")
	}
	spec := Spec{JobID: jobID, Action: action, Mode: mode, Reboot: reboot}
	switch action {
	case ActionRefresh:
		if mode != "" || reboot != RebootNever {
			return bad("refresh takes no mode and reboot never")
		}
	case ActionUpgrade:
		if mode != ModeSecurity && mode != ModeAll {
			return bad("upgrade needs mode security or all")
		}
		if reboot != RebootNever && reboot != RebootIfRequired {
			return bad("upgrade needs reboot never or if_required")
		}
	case ActionReboot:
		if mode != "" {
			return bad("reboot takes no mode")
		}
		if reboot != RebootIfRequired && reboot != RebootAlways {
			return bad("reboot needs if_required or always")
		}
	default:
		return bad("unknown action")
	}
	return spec, nil
}
