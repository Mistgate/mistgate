package update

import (
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
)

// OSUpdatesEnabled tells Fleet whether it may ask capable streams for the read-only status snapshot.
func (s *Service) OSUpdatesEnabled() bool { return !s.cfg.Edge }

func shouldReadPanelHostStatus(edge, rootPanel, hasReader bool) bool {
	return !edge && rootPanel && hasReader
}

// PanelHostOsStatus is the last in-process read from the panel's own host. It does not start a read.
func (s *Service) PanelHostOsStatus() *agentv1.OsStatus {
	if s.osStatusMonitor == nil || !s.OSUpdatesEnabled() {
		return nil
	}
	return s.osStatusMonitor.Snapshot()
}

// osStatusStaleAfter is how old a snapshot's measured time may get while the node is connected. The agent reads in full
// at least every osupdate.FullReadInterval and sends the result, so a snapshot older than three of those has stopped
// arriving; the floor keeps a short interval from flagging a host that only missed one cycle.
var osStatusStaleAfter = max(12*time.Hour, 3*osupdate.FullReadInterval)

func osUpdateSupported(capable bool, status *agentv1.OsStatus) bool {
	if !capable {
		return false
	}
	if status != nil && status.GetError() == "not_apt" {
		return false
	}
	// A failed os-release read leaves the distribution empty: the host is unknown, not unsupported.
	return status == nil || status.GetDistroId() == "ubuntu" || status.GetDistroId() == "debian" ||
		status.GetDistroId() == "" && status.GetError() == "read_failed"
}

func osStatusStale(status *agentv1.OsStatus, nowUnix int64) bool {
	return time.Duration(nowUnix-status.GetMeasuredUnix())*time.Second > osStatusStaleAfter
}

func osUpdateState(capable bool, connected bool, status *agentv1.OsStatus, nowUnix int64) adminv1.OsUpdateState {
	supported := osUpdateSupported(capable, status)
	switch {
	case !supported:
		return adminv1.OsUpdateState_OS_UPDATE_STATE_UNSUPPORTED
	case !connected:
		return adminv1.OsUpdateState_OS_UPDATE_STATE_OFFLINE
	case status == nil:
		return adminv1.OsUpdateState_OS_UPDATE_STATE_UNKNOWN
	case osStatusStale(status, nowUnix):
		// A connected node that stopped sending is not in the state it last reported.
		return adminv1.OsUpdateState_OS_UPDATE_STATE_UNKNOWN
	case status.GetJob() != nil && status.GetJob().GetState() == "running":
		return adminv1.OsUpdateState_OS_UPDATE_STATE_BUSY
	case status.GetJob() != nil && status.GetJob().GetState() == "failed" && !osJobAskedToWait(status.GetJob()):
		return adminv1.OsUpdateState_OS_UPDATE_STATE_FAILED
	case status.GetRebootRequired():
		return adminv1.OsUpdateState_OS_UPDATE_STATE_REBOOT_REQUIRED
	case status.GetError() != "" || status.GetApt() != "ok" && !(status.GetApt() == "half_configured" && status.GetPending() > 0):
		return adminv1.OsUpdateState_OS_UPDATE_STATE_UNKNOWN
	case status.GetSecurity() > 0 || status.GetApt() == "half_configured" && status.GetPending() > 0:
		// Security updates ask for an install; so does a half-installed package (the job repairs it first).
		return adminv1.OsUpdateState_OS_UPDATE_STATE_UPDATES
	default:
		// Nothing security is waiting and no reboot is due: the server is in order. Ordinary updates (the other packages)
		// that wait are a note beside the count, not the state: otherwise a server could never look all good.
		return adminv1.OsUpdateState_OS_UPDATE_STATE_UP_TO_DATE
	}
}
