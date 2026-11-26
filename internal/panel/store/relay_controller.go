package store

import "time"

const (
	RelayStageBridge      = "bridge"
	RelayStageDirectSpeed = "direct_speed"

	relayModeCheckFresh     = 15 * time.Minute // leg, reverse_reach, path and the speed proof: older than this decides nothing
	relayBridgeReportFresh  = 90 * time.Second // the agents report every few seconds; a bridge nobody reported on is not up
	relayBridgeStable       = 10 * time.Second // a connection younger than this may be a retry loop
	relayModeHold           = 30 * time.Minute // the least a relay stays in reverse before the panel tries to leave it, and after a bridge failure before it tries again
	relayDirectCheckSpacing = 30 * time.Minute // one direct leg sample counts toward the streak per this
	relayDirectReturnStreak = uint32(3)        // samples in a row (so at least an hour) before the speed proof
	relayDirectSpeedMB      = 8                // the proof is exactly one flow of this size
	relaySpeedRetryBusy     = 30 * time.Minute // a proof that did not start (limit, busy node) is asked again after this
	relaySpeedRetryFailed   = 6 * time.Hour    // a proof that ran and failed is a finding: the daily limit is for the owner too
	relayFailedRounds       = uint32(2)        // failed client-eye rounds in a row that tell a way is down
	relayFastReturnRounds   = uint32(1)        // ... after the panel has just gone back to direct: one is enough
)

// RelayModeInput is the snapshot the relay mode controller is allowed to consider. It has no store or clock dependency.
type RelayModeInput struct {
	Mode, ActiveMode, ModeReason  string
	Network                       string
	EntrySupported, ExitSupported bool
	Now, ModeChangedAt            time.Time
	Leg, ReverseReach, Bridge     RelayStage
	Path, DirectSpeed             RelayStage
	DirectStreak                  uint32
	DirectStreakAt                int64
	// The last client-eye round through the entry's port: when it failed, how many failed in a row, and the stage it
	// pointed at ("freeze" when a flow stalled after the first kilobytes while the packets of the leg were fine).
	MonitorFailAt     int64
	MonitorFailStreak uint32
	MonitorStage      string
}

// RelayModeDecision is the controller's proposed state. Notify is set only when desired state changes.
type RelayModeDecision struct {
	ActiveMode, ModeReason string
	DirectStreak           uint32
	DirectStreakAt         int64
	Notify                 bool
	// NeedDirectSpeed asks the caller to run the 8 MB proof of the direct way (entry to exit; CheckRelay stage direct_speed) and
	// ask again with its result. The controller itself never runs anything.
	NeedDirectSpeed bool
}

// EvaluateRelayMode implements the mode rules of the reverse-relay plan (§7). A check decides only while its own timestamp is
// fresh and not in the future; reverse reach is never borrowed from another stage, and nothing moves a relay without evidence of
// its own (the one exception, an owner's word, is also bounded by a working bridge).
//
// Two things keep it from chattering. Leaving reverse for direct takes three direct-leg samples at least 30 minutes apart, a
// 30 minute stay, and an 8 MB flow of the direct way that the panel runs itself (the leg is only packets: it passes a way that
// freezes after 16 KB); a failed sample or a failed flow starts the count again. And going to direct because the bridge broke
// (the leg alive) needs two failed client rounds, so one reconnect of the bridge does not do it, and then forbids a freeze-only
// reason to come back to reverse for the same 30 minutes.
func EvaluateRelayMode(in RelayModeInput) RelayModeDecision {
	out := RelayModeDecision{ActiveMode: in.ActiveMode, ModeReason: in.ModeReason,
		DirectStreak: min(in.DirectStreak, relayDirectReturnStreak), DirectStreakAt: in.DirectStreakAt}
	if out.ActiveMode == "" {
		out.ActiveMode = RelayModeDNAT
	}
	set := func(active, reason string) {
		if out.ActiveMode != active {
			out.DirectStreak, out.DirectStreakAt = 0, 0
			out.ActiveMode = active
			out.Notify = true
		}
		out.ModeReason = reason
	}
	reset := func() { out.DirectStreak, out.DirectStreakAt = 0, 0 }

	if in.Mode == RelayModeDNAT {
		set(RelayModeDNAT, "owner")
		reset()
		return out
	}
	if in.Network != "tcp" {
		set(RelayModeDNAT, "udp")
		reset()
		return out
	}
	capable := in.EntrySupported && in.ExitSupported
	if !capable && (out.ActiveMode == RelayModeReverse || in.Mode == RelayModeReverse) {
		set(RelayModeDNAT, "reverse_unsupported")
		reset()
		return out
	}
	bridgeUp := freshStage(in.Bridge, in.Now, relayBridgeReportFresh) && in.Bridge.Status == "ok"
	if in.Mode == RelayModeReverse {
		if bridgeUp {
			set(RelayModeReverse, "owner")
		}
		return out
	}
	if in.Mode != RelayModeAuto {
		return out
	}

	legOK := freshStage(in.Leg, in.Now, relayModeCheckFresh) && in.Leg.Status == "ok"
	legFailed := freshStage(in.Leg, in.Now, relayModeCheckFresh) && in.Leg.Status == "fail"
	monitorFailed := in.MonitorFailAt > 0 && freshStage(RelayStage{Status: "fail", At: in.MonitorFailAt}, in.Now, relayModeCheckFresh)
	reachOK := capable && freshStage(in.ReverseReach, in.Now, relayModeCheckFresh) && in.ReverseReach.Status == "ok"
	// the endpoint the probe was made for is the caller's to check (it clears a probe of an endpoint that moved)

	if out.ActiveMode == RelayModeDNAT {
		rounds, justLeft := relayFailedRounds, in.ModeReason == "direct_back" && in.Now.Sub(in.ModeChangedAt) < relayModeHold
		if justLeft {
			rounds = relayFastReturnRounds // we just left the bridge on a proof; a failed round right after says the proof was wrong
		}
		wrong := legFailed || in.MonitorStage == "freeze" || justLeft
		cooling := in.ModeReason == "bridge_failed" && !legFailed && in.Now.Sub(in.ModeChangedAt) < relayModeHold
		if monitorFailed && in.MonitorFailStreak >= rounds && wrong && !cooling && reachOK && bridgeUp {
			set(RelayModeReverse, "leg_failed")
		}
		return out
	}

	// reverse
	switch {
	case freshStage(in.Leg, in.Now, relayModeCheckFresh) && in.Leg.Status == "fail":
		reset()
	case legOK && in.Leg.At > out.DirectStreakAt &&
		(out.DirectStreak == 0 || in.Leg.At-out.DirectStreakAt >= int64(relayDirectCheckSpacing/time.Second)):
		out.DirectStreak = min(out.DirectStreak+1, relayDirectReturnStreak)
		out.DirectStreakAt = in.Leg.At
	}
	if freshStage(in.DirectSpeed, in.Now, relayModeCheckFresh) && in.DirectSpeed.Status == "fail" && in.DirectSpeed.At >= out.DirectStreakAt {
		reset() // the packets pass and the flow does not: the direct way is still cut
	}
	bridgeFailed := freshStage(in.Bridge, in.Now, relayBridgeReportFresh) && in.Bridge.Status == "fail"
	pathFailed := freshStage(in.Path, in.Now, relayModeCheckFresh) && in.Path.Status == "fail"
	if legOK && (bridgeFailed || pathFailed) && monitorFailed && in.MonitorFailStreak >= relayFailedRounds {
		set(RelayModeDNAT, "bridge_failed")
		return out
	}
	held := !in.ModeChangedAt.IsZero() && in.ModeChangedAt.Unix() > 0 && in.Now.Sub(in.ModeChangedAt) >= relayModeHold
	if out.DirectStreak < relayDirectReturnStreak || !held {
		return out
	}
	if freshStage(in.DirectSpeed, in.Now, relayModeCheckFresh) && in.DirectSpeed.Status == "ok" && in.DirectSpeed.MB == relayDirectSpeedMB {
		set(RelayModeDNAT, "direct_back")
		return out
	}
	// no proof yet: ask for one, but not again for a while after one that did not start or failed
	last := in.DirectSpeed
	wait := time.Duration(0)
	switch last.Status {
	case "fail":
		wait = relaySpeedRetryFailed
	case "skipped":
		wait = relaySpeedRetryBusy
	}
	out.NeedDirectSpeed = last.At <= 0 || last.At > in.Now.Unix() || in.Now.Sub(time.Unix(last.At, 0)) >= wait
	return out
}

func freshStage(stage RelayStage, now time.Time, maxAge time.Duration) bool {
	if stage.Status == "" || stage.At <= 0 || now.Unix() < stage.At {
		return false
	}
	return now.Sub(time.Unix(stage.At, 0)) <= maxAge
}

// ReverseReachOK says the exit reached the entry's bridge endpoint lately: a passing reverse_reach, not older than 15 minutes and
// not from the future, made for the endpoint the entry has NOW (port 0 on a stage is the 443 the first version probed).
func (c RelayCheck) ReverseReachOK(now time.Time, endpointPort uint16, family string) bool {
	port := c.ReverseReach.Port
	if port == 0 {
		port = 443
	}
	probed := c.ReverseReach.Family
	if probed == "" {
		probed = RelayFamily4 // a probe made before the leg had a family went to the entry's own IPv4 address
	}
	if family == "" {
		family = RelayFamily4
	}
	return c.ReverseReach.Status == "ok" && port == endpointPort && probed == family && freshStage(c.ReverseReach, now, relayModeCheckFresh)
}

// BridgeWanted says the panel wants the exit to hold its connections to the entry for this relay now. The reverse way needs them;
// so does an owner who asked for it; and an automatic relay needs them ready, so that the controller can move it in seconds, when
// the direct way fails (the leg, or a flow that froze) and the exit reaches the entry, and for 30 minutes after it went back to
// direct (a flow that froze again is then moved back at once). Nothing else holds a bridge "just in case". endpointPort is the
// port the entry's bridge endpoint has now (NodeRow.BridgeEndpointPort); capabilities are the caller's to check.
func (l RelayLink) BridgeWanted(now time.Time, endpointPort uint16) bool {
	if l.Network != "tcp" {
		return false
	}
	if l.Reverse() || l.Mode == RelayModeReverse {
		return true
	}
	if l.Mode != RelayModeAuto {
		return false
	}
	if l.ModeReason == "direct_back" && !l.ModeChangedAt.IsZero() && now.Sub(l.ModeChangedAt) < relayModeHold {
		return true
	}
	c, ok := l.Check()
	if !ok || !c.ReverseReachOK(now, endpointPort, l.ActiveFamily()) {
		return false
	}
	legFailed := freshStage(c.Leg, now, relayModeCheckFresh) && c.Leg.Status == "fail"
	frozen := c.Monitor != nil && !c.Monitor.OK && c.Monitor.Stage == "freeze" &&
		freshStage(RelayStage{Status: "fail", At: c.Monitor.AtUnix}, now, relayModeCheckFresh)
	return legFailed || frozen
}

// BridgeServed says the entry should run the bridge site for this relay: the relay may use the bridge at all (a TCP relay that
// is not the owner's DNAT-only choice), switched on or not, so that the site is up, and its pin known, before the first check
// that needs it and before the first time the exit has to dial. Capabilities are the caller's to check.
func (l RelayLink) BridgeServed() bool { return l.Network == "tcp" && l.Mode != RelayModeDNAT }
