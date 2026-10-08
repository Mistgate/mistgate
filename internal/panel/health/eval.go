package health

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
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
}

const (
	sevInfo     = 1
	sevWarning  = 2
	sevCritical = 3

	panelEgressSubject = "panel_egress"
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

// nodeHealth is what the node status needs (fleet.Health.NodeHealth).
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
	holdSynth  map[string]bool // a check result of the node is stale (no round since it came back): check alerts stay
	holdKeys   map[key]bool    // an alert's comparison window is still warming up
	superseded map[key]bool    // an open alert is explained by a more specific current check alert
	guard      bool            // the panel's own network is suspect: no per-node synthetic alert is raised
	health     map[string]nodeHealth
}

func (d *derived) holds(a store.HealthAlert) bool {
	if d.holdKeys[key{a.Kind, a.NodeID, a.Subject}] {
		return true
	}
	if a.NodeID == "" {
		return false
	}
	switch {
	case a.Kind == kUpdateFailed: // a fact about the rollout in the database, not about the node's link
		return false
	case a.Kind == kNodeDown:
		return d.holdDown[a.NodeID]
	case d.holdNode[a.NodeID]:
		return true
	case d.holdSynth[a.NodeID] && (a.Kind == kCheckFailed || a.Kind == kNoTraffic):
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

// evaluate runs one pass. Safe to call from anywhere; passes are serialised.
func (s *Service) evaluate(ctx context.Context) {
	s.evalMu.Lock()
	defer s.evalMu.Unlock()
	now := s.now()
	sn, err := s.snapshot(ctx)
	if err != nil {
		s.log.Warn("health: evaluate", "err", err)
		return
	}
	rows, err := s.st.DoctorResults(ctx, "")
	if err != nil {
		s.log.Warn("health: evaluate", "err", err)
		return
	}
	accepts, err := s.st.DoctorAccepts(ctx, "")
	if err != nil {
		s.log.Warn("health: evaluate", "err", err)
		return
	}
	signals, err := s.st.HealthSignals(ctx, now)
	if err != nil {
		s.log.Warn("health: evaluate", "err", err)
		return
	}
	active, err := s.st.ActiveAlerts(ctx)
	if err != nil {
		s.log.Warn("health: evaluate", "err", err)
		return
	}
	d := s.derive(ctx, now, sn, rows, acceptsByNode(accepts), signals, active)
	s.nhMu.Lock()
	s.nodeHealth = d.health
	s.nhMu.Unlock()
	if err := s.reconcile(ctx, now, d, active); err != nil {
		s.log.Warn("health: reconcile alerts", "err", err)
	}
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
func (s *Service) derive(ctx context.Context, now time.Time, sn *snapshot, rows []store.DoctorRow, accepts map[string]map[string]store.DoctorAccept,
	signals store.HealthSignalBatch, active []store.HealthAlert) *derived {
	d := &derived{conds: map[key]cond{}, accepted: map[key]bool{}, nodes: map[string]bool{}, holdNode: map[string]bool{}, holdDown: map[string]bool{},
		holdDoctor: map[string]bool{}, holdSynth: map[string]bool{}, holdKeys: map[key]bool{}, superseded: map[key]bool{}, health: map[string]nodeHealth{}}
	docs := map[string][]store.DoctorRow{}
	for _, r := range rows {
		docs[r.NodeID] = append(docs[r.NodeID], r)
	}
	add := func(c cond) { d.conds[c.key] = c }
	grace := now.Sub(s.startedAt) < s.cfg.StartGrace
	staleAfter := 2*s.interval(ctx) + time.Minute

	byNode := map[string][]probed{}
	var connected []store.NodeRow

	for _, n := range sn.nodes {
		d.nodes[n.ID] = true
		if n.State != "active" {
			d.holdNode[n.ID] = true
			continue
		}
		up, _, drift := s.fl.Live(n.ID)
		if !up {
			d.holdNode[n.ID] = true
			gap := now.Sub(latest(n.LastSeenAt, n.LastDisconnectedAt, n.LastConnectedAt))
			switch {
			case gap < s.cfg.BlipWindow: // a blip is an event, not an alert
			case grace:
				d.holdDown[n.ID] = true
			default:
				add(cond{key: key{kNodeDown, n.ID, ""}, severity: sevCritical, why: "health.alert.node_down.why",
					params: map[string]string{"minutes": strconv.Itoa(int(gap.Minutes()))}})
			}
			continue
		}
		connected = append(connected, n)
		if drift {
			add(cond{key: key{kStateDrift, n.ID, ""}, severity: sevWarning, why: "health.alert.state_drift.why"})
		}
		for _, c := range s.doctorConds(now, n, docs[n.ID], sn.byNode[n.ID], d, accepts[n.ID]) {
			add(c)
		}
		for _, t := range sn.byNode[n.ID] {
			if s.skipReason(t) != "" {
				continue
			}
			c := s.cellOf(ctx, t.in.ID)
			switch {
			case c.last == nil: // never probed: nothing to judge
			case now.Sub(c.last.At) > staleAfter:
				d.holdSynth[n.ID] = true // an old result from before the node was away is not the answer of now
			default:
				byNode[n.ID] = append(byNode[n.ID], probed{t, c})
			}
		}
	}

	// The panel's own network: the last round failed for most probed inbounds on nodes of three providers, so the
	// probe client is the suspect, not the nodes.
	var allProbed, allFailed int
	failProviders := map[string]bool{}
	for _, n := range connected {
		for _, p := range byNode[n.ID] {
			allProbed++
			if p.c.streak >= 1 {
				allFailed++
				failProviders[providerKey(n)] = true
			}
		}
	}
	if len(failProviders) >= 3 && allFailed*5 >= allProbed*4 {
		d.guard = true
		add(cond{key: key{kCheckFailed, "", panelEgressSubject}, severity: sevWarning, why: "health.alert.check_failed.why.panel_egress",
			params: map[string]string{"failed": strconv.Itoa(allFailed), "total": strconv.Itoa(allProbed), "providers": strconv.Itoa(len(failProviders))}})
	}

	for _, n := range connected {
		ps := byNode[n.ID]
		h := nodeHealth{total: len(ps), doctorFail: worstDoctorFail(docs[n.ID], d.holdDoctor[n.ID])}
		var failing []probed
		for _, p := range ps {
			if p.c.streak >= failStreakMin {
				failing = append(failing, p)
			}
		}
		h.failed = len(failing)
		if !d.guard && len(failing) > 0 {
			if len(failing) == len(ps) {
				h.noTraffic = true
				params := map[string]string{"failed": strconv.Itoa(h.failed), "total": strconv.Itoa(h.total)}
				ports := failedPorts(failing)
				params["ports"] = strings.Join(ports, ", ")
				if len(ports) == 1 {
					params["port"] = ports[0]
				}
				add(cond{key: key{kNoTraffic, n.ID, ""}, severity: sevCritical, why: "health.alert.no_traffic.why." + allFailVariant(failing), params: params})
			} else {
				// the doctor already says the node's WARP is dead: its WARP profiles failing is the same news, said once
				warpDead := !d.holdDoctor[n.ID] && failsCheck(docs[n.ID], "warp_path")
				for _, f := range failing {
					variant := whyVariant(f, ps)
					if variant == "warp_path" && warpDead {
						continue
					}
					sev := sevWarning
					if sameProtocol(sn.byNode[n.ID], f.t) == 1 {
						sev = sevCritical // the node's only inbound of that protocol
					}
					code := f.c.last.ErrorCode
					add(cond{key: key{kCheckFailed, n.ID, f.t.in.ID}, severity: sev,
						why: "health.alert.check_failed.why." + variant,
						params: map[string]string{"inbound": f.t.in.ID, "profile": f.t.in.ProfileName, "port": strconv.Itoa(int(f.t.spec.Listen.Port)),
							"error_code": code, "error_detail": f.c.last.ErrorDetail}})
				}
			}
		}
		d.health[n.ID] = h
	}
	for _, c := range s.extConds(ctx) { // e.g. a paused rollout (update_failed)
		add(cond{key: key{c.Kind, c.NodeID, c.Subject}, severity: c.Severity, why: c.Why, params: c.Params})
	}
	activeKeys := make(map[key]bool, len(active))
	for _, alert := range active {
		activeKeys[key{alert.Kind, alert.NodeID, alert.Subject}] = true
	}
	userConds(now, sn, signals, activeKeys, d, add)
	return d
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

// allFailVariant is the diagnosis of a node whose every inbound fails: one shared code names it. A shared timeout
// while the agent link (TCP) is up is the hoster cutting UDP: all of it when 443 is among the dead ports or two or
// more ports die (moving to "another port" cannot help then), else the one port (udp_blocked: change it on this node).
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
		if ports := failedPorts(failing); len(ports) > 1 || slices.Contains(ports, "443") {
			return "udp_all_blocked"
		}
		return "udp_blocked"
	}
	return first
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
	"port_conflicts", "net_baseline", "cert_expiry", "memory_pressure", "cpu_softirq", "kernel_headers", "awg_backend", "warp_path"}

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
		have[k] = true
		if c, ok := d.conds[k]; ok {
			refreshAfter := time.Second
			if a.Kind == kAccessEnded || a.Kind == kUserConnection || a.Kind == kUsersImpacted {
				refreshAfter = userAlertRefreshInterval
			}
			if changed(a, c) || now.Sub(a.LastSeen) >= refreshAfter {
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
