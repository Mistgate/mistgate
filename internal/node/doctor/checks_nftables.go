package doctor

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/nftinstall"
	"github.com/mistgate/mistgate/internal/node/unit"
)

// nftables: the server has the nft command. A cloud image of Debian 13 ships without the nftables package, and then the
// agent's SSH guard, port hopping, WARP routing and UDP delivery check cannot work (the agent says so once, as the node
// event nft_missing). WARN only. Fix install_nftables installs the package in a transient root unit, like the security
// updates job; the agent needs no restart, every firewall step looks the command up again at its next sweep.

type nftablesSnapshot struct {
	have      bool // the nft command is on PATH
	apt       bool // a Debian or Ubuntu host with apt-get: the one place the fix knows how to install
	canFix    bool // this agent can launch the job (Env.LaunchInstallNftables) and the host is one it can install on
	status    awgprep.Status
	hasStatus bool
	// The transient unit of the job as systemd sees it; "" when systemctl did not say.
	loadState   string
	activeState string
}

func (s nftablesSnapshot) jobRunning() bool { return jobUnitActive(s.activeState) }
func (s nftablesSnapshot) jobLoaded() bool  { return jobUnitLoaded(s.loadState) }

func inspectNftables(ctx context.Context, e *Env) nftablesSnapshot {
	s := nftablesSnapshot{have: e.has("nft")}
	if s.have {
		return s
	}
	osRelease, err := e.read("/etc/os-release")
	_, systemd := e.stat("/run/systemd/system")
	s.apt = err == nil && isDebianOrUbuntu(osRelease) && e.has("apt-get")
	s.canFix = s.apt && e.LaunchInstallNftables != nil && systemd == nil
	if e.StateDir != "" {
		s.status, s.hasStatus, _ = awgprep.ReadStatus(filepath.Join(e.StateDir, nftinstall.StatusFileName))
	}
	shown, _ := e.Run(ctx, "systemctl", "show", "--property=LoadState,ActiveState", nftinstall.UnitName+".service")
	s.loadState, s.activeState = parseUnitShow(string(shown))
	return s
}

func checkNftables(ctx context.Context, e *Env) Result {
	return checkNftablesResult(inspectNftables(ctx, e))
}

func checkNftablesResult(s nftablesSnapshot) Result {
	if s.have {
		return Result{Status: OK, Code: CodeNftablesOn, Detail: "the nft command is installed"}
	}
	if s.jobRunning() {
		return Result{Status: Warn, Code: CodeNftablesInstalling, Detail: "installing the nftables package"}
	}
	fix := ""
	if s.canFix {
		fix = FixInstallNftables
	}
	// The host decides; the status file only explains a host that still lacks nft (an earlier run that failed or was cut).
	if s.hasStatus {
		reason := ""
		switch s.status.State {
		case awgprep.StateRunning:
			reason = "the background task was interrupted before it finished"
		case awgprep.StateFailed:
			if reason = strings.TrimSpace(s.status.Reason); reason == "" {
				reason = "the background task failed"
			}
		}
		if reason != "" {
			reason = clip(reason, 150)
			return Result{Status: Warn, Code: CodeNftablesInstallFailed, Detail: "installing nftables failed: " + reason,
				Params: p("reason", reason), FixID: fix}
		}
	}
	return Result{Status: Warn, Code: CodeNftablesMissing,
		Detail: "the nft command is not installed: the SSH guard, port hopping, WARP routing and the UDP delivery check do not work",
		FixID:  fix}
}

func fixInstallNftables(ctx context.Context, e *Env, dry bool, params map[string]string) FixOutcome {
	if !noParams(params) {
		return FixOutcome{Err: ErrBadParams}
	}
	if e.LaunchInstallNftables == nil {
		return FixOutcome{Err: ErrUnsupportedHost}
	}
	s := inspectNftables(ctx, e)
	if s.have {
		return noop("the nft command is already installed")
	}
	if !s.canFix {
		return FixOutcome{Err: ErrNotApplicable}
	}
	if s.jobRunning() || s.jobLoaded() { // a finished unit systemd has not collected yet: systemd-run would refuse the name
		return FixOutcome{Err: ErrFixBusy}
	}
	out := FixOutcome{OK: true, Affected: 1, Params: p("package", nftinstall.Package), Detail: "would install the nftables package with apt-get"}
	if dry {
		return out
	}
	if err := e.LaunchInstallNftables(ctx); errors.Is(err, unit.ErrBusy) {
		return FixOutcome{Err: ErrFixBusy}
	} else if err != nil {
		return failed(err)
	}
	out.Params["started"] = "1" // the job runs on: the re-check shows installing, the result comes with a later report
	out.Detail = "nftables installation started in the background"
	return out
}
