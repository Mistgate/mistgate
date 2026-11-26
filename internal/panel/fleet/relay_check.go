package fleet

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/tcpcheck"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

// CheckRelay (relay.proto): does a relay work, in two stages, with only packets and a TLS hello and nothing changed on any host.
//
//   - leg: the entry node sends to the exit's server (its address and own port). TCP: a probe that has to come back "ours"
//     (the server's certificate, pin or REALITY chain, as the port check proves it). UDP: the entry sends a counted burst and the
//     exit counts it, as the UDP port check does.
//   - path: only when the relay is switched on and the entry reported the forward. A third node (never the entry or the exit)
//     sends to the entry's address and forwarded port; the answer is the same as for the leg, so TCP must prove the exit's
//     server and UDP is counted at the exit, after it went through the entry's DNAT. The panel itself sends only when no
//     third node can.
//
// The whole check holds both nodes' port-check gates (acquirePortCheck): the exit has one UDP counter table, and the reverse
// probe targets the entry. A relay check and a port check must not measure either node at once. The result goes into the relay
// (check_json) and comes back; it is the caller's to act on (the gate that switches a relay on is in the access module).
//
// stages names what to run (store.RelayStageLeg, RelayStagePath, RelayStageReverseReach, RelayStageReverseSpeed or
// RelayStageDirectSpeed; none = leg and path). A reverse reach check also runs after a failed leg. The two speed proofs only run
// when named: reverse_speed at the owner's request, direct_speed at the mode controller's (it is what lets a reverse relay go back
// to the direct way). The result carries the bridge stage of a relay in the reverse way, derived from the exit's report.
func (f *Fleet) CheckRelay(ctx context.Context, relayID string, stages ...string) (store.RelayCheck, error) {
	runLeg, runPath := len(stages) == 0 || slices.Contains(stages, store.RelayStageLeg), len(stages) == 0 || slices.Contains(stages, store.RelayStagePath)
	requestReverseReach := slices.Contains(stages, store.RelayStageReverseReach)
	runReverseSpeed := slices.Contains(stages, store.RelayStageReverseSpeed)
	runDirectSpeed := slices.Contains(stages, store.RelayStageDirectSpeed)
	link, err := f.st.RelayLink(ctx, relayID)
	if err != nil {
		return store.RelayCheck{}, err
	}
	release, err := f.acquireRelayPortChecks(ctx, link.ViaNodeID, link.ExitNodeID)
	if err != nil {
		return store.RelayCheck{}, err
	}
	defer release()
	// what the owner did while this check waited for the exit's gate counts
	if link, err = f.st.RelayLink(ctx, relayID); err != nil {
		return store.RelayCheck{}, err
	}
	exit, err := f.st.Node(ctx, link.ExitNodeID)
	if err != nil {
		return store.RelayCheck{}, err
	}
	entry, err := f.st.Node(ctx, link.ViaNodeID)
	if err != nil {
		return store.RelayCheck{}, err
	}
	row, err := f.st.FleetInbound(ctx, link.InboundID)
	if err != nil {
		return store.RelayCheck{}, err
	}

	now := f.now().UTC()
	check := store.RelayCheck{At: now.Unix(), Network: link.Network, Port: link.Port}
	spec, specErr := f.buildSpec(exit, row)
	if specErr == nil {
		check.To = row.RelayExit(exit.Address, spec.Listen.Port).String()
	}
	if !runLeg || !runPath {
		// a stage that does not run keeps what the last check of the same target found (its own at_unix says how old that is): the
		// leg probe of a reverse relay, made every half hour, must not take away the path that shows the bridge carries people
		if prev, ok := link.Check(); ok && prev.Network == check.Network && prev.To == check.To && check.To != "" {
			if !runLeg {
				check.Leg = prev.Leg
			}
			if !runPath && prev.Port == check.Port {
				check.Path = prev.Path
			}
		}
	}
	if prev, ok := link.Check(); ok {
		// the reverse stages answer a question of the entry's site, not of this run's target: one that does not run keeps what the
		// last check found, and its own at_unix says how old that is
		check.ReverseReach, check.ReverseSpeed = prev.ReverseReach, prev.ReverseSpeed
		if prev.Network == check.Network && prev.To == check.To && prev.Port == check.Port && check.To != "" {
			check.DirectSpeed = prev.DirectSpeed
		}
		if check.ReverseReach.Port != 0 && check.ReverseReach.Port != entry.BridgeEndpointPort() ||
			check.ReverseReach.Port == 0 && check.ReverseReach.At > 0 && entry.BridgeEndpointPort() != 443 {
			check.ReverseReach = store.RelayStage{} // a probe of the endpoint the entry had before says nothing about this one
		}
	}
	rc := relayRun{f: f, link: link, entry: entry, exit: exit, spec: spec, row: row, now: now}
	if specErr == nil {
		liveRows, err := f.Live(ctx)
		if err != nil {
			return store.RelayCheck{}, err
		}
		rc.live = liveRowsByID(liveRows)
	}
	switch {
	case specErr != nil:
		f.log.Warn("relay check: the server's spec does not build", "relay", link.ID, "err", specErr)
		check.Leg = rc.stage("fail", "spec", "")
		check.Path = rc.stage("skipped", "leg_failed", "")
	default:
		if runLeg {
			check.Leg = rc.leg(ctx)
		}
		switch {
		case !runPath:
		case runLeg && check.Leg.Status != "ok":
			if check.Leg.Unverifiable() {
				check.Path = rc.stage("skipped", "unverifiable", "")
			} else {
				check.Path = rc.stage("skipped", "leg_failed", "")
			}
		default:
			check.Path = rc.path(ctx)
		}
	}
	runReverseReach := requestReverseReach || runLeg && check.Leg.Status == "fail"
	if runReverseReach || runReverseSpeed {
		if rc.live == nil {
			liveRows, err := f.Live(ctx)
			if err != nil {
				f.log.Warn("relay reverse check: read live nodes", "code", "store_failed", "relay", link.ID, "err", err)
				if runReverseReach {
					check.ReverseReach = rc.stage("fail", "failed", "")
				}
				if runReverseSpeed {
					check.ReverseSpeed = rc.stage("fail", "failed", "")
				}
			} else {
				rc.live = liveRowsByID(liveRows)
			}
		}
		if rc.live != nil {
			if runReverseReach {
				check.ReverseReach = rc.reverseReach(ctx)
			}
			if runReverseSpeed {
				check.ReverseSpeed = rc.reverseSpeed(ctx)
			}
		}
	}
	if runDirectSpeed {
		if rc.live == nil {
			if liveRows, err := f.Live(ctx); err == nil {
				rc.live = liveRowsByID(liveRows)
			}
		}
		if rc.live != nil {
			check.DirectSpeed = rc.directSpeed(ctx)
		} else {
			check.DirectSpeed = rc.stage("fail", "failed", "")
		}
	}
	if link.Reverse() {
		check.Bridge = link.BridgeStage(now) // what the exit's agent says, not stored with the check
	}
	check.Settle()
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := f.st.SetRelayCheck(saveCtx, link.ID, check); err != nil && !errors.Is(err, store.ErrNotFound) {
		f.log.Warn("store relay check", "relay", link.ID, "err", err)
	}
	return check, nil
}

// relayRun is one CheckRelay: the relay and what its stages read.
type relayRun struct {
	f           *Fleet
	link        store.RelayLink
	entry, exit store.NodeRow
	spec        plugin.InboundSpec
	row         store.FleetInboundRow
	live        map[string]store.NodeLiveRow
	now         time.Time
}

func (r relayRun) stage(status, code, sender string) store.RelayStage {
	return store.RelayStage{Status: status, Code: code, Sender: sender, At: r.now.Unix()}
}

func (r relayRun) udp() bool { return r.link.Network == "udp" }

// capability is what a node must list to send (and, for UDP, to count) this relay's probes.
func (r relayRun) capability() string {
	if r.udp() {
		return agentcap.UDPCheck
	}
	return agentcap.TCPCheck
}

// online: the node's agent is connected and lists the capability; else the failure code.
func (r relayRun) online(id, code string) string {
	l := r.live[id]
	switch {
	case !r.f.reachable(l, id):
		return code
	case !slices.Contains(l.AgentCaps, r.capability()):
		return "agent_too_old"
	}
	return ""
}

// leg: the entry sends to the exit's server.
func (r relayRun) leg(ctx context.Context) store.RelayStage {
	if code := r.online(r.entry.ID, "node_offline"); code != "" {
		return r.stage("fail", code, "")
	}
	sender := portSender{node: r.entry, live: r.live[r.entry.ID]}
	if r.udp() {
		if code := r.online(r.exit.ID, "exit_offline"); code != "" {
			return r.stage("fail", code, "")
		}
		out := r.f.runPortCheckAttempt(ctx, r.exit, r.live[r.exit.ID], sender, portDial{Host: r.exit.Address}, []uint16{r.spec.Listen.Port})
		st, _ := r.udpStage(out, sender)
		return st
	}
	target, ok := relayTCPTarget(r.spec, r.row, r.row.RelayExit(r.exit.Address, r.spec.Listen.Port).Port)
	if !ok {
		return r.stage("skipped", "unverifiable", portSenderID(sender))
	}
	st, _ := r.tcpStage(ctx, r.exit.Address, sender, target)
	return st
}

// path: a third node sends to the entry's forwarded port; the exit proves or counts what arrives.
func (r relayRun) path(ctx context.Context) store.RelayStage {
	switch {
	case !r.link.Enabled:
		return r.stage("skipped", "off", "")
	case r.link.State == "failed":
		return r.stage("fail", "apply_failed", "")
	case r.link.State != "active":
		return r.stage("skipped", "pending", "")
	}
	if r.udp() {
		if code := r.online(r.exit.ID, "exit_offline"); code != "" {
			return r.stage("fail", code, "")
		}
	}
	var target tcpTarget
	if !r.udp() {
		var ok bool
		if target, ok = relayTCPTarget(r.spec, r.row, r.link.Port); !ok {
			return r.stage("skipped", "unverifiable", "")
		}
	}
	// One attempt per sender, two senders at most. A sender that ran and found the path broken is asked once more with
	// another one (a sender's own network may be what is cut); one that could not run the probe (busy, no route, a failed
	// command) is replaced. The panel sends only when no node ran one.
	nodes, err := r.f.st.Nodes(ctx, false)
	if err != nil {
		r.f.log.Warn("relay check: list nodes", "relay", r.link.ID, "err", err)
		return r.stage("fail", "failed", "")
	}
	var senders []portSender
	for _, s := range r.f.portSenders(r.entry, nodes, r.live, r.capability()) { // ordered by how unlike the entry they are
		if s.node.ID != r.exit.ID {
			senders = append(senders, s)
		}
	}
	if len(senders) > 2 {
		senders = senders[:2]
	}
	var failed, errored *store.RelayStage
	for _, s := range senders {
		st, ran := r.pathFrom(ctx, s, target)
		switch {
		case !ran:
			errored = &st
		case st.Status == "ok":
			return st
		case failed == nil:
			failed = &st // keep the first finding, and let the second sender have its say
		}
	}
	if failed != nil {
		return *failed
	}
	if panelSendsUDP {
		if r.panelOnTheRelay(ctx) {
			return r.stage("fail", "same_host", "panel")
		}
		st, _ := r.pathFrom(ctx, portSender{panel: true}, target)
		return st
	}
	if errored != nil {
		return *errored
	}
	return r.stage("fail", "no_sender", "")
}

// panelOnTheRelay: the panel runs on the entry's or the exit's host, where a send never leaves the machine and proves nothing
// about the path.
func (r relayRun) panelOnTheRelay(ctx context.Context) bool {
	for _, host := range []string{r.entry.Address, r.exit.Address} {
		if a, err := udpcheck.Resolve(ctx, host); err == nil && panelHasAddress(a) {
			return true
		}
	}
	return false
}

// pathFrom is one attempt of the path from one sender; ran is false when the sender could not run the probe at all.
func (r relayRun) pathFrom(ctx context.Context, sender portSender, target tcpTarget) (st store.RelayStage, ran bool) {
	if r.udp() {
		out := r.f.runPortCheckAttempt(ctx, r.exit, r.live[r.exit.ID], sender,
			portDial{Host: r.entry.Address, Ports: []uint16{r.link.Port}}, []uint16{r.spec.Listen.Port})
		return r.udpStage(out, sender)
	}
	return r.tcpStage(ctx, r.entry.Address, sender, target)
}

// udpStage reads one UDP run of a single port: the verdict of the exit's count, or why the run did not happen.
func (r relayRun) udpStage(out portCheckOutcome, sender portSender) (store.RelayStage, bool) {
	id := portSenderID(sender)
	if len(out.checks) == 0 || (out.errorCode != "" && out.errorCode != "inconclusive") {
		code := out.errorCode
		if code == "" || code == "inconclusive" {
			code = "failed"
		}
		return r.stage("fail", code, id), false
	}
	c := out.checks[0]
	st := r.stage("ok", "", id)
	st.Verdict, st.Sent, st.Got = c.Verdict, c.Sent, c.Got
	if c.Verdict != "ok" {
		st.Status, st.Code = "fail", c.Verdict
	}
	return st, true
}

// tcpStage probes host for the target's TLS identity from the sender: the server must be ours.
func (r relayRun) tcpStage(ctx context.Context, host string, sender portSender, t tcpTarget) (store.RelayStage, bool) {
	id := portSenderID(sender)
	verdicts, code := r.f.probeTCPTargets(ctx, host, sender, []tcpTarget{t}, nil, nil)
	if code != "" {
		return r.stage("fail", code, id), false
	}
	v := verdicts[t.Port]
	if !validTCPVerdict(v) {
		return r.stage("fail", "failed", id), false
	}
	st := r.stage("ok", "", id)
	st.Verdict = v
	if v != tcpcheck.VerdictOurs {
		st.Status, st.Code = "fail", v
	}
	return st, true
}

// relayTCPTarget is the TLS probe that tells whether a connection to the server's TCP port reached Mistgate, dialled on port
// (the server's own on the exit, the forwarded one on the entry); false when the server has no name to probe it by.
func relayTCPTarget(spec plugin.InboundSpec, row store.FleetInboundRow, port uint16) (tcpTarget, bool) {
	pins := make(map[string]string)
	if pin, ok := protocols.NormalizePin(row.CertPin); ok {
		pins[spec.ID] = pin
	}
	for _, t := range tcpTargets([]tcpSpec{{Profile: row.ProfileName, Spec: spec}}, pins) {
		if t.Port == spec.Listen.Port && t.Kind == tcpcheck.KindTLS {
			t.Port = port
			return t, true
		}
	}
	return tcpTarget{}, false
}
