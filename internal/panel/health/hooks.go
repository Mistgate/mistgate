package health

import (
	"context"
	"encoding/json"
	"errors"
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

const nodeHealthSummarySetting = "health.node_summary"

// NodeHealthSummaries is the synthetic-check and doctor view of the nodes for their status, as of the last evaluation:
// a node without an entry has nothing to report. The edge admin and jobs roles have separate isolates, so the admin reads
// the one settings row (one D1 read); a request calls this once, not per node. The VPS copies its in-memory result.
func (s *Service) NodeHealthSummaries(ctx context.Context) map[string]store.NodeHealthSummary {
	if s.cfg.Edge {
		value, err := s.st.Setting(ctx, nodeHealthSummarySetting)
		if errors.Is(err, store.ErrNotFound) {
			return map[string]store.NodeHealthSummary{}
		}
		if err != nil {
			s.log.Warn("health: read node summary", "err", err)
			return map[string]store.NodeHealthSummary{}
		}
		var out map[string]store.NodeHealthSummary
		if err := json.Unmarshal([]byte(value), &out); err != nil {
			s.log.Warn("health: decode node summary", "err", err)
			return map[string]store.NodeHealthSummary{}
		}
		if out == nil {
			return map[string]store.NodeHealthSummary{}
		}
		return out
	}
	s.nhMu.RLock()
	defer s.nhMu.RUnlock()
	out := make(map[string]store.NodeHealthSummary, len(s.nodeHealth))
	for nodeID, h := range s.nodeHealth {
		out[nodeID] = store.NodeHealthSummary{NoTraffic: h.noTraffic, Failed: h.failed, Total: h.total, DoctorFail: h.doctorFail}
	}
	return out
}

func (s *Service) storeNodeHealth(ctx context.Context, current map[string]nodeHealth) {
	if !s.cfg.Edge {
		return
	}
	summary := make(map[string]store.NodeHealthSummary, len(current))
	for nodeID, h := range current {
		if h == (nodeHealth{}) {
			continue // nothing to report: absent reads the same, and the row is not rewritten on every connect or disconnect
		}
		summary[nodeID] = store.NodeHealthSummary{NoTraffic: h.noTraffic, Failed: h.failed, Total: h.total, DoctorFail: h.doctorFail}
	}
	data, err := json.Marshal(summary)
	if err != nil {
		s.log.Warn("health: encode node summary", "err", err)
		return
	}
	value := string(data)
	if s.nodeHealthStored && value == s.nodeHealthJSON {
		return
	}
	if err := s.st.SetSettingIfChanged(ctx, nodeHealthSummarySetting, value); err != nil {
		s.log.Warn("health: store node summary", "err", err)
		return
	}
	s.nodeHealthJSON, s.nodeHealthStored = value, true
}

// AlertCounts is the badge: active alerts that are not muted and not INFO, and the critical ones among them. Alerts
// about people (ACCESS_ENDED, USER_CONNECTION, TORRENT, DEVICE_LIMIT) are not counted.
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
