package health

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func req[T any](m *T) *connect.Request[T] { return connect.NewRequest(m) }

func wantCode(t *testing.T, err error, code connect.Code, msgPart string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %v, got success", code)
	}
	if got := connect.CodeOf(err); got != code || !strings.Contains(err.Error(), msgPart) {
		t.Fatalf("want %v %q, got %v (%v)", code, msgPart, got, err)
	}
}

func TestListAlerts(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.node("nl1", "hostera", false)
	r := rpc{e.s}
	e.report("de1", false, res("resolver", dWarn, "set_resolver"))
	e.clock.Advance(time.Minute)
	e.report("de1", true, res("kernel_headers", dWarn, ""))
	e.clock.Advance(time.Minute)
	e.report("de1", true, res("disk_space", dFail, "journald_vacuum"))
	e.exec(`UPDATE node SET last_seen_at = 1, last_connected_at = 1 WHERE id = 'nl1'`)
	e.clock.Advance(time.Second)
	e.evaluate() // nl1 is down

	resp, err := r.ListAlerts(e.ctx, req(&adminv1.ListAlertsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, a := range resp.Msg.Active {
		order = append(order, a.Kind.String()+"/"+a.NodeName+"/"+a.Subject)
	}
	// critical first (newest first inside), then the rest by newest
	wantOrder := []string{"ALERT_KIND_NODE_DOWN/nl1/", "ALERT_KIND_DOCTOR_FAIL/de1/disk_space", "ALERT_KIND_DOCTOR_WARN/de1/kernel_headers", "ALERT_KIND_DOCTOR_WARN/de1/resolver"}
	if strings.Join(order, " ") != strings.Join(wantOrder, " ") {
		t.Fatalf("order %v, want %v", order, wantOrder)
	}
	by := map[string]*adminv1.Alert{}
	for _, a := range resp.Msg.Active {
		by[a.Subject+a.NodeName] = a
	}
	if got := strings.Join(by["disk_spacede1"].Actions, ","); got != "apply_fix:journald_vacuum,open_node,mute" {
		t.Fatalf("doctor fix actions: %s", got)
	}
	if got := strings.Join(by["nl1"].Actions, ","); got != "open_node,mute" {
		t.Fatalf("node down actions: %s", got)
	}
	// a warning can be accepted as normal for the node
	if a := by["kernel_headersde1"]; a.Severity != adminv1.AlertSeverity_ALERT_SEVERITY_INFO || strings.Join(a.Actions, ",") != "open_node,accept,mute" {
		t.Fatalf("info alert: %+v", a)
	}

	// the fix is not offered while the node cannot apply it
	e.fl.set("de1", liveState{up: true}) // an agent without the doctor capability
	resp, _ = r.ListAlerts(e.ctx, req(&adminv1.ListAlertsRequest{}))
	for _, a := range resp.Msg.Active {
		for _, act := range a.Actions {
			if strings.HasPrefix(act, "apply_fix") {
				t.Fatalf("a fix is offered for an agent that cannot apply it: %+v", a)
			}
		}
	}

	// filter by node
	resp, _ = r.ListAlerts(e.ctx, req(&adminv1.ListAlertsRequest{NodeId: "nl1"}))
	if len(resp.Msg.Active) != 1 || resp.Msg.Active[0].NodeId != "nl1" {
		t.Fatalf("filtered: %+v", resp.Msg.Active)
	}

	// history: resolved ones, newest first, no buttons but open_node; the window is clamped at 30 days
	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor}})
	e.clock.Advance(time.Minute)
	e.report("de1", true, res("disk_space", dOK, ""))
	resp, _ = r.ListAlerts(e.ctx, req(&adminv1.ListAlertsRequest{HistoryWindowS: 365 * 86400}))
	if len(resp.Msg.History) != 1 || resp.Msg.History[0].Subject != "disk_space" || resp.Msg.History[0].Resolution != "cleared" ||
		strings.Join(resp.Msg.History[0].Actions, ",") != "open_node" || resp.Msg.History[0].ResolvedAtUnix == 0 {
		t.Fatalf("history: %+v", resp.Msg.History)
	}
	e.clock.Advance(8 * 24 * time.Hour) // outside the default 7 days
	resp, _ = r.ListAlerts(e.ctx, req(&adminv1.ListAlertsRequest{}))
	if len(resp.Msg.History) != 0 {
		t.Fatalf("history beyond the window: %+v", resp.Msg.History)
	}
}

func TestNewAlertKindsUseTheirProtoNames(t *testing.T) {
	for _, tc := range []struct {
		stored string
		proto  adminv1.AlertKind
	}{
		{kAccessEnded, adminv1.AlertKind_ALERT_KIND_ACCESS_ENDED},
		{kUserConnection, adminv1.AlertKind_ALERT_KIND_USER_CONNECTION},
		{kUsersImpacted, adminv1.AlertKind_ALERT_KIND_USERS_IMPACTED},
	} {
		if got := kindProto[tc.stored]; got != tc.proto {
			t.Errorf("kindProto[%q] = %s, want %s", tc.stored, got, tc.proto)
		}
	}
}

func TestMuteAlertRPC(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	r := rpc{e.s}
	e.report("de1", false, res("disk_space", dFail, ""))
	id := e.active()["doctor_fail/de1/disk_space"].ID

	_, err := r.MuteAlert(e.ctx, req(&adminv1.MuteAlertRequest{AlertId: id, DurationS: 7*86400 + 1}))
	wantCode(t, err, connect.CodeInvalidArgument, "604800")
	_, err = r.MuteAlert(e.ctx, req(&adminv1.MuteAlertRequest{AlertId: "alt_nope", DurationS: 60}))
	wantCode(t, err, connect.CodeNotFound, "alert")

	m, err := r.MuteAlert(e.ctx, req(&adminv1.MuteAlertRequest{AlertId: id, DurationS: 3600}))
	if err != nil || m.Msg.Alert.MutedUntilUnix != e.clock.Now().Add(time.Hour).Unix() {
		t.Fatalf("mute: %+v %v", m, err)
	}
	if n, _ := e.s.AlertCounts(e.ctx); n != 0 {
		t.Fatal("muted alert in the badge")
	}
	m, err = r.MuteAlert(e.ctx, req(&adminv1.MuteAlertRequest{AlertId: id, DurationS: 0}))
	if err != nil || m.Msg.Alert.MutedUntilUnix != 0 {
		t.Fatalf("unmute: %+v %v", m, err)
	}
	e.clock.Advance(time.Second)
	e.report("de1", false, res("disk_space", dOK, ""))
	_, err = r.MuteAlert(e.ctx, req(&adminv1.MuteAlertRequest{AlertId: id, DurationS: 60}))
	wantCode(t, err, connect.CodeFailedPrecondition, "resolved")
	if rows, _ := e.st.ListAudit(e.ctx, "", 0, 10); len(rows) != 2 || rows[0].Action != "health.mute_alert" || rows[0].Actor != "adm_test" {
		t.Fatalf("audit: %+v", rows)
	}
}

func TestGetChecksMatrix(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.node("nl1", "hostera", false)
	a := e.inbound("de1", 443) // profile hy2-de1-b
	b := e.inbound("de1", 8443)
	c := e.inbound("nl1", 443)
	e.exec(`UPDATE inbound SET enabled = 0 WHERE id = ?`, b)
	e.s.invalidateSnapshot()

	now := e.clock.Now()
	cur := now.Unix() - now.Unix()%1800
	for _, x := range []struct {
		at  time.Time
		st  adminv1.CheckStatus
		lat uint32
	}{
		{time.Unix(cur+10, 0), cOK, 100}, {time.Unix(cur+20, 0), cOK, 300}, {time.Unix(cur+30, 0), cOK, 200}, {time.Unix(cur+40, 0), cFail, 0},
		{time.Unix(cur-1800*5+5, 0), cOK, 70}, {time.Unix(cur-1800*47+5, 0), cDeg, 90}, {time.Unix(cur-1800*48+5, 0), cOK, 10}, // the last one is outside
	} {
		e.s.record(e.ctx, a, Result{Status: x.st, At: x.at, LatencyMS: x.lat, ExitIP: "203.0.113.9", ExitCountry: "DE"})
	}
	e.s.record(e.ctx, a, Result{Status: cFail, At: now, ErrorCode: "tls", ErrorDetail: "certificate pin mismatch"})

	resp, err := rpc{e.s}.GetChecks(e.ctx, req(&adminv1.GetChecksRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := resp.Msg
	if m.IntervalS != 300 || m.NowUnix != now.Unix() || len(m.Columns) != 3 || len(m.Rows) != 2 {
		t.Fatalf("shape: interval %d columns %d rows %d", m.IntervalS, len(m.Columns), len(m.Rows))
	}
	for i := 1; i < len(m.Columns); i++ {
		if strings.ToLower(m.Columns[i-1].ProfileName) > strings.ToLower(m.Columns[i].ProfileName) {
			t.Fatalf("columns not in name order: %+v", m.Columns)
		}
	}
	for _, col := range m.Columns {
		if col.Protocol != "hysteria2" || col.ProfileId == "" {
			t.Fatalf("column %+v", col)
		}
	}
	if m.Rows[0].NodeName != "de1" || m.Rows[1].NodeName != "nl1" || m.Rows[1].NodeStatus != adminv1.NodeStatus_NODE_STATUS_DOWN {
		t.Fatalf("rows: %+v", m.Rows)
	}
	deployed := func(row *adminv1.CheckRow) map[string]*adminv1.CheckCell {
		out := map[string]*adminv1.CheckCell{}
		for _, cell := range row.Cells {
			if cell.Deployed {
				out[cell.InboundId] = cell
			}
		}
		return out
	}
	de, nl := deployed(m.Rows[0]), deployed(m.Rows[1])
	if len(m.Rows[0].Cells) != 3 || len(de) != 2 || len(nl) != 1 {
		t.Fatalf("cells: de1 %d/%d nl1 %d", len(m.Rows[0].Cells), len(de), len(nl))
	}
	for _, cell := range m.Rows[0].Cells {
		if !cell.Deployed && (cell.InboundId != "" || cell.Last != nil || len(cell.History) != 0) {
			t.Fatalf("an empty cell is not empty: %+v", cell)
		}
	}
	ca := de[a]
	if ca.FailStreak != 1 || ca.Last.Status != cFail || ca.Last.ErrorCode != "tls" || ca.Last.AtUnix != now.Unix() {
		t.Fatalf("cell a: %+v", ca)
	}
	if len(ca.History) != 48 {
		t.Fatalf("%d buckets", len(ca.History))
	}
	for i, bk := range ca.History {
		if bk.StartUnix != cur-47*1800+int64(i)*1800 || bk.StartUnix%1800 != 0 {
			t.Fatalf("bucket %d starts at %d", i, bk.StartUnix)
		}
	}
	last := ca.History[47]
	if last.Ok != 3 || last.Failed != 2 || last.LatencyMs != 200 { // median of 100, 200, 300
		t.Fatalf("current bucket: %+v", last)
	}
	if first := ca.History[0]; first.Ok != 1 || first.Failed != 0 || first.LatencyMs != 90 { // degraded counts as a working round
		t.Fatalf("first bucket: %+v", first)
	}
	if b5 := ca.History[42]; b5.Ok != 1 || b5.LatencyMs != 70 {
		t.Fatalf("bucket 42: %+v", b5)
	}
	// not probed on purpose: disabled inbound, offline node
	if s := de[b].Last; s.Status != adminv1.CheckStatus_CHECK_STATUS_SKIPPED || s.ErrorCode != "inbound_disabled" {
		t.Fatalf("disabled: %+v", s)
	}
	if s := nl[c].Last; s.Status != adminv1.CheckStatus_CHECK_STATUS_SKIPPED || s.ErrorCode != "node_offline" {
		t.Fatalf("offline: %+v", s)
	}

	// A profile the node could not start is not "off": the matrix gets its own code for it, and one for a profile that is
	// still starting, so neither looks like a profile the owner switched off.
	e.exec(`UPDATE inbound SET enabled = 1, state = 'failed', last_error = 'listen udp :8443: bind: address already in use' WHERE id = ?`, b)
	e.s.invalidateSnapshot()
	skipOf := func(id string) string {
		t.Helper()
		r, err := rpc{e.s}.GetChecks(e.ctx, req(&adminv1.GetChecksRequest{NodeId: "de1"}))
		if err != nil {
			t.Fatal(err)
		}
		for _, cell := range r.Msg.Rows[0].Cells {
			if cell.InboundId == id && cell.Last != nil && cell.Last.Status == adminv1.CheckStatus_CHECK_STATUS_SKIPPED {
				return cell.Last.ErrorCode
			}
		}
		return ""
	}
	if got := skipOf(b); got != "inbound_failed" {
		t.Errorf("failed inbound: %q", got)
	}
	e.exec(`UPDATE inbound SET state = 'pending', last_error = '' WHERE id = ?`, b)
	e.s.invalidateSnapshot()
	if got := skipOf(b); got != "inbound_pending" {
		t.Errorf("starting inbound: %q", got)
	}

	one, err := rpc{e.s}.GetChecks(e.ctx, req(&adminv1.GetChecksRequest{NodeId: "nl1"}))
	if err != nil || len(one.Msg.Rows) != 1 || len(one.Msg.Columns) != 1 {
		t.Fatalf("one node: %+v %v", one, err)
	}
	_, err = rpc{e.s}.GetChecks(e.ctx, req(&adminv1.GetChecksRequest{NodeId: "nod_nope"}))
	wantCode(t, err, connect.CodeNotFound, "node")
}

func TestGetDoctorViews(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.node("old1", "hostera", true)
	e.fl.set("old1", liveState{up: true}) // connected, no doctor
	e.node("off1", "hostera", false)
	e.report("off1", false, res("resolver", dWarn, "set_resolver"))
	e.clock.Advance(time.Minute)
	disk := res("disk_space", dWarn, "journald_vacuum", "used_pct", "85")
	disk.DetailCode = "disk_space.usage"
	clock := res("time_sync", dOK, "")
	clock.DetailCode = "Not A Code!" // what the node says is cleaned before it is stored: a malformed code is dropped
	e.report("de1", false, clock, disk, res("zzz_new_check", dSkip, ""), res("dstate_tasks", dFail, "", "hint", "qxl_ttm"))
	e.clock.Advance(90 * time.Second)

	resp, err := rpc{e.s}.GetDoctor(e.ctx, req(&adminv1.GetDoctorRequest{}))
	if err != nil || len(resp.Msg.Nodes) != 3 {
		t.Fatalf("%+v %v", resp, err)
	}
	by := map[string]*adminv1.NodeDoctor{}
	for _, n := range resp.Msg.Nodes {
		by[n.NodeName] = n
	}
	de := by["de1"]
	if !de.AgentSupported || !de.HasReport || de.Stale || de.AgeS != 90 || de.NodeStatus != adminv1.NodeStatus_NODE_STATUS_ONLINE {
		t.Fatalf("de1: %+v", de)
	}
	var ids []string
	for _, it := range de.Items {
		ids = append(ids, it.Id)
	}
	if strings.Join(ids, ",") != "disk_space,dstate_tasks,time_sync,zzz_new_check" { // the CHECK IDS order, unknown ids last
		t.Fatalf("item order %v", ids)
	}
	if it := de.Items[0]; it.FixId != "journald_vacuum" || it.WhyKey != "health.doctor.disk_space.why" || it.Params["used_pct"] != "85" || it.Detail == "" || it.TitleKey != "doctor.disk_space.title" {
		t.Fatalf("disk_space: %+v", it)
	}
	if it := de.Items[1]; it.WhyKey != "health.doctor.dstate_tasks.why.qxl_ttm" || it.FixId != "" {
		t.Fatalf("dstate_tasks: %+v", it)
	}
	if de.Items[2].WhyKey != "" { // OK rows need no explanation
		t.Fatalf("ok row: %+v", de.Items[2])
	}
	// the detail code travels with the English line; a row without one (an older agent) has none
	if de.Items[0].DetailCode != "disk_space.usage" || de.Items[1].DetailCode != "" || de.Items[2].DetailCode != "" {
		t.Fatalf("detail codes: %q %q %q", de.Items[0].DetailCode, de.Items[1].DetailCode, de.Items[2].DetailCode)
	}

	if o := by["old1"]; o.AgentSupported || o.HasReport || len(o.Items) != 0 {
		t.Fatalf("old agent: %+v", o)
	}
	off := by["off1"]
	if !off.AgentSupported || !off.HasReport || off.Items[0].FixId != "" || off.NodeStatus != adminv1.NodeStatus_NODE_STATUS_DOWN {
		t.Fatalf("offline node keeps its report but not the fix: %+v", off)
	}

	e.clock.Advance(30 * time.Minute)
	resp, _ = rpc{e.s}.GetDoctor(e.ctx, req(&adminv1.GetDoctorRequest{NodeId: "de1"}))
	if len(resp.Msg.Nodes) != 1 || !resp.Msg.Nodes[0].Stale || resp.Msg.Nodes[0].AgeS < 30*60 {
		t.Fatalf("stale: %+v", resp.Msg.Nodes)
	}
	_, err = rpc{e.s}.GetDoctor(e.ctx, req(&adminv1.GetDoctorRequest{NodeId: "nod_nope"}))
	wantCode(t, err, connect.CodeNotFound, "node")
}

func TestRunDoctor(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.node("nl1", "hostera", true)
	e.node("old1", "hostera", true)
	e.fl.set("old1", liveState{up: true})
	e.node("off1", "hostera", false)
	e.report("off1", false, res("ipv6", dWarn, ""))
	var mu sync.Mutex
	ran := map[string]int{}
	e.fl.doctor = func(_ context.Context, node string, checks []string) (*agentv1.DoctorReport, error) {
		mu.Lock()
		ran[node]++
		mu.Unlock()
		if len(checks) != 0 {
			t.Errorf("checks were limited: %v", checks)
		}
		if node == "nl1" {
			return &agentv1.DoctorReport{Error: "busy"}, nil
		}
		rep := &agentv1.DoctorReport{Results: []*agentv1.DoctorResult{res("disk_space", dWarn, "")}}
		e.s.DoctorReport(context.Background(), node, rep) // the fleet hands the report over before it answers
		return rep, nil
	}

	resp, err := rpc{e.s}.RunDoctor(e.ctx, req(&adminv1.RunDoctorRequest{NodeId: "de1"}))
	if err != nil || len(resp.Msg.Nodes) != 1 || resp.Msg.Nodes[0].Items[0].Id != "disk_space" {
		t.Fatalf("one node: %+v %v", resp, err)
	}
	_, err = rpc{e.s}.RunDoctor(e.ctx, req(&adminv1.RunDoctorRequest{NodeId: "nl1"}))
	wantCode(t, err, connect.CodeFailedPrecondition, "busy")
	_, err = rpc{e.s}.RunDoctor(e.ctx, req(&adminv1.RunDoctorRequest{NodeId: "old1"}))
	wantCode(t, err, connect.CodeFailedPrecondition, "agent too old")
	_, err = rpc{e.s}.RunDoctor(e.ctx, req(&adminv1.RunDoctorRequest{NodeId: "off1"}))
	wantCode(t, err, connect.CodeFailedPrecondition, "agent too old")
	_, err = rpc{e.s}.RunDoctor(e.ctx, req(&adminv1.RunDoctorRequest{NodeId: "nod_nope"}))
	wantCode(t, err, connect.CodeNotFound, "node")

	ran = map[string]int{}
	resp, err = rpc{e.s}.RunDoctor(e.ctx, req(&adminv1.RunDoctorRequest{}))
	if err != nil || len(resp.Msg.Nodes) != 4 {
		t.Fatalf("fleet: %+v %v", resp, err)
	}
	if ran["de1"] != 1 || ran["nl1"] != 1 || ran["old1"] != 0 || ran["off1"] != 0 { // only connected nodes with the capability
		t.Fatalf("ran: %v", ran)
	}
	for _, n := range resp.Msg.Nodes {
		if n.NodeName == "off1" && (!n.HasReport || n.Items[0].Id != "ipv6") { // offline: the old report, not an error
			t.Fatalf("offline node: %+v", n)
		}
	}
}

// ApplyFix in two steps: nothing is applied that the admin has not seen.
func TestApplyFixPlanAndConfirm(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.node("nl1", "hostera", true)
	in := e.inbound("de1", 443)
	other := e.inbound("nl1", 443)
	r := rpc{e.s}
	e.report("de1", false, res("disk_space", dFail, "journald_vacuum", "used_pct", "95"))
	want(t, e.active(), "doctor_fail/de1/disk_space")

	e.fl.fix = func(c fixCall) (*agentv1.CommandResult, error) {
		if c.dry {
			return &agentv1.CommandResult{Ok: true, Detail: "journal 3200 MB -> 200 MB", Params: map[string]string{"before_mb": "3200", "after_mb": "200"}}, nil
		}
		// the agent fixes it and re-checks: a partial report arrives right after the result
		e.s.DoctorReport(context.Background(), c.node, &agentv1.DoctorReport{Partial: true, Results: []*agentv1.DoctorResult{res("disk_space", dOK, "")}})
		return &agentv1.CommandResult{Ok: true, Affected: 1, Detail: "done", Params: map[string]string{"freed_mb": "3000"}}, nil
	}
	fix := func(node, id, plan string, dry bool, params map[string]string) (*adminv1.ApplyFixResponse, error) {
		resp, err := r.ApplyFix(e.ctx, req(&adminv1.ApplyFixRequest{NodeId: node, FixId: id, DryRun: dry, PlanId: plan, Params: params}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	// refused up front
	_, err := fix("de1", "rm_rf", "", true, nil)
	wantCode(t, err, connect.CodeInvalidArgument, "unknown fix")
	_, err = fix("nod_nope", "journald_vacuum", "", true, nil)
	wantCode(t, err, connect.CodeNotFound, "node")
	_, err = fix("de1", "journald_vacuum", "", true, map[string]string{"path": "/"})
	wantCode(t, err, connect.CodeInvalidArgument, "unexpected parameter")
	_, err = fix("de1", "restart_inbound", "", true, map[string]string{"inbound_id": other}) // another node's inbound
	wantCode(t, err, connect.CodeInvalidArgument, "not an inbound of this node")
	_, err = fix("de1", "journald_vacuum", "", false, nil)
	wantCode(t, err, connect.CodeFailedPrecondition, "no fresh plan")
	_, err = fix("de1", "journald_vacuum", "pln_made_up", false, nil)
	wantCode(t, err, connect.CodeFailedPrecondition, "no fresh plan")
	if len(e.fl.fixCalls) != 0 {
		t.Fatalf("nothing may reach the node before a plan: %+v", e.fl.fixCalls)
	}

	// step 1
	p, err := fix("de1", "journald_vacuum", "", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.PlanId == "" || p.PlanExpiresUnix != e.clock.Now().Add(10*time.Minute).Unix() || p.Plan.FixId != "journald_vacuum" || p.Plan.TitleKey != "health.fix.journald_vacuum.plan" ||
		p.Plan.Params["before_mb"] != "3200" || p.Plan.Detail == "" || p.Plan.Disruptive || p.Applied {
		t.Fatalf("plan: %+v", p)
	}
	if len(e.fl.fixCalls) != 1 || !e.fl.fixCalls[0].dry {
		t.Fatalf("the dry run: %+v", e.fl.fixCalls)
	}
	want(t, e.active(), "doctor_fail/de1/disk_space") // a plan changes nothing

	// the plan is for this node, this fix and these arguments only
	_, err = fix("nl1", "journald_vacuum", p.PlanId, false, nil)
	wantCode(t, err, connect.CodeFailedPrecondition, "no fresh plan")
	_, err = fix("de1", "apply_baseline", p.PlanId, false, nil)
	wantCode(t, err, connect.CodeFailedPrecondition, "no fresh plan")
	_, err = fix("de1", "restart_inbound", p.PlanId, false, map[string]string{"inbound_id": in})
	wantCode(t, err, connect.CodeFailedPrecondition, "no fresh plan")
	if len(e.fl.fixCalls) != 1 {
		t.Fatalf("a mismatching plan reached the node: %+v", e.fl.fixCalls)
	}

	// step 2
	e.clock.Advance(time.Minute)
	done, err := fix("de1", "journald_vacuum", p.PlanId, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !done.Applied || done.Error != "" || done.Affected != 1 || done.ResultParams["freed_mb"] != "3000" {
		t.Fatalf("result: %+v", done)
	}
	if done.Doctor == nil || done.Doctor.NodeId != "de1" || done.Doctor.Items[0].Status != adminv1.DoctorStatus_DOCTOR_STATUS_OK {
		t.Fatalf("the re-checked doctor state is missing: %+v", done.Doctor)
	}
	if len(e.fl.fixCalls) != 2 || e.fl.fixCalls[1].dry || e.fl.fixCalls[1].fix != "journald_vacuum" {
		t.Fatalf("the real call: %+v", e.fl.fixCalls)
	}
	want(t, e.active())
	if h := e.history(); len(h) != 1 || h[0].Resolution != "fix_applied" {
		t.Fatalf("the alert must end as fix_applied: %+v", h)
	}

	// single use
	_, err = fix("de1", "journald_vacuum", p.PlanId, false, nil)
	wantCode(t, err, connect.CodeFailedPrecondition, "no fresh plan")

	// both steps are in the audit log, with the plan facts
	rows, _ := e.st.ListAudit(e.ctx, "", 0, 10)
	rows = rows[:min(len(rows), 2)] // the newest two: the fixture's own profile rows came before them
	if len(rows) != 2 || rows[0].Action != "health.apply_fix" || rows[1].Action != "health.fix_plan" ||
		!strings.Contains(rows[1].Params, "3200 MB") || !strings.Contains(rows[0].Params, `"ok":"true"`) || rows[0].Actor != "adm_test" {
		t.Fatalf("audit: %+v", rows)
	}
}

func TestApplyFixPlanExpiresAndFailuresAreReported(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	in := e.inbound("de1", 443)
	r := rpc{e.s}
	call := func(dry bool, plan, fixID string, params map[string]string) (*adminv1.ApplyFixResponse, error) {
		resp, err := r.ApplyFix(e.ctx, req(&adminv1.ApplyFixRequest{NodeId: "de1", FixId: fixID, DryRun: dry, PlanId: plan, Params: params}))
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}

	e.fl.fix = func(c fixCall) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: true, Detail: "x"}, nil
	}
	p, err := call(true, "", "apply_baseline", nil)
	if err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(10*time.Minute + time.Second)
	_, err = call(false, p.PlanId, "apply_baseline", nil)
	wantCode(t, err, connect.CodeFailedPrecondition, "no fresh plan")

	// restart_inbound is disruptive, and takes an inbound of the node
	p, err = call(true, "", "restart_inbound", map[string]string{"inbound_id": in})
	if err != nil || !p.Plan.Disruptive {
		t.Fatalf("restart plan: %+v %v", p, err)
	}

	// the node refuses the dry run: no plan
	e.fl.fix = func(c fixCall) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: false, Error: "not_applicable"}, nil
	}
	_, err = call(true, "", "set_resolver", nil)
	wantCode(t, err, connect.CodeFailedPrecondition, "not_applicable")

	// the real run fails on the node: applied=false with the agent's code, nothing is marked as fixed
	e.fl.fix = func(c fixCall) (*agentv1.CommandResult, error) {
		if c.dry {
			return &agentv1.CommandResult{Ok: true, Detail: "x"}, nil
		}
		return &agentv1.CommandResult{Ok: false, Error: "failed: nft busy"}, nil
	}
	p, _ = call(true, "", "apply_baseline", nil)
	done, err := call(false, p.PlanId, "apply_baseline", nil)
	if err != nil || done.Applied || done.Error != "failed: nft busy" || done.Doctor != nil {
		t.Fatalf("failed fix: %+v %v", done, err)
	}
	if _, ok := e.s.recentFix["de1/apply_baseline"]; ok {
		t.Fatal("a failed fix counts as applied")
	}

	// an agent that cannot apply fixes is never asked
	e.fl.set("de1", liveState{up: true})
	n := len(e.fl.fixCalls)
	_, err = call(true, "", "apply_baseline", nil)
	wantCode(t, err, connect.CodeFailedPrecondition, "agent too old")
	if len(e.fl.fixCalls) != n {
		t.Fatal("an old agent was sent a fix")
	}
}

// One fix at a time per node.
func TestApplyFixOneAtATimePerNode(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	r := rpc{e.s}
	started, release := make(chan struct{}), make(chan struct{})
	e.fl.fix = func(c fixCall) (*agentv1.CommandResult, error) {
		if !c.dry && c.fix == "apply_baseline" {
			close(started)
			<-release
			e.s.DoctorReport(context.Background(), c.node, &agentv1.DoctorReport{Partial: true, Results: []*agentv1.DoctorResult{res("net_baseline", dOK, "")}})
		}
		return &agentv1.CommandResult{Ok: true, Detail: "x"}, nil
	}
	plan := func(id string) string {
		resp, err := r.ApplyFix(e.ctx, req(&adminv1.ApplyFixRequest{NodeId: "de1", FixId: id, DryRun: true}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.PlanId
	}
	p1, p2 := plan("apply_baseline"), plan("journald_vacuum")
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second) // the wait for the re-check report is cut short
		defer cancel()
		_, err := r.ApplyFix(ctx, req(&adminv1.ApplyFixRequest{NodeId: "de1", FixId: "apply_baseline", PlanId: p1}))
		done <- err
	}()
	<-started
	_, err := r.ApplyFix(e.ctx, req(&adminv1.ApplyFixRequest{NodeId: "de1", FixId: "journald_vacuum", PlanId: p2}))
	wantCode(t, err, connect.CodeFailedPrecondition, "another fix is running")
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReconnectWarpPlanIsDisruptiveAndRequiresConfirmation(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	r := rpc{e.s}
	e.fl.fix = func(c fixCall) (*agentv1.CommandResult, error) {
		return &agentv1.CommandResult{Ok: true, Detail: "would reconnect the configured WARP tunnel"}, nil
	}

	plan, err := r.ApplyFix(e.ctx, req(&adminv1.ApplyFixRequest{NodeId: "de1", FixId: "reconnect_warp", DryRun: true}))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Msg.Plan == nil || plan.Msg.Plan.FixId != "reconnect_warp" || !plan.Msg.Plan.Disruptive || plan.Msg.Plan.TitleKey != "health.fix.reconnect_warp.plan" {
		t.Fatalf("plan must warn before reconnecting WARP: %+v", plan.Msg)
	}
	_, err = r.ApplyFix(e.ctx, req(&adminv1.ApplyFixRequest{NodeId: "de1", FixId: "reconnect_warp"}))
	wantCode(t, err, connect.CodeFailedPrecondition, "no fresh plan")
	if len(e.fl.fixCalls) != 1 || !e.fl.fixCalls[0].dry {
		t.Fatalf("WARP changed before confirmation: %+v", e.fl.fixCalls)
	}
}
