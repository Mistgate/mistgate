package health

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/unit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Alert derivation. One evaluation computes, from the node states, the doctor rows and the check
// cells, the set of conditions that hold right now, and reconciles it with the active alerts: a condition without
// an alert opens one, an alert whose condition holds is touched, an alert whose condition is gone is resolved.
// Being a function of the current state, it is idempotent, survives a restart and needs no event bookkeeping.
// Conditions that cannot be judged (node offline, doctor report stale, the panel's own network suspect) hold the
// alerts they would decide instead of resolving them.

// Alert kinds as stored (the lower-case name of adminv1.AlertKind).
const (
	kNodeDown       = "node_down"
	kHostBlip       = "host_blip"
	kNoTraffic      = "no_traffic"
	kCheckFailed    = "check_failed"
	kDoctorWarn     = "doctor_warn"
	kDoctorFail     = "doctor_fail"
	kStateDrift     = "state_drift"
	kCertExpiry     = "cert_expiry"
	kAccessEnded    = "access_ended"
	kUserConnection = "user_connection"
	kUsersImpacted  = "users_impacted"
	kTorrent        = "torrent"
	kPortLossy      = "port_lossy"
	kDeviceLimit    = "device_limit"
	kRelayDown      = "relay_down"
)

var kindProto = map[string]adminv1.AlertKind{
	kNodeDown: adminv1.AlertKind_ALERT_KIND_NODE_DOWN, kHostBlip: adminv1.AlertKind_ALERT_KIND_HOST_BLIP,
	kNoTraffic: adminv1.AlertKind_ALERT_KIND_NO_TRAFFIC, kCheckFailed: adminv1.AlertKind_ALERT_KIND_CHECK_FAILED,
	kDoctorWarn: adminv1.AlertKind_ALERT_KIND_DOCTOR_WARN, kDoctorFail: adminv1.AlertKind_ALERT_KIND_DOCTOR_FAIL,
	kStateDrift: adminv1.AlertKind_ALERT_KIND_STATE_DRIFT, kCertExpiry: adminv1.AlertKind_ALERT_KIND_CERT_EXPIRY,
	"quota": adminv1.AlertKind_ALERT_KIND_QUOTA, "subscription_shared_suspect": adminv1.AlertKind_ALERT_KIND_SUBSCRIPTION_SHARED_SUSPECT,
	kUpdateFailed:   adminv1.AlertKind_ALERT_KIND_UPDATE_FAILED,
	kAccessEnded:    adminv1.AlertKind_ALERT_KIND_ACCESS_ENDED,
	kUserConnection: adminv1.AlertKind_ALERT_KIND_USER_CONNECTION,
	kUsersImpacted:  adminv1.AlertKind_ALERT_KIND_USERS_IMPACTED,
	kTorrent:        adminv1.AlertKind_ALERT_KIND_TORRENT,
	kPortLossy:      adminv1.AlertKind_ALERT_KIND_PORT_LOSSY,
	kDeviceLimit:    adminv1.AlertKind_ALERT_KIND_DEVICE_LIMIT,
	kCountryBlocked: adminv1.AlertKind_ALERT_KIND_COUNTRY_BLOCKED,
	kRelayDown:      adminv1.AlertKind_ALERT_KIND_RELAY_DOWN,
}

const (
	sevInfo     = 1
	sevWarning  = 2
	sevCritical = 3

	flapMin            = 3 // blips and agent restarts in an hour that make a node "flapping"
	panelEgressSubject = "panel_egress"
	panelLinkSubject   = "panel_link"
	panelProbeSubject  = "probe_path"
	certWarnBefore     = 14 * 24 * time.Hour
	certCritBefore     = 3 * 24 * time.Hour
)

type key struct{ kind, node, subject string }

// cond is a condition that holds: what the alert will say.
type cond struct {
	key
	severity   int
	why        string
	params     map[string]string
	fixID      string
	titleFixed string // "" = health.alert.<kind>.title
}

func (c cond) alert() store.HealthAlert {
	return store.HealthAlert{Kind: c.kind, Severity: c.severity, NodeID: c.node, Subject: c.subject, Params: c.params,
		TitleKey: titleKey(c.kind), WhyKey: c.why}
}

func titleKey(kind string) string { return "health.alert." + kind + ".title" }

// nodeHealth is what the node status needs (Service.NodeHealthSummaries).
type nodeHealth struct {
	noTraffic     bool
	failed, total int
	doctorFail    string
}

// derived is the outcome of looking at the world once.
type derived struct {
	conds      map[key]cond
	accepted   map[key]bool    // doctor warnings the owner accepted as normal: their alert ends "accepted"
	nodes      map[string]bool // nodes that exist (not retired)
	holdNode   map[string]bool // not connected, pending: only NODE_DOWN is decided for them
	holdDown   map[string]bool // not connected and inside the start grace: NODE_DOWN stays as it is
	holdDoctor map[string]bool // the doctor report is stale: doctor alerts stay as they are
	holdKeys   map[key]bool    // an alert cannot be judged yet: its comparison window is warming up, or the check result behind it is stale
	superseded map[key]bool    // an open alert is explained by a more specific current check alert
	guard      bool            // the panel's own network is suspect: no per-node synthetic alert is raised
	staleAfter time.Duration   // how old a check result may be and still count (the interval setting is read once per pass)
	health     map[string]nodeHealth
}

func (d *derived) holds(a store.HealthAlert) bool {
	if a.Kind == kRelayDown && d.superseded[key{a.Kind, a.NodeID, a.Subject}] {
		return false
	}
	if d.holdKeys[key{a.Kind, a.NodeID, a.Subject}] {
		return true
	}
	if a.NodeID == "" {
		return false
	}
	switch {
	case a.Kind == kUpdateFailed: // a fact about the rollout in the database, not about the node's link
		return false
	case a.Kind == kPortLossy: // latest stored verdict and inbound configuration remain judgeable while offline
		return false
	case a.Kind == kNodeDown:
		return d.holdDown[a.NodeID]
	case d.holdNode[a.NodeID]:
		return true
	case d.holdDoctor[a.NodeID] && (a.Kind == kDoctorWarn || a.Kind == kDoctorFail || a.Kind == kCertExpiry):
		return true
	}
	return false
}

func (s *Service) runEvaluator(ctx context.Context) {
	t := time.NewTicker(evalEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
			select { // a burst of rounds and reports is one pass
			case <-time.After(200 * time.Millisecond):
			case <-ctx.Done():
				return
			}
		}
		s.evaluate(ctx)
	}
}

// EvaluateOnce is the evaluator pass of the Cloudflare edition's cron tick (the VPS runs runEvaluator). The caller gives
// the pass its deadline. The pass is skipped, not waited for, while this isolate is still in one (a mutex held across D1
// I/O must never block the tick), and a D1 lease keeps two isolates from reconciling the same transitions twice.
func (s *Service) EvaluateOnce(ctx context.Context) {
	if !s.evalMu.TryLock() {
		return
	}
	defer s.evalMu.Unlock()
	deadline, ok := ctx.Deadline()
	if !ok {
		s.log.Warn("health: edge evaluator requires a deadline")
		return
	}
	ttl := time.Until(deadline)
	if ttl <= 0 || s.cfg.RoundLease == nil || !s.cfg.RoundLease(ctx, "health-eval", ttl) {
		return
	}
	s.evaluateLocked(ctx)
}

// evaluate runs one pass. Safe to call from anywhere; passes are serialised.
func (s *Service) evaluate(ctx context.Context) {
	s.evalMu.Lock()
	defer s.evalMu.Unlock()
	s.evaluateLocked(ctx)
}

// evaluateLocked is one pass; the caller holds evalMu.
func (s *Service) evaluateLocked(ctx context.Context) {
	now := s.now()
	sn, err := s.snapshot(ctx)
	if err != nil {
		s.log.Warn("health: evaluate", "err", err)
		return
	}
	var liveRows []store.NodeLiveRow
	var rows []store.DoctorRow
	var accepts []store.DoctorAccept
	var portChecks []store.PortCheck
	needFleetData := len(sn.nodes) > 0 || !s.cfg.Edge
	if needFleetData {
		liveRows, err = s.fl.Live(ctx)
		if err != nil {
			s.log.Warn("health: evaluate live", "err", err)
			return
		}
		rows, err = s.st.DoctorResults(ctx, "")
		if err != nil {
			s.log.Warn("health: evaluate", "err", err)
			return
		}
		accepts, err = s.st.DoctorAccepts(ctx, "")
		if err != nil {
			s.log.Warn("health: evaluate", "err", err)
			return
		}
	}
	signals, err := s.st.HealthSignals(ctx, now)
	if err != nil {
		s.log.Warn("health: evaluate", "err", err)
		return
	}
	if needFleetData {
		portChecks, err = s.st.PortChecks(ctx)
		if err != nil {
			s.log.Warn("health: evaluate", "err", err)
			return
		}
	}
	active, err := s.st.ActiveAlerts(ctx)
	if err != nil {
		s.log.Warn("health: evaluate", "err", err)
		return
	}
	d := s.derive(ctx, now, sn, indexLiveRows(liveRows), rows, acceptsByNode(accepts), signals, portChecks, active)
	s.addRelayDownConds(ctx, now, sn, signals.RelayLinks, active, d)
	s.nhMu.Lock()
	s.nodeHealth = d.health
	s.nhMu.Unlock()
	s.storeNodeHealth(ctx, d.health)
	if err := s.reconcile(ctx, now, d, active); err != nil {
		s.log.Warn("health: reconcile alerts", "err", err)
	}
}

func indexLiveRows(rows []store.NodeLiveRow) map[string]store.NodeLiveRow {
	out := make(map[string]store.NodeLiveRow, len(rows))
	for _, row := range rows {
		out[row.NodeID] = row
	}
	return out
}

// acceptsByNode indexes the accepted doctor warnings: node id -> check id -> acceptance.
func acceptsByNode(list []store.DoctorAccept) map[string]map[string]store.DoctorAccept {
	out := map[string]map[string]store.DoctorAccept{}
	for _, a := range list {
		if out[a.NodeID] == nil {
			out[a.NodeID] = map[string]store.DoctorAccept{}
		}
		out[a.NodeID][a.CheckID] = a
	}
	return out
}

// accepted reports whether a doctor row is a warning the owner accepted for exactly this detail code. An empty code
// (an older agent, or an acceptance stored before the panel refused those) names nothing and never matches.
func accepted(r store.DoctorRow, acc map[string]store.DoctorAccept) (store.DoctorAccept, bool) {
	a, ok := acc[r.CheckID]
	return a, ok && r.Status == int(agentv1.DoctorStatus_DOCTOR_STATUS_WARN) && a.DetailCode != "" && a.DetailCode == r.DetailCode
}

// derive computes the conditions that hold now.
func (s *Service) derive(ctx context.Context, now time.Time, sn *snapshot, live map[string]store.NodeLiveRow, rows []store.DoctorRow, accepts map[string]map[string]store.DoctorAccept,
	signals store.HealthSignalBatch, portChecks []store.PortCheck, active []store.HealthAlert) *derived {
	d := &derived{conds: map[key]cond{}, accepted: map[key]bool{}, nodes: map[string]bool{}, holdNode: map[string]bool{}, holdDown: map[string]bool{},
		holdDoctor: map[string]bool{}, holdKeys: map[key]bool{}, superseded: map[key]bool{}, health: map[string]nodeHealth{}}
	docs := map[string][]store.DoctorRow{}
	for _, r := range rows {
		docs[r.NodeID] = append(docs[r.NodeID], r)
	}
	add := func(c cond) { d.conds[c.key] = c }
	grace := !s.cfg.Edge && now.Sub(s.startedAt) < s.cfg.StartGrace
	staleAfter := 2*s.interval(ctx) + time.Minute
	d.staleAfter = staleAfter

	byNode := map[string][]probed{}
	var connected []store.NodeRow
	var downs []cond
	staleOn := map[string]bool{} // nodes with an inbound whose last result is older than the probe cadence allows
	downProviders := map[string]bool{}
	activeNodes := 0

	for _, n := range sn.nodes {
		d.nodes[n.ID] = true
		if n.State != "active" {
			d.holdNode[n.ID] = true
			continue
		}
		activeNodes++
		nodeLive := live[n.ID]
		if !nodeLive.Connected {
			d.holdNode[n.ID] = true
			gap := now.Sub(latest(nodeLive.LastSeenAt, n.LastSeenAt, n.LastDisconnectedAt, n.LastConnectedAt))
			switch {
			case gap < s.cfg.BlipWindow: // a blip is an event, not an alert
			case grace:
				d.holdDown[n.ID] = true
			default:
				downs = append(downs, cond{key: key{kNodeDown, n.ID, ""}, severity: sevCritical, why: "health.alert.node_down.why",
					params: map[string]string{"minutes": strconv.Itoa(int(gap.Minutes()))}})
				downProviders[providerKey(n)] = true
			}
			continue
		}
		connected = append(connected, n)
		if nodeLive.Drift {
			add(cond{key: key{kStateDrift, n.ID, ""}, severity: sevWarning, why: "health.alert.state_drift.why"})
		}
		for _, c := range s.doctorConds(now, n, docs[n.ID], sn.byNode[n.ID], d, accepts[n.ID]) {
			add(c)
		}
		warpDead := !d.holdDoctor[n.ID] && failsCheck(docs[n.ID], "warp_path")
		for _, t := range sn.byNode[n.ID] {
			if t.in.Enabled && t.in.State == "failed" { // the node could not start it: no round will ever say so
				if warpDead && t.err == nil && t.spec.Egress == "warp" {
					d.superseded[key{kCheckFailed, n.ID, t.in.ID}] = true // the doctor's warp_path alert says it
				} else {
					add(notStartedCond(sn.byNode[n.ID], t))
				}
			}
			if s.skipReason(t, live[t.node.ID]) != "" {
				continue
			}
			c := s.cellOf(ctx, t.in.ID)
			switch {
			case c.last == nil: // never probed: nothing to judge
			case now.Sub(c.last.At) > staleAfter:
				// an old result is not the answer of now: it holds its own alert, and the node's NO_TRAFFIC cannot be decided
				d.holdKeys[key{kCheckFailed, n.ID, t.in.ID}] = true
				d.holdKeys[key{kNoTraffic, n.ID, ""}] = true
				staleOn[n.ID] = true
			default:
				byNode[n.ID] = append(byNode[n.ID], probed{t, c})
			}
		}
	}

	// Most nodes of three providers silent at once: the panel lost its link, not the nodes (they keep serving people).
	if len(downProviders) >= 3 && len(downs)*5 >= activeNodes*4 {
		for _, c := range downs {
			d.holdDown[c.node] = true
		}
		add(cond{key: key{kNodeDown, "", panelLinkSubject}, severity: sevCritical, why: "health.alert.node_down.why.panel_link",
			params: map[string]string{"nodes": strconv.Itoa(len(downs)), "total": strconv.Itoa(activeNodes)}})
	} else {
		for _, c := range downs {
			add(c)
		}
	}

	// The panel's own network: the last round failed for most probed inbounds on nodes of three providers, so the
	// probe client is the suspect, not the nodes.
	var allProbed, allFailed, freshFailed int
	failProviders, freshProviders := map[string]bool{}, map[string]bool{}
	freshNodes := map[string]string{}
	for _, n := range connected {
		for _, p := range byNode[n.ID] {
			allProbed++
			if p.c.streak >= 1 {
				allFailed++
				failProviders[providerKey(n)] = true
			}
			if p.c.streak >= 1 && p.c.streak <= 3 {
				freshFailed++
				freshProviders[providerKey(n)] = true
				freshNodes[n.ID] = n.Name
			}
		}
	}
	if len(failProviders) >= 3 && allFailed*5 >= allProbed*4 {
		d.guard = true
		add(cond{key: key{kCheckFailed, "", panelEgressSubject}, severity: sevWarning, why: "health.alert.check_failed.why.panel_egress",
			params: map[string]string{"failed": strconv.Itoa(allFailed), "total": strconv.Itoa(allProbed), "providers": strconv.Itoa(len(failProviders))}})
	}
	// Two providers can share a short panel-path failure before the broad guard threshold is met.
	correlated := !d.guard && len(freshProviders) >= 2
	if correlated {
		var names []string
		for _, n := range connected {
			if name, ok := freshNodes[n.ID]; ok {
				if name == "" {
					name = n.ID
				}
				names = append(names, name)
			}
		}
		sort.Strings(names)
		if len(names) > 5 {
			names = append(names[:5], "+"+strconv.Itoa(len(names)-5))
		}
		add(cond{key: key{kCheckFailed, "", panelProbeSubject}, severity: sevWarning, why: "health.alert.check_failed.why.probe_path",
			params: map[string]string{"nodes": strings.Join(names, ", "), "providers": strconv.Itoa(len(freshProviders)),
				"failed": strconv.Itoa(freshFailed), "total": strconv.Itoa(allProbed)}})
	}

	for _, n := range connected {
		ps := byNode[n.ID]
		var nodeProbed, nodeFailing, nodeJudgedFailing []probed
		for _, p := range ps {
			if cdnHost(p.t) == "" {
				nodeProbed = append(nodeProbed, p)
			}
		}
		h := nodeHealth{total: len(nodeProbed), doctorFail: worstDoctorFail(docs[n.ID], d.holdDoctor[n.ID])}
		var failing, judgedFailing []probed
		for _, p := range ps {
			fresh := correlated && p.c.streak >= 1 && p.c.streak <= 3
			if fresh {
				d.holdKeys[key{kCheckFailed, n.ID, p.t.in.ID}] = true
			}
			if p.c.streak >= failStreakMin {
				failing = append(failing, p)
				if !fresh {
					judgedFailing = append(judgedFailing, p)
				}
				if cdnHost(p.t) == "" {
					nodeFailing = append(nodeFailing, p)
					if !fresh {
						nodeJudgedFailing = append(nodeJudgedFailing, p)
					}
				}
			}
		}
		h.failed = len(nodeFailing)
		if correlated {
			for _, p := range ps {
				if cdnHost(p.t) == "" && p.c.streak >= 1 && p.c.streak <= 3 {
					d.holdKeys[key{kNoTraffic, n.ID, ""}] = true
					break
				}
			}
		}
		if !d.guard && len(judgedFailing) > 0 {
			if len(nodeProbed) > 0 && len(nodeJudgedFailing) == len(nodeProbed) && !staleOn[n.ID] {
				h.noTraffic = true
				params := map[string]string{"failed": strconv.Itoa(h.failed), "total": strconv.Itoa(h.total)}
				ports := failedPorts(nodeJudgedFailing)
				params["ports"] = strings.Join(ports, ", ")
				if len(ports) == 1 {
					params["port"] = ports[0]
				}
				add(cond{key: key{kNoTraffic, n.ID, ""}, severity: sevCritical, why: "health.alert.no_traffic.why." + allFailVariant(nodeJudgedFailing), params: params})
			} else {
				// the doctor already says the node's WARP is dead: its WARP profiles failing is the same news, said once
				warpDead := !d.holdDoctor[n.ID] && failsCheck(docs[n.ID], "warp_path")
				for _, f := range judgedFailing {
					variant := whyVariant(f, ps)
					cdn := cdnHost(f.t)
					if cdn != "" {
						variant = "via_cdn"
					}
					if variant == "warp_path" && warpDead {
						continue
					}
					sev := sevWarning
					if sameProtocol(sn.byNode[n.ID], f.t) == 1 {
						sev = sevCritical // the node's only inbound of that protocol
					}
					code := f.c.last.ErrorCode
					params := map[string]string{"inbound": f.t.in.ID, "profile": f.t.in.ProfileName, "port": strconv.Itoa(int(f.t.spec.Listen.Port)),
						"error_code": code, "error_detail": f.c.last.ErrorDetail}
					if cdn != "" {
						params["cdn_host"] = cdn
					}
					add(cond{key: key{kCheckFailed, n.ID, f.t.in.ID}, severity: sev,
						why:    "health.alert.check_failed.why." + variant,
						params: params})
				}
			}
		}
		d.health[n.ID] = h
	}
	// A node that keeps dropping, or whose agent keeps restarting, is never silent long enough for NODE_DOWN: three events
	// in the last hour are one warning. Its own subject keeps the quiet blip records of the history apart from it.
	nodeActive := map[string]bool{}
	for _, n := range sn.nodes {
		nodeActive[n.ID] = n.State == "active"
	}
	for _, f := range signals.NodeFlaps {
		if f.Count >= flapMin && nodeActive[f.NodeID] {
			add(cond{key: key{kHostBlip, f.NodeID, "flapping"}, severity: sevWarning, why: "health.alert.host_blip.why.flapping",
				params: map[string]string{"count": strconv.Itoa(f.Count)}})
		}
	}
	for _, c := range s.extConds(ctx) { // e.g. a paused rollout (update_failed)
		add(cond{key: key{c.Kind, c.NodeID, c.Subject}, severity: c.Severity, why: c.Why, params: c.Params})
	}
	addPortLossyConds(sn, portChecks, add)
	activeKeys := make(map[key]bool, len(active))
	activeAlerts := make(map[key]store.HealthAlert, len(active))
	for _, alert := range active {
		k := key{alert.Kind, alert.NodeID, alert.Subject}
		activeKeys[k] = true
		activeAlerts[k] = alert
	}
	userConds(now, sn, signals, activeKeys, d, add)
	s.addCountryConds(ctx, sn, live, activeAlerts, d, add)
	return d
}

// notStartedCond is the alert of a profile the node could not start (inbound state failed): critical when it was the
// node's only profile of its protocol.
func notStartedCond(ts []*target, t *target) cond {
	sev := sevWarning
	if sameProtocol(ts, t) == 1 {
		sev = sevCritical
	}
	params := map[string]string{"inbound": t.in.ID, "profile": t.in.ProfileName, "error_detail": t.in.LastError}
	if t.err == nil {
		params["port"] = strconv.Itoa(int(t.spec.Listen.Port))
	}
	return cond{key: key{kCheckFailed, t.node.ID, t.in.ID}, severity: sev, why: "health.alert.check_failed.why.not_started", params: params}
}

func cdnHost(t *target) string {
	if t == nil || t.in.Protocol != "vless" {
		return ""
	}
	var settings struct {
		Security string `json:"security"`
		CDN      struct {
			Host string `json:"host"`
		} `json:"cdn"`
	}
	if json.Unmarshal(t.settings, &settings) != nil || settings.Security != "tls" {
		return ""
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(settings.CDN.Host), "."))
}

// addPortLossyConds derives a port alert from each enabled inbound's current port and its latest stored verdict.
// check_failed remains active alongside this alert: it describes a client-eye failure, while this check measures
// whether tagged UDP packets arrived at the node.
func addPortLossyConds(sn *snapshot, checks []store.PortCheck, add func(cond)) {
	type nodePort struct {
		node string
		port uint16
	}
	latest := make(map[nodePort]store.PortCheck, len(checks))
	for _, check := range checks {
		latest[nodePort{node: check.NodeID, port: check.Port}] = check
	}
	names := make(map[string]string, len(sn.nodes))
	for _, node := range sn.nodes {
		names[node.ID] = node.Name
	}
	for _, node := range sn.nodes {
		for _, inbound := range sn.byNode[node.ID] {
			if !inbound.in.Enabled || inbound.err != nil {
				continue
			}
			port := uint16(inbound.spec.Listen.Port)
			check, ok := latest[nodePort{node: node.ID, port: port}]
			if !ok || (check.Verdict != "lossy" && check.Verdict != "broken") {
				continue
			}
			sender := check.Sender
			if name := names[sender]; name != "" {
				sender = name
			}
			add(cond{key: key{kPortLossy, node.ID, inbound.in.ID}, severity: sevWarning,
				why: "health.alert.port_lossy.why", params: map[string]string{
					"profile": inbound.in.ProfileName, "port": strconv.Itoa(int(port)),
					"sent": strconv.FormatUint(uint64(check.Sent), 10), "got": strconv.FormatUint(uint64(check.Got), 10),
					"sender": sender, "checked_unix": strconv.FormatInt(check.CheckedAt.Unix(), 10),
				}})
		}
	}
}

// probed is an inbound that has been probed at least once, with its cell.
type probed struct {
	t *target
	c cell
}

func (p probed) passing() bool { return p.c.streak < failStreakMin }

// whyVariant is the diagnosis of one failing inbound among the probed inbounds of its node.
func whyVariant(f probed, all []probed) string {
	code := f.c.last.ErrorCode
	switch code {
	case "timeout":
		if !udpProbeProtocol(f.t.spec.Protocol) {
			break
		}
		for _, o := range all {
			if o.t != f.t && o.passing() && o.t.spec.Listen.Port != f.t.spec.Listen.Port {
				return "udp_blocked" // another inbound of the node answers on another port: the hoster cuts this one
			}
		}
	case "exit_unreachable", "http_status":
		if f.t.spec.Egress == "warp" {
			for _, o := range all {
				if o.t != f.t && o.passing() && o.t.spec.Protocol == f.t.spec.Protocol && o.t.spec.Egress != "warp" {
					return "warp_path" // the direct path of the same protocol is fine, the WARP exit is dead
				}
			}
		}
	}
	if code == "" {
		return "unknown"
	}
	return code
}

// allFailVariant is the diagnosis of a node whose every inbound fails: one shared code names it. A timeout only points
// to blocked UDP when all failed inbounds use a UDP protocol; TCP timeouts have a different cause.
func allFailVariant(failing []probed) string {
	first := failing[0].c.last.ErrorCode
	for _, f := range failing[1:] {
		if f.c.last.ErrorCode != first {
			return "mixed"
		}
	}
	switch first {
	case "":
		return "unknown"
	case "timeout":
		for _, f := range failing {
			if !udpProbeProtocol(f.t.spec.Protocol) {
				return "unknown"
			}
		}
		if ports := failedPorts(failing); len(ports) > 1 || slices.Contains(ports, "443") {
			return "udp_all_blocked"
		}
		return "udp_blocked"
	}
	return first
}

func udpProbeProtocol(protocol string) bool {
	return protocol == "hysteria2" || protocol == "awg"
}

// failedPorts are the distinct listen ports of the failing inbounds, in order.
func failedPorts(failing []probed) []string {
	var nums []int
	for _, f := range failing {
		if p := int(f.t.spec.Listen.Port); p != 0 && !slices.Contains(nums, p) {
			nums = append(nums, p)
		}
	}
	slices.Sort(nums)
	out := make([]string, len(nums))
	for i, p := range nums {
		out[i] = strconv.Itoa(p)
	}
	return out
}

// failsCheck reports whether a node's doctor rows hold a FAIL of the check.
func failsCheck(rows []store.DoctorRow, check string) bool {
	return slices.ContainsFunc(rows, func(r store.DoctorRow) bool {
		return r.CheckID == check && r.Status == int(agentv1.DoctorStatus_DOCTOR_STATUS_FAIL)
	})
}

func sameProtocol(ts []*target, t *target) int {
	n := 0
	for _, o := range ts {
		if o.in.Enabled && o.in.Protocol == t.in.Protocol {
			n++
		}
	}
	return n
}

func providerKey(n store.NodeRow) string {
	if n.Provider == "" {
		return "?" + n.ID // unknown provider: never counted together with another node
	}
	return strings.ToLower(n.Provider)
}

func latest(ts ...time.Time) time.Time {
	var m time.Time
	for _, t := range ts {
		if t.After(m) {
			m = t
		}
	}
	return m
}

// checkOrder is the fixed order of agent.proto "CHECK IDS"; unknown ids go after these, by name.
var checkOrder = []string{"disk_space", "journald_size", "dstate_tasks", "time_sync", "resolver", "ipv6", "foreign_vpn", "foreign_nft",
	"port_conflicts", "net_baseline", "cert_expiry", "memory_pressure", "cpu_softirq", "kernel_headers", "awg_backend", "warp_path", "unit_generation"}

func checkRank(id string) int {
	for i, c := range checkOrder {
		if c == id {
			return i
		}
	}
	return len(checkOrder)
}

func sortDoctor(rows []store.DoctorRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := checkRank(rows[i].CheckID), checkRank(rows[j].CheckID)
		if a != b {
			return a < b
		}
		return rows[i].CheckID < rows[j].CheckID
	})
}

func worstDoctorFail(rows []store.DoctorRow, stale bool) string {
	if stale {
		return ""
	}
	sortDoctor(rows)
	for _, r := range rows {
		if r.Status == int(agentv1.DoctorStatus_DOCTOR_STATUS_FAIL) {
			return r.CheckID
		}
	}
	return ""
}

// doctorWhyKey is the panel-side explanation of a doctor row: "health.doctor.<id>.why", with the agent's hint as a
// variant ("health.doctor.dstate_tasks.why.qxl_ttm").
func doctorWhyKey(r store.DoctorRow) string {
	k := "health.doctor." + r.CheckID + ".why"
	if h := r.Params["hint"]; h != "" {
		k += "." + h
	}
	return k
}

// withNames is a copy of a doctor row's params with the profile names next to the inbound ids the agent sent (the UI
// names profiles, never inb_ ids): "profile" next to inbound_id, "profiles" next to inbounds, "warp_profiles" next to
// warp_inbounds. A warp_path row without inbounds gets the node's enabled WARP-exit profiles as "profiles" (the ones
// that go down with WARP). ts are the node's inbounds.
func withNames(checkID string, params map[string]string, ts []*target) map[string]string {
	name := map[string]string{}
	for _, t := range ts {
		name[t.in.ID] = t.in.ProfileName
	}
	names := func(ids string) string {
		var out []string
		for _, id := range strings.Split(ids, ",") {
			if n := name[strings.TrimSpace(id)]; n != "" {
				out = append(out, n)
			}
		}
		return strings.Join(out, ", ")
	}
	out := make(map[string]string, len(params)+2)
	maps.Copy(out, params)
	if n := name[params["inbound_id"]]; n != "" {
		out["profile"] = n
	}
	if v := names(params["inbounds"]); v != "" {
		out["profiles"] = v
	}
	if v := names(params["warp_inbounds"]); v != "" {
		out["warp_profiles"] = v
	}
	if checkID == "warp_path" && out["profiles"] == "" {
		var warp []string
		for _, t := range ts {
			if t.in.Enabled && t.err == nil && t.spec.Egress == "warp" {
				warp = append(warp, t.in.ProfileName)
			}
		}
		if len(warp) > 0 {
			out["profiles"] = strings.Join(warp, ", ")
		}
	}
	return out
}

func unitGenerationNeedsWarning(rows []store.DoctorRow, unitRow store.DoctorRow) bool {
	gen, err := strconv.Atoi(unitRow.Params["unit_gen"])
	if err != nil || gen < 2 {
		return true
	}
	if gen >= unit.Generation {
		return false
	}
	for _, r := range rows {
		switch r.CheckID {
		case "awg_backend":
			if r.Params["backend"] == "userspace" || r.Params["mode"] == "userspace" ||
				r.Params["hint"] == "unit_outdated" || r.Params["hint"] == "no_tun" ||
				strings.Contains(strings.ToLower(r.Detail+" "+r.Params["reason"]), "/dev/net/tun") {
				return true
			}
		case "warp_path":
			if r.Params["backend"] == "userspace" || strings.Contains(strings.ToLower(r.Detail+" "+r.Params["reason"]), "/dev/net/tun") {
				return true
			}
		}
	}
	return false
}

// transientSkip are the doctor's SKIP codes that mean "not now", not "nothing to check": they hold the check's alert
// (internal/node/doctor/codes.go: CodeCPUCollecting, CodeDstateCut, CodeSkipTimeout, CodeSkipInternal; the panel does not
// import the node's package).
var transientSkip = map[string]bool{"cpu_softirq.collecting": true, "dstate_tasks.interrupted": true, "skip.timeout": true, "skip.internal": true}

// certWhy is the reason of a certificate alert about one profile: it names the profile and its domain (the agent's
// own certificate keeps the doctor's general text).
func certWhy(reason string) string {
	switch reason {
	case "expired":
		return "health.alert.cert_expiry.why.expired"
	case "san_mismatch":
		return "health.alert.cert_expiry.why.san_mismatch"
	}
	return "health.alert.cert_expiry.why"
}

// doctorConds turns the stored doctor rows of a connected node into conditions (debounce = the report
// itself; cert_expiry raises CERT_EXPIRY and never DOCTOR_*), and adds the panel's own view of the certificates
// for an agent that has no doctor. A warning the owner accepted for its detail code raises nothing.
func (s *Service) doctorConds(now time.Time, n store.NodeRow, rows []store.DoctorRow, ts []*target, d *derived, acc map[string]store.DoctorAccept) []cond {
	var out []cond
	var received time.Time
	hasCert := false
	for _, r := range rows {
		received = latest(received, r.Received)
		hasCert = hasCert || r.CheckID == "cert_expiry"
	}
	if len(rows) > 0 && now.Sub(received) > doctorStale {
		d.holdDoctor[n.ID] = true
		rows = nil
	}
	hasAWG := false
	for _, t := range ts {
		hasAWG = hasAWG || strings.Contains(t.in.Protocol, "awg") || strings.Contains(t.in.Protocol, "amnezia")
	}
	for _, r := range rows {
		if r.Status == int(agentv1.DoctorStatus_DOCTOR_STATUS_SKIP) && transientSkip[r.DetailCode] { // could not judge now: keep what was said
			d.holdKeys[key{kDoctorWarn, n.ID, r.CheckID}] = true
			d.holdKeys[key{kDoctorFail, n.ID, r.CheckID}] = true
		}
		if r.Status != int(agentv1.DoctorStatus_DOCTOR_STATUS_WARN) && r.Status != int(agentv1.DoctorStatus_DOCTOR_STATUS_FAIL) {
			continue
		}
		if _, ok := accepted(r, acc); ok {
			d.accepted[key{kDoctorWarn, n.ID, r.CheckID}] = true
			continue
		}
		fail := r.Status == int(agentv1.DoctorStatus_DOCTOR_STATUS_FAIL)
		params := withNames(r.CheckID, r.Params, ts)
		params["check"] = r.CheckID
		if r.DetailCode != "" {
			params["detail_code"] = r.DetailCode
		}
		if r.Detail != "" {
			params["detail"] = r.Detail
		}
		fix := knownFix(r.FixID)
		if fix != "" {
			params["fix_id"] = fix
		}
		c := cond{params: params, why: doctorWhyKey(r), fixID: fix}
		switch {
		case r.CheckID == "cert_expiry":
			subject := r.Params["inbound_id"]
			if subject == "" {
				subject = "cert"
			} else {
				c.why = certWhy(r.Params["reason"])
			}
			c.key, c.severity = key{kCertExpiry, n.ID, subject}, sevWarning
			if fail {
				c.severity = sevCritical
			}
		case fail:
			c.key, c.severity = key{kDoctorFail, n.ID, r.CheckID}, sevCritical
		default:
			c.key, c.severity = key{kDoctorWarn, n.ID, r.CheckID}, sevWarning
			if r.CheckID == "kernel_headers" && !hasAWG {
				c.severity = sevInfo // nothing on this node needs the AmneziaWG module
			}
			if r.CheckID == "unit_generation" && !unitGenerationNeedsWarning(rows, r) {
				c.severity = sevInfo // gen 2 remains safe without userspace tunnels or a /dev/net/tun failure
			}
		}
		out = append(out, c)
	}
	if !hasCert {
		for _, t := range ts {
			if !t.in.Enabled || t.in.State != "active" || t.in.CertNotAfter.IsZero() {
				continue
			}
			left := t.in.CertNotAfter.Sub(now)
			if left >= certWarnBefore {
				continue
			}
			c := cond{key: key{kCertExpiry, n.ID, t.in.ID}, severity: sevWarning, why: "health.alert.cert_expiry.why",
				params: map[string]string{"inbound": t.in.ID, "profile": t.in.ProfileName, "server_name": t.spec.TLS.ServerName,
					"days_left": strconv.Itoa(max(int(left/(24*time.Hour)), 0))}}
			if left < certCritBefore {
				c.severity = sevCritical
			}
			if left <= 0 {
				c.why += ".expired"
			}
			out = append(out, c)
		}
	}
	return out
}

// reconcile applies derived conditions to the stored alerts.
func (s *Service) reconcile(ctx context.Context, now time.Time, d *derived, active []store.HealthAlert) error {
	have := map[key]bool{}
	for _, a := range active {
		k := key{a.Kind, a.NodeID, a.Subject}
		if a.Kind == kPortLossy {
			if c, ok := d.conds[k]; ok && a.Params["port"] != c.params["port"] {
				if err := s.resolve(ctx, a, "cleared", now); err != nil {
					return err
				}
				continue
			}
		}
		have[k] = true
		if c, ok := d.conds[k]; ok {
			refreshAfter := time.Second
			if a.Kind == kAccessEnded || a.Kind == kUserConnection || a.Kind == kUsersImpacted || a.Kind == kTorrent || a.Kind == kDeviceLimit {
				refreshAfter = userAlertRefreshInterval
			}
			// A torrent or device-limit alert's params (count, last attempt) move with every attempt: they are written with
			// the 5-minute refresh, not on every evaluation. Its severity and text never change.
			if (changed(a, c) && a.Kind != kTorrent && a.Kind != kDeviceLimit) || now.Sub(a.LastSeen) >= refreshAfter {
				na := c.alert()
				na.ID = a.ID
				if err := s.st.TouchAlert(ctx, na, now); err != nil {
					return err
				}
			}
			continue
		}
		if a.NodeID != "" && !d.nodes[a.NodeID] {
			if err := s.resolve(ctx, a, "node_retired", now); err != nil {
				return err
			}
			continue
		}
		if d.holds(a) {
			continue
		}
		if err := s.resolve(ctx, a, s.resolution(a, d, now), now); err != nil {
			return err
		}
	}
	keys := make([]key, 0, len(d.conds))
	for k := range d.conds {
		if !have[k] {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].kind+keys[i].node+keys[i].subject < keys[j].kind+keys[j].node+keys[j].subject
	})
	for _, k := range keys {
		a, _, err := s.st.OpenAlert(ctx, d.conds[k].alert(), reopenWindow, now)
		if err != nil {
			return err
		}
		s.transition(a, false)
		s.alertEvent(ctx, a, now)
	}
	return nil
}

// alertEvent puts a traffic alert on the node's history: when the traffic stopped and when it came back, with how
// long it lasted, so the node's Events tab answers "what happened last night". node_down has its own events from the
// fleet; an alert that a bigger one replaced ended nothing for the people on the node.
func (s *Service) alertEvent(ctx context.Context, a store.HealthAlert, now time.Time) {
	resolved := !a.ResolvedAt.IsZero()
	if a.NodeID == "" || (a.Kind != kNoTraffic && a.Kind != kCheckFailed) || (resolved && a.Resolution != "cleared" && a.Resolution != "fix_applied") {
		return
	}
	minutes := strconv.Itoa(int(now.Sub(a.OpenedAt).Minutes()))
	e := store.EventRow{Time: now, Source: "panel", NodeID: a.NodeID}
	switch {
	case a.Kind == kNoTraffic && !resolved:
		e.Code, e.Severity, e.Params = "traffic_stopped", 3, map[string]string{"failed": a.Params["failed"], "total": a.Params["total"]}
	case a.Kind == kNoTraffic:
		e.Code, e.Severity, e.Params = "traffic_resumed", 1, map[string]string{"minutes": minutes}
	case !resolved:
		e.Code, e.Severity, e.Params = "check_failing", 2, map[string]string{"profile": a.Params["profile"]}
	default:
		e.Code, e.Severity, e.Params = "check_recovered", 1, map[string]string{"profile": a.Params["profile"], "minutes": minutes}
	}
	if a.Kind == kCheckFailed {
		e.InboundID = a.Subject
	}
	if err := s.st.InsertEvent(ctx, e); err != nil {
		s.log.Warn("health: alert event", "node", a.NodeID, "code", e.Code, "err", err)
	}
}

func changed(a store.HealthAlert, c cond) bool {
	if a.Severity != c.severity || a.WhyKey != c.why || len(a.Params) != len(c.params) {
		return true
	}
	for k, v := range c.params {
		if a.Params[k] != v {
			return true
		}
	}
	return false
}

func (s *Service) resolve(ctx context.Context, a store.HealthAlert, resolution string, now time.Time) error {
	ok, err := s.st.ResolveAlert(ctx, a.ID, resolution, now)
	if err != nil || !ok {
		return err
	}
	a.ResolvedAt, a.Resolution = now, resolution
	s.transition(a, true)
	s.alertEvent(ctx, a, now)
	return nil
}

// resolution says how an alert ended: replaced by a worse one, node_returned, accepted, fix_applied or simply cleared.
func (s *Service) resolution(a store.HealthAlert, d *derived, now time.Time) string {
	_, worse := d.conds[key{kDoctorFail, a.NodeID, a.Subject}]
	_, silent := d.conds[key{kNoTraffic, a.NodeID, ""}]
	_, warpDead := d.conds[key{kDoctorFail, a.NodeID, "warp_path"}]
	switch {
	case d.superseded[key{a.Kind, a.NodeID, a.Subject}]:
		return "superseded"
	case a.Kind == kNodeDown:
		return "node_returned"
	case a.Kind == kDoctorWarn && worse, a.Kind == kCheckFailed && silent,
		a.Kind == kCheckFailed && warpDead && strings.HasSuffix(a.WhyKey, ".warp_path"), // the doctor's WARP alert says it
		d.guard && a.NodeID != "" && (a.Kind == kCheckFailed || a.Kind == kNoTraffic),   // the panel is blind, not the node
		// nothing on the node is checked any more (its inbounds failed, were turned off or removed): that ended the
		// alert, not traffic coming back, so no traffic_resumed
		a.Kind == kNoTraffic && d.health[a.NodeID].total == 0:

		return "superseded"
	case d.accepted[key{a.Kind, a.NodeID, a.Subject}]:
		return "accepted"
	}
	if fix := a.Params["fix_id"]; fix != "" {
		s.fixMu.Lock()
		at, ok := s.recentFix[a.NodeID+"/"+fix]
		s.fixMu.Unlock()
		if ok && now.Sub(at) <= fixResolveFor {
			return "fix_applied"
		}
	}
	return "cleared"
}

func (s *Service) transition(a store.HealthAlert, resolved bool) {
	if s.cfg.OnTransition != nil {
		s.cfg.OnTransition(Transition{Alert: a, Resolved: resolved})
	}
}
