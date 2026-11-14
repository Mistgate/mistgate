package access

import (
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// exitReachHintAge is how long the finding of a reverse_reach stage is still shown as a hint on a candidate.
const exitReachHintAge = 24 * time.Hour

// requestedRelayStages names the stages CheckRelay runs for a request: the direct ones always, and the reverse ones the owner asked
// for (reverse_reach also runs by itself after a failed leg; reverse_speed never does). The speed proof implies reverse_reach.
func requestedRelayStages(m *adminv1.CheckRelayRequest) []string {
	if !m.ReverseReach && !m.ReverseSpeed {
		return nil // none = leg and path
	}
	stages := []string{store.RelayStageLeg, store.RelayStagePath, store.RelayStageReverseReach}
	if m.ReverseSpeed {
		stages = append(stages, store.RelayStageReverseSpeed)
	}
	return stages
}

// exitReachHint is what the last reverse_reach stage of any relay of this entry through this exit found (the newest one in
// exitReachHintAge): the exit's TLS probe of the entry's own site on TCP 443. "no_data" when none ran, or the stage could not
// say (the exit's agent too old, no site on the entry, a refused connection: nothing listens there, the exit may still reach the
// host).
func exitReachHint(links []store.RelayLink, exitID string, now time.Time) *adminv1.RelayReachHint {
	var newest store.RelayStage
	for _, l := range links {
		c, ok := l.Check()
		if !ok || l.ExitNodeID != exitID || c.ReverseReach.At <= newest.At {
			continue
		}
		newest = c.ReverseReach
	}
	hint := &adminv1.RelayReachHint{Verdict: "no_data"}
	if newest.At <= 0 || now.Sub(time.Unix(newest.At, 0)) > exitReachHintAge {
		return hint
	}
	switch {
	case newest.Status == "ok":
		hint.Verdict = "reach"
	case newest.Status == "fail" && newest.Code == "timeout": // the connection gets no answer
		hint.Verdict = "blocked"
	case newest.Status == "fail" && newest.Code == "other": // something answers on 443, but it is not the entry's site
		hint.Verdict = "partial"
	default:
		return hint
	}
	hint.Source, hint.AtUnix = "exit_probe", newest.At
	return hint
}
