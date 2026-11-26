package access

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// ControlRelay applies the mode controller (store.EvaluateRelayMode, a pure function) to one relay. The fleet calls it after the
// agents' bridge reports, a relay check and a change of an agent's capabilities; health calls it after every client-eye round.
// It reads the relay, decides, and writes only what changed.
func (s *Service) ControlRelay(ctx context.Context, id string) {
	s.controlRelay(ctx, id, true, true)
}

// controlRelay decides for one relay. notify says whether to tell the nodes at once (a caller that is about to do it, or that holds
// a lock, passes false); settle says whether a switch to the bridge is followed by the proof that people get through it (the
// switch-on that waits for the entry itself does that proof, and must not have a second one run beside it).
func (s *Service) controlRelay(ctx context.Context, id string, notify, settle bool) {
	if _, busy := s.controlling.LoadOrStore(id, struct{}{}); busy {
		return
	}
	defer s.controlling.Delete(id)
	for pass := 0; pass < 2; pass++ {
		l, err := s.st.RelayLink(ctx, id)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
				s.log.Warn("read relay for mode control", "code", "relay_mode_read_failed", "relay", id, "err", err)
			}
			return
		}
		if !l.Enabled || !l.Live() {
			return
		}
		entry, err := s.st.Node(ctx, l.ViaNodeID)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
				s.log.Warn("read entry for mode control", "code", "relay_mode_read_failed", "relay", id, "err", err)
			}
			return
		}
		capable := hasReverse(l.ViaNodeCaps) && hasReverse(l.ExitNodeCaps)
		if capable && l.BridgeServed() && entry.BridgePath == "" {
			// The bridge site of an entry is raised before anything needs it (the first relay that may use the bridge asks for it).
			if entry, err = s.ensureBridgeEndpoint(ctx, l.ViaNodeID, nil); err != nil {
				s.log.Warn("choose the bridge endpoint of an entry", "code", "bridge_endpoint_failed", "relay", id, "err", err)
				return
			}
		}
		again := s.controlRelayOnce(ctx, l, entry, notify, settle)
		if !again {
			return
		}
	}
}

// controlRelayOnce makes one decision; it returns true when the controller asked for the 8 MB proof of the direct way and it ran,
// so that the caller decides again with its result.
func (s *Service) controlRelayOnce(ctx context.Context, l store.RelayLink, entry store.NodeRow, notify, settle bool) bool {
	now := s.now()
	l = s.relayViewOf(ctx, l)
	if l.Leg == store.RelayLegAuto {
		// A family that dies moves the relay to the other one by itself, and back when that one dies: the check the health round
		// just stored is the evidence. Which stage counts depends on the way the relay stands in.
		if c, ok := l.Check(); ok {
			if relayViaReverse(l) {
				if c.ReverseReach.Status == "fail" && s.tryOtherLeg(ctx, l, true) {
					return true
				}
			} else if c.Leg.Status == "fail" && s.tryOtherLeg(ctx, l, false) {
				return true
			}
		}
	}
	c, _ := l.Check()
	port := entry.BridgeEndpointPort()
	reach := c.ReverseReach
	if !c.ReverseReachOK(now, port, l.ActiveFamily()) && reach.Status == "ok" {
		reach = store.RelayStage{} // passed, but for another endpoint or too long ago: not evidence
	}
	in := store.RelayModeInput{
		Mode: l.Mode, ActiveMode: l.ActiveMode, ModeReason: l.ModeReason, Network: l.Network,
		EntrySupported: hasReverse(l.ViaNodeCaps), ExitSupported: hasReverse(l.ExitNodeCaps),
		Now: now, ModeChangedAt: l.ModeChangedAt, Leg: c.Leg, ReverseReach: reach,
		Bridge: l.BridgeStage(now), Path: c.Path, DirectSpeed: c.DirectSpeed,
		DirectStreak: l.DirectStreak(), DirectStreakAt: l.DirectStreakAt(),
	}
	if c.Monitor != nil {
		in.MonitorFailStreak, in.MonitorStage = c.Monitor.FailStreak, c.Monitor.Stage
		if !c.Monitor.OK {
			in.MonitorFailAt = c.Monitor.AtUnix
		}
	}
	// The exit dials the entry only while the panel wants the bridge (BridgeWanted), and that comes from the stages this run just read
	// (a failed leg or a frozen flow, with the exit reaching the entry), which nothing else turns into desired state: the nodes are
	// told when it changes, or the exit would never hold the bridge the controller then waits for.
	wanted := l.BridgeWanted(now, port)
	previous, loaded := s.bridgeWanted.Swap(l.ID, wanted)
	wantChanged := loaded && previous.(bool) != wanted || !loaded && wanted
	decision := store.EvaluateRelayMode(in)
	// a change of the way tells the nodes by itself (applyRelayMode)
	if wantChanged && notify && decision.ActiveMode == l.ActiveMode && decision.ModeReason == l.ModeReason {
		s.notify.StateChanged()
	}
	if decision.DirectStreak != l.DirectStreak() || decision.DirectStreakAt != l.DirectStreakAt() {
		if err := s.st.SetRelayDirectState(ctx, l, decision.DirectStreak, decision.DirectStreakAt); err != nil {
			if errors.Is(err, store.ErrAccessVersion) {
				return false
			}
			s.log.Warn("store relay direct streak", "code", "relay_direct_streak_failed", "relay", l.ID, "err", err)
			return false
		}
	}
	if decision.ActiveMode != l.ActiveMode || decision.ModeReason != l.ModeReason {
		s.applyRelayMode(ctx, l, decision.ActiveMode, decision.ModeReason, notify, settle)
		return false
	}
	if decision.NeedDirectSpeed && s.cfg.CheckRelay != nil {
		// The direct leg held three checks an hour apart; the panel now asks for the one proof the leg cannot give, an 8 MB flow
		// of the direct way, inside the daily limits of speed tests. It runs here, for the panel, never by the owner's click.
		proofCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directSpeedBudget)
		_, err := s.cfg.CheckRelay(proofCtx, l.ID, store.RelayStageDirectSpeed)
		cancel()
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
				s.log.Warn("direct speed proof of a reverse relay", "code", "relay_direct_speed_failed", "relay", l.ID, "err", err)
			}
			return false
		}
		return true
	}
	return false
}

// directSpeedBudget bounds the 8 MB flow: speedRunWait (40 s) with the time to arm and stop the listener.
const directSpeedBudget = 90 * time.Second

// applyRelayMode stores a decision of the controller: the way, the reason, the event, the entry's and the exit's new desired
// state, and, when the relay moves to the bridge, the proof that people get through it.
func (s *Service) applyRelayMode(ctx context.Context, l store.RelayLink, active, reason string, notify, settle bool) {
	changed := active != l.ActiveMode
	if err := s.st.SetRelayActiveMode(ctx, l.ID, active, reason, s.now()); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("store relay active mode", "code", "relay_mode_store_failed", "relay", l.ID, "err", err)
		}
		return
	}
	if !changed {
		return
	}
	if notify {
		s.notify.StateChanged()
	}
	if err := s.st.InsertEvent(ctx, store.EventRow{Time: s.now(), Severity: 1, Code: "relay_mode_changed", Source: "panel", NodeID: l.ViaNodeID,
		Params: map[string]string{"relay": l.ID, "from": l.ActiveMode, "to": active, "reason": reason}}); err != nil {
		s.log.Warn("record relay mode change", "code", "relay_mode_event_failed", "relay", l.ID, "err", err)
	}
	if settle && active == store.RelayModeReverse && reason != "owner" {
		s.afterResponse(func() { s.settleReverseSwitch(context.Background(), l.ID) })
	}
}

// settleReverseSwitch is the proof after the controller moved a relay to the bridge: the entry installs the listener, and people
// from a third node get through to the exit's server. Unlike the first switch-on (relaySettle), a failure does not switch the
// relay off: the relay worked in the way it was in, so it goes back to it, with the reason bridge_failed, and the controller
// leaves it there for 30 minutes unless the direct leg itself fails.
func (s *Service) settleReverseSwitch(ctx context.Context, id string) {
	work, cancel := context.WithTimeout(ctx, relayApplyWait+2*time.Minute)
	defer cancel()
	l, err := s.st.RelayLink(work, id)
	if err != nil || !l.Enabled || !l.Reverse() {
		return
	}
	code := s.awaitRelayForward(work, id)
	if code == "" {
		code = s.relayPathCheckStrict(work, id, true)
	}
	if code == "" {
		return
	}
	s.recordRelayPathFailure(work, id, code)
	if current, err := s.st.RelayLink(work, id); err == nil && current.Enabled && current.Reverse() {
		s.log.Warn("a reverse relay does not carry people, back to direct", "code", "relay_reverse_switch_failed", "relay", id, "stage", store.RelayStagePath, "failure", code)
		s.applyRelayMode(work, current, store.RelayModeDNAT, "bridge_failed", true, false)
	}
}

func hasReverse(caps []string) bool { return slices.Contains(caps, agentcap.RelayReverse) }
