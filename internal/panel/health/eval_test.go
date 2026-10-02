package health

import (
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	cFail = adminv1.CheckStatus_CHECK_STATUS_FAILED
	cOK   = adminv1.CheckStatus_CHECK_STATUS_OK
	cDeg  = adminv1.CheckStatus_CHECK_STATUS_DEGRADED
)

// A synthetic alert opens only after two failed rounds, resolves by itself on the next passing round, and a
// failure that returns within an hour re-opens the same row.
func TestSyntheticAlertOpensAfterTwoRoundsAndReopens(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(a)
	e.pass(b)

	e.fail(a, "auth")
	want(t, e.active()) // one failed round is not an alert
	e.fail(a, "auth")
	got := e.active()
	want(t, got, "check_failed/de1/"+a)
	al := got["check_failed/de1/"+a]
	if al.Severity != sevWarning { // two hy2 inbounds on the node: not the only one of its protocol
		t.Fatalf("severity %d", al.Severity)
	}
	if al.WhyKey != "health.alert.check_failed.why.auth" || al.TitleKey != "health.alert.check_failed.title" || al.Params["inbound"] != a {
		t.Fatalf("alert texts: %+v", al)
	}
	first := al.ID

	e.pass(a)
	want(t, e.active())
	if h := e.history(); len(h) != 1 || h[0].Resolution != "cleared" || h[0].ID != first {
		t.Fatalf("history: %+v", h)
	}

	// the same failure within an hour: same row, same first_seen
	e.fail(a, "auth")
	e.fail(a, "auth")
	re := e.active()["check_failed/de1/"+a]
	if re.ID != first || !re.FirstSeen.Equal(al.FirstSeen) {
		t.Fatalf("re-opened alert: %+v, first was %+v", re, al)
	}

	// after more than an hour a new row
	e.pass(a)
	e.clock.Advance(61 * time.Minute)
	e.fail(a, "auth")
	e.fail(a, "auth")
	if fresh := e.active()["check_failed/de1/"+a]; fresh.ID == first {
		t.Fatalf("an alert that ended an hour ago was re-opened: %+v", fresh)
	}
}

func TestDegradedNeverOpensAnAlertAndBreaksTheStreak(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "exit_unreachable")
	e.round(a, cDeg, "http_status")
	e.fail(a, "exit_unreachable")
	want(t, e.active())
	e.round(a, cDeg, "http_status")
	e.round(a, cDeg, "http_status")
	want(t, e.active())
}

// Every probed inbound failing is one critical NO_TRAFFIC of the node (which supersedes CHECK_FAILED, and the
// node status reads it); when all of them only time out on 443 and 8443, the hoster cuts UDP as a whole.
func TestNoTrafficSupersedesCheckFailed(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "timeout")
	e.fail(a, "timeout")
	got := e.active()
	want(t, got, "check_failed/de1/"+a)
	if w := got["check_failed/de1/"+a].WhyKey; w != "health.alert.check_failed.why.udp_blocked" { // b answers on another port
		t.Fatalf("why = %s", w)
	}
	if nt, _, _, _ := e.s.NodeHealth("de1"); nt {
		t.Fatal("no_traffic with one inbound still passing")
	}

	e.fail(b, "timeout")
	e.fail(b, "timeout")
	got = e.active()
	want(t, got, "no_traffic/de1/")
	nt := got["no_traffic/de1/"]
	if nt.Severity != sevCritical || nt.WhyKey != "health.alert.no_traffic.why.udp_all_blocked" || nt.Params["failed"] != "2" || nt.Params["total"] != "2" ||
		nt.Params["ports"] != "443, 8443" {
		t.Fatalf("no_traffic: %+v", nt)
	}
	if noTraffic, failed, total, _ := e.s.NodeHealth("de1"); !noTraffic || failed != 2 || total != 2 {
		t.Fatalf("node health: %v %d %d", noTraffic, failed, total)
	}
	var superseded bool
	for _, h := range e.history() {
		superseded = superseded || (h.Kind == kCheckFailed && h.Resolution == "superseded")
	}
	if !superseded {
		t.Fatalf("check_failed was not superseded: %+v", e.history())
	}

	e.pass(a)
	e.pass(b)
	want(t, e.active())
	if nt, _, _, _ := e.s.NodeHealth("de1"); nt {
		t.Fatal("no_traffic after recovery")
	}
}

func TestMixedErrorsAndOnlyInboundOfProtocolIsCritical(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.fail(a, "tls")
	e.fail(a, "tls")
	e.fail(b, "timeout")
	e.fail(b, "timeout")
	if nt := e.active()["no_traffic/de1/"]; nt.WhyKey != "health.alert.no_traffic.why.mixed" {
		t.Fatalf("no_traffic: %+v", nt)
	}
}

func TestOnlyInboundOfItsProtocolMakesCheckFailedCritical(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "tls")
	e.fail(a, "tls")
	if al := e.active()["check_failed/de1/"+a]; al.Severity != sevWarning || al.WhyKey != "health.alert.check_failed.why.tls" {
		t.Fatalf("two hy2 inbounds: %+v", al)
	}
	e.exec(`UPDATE inbound SET enabled = 0 WHERE id = ?`, b)
	e.evaluate()
	// b is off: a is now the only hy2 inbound of the node, and it is probed alone, so this is NO_TRAFFIC
	want(t, e.active(), "no_traffic/de1/")
}

func TestWhyVariantWarpPath(t *testing.T) {
	spec := func(port uint16, egress string) *target {
		t := &target{}
		t.spec.Listen.Port, t.spec.Egress, t.spec.Protocol = port, egress, "hysteria2"
		return t
	}
	failing := probed{spec(443, "warp"), cell{last: &Result{ErrorCode: "exit_unreachable"}, streak: 3}}
	direct := probed{spec(8443, "direct"), cell{last: &Result{}, streak: 0}}
	if got := whyVariant(failing, []probed{failing, direct}); got != "warp_path" {
		t.Fatalf("got %s", got)
	}
	if got := whyVariant(failing, []probed{failing}); got != "exit_unreachable" {
		t.Fatalf("no passing direct inbound: %s", got)
	}
	timeout := probed{spec(443, "direct"), cell{last: &Result{ErrorCode: "timeout"}, streak: 3}}
	if got := whyVariant(timeout, []probed{timeout, direct}); got != "udp_blocked" {
		t.Fatalf("got %s", got)
	}
	if got := whyVariant(timeout, []probed{timeout}); got != "timeout" {
		t.Fatalf("got %s", got)
	}
}

// A silence shorter than the blip window is no alert; a longer one opens NODE_DOWN, suppresses the node's other
// alerts without resolving them, and resolves as node_returned when the node is back. A panel that just started
// does not call the silent nodes down while their agents reconnect.
func TestNodeDownAndBlip(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a := e.inbound("de1", 443)
	e.report("de1", false, res("disk_space", dWarn, "journald_vacuum"))
	want(t, e.active(), "doctor_warn/de1/disk_space")
	_ = a

	e.fl.set("de1", liveState{up: false, caps: []string{capDoctor}})
	e.clock.Advance(5 * time.Minute) // a blip
	e.evaluate()
	want(t, e.active(), "doctor_warn/de1/disk_space")

	e.clock.Advance(6 * time.Minute) // 11 minutes of silence
	e.evaluate()
	got := e.active()
	want(t, got, "doctor_warn/de1/disk_space", "node_down/de1/")
	if nd := got["node_down/de1/"]; nd.Severity != sevCritical || nd.Params["minutes"] == "" {
		t.Fatalf("node_down: %+v", nd)
	}

	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor}})
	e.evaluate()
	want(t, e.active(), "doctor_warn/de1/disk_space")
	found := false
	for _, h := range e.history() {
		if h.Kind == kNodeDown {
			found = h.Resolution == "node_returned"
		}
	}
	if !found {
		t.Fatalf("history: %+v", e.history())
	}
}

func TestNodeDownWaitsForTheStartGrace(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.StartGrace = 5 * time.Minute })
	e.node("de1", "hetzner", false)
	old := e.clock.Now().Add(-time.Hour).Unix() // long silent before the panel started
	e.exec(`UPDATE node SET last_seen_at = ?, last_connected_at = ?`, old, old)
	e.evaluate()
	want(t, e.active())
	e.clock.Advance(6 * time.Minute)
	e.evaluate()
	want(t, e.active(), "node_down/de1/")
}

func TestHostBlipIsAResolvedHistoryRecord(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	now := e.clock.Now()
	e.s.NodeReturned(e.ctx, "de1", now.Add(-4*time.Minute), now)
	want(t, e.active())
	h := e.history()
	if len(h) != 1 || h[0].Kind != kHostBlip || h[0].Severity != sevInfo || h[0].Resolution != "node_returned" || h[0].Params["minutes"] != "4" ||
		!h[0].FirstSeen.Equal(now.Add(-4*time.Minute)) {
		t.Fatalf("history: %+v", h)
	}
	if n, _ := e.s.AlertCounts(e.ctx); n != 0 {
		t.Fatal("a blip record was counted in the badge")
	}
}

// Doctor rows become alerts as the report says (debounce is the agent's), WARN -> FAIL supersedes, the next OK
// resolves, and a resolve right after ApplyFix is "fix_applied".
func TestDoctorAlerts(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)

	e.report("de1", false, res("disk_space", dWarn, "journald_vacuum", "used_pct", "85"), res("resolver", dOK, ""), res("ipv6", dSkip, ""))
	got := e.active()
	want(t, got, "doctor_warn/de1/disk_space")
	al := got["doctor_warn/de1/disk_space"]
	if al.Severity != sevWarning || al.WhyKey != "health.doctor.disk_space.why" || al.Params["used_pct"] != "85" || al.Params["fix_id"] != "journald_vacuum" ||
		al.TitleKey != "health.alert.doctor_warn.title" || al.Subject != "disk_space" {
		t.Fatalf("alert: %+v", al)
	}

	e.report("de1", true, res("disk_space", dFail, "journald_vacuum", "used_pct", "95")) // a partial report merges
	got = e.active()
	want(t, got, "doctor_fail/de1/disk_space")
	if got["doctor_fail/de1/disk_space"].Severity != sevCritical {
		t.Fatal("FAIL is not critical")
	}
	if h := e.history(); len(h) != 1 || h[0].Kind != kDoctorWarn || h[0].Resolution != "superseded" {
		t.Fatalf("history: %+v", h)
	}

	// the admin applies the fix, then the re-check is OK
	e.s.fixMu.Lock()
	e.s.recentFix["de1/journald_vacuum"] = e.clock.Now()
	e.s.fixMu.Unlock()
	e.report("de1", true, res("disk_space", dOK, ""))
	want(t, e.active())
	if h := e.history(); h[0].Kind != kDoctorFail || h[0].Resolution != "fix_applied" {
		t.Fatalf("history: %+v", h)
	}
	if rows, _ := e.st.DoctorResults(e.ctx, "de1"); len(rows) != 3 {
		t.Fatalf("the partial report dropped the other checks: %d rows", len(rows))
	}

	// not after a fix: cleared
	e.report("de1", true, res("resolver", dFail, ""))
	e.report("de1", true, res("resolver", dOK, ""))
	if h := e.history(); h[0].Kind != kDoctorFail || h[0].Subject != "resolver" || h[0].Resolution != "cleared" {
		t.Fatalf("history: %+v", h)
	}
}

func TestCertExpiryFromDoctorNeverDuplicatesAsDoctorAlert(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("cert_expiry", dWarn, "", "days_left", "9"))
	want(t, e.active(), "cert_expiry/de1/cert")
	e.report("de1", false, res("cert_expiry", dFail, "", "days_left", "2", "inbound_id", "inb_x"))
	got := e.active()
	want(t, got, "cert_expiry/de1/inb_x")
	if got["cert_expiry/de1/inb_x"].Severity != sevCritical {
		t.Fatal("a failing certificate is critical")
	}
}

func TestKernelHeadersIsInfoWithoutAmneziaWG(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("kernel_headers", dWarn, ""))
	if al := e.active()["doctor_warn/de1/kernel_headers"]; al.Severity != sevInfo {
		t.Fatalf("kernel_headers: %+v", al)
	}
	if n, _ := e.s.AlertCounts(e.ctx); n != 0 {
		t.Fatalf("an INFO alert is in the badge: %d", n)
	}
}

func TestDoctorFixIdsAreTheFiveKnownOnes(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dWarn, "rm_rf_slash"), res("resolver", dWarn, "set_resolver"), res("ipv6", dOK, "set_resolver"), res("warp_path", dFail, "reconnect_warp"))
	rows, _ := e.st.DoctorResults(e.ctx, "de1")
	fixes := map[string]string{}
	for _, r := range rows {
		fixes[r.CheckID] = r.FixID
	}
	if fixes["disk_space"] != "" || fixes["resolver"] != "set_resolver" || fixes["ipv6"] != "" || fixes["warp_path"] != "reconnect_warp" {
		t.Fatalf("fix ids: %v", fixes)
	}
}

// A stale report says nothing about the present: its alerts stay as they are and no new ones open from it.
func TestStaleDoctorReportHoldsItsAlerts(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dWarn, ""))
	e.clock.Advance(30 * time.Minute)
	e.evaluate()
	want(t, e.active(), "doctor_warn/de1/disk_space")
	if _, _, _, fail := e.s.NodeHealth("de1"); fail != "" {
		t.Fatal("stale doctor state reaches the node status")
	}
	e.report("de1", false, res("disk_space", dOK, "")) // a fresh report decides again
	want(t, e.active())
}

func TestStateDriftFollowsTheFleet(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor}, drift: true})
	e.evaluate()
	want(t, e.active(), "state_drift/de1/")
	e.fl.set("de1", liveState{up: true, caps: []string{capDoctor}})
	e.evaluate()
	want(t, e.active())
}

func TestRetiredNodeResolvesItsAlerts(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dWarn, ""))
	e.exec(`UPDATE node SET state = 'retired', retired_at = 1 WHERE id = 'de1'`)
	e.evaluate()
	want(t, e.active())
	if h := e.history(); len(h) != 1 || h[0].Resolution != "node_retired" {
		t.Fatalf("history: %+v", h)
	}
}

// A muted alert still resolves; the badge leaves it out while it is muted and takes it back after.
func TestMuteHidesFromBadgeNotFromResolving(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dFail, ""), res("resolver", dWarn, ""))
	if n, c := e.s.AlertCounts(e.ctx); n != 2 || c != 1 {
		t.Fatalf("badge %d/%d", n, c)
	}
	id := e.active()["doctor_fail/de1/disk_space"].ID
	if _, err := e.st.MuteAlert(e.ctx, id, e.clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n, c := e.s.AlertCounts(e.ctx); n != 1 || c != 0 {
		t.Fatalf("badge while muted %d/%d", n, c)
	}
	e.evaluate() // evaluation goes on, the mute stays
	if al := e.active()["doctor_fail/de1/disk_space"]; al.MutedUntil.IsZero() {
		t.Fatal("mute lost")
	}
	e.clock.Advance(2 * time.Hour)
	e.report("de1", false, res("disk_space", dFail, ""), res("resolver", dWarn, ""))
	if n, _ := e.s.AlertCounts(e.ctx); n != 2 {
		t.Fatalf("badge after the mute ended: %d", n)
	}
	e.report("de1", false, res("disk_space", dOK, ""), res("resolver", dWarn, ""))
	var resolved bool
	for _, h := range e.history() {
		resolved = resolved || (h.ID == id && h.Resolution == "cleared")
	}
	if !resolved {
		t.Fatal("a muted alert did not resolve")
	}
}

// When most probed inbounds fail on nodes of three providers the panel's own network is suspect: one fleet
// alert, no per-node alerts, no NO_TRAFFIC status; it resolves when rounds pass again.
func TestBlindPanelGuard(t *testing.T) {
	e := newEnv(t)
	var ins []string
	for _, n := range []struct{ id, provider string }{{"n1", "hetzner"}, {"n2", "hostera"}, {"n3", "hosterb"}, {"n4", "hetzner"}} {
		e.node(n.id, n.provider, true)
		ins = append(ins, e.inbound(n.id, 443))
	}
	for _, in := range ins {
		e.fail(in, "timeout")
	}
	for _, in := range ins {
		e.fail(in, "timeout")
	}
	got := e.active()
	want(t, got, "check_failed//panel_egress")
	if got["check_failed//panel_egress"].WhyKey != "health.alert.check_failed.why.panel_egress" {
		t.Fatalf("guard alert: %+v", got)
	}
	if nt, _, _, _ := e.s.NodeHealth("n1"); nt {
		t.Fatal("NO_TRAFFIC status while the panel is blind")
	}
	for _, in := range ins {
		e.pass(in)
	}
	want(t, e.active())
}

func TestGuardNeedsThreeProviders(t *testing.T) {
	e := newEnv(t)
	var ins []string
	for _, n := range []struct{ id, provider string }{{"n1", "hetzner"}, {"n2", "hetzner"}, {"n3", "hostera"}} {
		e.node(n.id, n.provider, true)
		ins = append(ins, e.inbound(n.id, 443))
	}
	for range 2 {
		for _, in := range ins {
			e.fail(in, "timeout")
		}
	}
	got := e.active()
	want(t, got, "no_traffic/n1/", "no_traffic/n2/", "no_traffic/n3/")
}

// The agent's own view of a certificate is the fallback for agents without a doctor.
func TestPanelViewOfCertificates(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a := e.inbound("de1", 443)
	e.exec(`UPDATE inbound SET cert_not_after = ? WHERE id = ?`, e.clock.Now().Add(10*24*time.Hour).Unix(), a)
	e.evaluate()
	got := e.active()
	want(t, got, "cert_expiry/de1/"+a)
	if al := got["cert_expiry/de1/"+a]; al.Severity != sevWarning || al.Params["days_left"] != "10" {
		t.Fatalf("10 days: %+v", al)
	}
	e.exec(`UPDATE inbound SET cert_not_after = ? WHERE id = ?`, e.clock.Now().Add(-time.Hour).Unix(), a)
	e.evaluate()
	if al := e.active()["cert_expiry/de1/"+a]; al.Severity != sevCritical || al.WhyKey != "health.alert.cert_expiry.why.expired" {
		t.Fatalf("expired: %+v", al)
	}
	// with a doctor report that covers certificates, the doctor is the only source
	e.report("de1", false, res("cert_expiry", dOK, ""))
	want(t, e.active())
}

func TestTransitionHook(t *testing.T) {
	var seen []Transition
	e := newEnv(t, func(c *Config) { c.OnTransition = func(tr Transition) { seen = append(seen, tr) } })
	e.node("de1", "hetzner", true)
	e.report("de1", false, res("disk_space", dWarn, ""))
	e.report("de1", false, res("disk_space", dOK, ""))
	if len(seen) != 2 || seen[0].Resolved || !seen[1].Resolved || seen[1].Alert.Resolution != "cleared" {
		t.Fatalf("transitions: %+v", seen)
	}
}

func TestReportsAreCleanedAndBounded(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	long := make([]byte, 600)
	for i := range long {
		long[i] = 'x'
	}
	r := res("disk_space", dWarn, "")
	r.Detail = string(long)
	r.Params["k"] = string(long)
	bad := []*agentv1.DoctorResult{res("Bad Id!", dWarn, ""), {Id: "ok_one", Status: 9}}
	all := append([]*agentv1.DoctorResult{r}, bad...)
	e.s.DoctorReport(e.ctx, "de1", &agentv1.DoctorReport{Results: all})
	rows, _ := e.st.DoctorResults(e.ctx, "de1")
	if len(rows) != 1 || len(rows[0].Detail) > 256 || len(rows[0].Params["k"]) > 256 || rows[0].TitleKey != "doctor.disk_space.title" {
		t.Fatalf("rows: %+v", rows)
	}
	// a report that says it did not run changes nothing
	e.s.DoctorReport(e.ctx, "de1", &agentv1.DoctorReport{Error: "busy", Results: []*agentv1.DoctorResult{res("ipv6", dFail, "")}})
	if rows, _ := e.st.DoctorResults(e.ctx, "de1"); len(rows) != 1 {
		t.Fatalf("busy report stored: %+v", rows)
	}
}

// A result from before the node was away is not an answer of now: its alert stays until a fresh round decides, and
// no alert opens from it.
func TestStaleCheckResultsHoldTheirAlerts(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	a, b := e.inbound("de1", 443), e.inbound("de1", 8443)
	e.pass(b)
	e.fail(a, "tls")
	e.fail(a, "tls")
	want(t, e.active(), "check_failed/de1/"+a)

	e.clock.Advance(20 * time.Minute) // nobody probed for a while (the node was away)
	e.evaluate()
	want(t, e.active(), "check_failed/de1/"+a)
	if nt, _, _, _ := e.s.NodeHealth("de1"); nt {
		t.Fatal("stale results feed the node status")
	}
	e.pass(a)
	e.pass(b)
	want(t, e.active())
}

// Alerts that opened before the whole picture was visible (the first inbounds to fail twice) are superseded by the
// fleet alert once most probed inbounds on three providers fail.
func TestGuardSupersedesEarlierNodeAlerts(t *testing.T) {
	e := newEnv(t)
	var ins []string
	for _, n := range []struct{ id, provider string }{{"n1", "hetzner"}, {"n2", "hostera"}, {"n3", "hosterb"}} {
		e.node(n.id, n.provider, true)
		ins = append(ins, e.inbound(n.id, 443))
	}
	e.fail(ins[0], "timeout")
	e.fail(ins[0], "timeout")
	want(t, e.active(), "no_traffic/n1/")
	e.fail(ins[1], "timeout")
	e.fail(ins[2], "timeout")
	want(t, e.active(), "check_failed//panel_egress")
	var superseded bool
	for _, h := range e.history() {
		superseded = superseded || (h.Kind == kNoTraffic && h.NodeID == "n1" && h.Resolution == "superseded")
	}
	if !superseded {
		t.Fatalf("history: %+v", e.history())
	}
}

// The doctor list is shown in the order of agent.proto CHECK IDS, the L3 checks included; an id the panel does not
// know yet goes after them.
func TestDoctorRowsFollowTheCheckIDOrder(t *testing.T) {
	ids := []string{"warp_path", "aa_new", "awg_backend", "kernel_headers", "disk_space"}
	rows := make([]store.DoctorRow, len(ids))
	for i, id := range ids {
		rows[i].CheckID = id
	}
	sortDoctor(rows)
	var got []string
	for _, r := range rows {
		got = append(got, r.CheckID)
	}
	if want := "disk_space kernel_headers awg_backend warp_path aa_new"; strings.Join(got, " ") != want {
		t.Fatalf("order %v, want %s", got, want)
	}
}
