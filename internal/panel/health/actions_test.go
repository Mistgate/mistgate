package health

import (
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// A timeout on 443, or on two and more ports at once, is the hoster cutting UDP as a whole: "move to another port" would
// not help. One dead port that is not 443 is that port (change it on this node).
func TestUDPDiagnosis(t *testing.T) {
	timeout := func(port uint16) probed {
		tg := &target{}
		tg.spec.Listen.Port = port
		return probed{tg, cell{last: &Result{ErrorCode: "timeout"}, streak: 3}}
	}
	for _, tc := range []struct {
		ports []uint16
		want  string
	}{
		{[]uint16{8443}, "udp_blocked"},
		{[]uint16{443}, "udp_all_blocked"},
		{[]uint16{8443, 2053}, "udp_all_blocked"},
	} {
		var fs []probed
		for _, p := range tc.ports {
			fs = append(fs, timeout(p))
		}
		if got := allFailVariant(fs); got != tc.want {
			t.Errorf("%v: %s, want %s", tc.ports, got, tc.want)
		}
	}

	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a := e.inbound("de1", 8443)
	e.fail(a, "timeout")
	e.fail(a, "timeout")
	nt := e.active()["no_traffic/de1/"]
	if nt.WhyKey != "health.alert.no_traffic.why.udp_blocked" || nt.Params["port"] != "8443" || nt.Params["ports"] != "8443" {
		t.Fatalf("one dead port: %+v", nt)
	}
}

func TestAlertActionsByDiagnosis(t *testing.T) {
	al := func(kind, why, subject string, params map[string]string) store.HealthAlert {
		return store.HealthAlert{Kind: kind, NodeID: "de1", Subject: subject, WhyKey: why, Params: params}
	}
	for _, tc := range []struct {
		a    store.HealthAlert
		want string
	}{
		{al(kCheckFailed, "health.alert.check_failed.why.timeout", "inb_1", nil), "restart_inbound"},
		{al(kCheckFailed, "health.alert.check_failed.why.refused", "inb_1", nil), "restart_inbound"},
		{al(kCheckFailed, "health.alert.check_failed.why.udp_blocked", "inb_1", nil), "open_profiles"},
		{al(kCheckFailed, "health.alert.check_failed.why.warp_path", "inb_1", nil), "open_warp"},
		{al(kCheckFailed, "health.alert.check_failed.why.exit_unreachable", "inb_1", nil), ""},
		{al(kNoTraffic, "health.alert.no_traffic.why.auth", "", nil), "restart_inbounds"},
		{al(kNoTraffic, "health.alert.no_traffic.why.udp_all_blocked", "", nil), ""},
		{al(kNoTraffic, "health.alert.no_traffic.why.udp_blocked", "", nil), "open_profiles"},
		{al(kCertExpiry, "health.alert.cert_expiry.why", "inb_1", nil), "open_profiles"},
		{al(kCertExpiry, "health.doctor.cert_expiry.why", "cert", nil), ""},
		{al(kDoctorFail, "health.doctor.warp_path.why", "warp_path", map[string]string{"check": "warp_path"}), "open_warp"},
		{al(kDoctorWarn, "health.doctor.port_conflicts.why", "port_conflicts", map[string]string{"check": "port_conflicts"}), "open_profiles"},
	} {
		if got := strings.Join(alertActions(tc.a), ","); got != tc.want {
			t.Errorf("%s %s: %q, want %q", tc.a.Kind, tc.a.WhyKey, got, tc.want)
		}
	}
}

// A profile that refuses the check gets a restart button, and the alert says how many connections it would drop.
func TestRestartActionCountsTheConnections(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.fl.online = map[string]int{a: 3, b: 5}
	e.pass(b)
	e.fail(a, "auth")
	e.fail(a, "auth")
	resp, err := rpc{e.s}.ListAlerts(e.ctx, req(&adminv1.ListAlertsRequest{}))
	if err != nil || len(resp.Msg.Active) != 1 {
		t.Fatalf("alerts: %+v %v", resp, err)
	}
	m := resp.Msg.Active[0]
	if strings.Join(m.Actions, ",") != "restart_inbound,open_node,mute" || m.Params["online"] != "3" || m.Params["inbound"] != a || m.Params["profile"] == "" {
		t.Fatalf("alert %+v", m)
	}
	if stored := e.active()["check_failed/de1/"+a]; stored.Params["online"] != "" {
		t.Fatal("the live count was stored with the alert")
	}
}

func withCode(r *agentv1.DoctorResult, code string) *agentv1.DoctorResult {
	r.DetailCode = code
	return r
}

// "This is normal for this node": the warning opens no alert while its detail code stays, and counts again once it
// changes or turns FAIL.
func TestAcceptDoctorWarning(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	r := rpc{e.s}
	nat := func() *agentv1.DoctorResult {
		return withCode(res("foreign_nft", dWarn, "", "tables", "ip filter,ip nat", "nat_tables", "ip nat"), "foreign_nft.nat")
	}
	e.report("de1", false, nat(), withCode(res("cert_expiry", dWarn, "", "days_left", "9", "reason", "expiring"), "cert_expiry.agent"),
		res("disk_space", dFail, ""))
	want(t, e.active(), "doctor_warn/de1/foreign_nft", "cert_expiry/de1/cert", "doctor_fail/de1/disk_space")

	accept := func(check string) (*adminv1.NodeDoctor, error) {
		resp, err := r.AcceptDoctorItem(e.ctx, req(&adminv1.AcceptDoctorItemRequest{NodeId: "de1", CheckId: check}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.Doctor, nil
	}
	_, err := accept("disk_space")
	wantCode(t, err, connect.CodeFailedPrecondition, "only a warning")
	_, err = accept("cert_expiry")
	wantCode(t, err, connect.CodeFailedPrecondition, "only a warning")
	_, err = accept("nope")
	wantCode(t, err, connect.CodeNotFound, "doctor item")

	nd, err := accept("foreign_nft")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range nd.Items {
		if it.Id == "foreign_nft" && (it.AcceptedUnix != e.clock.Now().Unix() || it.AcceptedBy != "adm_test") {
			t.Fatalf("accepted item %+v", it)
		}
	}
	want(t, e.active(), "cert_expiry/de1/cert", "doctor_fail/de1/disk_space")
	if h := e.history(); len(h) != 1 || h[0].Resolution != "accepted" {
		t.Fatalf("history %+v", h)
	}
	e.report("de1", true, nat()) // the same fact again: still quiet
	want(t, e.active(), "cert_expiry/de1/cert", "doctor_fail/de1/disk_space")

	// a new fact (a foreign rule hits our ports) is not what was accepted
	e.report("de1", true, withCode(res("foreign_nft", dWarn, "", "ports", "443"), "foreign_nft.hits_ports"))
	if _, ok := e.active()["doctor_warn/de1/foreign_nft"]; !ok {
		t.Fatal("a changed warning stayed accepted")
	}
	if acc, _ := e.st.DoctorAccepts(e.ctx, "de1"); len(acc) != 0 {
		t.Fatalf("stale acceptance kept: %+v", acc)
	}

	// accepted again, then FAIL: it counts, and the acceptance is gone
	if _, err := accept("foreign_nft"); err != nil {
		t.Fatal(err)
	}
	e.report("de1", true, withCode(res("foreign_nft", dFail, "", "ports", "443"), "foreign_nft.hits_ports"))
	if _, ok := e.active()["doctor_fail/de1/foreign_nft"]; !ok {
		t.Fatal("a FAIL is hidden by an acceptance")
	}

	// taking it back makes it count at once
	e.report("de1", true, nat())
	if _, err := accept("foreign_nft"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.UnacceptDoctorItem(e.ctx, req(&adminv1.UnacceptDoctorItemRequest{NodeId: "de1", CheckId: "foreign_nft"})); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.active()["doctor_warn/de1/foreign_nft"]; !ok {
		t.Fatal("the warning is still quiet after the acceptance was taken back")
	}
	var actions []string
	rows, _ := e.st.ListAudit(e.ctx, "", 0, 20)
	for _, row := range rows {
		actions = append(actions, row.Action)
	}
	if got := strings.Join(actions, " "); !strings.Contains(got, "health.accept_doctor") || !strings.Contains(got, "health.unaccept_doctor") {
		t.Fatalf("audit %s", got)
	}
}

// An acceptance lands only while the stored result is still that warning. An accept that read the WARN before a FAIL came
// in (the FAIL dropped the acceptances already) must not be stored after it: the same WARN coming back would then be
// hidden although a FAIL happened in between.
func TestAcceptDoctorNeedsTheWarningStillThere(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	nat := func() *agentv1.DoctorResult {
		return withCode(res("foreign_nft", dWarn, "", "tables", "ip nat"), "foreign_nft.nat")
	}
	e.report("de1", false, nat())
	rows, err := e.st.DoctorResults(e.ctx, "de1") // what AcceptDoctorItem read
	if err != nil || len(rows) != 1 {
		t.Fatalf("results %+v %v", rows, err)
	}
	read := rows[0]
	e.report("de1", true, withCode(res("foreign_nft", dFail, "", "ports", "443"), "foreign_nft.nat"))
	ok, err := e.st.AcceptDoctor(e.ctx, store.DoctorAccept{NodeID: "de1", CheckID: read.CheckID, DetailCode: read.DetailCode, By: "adm_test", At: e.clock.Now()})
	if err != nil || ok {
		t.Fatalf("an acceptance of a WARN that is a FAIL now: stored %v, %v", ok, err)
	}
	if acc, _ := e.st.DoctorAccepts(e.ctx, "de1"); len(acc) != 0 {
		t.Fatalf("stored %+v", acc)
	}
	e.report("de1", true, nat())
	if _, ok := e.active()["doctor_warn/de1/foreign_nft"]; !ok {
		t.Fatal("the warning that came back after a FAIL is hidden")
	}
	// the RPC refuses a FAIL, and on the current WARN it stores the acceptance
	if _, err := (rpc{e.s}).AcceptDoctorItem(e.ctx, req(&adminv1.AcceptDoctorItemRequest{NodeId: "de1", CheckId: "foreign_nft"})); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.st.AcceptDoctor(e.ctx, store.DoctorAccept{NodeID: "de1", CheckID: "foreign_nft", DetailCode: "foreign_nft.other", By: "adm_test", At: e.clock.Now()}); err != nil || ok {
		t.Fatalf("an acceptance for another detail code: stored %v, %v", ok, err)
	}
}

// An agent too old to send detail codes reports a WARN without one. It cannot be accepted (an empty code would cover any
// later warning of that check), and an empty acceptance stored before the panel refused them hides nothing.
func TestDoctorWarningWithoutACodeIsNeverAccepted(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("foreign_nft", dWarn, "", "tables", "ip nat"))
	_, err := (rpc{e.s}).AcceptDoctorItem(e.ctx, req(&adminv1.AcceptDoctorItemRequest{NodeId: "de1", CheckId: "foreign_nft"}))
	wantCode(t, err, connect.CodeFailedPrecondition, "no detail code")
	if acc, _ := e.st.DoctorAccepts(e.ctx, "de1"); len(acc) != 0 {
		t.Fatalf("stored %+v", acc)
	}
	e.exec(`INSERT INTO doctor_accept (node_id, check_id, detail_code, accepted_by, accepted_at) VALUES ('de1', 'foreign_nft', '', 'adm_test', 1)`)
	e.report("de1", true, res("foreign_nft", dWarn, "", "ports", "443")) // another fact, again without a code
	if _, ok := e.active()["doctor_warn/de1/foreign_nft"]; !ok {
		t.Fatal("an acceptance without a code hid a warning")
	}
}

// A host without IPv6 runs WARP over its IPv4 endpoint: the agent's warning is stored as a fine fact.
func TestIPv6WithoutWarpIsFine(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, withCode(res("ipv6", dWarn, "", "ipv6", "no", "warp_inbounds", "inb_w"), "ipv6.none_warp"))
	want(t, e.active())
	rows, _ := e.st.DoctorResults(e.ctx, "de1")
	if len(rows) != 1 || rows[0].Status != int(dOK) || rows[0].DetailCode != codeIPv6WarpOK {
		t.Fatalf("stored %+v", rows)
	}
	// IPv6 that is there but cannot connect stays a warning
	e.report("de1", false, withCode(res("ipv6", dWarn, "", "ipv6", "yes", "connect", "failed"), "ipv6.connect_failed"))
	want(t, e.active(), "doctor_warn/de1/ipv6")
}

// warpInbound deploys a hy2 profile whose exit is WARP.
func (e *env) warpInbound(nodeID string, port uint32) string {
	e.t.Helper()
	id := e.inbound(nodeID, port)
	e.exec(`UPDATE profile SET settings_json = json_set(settings_json, '$.egress', 'warp') WHERE id = (SELECT profile_id FROM inbound WHERE id = ?)`, id)
	e.s.invalidateSnapshot()
	return id
}

// Dead WARP is said once: while the doctor's warp_path FAIL is up, the WARP profiles failing their check open no
// alert of their own (and one that was open is superseded); the doctor's alert names those profiles.
func TestDeadWarpIsSaidOnce(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	direct, w := e.inbound("de1", 443), e.warpInbound("de1", 8443)
	e.pass(direct)
	e.fail(w, "exit_unreachable")
	e.fail(w, "exit_unreachable")
	got := e.active()
	want(t, got, "check_failed/de1/"+w)
	if got["check_failed/de1/"+w].WhyKey != "health.alert.check_failed.why.warp_path" {
		t.Fatalf("why %s", got["check_failed/de1/"+w].WhyKey)
	}
	e.report("de1", false, withCode(res("warp_path", dFail, "", "state", "down"), "warp_path.down"))
	got = e.active()
	want(t, got, "doctor_fail/de1/warp_path")
	if code := got["doctor_fail/de1/warp_path"].Params["detail_code"]; code != "warp_path.down" {
		t.Fatalf("the doctor's alert lost its detail code: %q", code)
	}
	if p := got["doctor_fail/de1/warp_path"].Params["profiles"]; p == "" || !strings.Contains(p, "hy2-de1") {
		t.Fatalf("the doctor's alert does not name the WARP profiles: %+v", got["doctor_fail/de1/warp_path"].Params)
	}
	var superseded bool
	for _, h := range e.history() {
		superseded = superseded || (h.Kind == kCheckFailed && h.Resolution == "superseded")
	}
	if !superseded {
		t.Fatalf("the check alert was not superseded: %+v", e.history())
	}
	e.fail(w, "exit_unreachable")
	want(t, e.active(), "doctor_fail/de1/warp_path")
}

// A certificate alert names its profile and domain, from the doctor and from the panel's own view.
func TestCertAlertNamesTheProfile(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a := e.inbound("de1", 443)
	e.exec(`UPDATE inbound SET cert_not_after = ? WHERE id = ?`, e.clock.Now().Add(5*24*time.Hour).Unix(), a)
	e.evaluate()
	al := e.active()["cert_expiry/de1/"+a]
	if al.Params["profile"] == "" || al.Params["days_left"] != "5" || al.WhyKey != "health.alert.cert_expiry.why" {
		t.Fatalf("panel view %+v", al)
	}
	e.report("de1", false, withCode(res("cert_expiry", dWarn, "", "inbound_id", a, "server_name", "de2.example.com", "reason", "expiring", "days_left", "5"), "cert_expiry.inbound"))
	al = e.active()["cert_expiry/de1/"+a]
	if al.Params["profile"] == "" || al.Params["server_name"] != "de2.example.com" || al.WhyKey != "health.alert.cert_expiry.why" {
		t.Fatalf("doctor view %+v", al)
	}
	e.report("de1", false, withCode(res("cert_expiry", dFail, "", "inbound_id", a, "server_name", "de2.example.com", "reason", "san_mismatch", "days_left", "40"), "cert_expiry.inbound"))
	if al = e.active()["cert_expiry/de1/"+a]; al.WhyKey != "health.alert.cert_expiry.why.san_mismatch" {
		t.Fatalf("mismatch %+v", al)
	}
}

// A muted alert that gets worse rings again.
func TestMuteEndsWhenItGetsWorse(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a := e.inbound("de1", 443)
	e.exec(`UPDATE inbound SET cert_not_after = ? WHERE id = ?`, e.clock.Now().Add(5*24*time.Hour).Unix(), a)
	e.evaluate()
	id := e.active()["cert_expiry/de1/"+a].ID
	if _, err := e.st.MuteAlert(e.ctx, id, e.clock.Now().Add(7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	e.clock.Advance(time.Hour)
	e.evaluate() // the same severity: the mute stays
	if al := e.active()["cert_expiry/de1/"+a]; al.MutedUntil.IsZero() {
		t.Fatal("the mute was lost without a reason")
	}
	e.clock.Advance(3 * 24 * time.Hour) // under 3 days: critical
	e.evaluate()
	if al := e.active()["cert_expiry/de1/"+a]; al.Severity != sevCritical || !al.MutedUntil.IsZero() {
		t.Fatalf("worse but still muted: %+v", al)
	}
}

// events are the node's traffic events, newest first (the profiles' own events left out).
func (e *env) events(node string) []store.EventRow {
	e.t.Helper()
	rows, _, err := e.st.Events(e.ctx, store.EventFilter{NodeID: node, Limit: 50})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []store.EventRow
	for _, r := range rows {
		switch r.Code {
		case "traffic_stopped", "traffic_resumed", "check_failing", "check_recovered":
			out = append(out, r)
		}
	}
	return out
}

// When traffic stopped and when it came back is on the node's history, with how long it lasted.
func TestTrafficAlertsWriteTheNodesHistory(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "auth")
	e.fail(a, "auth")
	e.clock.Advance(10 * time.Minute)
	e.pass(a)
	codes := func() string {
		var out []string
		for _, ev := range e.events("de1") {
			out = append(out, ev.Code)
		}
		return strings.Join(out, " ")
	}
	if got := codes(); got != "check_recovered check_failing" {
		t.Fatalf("events %s", got)
	}
	ev := e.events("de1")[0]
	if ev.InboundID != a || ev.Params["minutes"] != "10" || ev.Params["profile"] == "" || ev.Severity != 1 {
		t.Fatalf("recovered %+v", ev)
	}

	// everything fails: one "stopped"; the check alert it replaces says nothing about a recovery
	e.fail(a, "auth")
	e.fail(a, "auth")
	e.fail(b, "auth")
	e.fail(b, "auth")
	e.clock.Advance(28 * time.Minute)
	e.pass(a)
	e.pass(b)
	// newest first: back, stopped (a's check alert is superseded by it, so no recovery of its own), a failing
	if got := codes(); got != "traffic_resumed traffic_stopped check_failing check_recovered check_failing" {
		t.Fatalf("events %s", got)
	}
	if ev := e.events("de1")[0]; ev.Params["minutes"] == "" || ev.Severity != 1 {
		t.Fatalf("resumed %+v", ev)
	}
}

// A check that fails again within the re-open window keeps its alert row (and first_seen), but the recovery event counts
// the minutes of the last episode only, not the gap in between.
func TestRecoveryMinutesCountFromTheReopen(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "auth")
	e.fail(a, "auth")
	first := e.active()["check_failed/de1/"+a].FirstSeen
	e.clock.Advance(10 * time.Minute)
	e.pass(a)
	e.clock.Advance(20 * time.Minute) // healthy for 20 minutes, then it fails again
	e.pass(b)
	e.fail(a, "auth")
	e.fail(a, "auth")
	if re := e.active()["check_failed/de1/"+a]; !re.FirstSeen.Equal(first) {
		t.Fatalf("not re-opened: first_seen %v, want %v", re.FirstSeen, first)
	}
	e.clock.Advance(3 * time.Minute)
	e.pass(a)
	ev := e.events("de1")[0]
	if ev.Code != "check_recovered" || ev.Params["minutes"] != "3" {
		t.Fatalf("recovery after a re-open: %+v", ev)
	}
}

// The node's only inbound stops answering (no_traffic), then stops running (failed): nothing on the node is checked any
// more, which is not traffic coming back. The alert ends as superseded and the history says nothing about a resume.
func TestTrafficIsNotResumedWhenNothingIsLeftToCheck(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a := e.inbound("de1", 443)
	e.fail(a, "auth")
	e.fail(a, "auth")
	want(t, e.active(), "no_traffic/de1/")
	e.exec(`UPDATE inbound SET state = 'failed' WHERE id = ?`, a)
	e.s.invalidateSnapshot()
	e.evaluate()
	want(t, e.active())
	if h := e.history(); len(h) != 1 || h[0].Kind != kNoTraffic || h[0].Resolution != "superseded" {
		t.Fatalf("history %+v", h)
	}
	for _, ev := range e.events("de1") {
		if ev.Code == "traffic_resumed" {
			t.Fatalf("a failed inbound reads as traffic resumed: %+v", e.events("de1"))
		}
	}
}

// The doctor and the restart plan name profiles, not inb_ ids, and the fleet tab knows the agent and when it was seen.
func TestDoctorNamesProfiles(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a := e.inbound("de1", 8443)
	w := e.warpInbound("de1", 2053)
	e.fl.online = map[string]int{a: 2}
	e.exec(`UPDATE node SET agent_version = '0.3.1' WHERE id = 'de1'`)
	e.report("de1", false,
		withCode(res("port_conflicts", dFail, "restart_inbound", "inbound_id", a, "port", "8443", "network", "udp", "process", "xray"), "port_conflicts.held"),
		withCode(res("warp_path", dWarn, "", "state", "starting"), "warp_path.starting"))
	resp, err := rpc{e.s}.GetDoctor(e.ctx, req(&adminv1.GetDoctorRequest{NodeId: "de1"}))
	if err != nil {
		t.Fatal(err)
	}
	nd := resp.Msg.Nodes[0]
	if nd.AgentVersion != "0.3.1" || nd.LastSeenUnix == 0 {
		t.Fatalf("node %+v", nd)
	}
	params := map[string]map[string]string{}
	for _, it := range nd.Items {
		params[it.Id] = it.Params
	}
	if params["port_conflicts"]["profile"] == "" || params["port_conflicts"]["inbound_id"] != a {
		t.Fatalf("port_conflicts %+v", params["port_conflicts"])
	}
	wantName := ""
	for _, tg := range e.mustSnapshot().byNode["de1"] {
		if tg.in.ID == w {
			wantName = tg.in.ProfileName
		}
	}
	if params["warp_path"]["profiles"] != wantName {
		t.Fatalf("warp_path %+v, want %q", params["warp_path"], wantName)
	}

	plan, err := rpc{e.s}.ApplyFix(e.ctx, req(&adminv1.ApplyFixRequest{NodeId: "de1", FixId: "restart_inbound", DryRun: true, Params: map[string]string{"inbound_id": a}}))
	if err != nil {
		t.Fatal(err)
	}
	if p := plan.Msg.Plan.Params; p["profiles"] != params["port_conflicts"]["profile"] || p["online"] != "2" {
		t.Fatalf("plan %+v", p)
	}
}

func (e *env) mustSnapshot() *snapshot {
	e.t.Helper()
	sn, err := e.s.snapshot(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return sn
}
