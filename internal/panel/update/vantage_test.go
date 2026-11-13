package update

import (
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// The gate's second opinion (vantage.go): the panel's own path to a node may be lossy while the node is fine.

// resolver plays the doctor's resolver result of a node.
func (e *env) resolver(id string, status int, measured time.Time) {
	e.t.Helper()
	row := store.DoctorRow{NodeID: id, CheckID: "resolver", Status: status, TitleKey: "doctor.resolver", Measured: measured}
	if err := e.st.PutDoctor(e.ctx, id, []store.DoctorRow{row}, false, e.clk.Now()); err != nil {
		e.t.Fatal(err)
	}
}

// seenFrom is what a port check from sender answers: every UDP port ok and every TCP port ours, measured now.
func (e *env) seenFrom(sender string) (string, []store.PortCheck, []store.TCPCheck, string) {
	at := e.clk.Now()
	return sender, []store.PortCheck{{Port: 443, Verdict: "ok", Sender: sender, CheckedAt: at}},
		[]store.TCPCheck{{Port: 443, Verdict: tcpcheck.VerdictOurs, Sender: sender, CheckedAt: at}}, ""
}

// seenBy answers every run with seenFrom(sender).
func (e *env) seenBy(sender string) {
	e.fl.onGate = func(string, int) (string, []store.PortCheck, []store.TCPCheck, string) { return e.seenFrom(sender) }
}

// osLossyGate: nl1's OS job is done, the step is gating, the panel's probes time out (for two minutes), the node's own
// word is good and nl2 is another node that could look. The gate has not looked yet.
func (e *env) osLossyGate() (id, other string, x store.OsStepRow) {
	e.t.Helper()
	other = e.addNode("nl2", nodeOpts{caps: osCaps})
	id = e.addNode("nl1", nodeOpts{caps: osCaps, inbounds: 1})
	e.osBoot(id, e.clk.Now().Add(-48*time.Hour), kernelOld)
	e.osStatus(id, nil)
	e.hl.lossy(id, true, 2*time.Minute)
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x = e.osStep(id)
	e.osJob(id, &agentv1.OsJob{JobId: x.JobID, Action: "upgrade", Mode: "security", State: "done", Upgraded: 3, Reboot: "not_asked"})
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "3", "reboot": "not_asked", "kernel": kernelOld})
	e.clk.Advance(time.Second)
	e.resolver(id, 1, e.clk.Now())
	return id, other, x
}

// failAtDeadline lets the gate window run out and ticks once.
func (e *env) failAtDeadline() {
	e.t.Helper()
	e.clk.Advance(9 * time.Minute)
	e.osTick()
}

func TestVantagePassesWhenOnlyThePanelPathIsLossy(t *testing.T) {
	e := newEnv(t)
	id, other, _ := e.osLossyGate()
	e.seenBy(other)
	for i := 0; i < 4; i++ { // the first tick starts V (it ends inside the tick helper), a later one reads it
		e.osTick()
	}
	x := e.wantOsStep(id, store.OsStepPassed, "")
	if x.Params["vantage"] != "nl2" || x.Params["panel_path"] != "lossy" || x.Params["upgraded"] != "3" {
		t.Fatalf("step params %v", x.Params)
	}
	if n := e.fl.gateRuns(id); n != 1 {
		t.Fatalf("the second vantage ran %d times, want once per window", n)
	}
	if ev := e.wantEvent(id, "panel_path_lossy"); ev.Severity != 1 || ev.Params["vantage"] != "nl2" {
		t.Fatalf("event %+v", ev)
	}
	e.wantOsRollout(store.RolloutDone, "")
}

func TestVantageAgentRolloutPassesToo(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	other := e.addNode("nl2", nodeOpts{})
	a := e.addNode("nl1", nodeOpts{inbounds: 1})
	e.seenBy(other)
	e.hl.lossy(a, true, 2*time.Minute)
	e.startRollout(a)
	e.tick()
	e.upgrade(a)
	e.tick()
	e.commit(a)
	e.clk.Advance(time.Second)
	e.resolver(a, 1, e.clk.Now())
	for i := 0; i < 3; i++ {
		e.clk.Advance(5 * time.Second)
		e.tick()
	}
	x := e.wantStep(a, store.StepPassed, "")
	if x.Params["vantage"] != "nl2" || x.Params["panel_path"] != "lossy" {
		t.Fatalf("step params %v", x.Params)
	}
	if len(e.fl.rolledBack()) != 0 {
		t.Fatal("a pass was rolled back")
	}
}

func TestVantageNeverAskedWhenTheFailureIsNotATimeout(t *testing.T) {
	e := newEnv(t)
	id, other, _ := e.osLossyGate()
	e.seenBy(other)
	e.hl.lossy(id, false, 2*time.Minute) // a tls failure, a refusal: the node's own fault
	e.osTick()
	e.osTick()
	e.failAtDeadline()
	x := e.wantOsStep(id, store.OsStepFailed, errProbeFailed)
	if n := e.fl.gateRuns(id); n != 0 || x.Params["vantage"] != "" || x.Params["vantage_verdict"] != "" {
		t.Fatalf("asked %d times, params %v", n, x.Params)
	}
}

func TestVantageNeedsTheFailuresToHaveLastedAMinute(t *testing.T) {
	e := newEnv(t)
	id, other, _ := e.osLossyGate()
	e.seenBy(other)
	e.hl.lossy(id, true, 59*time.Second)
	e.osTick()
	e.osTick()
	if n := e.fl.gateRuns(id); n != 0 {
		t.Fatalf("asked after 59 s of failures: %d", n)
	}
	e.wantOsStep(id, store.OsStepGating, "")
}

func TestVantageNeedsAFreshResolverAnswerFromTheNode(t *testing.T) {
	for name, set := range map[string]func(e *env, id string){
		"stale": func(e *env, id string) { e.resolver(id, 1, e.clk.Now().Add(-time.Hour)) },
		"warn":  func(e *env, id string) { e.resolver(id, 2, e.clk.Now()) },
		"none":  func(e *env, id string) { e.exec(`DELETE FROM doctor_result WHERE node_id = ?`, id) },
		"stopped inbound": func(e *env, id string) {
			e.exec(`UPDATE inbound SET state = 'pending' WHERE node_id = ?`, id)
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			id, other, _ := e.osLossyGate()
			e.seenBy(other)
			set(e, id)
			e.osTick()
			e.osTick()
			e.failAtDeadline()
			e.wantOsStep(id, store.OsStepFailed, errProbeFailed)
			if n := e.fl.gateRuns(id); n != 0 {
				t.Fatalf("asked %d times without the node's own word", n)
			}
		})
	}
}

func TestVantageSaysBadWhenAnotherNodeDoesNotSeeThePortsEither(t *testing.T) {
	e := newEnv(t)
	id, other, _ := e.osLossyGate()
	e.fl.onGate = func(string, int) (string, []store.PortCheck, []store.TCPCheck, string) {
		sender, udp, tcp, code := e.seenFrom(other)
		udp[0].Verdict = "lossy"
		return sender, udp, tcp, code
	}
	for i := 0; i < 3; i++ {
		e.osTick()
	}
	e.failAtDeadline()
	x := e.wantOsStep(id, store.OsStepFailed, errProbeFailed)
	if x.Params["vantage_verdict"] != "bad" || x.Params["vantage"] != "nl2" || x.Params["panel_path"] != "" {
		t.Fatalf("step params %v", x.Params)
	}
	if n := e.fl.gateRuns(id); n != 1 {
		t.Fatalf("a bad answer was asked again: %d runs", n)
	}
}

func TestVantageAnErroredRunIsAskedAgain(t *testing.T) {
	e := newEnv(t)
	id, other, _ := e.osLossyGate()
	e.fl.onGate = func(_ string, run int) (string, []store.PortCheck, []store.TCPCheck, string) {
		if run == 1 {
			return other, nil, nil, "inconclusive"
		}
		return e.seenFrom(other)
	}
	for i := 0; i < 6; i++ {
		e.osTick()
	}
	e.wantOsStep(id, store.OsStepPassed, "")
	if n := e.fl.gateRuns(id); n != 2 {
		t.Fatalf("runs %d, want the errored one and its retry", n)
	}
}

func TestVantageGivesUpAfterThreeErroredRuns(t *testing.T) {
	e := newEnv(t)
	id, other, _ := e.osLossyGate()
	e.fl.onGate = func(string, int) (string, []store.PortCheck, []store.TCPCheck, string) {
		return other, nil, nil, "failed"
	}
	for i := 0; i < 8; i++ {
		e.osTick()
	}
	e.failAtDeadline()
	x := e.wantOsStep(id, store.OsStepFailed, errProbeFailed)
	if n := e.fl.gateRuns(id); n != 3 || x.Params["vantage_verdict"] != "none" || x.Params["no_vantage"] != "" {
		t.Fatalf("runs %d, params %v", n, x.Params)
	}
}

func TestVantageNoOtherNodeActsAsToday(t *testing.T) {
	e := newEnv(t)
	id, _, _ := e.osLossyGate() // the fake answers "no_sender" without onGate
	for i := 0; i < 4; i++ {
		e.osTick()
	}
	e.wantOsStep(id, store.OsStepGating, "") // nothing passes early
	e.failAtDeadline()
	x := e.wantOsStep(id, store.OsStepFailed, errProbeFailed)
	if x.Params["no_vantage"] != "1" || x.Params["vantage_verdict"] != "none" {
		t.Fatalf("step params %v", x.Params)
	}
	if n := e.fl.gateRuns(id); n != 1 {
		t.Fatalf("runs %d", n)
	}
}

func TestVantageTheOnlySenderBeingThePanelOrTheTargetIsNoVantage(t *testing.T) {
	for _, sender := range []string{"panel", "nod_nl1"} {
		e := newEnv(t)
		id, _, _ := e.osLossyGate()
		e.seenBy(sender)
		for i := 0; i < 4; i++ {
			e.osTick()
		}
		e.wantOsStep(id, store.OsStepGating, "")
		e.failAtDeadline()
		e.wantOsStep(id, store.OsStepFailed, errProbeFailed)
	}
}

func TestVantageRunsAgainAfterAPanelRestart(t *testing.T) {
	e := newEnv(t)
	id, other, _ := e.osLossyGate()
	// the first run is answered but never read: the check is decided in memory only
	e.seenBy(other)
	e.osTick()
	e.osTick()
	if n := e.fl.gateRuns(id); n != 1 {
		t.Fatalf("runs %d before the restart", n)
	}
	e.restart()
	e.s.recoverOs(e.ctx)
	e.osTick()
	if n := e.fl.gateRuns(id); n != 2 {
		t.Fatalf("runs %d after the restart, want the check to be asked again", n)
	}
	e.osTick()
	e.wantOsStep(id, store.OsStepPassed, "")
}

func TestVantageTheTickDoesNotWaitForIt(t *testing.T) {
	e := newEnv(t)
	id, other, _ := e.osLossyGate()
	started, release := make(chan struct{}), make(chan struct{})
	e.fl.onGate = func(string, int) (string, []store.PortCheck, []store.TCPCheck, string) {
		close(started)
		<-release
		return e.seenFrom(other)
	}
	e.osTick() // the gate window begins
	done := make(chan struct{})
	go func() {
		e.s.tickOs(e.ctx) // not the helper: it would wait for the goroutines
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the tick waited for the second vantage")
	}
	<-started
	// the window runs out while the check is still going: the step waits for it instead of failing
	e.clk.Advance(9 * time.Minute)
	e.s.tickOs(e.ctx)
	e.wantOsStep(id, store.OsStepGating, "")
	close(release)
	e.s.wg.Wait()
	e.osTick()
	e.wantOsStep(id, store.OsStepPassed, "")
}

func TestVantageVerdict(t *testing.T) {
	since := time.Unix(1_791_100_000, 0)
	at := since.Add(time.Second)
	ok := func() []store.PortCheck { return []store.PortCheck{{Port: 443, Verdict: "ok", CheckedAt: at}} }
	ours := func() []store.TCPCheck {
		return []store.TCPCheck{{Port: 443, Verdict: tcpcheck.VerdictOurs, CheckedAt: at}}
	}
	cases := []struct {
		name, sender, code string
		udp                []store.PortCheck
		tcp                []store.TCPCheck
		want               string
	}{
		{"all seen", "nod_b", "", ok(), ours(), vantageOK},
		{"udp only", "nod_b", "", ok(), nil, vantageOK},
		{"udp lossy", "nod_b", "", []store.PortCheck{{Port: 443, Verdict: "broken", CheckedAt: at}}, ours(), vantageBad},
		{"tcp foreign", "nod_b", "", ok(), []store.TCPCheck{{Port: 443, Verdict: tcpcheck.VerdictOther, CheckedAt: at}}, vantageBad},
		{"tcp refused", "nod_b", "", ok(), []store.TCPCheck{{Port: 443, Verdict: tcpcheck.VerdictRefused, CheckedAt: at}}, vantageBad},
		{"tcp did not run", "nod_b", "", ok(), []store.TCPCheck{{Port: 443, Verdict: "", CheckedAt: at}}, vantageErr},
		{"older udp row", "nod_b", "", []store.PortCheck{{Port: 443, Verdict: "ok", CheckedAt: since.Add(-time.Second)}}, ours(), vantageErr},
		{"older tcp row", "nod_b", "", ok(), []store.TCPCheck{{Port: 443, Verdict: tcpcheck.VerdictOurs, CheckedAt: since.Add(-time.Second)}}, vantageErr},
		{"no rows", "nod_b", "", nil, nil, vantageErr},
		{"the panel", "panel", "", ok(), ours(), vantageNone},
		{"the target", "nod_a", "", ok(), ours(), vantageNone},
		{"no sender", "", "no_sender", nil, nil, vantageNone},
		{"same host", "nod_b", "same_host", ok(), nil, vantageNone},
		{"inconclusive", "nod_b", "inconclusive", ok(), ours(), vantageErr},
	}
	for _, c := range cases {
		if got := vantageVerdict("nod_a", since, c.sender, c.udp, c.tcp, c.code); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}
