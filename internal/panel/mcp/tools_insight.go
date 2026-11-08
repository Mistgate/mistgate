//go:build !js

package mcp

import (
	"errors"
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// The composite read views: timeline, fleet_digest and user_diagnose. They are composed in process from the same admin
// procedures the single-purpose read tools call, so the role policy, the token allow-list and the scrubbing are the same.
// What they add is the join an agent would otherwise do by hand on timestamps: one window, counted once.
//
// Hooks for the stream that adds node_runtime, path_loss_matrix, host history and crash reasons (tools_data.go): the
// digest and the timeline do not depend on them. Where they would add a reason, a TODO names the RPC.

const (
	insightEventPages  = 5   // pages of 200 events read for one window
	insightAuditPages  = 5   // pages of 100 audit rows read for one window
	maxOutagesPerNode  = 10  // listed per node; the reason carries the full count
	maxAlertsPerNode   = 10  // listed per node
	maxDigestSeriesLen = 168 // hourly points
	lossHighPermille   = 50  // 5% of packets declared lost
	lossMinPackets     = 200
	rttHighMs          = 400
	rttMinSamples      = 3
)

func insightTools() []toolDef {
	timelineDesc := "One time-ordered list of what happened, newest first, normalised to {at, kind, severity, code, node, subject, summary_key, params, ref} and merged from the event feed, " +
		"alert open and resolve edges, the nodes' saved journal (one item per node, source and hour), agent restarts (kind restart: the agent_started events), " +
		"update rollouts and, for the admin profile only, the audit log of admin changes (addresses removed). Give node (id or exact name) or user (a user id), not both. " +
		"The window is hours (1 to 168, default 24) or from and to (unix seconds, at most 168 hours apart); only items inside it are returned, so \"today\" is hours 24 and you need not judge times yourself. " +
		"kinds keeps only some of: event, restart, alert (opened and resolved), alert_opened, alert_resolved, journal, rollout, audit. min_severity is info (default), warning or error. " +
		"changes_only answers \"what changed since\": it keeps audit rows, rollouts and events that are changes (state_applied, profile, relay, settings, update and rollback events, enrolled and retired nodes, admin actions) and drops alerts, journal lines and the rest. " +
		"limit is 50 by default, at most 100. next_cursor continues the walk across every source: pass it back as cursor with the same other arguments. " +
		"partial_sources names a source that hit its read cap, so older items of the window may be missing from it. Times are unix seconds."
	digestDesc := "The fleet's problems in one call. hours is 1 to 72 (default 24); everything is counted only inside that window, so \"which nodes had problems today\" is hours 24. " +
		"problem_nodes lists each node with a reason list (code, count, last_at): outages and blips with durations (from node_down, node_blip and node_recovered events, minutes_in_window clipped to the window), " +
		"alerts opened in the window or still open by kind, journal warnings and errors by source, failed check rounds, path_lossy and other warning events, country verdict changes, update failures and rollbacks, agent crash restarts (agent_crash, with crash_loop at three within 30 minutes), host reboots (host_reboot), UDP paths that lose packets (path_loss, per sender and target), and a node that is not online now. " +
		"quiet_nodes names the nodes with none of that. totals holds fleet totals, and traffic_hourly and online_hourly are the hourly series (bytes up plus down; peak people online) that fleet_status leaves out; problem nodes carry spark_bytes_24h. " +
		"Check history is hourly: buckets that overlap the window count. unavailable names a part that could not be read. Use timeline for the order of events on one node and node_get, node_journal or checks_results for detail."
	diagnoseDesc := "Why one person cannot connect, in one call: user_id (as users_search gives it) and hours (1 to 168, default 24) for the connection counters. " +
		"Returns the user and their access (status, expiry, quota and usage, device limit and devices with platform, client and fingerprint), which nodes and profiles their subscription gives them (structure only, as subscription_preview: no link, no keys, no addresses), " +
		"per device and node the connection verdict from the nodes' link counters (ok, rejected, unanswered, answers_lost, stalled, throttled, frozen, lossy, slow, no_attempts or no_counters, with signals and the counts: connects, rejects, stalls, freezes, rtt, loss, and with links/3 agents tcp_stalls, tcp_stalls_16k, initiations, responses, responses_unmatched; not_known names those of them this node cannot tell, so their zero means nothing), " +
		"the alerts and events about the person in the window, the state of the nodes they use (status, country verdict, check failures), and findings: a short machine-readable list " +
		"(device_limit_reached, expired, disabled, over_quota, never_connected, no_attempts_in_window, rejects_auth, handshake_unanswered (AmneziaWG: the node does not answer: our side), handshake_answers_lost (the node answers and the answers do not arrive: the way back is cut), stalls, tcp_freeze_16k (VLESS: connections stop after the first 8 to 32 KiB: TCP throttling), freezes, high_loss, high_rtt, node_down, node_blocked_in_country, no_node_access, no_profiles, device_blocked, no_counters), " +
		"each with severity, evidence refs (user:..., conn:<device>:<node>, alert:<id>, event:<id>, check:<node>) and detail. verdict is the most severe finding, or no_issue_found. " +
		"Counts only: never a key, a link, a page password or a client address. Counters mean what user_connection says they mean."
	return []toolDef{
		readTool("timeline", ProfileReadonly,
			procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.FleetServiceListEventsProcedure, adminv1connect.HealthServiceListAlertsProcedure,
				adminv1connect.NodeServiceGetNodeJournalProcedure, adminv1connect.UpdateServiceGetUpdatesProcedure),
			timelineDesc, func(c *call, in timelineArg) (any, error) { return c.timeline(in) }),
		// The admin profile adds the audit slice; a server takes the highest entry its profile reaches.
		readTool("timeline", ProfileAdmin,
			procs(adminv1connect.NodeServiceListNodesProcedure, adminv1connect.FleetServiceListEventsProcedure, adminv1connect.HealthServiceListAlertsProcedure,
				adminv1connect.NodeServiceGetNodeJournalProcedure, adminv1connect.UpdateServiceGetUpdatesProcedure, adminv1connect.AuthServiceListAuditProcedure),
			timelineDesc, func(c *call, in timelineArg) (any, error) { return c.timeline(in) }),
		readTool("fleet_digest", ProfileReadonly,
			procs(adminv1connect.FleetServiceOverviewProcedure, adminv1connect.FleetServiceListEventsProcedure, adminv1connect.HealthServiceListAlertsProcedure,
				adminv1connect.NodeServiceGetNodeJournalProcedure, adminv1connect.HealthServiceGetChecksProcedure, adminv1connect.CountryReachabilityServiceGetReachabilityHistoryProcedure,
				adminv1connect.NodeServiceGetPathLossProcedure),
			digestDesc, func(c *call, in digestArg) (any, error) { return c.fleetDigest(in) }),
		readTool("user_diagnose", ProfileReadonly,
			procs(adminv1connect.UserServiceGetUserProcedure, adminv1connect.UserServiceGetUserConnectionProcedure, adminv1connect.NodeServiceListNodesProcedure,
				adminv1connect.FleetServiceOverviewProcedure, adminv1connect.FleetServiceListEventsProcedure, adminv1connect.HealthServiceListAlertsProcedure, adminv1connect.HealthServiceGetChecksProcedure),
			diagnoseDesc, func(c *call, in diagnoseArg) (any, error) { return c.userDiagnose(in) }),
	}
}

// ---------------------------------------------------------------------------------------------------------------------
// shared reads

// windowEvents reads the feed newest first until it is older than from, and keeps what lies inside [from, to]. more is true when
// the pages ran out before the window did.
func (c *call) windowEvents(req *adminv1.ListEventsRequest, from, to int64) (evs []*adminv1.Event, more bool, err error) {
	req.Limit = 200
	for page := 0; page < insightEventPages; page++ {
		r, err := c.cl.Fleet.ListEvents(c.ctx, connect.NewRequest(req))
		if err != nil {
			return nil, false, apiError(err)
		}
		got := r.Msg.GetEvents()
		old := false
		for _, e := range got {
			if e.GetTimeUnix() < from {
				old = true
				break
			}
			if e.GetTimeUnix() <= to {
				evs = append(evs, e)
			}
		}
		if old || !r.Msg.GetHasMore() || len(got) == 0 {
			return evs, false, nil
		}
		if page == insightEventPages-1 {
			return evs, true, nil
		}
		req.BeforeId = got[len(got)-1].GetId()
	}
	return evs, false, nil
}

var (
	ipv4Text = regexp.MustCompile(`(?:[0-9]{1,3}[.]){3}[0-9]{1,3}`)
	ipv6Text = regexp.MustCompile(`(?i)(?:[0-9a-f]{1,4}:){2,7}[0-9a-f]{1,4}|(?:[0-9a-f]{1,4}:){1,7}:(?:[0-9a-f]{1,4})?|::[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*`)
)

// noAddr removes IP literals from text that came from data: an event or an alert may carry the address a node or a person
// connected from, and these views never show one (a time such as 12:30:45 stays).
func noAddr(s string) string {
	s = ipv4Text.ReplaceAllString(s, "[addr]")
	return ipv6Text.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Contains(m, "::") || strings.ContainsAny(strings.ToLower(m), "abcdef") || strings.Count(m, ":") >= 5 {
			return "[addr]"
		}
		return m
	})
}

// insMap is cleanMap with the addresses taken out of the values.
func insMap(m map[string]string, valueMax int) map[string]string {
	out := cleanMap(m, valueMax)
	for k, v := range out {
		out[k] = noAddr(v)
	}
	return out
}

func hoursFor(span int64, max uint32) uint32 {
	h := uint32((span + 3599) / 3600)
	return min(max, h)
}

func atoiParam(m map[string]string, k string) int {
	n, _ := strconv.Atoi(m[k])
	return max(n, 0)
}

func sevRank(s string) int {
	switch s {
	case "info":
		return 1
	case "warning":
		return 2
	case "error":
		return 3
	}
	return 0
}

func eventSev(s adminv1.EventSeverity) string { return enumName("EVENT_SEVERITY_", s.String()) }

// alertSev maps an alert's severity onto the event scale; critical is an error.
func alertSev(s adminv1.AlertSeverity) string {
	if s == adminv1.AlertSeverity_ALERT_SEVERITY_CRITICAL {
		return "error"
	}
	return enumName("ALERT_SEVERITY_", s.String())
}

// ---------------------------------------------------------------------------------------------------------------------
// timeline

type timelineArg struct {
	Node        string   `json:"node,omitempty" jsonschema:"node id or exact name; not together with user"`
	User        string   `json:"user,omitempty" jsonschema:"user id; not together with node"`
	Hours       int      `json:"hours,omitempty" jsonschema:"window, 1 to 168; default 24 (not together with from or to)"`
	From        int64    `json:"from,omitempty" jsonschema:"window start, unix seconds"`
	To          int64    `json:"to,omitempty" jsonschema:"window end, unix seconds; default now"`
	Kinds       []string `json:"kinds,omitempty" jsonschema:"any of event, restart, alert, alert_opened, alert_resolved, journal, rollout, audit"`
	MinSeverity string   `json:"min_severity,omitempty" jsonschema:"info (default), warning or error"`
	ChangesOnly bool     `json:"changes_only,omitempty" jsonschema:"keep only changes: audit rows, rollouts, profile, relay, settings, update and state_applied events"`
	Limit       int      `json:"limit,omitempty" jsonschema:"default 50, at most 100"`
	Cursor      string   `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page, with the same other arguments"`
}

type tlItemV struct {
	At         int64             `json:"at"`
	Kind       string            `json:"kind"`
	Severity   string            `json:"severity"`
	Code       string            `json:"code"`
	NodeID     string            `json:"node_id,omitempty"`
	Node       string            `json:"node,omitempty"`
	Subject    string            `json:"subject,omitempty"`
	SummaryKey string            `json:"summary_key"`
	Params     map[string]string `json:"params,omitempty"`
	Ref        string            `json:"ref"`
}

// TimelineV is the timeline result.
type TimelineV struct {
	NowUnix        int64     `json:"now_unix"`
	SinceUnix      int64     `json:"since_unix"`
	UntilUnix      int64     `json:"until_unix"`
	Items          []tlItemV `json:"items"`
	HasMore        bool      `json:"has_more"`
	NextCursor     string    `json:"next_cursor,omitempty"`
	PartialSources []string  `json:"partial_sources,omitempty"`
	Unavailable    []string  `json:"unavailable,omitempty"`
	Truncated      bool      `json:"truncated,omitempty"`
}

func (v *TimelineV) shrink() bool {
	v.Truncated, v.HasMore = true, true
	ok := cut(&v.Items)
	if n := len(v.Items); n > 0 {
		v.NextCursor = tlCursor(v.Items[n-1])
	}
	return ok
}

var tlCursorShape = regexp.MustCompile(`^(\d{1,12})~([A-Za-z0-9_.:-]{1,160})$`)

func tlCursor(it tlItemV) string { return fmt.Sprintf("%d~%s", it.At, it.Ref) }

// tlBefore says whether a comes before b in the walk: newest first, ties by ref.
func tlBefore(a, b tlItemV) bool {
	if a.At != b.At {
		return a.At > b.At
	}
	return a.Ref < b.Ref
}

var timelineKinds = []string{"event", "restart", "alert", "alert_opened", "alert_resolved", "journal", "rollout", "audit"}

func kindWanted(want []string, kind string) bool {
	if len(want) == 0 {
		return true
	}
	for _, w := range want {
		if w == kind || w == "alert" && strings.HasPrefix(kind, "alert_") {
			return true
		}
	}
	return false
}

// isChangeEvent: the events that are somebody changing the fleet, not the fleet reporting.
func isChangeEvent(e *adminv1.Event) bool {
	if e.GetSource() == "admin" {
		return true
	}
	code := e.GetCode()
	for _, p := range []string{"update_", "profile_", "relay_", "settings_", "rollback", "apply_"} {
		if strings.HasPrefix(code, p) {
			return true
		}
	}
	switch code {
	case "state_applied", "node_enrolled", "node_retired", "user_created", "profiles_restarted":
		return true
	}
	return false
}

func shortHash(parts ...string) string {
	h := fnv.New32a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return strconv.FormatUint(uint64(h.Sum32()), 36)
}

func (c *call) timeline(in timelineArg) (any, error) {
	if in.Node != "" && in.User != "" {
		return nil, errors.New("give node or user, not both")
	}
	now := c.e.now().Unix()
	since, until, err := insightWindow(now, in.Hours, in.From, in.To, 24, 168)
	if err != nil {
		return nil, err
	}
	for _, k := range in.Kinds {
		if !slices.Contains(timelineKinds, k) {
			return nil, errors.New("kinds: one of " + strings.Join(timelineKinds, ", "))
		}
	}
	minSev := "info"
	if in.MinSeverity != "" {
		if _, err := eventSeverity(in.MinSeverity); err != nil {
			return nil, err
		}
		minSev = in.MinSeverity
	}
	if in.User != "" {
		if err := validID("user", in.User); err != nil {
			return nil, err
		}
	}
	var cur *tlItemV
	if in.Cursor != "" {
		m := tlCursorShape.FindStringSubmatch(in.Cursor)
		if m == nil {
			return nil, errors.New("cursor: use next_cursor of the previous page")
		}
		at, _ := strconv.ParseInt(m[1], 10, 64)
		cur = &tlItemV{At: at, Ref: m[2]}
	}
	nodeID := ""
	if in.Node != "" {
		if nodeID, _, err = c.nodeRef(in.Node); err != nil {
			return nil, err
		}
	}
	v := &TimelineV{NowUnix: now, SinceUnix: since, UntilUnix: until, Items: []tlItemV{}}
	var all []tlItemV
	add := func(it tlItemV) {
		if it.At < since || it.At > until || sevRank(it.Severity) < sevRank(minSev) || !kindWanted(in.Kinds, it.Kind) {
			return
		}
		all = append(all, it)
	}

	// events and restarts
	evReq := &adminv1.ListEventsRequest{NodeId: nodeID, UserId: in.User, MinSeverity: eventSeverityOrZero(minSev)}
	evs, more, err := c.windowEvents(evReq, since, until)
	if err != nil {
		return nil, err
	}
	if more {
		v.PartialSources = append(v.PartialSources, "events")
	}
	for _, e := range evs {
		if in.ChangesOnly && !isChangeEvent(e) {
			continue
		}
		kind := "event"
		if e.GetCode() == "agent_started" {
			kind = "restart"
		}
		subject := nm(e.GetUserName())
		if subject == "" {
			subject = nm(e.GetProfileName())
		}
		add(tlItemV{At: e.GetTimeUnix(), Kind: kind, Severity: eventSev(e.GetSeverity()), Code: clean(e.GetCode(), 80),
			NodeID: e.GetNodeId(), Node: nm(e.GetNodeName()), Subject: subject, SummaryKey: "event." + clean(e.GetCode(), 80),
			Params: insMap(e.GetParams(), maxParamValue), Ref: "event:" + strconv.FormatInt(e.GetId(), 10)})
	}

	// alert edges
	if !in.ChangesOnly {
		req := &adminv1.ListAlertsRequest{NodeId: nodeID, HistoryWindowS: uint32(min(max(now-since, 3600), 30*24*3600))}
		if r, err := c.cl.Health.ListAlerts(c.ctx, connect.NewRequest(req)); err != nil {
			v.Unavailable = append(v.Unavailable, "alerts")
		} else {
			for _, a := range slices.Concat(r.Msg.GetActive(), r.Msg.GetHistory()) {
				if in.User != "" && a.GetSubject() != in.User {
					continue
				}
				if len(r.Msg.GetHistory()) >= 200 {
					if !slices.Contains(v.PartialSources, "alerts") {
						v.PartialSources = append(v.PartialSources, "alerts")
					}
				}
				for _, it := range alertEdges(a) {
					add(it)
				}
			}
		}
	}

	// the journal, and the rollouts: node-level sources, not about a person
	if in.User == "" {
		if !in.ChangesOnly {
			c.journalItems(v, nodeID, since, now, minSev, add)
		}
		c.rolloutItems(v, nodeID, add)
	}

	// the audit slice: admin profile only
	if c.profile == ProfileAdmin {
		c.auditItems(v, in, nodeID, since, until, add)
	}

	slices.SortFunc(all, func(a, b tlItemV) int {
		switch {
		case tlBefore(a, b):
			return -1
		case tlBefore(b, a):
			return 1
		}
		return 0
	})
	limit := listLimit(in.Limit)
	for _, it := range all {
		if cur != nil && !tlBefore(*cur, it) {
			continue
		}
		if len(v.Items) == limit {
			v.HasMore = true
			break
		}
		v.Items = append(v.Items, it)
	}
	if v.HasMore {
		v.NextCursor = tlCursor(v.Items[len(v.Items)-1])
	}
	return v, nil
}

func eventSeverityOrZero(s string) adminv1.EventSeverity {
	x, _ := eventSeverity(s)
	return x
}

// insightWindow resolves hours or from/to into [since, until] unix seconds. A window is at most max hours.
func insightWindow(now int64, hours int, from, to int64, def, max int) (since, until int64, err error) {
	if from != 0 || to != 0 {
		if hours != 0 {
			return 0, 0, errors.New("give hours, or from and to, not both")
		}
		until = to
		if until == 0 {
			until = now
		}
		since = from
		if since == 0 {
			since = until - int64(def)*3600
		}
		if since < 0 || until <= since {
			return 0, 0, errors.New("from must be earlier than to (unix seconds)")
		}
		if until-since > int64(max)*3600 {
			return 0, 0, fmt.Errorf("the window is at most %d hours", max)
		}
		return since, until, nil
	}
	if hours < 0 || hours > max {
		return 0, 0, fmt.Errorf("hours must be 1 to %d", max)
	}
	if hours == 0 {
		hours = def
	}
	return now - int64(hours)*3600, now, nil
}

func alertEdges(a *adminv1.Alert) []tlItemV {
	opened := a.GetOpenedUnix()
	if opened == 0 {
		opened = a.GetFirstSeenUnix()
	}
	kind := enumName("ALERT_KIND_", a.GetKind().String())
	params := insMap(a.GetParams(), maxParamValue)
	if params == nil {
		params = map[string]string{}
	}
	params["alert_severity"] = enumName("ALERT_SEVERITY_", a.GetSeverity().String())
	base := tlItemV{Severity: alertSev(a.GetSeverity()), Code: kind, NodeID: a.GetNodeId(), Node: nm(a.GetNodeName()), Subject: nm(a.GetSubject()),
		SummaryKey: clean(a.GetTitleKey(), 80), Params: params}
	out := []tlItemV{}
	if opened > 0 {
		it := base
		it.At, it.Kind, it.Ref = opened, "alert_opened", "alert:"+clean(a.GetId(), 60)+":opened"
		out = append(out, it)
	}
	if a.GetResolvedAtUnix() > 0 {
		it := base
		it.At, it.Kind, it.Ref = a.GetResolvedAtUnix(), "alert_resolved", "alert:"+clean(a.GetId(), 60)+":resolved"
		p := map[string]string{}
		for k, x := range params {
			p[k] = x
		}
		p["resolution"] = clean(a.GetResolution(), 60)
		it.Params = p
		out = append(out, it)
	}
	return out
}

func (c *call) journalItems(v *TimelineV, nodeID string, since, now int64, minSev string, add func(tlItemV)) {
	level := adminv1.EventSeverity_EVENT_SEVERITY_WARNING
	if minSev == "error" {
		level = adminv1.EventSeverity_EVENT_SEVERITY_ERROR
	}
	r, err := c.cl.Node.GetNodeJournal(c.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{
		NodeId: nodeID, Hours: hoursFor(now-since, 168), MinLevel: level, Limit: 200,
	}))
	if err != nil {
		v.Unavailable = append(v.Unavailable, "journal")
		return
	}
	if r.Msg.GetTruncated() {
		v.PartialSources = append(v.PartialSources, "journal")
	}
	for _, e := range r.Msg.GetEntries() {
		at := e.GetLastUnix()
		if at == 0 {
			at = e.GetHourStartUnix()
		}
		p := insMap(e.GetAttrs(), 160)
		if p == nil {
			p = map[string]string{}
		}
		p["count"] = strconv.FormatUint(uint64(e.GetCount()), 10)
		p["hour_unix"] = strconv.FormatInt(e.GetHourStartUnix(), 10)
		if e.GetInboundId() != "" {
			p["inbound"] = clean(e.GetInboundId(), 64)
		}
		if e.GetProfileName() != "" {
			p["profile"] = nm(e.GetProfileName())
		}
		add(tlItemV{At: at, Kind: "journal", Severity: eventSev(e.GetLevel()), Code: clean(e.GetSource(), 32),
			NodeID: e.GetNodeId(), Node: nm(e.GetNodeName()), SummaryKey: noAddr(clean(e.GetMessage(), 256)), Params: p,
			Ref: fmt.Sprintf("journal:%s:%d:%s", clean(e.GetNodeId(), 60), e.GetHourStartUnix(), shortHash(e.GetSource(), e.GetLevel().String(), e.GetMessage(), e.GetInboundId()))})
	}
}

// rolloutItems adds the start and the end of the active or last rollout. The steps carry no time of their own, so they are
// counted into the items' params; each node's own update, failure and rollback is an event in the feed.
func (c *call) rolloutItems(v *TimelineV, nodeID string, add func(tlItemV)) {
	r, err := c.cl.Update.GetUpdates(c.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
	if err != nil {
		v.Unavailable = append(v.Unavailable, "rollout")
		return
	}
	ro := r.Msg.GetRollout()
	if ro == nil || ro.GetCreatedUnix() == 0 {
		return
	}
	failed, member := 0, nodeID == ""
	for _, s := range ro.GetSteps() {
		if s.GetNodeId() == nodeID {
			member = true
		}
		if s.GetErrorKey() != "" {
			failed++
		}
	}
	if !member {
		return
	}
	p := map[string]string{"to_version": clean(ro.GetToVersion(), 60), "status": enumName("ROLLOUT_STATUS_", ro.GetStatus().String()),
		"steps": strconv.Itoa(len(ro.GetSteps())), "steps_failed": strconv.Itoa(failed), "batch_size": strconv.FormatUint(uint64(ro.GetBatchSize()), 10)}
	sev := "info"
	if failed > 0 {
		sev = "warning"
	}
	add(tlItemV{At: ro.GetCreatedUnix(), Kind: "rollout", Severity: "info", Code: "rollout_started", SummaryKey: "rollout.started", Params: p, Ref: "rollout:" + clean(ro.GetId(), 60) + ":started"})
	if ro.GetFinishedUnix() > 0 {
		add(tlItemV{At: ro.GetFinishedUnix(), Kind: "rollout", Severity: sev, Code: "rollout_finished", SummaryKey: "rollout.finished", Params: p, Ref: "rollout:" + clean(ro.GetId(), 60) + ":finished"})
	}
}

func (c *call) auditItems(v *TimelineV, in timelineArg, nodeID string, since, until int64, add func(tlItemV)) {
	var before int64
	for page := 0; page < insightAuditPages; page++ {
		r, err := c.cl.Auth.ListAudit(c.ctx, connect.NewRequest(&adminv1.ListAuditRequest{BeforeId: before, PageSize: 100}))
		if err != nil {
			v.Unavailable = append(v.Unavailable, "audit")
			return
		}
		old := false
		for _, e := range r.Msg.GetEntries() {
			if e.GetTimeUnix() < since {
				old = true
				break
			}
			raw := e.GetParamsJson()
			if nodeID != "" && !strings.Contains(raw, nodeID) || in.User != "" && !strings.Contains(raw, in.User) {
				continue
			}
			add(tlItemV{At: e.GetTimeUnix(), Kind: "audit", Severity: "info", Code: clean(e.GetAction(), 80), Subject: nm(e.GetActorName()),
				SummaryKey: "audit." + clean(e.GetAction(), 80),
				Params: map[string]string{"source": enumName("AUDIT_SOURCE_", e.GetSource().String()), "actor": clean(e.GetActorId(), 80),
					"result": clean(e.GetResult(), 60), "params": noAddr(auditParamsWithoutAddresses(raw))},
				Ref: "audit:" + strconv.FormatInt(e.GetId(), 10)})
		}
		next := r.Msg.GetNextBeforeId()
		if old || next == 0 {
			return
		}
		if page == insightAuditPages-1 {
			v.PartialSources = append(v.PartialSources, "audit")
			return
		}
		before = next
	}
}

// ---------------------------------------------------------------------------------------------------------------------
// fleet_digest

type digestArg struct {
	Hours int `json:"hours,omitempty" jsonschema:"window, 1 to 72; default 24"`
}

type digestReasonV struct {
	Code    string `json:"code"`
	Count   int    `json:"count"`
	LastAt  int64  `json:"last_at,omitempty"`
	Minutes int    `json:"minutes,omitempty"` // outages and blips: the total length
	Detail  string `json:"detail,omitempty"`
}

type outageV struct {
	StartUnix       int64 `json:"start_unix"`
	EndUnix         int64 `json:"end_unix,omitempty"`
	Minutes         int   `json:"minutes"`
	MinutesInWindow int   `json:"minutes_in_window"`
	Ongoing         bool  `json:"ongoing,omitempty"`
	BeganBefore     bool  `json:"began_before_window,omitempty"`
}

type blipV struct {
	AtUnix   int64 `json:"at_unix"`
	Minutes  int   `json:"minutes"`
	Rebooted bool  `json:"rebooted,omitempty"`
}

type digestAlertV struct {
	Kind         string `json:"kind"`
	Severity     string `json:"severity"`
	State        string `json:"state"` // open or resolved
	OpenedUnix   int64  `json:"opened_unix"`
	ResolvedUnix int64  `json:"resolved_unix,omitempty"`
	Resolution   string `json:"resolution,omitempty"`
	Subject      string `json:"subject,omitempty"`
	Ref          string `json:"ref"`
}

type journalCountV struct {
	Source   string `json:"source"`
	Warnings int    `json:"warnings"`
	Errors   int    `json:"errors"`
}

type countryChangeV struct {
	Source  string `json:"source"`
	AtUnix  int64  `json:"at_unix"`
	Verdict string `json:"verdict"`
}

type digestNodeV struct {
	ID      string           `json:"id"`
	Name    string           `json:"name"`
	Status  string           `json:"status"`
	Reason  string           `json:"status_reason,omitempty"`
	Reasons []digestReasonV  `json:"reasons"`
	Outages []outageV        `json:"outages,omitempty"`
	Blips   []blipV          `json:"blips,omitempty"`
	Alerts  []digestAlertV   `json:"alerts,omitempty"`
	Journal []journalCountV  `json:"journal,omitempty"`
	Country []countryChangeV `json:"country_changes,omitempty"`
	Spark   []uint64         `json:"spark_bytes_24h,omitempty"`
	score   int
}

type seriesPointV struct {
	HourUnix   int64             `json:"hour_unix"`
	Total      uint64            `json:"total"`
	ByProtocol map[string]uint64 `json:"by_protocol,omitempty"`
}

type digestTotalsV struct {
	NodesTotal        uint32 `json:"nodes_total"`
	NodesProblemNow   uint32 `json:"nodes_problem_now"`
	NodesWithProblems int    `json:"nodes_with_problems_in_window"`
	UsersOnline       uint32 `json:"users_online"`
	AlertsActive      uint32 `json:"alerts_active"`
	AlertsCritical    uint32 `json:"alerts_critical"`
	AlertsAboutPeople int    `json:"alerts_about_people"`
	Outages           int    `json:"outages"`
	Blips             int    `json:"blips"`
	TrafficBytes      uint64 `json:"traffic_bytes_window"`
	PeakOnline        uint64 `json:"peak_online"`
	EventsInWindow    int    `json:"events_in_window"`
	EventsCapped      bool   `json:"events_capped,omitempty"`
}

type codeCountV struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
}

// DigestV is the fleet_digest result.
type DigestV struct {
	NowUnix     int64          `json:"now_unix"`
	SinceUnix   int64          `json:"since_unix"`
	Hours       int            `json:"hours"`
	Totals      digestTotalsV  `json:"totals"`
	Problems    []digestNodeV  `json:"problem_nodes"`
	Quiet       []string       `json:"quiet_nodes"`
	OtherEvents []codeCountV   `json:"other_events,omitempty"`
	Traffic     []seriesPointV `json:"traffic_hourly"`
	Online      []seriesPointV `json:"online_hourly"`
	Unavailable []string       `json:"unavailable,omitempty"`
	Truncated   bool           `json:"truncated,omitempty"`
}

func (v *DigestV) shrink() bool {
	v.Truncated = true
	return cut(&v.Quiet) || cutOld(&v.Traffic) || cutOld(&v.Online) || cut(&v.Problems)
}

type digestBuilder struct {
	byID  map[string]*digestNodeV
	order []*digestNodeV
	rs    map[string]map[string]int // node id -> reason code -> index in node.Reasons
}

func (b *digestBuilder) node(id, name string) *digestNodeV {
	if n, ok := b.byID[id]; ok {
		if n.Name == "" {
			n.Name = name
		}
		return n
	}
	n := &digestNodeV{ID: id, Name: name, Status: "unknown", Reasons: []digestReasonV{}}
	b.byID[id] = n
	b.order = append(b.order, n)
	b.rs[id] = map[string]int{}
	return n
}

// reason adds to a node's reason list; weight orders the problem nodes.
func (b *digestBuilder) reason(id, name, code string, n int, at int64, minutes int, detail string, weight int) {
	node := b.node(id, name)
	i, ok := b.rs[id][code]
	if !ok {
		node.Reasons = append(node.Reasons, digestReasonV{Code: code})
		i = len(node.Reasons) - 1
		b.rs[id][code] = i
	}
	r := &node.Reasons[i]
	r.Count += n
	r.Minutes += minutes
	if at > r.LastAt {
		r.LastAt = at
	}
	if detail != "" {
		r.Detail = clean(detail, 120)
	}
	node.score += weight * n
}

// forcedProblemCodes are events that are a problem whatever their severity.
var forcedProblemCodes = map[string]bool{
	"node_down": true, "node_blip": true, "update_failed": true, "update_rolled_back": true, "update_step_failed": true,
	"update_step_rolled_back": true, "rollback_failed": true, "path_lossy": true, "port_lossy": true, "state_drift": true,
	"engine_failed": true,
}

func (c *call) fleetDigest(in digestArg) (any, error) {
	if in.Hours < 0 || in.Hours > 72 {
		return nil, errors.New("hours must be 1 to 72")
	}
	hours := in.Hours
	if hours == 0 {
		hours = 24
	}
	now := c.e.now().Unix()
	since := now - int64(hours)*3600
	rng := adminv1.OverviewRange_OVERVIEW_RANGE_24H
	if hours > 24 {
		rng = adminv1.OverviewRange_OVERVIEW_RANGE_7D
	}
	ov, err := c.cl.Fleet.Overview(c.ctx, connect.NewRequest(&adminv1.OverviewRequest{Range: rng, EventLimit: 1}))
	if err != nil {
		return nil, apiError(err)
	}
	o := ov.Msg
	evs, capped, err := c.windowEvents(&adminv1.ListEventsRequest{}, since, now)
	if err != nil {
		return nil, err
	}
	al, err := c.cl.Health.ListAlerts(c.ctx, connect.NewRequest(&adminv1.ListAlertsRequest{HistoryWindowS: uint32(hours) * 3600}))
	if err != nil {
		return nil, apiError(err)
	}

	v := &DigestV{NowUnix: now, SinceUnix: since, Hours: hours, Problems: []digestNodeV{}, Quiet: []string{}, Traffic: []seriesPointV{}, Online: []seriesPointV{}}
	b := &digestBuilder{byID: map[string]*digestNodeV{}, rs: map[string]map[string]int{}}
	sparks := map[string][]uint64{}
	for _, n := range o.GetNodes() {
		node := b.node(n.GetId(), nm(n.GetName()))
		node.Status, node.Reason = nodeStatus(n.GetStatus()), clean(n.GetReason().GetCode(), 60)
		sparks[n.GetId()] = n.GetSparkBytes()
		if st := n.GetStatus(); st != adminv1.NodeStatus_NODE_STATUS_ONLINE && st != adminv1.NodeStatus_NODE_STATUS_RETIRED && st != adminv1.NodeStatus_NODE_STATUS_PENDING {
			b.reason(n.GetId(), "", "status_"+node.Status, 1, now, 0, node.Reason, 6)
		}
	}
	v.Totals = digestTotalsV{NodesTotal: o.GetNodesTotal(), NodesProblemNow: o.GetNodesProblem(), UsersOnline: o.GetUsersOnline(),
		AlertsActive: o.GetAlertsActive(), AlertsCritical: o.GetAlertsCritical(), EventsInWindow: len(evs), EventsCapped: capped}
	for _, p := range o.GetTraffic() {
		if p.GetStartUnix()+3600 <= since {
			continue
		}
		pt := seriesPointV{HourUnix: p.GetStartUnix(), ByProtocol: map[string]uint64{}}
		for _, x := range p.GetValues() {
			pt.Total += x.GetValue()
			pt.ByProtocol[clean(x.GetProtocol(), 40)] = x.GetValue()
		}
		v.Totals.TrafficBytes += pt.Total
		v.Traffic = append(v.Traffic, pt)
	}
	for _, p := range o.GetOnline() {
		if p.GetStartUnix()+3600 <= since {
			continue
		}
		pt := seriesPointV{HourUnix: p.GetStartUnix(), ByProtocol: map[string]uint64{}}
		for _, x := range p.GetValues() {
			pt.Total += x.GetValue()
			pt.ByProtocol[clean(x.GetProtocol(), 40)] = x.GetValue()
		}
		v.Totals.PeakOnline = max(v.Totals.PeakOnline, pt.Total)
		v.Online = append(v.Online, pt)
	}
	v.Traffic, v.Online = lastN(v.Traffic, maxDigestSeriesLen), lastN(v.Online, maxDigestSeriesLen)

	// events: outages and blips first, then the warnings
	slices.SortFunc(evs, func(a, b *adminv1.Event) int {
		if a.GetTimeUnix() != b.GetTimeUnix() {
			return int(a.GetTimeUnix() - b.GetTimeUnix())
		}
		return int(a.GetId() - b.GetId())
	})
	open := map[string]*outageV{}
	finish := func(id string, o *outageV) {
		node := b.byID[id]
		if len(node.Outages) < maxOutagesPerNode {
			node.Outages = append(node.Outages, *o)
		}
		b.reason(id, "", "outage", 1, max(o.EndUnix, o.StartUnix), o.MinutesInWindow, "", 10)
		v.Totals.Outages++
	}
	others := map[string]int{}
	for _, e := range evs {
		id, code, t := e.GetNodeId(), e.GetCode(), e.GetTimeUnix()
		if id == "" {
			if sevRank(eventSev(e.GetSeverity())) >= 2 {
				others[clean(code, 80)]++
			}
			continue
		}
		b.node(id, nm(e.GetNodeName()))
		switch code {
		case "node_down":
			if _, ok := open[id]; !ok {
				start := t - int64(atoiParam(e.GetParams(), "minutes"))*60
				open[id] = &outageV{StartUnix: start, BeganBefore: start < since}
			}
		case "node_recovered":
			mins := atoiParam(e.GetParams(), "minutes")
			o := open[id]
			delete(open, id)
			if o == nil {
				start := t - int64(mins)*60
				o = &outageV{StartUnix: start, BeganBefore: start < since}
			}
			o.EndUnix = t
			if mins == 0 {
				mins = int((t - o.StartUnix) / 60)
			}
			o.Minutes = mins
			o.MinutesInWindow = overlapMinutes(o.StartUnix, o.EndUnix, since, now)
			finish(id, o)
		case "node_blip":
			node := b.node(id, "")
			bl := blipV{AtUnix: t, Minutes: atoiParam(e.GetParams(), "minutes"), Rebooted: e.GetParams()["rebooted"] == "true"}
			if len(node.Blips) < maxOutagesPerNode {
				node.Blips = append(node.Blips, bl)
			}
			b.reason(id, "", "blip", 1, t, bl.Minutes, "", 3)
			v.Totals.Blips++
		default:
			sev := sevRank(eventSev(e.GetSeverity()))
			if sev >= 2 || forcedProblemCodes[code] {
				w := 2
				if sev >= 3 {
					w = 4
				}
				detail := ""
				if r := e.GetParams()["reason"]; r != "" {
					detail = r
				}
				b.reason(id, "", clean(code, 80), 1, t, 0, detail, w)
			}
		}
	}
	for id, o := range open {
		o.Ongoing = true
		o.Minutes = int((now - o.StartUnix) / 60)
		o.MinutesInWindow = overlapMinutes(o.StartUnix, now, since, now)
		finish(id, o)
	}

	// alerts: open now, or resolved inside the window
	for _, a := range slices.Concat(al.Msg.GetActive(), al.Msg.GetHistory()) {
		if a.GetNodeId() == "" {
			v.Totals.AlertsAboutPeople++
			continue
		}
		opened := a.GetOpenedUnix()
		if opened == 0 {
			opened = a.GetFirstSeenUnix()
		}
		state := "open"
		if a.GetResolvedAtUnix() > 0 {
			state = "resolved"
			if a.GetResolvedAtUnix() < since {
				continue
			}
		}
		kind := enumName("ALERT_KIND_", a.GetKind().String())
		node := b.node(a.GetNodeId(), nm(a.GetNodeName()))
		if state == "open" && a.GetKind() == adminv1.AlertKind_ALERT_KIND_NODE_DOWN && !slices.ContainsFunc(node.Outages, func(o outageV) bool {
			return o.StartUnix-120 <= opened && (o.Ongoing || o.EndUnix == 0 || o.EndUnix >= opened)
		}) {
			// down since before the window: the alert is the only record of when it began
			start := opened
			o := outageV{StartUnix: start, Ongoing: true, BeganBefore: start < since, Minutes: int((now - start) / 60), MinutesInWindow: overlapMinutes(start, now, since, now)}
			if len(node.Outages) < maxOutagesPerNode {
				node.Outages = append(node.Outages, o)
			}
			b.reason(a.GetNodeId(), "", "outage", 1, now, o.MinutesInWindow, "", 10)
			v.Totals.Outages++
		}
		if len(node.Alerts) < maxAlertsPerNode {
			node.Alerts = append(node.Alerts, digestAlertV{Kind: kind, Severity: enumName("ALERT_SEVERITY_", a.GetSeverity().String()), State: state,
				OpenedUnix: opened, ResolvedUnix: a.GetResolvedAtUnix(), Resolution: clean(a.GetResolution(), 60), Subject: nm(a.GetSubject()), Ref: "alert:" + clean(a.GetId(), 60)})
		}
		w := 3
		if a.GetSeverity() == adminv1.AlertSeverity_ALERT_SEVERITY_CRITICAL {
			w = 8
		}
		if state == "resolved" {
			w = 1
		}
		b.reason(a.GetNodeId(), "", "alert_"+state+":"+kind, 1, max(opened, a.GetResolvedAtUnix()), 0, "", w)
	}

	// the journal
	if jr, err := c.cl.Node.GetNodeJournal(c.ctx, connect.NewRequest(&adminv1.GetNodeJournalRequest{
		Hours: hoursFor(now-since, 72), MinLevel: adminv1.EventSeverity_EVENT_SEVERITY_WARNING, Limit: 200,
	})); err != nil {
		v.Unavailable = append(v.Unavailable, "journal")
	} else {
		if jr.Msg.GetTruncated() {
			v.Unavailable = append(v.Unavailable, "journal_partial")
		}
		per := map[string]map[string]*journalCountV{}
		for _, e := range jr.Msg.GetEntries() {
			at := e.GetLastUnix()
			if at == 0 {
				at = e.GetHourStartUnix() + 3599
			}
			if at < since || e.GetNodeId() == "" {
				continue
			}
			id := e.GetNodeId()
			if per[id] == nil {
				per[id] = map[string]*journalCountV{}
			}
			src := clean(e.GetSource(), 32)
			jc := per[id][src]
			if jc == nil {
				jc = &journalCountV{Source: src}
				per[id][src] = jc
			}
			code, w := "journal_warning", 1
			if e.GetLevel() == adminv1.EventSeverity_EVENT_SEVERITY_ERROR {
				jc.Errors += int(e.GetCount())
				code, w = "journal_error", 2
			} else {
				jc.Warnings += int(e.GetCount())
			}
			b.reason(id, nm(e.GetNodeName()), code, int(e.GetCount()), at, 0, src, w)
		}
		for id, m := range per {
			list := make([]journalCountV, 0, len(m))
			for _, jc := range m {
				list = append(list, *jc)
			}
			slices.SortFunc(list, func(a, b journalCountV) int { return strings.Compare(a.Source, b.Source) })
			b.byID[id].Journal = list
		}
	}

	// check rounds: the history is hourly, so a bucket that overlaps the window counts
	if cr, err := c.cl.Health.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{})); err != nil {
		v.Unavailable = append(v.Unavailable, "checks")
	} else {
		for _, row := range cr.Msg.GetRows() {
			failed, streak, code := 0, uint32(0), ""
			for _, cell := range row.GetCells() {
				if !cell.GetDeployed() {
					continue
				}
				for _, bk := range cell.GetHistory() {
					if bk.GetStartUnix()+3600 > since {
						failed += int(bk.GetFailed())
					}
				}
				if cell.GetFailStreak() > streak {
					streak = cell.GetFailStreak()
					code = cell.GetLast().GetErrorCode()
				}
			}
			if failed > 0 || streak > 0 {
				detail := ""
				if streak > 0 {
					detail = fmt.Sprintf("failing now, streak %d %s", streak, code)
				}
				b.reason(row.GetNodeId(), nm(row.GetNodeName()), "check_failed", max(failed, 1), cr.Msg.GetNowUnix(), 0, detail, 2)
			}
		}
	}

	// country verdict changes
	days := uint32(min(max((hours+23)/24, 1), 3))
	if rh, err := c.cl.Reachability.GetReachabilityHistory(c.ctx, connect.NewRequest(&adminv1.GetReachabilityHistoryRequest{Days: days, Family: 4})); err == nil {
		for _, nh := range rh.Msg.GetNodes() {
			for _, lane := range nh.GetLanes() {
				for i, pt := range lane.GetPoints() {
					if i == 0 || pt.GetAtUnix() < since {
						continue // the first point only starts the lane
					}
					node := b.node(nh.GetNodeId(), "")
					if len(node.Country) < maxOutagesPerNode {
						node.Country = append(node.Country, countryChangeV{Source: clean(lane.GetSource(), 30), AtUnix: pt.GetAtUnix(), Verdict: enumName("COUNTRY_VERDICT_", pt.GetVerdict().String())})
					}
					b.reason(nh.GetNodeId(), "", "country_verdict_changed", 1, pt.GetAtUnix(), 0, enumName("COUNTRY_VERDICT_", pt.GetVerdict().String()), 2)
				}
			}
		}
	}
	// Restarts: the agent_started events of the window say how the previous run ended (crash = 1: it did not end cleanly and
	// the host had not just booted; reason boot: the host rebooted). Three crashes inside 30 minutes are a crash loop, the
	// same bar as the agent_crash_loop alert. evs is oldest first here.
	crashAt := map[string][]int64{}
	for _, e := range evs {
		id, p := e.GetNodeId(), e.GetParams()
		if e.GetCode() != "agent_started" || id == "" || b.byID[id] == nil {
			continue
		}
		switch {
		case p["crash"] == "1":
			detail := "prev run ended uncleanly"
			switch {
			case p["oom"] == "1":
				detail = "killed for memory (oom)"
			case p["exit_signal"] != "":
				detail = "killed by signal " + p["exit_signal"]
			case p["exit_code"] != "":
				detail = "exit code " + p["exit_code"]
			}
			b.reason(id, "", "agent_crash", 1, e.GetTimeUnix(), 0, detail, 4)
			crashAt[id] = append(crashAt[id], e.GetTimeUnix())
		case p["reason"] == "boot":
			b.reason(id, "", "host_reboot", 1, e.GetTimeUnix(), 0, "", 3)
		}
	}
	for id, ts := range crashAt {
		most, lo := 0, 0
		for hi := range ts {
			for ts[hi]-ts[lo] > 30*60 {
				lo++
			}
			most = max(most, hi-lo+1)
		}
		if most >= 3 {
			b.reason(id, "", "crash_loop", 1, ts[len(ts)-1], 0, fmt.Sprintf("%d crash restarts within 30 minutes", most), 6)
		}
	}
	// Per-pair UDP loss: the sender loses packets on the way to the target while another sender gets through.
	if pl, err := c.cl.Node.GetPathLoss(c.ctx, connect.NewRequest(&adminv1.GetPathLossRequest{Hours: uint32(hours)})); err == nil {
		for _, row := range pl.Msg.GetRows() {
			if row.GetCheckedUnix() < since || b.byID[row.GetFromNodeId()] == nil {
				continue
			}
			b.reason(row.GetFromNodeId(), "", "path_loss", 1, row.GetCheckedUnix(), 0,
				fmt.Sprintf("to %s: %.0f%% lost", nm(row.GetToName()), row.GetLossPct()), 3)
		}
	}

	for _, node := range b.order {
		if len(node.Reasons) == 0 || node.Status == "retired" {
			continue
		}
		if s := sparks[node.ID]; len(s) > 0 {
			node.Spark = s
		}
		v.Problems = append(v.Problems, *node)
	}
	for _, node := range b.order {
		if len(node.Reasons) == 0 && node.Status != "retired" && node.Name != "" {
			v.Quiet = append(v.Quiet, node.Name)
		}
	}
	slices.SortFunc(v.Problems, func(a, b digestNodeV) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return strings.Compare(a.Name, b.Name)
	})
	slices.Sort(v.Quiet)
	v.Totals.NodesWithProblems = len(v.Problems)
	for k, n := range others {
		v.OtherEvents = append(v.OtherEvents, codeCountV{Code: k, Count: n})
	}
	slices.SortFunc(v.OtherEvents, func(a, b codeCountV) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Code, b.Code)
	})
	return v, nil
}

func lastN[T any](s []T, n int) []T {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// overlapMinutes is how long [start, end] lies inside [since, now], in whole minutes.
func overlapMinutes(start, end, since, now int64) int {
	s, e := max(start, since), min(end, now)
	if e <= s {
		return 0
	}
	return int((e - s) / 60)
}

// ---------------------------------------------------------------------------------------------------------------------
// user_diagnose

type diagnoseArg struct {
	UserID string `json:"user_id" jsonschema:"the user id, as users_search gives it"`
	Hours  int    `json:"hours,omitempty" jsonschema:"window of the connection counters, 1 to 168; default 24"`
}

type diagUserV struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	Enabled     bool   `json:"enabled"`
	Expired     bool   `json:"expired"`
	ExpiresUnix int64  `json:"expires_unix"`
	QuotaBytes  uint64 `json:"quota_bytes"`
	UsedBytes   uint64 `json:"used_bytes"`
	QuotaReset  string `json:"quota_reset,omitempty"`
	DeviceLimit uint32 `json:"device_limit"`
	DevicesUsed uint32 `json:"devices_used"`
	LastSeen    int64  `json:"last_seen_unix"`
	Online      bool   `json:"online"`
	CreatedUnix int64  `json:"created_unix"`
}

type diagDeviceV struct {
	ID           string     `json:"id"`
	Kind         string     `json:"kind,omitempty"`
	Platform     string     `json:"platform,omitempty"`
	Model        string     `json:"model,omitempty"`
	Client       string     `json:"client,omitempty"`
	Fingerprint  string     `json:"fingerprint,omitempty"`
	Online       bool       `json:"online"`
	OnlineNodes  []nodeRefV `json:"online_nodes,omitempty"`
	FirstSeen    int64      `json:"first_seen_unix"`
	LastSeen     int64      `json:"last_seen_unix"`
	Stale        bool       `json:"stale,omitempty"`
	Protocols    []string   `json:"protocols,omitempty"`
	Connects     uint32     `json:"connects_in_window"`
	SeenInWindow bool       `json:"counted_in_window"`
}

type diagNodeV struct {
	NodeID    string   `json:"node_id"`
	Name      string   `json:"name"`
	Protocols []string `json:"protocols,omitempty"`
	Status    string   `json:"status"`
	Reason    string   `json:"status_reason,omitempty"`
	Country   string   `json:"country_verdict,omitempty"`
	Confident string   `json:"country_confidence,omitempty"`
	CheckFail bool     `json:"check_failing,omitempty"`
	Streak    uint32   `json:"check_fail_streak,omitempty"`
	NoCounter bool     `json:"no_counters,omitempty"`
}

type diagProfileV struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Protocol string   `json:"protocol"`
	Missing  []string `json:"formats_missing,omitempty"`
}

type diagConnV struct {
	DeviceID     string   `json:"device_id"`
	Platform     string   `json:"platform,omitempty"`
	Client       string   `json:"client,omitempty"`
	NodeID       string   `json:"node_id"`
	NodeName     string   `json:"node_name"`
	Protocols    []string `json:"protocols,omitempty"`
	Verdict      string   `json:"verdict"`
	Signals      []string `json:"signals,omitempty"`
	Connects     uint32   `json:"connects"`
	Rejects      uint32   `json:"rejects"`
	Drops        uint32   `json:"drops"`
	Stalls       uint32   `json:"stalls"`
	Freezes      uint32   `json:"freezes,omitempty"`
	Quiet        uint32   `json:"quiet,omitempty"`
	ClientCloses uint32   `json:"client_closes,omitempty"`
	RttMs        *uint32  `json:"rtt_ms,omitempty"`
	LossPermille *uint32  `json:"loss_permille,omitempty"`
	OnlineS      uint64   `json:"online_s"`
	LastHour     int64    `json:"last_hour_unix,omitempty"`
	// links/3 of the device's keys: VLESS TCP stalls, AmneziaWG handshake counts; not_known names those its protocol could
	// have and this node cannot tell (an older agent, the kernel AWG backend, a VLESS transport without them).
	TCPStalls          uint32   `json:"tcp_stalls,omitempty"`
	TCPStalls16K       uint32   `json:"tcp_stalls_16k,omitempty"`
	Initiations        uint32   `json:"initiations,omitempty"`
	Responses          uint32   `json:"responses,omitempty"`
	ResponsesUnmatched uint32   `json:"responses_unmatched,omitempty"`
	NotKnown           []string `json:"not_known,omitempty"`
}

type diagEventV struct {
	At       int64             `json:"at"`
	Severity string            `json:"severity"`
	Code     string            `json:"code"`
	Node     string            `json:"node,omitempty"`
	Params   map[string]string `json:"params,omitempty"`
	Ref      string            `json:"ref"`
}

type diagAlertV struct {
	Kind         string `json:"kind"`
	Severity     string `json:"severity"`
	State        string `json:"state"`
	TitleKey     string `json:"title_key,omitempty"`
	OpenedUnix   int64  `json:"opened_unix"`
	ResolvedUnix int64  `json:"resolved_unix,omitempty"`
	Ref          string `json:"ref"`
}

type findingV struct {
	Code     string            `json:"code"`
	Severity string            `json:"severity"`
	Evidence []string          `json:"evidence"`
	Detail   map[string]string `json:"detail,omitempty"`
}

// DiagnoseV is the user_diagnose result.
type DiagnoseV struct {
	NowUnix        int64            `json:"now_unix"`
	SinceUnix      int64            `json:"since_unix"`
	Hours          int              `json:"hours"`
	Verdict        string           `json:"verdict"`
	Findings       []findingV       `json:"findings"`
	User           diagUserV        `json:"user"`
	Devices        []diagDeviceV    `json:"devices"`
	BlockedDevices []blockedDeviceV `json:"blocked_devices,omitempty"`
	Profiles       []diagProfileV   `json:"profiles"`
	Nodes          []diagNodeV      `json:"nodes"`
	Connections    []diagConnV      `json:"connections"`
	Alerts         []diagAlertV     `json:"alerts"`
	Events         []diagEventV     `json:"events"`
	EventsCapped   bool             `json:"events_capped,omitempty"`
	Unavailable    []string         `json:"unavailable,omitempty"`
	Truncated      bool             `json:"truncated,omitempty"`
}

func (v *DiagnoseV) shrink() bool {
	v.Truncated = true
	return cut(&v.Events) || cut(&v.Alerts) || cut(&v.Connections) || cut(&v.Devices)
}

func (c *call) userDiagnose(in diagnoseArg) (any, error) {
	if in.Hours < 0 || in.Hours > 168 {
		return nil, errors.New("hours must be 1 to 168")
	}
	hours := in.Hours
	if hours == 0 {
		hours = 24
	}
	ur, err := c.getUser(in.UserID)
	if err != nil {
		return nil, err
	}
	now := c.e.now().Unix()
	since := now - int64(hours)*3600
	u := ur.GetUser()
	v := &DiagnoseV{NowUnix: now, SinceUnix: since, Hours: hours, Findings: []findingV{}, Devices: []diagDeviceV{}, Profiles: []diagProfileV{}, Nodes: []diagNodeV{},
		Connections: []diagConnV{}, Alerts: []diagAlertV{}, Events: []diagEventV{}}
	uid := clean(u.GetId(), 80)
	status := enumName("USER_STATUS_", u.GetStatus().String())
	v.User = diagUserV{ID: uid, Name: nm(u.GetName()), Status: status, Enabled: u.GetStatus() != adminv1.UserStatus_USER_STATUS_DISABLED,
		Expired:     u.GetStatus() == adminv1.UserStatus_USER_STATUS_EXPIRED || u.GetExpiresUnix() > 0 && u.GetExpiresUnix() <= now,
		ExpiresUnix: u.GetExpiresUnix(), QuotaBytes: u.GetQuotaBytes(), UsedBytes: u.GetUsedBytes(), QuotaReset: enumName("QUOTA_RESET_", u.GetQuotaReset().String()),
		DeviceLimit: u.GetDeviceLimit(), DevicesUsed: u.GetDevicesUsed(), LastSeen: u.GetLastSeenUnix(), Online: u.GetOnline(), CreatedUnix: u.GetCreatedUnix()}
	add := func(code, sev string, detail map[string]string, evidence ...string) {
		v.Findings = append(v.Findings, findingV{Code: code, Severity: sev, Evidence: evidence, Detail: detail})
	}

	// access: which nodes and profiles the subscription gives, as subscription_preview does
	names := nodeNames(ur)
	selected := map[string]*diagNodeV{}
	for _, na := range nodeAccessViews(ur.GetNodeAccess()) {
		if !na.Selected {
			continue
		}
		dn := &diagNodeV{NodeID: na.NodeID, Name: na.Name, Protocols: na.Protocols, Status: "unknown"}
		selected[na.NodeID] = dn
	}
	for _, p := range profileRefs(ur.GetProfiles()) {
		dp := diagProfileV{ID: p.ID, Name: p.Name, Protocol: p.Protocol}
		for _, f := range p.Formats {
			if !f.OK {
				dp.Missing = append(dp.Missing, f.Format)
			}
		}
		v.Profiles = append(v.Profiles, dp)
	}

	// the counters
	ncr, cerr := c.cl.User.GetUserConnection(c.ctx, connect.NewRequest(&adminv1.GetUserConnectionRequest{UserId: in.UserID, Hours: uint32(hours)}))
	countersOK := cerr == nil
	if !countersOK {
		v.Unavailable = append(v.Unavailable, "connection_counters")
	}
	withoutCounters := map[string]bool{}
	type connKey struct{ dev, node string }
	groups := map[connKey]*diagConnV{}
	var keys []connKey
	devConnects := map[string]uint32{}
	var rttSum, rttN, lossNum, lossDen = map[connKey]uint64{}, map[connKey]uint64{}, map[connKey]uint64{}, map[connKey]uint64{}
	if countersOK {
		for _, id := range ncr.Msg.GetNodesWithoutCounters() {
			withoutCounters[id] = true
		}
		for _, r := range ncr.Msg.GetRows() {
			k := connKey{r.GetDeviceId(), r.GetNodeId()}
			g := groups[k]
			if g == nil {
				g = &diagConnV{DeviceID: clean(r.GetDeviceId(), 80), Platform: clean(r.GetPlatform(), 40), Client: clean(r.GetClient(), 80), NodeID: clean(r.GetNodeId(), 80), NodeName: nm(r.GetNodeName())}
				groups[k] = g
				keys = append(keys, k)
			}
			if p := clean(r.GetProtocol(), 40); p != "" && !slices.Contains(g.Protocols, p) {
				g.Protocols = append(g.Protocols, p)
			}
			g.Connects += r.GetConnects()
			g.Rejects += r.GetRejects()
			g.Drops += r.GetDrops()
			g.Stalls += r.GetStalls()
			g.Freezes += r.GetFreezes()
			g.Quiet += r.GetQuiet()
			g.ClientCloses += r.GetClientCloses()
			g.OnlineS += r.GetOnlineS()
			g.TCPStalls += r.GetTcpStalls()
			g.TCPStalls16K += r.GetTcpStalls_16K()
			g.Initiations += r.GetInitiations()
			g.Responses += r.GetResponses()
			g.ResponsesUnmatched += r.GetResponsesUnmatched()
			for _, f := range keyReasonFields(r.GetProtocol()) {
				if slices.Contains(r.GetUnsupported(), f) && !slices.Contains(g.NotKnown, f) {
					g.NotKnown = append(g.NotKnown, f)
				}
			}
			g.LastHour = max(g.LastHour, r.GetLastHourUnix())
			devConnects[r.GetDeviceId()] += r.GetConnects()
			if n := uint64(r.GetRttSamples()); n > 0 {
				rttSum[k] += uint64(r.GetRttMs()) * n
				rttN[k] += n
			}
			if n := r.GetPktsSent(); n > 0 {
				lossNum[k] += uint64(r.GetLossPermille()) * n
				lossDen[k] += n
			}
		}
	}
	slices.SortFunc(keys, func(a, b connKey) int { return strings.Compare(a.dev+"/"+a.node, b.dev+"/"+b.node) })
	for _, k := range keys {
		g := groups[k]
		var rtt, loss *uint32
		if rttN[k] > 0 {
			x := uint32(rttSum[k] / rttN[k])
			rtt = &x
		}
		if lossDen[k] > 0 {
			x := uint32(lossNum[k] / lossDen[k])
			loss = &x
		}
		g.RttMs, g.LossPermille = rtt, loss
		ref := "conn:" + g.DeviceID + ":" + g.NodeID
		if g.Rejects > 0 {
			g.Signals = append(g.Signals, "rejects")
			add("rejects_auth", "error", map[string]string{"rejects": strconv.FormatUint(uint64(g.Rejects), 10), "why": "a known key was refused because its term ended", "node": g.NodeName}, ref)
		}
		awgKnown := !slices.Contains(g.NotKnown, "responses_unmatched") && slices.Contains(g.Protocols, "awg")
		if awgKnown && g.Initiations >= minReasonCount && g.Responses*5 < g.Initiations {
			// the node does not answer: our side, whatever the path does
			g.Signals = append(g.Signals, "unanswered")
			add("handshake_unanswered", "error", map[string]string{"initiations": u32(g.Initiations), "responses": u32(g.Responses), "node": g.NodeName,
				"why": "the device's initiations reach the node and it does not answer them: replays, a flood limit or an engine failure on the node"}, ref)
		}
		if awgKnown && g.Responses >= minReasonCount && g.ResponsesUnmatched*100 >= g.Responses*answerLostPct {
			g.Signals = append(g.Signals, "answers_lost")
			add("handshake_answers_lost", "warning", map[string]string{"responses": u32(g.Responses), "responses_unmatched": u32(g.ResponsesUnmatched), "node": g.NodeName,
				"why": "the node answered the device's handshakes and the answers never arrived: the way back to the device is cut, or the device cannot read them"}, ref)
		}
		if g.Stalls > 0 && (g.Connects == 0 || g.Stalls >= 3) {
			g.Signals = append(g.Signals, "stalls")
			d := map[string]string{"stalls": strconv.FormatUint(uint64(g.Stalls), 10), "connects": strconv.FormatUint(uint64(g.Connects), 10), "node": g.NodeName}
			if slices.Contains(g.NotKnown, "responses_unmatched") {
				d["why"] = "not known: the node's agent or its kernel AmneziaWG backend cannot tell an unanswered initiation from a lost answer"
			}
			add("stalls", "warning", d, ref)
		}
		if g.TCPStalls16K >= 2 && g.TCPStalls16K*2 >= g.TCPStalls {
			g.Signals = append(g.Signals, "tcp_freeze_16k")
			add("tcp_freeze_16k", "warning", map[string]string{"tcp_stalls": u32(g.TCPStalls), "tcp_stalls_16k": u32(g.TCPStalls16K), "connects": u32(g.Connects), "node": g.NodeName,
				"why": "the device's VLESS connections stop after the first 8 to 32 KiB: the TSPU's freeze of a TCP stream; a UDP protocol or XHTTP behind a CDN avoids it"}, ref)
		}
		if g.Freezes >= 2 {
			g.Signals = append(g.Signals, "freezes")
			add("freezes", "warning", map[string]string{"freezes": strconv.FormatUint(uint64(g.Freezes), 10), "node": g.NodeName}, ref)
		}
		if loss != nil && *loss >= lossHighPermille && lossDen[k] >= lossMinPackets {
			g.Signals = append(g.Signals, "high_loss")
			add("high_loss", "warning", map[string]string{"loss_permille": strconv.FormatUint(uint64(*loss), 10), "node": g.NodeName}, ref)
		}
		if rtt != nil && *rtt >= rttHighMs && rttN[k] >= rttMinSamples {
			g.Signals = append(g.Signals, "high_rtt")
			add("high_rtt", "info", map[string]string{"rtt_ms": strconv.FormatUint(uint64(*rtt), 10), "node": g.NodeName}, ref)
		}
		switch {
		case slices.Contains(g.Signals, "rejects"):
			g.Verdict = "rejected"
		case slices.Contains(g.Signals, "unanswered"):
			g.Verdict = "unanswered"
		case slices.Contains(g.Signals, "answers_lost"):
			g.Verdict = "answers_lost"
		case slices.Contains(g.Signals, "stalls"):
			g.Verdict = "stalled"
		case slices.Contains(g.Signals, "tcp_freeze_16k"):
			g.Verdict = "throttled"
		case slices.Contains(g.Signals, "freezes"):
			g.Verdict = "frozen"
		case slices.Contains(g.Signals, "high_loss"):
			g.Verdict = "lossy"
		case slices.Contains(g.Signals, "high_rtt"):
			g.Verdict = "slow"
		case g.Connects+g.Rejects+g.Stalls+g.Drops == 0:
			g.Verdict = "no_attempts"
		default:
			g.Verdict = "ok"
		}
		v.Connections = append(v.Connections, *g)
	}

	// the nodes they use: status, checks and the country verdict
	if ov, err := c.cl.Fleet.Overview(c.ctx, connect.NewRequest(&adminv1.OverviewRequest{Range: adminv1.OverviewRange_OVERVIEW_RANGE_24H, EventLimit: 1})); err != nil {
		v.Unavailable = append(v.Unavailable, "node_status")
	} else {
		for _, n := range ov.Msg.GetNodes() {
			if dn := selected[n.GetId()]; dn != nil {
				dn.Status, dn.Reason = nodeStatus(n.GetStatus()), clean(n.GetReason().GetCode(), 60)
				if dn.Name == "" {
					dn.Name = nm(n.GetName())
				}
			}
		}
	}
	if cr, err := c.cl.Health.GetChecks(c.ctx, connect.NewRequest(&adminv1.GetChecksRequest{})); err != nil {
		v.Unavailable = append(v.Unavailable, "checks")
	} else {
		for _, cc := range cr.Msg.GetCountryChecks() {
			if dn := selected[cc.GetNodeId()]; dn != nil {
				dn.Country, dn.Confident = enumName("COUNTRY_VERDICT_", cc.GetVerdict().String()), clean(cc.GetConfidence(), 20)
			}
		}
		for _, row := range cr.Msg.GetRows() {
			if dn := selected[row.GetNodeId()]; dn != nil {
				for _, cell := range row.GetCells() {
					if cell.GetDeployed() && cell.GetFailStreak() > 0 {
						dn.CheckFail, dn.Streak = true, max(dn.Streak, cell.GetFailStreak())
					}
				}
			}
		}
	}
	selIDs := make([]string, 0, len(selected))
	for id := range selected {
		selIDs = append(selIDs, id)
	}
	slices.SortFunc(selIDs, func(a, b string) int { return strings.Compare(selected[a].Name+a, selected[b].Name+b) })
	for _, id := range selIDs {
		dn := selected[id]
		dn.NoCounter = withoutCounters[id]
		v.Nodes = append(v.Nodes, *dn)
		switch {
		case dn.Status == "down":
			add("node_down", "error", map[string]string{"node": dn.Name, "reason": dn.Reason}, "node:"+id)
		case dn.Status != "online" && dn.Status != "unknown":
			add("node_problem", "warning", map[string]string{"node": dn.Name, "status": dn.Status, "reason": dn.Reason}, "node:"+id)
		}
		switch dn.Country {
		case "blocked", "tcp_blocked", "partial":
			sev := "warning"
			if dn.Country != "partial" {
				sev = "error"
			}
			add("node_blocked_in_country", sev, map[string]string{"node": dn.Name, "verdict": dn.Country, "confidence": dn.Confident}, "check:"+id)
		}
	}

	// devices
	for _, d := range deviceViews(ur.GetDevices(), names) {
		dd := diagDeviceV{ID: d.ID, Kind: d.Kind, Platform: d.Platform, Model: d.Model, Client: d.Client, Fingerprint: d.Fingerprint, Online: d.Online,
			OnlineNodes: d.OnlineNodes, FirstSeen: d.FirstSeen, LastSeen: d.LastSeen, Stale: d.Stale, Protocols: d.Protocols, Connects: devConnects[d.ID]}
		_, dd.SeenInWindow = devConnects[d.ID]
		v.Devices = append(v.Devices, dd)
	}
	v.BlockedDevices = blockedDeviceViews(ur.GetBlockedDevices())
	for _, b := range v.BlockedDevices {
		add("device_blocked", "info", map[string]string{"platform": b.Platform, "model": b.Model}, "blocked:"+b.ID)
	}

	// alerts and events about the person
	device := ""
	if al, err := c.cl.Health.ListAlerts(c.ctx, connect.NewRequest(&adminv1.ListAlertsRequest{HistoryWindowS: uint32(hours) * 3600})); err != nil {
		v.Unavailable = append(v.Unavailable, "alerts")
	} else {
		for _, a := range slices.Concat(al.Msg.GetActive(), al.Msg.GetHistory()) {
			if a.GetSubject() != in.UserID {
				continue
			}
			state := "open"
			if a.GetResolvedAtUnix() > 0 {
				state = "resolved"
			}
			opened := a.GetOpenedUnix()
			if opened == 0 {
				opened = a.GetFirstSeenUnix()
			}
			ref := "alert:" + clean(a.GetId(), 60)
			v.Alerts = append(v.Alerts, diagAlertV{Kind: enumName("ALERT_KIND_", a.GetKind().String()), Severity: enumName("ALERT_SEVERITY_", a.GetSeverity().String()), State: state,
				TitleKey: clean(a.GetTitleKey(), 80), OpenedUnix: opened, ResolvedUnix: a.GetResolvedAtUnix(), Ref: ref})
			if state == "open" && a.GetKind() == adminv1.AlertKind_ALERT_KIND_DEVICE_LIMIT {
				device = ref
			}
		}
	}
	evs, capped, err := c.windowEvents(&adminv1.ListEventsRequest{UserId: in.UserID}, since, now)
	if err != nil {
		v.Unavailable = append(v.Unavailable, "events")
	}
	v.EventsCapped = capped
	for i, e := range evs {
		ref := "event:" + strconv.FormatInt(e.GetId(), 10)
		if e.GetCode() == "device_limit" && device == "" {
			device = ref
		}
		if i < 30 {
			v.Events = append(v.Events, diagEventV{At: e.GetTimeUnix(), Severity: eventSev(e.GetSeverity()), Code: clean(e.GetCode(), 80), Node: nm(e.GetNodeName()),
				Params: insMap(e.GetParams(), maxParamValue), Ref: ref})
		}
	}

	// the account itself
	switch {
	case !v.User.Enabled:
		add("disabled", "error", nil, "user:status")
	case v.User.Expired:
		add("expired", "error", map[string]string{"expires_unix": strconv.FormatInt(u.GetExpiresUnix(), 10)}, "user:expires")
	}
	if status == "limited" || u.GetQuotaBytes() > 0 && u.GetUsedBytes() >= u.GetQuotaBytes() {
		add("over_quota", "error", map[string]string{"used_bytes": strconv.FormatUint(u.GetUsedBytes(), 10), "quota_bytes": strconv.FormatUint(u.GetQuotaBytes(), 10)}, "user:quota")
	}
	if u.GetDeviceLimit() > 0 && u.GetDevicesUsed() >= u.GetDeviceLimit() || device != "" {
		ev := []string{"user:devices"}
		if device != "" {
			ev = append(ev, device)
		}
		add("device_limit_reached", "warning", map[string]string{"devices": strconv.FormatUint(uint64(u.GetDevicesUsed()), 10), "limit": strconv.FormatUint(uint64(u.GetDeviceLimit()), 10)}, ev...)
	}
	if len(v.Profiles) == 0 {
		add("no_profiles", "error", nil, "user:profiles")
	}
	if len(selected) == 0 {
		add("no_node_access", "error", nil, "user:nodes")
	}
	total := uint32(0)
	for _, g := range v.Connections {
		total += g.Connects + g.Rejects + g.Stalls + g.Drops + g.Initiations
	}
	switch {
	case u.GetLastSeenUnix() == 0 && len(v.Devices) == 0 && total == 0:
		add("never_connected", "warning", nil, "user:last_seen")
	case countersOK && total == 0 && len(withoutCounters) < len(selected):
		add("no_attempts_in_window", "info", map[string]string{"hours": strconv.Itoa(hours)}, "user:last_seen")
	}
	if len(selected) > 0 && len(withoutCounters) >= len(selected) {
		add("no_counters", "info", nil, "user:nodes")
	}

	slices.SortStableFunc(v.Findings, func(a, b findingV) int { return sevRank(b.Severity) - sevRank(a.Severity) })
	v.Verdict = "no_issue_found"
	if len(v.Findings) > 0 && sevRank(v.Findings[0].Severity) >= 2 {
		v.Verdict = v.Findings[0].Code
	}
	return v, nil
}
