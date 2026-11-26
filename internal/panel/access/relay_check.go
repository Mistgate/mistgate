package access

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The checks of a relay (relay.proto CheckRelay) and the rule that lets one be switched on. The probes are the fleet module's
// (fleet.CheckRelay, handed in as Config.CheckRelay); here are the rule, the wait for the entry's forward and the candidate list.
//
// A relay is switched on in two steps, and no step is skipped:
//
//  1. The gate (relayCheckGate): the leg check of CheckRelay passed or was marked unverifiable, less than relayCheckTTL ago,
//     for the relay's present target. A relay that is off has no forward, so the leg is all that can be checked before.
//  2. relaySettle, once the switch is stored and the entry has been told: wait for the entry to report the forward, then check
//     the path from outside. A failure switches it off; an unverifiable path stays on but is not marked verified.

// relayCheckTTL is how fresh a passing leg check must be to switch a relay on. The owner reads the result and decides, which
// takes minutes, and the leg (a block of the exit by the entry's network, the exit's firewall) does not change in that time
// often enough to matter; an hour-old check says nothing about now. The path is proved again right after the switch anyway.
const relayCheckTTL = 15 * time.Minute

// relayApplyWait is how long switching on waits for the entry to report its forward (the desired state goes out at once and
// the agent installs the table in a second or two; an entry that is off line never answers).
const relayApplyWait = 30 * time.Second

// relayTo is the target the forward points at: the exit's address for the relay's active family and its server's port (the
// public port when the inbound has one), the text a check is made for.
func (s *Service) relayTo(ctx context.Context, l store.RelayLink) string {
	to, _ := s.relayTarget(ctx, l)
	return to
}

func (s *Service) relayTarget(ctx context.Context, l store.RelayLink) (string, string) {
	f, merged, err := s.loadFull(ctx, l.InboundID)
	if err != nil {
		return "", ""
	}
	spec, err := s.buildSpec(f, merged)
	if err != nil {
		return "", ""
	}
	host := relayExitHost(l, f.Node)
	if host == "" {
		return "", ""
	}
	return relayEndpoint(l, f, host, spec.Listen.Port)
}

// relayExitHost is the exit's address for the relay's active family: the node's own address for IPv4, and for IPv6 its IPv6 entry
// when it is on, else the first stable address the doctor named for its uplink. "" when the IPv6 leg has no address to point at.
func relayExitHost(l store.RelayLink, n store.AccessNode) string {
	if l.ActiveFamily() != store.RelayFamily6 {
		return n.Address
	}
	host, _, ok := store.RelayHostFor(l.Relay, n.Address, n.IPv6Entry, n.IPv6)
	if !ok {
		return ""
	}
	return host
}

func relayEndpoint(l store.RelayLink, f store.AccessInboundFull, host string, port uint16) (string, string) {
	return l.Target(f.RelayInbound(), host, port)
}

// relayView is the link seen for its exit inbound's current target (store.RelayLink.ForInbound).
func relayView(l store.RelayLink, f store.AccessInboundFull, host string, port uint16) store.RelayLink {
	v, _ := l.ForInbound(f.RelayInbound(), host, port)
	return v
}

// relayViewOf is relayView for a link whose exit inbound is read here; one whose target cannot be built has no evidence at all.
func (s *Service) relayViewOf(ctx context.Context, l store.RelayLink) store.RelayLink {
	f, merged, err := s.loadFull(ctx, l.InboundID)
	if err != nil {
		return l.ForTarget("", "")
	}
	spec, err := s.buildSpec(f, merged)
	if err != nil {
		return l.ForTarget("", "")
	}
	host := relayExitHost(l, f.Node)
	if host == "" {
		return l.ForTarget("", "")
	}
	return relayView(l, f, host, spec.Listen.Port)
}

// The leg ends at the exit; moving the entry's listen port invalidates the path but not the leg.
func (s *Service) relayLegMatches(ctx context.Context, l store.RelayLink, c store.RelayCheck) bool {
	return c.Network == l.Network && c.To != "" && c.To == s.relayTo(ctx, l)
}

// relayCheckGate lets a relay be switched on: its latest check proved the leg or found it unverifiable, recently, for this target.
// (The leg does not depend on the entry's port; a port moved after the check is covered by the path check that follows the switch.)
func (s *Service) relayCheckGate(ctx context.Context, l store.RelayLink) error {
	c, ok := l.Check()
	if ok && l.Leg == store.RelayLegAuto && c.Leg.Status == "fail" && s.tryOtherLeg(ctx, l) {
		// The other family proved the leg: the relay is now checked for the target it would really forward to.
		if next, err := s.st.RelayLink(ctx, l.ID); err == nil {
			l, c, ok = next, mustRelayCheck(next), true
		}
	}
	if !ok || s.cfg.CheckRelay == nil || c.Leg.Status == "" {
		return coded(connect.CodeFailedPrecondition, "relay_unchecked")
	}
	age := s.now().Sub(time.Unix(c.Leg.At, 0))
	if age > relayCheckTTL || age < -time.Minute || !s.relayLegMatches(ctx, l, c) {
		return coded(connect.CodeFailedPrecondition, "relay_check_stale")
	}
	if !c.Leg.Acceptable() {
		return coded(connect.CodeFailedPrecondition, "relay_check_failed", "stage", store.RelayStageLeg, "code", c.Leg.Code)
	}
	return nil
}

// relayLegRetry is how soon the panel tries the other address family again after a move: one probe per half hour, not one per
// check, and the leg of a dead family stays dead for much longer than that.
const relayLegRetry = 30 * time.Minute

// mustRelayCheck is the stored check of a relay reloaded after a move of the leg family; no check means an empty one, and the
// gate refuses it as unchecked.
func mustRelayCheck(l store.RelayLink) store.RelayCheck {
	c, _ := l.Check()
	return c
}

// tryOtherLeg moves an auto relay to the other address family when the family in use cannot reach the exit, and keeps the move
// only when the leg check of the other family passes. Explicit families never move. It returns true when the family in use
// changed; the caller then reads the relay and its check again.
func (s *Service) tryOtherLeg(ctx context.Context, l store.RelayLink) bool {
	if l.Leg != store.RelayLegAuto || s.cfg.CheckRelay == nil {
		return false
	}
	from := l.ActiveFamily()
	to := store.RelayFamily6
	if from == store.RelayFamily6 {
		to = store.RelayFamily4
	}
	if to == store.RelayFamily6 {
		if !slices.Contains(l.ViaNodeCaps, agentcap.RelayForwardV6) {
			return false
		}
		if _, ok := s.relayExitIPv6(ctx, l); !ok {
			return false // no address to try: not a dead leg, a missing one, and the check says so
		}
	}
	now := s.now()
	if !l.LegTriedAt.IsZero() && now.Sub(l.LegTriedAt) < relayLegRetry {
		return false
	}
	before, beforeOK := l.Check()
	if err := s.st.SetRelayActiveLeg(ctx, l.ID, to, now, now); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("access: move the relay leg", "code", "relay_leg_store_failed", "relay", l.ID, "err", err)
		}
		return false
	}
	s.notify.StateChanged()
	c, err := s.cfg.CheckRelay(ctx, l.ID, store.RelayStageLeg)
	if err == nil && c.Leg.Status == "ok" {
		s.log.Info("relay leg moved to the other family", "code", "relay_leg_changed", "relay", l.ID, "from", from, "to", to)
		if err := s.st.InsertEvent(ctx, store.EventRow{Time: now, Severity: 1, Code: "relay_leg_changed", Source: "panel", NodeID: l.ViaNodeID,
			Params: map[string]string{"relay": l.ID, "from": from, "to": to}}); err != nil {
			s.log.Warn("record relay leg change", "code", "relay_leg_event_failed", "relay", l.ID, "err", err)
		}
		return true
	}
	// The other family does not work either: put the family back, stamp the attempt so the next one waits, and leave the relay
	// with the failure of the family it had.
	if err := s.st.SetRelayActiveLeg(ctx, l.ID, from, s.now(), s.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("access: put the relay leg back", "code", "relay_leg_restore_failed", "relay", l.ID, "err", err)
	}
	if beforeOK {
		// The check of the tried family must not stand as the check of the family in use: the gate compares its target with the
		// forward the relay would install, and the two would never match.
		if err := s.st.SetRelayCheck(ctx, l.ID, before); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("access: put the relay check back", "code", "relay_leg_check_restore_failed", "relay", l.ID, "err", err)
		}
	} else {
		// The first check of this relay: there is no earlier result to put back, so ask the family in use once more. Its failure
		// is what the owner must see, and the gate must compare the target the relay would really forward to.
		_, _ = s.cfg.CheckRelay(ctx, l.ID, store.RelayStageLeg)
	}
	s.notify.StateChanged()
	return false
}

// relayExitIPv6 is the exit's address for an IPv6 leg, when it has one.
func (s *Service) relayExitIPv6(ctx context.Context, l store.RelayLink) (string, bool) {
	exit, err := s.st.Access().Node(ctx, l.ExitNodeID)
	if err != nil {
		return "", false
	}
	d, err := s.st.IPv6Doctor(ctx, l.ExitNodeID)
	if err != nil {
		d = store.IPv6Doctor{State: store.IPv6Unknown}
	}
	host, _, ok := store.RelayHostFor(store.Relay{ActiveLeg: store.RelayFamily6}, exit.Address, exit.IPv6Entry, d)
	return host, ok
}

// relayModeGate is the rule for switching a relay on in the mode asked for (relay.proto CheckRelay and the contract of the way):
//
//   - dnat: as always, the leg check of the last 15 minutes passed or could not be made.
//   - reverse: both agents list "relay-reverse/1", the server is TCP, and a reverse_reach check, made now for the entry's bridge
//     endpoint as it is now, passed. The leg is not asked for: the way exists because the direct leg may not work.
//   - auto: the dnat gate when it passes. When it does not and the reverse way is possible, this runs the check itself (leg, and
//     reverse_reach after a failed leg): a passing leg switches the relay on direct, a failed leg and a passing reverse_reach
//     switch it on through the bridge. Otherwise the refusal is that of the leg, with the reverse_reach code beside it. A relay
//     that cannot go through the bridge (UDP, an agent that is too old) is gated exactly as before the reverse way existed.
func (s *Service) relayModeGate(ctx context.Context, l store.RelayLink, mode string) error {
	switch mode {
	case store.RelayModeDNAT:
		return s.relayGate(ctx, l)
	case store.RelayModeReverse:
		return s.reverseRelayGate(ctx, l, mode)
	case store.RelayModeAuto:
		direct := s.relayGate(ctx, l)
		if direct == nil || reverseBlocked(l) != "" || s.cfg.CheckRelay == nil {
			return direct
		}
		_, restore, err := s.prepareBridgeSite(ctx, l, mode)
		if err != nil {
			return direct // the bridge cannot be probed: the answer is the direct gate's
		}
		check, err := s.cfg.CheckRelay(ctx, l.ID)
		if err != nil {
			restore()
			return direct
		}
		entry, err := s.st.Node(ctx, l.ViaNodeID)
		if err != nil {
			restore()
			return s.internal("get entry", err)
		}
		switch {
		case check.Leg.Acceptable():
			restore()
			return nil
		case check.Leg.Status == "fail" && check.ReverseReachOK(s.now(), entry.BridgeEndpointPort()):
			return nil
		}
		restore()
		if check.Leg.Status == "" {
			return direct
		}
		return coded(connect.CodeFailedPrecondition, "relay_check_failed", "stage", store.RelayStageLeg, "code", check.Leg.Code,
			"reverse_reach", cmp.Or(check.ReverseReach.Code, check.ReverseReach.Status, "unknown"))
	default:
		return invalid("mode must be auto, dnat or reverse")
	}
}

func (s *Service) reverseRelayGate(ctx context.Context, l store.RelayLink, mode string) error {
	if reason := reverseBlocked(l); reason != "" {
		return coded(connect.CodeFailedPrecondition, "reverse_unavailable", "reason", reason)
	}
	if s.cfg.CheckRelay == nil {
		return coded(connect.CodeFailedPrecondition, "reverse_unavailable", "reason", "reverse_reach_unknown")
	}
	entry, restore, err := s.prepareBridgeSite(ctx, l, mode)
	if err != nil {
		return coded(connect.CodeFailedPrecondition, "reverse_unavailable", "reason", "reverse_reach_unknown")
	}
	check, err := s.cfg.CheckRelay(ctx, l.ID, store.RelayStageReverseReach)
	if err != nil {
		restore()
		return coded(connect.CodeFailedPrecondition, "reverse_unavailable", "reason", "reverse_reach_failed")
	}
	if !check.ReverseReachOK(s.now(), entry.BridgeEndpointPort()) {
		restore()
		reason := "reverse_reach_unknown"
		switch {
		case check.ReverseReach.At > 0 && !freshRelayStage(check.ReverseReach, s.now(), relayCheckTTL):
			reason = "reverse_reach_stale"
		case check.ReverseReach.Code != "":
			reason = "reverse_reach_" + check.ReverseReach.Code
		}
		return coded(connect.CodeFailedPrecondition, "reverse_unavailable", "reason", reason)
	}
	return nil
}

// prepareBridgeSite makes the entry's bridge endpoint (its path and port, chosen once) and, when that is a site of the bridge's own
// (a port of its own, not the node's site on 443), has it served and its pin reported before the check that probes it. The entry
// serves the site for the relays that may use the bridge (store.RelayLink.BridgeServed); a relay that is still dnat is made auto or
// reverse first, and restore puts the mode back when the check refuses it.
func (s *Service) prepareBridgeSite(ctx context.Context, l store.RelayLink, mode string) (store.NodeRow, func(), error) {
	restore := func() {}
	entry, err := s.ensureBridgeEndpoint(ctx, l.ViaNodeID, nil)
	if err != nil {
		return entry, restore, err
	}
	if entry.BridgePort == 0 {
		return entry, restore, nil
	}
	if l.Mode == store.RelayModeDNAT && mode != store.RelayModeDNAT {
		next, orig := mode, l.Mode
		if err := s.st.UpdateRelay(ctx, l.ID, store.RelayPatch{Mode: &next}, s.now()); err != nil {
			return entry, restore, err
		}
		s.notify.StateChanged()
		restore = func() {
			if err := s.st.UpdateRelay(context.WithoutCancel(ctx), l.ID, store.RelayPatch{Mode: &orig}, s.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
				s.log.Warn("put the relay mode back", "code", "relay_mode_restore_failed", "relay", l.ID, "err", err)
			}
			s.notify.StateChanged()
		}
	}
	if err := s.awaitBridgeSite(ctx, l.ID, entry); err != nil {
		restore()
		return entry, func() {}, err
	}
	return entry, restore, nil
}

// awaitBridgeSite waits for the entry's agent to report the pin of its bridge site (a separate port; the node's site on 443 has
// none to wait for): the exits verify it by that pin, and so does the probe.
func (s *Service) awaitBridgeSite(ctx context.Context, relayID string, entry store.NodeRow) error {
	if entry.BridgePort == 0 {
		return nil
	}
	deadline := time.NewTimer(s.relayWait)
	defer deadline.Stop()
	tick := time.NewTicker(s.relayPoll)
	defer tick.Stop()
	for {
		l, err := s.st.RelayLink(context.WithoutCancel(ctx), relayID)
		if err != nil {
			return err
		}
		if l.BridgeSitePin(entry.BridgePort, s.now()) != "" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("bridge site pin timeout")
		case <-tick.C:
		}
	}
}

func freshRelayStage(stage store.RelayStage, now time.Time, ttl time.Duration) bool {
	return stage.At > 0 && stage.At <= now.Unix() && now.Unix()-stage.At <= int64(ttl/time.Second)
}

// relaySettle runs after a relay was switched on: the entry installs the forward, then the path is checked from outside. When
// the path fails the relay is switched off again; an unverifiable path leaves it on but unverified.
func (s *Service) relaySettle(ctx context.Context, l store.RelayLink) error {
	stage := store.RelayStagePath
	if s.relayNeedsBridge(ctx, l) {
		stage = store.RelayStageBridge
		if code := s.awaitRelayBridge(ctx, l.ID); code != "" {
			s.recordRelayBridgeFailure(ctx, l.ID, code)
			return s.disableFailedRelay(ctx, l, stage, code)
		}
		s.controlRelay(ctx, l.ID, true, false) // the proof of this switch-on is the one below
		current, err := s.st.RelayLink(context.WithoutCancel(ctx), l.ID)
		if err != nil {
			return s.internal("read relay after bridge", err)
		}
		if current.ActiveMode != store.RelayModeReverse && current.Mode == store.RelayModeAuto && s.relayNeedsBridge(ctx, current) {
			s.changeRelayMode(ctx, current, store.RelayModeReverse, "leg_failed")
			current, err = s.st.RelayLink(context.WithoutCancel(ctx), l.ID)
			if err != nil {
				return s.internal("read relay after mode change", err)
			}
		}
		if current.Mode == store.RelayModeReverse && current.ActiveMode != store.RelayModeReverse {
			return s.disableFailedRelay(ctx, current, store.RelayStageBridge, "mode_not_ready")
		}
		l = current
	}
	code := s.awaitRelayForward(ctx, l.ID)
	if code == "" {
		stage = store.RelayStagePath
		code = s.relayPathCheckStrict(ctx, l.ID, l.ActiveMode == store.RelayModeReverse)
	}
	if code != "" {
		if stage == store.RelayStagePath {
			s.recordRelayPathFailure(ctx, l.ID, code)
		} else {
			s.recordRelayBridgeFailure(ctx, l.ID, code)
		}
	}
	if code == "" {
		return nil
	}
	return s.disableFailedRelay(ctx, l, stage, code)
}

func (s *Service) disableFailedRelay(ctx context.Context, l store.RelayLink, stage, code string) error {
	bg := context.WithoutCancel(ctx)
	off := false
	s.relayMu.Lock()
	err := s.st.UpdateRelay(bg, l.ID, store.RelayPatch{Enabled: &off}, s.now())
	s.relayMu.Unlock()
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("access: switch a failed relay off", "code", "relay_disable_failed", "relay", l.ID, "err", err)
	}
	s.relayHome(bg, l.ID)
	s.audit(bg, actor(ctx), "relay.update", map[string]any{"relay": l.ID, "via": l.ViaNodeName, "exit": l.ExitNodeName,
		"enabled": false, "reason": "check_failed", "stage": stage, "code": code})
	s.notify.StateChanged()
	return coded(connect.CodeFailedPrecondition, "relay_check_failed", "stage", stage, "code", code)
}

// relayHome puts a relay that was switched off back in the plain way: a relay that is off has no way, and one that was on in the
// reverse way and is switched on again must start from direct, as a new one does, and not carry the way it was in last.
func (s *Service) relayHome(ctx context.Context, id string) {
	if l, err := s.st.RelayLink(ctx, id); err == nil && l.ActiveMode != store.RelayModeDNAT {
		if err := s.st.SetRelayActiveMode(ctx, id, store.RelayModeDNAT, "", s.now()); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.log.Warn("put a switched-off relay back in the plain way", "code", "relay_mode_store_failed", "relay", id, "err", err)
		}
	}
}

// relayNeedsBridge says switching this relay on goes through the bridge: the owner asked for it, it stands in it, or it is auto and
// the direct leg failed while the exit reaches the entry's bridge endpoint (both lately, as the gate saw them).
func (s *Service) relayNeedsBridge(ctx context.Context, l store.RelayLink) bool {
	if l.Network != "tcp" {
		return false
	}
	if l.ActiveMode == store.RelayModeReverse || l.Mode == store.RelayModeReverse {
		return true
	}
	check, ok := l.Check()
	if !ok || l.Mode != store.RelayModeAuto || !freshRelayStage(check.Leg, s.now(), relayCheckTTL) || check.Leg.Status != "fail" || !s.relayLegMatches(ctx, l, check) {
		return false
	}
	entry, err := s.st.Node(ctx, l.ViaNodeID)
	return err == nil && check.ReverseReachOK(s.now(), entry.BridgeEndpointPort())
}

// awaitRelayBridge waits for the exit's agent to report the bridge held for 10 seconds: "" when it does, else why not (an error
// the agent reported, or that nothing came in time).
func (s *Service) awaitRelayBridge(ctx context.Context, id string) string {
	deadline := time.NewTimer(s.relayWait)
	defer deadline.Stop()
	tick := time.NewTicker(s.relayPoll)
	defer tick.Stop()
	for {
		l, err := s.st.RelayLink(context.WithoutCancel(ctx), id)
		if err != nil {
			return "failed"
		}
		if !l.Enabled {
			return "switched_off"
		}
		stage := l.BridgeStage(s.now())
		switch {
		case stage.Status == "ok":
			return ""
		case stage.Status == "fail" && stage.Code != "disconnected" && stage.Code != "stale":
			return stage.Code
		}
		select {
		case <-ctx.Done():
			return "apply_timeout"
		case <-deadline.C:
			return "apply_timeout"
		case <-tick.C:
		}
	}
}

// recordRelayBridgeFailure keeps why the bridge did not come up as the exit's own report (an error word), where the bridge stage
// is read from; the relay is switched off after it, and its last check says so.
func (s *Service) recordRelayBridgeFailure(ctx context.Context, id, code string) {
	_ = s.st.SetRelayBridgeSide(context.WithoutCancel(ctx), id, "exit", &store.RelayBridgeSideCheck{Error: code, AtUnix: s.now().Unix()})
}

func (s *Service) changeRelayMode(ctx context.Context, l store.RelayLink, active, reason string) {
	if l.ActiveMode == active && l.ModeReason == reason {
		return
	}
	if err := s.st.SetRelayActiveMode(context.WithoutCancel(ctx), l.ID, active, reason, s.now()); err != nil {
		s.log.Warn("store relay active mode", "code", "relay_mode_store_failed", "relay", l.ID, "err", err)
		return
	}
	if l.ActiveMode != active {
		s.notify.StateChanged()
		if err := s.st.InsertEvent(context.WithoutCancel(ctx), store.EventRow{Time: s.now(), Severity: 1, Code: "relay_mode_changed", Source: "panel", NodeID: l.ViaNodeID,
			Params: map[string]string{"relay": l.ID, "from": l.ActiveMode, "to": active, "reason": reason}}); err != nil {
			s.log.Warn("record relay mode change", "code", "relay_mode_event_failed", "relay", l.ID, "err", err)
		}
	}
}

// awaitRelayForward waits for the entry's agent to report the forward: "" when it is active, else why not.
func (s *Service) awaitRelayForward(ctx context.Context, id string) string {
	deadline := time.NewTimer(s.relayWait)
	defer deadline.Stop()
	tick := time.NewTicker(s.relayPoll)
	defer tick.Stop()
	for {
		l, err := s.st.RelayLink(context.WithoutCancel(ctx), id)
		switch {
		case err != nil:
			return "failed"
		case !l.Enabled:
			return "switched_off"
		case l.State == "active":
			return ""
		case l.State == "failed":
			return "apply_failed"
		}
		select {
		case <-ctx.Done():
			return "apply_timeout"
		case <-deadline.C:
			return "apply_timeout"
		case <-tick.C:
		}
	}
}

// relayPathCheck runs the path stage through the fleet: "" when it passed or was unverifiable, else the failure code.
func (s *Service) relayPathCheck(ctx context.Context, id string) string {
	return s.relayPathCheckStrict(ctx, id, false)
}

func (s *Service) relayPathCheckStrict(ctx context.Context, id string, requireThirdNode bool) string {
	if s.cfg.CheckRelay == nil {
		return "failed"
	}
	c, err := s.cfg.CheckRelay(ctx, id, store.RelayStagePath)
	switch {
	case err != nil:
		s.log.Warn("access: path check of a relay", "code", "relay_path_check_failed", "relay", id, "err", err)
		return "failed"
	case c.Path.Acceptable() && (!requireThirdNode || c.Path.Status == "ok" && c.Path.Sender != "panel"):
		return ""
	case requireThirdNode && c.Path.Status == "ok" && c.Path.Sender == "panel":
		return "no_sender"
	case c.Path.Status != "ok":
		return cmp.Or(c.Path.Code, "failed")
	}
	return ""
}

// recordRelayPathFailure puts a path failure the fleet never saw (the forward was not installed) into the relay's check, next to
// the leg that allowed the switch.
func (s *Service) recordRelayPathFailure(ctx context.Context, id, code string) {
	bg := context.WithoutCancel(ctx)
	l, err := s.st.RelayLink(bg, id)
	if err != nil {
		return
	}
	c, ok := l.Check()
	if !ok {
		return
	}
	c.At = s.now().Unix()
	c.Path = store.RelayStage{Status: "fail", Code: code, At: c.At}
	c.Settle()
	if err := s.st.SetRelayCheck(bg, id, c); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("access: store a relay check", "code", "relay_check_store_failed", "relay", id, "err", err)
	}
}

// clearRelayPath forgets the path result of a relay about to be switched on or moved: that result described the forward as it was.
func (s *Service) clearRelayPath(ctx context.Context, id string) {
	l, err := s.st.RelayLink(ctx, id)
	if err != nil {
		return
	}
	c, ok := l.Check()
	if !ok || c.Path == (store.RelayStage{}) {
		return
	}
	c.Path = store.RelayStage{}
	c.Settle()
	if err := s.st.SetRelayCheck(ctx, id, c); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("access: store a relay check", "code", "relay_check_store_failed", "relay", id, "err", err)
	}
}

// SetRelayHints plugs in the source of the hints of ListRelayCandidates (health.Service.RelayHints). The health module is built
// after this one. Call it before the service takes requests.
func (s *Service) SetRelayHints(f func(ctx context.Context, entryID string, nodeIDs []string) (map[string]*adminv1.RelayCountryHint, map[string]*adminv1.RelayReachHint)) {
	s.relayHints = f
}

// SetRelaySettleForTest replaces the step that follows switching a relay on (nil restores the real one). Call it before the
// service takes requests.
func (s *Service) SetRelaySettleForTest(settle func(context.Context, store.RelayLink) error) {
	if settle == nil {
		settle = s.relaySettle
	}
	s.relaySettleFn = settle
}

// SetRelayWaitForTest shortens the wait for the entry's report and its poll.
func (s *Service) SetRelayWaitForTest(wait, poll time.Duration) {
	s.relayWait, s.relayPoll = wait, poll
}

func (s *Service) CheckRelay(ctx context.Context, req *connect.Request[adminv1.CheckRelayRequest]) (*connect.Response[adminv1.CheckRelayResponse], error) {
	link, err := s.st.RelayLink(ctx, req.Msg.Id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("relay")
	case err != nil:
		return nil, s.internal("get relay", err)
	}
	if s.cfg.CheckRelay == nil {
		return nil, coded(connect.CodeFailedPrecondition, "check_unavailable")
	}
	if link.BridgeServed() && reverseBlocked(link) == "" {
		// the check of the exit towards the entry's bridge site needs the site up and its pin known
		if entry, err := s.st.Node(ctx, link.ViaNodeID); err == nil && entry.BridgePort != 0 {
			_ = s.awaitBridgeSite(ctx, link.ID, entry)
		}
	}
	c, err := s.cfg.CheckRelay(ctx, link.ID, requestedRelayStages(req.Msg)...)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("relay")
	case err != nil:
		return nil, s.internal("check relay", err)
	}
	if link.Leg == store.RelayLegAuto && c.Leg.Status == "fail" && s.tryOtherLeg(ctx, link) {
		// The owner's check of an auto relay moved the leg to the other family and proved it there: the answer is that check.
		if next, err := s.st.RelayLink(ctx, link.ID); err == nil {
			c = mustRelayCheck(next)
		}
	}
	s.controlRelay(ctx, link.ID, true, true)
	s.audit(ctx, actor(ctx), "relay.check", map[string]any{"relay": link.ID, "via": link.ViaNodeName, "exit": link.ExitNodeName,
		"ok": c.OK, "verified": c.Verified(), "stage": c.Stage, "leg": c.Leg.Status, "leg_code": c.Leg.Code, "path": c.Path.Status, "path_code": c.Path.Code,
		"reverse_reach": c.ReverseReach.Status, "reverse_reach_code": c.ReverseReach.Code, "reverse_speed": c.ReverseSpeed.Status,
		"reverse_speed_code": c.ReverseSpeed.Code})
	next, err := s.st.RelayLink(ctx, link.ID)
	if err != nil {
		return nil, s.internal("read relay", err)
	}
	return connect.NewResponse(&adminv1.CheckRelayResponse{Relay: s.relayMsg(ctx, next)}), nil
}

func relayCheckMsg(c store.RelayCheck, nameOf func(string) string) *adminv1.RelayCheck {
	stage := func(s store.RelayStage) *adminv1.RelayStage {
		return &adminv1.RelayStage{Status: s.Status, Code: s.Code, SenderId: s.Sender, SenderName: nameOf(s.Sender),
			Verdict: s.Verdict, Sent: s.Sent, Got: s.Got, AtUnix: s.At, Mb: s.MB, MbitS: s.MbitS, Bytes: s.Bytes,
			ConnectMs: s.ConnectMS, FirstByteMs: s.FirstByteMS, DurationMs: s.DurationMS, Port: uint32(s.Port)}
	}
	msg := &adminv1.RelayCheck{AtUnix: c.At, Network: c.Network, Port: uint32(c.Port), To: c.To, Leg: stage(c.Leg), Path: stage(c.Path),
		Ok: c.OK, Stage: c.Stage, Verified: c.Verified()}
	if c.ReverseReach != (store.RelayStage{}) { // absent when it did not run
		msg.ReverseReach = stage(c.ReverseReach)
	}
	if c.ReverseSpeed != (store.RelayStage{}) {
		msg.ReverseSpeed = stage(c.ReverseSpeed)
	}
	if c.DirectSpeed != (store.RelayStage{}) {
		msg.DirectSpeed = stage(c.DirectSpeed)
	}
	if c.Bridge != (store.RelayStage{}) {
		msg.Bridge = stage(c.Bridge)
	}
	return msg
}

// relayCheckOf is the message of the relay's latest check (nil before the first), naming the sending nodes.
func (s *Service) relayCheckOf(ctx context.Context, l store.RelayLink) *adminv1.RelayCheck {
	c, ok := l.Check()
	if !ok || c.At == 0 {
		return nil
	}
	if _, reported := l.Bridge(); l.Reverse() || reported {
		c.Bridge = l.BridgeStage(s.now()) // derived from the exit's report, shown while there is a bridge to speak of
	}
	names := map[string]string{}
	return relayCheckMsg(c, func(id string) string {
		if id == "" || id == "panel" {
			return id
		}
		if _, seen := names[id]; !seen {
			names[id] = id
			if n, err := s.st.Access().Node(ctx, id); err == nil {
				names[id] = n.Name
			}
		}
		return names[id]
	})
}

func (s *Service) ListRelayCandidates(ctx context.Context, req *connect.Request[adminv1.ListRelayCandidatesRequest]) (*connect.Response[adminv1.ListRelayCandidatesResponse], error) {
	a := s.st.Access()
	entry, err := a.Node(ctx, req.Msg.ViaNodeId)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("node")
	case err != nil:
		return nil, s.internal("get node", err)
	}
	nodes, err := a.Nodes(ctx)
	if err != nil {
		return nil, s.internal("list nodes", err)
	}
	fullInbounds, err := a.InboundsFull(ctx, "")
	if err != nil {
		return nil, s.internal("list inbounds", err)
	}
	inboundsByNode := make(map[string][]store.AccessInboundFull)
	for _, f := range fullInbounds {
		inboundsByNode[f.Node.ID] = append(inboundsByNode[f.Node.ID], f)
	}
	links, err := s.st.RelayLinks(ctx, store.RelayFilter{ViaNodeID: entry.ID})
	if err != nil {
		return nil, s.internal("list relays", err)
	}
	entryNode, err := s.st.Node(ctx, entry.ID)
	if err != nil {
		return nil, s.internal("get entry node", err)
	}
	relayOf := make(map[string]string, len(links)) // the exit's server -> this entry's relay
	for _, l := range links {
		relayOf[l.InboundID] = l.ID
	}

	var out []*adminv1.RelayCandidate
	var exits []string
	candidateInbounds := map[string]store.AccessInboundFull{}
	for _, n := range nodes {
		if n.ID == entry.ID || n.State != "active" {
			continue
		}
		for _, f := range inboundsByNode[n.ID] {
			if !store.InboundLive(f.Inbound.Enabled, f.Inbound.State, f.Node.State) {
				continue
			}
			c := &adminv1.RelayCandidate{InboundId: f.Inbound.ID, NodeId: n.ID, NodeName: n.Name, ProfileId: f.Profile.ID,
				ProfileName: f.Profile.Name, Protocol: f.Profile.Protocol, RelayId: relayOf[f.Inbound.ID]}
			if merged, err := s.mergedSettings(f.Profile); err != nil {
				return nil, s.internal("open profile secrets", err)
			} else if spec, err := s.buildSpec(f, merged); err != nil {
				c.Unsupported = "build_failed"
			} else {
				c.Network, c.Port, c.Hop = spec.Listen.Network, uint32(f.RelayInbound().RelayExit(f.Node.Address, spec.Listen.Port).Port), spec.Listen.HopFrom != 0
				c.Unsupported = protocols.IPv6Unsupported(spec)
			}
			c.ReverseBlocked = reverseBlockedOf(c.Network, entry.AgentCaps, n.AgentCaps)
			out = append(out, c)
			candidateInbounds[f.Inbound.ID] = f
			if !slices.Contains(exits, n.ID) {
				exits = append(exits, n.ID)
			}
		}
	}
	if len(out) > 0 {
		nodeSet := map[string]bool{entry.ID: true}
		awgProfiles := map[string]bool{}
		for _, c := range out {
			nodeSet[c.NodeId] = true
			if c.Protocol == "awg" {
				awgProfiles[c.ProfileId] = true
			}
		}
		nodeIDs := make([]string, 0, len(nodeSet))
		for id := range nodeSet {
			nodeIDs = append(nodeIDs, id)
		}
		groups, err := a.Groups(ctx)
		if err != nil {
			return nil, s.internal("list relay audience groups", err)
		}
		groupOf := make(map[string]store.AccessGroup, len(groups))
		for _, g := range groups {
			groupOf[g.ID] = g
		}
		users, err := a.RelayAudienceUsers(ctx, nodeIDs)
		if err != nil {
			return nil, s.internal("list relay audience people", err)
		}
		profileIDs := make([]string, 0, len(awgProfiles))
		for id := range awgProfiles {
			profileIDs = append(profileIDs, id)
		}
		keyCounts, err := a.RelayStaticAWGDeviceCounts(ctx, profileIDs)
		if err != nil {
			return nil, s.internal("list relay audience keys", err)
		}
		now := s.now()
		var active []store.AccessUser // disabled, expired and over-quota people get no twin, as in their subscription
		for _, u := range users {
			if _, status := currentSubscriptionUser(u, now); status == StatusActive {
				active = append(active, u)
			}
		}
		for _, c := range out {
			f, ok := candidateInbounds[c.InboundId]
			if !ok {
				continue
			}
			audience := &adminv1.RelayAudience{}
			for _, u := range active {
				if !s.relayRecipientUsableFor(store.RelayLink{ViaNodeID: entry.ID}, f, store.GroupsOf(u, groupOf), u, plugin.FormatMistgate) {
					continue
				}
				if audience.People < ^uint32(0) {
					audience.People++
				}
				if c.Protocol == "awg" {
					count := keyCounts[u.ID][c.ProfileId]
					left := uint64(^uint32(0) - audience.StaticAwgDevices)
					if count > left {
						audience.StaticAwgDevices = ^uint32(0)
					} else {
						audience.StaticAwgDevices += uint32(count)
					}
				}
			}
			c.Audience = audience
		}
	}

	var country map[string]*adminv1.RelayCountryHint
	var reach map[string]*adminv1.RelayReachHint
	if s.relayHints != nil {
		country, reach = s.relayHints(ctx, entry.ID, exits)
	}
	hint := func(id string) *adminv1.RelayCountryHint {
		if h := country[id]; h != nil {
			return h
		}
		return &adminv1.RelayCountryHint{Verdict: "no_data"}
	}
	for _, c := range out {
		c.Country = hint(c.NodeId)
		c.ExitReachesEntry = exitReachHint(links, c.NodeId, entryNode.BridgeEndpointPort(), s.now())
		if c.Reach = reach[c.NodeId]; c.Reach == nil {
			c.Reach = &adminv1.RelayReachHint{Verdict: "no_data"}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Unsupported == "") != (b.Unsupported == "") {
			return a.Unsupported == ""
		}
		if ra, rb := cutRank(a.Country.Verdict), cutRank(b.Country.Verdict); ra != rb {
			return ra < rb
		}
		if an, bn := strings.ToLower(a.NodeName), strings.ToLower(b.NodeName); an != bn {
			return an < bn
		}
		return strings.ToLower(a.ProfileName) < strings.ToLower(b.ProfileName)
	})
	return connect.NewResponse(&adminv1.ListRelayCandidatesResponse{Candidates: out, AgentSupported: slices.Contains(entry.AgentCaps, agentcap.RelayForward),
		EntryCountry: hint(entry.ID)}), nil
}

// cutRank puts the exits the country check says are cut off first: those are the ones a relay is for.
func cutRank(verdict string) int {
	switch verdict {
	case "blocked":
		return 0
	case "partial":
		return 1
	}
	return 2
}
