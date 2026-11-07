//go:build !js

package mcp

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

// The insight tools run over an insightWorld: the base fixture world with the feeds an operator asks about (events, alerts,
// journal, audit, counters, series) replaced by data placed around a fixed "now".

const (
	insNow = int64(1_800_000_000)
	nodeC  = "nod_cccccccccccccccccccccccccc" // fi1: quiet
	nodeD  = "nod_dddddddddddddddddddddddddd" // msk1: its trouble is from yesterday
	hour   = int64(3600)
)

type insightWorld struct {
	*world
	events  []*adminv1.Event // newest first
	active  []*adminv1.Alert
	history []*adminv1.Alert
	conn    *adminv1.GetUserConnectionResponse
	checks  *adminv1.GetChecksResponse
	audit   []*adminv1.AuditEntry
}

func ev(id, at int64, sev adminv1.EventSeverity, code, node, name string, params map[string]string) *adminv1.Event {
	return &adminv1.Event{Id: id, TimeUnix: at, Severity: sev, Code: code, NodeId: node, NodeName: name, Params: params}
}

const (
	sInfo = adminv1.EventSeverity_EVENT_SEVERITY_INFO
	sWarn = adminv1.EventSeverity_EVENT_SEVERITY_WARNING
	sErr  = adminv1.EventSeverity_EVENT_SEVERITY_ERROR
)

func newInsightWorld() *insightWorld {
	iw := &insightWorld{world: newWorld()}
	iw.events = []*adminv1.Event{
		ev(18, insNow-300, sWarn, "user_over_quota", "", "", nil),
		ev(17, insNow-400, sInfo, "state_applied", nodeA, "de1", nil),
		ev(16, insNow-500, sWarn, "path_lossy", nodeA, "de1", map[string]string{"target": "nl1", "note": "see " + canaryAddr}),
		ev(15, insNow-900, sInfo, "agent_started", nodeA, "de1", nil),
		ev(14, insNow-1800, sInfo, "node_blip", nodeA, "de1", map[string]string{"minutes": "4", "rebooted": "false"}),
		ev(13, insNow-hour, sInfo, "node_recovered", nodeB, "nl1", map[string]string{"minutes": "62"}),
		ev(12, insNow-2*hour, sErr, "node_down", nodeB, "nl1", map[string]string{"minutes": "2"}),
		ev(6, insNow-29*hour, sInfo, "node_recovered", nodeD, "msk1", map[string]string{"minutes": "65"}),
		ev(5, insNow-30*hour, sErr, "node_down", nodeD, "msk1", map[string]string{"minutes": "5"}),
		ev(4, insNow-26*hour, sErr, "engine_failed", nodeD, "msk1", nil),
	}
	slices.SortFunc(iw.events, func(a, b *adminv1.Event) int { return int(b.GetTimeUnix() - a.GetTimeUnix()) })
	iw.active = []*adminv1.Alert{
		{Id: "alt_down", Severity: adminv1.AlertSeverity_ALERT_SEVERITY_CRITICAL, Kind: adminv1.AlertKind_ALERT_KIND_NODE_DOWN, NodeId: nodeB, NodeName: "nl1",
			TitleKey: "health.alert.node_down.title", OpenedUnix: insNow - 2*hour, FirstSeenUnix: insNow - 2*hour},
		{Id: "alt_limit", Severity: adminv1.AlertSeverity_ALERT_SEVERITY_INFO, Kind: adminv1.AlertKind_ALERT_KIND_DEVICE_LIMIT, Subject: "usr_alice",
			TitleKey: "health.alert.device_limit.title", OpenedUnix: insNow - 5*hour},
	}
	iw.history = []*adminv1.Alert{
		{Id: "alt_loss", Severity: adminv1.AlertSeverity_ALERT_SEVERITY_WARNING, Kind: adminv1.AlertKind_ALERT_KIND_PATH_LOSSY, NodeId: nodeA, NodeName: "de1",
			TitleKey: "health.alert.path_lossy.title", Params: map[string]string{"target": "nl1", "why": canaryVless}, OpenedUnix: insNow - 6*hour, ResolvedAtUnix: insNow - 3*hour, Resolution: "cleared"},
	}
	return iw
}

func (w *insightWorld) ListEvents(_ context.Context, r *connect.Request[adminv1.ListEventsRequest]) (*connect.Response[adminv1.ListEventsResponse], error) {
	var page []*adminv1.Event
	more := false
	for _, e := range w.events {
		m := r.Msg
		if b := m.GetBeforeId(); b != 0 && e.GetId() >= b {
			continue
		}
		if m.GetNodeId() != "" && e.GetNodeId() != m.GetNodeId() || m.GetUserId() != "" && e.GetUserId() != m.GetUserId() || e.GetSeverity() < m.GetMinSeverity() {
			continue
		}
		if uint32(len(page)) == m.GetLimit() {
			more = true
			break
		}
		page = append(page, e)
	}
	return connect.NewResponse(&adminv1.ListEventsResponse{Events: page, HasMore: more}), nil
}

func (w *insightWorld) Overview(context.Context, *connect.Request[adminv1.OverviewRequest]) (*connect.Response[adminv1.OverviewResponse], error) {
	spark := make([]uint64, 24)
	spark[23] = 5 << 20
	card := func(id, name string, st adminv1.NodeStatus, reason string) *adminv1.NodeCard {
		c := &adminv1.NodeCard{Id: id, Name: name, Status: st, SparkBytes: spark}
		if reason != "" {
			c.Reason = &adminv1.StatusReason{Code: reason}
		}
		return c
	}
	var traffic, online []*adminv1.SeriesPoint
	for i := int64(48); i >= 0; i-- { // hourly points, oldest first; the last one is the current hour
		start := (insNow/hour)*hour - i*hour
		traffic = append(traffic, &adminv1.SeriesPoint{StartUnix: start, Values: []*adminv1.ProtocolValue{{Protocol: "hysteria2", Value: 1000}, {Protocol: "vless", Value: 10}}})
		online = append(online, &adminv1.SeriesPoint{StartUnix: start, Values: []*adminv1.ProtocolValue{{Protocol: "hysteria2", Value: 3}}})
	}
	return connect.NewResponse(&adminv1.OverviewResponse{
		NowUnix: insNow, NodesTotal: 4, NodesProblem: 1, UsersOnline: 3, AlertsActive: 1, AlertsCritical: 1,
		Nodes: []*adminv1.NodeCard{
			card(nodeA, "de1", adminv1.NodeStatus_NODE_STATUS_ONLINE, ""),
			card(nodeB, "nl1", adminv1.NodeStatus_NODE_STATUS_DOWN, "agent_offline"),
			card(nodeC, "fi1", adminv1.NodeStatus_NODE_STATUS_ONLINE, ""),
			card(nodeD, "msk1", adminv1.NodeStatus_NODE_STATUS_ONLINE, ""),
		},
		Traffic: traffic, Online: online,
	}), nil
}

func (w *insightWorld) ListAlerts(_ context.Context, r *connect.Request[adminv1.ListAlertsRequest]) (*connect.Response[adminv1.ListAlertsResponse], error) {
	keep := func(in []*adminv1.Alert) (out []*adminv1.Alert) { // a node filter leaves out fleet-wide and user alerts, as the real RPC does
		for _, a := range in {
			if r.Msg.GetNodeId() == "" || a.GetNodeId() == r.Msg.GetNodeId() {
				out = append(out, a)
			}
		}
		return out
	}
	return connect.NewResponse(&adminv1.ListAlertsResponse{NowUnix: insNow, Active: keep(w.active), History: keep(w.history)}), nil
}

func (w *insightWorld) GetNodeJournal(_ context.Context, r *connect.Request[adminv1.GetNodeJournalRequest]) (*connect.Response[adminv1.GetNodeJournalResponse], error) {
	all := []*adminv1.NodeJournalEntry{
		{NodeId: nodeA, NodeName: "de1", HourStartUnix: (insNow / hour) * hour, Level: sWarn, Source: "awg", Message: "handshake failed", InboundId: "inb_1", ProfileName: "Fast",
			Attrs: map[string]string{"detail": "see " + canaryTK}, Count: 2, FirstUnix: insNow - 1500, LastUnix: insNow - 1000},
		{NodeId: nodeA, NodeName: "de1", HourStartUnix: (insNow/hour)*hour - hour, Level: sErr, Source: "xray", Message: "AUTH_FAILED", Count: 1, FirstUnix: insNow - 5000, LastUnix: insNow - 4800},
		{NodeId: nodeD, NodeName: "msk1", HourStartUnix: ((insNow - 40*hour) / hour) * hour, Level: sWarn, Source: "agent", Message: "old trouble", Count: 9, FirstUnix: insNow - 40*hour, LastUnix: insNow - 40*hour + 60},
	}
	var out []*adminv1.NodeJournalEntry
	since := insNow - int64(r.Msg.GetHours())*hour
	for _, e := range all {
		if r.Msg.GetNodeId() != "" && e.GetNodeId() != r.Msg.GetNodeId() || e.GetLevel() < r.Msg.GetMinLevel() {
			continue
		}
		if r.Msg.GetHours() > 0 && e.GetLastUnix() < since { // the real RPC reads the hours back from now
			continue
		}
		out = append(out, e)
	}
	return connect.NewResponse(&adminv1.GetNodeJournalResponse{SinceUnix: since, NowUnix: insNow, Entries: out}), nil
}

func (w *insightWorld) GetUpdates(context.Context, *connect.Request[adminv1.GetUpdatesRequest]) (*connect.Response[adminv1.GetUpdatesResponse], error) {
	return connect.NewResponse(&adminv1.GetUpdatesResponse{NowUnix: insNow, Rollout: &adminv1.Rollout{
		Id: "rol_1", Status: adminv1.RolloutStatus_ROLLOUT_STATUS_DONE, ToVersion: "v2", BatchSize: 1, CreatedUnix: insNow - 4*hour, FinishedUnix: insNow - 3*hour - 1800,
		Steps: []*adminv1.RolloutStep{{NodeId: nodeA, NodeName: "de1", State: adminv1.StepState_STEP_STATE_PASSED}, {NodeId: nodeB, NodeName: "nl1", ErrorKey: "update.failed"}},
	}}), nil
}

func (w *insightWorld) ListAudit(context.Context, *connect.Request[adminv1.ListAuditRequest]) (*connect.Response[adminv1.ListAuditResponse], error) {
	return connect.NewResponse(&adminv1.ListAuditResponse{Entries: w.audit}), nil
}

func (w *insightWorld) GetUserConnection(_ context.Context, r *connect.Request[adminv1.GetUserConnectionRequest]) (*connect.Response[adminv1.GetUserConnectionResponse], error) {
	if w.conn != nil {
		return connect.NewResponse(w.conn), nil
	}
	return connect.NewResponse(&adminv1.GetUserConnectionResponse{SinceUnix: insNow - 24*hour, NowUnix: insNow}), nil
}

func (w *insightWorld) GetChecks(context.Context, *connect.Request[adminv1.GetChecksRequest]) (*connect.Response[adminv1.GetChecksResponse], error) {
	if w.checks != nil {
		return connect.NewResponse(w.checks), nil
	}
	hist := func(failedHoursAgo int64, failed uint32) []*adminv1.CheckBucket {
		return []*adminv1.CheckBucket{{StartUnix: ((insNow / hour) - failedHoursAgo) * hour, Ok: 5, Failed: failed}}
	}
	return connect.NewResponse(&adminv1.GetChecksResponse{NowUnix: insNow, Rows: []*adminv1.CheckRow{
		{NodeId: nodeC, NodeName: "fi1", Cells: []*adminv1.CheckCell{{Deployed: true, History: hist(30, 0)}}},
		{NodeId: nodeD, NodeName: "msk1", Cells: []*adminv1.CheckCell{{Deployed: true, History: hist(30, 4)}}}, // failures yesterday only
		{NodeId: nodeA, NodeName: "de1", Cells: []*adminv1.CheckCell{{Deployed: true, History: hist(2, 3)}}},
	}}), nil
}

func (w *insightWorld) GetReachabilityHistory(context.Context, *connect.Request[adminv1.GetReachabilityHistoryRequest]) (*connect.Response[adminv1.GetReachabilityHistoryResponse], error) {
	return connect.NewResponse(&adminv1.GetReachabilityHistoryResponse{Country: "RU", Nodes: []*adminv1.NodeReachabilityHistory{{NodeId: nodeC, Lanes: []*adminv1.ReachabilityLane{{Source: "globalping",
		Points: []*adminv1.ReachabilityPoint{{AtUnix: insNow - 40*hour, Verdict: adminv1.CountryVerdict_COUNTRY_VERDICT_OK}, {AtUnix: insNow - 30*hour, Verdict: adminv1.CountryVerdict_COUNTRY_VERDICT_BLOCKED}}}}}}}), nil
}

func (w *insightWorld) api(a *fakeAuth) http.Handler {
	mux := http.NewServeMux()
	for _, h := range []func() (string, http.Handler){
		func() (string, http.Handler) { return adminv1connect.NewFleetServiceHandler(w) },
		func() (string, http.Handler) { return adminv1connect.NewNodeServiceHandler(w) },
		func() (string, http.Handler) { return adminv1connect.NewHealthServiceHandler(w) },
		func() (string, http.Handler) { return adminv1connect.NewUserServiceHandler(w) },
		func() (string, http.Handler) { return adminv1connect.NewUpdateServiceHandler(w) },
		func() (string, http.Handler) { return adminv1connect.NewAuthServiceHandler(w) },
		func() (string, http.Handler) { return adminv1connect.NewCountryReachabilityServiceHandler(w) },
	} {
		p, hh := h()
		mux.Handle(p, hh)
	}
	return a.requireSession(mux)
}

// newInsightEnv is newTestEnv over an insightWorld, with the clock at insNow.
func newInsightEnv(t *testing.T) (*testEnv, *insightWorld) {
	t.Helper()
	iw := newInsightWorld()
	e := &testEnv{t: t, w: iw.world, clock: time.Unix(insNow, 0)}
	e.plans = &fakePlans{byID: map[string]*Plan{}, now: e.now}
	e.auth = &fakeAuth{tokens: map[string]*fakeToken{}, plans: e.plans, w: e.w}
	h, err := New(Config{Plans: e.plans, Auth: e.auth, API: iw.api(e.auth), Now: e.now})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", e.auth.bearer(h))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.srv = &httptest.Server{Listener: ln, Config: &http.Server{Handler: mux}}
	e.srv.Start()
	t.Cleanup(e.srv.Close)
	e.url = e.srv.URL + "/mcp"
	return e, iw
}

func codes(items []tlItemV) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Kind+":"+it.Code)
	}
	return out
}

func TestTimelineMergeOrderWindowAndCursor(t *testing.T) {
	e, _ := newInsightEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	out := mustOK(t, s, "timeline", map[string]any{"hours": 24, "limit": 100})
	noCanary(t, "timeline", out)
	tl := decode[TimelineV](t, out)
	since := insNow - 24*hour
	if tl.SinceUnix != since || tl.UntilUnix != insNow {
		t.Fatalf("window = %d..%d", tl.SinceUnix, tl.UntilUnix)
	}
	for i, it := range tl.Items {
		if it.At < since || it.At > insNow {
			t.Errorf("item outside the window: %+v", it)
		}
		if i > 0 && tlBefore(it, tl.Items[i-1]) {
			t.Errorf("items %d and %d are out of order", i-1, i)
		}
		if it.Kind == "audit" {
			t.Errorf("readonly sees an audit item: %+v", it)
		}
	}
	got := strings.Join(codes(tl.Items), " ")
	for _, want := range []string{"event:user_over_quota", "event:path_lossy", "restart:agent_started", "event:node_blip", "event:node_down", "event:node_recovered",
		"alert_opened:node_down", "alert_opened:path_lossy", "alert_resolved:path_lossy", "journal:awg", "journal:xray", "rollout:rollout_started", "rollout:rollout_finished"} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline lacks %s: %s", want, got)
		}
	}
	for _, old := range []string{"engine_failed", "journal:agent"} {
		if strings.Contains(got, old) {
			t.Errorf("yesterday's %s is in today's timeline", old)
		}
	}
	// one cursor walks every source: pages of three add up to the whole
	var walked []tlItemV
	cursor := ""
	for page := 0; page < 20; page++ {
		args := map[string]any{"hours": 24, "limit": 3}
		if cursor != "" {
			args["cursor"] = cursor
		}
		p := decode[TimelineV](t, mustOK(t, s, "timeline", args))
		walked = append(walked, p.Items...)
		if !p.HasMore {
			break
		}
		cursor = p.NextCursor
	}
	if len(walked) != len(tl.Items) {
		t.Fatalf("walked %d items, one page has %d", len(walked), len(tl.Items))
	}
	for i := range walked {
		if walked[i].Ref != tl.Items[i].Ref {
			t.Fatalf("item %d: %s vs %s", i, walked[i].Ref, tl.Items[i].Ref)
		}
	}
	// a wider window brings yesterday in; an explicit from/to takes only its own slice
	wide := decode[TimelineV](t, mustOK(t, s, "timeline", map[string]any{"hours": 48, "limit": 100}))
	if !strings.Contains(strings.Join(codes(wide.Items), " "), "event:engine_failed") {
		t.Errorf("48 hours misses yesterday's engine_failed")
	}
	slice := decode[TimelineV](t, mustOK(t, s, "timeline", map[string]any{"from": insNow - 3*hour, "to": insNow - hour - 100, "limit": 100}))
	for _, it := range slice.Items {
		if it.At < insNow-3*hour || it.At > insNow-hour-100 {
			t.Errorf("from/to slice holds %+v", it)
		}
	}
	// node, kinds and severity filters
	de := decode[TimelineV](t, mustOK(t, s, "timeline", map[string]any{"node": "de1", "hours": 24, "limit": 100}))
	for _, it := range de.Items {
		if it.NodeID != nodeA && it.Kind != "rollout" {
			t.Errorf("node filter let %+v through", it)
		}
	}
	al := decode[TimelineV](t, mustOK(t, s, "timeline", map[string]any{"kinds": []string{"alert"}, "limit": 100}))
	if len(al.Items) != 4 {
		t.Errorf("alert edges = %d, want 4: %v", len(al.Items), codes(al.Items))
	}
	er := decode[TimelineV](t, mustOK(t, s, "timeline", map[string]any{"min_severity": "error", "limit": 100}))
	for _, it := range er.Items {
		if it.Severity != "error" {
			t.Errorf("min_severity error returned %+v", it)
		}
	}
	// user view: events and alerts about the person only
	e.w.users["usr_alice"].User.Id = "usr_alice"
	// bad arguments
	for _, bad := range []map[string]any{{"node": "de1", "user": "usr_alice"}, {"hours": 200}, {"kinds": []string{"nope"}}, {"cursor": "garbage"}, {"hours": 5, "from": insNow - 100}, {"from": insNow, "to": insNow - 5}, {"from": 1, "to": insNow}} {
		mustFail(t, s, "timeline", bad)
	}
}

func TestTimelineAuditSliceIsAdminOnly(t *testing.T) {
	e, iw := newInsightEnv(t)
	iw.audit = []*adminv1.AuditEntry{
		{Id: 9, TimeUnix: insNow - 100, Source: adminv1.AuditSource_AUDIT_SOURCE_PANEL, ActorId: "adm_1", ActorName: "owner", Action: "node_update", Ip: "198.51.100.5",
			ParamsJson: `{"node_id":"` + nodeA + `","address":"` + canaryAddr + `","addr":"2001:db8::5","nested":{"address":"` + canaryAddr6 + `","keep":"yes"}}`},
		{Id: 8, TimeUnix: insNow - 48*hour, Source: adminv1.AuditSource_AUDIT_SOURCE_PANEL, ActorId: "adm_1", Action: "old_change"},
	}
	_, ro := e.token(ProfileReadonly)
	_, ad := e.token(ProfileAdmin)
	_, op := e.token(ProfileOperator)
	for _, p := range []string{ro, op} {
		out := mustOK(t, e.session(p), "timeline", map[string]any{"limit": 100})
		if strings.Contains(out, `"audit"`) || strings.Contains(out, "node_update") {
			t.Fatalf("a non-admin profile sees the audit slice: %s", out)
		}
	}
	if n := len(e.w.calls(adminv1connect.AuthServiceListAuditProcedure)); n != 0 {
		t.Errorf("non-admin timeline called ListAudit %d times", n)
	}
	out := mustOK(t, e.session(ad), "timeline", map[string]any{"limit": 100})
	noCanary(t, "admin timeline", out)
	for _, leak := range []string{"198.51.100.5", "2001:db8::5", `"ip"`} {
		if strings.Contains(out, leak) {
			t.Errorf("audit slice leaks %q: %s", leak, out)
		}
	}
	tl := decode[TimelineV](t, out)
	var rows []tlItemV
	for _, it := range tl.Items {
		if it.Kind == "audit" {
			rows = append(rows, it)
		}
	}
	if len(rows) != 1 || rows[0].Code != "node_update" || rows[0].Ref != "audit:9" || !strings.Contains(rows[0].Params["params"], "keep") {
		t.Fatalf("audit rows = %+v (the old one is outside the window)", rows)
	}
	// changes_only keeps audit, rollouts and change events, and nothing else
	co := decode[TimelineV](t, mustOK(t, e.session(ad), "timeline", map[string]any{"changes_only": true, "limit": 100}))
	got := strings.Join(codes(co.Items), " ")
	for _, want := range []string{"audit:node_update", "rollout:rollout_started", "event:state_applied"} {
		if !strings.Contains(got, want) {
			t.Errorf("changes_only lacks %s: %s", want, got)
		}
	}
	for _, drop := range []string{"journal", "alert_", "agent_started", "node_blip", "path_lossy"} {
		if strings.Contains(got, drop) {
			t.Errorf("changes_only keeps %s: %s", drop, got)
		}
	}
}

func TestTimelineShrinksToTheCap(t *testing.T) {
	e, iw := newInsightEnv(t)
	iw.events = nil
	for i := int64(1); i <= 100; i++ {
		iw.events = append(iw.events, ev(1000-i, insNow-i, sWarn, "stats_stale", nodeA, "de1", map[string]string{"detail": strings.Repeat("x ", 90)}))
	}
	_, secret := e.token(ProfileReadonly)
	out := mustOK(t, e.session(secret), "timeline", map[string]any{"limit": 100})
	if len(out) > maxResultBytes {
		t.Fatalf("result is %d bytes", len(out))
	}
	tl := decode[TimelineV](t, out)
	if !tl.Truncated || !tl.HasMore || tl.NextCursor == "" {
		t.Fatalf("truncated=%v has_more=%v cursor=%q", tl.Truncated, tl.HasMore, tl.NextCursor)
	}
	next := decode[TimelineV](t, mustOK(t, e.session(secret), "timeline", map[string]any{"limit": 100, "cursor": tl.NextCursor}))
	if len(next.Items) == 0 || next.Items[0].At > tl.Items[len(tl.Items)-1].At {
		t.Fatalf("the cursor does not continue after the last item")
	}
}

func TestFleetDigestCountsOnlyTheWindow(t *testing.T) {
	e, _ := newInsightEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	out := mustOK(t, s, "fleet_digest", map[string]any{"hours": 24})
	noCanary(t, "fleet_digest", out)
	d := decode[DigestV](t, out)
	byName := map[string]digestNodeV{}
	for _, n := range d.Problems {
		byName[n.Name] = n
	}
	if len(d.Problems) != 2 || byName["de1"].ID == "" || byName["nl1"].ID == "" {
		t.Fatalf("problem nodes = %v, want de1 and nl1", slices.Collect(func(y func(string) bool) {
			for _, n := range d.Problems {
				if !y(n.Name) {
					return
				}
			}
		}))
	}
	if !slices.Equal(d.Quiet, []string{"fi1", "msk1"}) {
		t.Errorf("quiet = %v: yesterday's outage, failed checks, journal and verdict change must not count today", d.Quiet)
	}
	nl := byName["nl1"]
	if len(nl.Outages) != 1 || nl.Outages[0].Minutes != 62 || nl.Outages[0].Ongoing || nl.Outages[0].MinutesInWindow != 62 {
		t.Errorf("nl1 outages = %+v, want one of 62 minutes", nl.Outages)
	}
	if d.Totals.Outages != 1 || d.Totals.Blips != 1 {
		t.Errorf("totals outages=%d blips=%d, want 1 and 1", d.Totals.Outages, d.Totals.Blips)
	}
	reasons := func(n digestNodeV) string {
		var c []string
		for _, r := range n.Reasons {
			c = append(c, r.Code)
		}
		return strings.Join(c, ",")
	}
	for _, want := range []string{"outage", "status_down", "alert_open:node_down"} {
		if !strings.Contains(reasons(nl), want) {
			t.Errorf("nl1 reasons %q lack %s", reasons(nl), want)
		}
	}
	de := byName["de1"]
	for _, want := range []string{"blip", "path_lossy", "alert_resolved:path_lossy", "journal_warning", "journal_error", "check_failed"} {
		if !strings.Contains(reasons(de), want) {
			t.Errorf("de1 reasons %q lack %s", reasons(de), want)
		}
	}
	if len(de.Blips) != 1 || de.Blips[0].Minutes != 4 || len(de.Journal) != 2 || len(de.Spark) != 24 {
		t.Errorf("de1 blips=%+v journal=%+v spark=%d", de.Blips, de.Journal, len(de.Spark))
	}
	if d.Totals.EventsInWindow != 7 {
		t.Errorf("events in window = %d, want 7 (the three of yesterday are out)", d.Totals.EventsInWindow)
	}
	if d.Totals.AlertsAboutPeople != 1 || len(d.OtherEvents) != 1 || d.OtherEvents[0].Code != "user_over_quota" {
		t.Errorf("people alerts=%d other events=%v", d.Totals.AlertsAboutPeople, d.OtherEvents)
	}
	if len(d.Traffic) != 25 || len(d.Online) != 25 || d.Totals.TrafficBytes != 25*1010 || d.Totals.PeakOnline != 3 {
		t.Errorf("series %d/%d bytes %d peak %d", len(d.Traffic), len(d.Online), d.Totals.TrafficBytes, d.Totals.PeakOnline)
	}
	// 48 hours reaches yesterday: msk1's outage (65 minutes, down 30 hours ago), the engine failure, checks, journal and the verdict change
	wide := decode[DigestV](t, mustOK(t, s, "fleet_digest", map[string]any{"hours": 48}))
	var msk digestNodeV
	for _, n := range wide.Problems {
		if n.Name == "msk1" {
			msk = n
		}
	}
	if len(msk.Outages) != 1 || msk.Outages[0].Minutes != 65 || msk.Outages[0].MinutesInWindow != 65 || !slices.Equal(wide.Quiet, []string{}) && len(wide.Quiet) != 0 {
		t.Errorf("48 h: msk1 outages=%+v quiet=%v", msk.Outages, wide.Quiet)
	}
	for _, want := range []string{"engine_failed", "check_failed", "journal_warning"} {
		if !strings.Contains(reasons(msk), want) {
			t.Errorf("48 h: msk1 reasons %q lack %s", reasons(msk), want)
		}
	}
	for _, bad := range []map[string]any{{"hours": 73}, {"hours": -1}} {
		mustFail(t, s, "fleet_digest", bad)
	}
}

func TestFleetDigestOutageClippedAndOngoing(t *testing.T) {
	e, iw := newInsightEnv(t)
	// down 26 hours ago, back 20 hours ago: it began before the 24 hour window and only 4 hours of it are inside
	iw.events = []*adminv1.Event{
		ev(3, insNow-20*hour, sInfo, "node_recovered", nodeD, "msk1", map[string]string{"minutes": "360"}),
		ev(2, insNow-26*hour, sErr, "node_down", nodeD, "msk1", map[string]string{"minutes": "0"}),
	}
	iw.active, iw.history = nil, nil
	_, secret := e.token(ProfileReadonly)
	d := decode[DigestV](t, mustOK(t, e.session(secret), "fleet_digest", nil))
	var msk digestNodeV
	for _, n := range d.Problems {
		if n.Name == "msk1" {
			msk = n
		}
	}
	if len(msk.Outages) != 1 || !msk.Outages[0].BeganBefore || msk.Outages[0].Minutes != 360 || msk.Outages[0].MinutesInWindow != 4*60 {
		t.Fatalf("clipped outage = %+v", msk.Outages)
	}
	// still down, no event in the window: the node_down alert dates it
	iw.events = nil
	iw.active = []*adminv1.Alert{{Id: "alt_d", Kind: adminv1.AlertKind_ALERT_KIND_NODE_DOWN, Severity: adminv1.AlertSeverity_ALERT_SEVERITY_CRITICAL, NodeId: nodeB, NodeName: "nl1", OpenedUnix: insNow - 30*hour}}
	d = decode[DigestV](t, mustOK(t, e.session(secret), "fleet_digest", nil))
	for _, n := range d.Problems {
		if n.Name == "nl1" && (len(n.Outages) != 1 || !n.Outages[0].Ongoing || n.Outages[0].MinutesInWindow != 24*60 || n.Outages[0].Minutes != 30*60) {
			t.Fatalf("ongoing outage = %+v", n.Outages)
		}
	}
}

func finding(v DiagnoseV, code string) *findingV {
	for i := range v.Findings {
		if v.Findings[i].Code == code {
			return &v.Findings[i]
		}
	}
	return nil
}

func diagnose(t *testing.T, e *testEnv, user string) (DiagnoseV, string) {
	t.Helper()
	_, secret := e.token(ProfileReadonly)
	out := mustOK(t, e.session(secret), "user_diagnose", map[string]any{"user_id": user})
	noCanary(t, "user_diagnose", out)
	return decode[DiagnoseV](t, out), out
}

func TestUserDiagnoseFindings(t *testing.T) {
	row := func(dev, node, name string, f func(r *adminv1.DeviceConnection)) *adminv1.DeviceConnection {
		r := &adminv1.DeviceConnection{DeviceId: dev, NodeId: node, NodeName: name, InboundId: "inb_1", Protocol: "hysteria2", Platform: "android", Client: "Happ/3.4.2", Kind: "link", Connects: 5, OnlineS: 600}
		if f != nil {
			f(r)
		}
		return r
	}
	cases := []struct {
		name  string
		setup func(w *insightWorld)
		want  []string // finding codes that must be present
		not   []string
		check func(t *testing.T, d DiagnoseV)
	}{
		{"healthy", func(w *insightWorld) {
			w.active = nil
			w.conn = &adminv1.GetUserConnectionResponse{Rows: []*adminv1.DeviceConnection{row("dev_link", nodeA, "de1", nil)}}
		}, nil, []string{"rejects_auth", "expired", "high_loss", "never_connected", "device_limit_reached"}, func(t *testing.T, d DiagnoseV) {
			if d.Verdict != "no_issue_found" || len(d.Connections) != 1 || d.Connections[0].Verdict != "ok" {
				t.Errorf("verdict %q connections %+v", d.Verdict, d.Connections)
			}
			if len(d.Nodes) != 1 || d.Nodes[0].Name != "de1" || d.Nodes[0].Status != "online" || len(d.Profiles) != 2 {
				t.Errorf("nodes %+v profiles %+v", d.Nodes, d.Profiles)
			}
		}},
		{"expired", func(w *insightWorld) {
			u := w.users["usr_alice"].User
			u.Status, u.ExpiresUnix = adminv1.UserStatus_USER_STATUS_EXPIRED, insNow-hour
		}, []string{"expired"}, nil, func(t *testing.T, d DiagnoseV) {
			if d.Verdict != "expired" || !d.User.Expired || finding(d, "expired").Evidence[0] != "user:expires" {
				t.Errorf("verdict %q user %+v", d.Verdict, d.User)
			}
		}},
		{"disabled", func(w *insightWorld) { w.users["usr_alice"].User.Status = adminv1.UserStatus_USER_STATUS_DISABLED }, []string{"disabled"}, []string{"expired"}, nil},
		{"over quota", func(w *insightWorld) { w.users["usr_alice"].User.UsedBytes = 10 << 30 }, []string{"over_quota"}, nil, nil},
		{"device limit", func(w *insightWorld) {
			w.users["usr_alice"].User.DeviceLimit, w.users["usr_alice"].User.DevicesUsed = 3, 3
		}, []string{"device_limit_reached"}, nil, func(t *testing.T, d DiagnoseV) {
			f := finding(d, "device_limit_reached")
			if !slices.Contains(f.Evidence, "alert:alt_limit") || !slices.Contains(f.Evidence, "user:devices") {
				t.Errorf("evidence %v", f.Evidence)
			}
		}},
		{"never connected", func(w *insightWorld) {
			u := w.users["usr_alice"]
			u.Devices, u.User.LastSeenUnix, u.BlockedDevices = nil, 0, nil
		}, []string{"never_connected"}, []string{"no_attempts_in_window"}, nil},
		{"rejected key", func(w *insightWorld) {
			w.conn = &adminv1.GetUserConnectionResponse{Rows: []*adminv1.DeviceConnection{row("dev_link", nodeA, "de1", func(r *adminv1.DeviceConnection) { r.Rejects = 7 })}}
		}, []string{"rejects_auth"}, nil, func(t *testing.T, d DiagnoseV) {
			if d.Verdict != "rejects_auth" || d.Connections[0].Verdict != "rejected" || finding(d, "rejects_auth").Evidence[0] != "conn:dev_link:"+nodeA {
				t.Errorf("verdict %q conns %+v findings %+v", d.Verdict, d.Connections, d.Findings)
			}
		}},
		{"loss stalls freezes", func(w *insightWorld) {
			w.conn = &adminv1.GetUserConnectionResponse{Rows: []*adminv1.DeviceConnection{
				row("dev_link", nodeA, "de1", func(r *adminv1.DeviceConnection) {
					r.LossPermille, r.PktsSent, r.RttMs, r.RttSamples, r.Freezes = 120, 1000, 800, 10, 3
				}),
				row("dev_1", nodeA, "de1", func(r *adminv1.DeviceConnection) { r.Connects, r.Stalls, r.Protocol = 0, 9, "awg" }),
			}}
		}, []string{"high_loss", "high_rtt", "freezes", "stalls"}, []string{"rejects_auth"}, func(t *testing.T, d DiagnoseV) {
			if d.Connections[0].DeviceID != "dev_1" || d.Connections[0].Verdict != "stalled" || d.Connections[1].Verdict != "frozen" {
				t.Errorf("connections %+v", d.Connections)
			}
			if l := d.Connections[1].LossPermille; l == nil || *l != 120 {
				t.Errorf("loss = %v", l)
			}
		}},
		{"node blocked and down", func(w *insightWorld) {
			w.users["usr_alice"].NodeAccess = append(w.users["usr_alice"].NodeAccess, &adminv1.NodeAccess{NodeId: nodeB, NodeName: "nl1", Selected: true, Protocols: []string{"vless"}})
			w.checks = &adminv1.GetChecksResponse{NowUnix: insNow,
				CountryChecks: []*adminv1.CountryCheck{{NodeId: nodeA, Verdict: adminv1.CountryVerdict_COUNTRY_VERDICT_TCP_BLOCKED, Confidence: "eyeball", Country: "RU"}},
				Rows:          []*adminv1.CheckRow{{NodeId: nodeB, NodeName: "nl1", Cells: []*adminv1.CheckCell{{Deployed: true, FailStreak: 4}}}}}
		}, []string{"node_blocked_in_country", "node_down"}, nil, func(t *testing.T, d DiagnoseV) {
			if f := finding(d, "node_blocked_in_country"); f.Evidence[0] != "check:"+nodeA || f.Detail["verdict"] != "tcp_blocked" {
				t.Errorf("blocked finding %+v", f)
			}
			if len(d.Nodes) != 2 || d.Nodes[1].Name != "nl1" || !d.Nodes[1].CheckFail || d.Nodes[1].Streak != 4 {
				t.Errorf("nodes %+v", d.Nodes)
			}
		}},
		{"no access", func(w *insightWorld) {
			w.users["usr_alice"].NodeAccess, w.users["usr_alice"].Profiles = nil, nil
		}, []string{"no_node_access", "no_profiles"}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, iw := newInsightEnv(t)
			tc.setup(iw)
			d, _ := diagnose(t, e, "usr_alice")
			for _, c := range tc.want {
				if finding(d, c) == nil {
					t.Errorf("no %s finding: %+v", c, d.Findings)
				}
			}
			for _, c := range tc.not {
				if finding(d, c) != nil {
					t.Errorf("unexpected %s finding", c)
				}
			}
			for _, f := range d.Findings {
				if len(f.Evidence) == 0 || f.Severity == "" {
					t.Errorf("finding %+v has no evidence or severity", f)
				}
			}
			if tc.check != nil {
				tc.check(t, d)
			}
		})
	}
}

func TestUserDiagnoseSecretsAndErrors(t *testing.T) {
	e, iw := newInsightEnv(t)
	u := iw.users["usr_alice"]
	u.Devices[0].Address = canaryAddr // a device's tunnel address
	u.Devices[0].Id = "dev_1"
	iw.events = []*adminv1.Event{{Id: 1, TimeUnix: insNow - 100, Severity: sWarn, Code: "user_over_quota", UserId: "usr_alice", UserName: "alice",
		Params: map[string]string{"ip": canaryAddr, "note": "see " + canaryVless + " " + canaryTK + " " + canaryKey + " " + canaryAddr6}}}
	iw.active = []*adminv1.Alert{{Id: "alt_u", Kind: adminv1.AlertKind_ALERT_KIND_ACCESS_ENDED, Subject: "usr_alice", TitleKey: "k " + canaryTK, OpenedUnix: insNow - hour}}
	iw.conn = &adminv1.GetUserConnectionResponse{Rows: []*adminv1.DeviceConnection{{DeviceId: "dev_1", NodeId: nodeA, NodeName: "de1", ProfileName: "Home " + canaryTK, Connects: 1, Platform: "ios"}}}
	d, out := diagnose(t, e, "usr_alice")
	if len(d.Events) != 1 || len(d.Alerts) != 1 {
		t.Fatalf("events %d alerts %d", len(d.Events), len(d.Alerts))
	}
	for _, leak := range []string{"10.8.0.2", "address", "last_handshake", "subscription", "password", "psk"} {
		if strings.Contains(strings.ToLower(out), leak) {
			t.Errorf("user_diagnose output holds %q: %s", leak, out)
		}
	}
	for _, bad := range []map[string]any{{"user_id": "bad id!"}, {"user_id": "usr_nobody"}, {"user_id": "usr_alice", "hours": 500}} {
		_, secret := e.token(ProfileReadonly)
		mustFail(t, e.session(secret), "user_diagnose", bad)
	}
	// a node-less user: no alert subject match leaves the lists empty, not null
	d2, out2 := diagnose(t, e, "usr_n0")
	if strings.Contains(out2, ":null") || len(d2.Alerts) != 0 {
		t.Errorf("empty lists are null or non-empty: %s", out2)
	}
}

func TestInsightToolsAreInTheRegistry(t *testing.T) {
	e, _ := newInsightEnv(t)
	for _, p := range []Profile{ProfileReadonly, ProfileOperator, ProfileAdmin} {
		_, secret := e.token(p)
		names := toolNames(t, e.session(secret))
		for _, n := range []string{"timeline", "fleet_digest", "user_diagnose"} {
			if !slices.Contains(names, n) {
				t.Errorf("%s does not see %s", p, n)
			}
		}
	}
	entries := map[Profile]int{}
	for _, td := range registry {
		if td.name == "timeline" {
			entries[td.min]++
			if td.min == ProfileAdmin && !slices.Contains(td.procs, adminv1connect.AuthServiceListAuditProcedure) {
				t.Error("the admin timeline entry does not list ListAudit")
			}
			if td.min == ProfileReadonly && slices.Contains(td.procs, adminv1connect.AuthServiceListAuditProcedure) {
				t.Error("the readonly timeline entry lists ListAudit")
			}
		}
	}
	if entries[ProfileReadonly] != 1 || entries[ProfileAdmin] != 1 {
		t.Errorf("timeline entries = %v", entries)
	}
}

// events_search shared the gap the timeline closed: a free-text event param could carry a bare address.
func TestEventsSearchDropsAddressesFromParams(t *testing.T) {
	v := eventsView([]*adminv1.Event{{Id: 1, TimeUnix: 1, Code: "relay_down", Params: map[string]string{"error": "dial 198.51.100.7:443 refused", "v6": "to 2001:db8::5 failed", "port": "443"}}}, "", false)
	got := v.Events[0].Params
	if strings.Contains(got["error"], "198.51.100.7") || strings.Contains(got["v6"], "2001:db8::5") || got["port"] != "443" {
		t.Fatalf("event params kept an address or lost a plain value: %v", got)
	}
}
