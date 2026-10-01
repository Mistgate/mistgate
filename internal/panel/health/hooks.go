package health

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The rest of fleet.Health (DoctorReport is in doctor.go).

// NodeReturned writes the history record of a host blip: an INFO alert that was never active, from the start of the
// silence to the return (the hoster blipped: an event, no alert, no reboot). It never notifies.
func (s *Service) NodeReturned(ctx context.Context, nodeID string, silentSince, now time.Time) {
	a := store.HealthAlert{Kind: kHostBlip, Severity: sevInfo, NodeID: nodeID, TitleKey: titleKey(kHostBlip), WhyKey: "health.alert.host_blip.why",
		Params:    map[string]string{"minutes": strconv.Itoa(int(now.Sub(silentSince).Minutes()))},
		FirstSeen: silentSince, LastSeen: now, ResolvedAt: now, Resolution: "node_returned", CreatedAt: now}
	if err := s.st.InsertResolvedAlert(ctx, a); err != nil {
		s.log.Warn("health: blip record", "node", nodeID, "err", err)
	}
}

// NodeHealth is the synthetic-check and doctor view of a node for its status (as of the last evaluation).
func (s *Service) NodeHealth(nodeID string) (noTraffic bool, failed, total int, doctorFail string) {
	s.nhMu.RLock()
	h := s.nodeHealth[nodeID]
	s.nhMu.RUnlock()
	return h.noTraffic, h.failed, h.total, h.doctorFail
}

// AlertCounts is the badge: active alerts that are not muted and not INFO, and the critical ones among them.
func (s *Service) AlertCounts(ctx context.Context) (active, critical uint32) {
	a, c, err := s.st.AlertCounts(ctx, s.now())
	if err != nil {
		s.log.Warn("health: alert counts", "err", err)
		return 0, 0
	}
	return uint32(a), uint32(c)
}

func jsonString(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}
