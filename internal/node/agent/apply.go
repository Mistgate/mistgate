package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// applyDesired merges one DesiredState into the model, drives the engines to it and describes the outcome.
// It returns nil for a message that is ignored (an old delta).
func (a *Agent) applyDesired(ctx context.Context, ds *pb.DesiredState) *pb.ApplyResult {
	start := time.Now()
	cur := a.snapshotModel()
	res := &pb.ApplyResult{Revision: ds.Revision}
	done := func() *pb.ApplyResult {
		res.DurationMs = uint32(time.Since(start).Milliseconds())
		return res
	}
	if ds.BaseRevision != 0 && ds.Revision < cur.revision {
		a.log.Debug("old delta ignored", "revision", ds.Revision, "applied", cur.revision)
		return nil
	}
	if ds.BaseRevision != 0 && ds.Revision == cur.revision { // re-delivery of what we already run
		a.persistModel(cur, true)
		res.Status = pb.ApplyStatus_APPLY_STATUS_APPLIED
		res.StateHash = a.observedHash(cur)
		res.Inbounds = a.cachedResults(cur)
		res.Status = applyStatus(res.Inbounds)
		if cur.partial {
			res.Status = pb.ApplyStatus_APPLY_STATUS_PARTIAL
		}
		res.Relays = a.relay.results
		return done()
	}

	next, err := merge(cur, ds, a.knows)
	var partial *partialStateError
	if err != nil && errors.As(err, &partial) {
		err = nil
	}
	switch {
	case errors.Is(err, errBaseMismatch):
		a.log.Info("delta on the wrong base", "base", ds.BaseRevision, "applied", cur.revision)
		res.Status, res.Error = pb.ApplyStatus_APPLY_STATUS_BASE_MISMATCH, err.Error()
		res.StateHash = a.observedHash(cur)
		return done()
	case err != nil:
		a.log.Warn("desired state rejected", "revision", ds.Revision, "err", err)
		res.Status, res.Error = pb.ApplyStatus_APPLY_STATUS_REJECTED, err.Error()
		res.StateHash = a.observedHash(cur)
		return done()
	}
	if partial != nil {
		a.reportStateIssues(partial.issues)
	}
	a.clearChangedApplyMarkers(ds, next)
	if ds.Settings != nil {
		a.settings.Store(ds.Settings)
		if ds.Settings.GetOsStatus() {
			a.wakeOsStatusReader()
		}
		a.configureNetworks(ds.Settings)
	}
	a.startAwgKmod(ctx, next)
	if ds.StateHash != "" && ds.StateHash != next.hash() {
		// Not fatal: the authoritative comparison is the observed hash below, but this tells us whether the
		// merge itself diverged from the panel's idea of the state.
		a.log.Warn("merged state hash differs from the panel's", "revision", ds.Revision, "base", ds.BaseRevision)
	}

	results := a.reconcile(ctx, next, nil)
	a.setModel(next)
	a.persistModel(next, false)
	res.Status = applyStatus(results)
	if partial != nil || next.partial {
		res.Status = pb.ApplyStatus_APPLY_STATUS_PARTIAL
	}
	res.Inbounds = results
	res.Relays = a.relay.results
	res.StateHash = a.observedHash(next)
	if ds.BaseRevision == 0 { // deltas are frequent; only a full apply is an event worth reporting
		added, removed, changed, users := diffModels(cur, next)
		a.event(pb.Severity_SEVERITY_INFO, "state_applied", "", map[string]string{
			"revision": strconv.FormatUint(next.revision, 10), "inbounds": strconv.Itoa(len(results)),
			"added": strconv.Itoa(added), "removed": strconv.Itoa(removed), "changed": strconv.Itoa(changed), "users": strconv.Itoa(users),
		})
	}
	a.log.Info("desired state applied", "revision", next.revision, "full", ds.BaseRevision == 0, "inbounds", len(results), "status", res.Status)
	return done()
}

func applyStatus(results []*pb.InboundResult) pb.ApplyStatus {
	for _, r := range results {
		if r.Error != "" {
			return pb.ApplyStatus_APPLY_STATUS_PARTIAL
		}
	}
	return pb.ApplyStatus_APPLY_STATUS_APPLIED
}

// diffModels counts what a full apply changed against the state the agent held: inbounds added, removed, with another
// spec, and credentials (users' devices) added, removed or changed across all inbounds present on both sides.
func diffModels(cur, next *model) (added, removed, changed, creds int) {
	for id, n := range next.inbounds {
		o := cur.inbounds[id]
		if o == nil {
			added++
			creds += len(n.creds)
			continue
		}
		if statehash.Spec(o.spec) != statehash.Spec(n.spec) {
			changed++
		}
		for cid, nc := range n.creds {
			if oc, ok := o.creds[cid]; !ok || !reflect.DeepEqual(oc, nc) {
				creds++
			}
		}
		for cid := range o.creds {
			if _, ok := n.creds[cid]; !ok {
				creds++
			}
		}
	}
	for id, o := range cur.inbounds {
		if next.inbounds[id] == nil {
			removed++
			creds += len(o.creds)
		}
	}
	return
}

// reconcile drives the engines and the host to next. Inbounds whose spec and ACTIVE credential set are
// unchanged since the last successful apply are not touched, so a change on one inbound never disturbs
// the others. force lists inbounds that must be applied even if unchanged (restart).
func (a *Agent) reconcile(ctx context.Context, next *model, force map[string]bool) []*pb.InboundResult {
	prev := a.snapshotModel()
	prevRelayClaims := a.relay.claims
	// The host side of the L3 protocols comes first (l3.go): WARP, then the routes and the firewall of the tunnel
	// interfaces, then the engines that create those interfaces. An inbound that depends on a step that failed is
	// blocked (a.blocked) instead of started.
	a.engineSettings(ctx, next)
	a.syncWarp(ctx, next)
	a.syncTunnels(ctx, next)
	// The forwards come before the engines too: a TCP port a forward holds is a claim like an inbound's, so a decoy site
	// gives it up while the forward is installed and keeps it when the forward failed.
	a.syncRelays(ctx, next)
	nextTCP := withRelayClaims(tcpPortClaims(next), a.relay.claims)
	claimedTCP := mergeTCPPortClaims(withRelayClaims(tcpPortClaims(prev), prevRelayClaims), nextTCP)
	var removedFailures []*pb.InboundResult
	for _, id := range prev.ids() {
		old := prev.inbounds[id]
		n := next.inbounds[id]
		if n != nil && n.parseErr == "" && n.applyErr == "" && n.spec.Protocol == old.spec.Protocol {
			continue
		}
		if e := a.engines[old.spec.Protocol]; e != nil {
			if err := a.removeEngine(ctx, old.spec.Protocol, e, id); err != nil {
				if isEnginePanic(err) {
					if n != nil {
						n.applyErr = err.Error()
					} else {
						removedFailures = append(removedFailures, &pb.InboundResult{InboundId: id, State: pb.InboundRunState_INBOUND_RUN_STATE_FAILED, Error: err.Error()})
						a.event(pb.Severity_SEVERITY_ERROR, "engine_failed", id, map[string]string{"error": err.Error()})
					}
				} else {
					a.log.Warn("remove inbound", "inbound", id, "err", err)
				}
			}
		}
		delete(a.held, id)
	}
	a.syncTCPClaims(ctx, claimedTCP)
	now := a.now()
	results := make([]*pb.InboundResult, 0, len(next.inbounds))
	for _, id := range next.ids() {
		results = append(results, a.applyInbound(ctx, next.inbounds[id], now, force[id]))
	}
	a.syncTCPClaims(ctx, nextTCP)
	// NFQUEUE rules are installed before an AWG interface is created. Refresh its interface-index map now that
	// the engines have applied, so packets use exact active interface identities from this point on.
	a.syncTorrentGuardApplied(ctx, next, results)
	active := successfullyAppliedEnabledInbounds(results)
	hops := a.syncHops(ctx, next, results, active)
	a.syncInboundPorts(ctx, next, active, hops)
	a.noteCerts(results)
	results = append(results, removedFailures...)
	var failed []string
	for _, r := range results {
		if r.Error != "" {
			failed = append(failed, r.InboundId)
		}
	}
	a.failedApply.Store(&failed)
	return results
}

func tcpPortClaims(next *model) map[uint16]string {
	claimed := make(map[uint16]string)
	for _, id := range next.ids() {
		spec := next.inbounds[id].spec
		if next.inbounds[id].parseErr == "" && next.inbounds[id].applyErr == "" && spec.Enabled && spec.Listen.Network == "tcp" {
			if _, exists := claimed[spec.Listen.Port]; !exists { // ids are sorted; keep the same lower-id winner as hop checks
				claimed[spec.Listen.Port] = id
			}
		}
	}
	return claimed
}

func mergeTCPPortClaims(prev, next map[uint16]string) map[uint16]string {
	claimed := make(map[uint16]string, len(prev)+len(next))
	for port, id := range prev {
		claimed[port] = id
	}
	for port, id := range next {
		claimed[port] = id
	}
	return claimed
}

func (a *Agent) syncTCPClaims(ctx context.Context, claimed map[uint16]string) {
	for _, protocol := range a.protocols {
		if user, ok := a.engines[protocol].(engine.TCPPortUser); ok {
			user.TCPClaims(ctx, claimed)
		}
	}
}

func (a *Agent) applyInbound(ctx context.Context, in *inbound, now time.Time, force bool) *pb.InboundResult {
	spec := in.spec
	eng := a.engines[spec.Protocol]
	creds := in.active(now)
	expired := len(in.creds) - len(creds)
	sh, ch := statehash.Spec(spec), statehash.Creds(creds)
	h := a.held[spec.ID]
	// An inbound that may not run now (egress WARP without WARP, or the host side of its path failed) is checked before
	// the cache: a running one is stopped, there is never a silent direct exit.
	why, blocked := a.blocked[spec.ID]
	blocked = blocked && spec.Enabled
	if !blocked && in.parseErr == "" && in.applyErr == "" && h != nil && !force && h.spec == sh && h.creds == ch &&
		h.res.State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED {
		res := proto.Clone(h.res).(*pb.InboundResult)
		a.refreshState(res)
		return res
	}

	var rep engine.ApplyReport
	var err error
	if a.crashSkips[spec.ID] {
		delete(a.crashSkips, spec.ID)
		err = errors.New("skipped after a crash while applying it")
	} else if in.applyErr != "" {
		err = errors.New(in.applyErr)
	} else if in.parseErr != "" {
		err = errors.New(in.parseErr)
	} else if blocked {
		err = a.blockedFail(ctx, spec, why)
	} else {
		rep, err = a.applyEngine(ctx, spec, creds, eng)
	}
	res := &pb.InboundResult{InboundId: spec.ID, SpecHash: sh}
	wasFailing := h != nil && h.res.State == pb.InboundRunState_INBOUND_RUN_STATE_FAILED
	if err != nil {
		res.State, res.Error = pb.InboundRunState_INBOUND_RUN_STATE_FAILED, err.Error()
		if h == nil || h.res.Error != res.Error || h.res.State != pb.InboundRunState_INBOUND_RUN_STATE_FAILED {
			if !isEnginePanic(err) {
				a.log.Error("inbound failed", "inbound", spec.ID, "err", err)
			}
			if isBackendUnavailable(err) { // agent.proto "AWG AND WARP": awg_backend_unavailable {reason}
				a.event(pb.Severity_SEVERITY_ERROR, "awg_backend_unavailable", spec.ID, map[string]string{"reason": res.Error})
			} else {
				a.event(pb.Severity_SEVERITY_ERROR, "engine_failed", spec.ID, map[string]string{"error": res.Error})
			}
		}
	} else {
		if rep.SpecHash != "" {
			res.SpecHash = rep.SpecHash
		}
		res.CredCount = uint32(rep.CredCount)
		res.Restarted = rep.Restarted
		if w := append(append([]string(nil), in.credWarn...), rep.Warnings...); len(w) != 0 {
			res.Error = fmt.Sprintf("%d credential%s skipped: %s", len(w), plural(len(w)), strings.Join(w, "; "))
		}
		res.CertPinSha256 = rep.Cert.PinSHA256
		if !rep.Cert.NotAfter.IsZero() {
			res.CertNotAfterUnix = rep.Cert.NotAfter.Unix()
		}
		res.State = pb.InboundRunState_INBOUND_RUN_STATE_RUNNING
		if !spec.Enabled {
			res.State = pb.InboundRunState_INBOUND_RUN_STATE_STOPPED
		}
		a.refreshState(res)
		// The reason is what the agent knows; the panel adds who asked when it was an admin (profile name and
		// protocol are joined in by the event list).
		switch {
		case !spec.Enabled:
		case force:
			a.event(pb.Severity_SEVERITY_INFO, "engine_restarted", spec.ID, map[string]string{"protocol": spec.Protocol, "reason": "restart_command"})
		case rep.Restarted && h != nil && !wasFailing:
			a.event(pb.Severity_SEVERITY_INFO, "engine_restarted", spec.ID, map[string]string{"protocol": spec.Protocol, "reason": "config_changed"})
		case h == nil || wasFailing:
			why := "profile_added"
			switch {
			case !a.warm: // the persisted state coming back after the process started
				why = "agent_start"
			case wasFailing:
				why = "recovered"
			}
			a.event(pb.Severity_SEVERITY_INFO, "engine_started", spec.ID, map[string]string{"protocol": spec.Protocol, "reason": why})
		}
	}
	prevExpired := 0
	if h != nil {
		prevExpired = h.expired
	}
	if expired > prevExpired {
		a.event(pb.Severity_SEVERITY_INFO, "credential_expired", spec.ID, map[string]string{"count": strconv.Itoa(expired - prevExpired)})
	}
	a.held[spec.ID] = &held{spec: sh, creds: ch, expired: expired, res: proto.Clone(res).(*pb.InboundResult)}
	return res
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

type enginePanicError struct{ reason string }

func (e *enginePanicError) Error() string { return e.reason }

func enginePanicReason(v any) string {
	var detail string
	switch p := v.(type) {
	case string:
		detail = p
	case error:
		detail = p.Error()
	default:
		detail = fmt.Sprintf("%T", p)
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = fmt.Sprintf("%T", v)
	}
	if len(detail) > 160 {
		detail = detail[:160]
	}
	return "engine panic: " + detail
}

func isEnginePanic(err error) bool {
	var target *enginePanicError
	return errors.As(err, &target)
}

func (a *Agent) applyEngine(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred, eng engine.Engine) (rep engine.ApplyReport, err error) {
	prior := a.beginApplyMarker(spec.ID, statehash.Spec(spec))
	defer func() {
		if v := recover(); v != nil {
			err = &enginePanicError{reason: enginePanicReason(v)}
			a.log.Error("engine panic", "protocol", spec.Protocol, "inbound", spec.ID, "operation", "apply", "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
		}
		if err == nil || !prior {
			a.finishApplyMarker(spec.ID)
		}
	}()
	// This catches synchronous engine panics only; panics in goroutines started by an engine are out of scope.
	return eng.Apply(ctx, spec, creds)
}

func (a *Agent) removeEngine(ctx context.Context, protocol string, eng engine.Engine, id string) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = &enginePanicError{reason: enginePanicReason(v)}
			a.log.Error("engine panic", "protocol", protocol, "inbound", id, "operation", "remove", "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
		}
	}()
	return eng.Remove(ctx, id)
}

func (a *Agent) loadApplyMarkers() {
	if a.applyMarkersLoaded {
		return
	}
	markers, err := loadApplyMarkers(a.cfg.StateDir)
	if err != nil {
		a.log.Warn("apply crash marker ignored", "err", err)
		markers = map[string]string{}
		_ = saveApplyMarkers(a.cfg.StateDir, markers)
	}
	a.applyMarkers, a.applyMarkersLoaded = markers, true
	a.crashSkips = make(map[string]bool, len(markers))
	for id := range markers {
		a.crashSkips[id] = true
	}
}

func (a *Agent) beginApplyMarker(id, specHash string) bool {
	a.loadApplyMarkers()
	prior := a.applyMarkers[id] == specHash
	a.applyMarkers[id] = specHash
	if err := saveApplyMarkers(a.cfg.StateDir, a.applyMarkers); err != nil {
		a.log.Error("write apply crash marker", "inbound", id, "err", err)
	}
	return prior
}

func (a *Agent) finishApplyMarker(id string) {
	a.loadApplyMarkers()
	delete(a.applyMarkers, id)
	if err := saveApplyMarkers(a.cfg.StateDir, a.applyMarkers); err != nil {
		a.log.Warn("clear apply crash marker", "inbound", id, "err", err)
	}
}

func (a *Agent) clearChangedApplyMarkers(ds *pb.DesiredState, next *model) {
	a.loadApplyMarkers()
	removed := make(map[string]bool, len(ds.RemovedInboundIds))
	for _, id := range ds.RemovedInboundIds {
		removed[id] = true
	}
	sentSpecs := make(map[string]string, len(ds.Inbounds))
	for _, is := range ds.Inbounds {
		if is != nil && is.InboundId != "" && is.Spec != nil {
			if in := next.inbounds[is.InboundId]; in != nil {
				sentSpecs[is.InboundId] = statehash.Spec(in.spec)
			}
		}
	}
	changed := false
	for id, hash := range a.applyMarkers {
		if newHash, sent := sentSpecs[id]; sent && newHash == hash {
			continue
		}
		if _, sent := sentSpecs[id]; !sent && ds.BaseRevision != 0 && !removed[id] {
			continue
		}
		delete(a.applyMarkers, id)
		delete(a.crashSkips, id)
		changed = true
	}
	if changed {
		if err := saveApplyMarkers(a.cfg.StateDir, a.applyMarkers); err != nil {
			a.log.Warn("clear changed apply crash marker", "err", err)
		}
	}
}

func (a *Agent) reportStateIssues(issues []stateIssue) {
	for _, issue := range issues {
		if issue.kind == "inbound" {
			continue // applyInbound reports the failed inbound with its parse reason.
		}
		a.log.Warn("desired state item skipped", "kind", issue.kind, "id", issue.id, "reason", issue.reason)
		a.event(pb.Severity_SEVERITY_ERROR, "desired_state_item_skipped", issue.id, map[string]string{"kind": issue.kind, "error": issue.reason})
	}
}

// refreshState takes the live run state from the engine's health, which can change after Apply.
func (a *Agent) refreshState(res *pb.InboundResult) {
	for _, p := range a.protocols {
		for _, hh := range a.engines[p].Health() {
			if hh.InboundID != res.InboundId {
				continue
			}
			res.State = pb.InboundRunState(hh.State)
			if hh.State == plugin.RunFailed && hh.Detail != "" {
				res.Error = hh.Detail
			}
			// the certificate may have been issued (ACME) or renewed after the engine answered Apply; a result that
			// carried none must not blank the panel's value on a later re-delivery
			if hh.CertPinSHA256 != "" {
				res.CertPinSha256 = hh.CertPinSHA256
				if !hh.CertNotAfter.IsZero() {
					res.CertNotAfterUnix = hh.CertNotAfter.Unix()
				}
			}
		}
	}
}

// syncHops installs the port-hop redirects of the inbounds that are up. It changes the firewall only when
// the set of hops changed. A failure is reported on the affected inbounds and retried on the next apply.
func successfullyAppliedEnabledInbounds(results []*pb.InboundResult) map[string]bool {
	active := make(map[string]bool, len(results))
	for _, r := range results {
		if r.Error == "" && r.State == pb.InboundRunState_INBOUND_RUN_STATE_RUNNING {
			active[r.InboundId] = true
		}
	}
	return active
}

func (a *Agent) syncHops(ctx context.Context, next *model, results []*pb.InboundResult, active map[string]bool) []hostctl.Hop {
	var hops []hostctl.Hop
	rej := map[string]string{}
	for _, id := range next.ids() {
		s := next.inbounds[id].spec
		if s.Enabled && active[id] && s.Listen.HopFrom != 0 {
			h := hostctl.Hop{InboundID: id, Network: s.Listen.Network, From: s.Listen.HopFrom, To: s.Listen.HopTo, Port: s.Listen.Port}
			// A range the node must not redirect is refused for THIS inbound only; the others still install.
			if err := checkHop(h, hops, next, a.host.SSHPorts()); err != nil {
				rej[id] = "port hop rejected: " + err.Error()
				continue
			}
			hops = append(hops, h)
		}
	}
	for id, msg := range rej {
		if a.hopRej[id] != msg {
			a.log.Error("port hop rejected", "inbound", id, "reason", msg)
			a.event(pb.Severity_SEVERITY_ERROR, "hop_rejected", id, map[string]string{"error": msg})
		}
	}
	a.hopRej = rej
	overlayHopRej(results, rej)
	if a.hopsSet && hopsEqual(hops, a.hops) {
		return hops
	}
	if err := a.host.SetPortHops(ctx, hops); err != nil {
		a.hopsSet = false
		a.log.Error("port hopping not installed", "err", err)
		for _, r := range results {
			for _, h := range hops {
				if h.InboundID == r.InboundId && r.Error == "" {
					r.Error = "port hop: " + err.Error()
				}
			}
		}
		a.event(pb.Severity_SEVERITY_ERROR, "hop_failed", "", map[string]string{"error": err.Error()})
		return nil
	}
	a.hops, a.hopsSet = hops, true
	return hops
}

// syncInboundPorts mirrors only enabled listeners whose engine apply succeeded. UDP hop ranges are included only after
// their exact nft redirect was accepted and installed. A failure is a node-level warning, not an inbound error: the
// listener runs, and the host firewall may well be open by the owner's own rules.
func (a *Agent) syncInboundPorts(ctx context.Context, next *model, active map[string]bool, hops []hostctl.Hop) {
	byHop := make(map[string]hostctl.Hop, len(hops))
	for _, h := range hops {
		byHop[h.InboundID] = h
	}
	udpSet := make(map[hostctl.UDPInboundPort]struct{})
	tcpSet := make(map[uint16]struct{})
	var errs []error
	for _, id := range next.ids() {
		s := next.inbounds[id].spec
		if !s.Enabled || !active[id] {
			continue
		}
		switch s.Listen.Network {
		case "udp":
			if s.Listen.Port == 0 {
				errs = append(errs, fmt.Errorf("inbound %s has an invalid UDP listener port %d", id, s.Listen.Port))
				continue
			}
			udpSet[hostctl.UDPInboundPort{Port: s.Listen.Port}] = struct{}{}
			if h, ok := byHop[id]; ok && h.Network == "udp" {
				udpSet[hostctl.UDPInboundPort{From: h.From, To: h.To}] = struct{}{}
			}
		case "tcp":
			if s.Listen.Port == 0 {
				errs = append(errs, fmt.Errorf("inbound %s has an invalid TCP listener port %d", id, s.Listen.Port))
				continue
			}
			tcpSet[s.Listen.Port] = struct{}{}
		}
	}
	udp := make([]hostctl.UDPInboundPort, 0, len(udpSet))
	for port := range udpSet {
		udp = append(udp, port)
	}
	sort.Slice(udp, func(i, j int) bool {
		if udp[i].Port != udp[j].Port {
			return udp[i].Port < udp[j].Port
		}
		if udp[i].From != udp[j].From {
			return udp[i].From < udp[j].From
		}
		return udp[i].To < udp[j].To
	})
	tcp := make([]uint16, 0, len(tcpSet))
	for port := range tcpSet {
		tcp = append(tcp, port)
	}
	sort.Slice(tcp, func(i, j int) bool { return tcp[i] < tcp[j] })
	if err := a.host.SyncInboundPorts(ctx, udp, tcp); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		a.markHostFirewallSyncFailure(err, udp, tcp)
	} else {
		a.clearHostFirewallSyncFailure()
	}
}

// markHostFirewallSyncFailure reports one warning per distinct failure; the same failure on later reconciles is quiet.
// "ports" lists the UDP ports and hop ranges the server's firewall has to let in ("443, 20000-20010"), for the owner.
func (a *Agent) markHostFirewallSyncFailure(err error, udp []hostctl.UDPInboundPort, tcp []uint16) {
	errMsg := err.Error()
	if a.hostFirewallErr != errMsg {
		a.hostFirewallErr = errMsg
		a.log.Warn("host firewall inbound rules were not fully reconciled", "err", err)
		list := make([]string, len(udp))
		for i, p := range udp {
			if p.Port != 0 {
				list[i] = strconv.Itoa(int(p.Port))
			} else {
				list[i] = fmt.Sprintf("%d-%d", p.From, p.To)
			}
		}
		if len(tcp) > 0 {
			tcpPorts := make([]string, len(tcp))
			for i, port := range tcp {
				tcpPorts[i] = strconv.Itoa(int(port))
			}
			list = append(list, "tcp:"+strings.Join(tcpPorts, ","))
		}
		a.event(pb.Severity_SEVERITY_WARNING, "host_firewall_sync_failed", "", map[string]string{"error": errMsg, "ports": strings.Join(list, ", ")})
	}
}

func (a *Agent) clearHostFirewallSyncFailure() {
	if a.hostFirewallErr == "" {
		return
	}
	resolved := a.hostFirewallErr
	a.hostFirewallErr = ""
	a.event(pb.Severity_SEVERITY_INFO, "host_firewall_sync_recovered", "", map[string]string{"resolved_error": resolved})
}

// checkHop is hostctl.ValidateHop plus what only the agent knows: the ports of the node's other inbounds
// on the same network and the hops already accepted in this round (ids are visited in order, so the
// lower id wins an overlap and the higher one is refused).
func checkHop(h hostctl.Hop, accepted []hostctl.Hop, m *model, ssh []uint16) error {
	reserved := append([]uint16(nil), ssh...)
	for id, in := range m.inbounds {
		if id != h.InboundID && in.spec.Listen.Network == h.Network {
			reserved = append(reserved, in.spec.Listen.Port)
		}
	}
	if err := hostctl.ValidateHop(h, reserved); err != nil {
		return err
	}
	for _, o := range accepted {
		if o.Network == h.Network && h.From <= o.To && o.From <= h.To {
			return fmt.Errorf("hop range %d-%d overlaps the range of inbound %s", h.From, h.To, o.InboundID)
		}
	}
	return nil
}

// overlayHopRej puts the hop refusals onto the inbound results (the engine part of the inbound is fine,
// only its port-hop range is not installed).
func overlayHopRej(results []*pb.InboundResult, rej map[string]string) {
	for _, r := range results {
		if msg := rej[r.InboundId]; msg != "" && r.Error == "" {
			r.Error = msg
		}
	}
}

func hopsEqual(x, y []hostctl.Hop) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func (a *Agent) cachedResults(m *model) []*pb.InboundResult {
	var out []*pb.InboundResult
	for _, id := range m.ids() {
		if h := a.held[id]; h != nil {
			r := proto.Clone(h.res).(*pb.InboundResult)
			a.refreshState(r)
			out = append(out, r)
		}
	}
	overlayHopRej(out, a.hopRej)
	return out
}

// observedHash hashes what the engines ACTUALLY hold (Engine.Observed), with two deliberate additions so
// that intended behaviour is not reported as drift: credentials the agent withheld because their term
// ended (the panel still lists them until it recomputes), and inbounds that are disabled (an engine keeps
// nothing for them, the panel still lists them).
func (a *Agent) observedHash(m *model) string {
	now := a.now()
	var all []statehash.Inbound
	have := map[string]bool{}
	for _, p := range a.protocols {
		for _, o := range a.engines[p].Observed() {
			have[o.Spec.ID] = true
			if in := m.inbounds[o.Spec.ID]; in != nil {
				o.Creds = withExpired(o.Creds, in, now)
			}
			all = append(all, o)
		}
	}
	for id, in := range m.inbounds {
		if in.parseErr == "" && in.applyErr == "" && !in.spec.Enabled && !have[id] {
			all = append(all, statehash.Inbound{Spec: in.spec, Creds: in.sortedCreds()})
		}
	}
	return statehash.StateRelays(all, m.warp, m.relays)
}

func withExpired(held []plugin.UserCred, in *inbound, now time.Time) []plugin.UserCred {
	have := make(map[string]bool, len(held))
	for _, c := range held {
		have[c.CredID] = true
	}
	out := held
	copied := false
	for _, c := range in.sortedCreds() {
		if have[c.CredID] || c.ValidUntil.IsZero() || c.ValidUntil.After(now) {
			continue
		}
		if !copied {
			out = append([]plugin.UserCred(nil), held...)
			copied = true
		}
		out = append(out, c)
	}
	return out
}

// hash is the state hash of the model as the panel computes it.
func (m *model) hash() string {
	all := make([]statehash.Inbound, 0, len(m.inbounds))
	for _, id := range m.ids() {
		in := m.inbounds[id]
		if in.parseErr != "" {
			continue
		}
		all = append(all, statehash.Inbound{Spec: in.spec, Creds: in.sortedCreds()})
	}
	return statehash.StateRelays(all, m.warp, m.relays)
}
