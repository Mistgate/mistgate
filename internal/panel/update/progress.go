package update

import (
	"context"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

type progressExpectation struct {
	RequestID  string
	SentAtUnix int64
}

var progressPhases = map[string]bool{
	"accepted":    true,
	"downloading": true,
	"verifying":   true,
	"installing":  true,
	"restarting":  true,
	"completed":   true,
	"failed":      true,
}

// A progress frame is best effort and can cross a reconnect. Keep the phase monotonic even when two frames have the
// same second-level timestamp, which is common for accepted/failed frames emitted back to back.
var progressPhaseOrder = map[string]int{
	"accepted":    0,
	"downloading": 1,
	"verifying":   2,
	"installing":  3,
	"restarting":  4,
	"completed":   5,
	"failed":      5,
}

// RecordProgress is deliberately in memory: progress is advisory and high-frequency writes must not compete with the
// rollout's durable state. It is accepted only for a currently SENT step and only for that step's request ID, so a
// delayed frame cannot repaint a later update. A reconnect or a fresh rollout naturally replaces stale values.
func (s *Service) RecordProgress(_ context.Context, nodeID string, p *agentv1.UpdateProgress) error {
	if p == nil || nodeID == "" || p.RequestId == "" {
		return nil
	}
	if !progressPhases[p.Phase] || (p.BytesTotal > 0 && p.BytesDownloaded > p.BytesTotal) {
		return nil
	}
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	expected, ok := s.progressActive[nodeID]
	if !ok || (expected.RequestID != "" && expected.RequestID != p.RequestId) ||
		(expected.SentAtUnix > 0 && p.UpdatedUnix > 0 && p.UpdatedUnix < expected.SentAtUnix) {
		return nil
	}
	if previous, ok := s.progress[nodeID]; ok {
		if p.UpdatedUnix > 0 && previous.UpdatedUnix > 0 && p.UpdatedUnix < previous.UpdatedUnix {
			return nil
		}
		if progressPhaseOrder[p.Phase] < progressPhaseOrder[previous.Phase] ||
			(previous.Phase == "completed" || previous.Phase == "failed") && p.Phase != previous.Phase {
			return nil
		}
		downloaded, total := p.BytesDownloaded, p.BytesTotal
		terminal := p.Phase == "completed" || p.Phase == "failed"
		if downloaded < previous.Downloaded {
			if !terminal {
				return nil
			}
			// Failure/completion frames do not always carry the last counter. Preserve the last known value rather than
			// making a terminal frame look like a fresh zero-byte download.
			downloaded = previous.Downloaded
		}
		// Once a total is known, dropping it back to zero or changing it would make the UI percentage regress or
		// allow a delayed frame from a different response to repaint the current download.
		if previous.Total > 0 {
			if total == 0 && terminal {
				total = previous.Total
			} else if total == 0 || total != previous.Total {
				return nil
			}
		}
		if total > 0 && downloaded > total {
			return nil
		}
		p.BytesDownloaded, p.BytesTotal = downloaded, total
	}
	s.progress[nodeID] = progressState{RequestID: p.RequestId, Phase: p.Phase, Downloaded: p.BytesDownloaded, Total: p.BytesTotal, Speed: p.BytesPerSecond, UpdatedUnix: p.UpdatedUnix}
	return nil
}

func (s *Service) beginProgress(nodeID, requestID string, sentAt time.Time) {
	if nodeID == "" {
		return
	}
	s.progressMu.Lock()
	s.progressActive[nodeID] = progressExpectation{RequestID: requestID, SentAtUnix: sentAt.Unix()}
	delete(s.progress, nodeID)
	s.progressMu.Unlock()
}

func (s *Service) clearProgress(nodeID string) {
	s.progressMu.Lock()
	delete(s.progress, nodeID)
	delete(s.progressActive, nodeID)
	s.progressMu.Unlock()
}
