package agent

import (
	"context"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
)

// osStatusConnectDelay is how long a new connection is left alone before the first read, so a read does not compete
// with the node's start-up work. Tests shorten it through Agent.osStatusDelay.
const osStatusConnectDelay = 30 * time.Second

func (a *Agent) canOSUpdate() bool {
	if a.cfg.OSUpdateCapability != nil {
		return a.cfg.OSUpdateCapability()
	}
	return osupdate.CapabilityAvailable()
}

func (a *Agent) wakeOsStatusReader() {
	select {
	case a.osStatusWake <- struct{}{}:
	default:
	}
}

func (a *Agent) sendOsStatusSnapshot(s *session) {
	if s == nil || !a.settings.Load().GetOsStatus() {
		return
	}
	if status := a.osUpdateMonitor.Snapshot(); status != nil {
		s.send(osStatusMessage(status))
	}
}

func osStatusMessage(status *pb.OsStatus) *pb.ConnectRequest {
	return &pb.ConnectRequest{Message: &pb.ConnectRequest_OsStatus{OsStatus: status}}
}

// pollOsStatus is separately callable in tests so the old-panel gate can be checked without waiting for the timer.
func (a *Agent) pollOsStatus(ctx context.Context) (*pb.OsStatus, bool) {
	if !a.settings.Load().GetOsStatus() || a.osUpdateMonitor == nil {
		return nil, false
	}
	status, changed, read, err := a.osUpdateMonitor.Poll(ctx, false)
	if err != nil {
		a.log.Warn("OS status read failed", "code", "os_status_read_failed", "err", err)
	}
	if read && changed && status != nil {
		if s := a.cur.Load(); s != nil {
			s.send(osStatusMessage(status))
		}
	}
	return status, read
}

func (a *Agent) osStatusLoop(ctx context.Context) {
	ticker := time.NewTicker(osupdate.FingerprintInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.osStatusWake:
			if !a.settings.Load().GetOsStatus() {
				continue
			}
			connected := a.connAt.Load()
			if connected == 0 {
				continue
			}
			wait := a.osStatusDelay - time.Since(time.Unix(0, connected))
			if wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			if a.settings.Load().GetOsStatus() {
				a.pollOsStatus(ctx)
			}
		case <-ticker.C:
			if a.connAt.Load() != 0 && a.settings.Load().GetOsStatus() && time.Since(time.Unix(0, a.connAt.Load())) >= a.osStatusDelay {
				a.pollOsStatus(ctx)
			}
		}
	}
}
