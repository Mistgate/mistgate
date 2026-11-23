package update

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// round is the newest check round of an inbound, `ago` before now.
func (e *env) round(inbound string, status adminv1.CheckStatus, code string, ago time.Duration) store.CheckSample {
	return store.CheckSample{InboundID: inbound, At: e.clk.Now().Add(-ago), Status: int(status), ErrorCode: code}
}

const (
	checkOK   = adminv1.CheckStatus_CHECK_STATUS_OK
	checkFail = adminv1.CheckStatus_CHECK_STATUS_FAILED
)

// nameProfiles gives the profiles of the node their names ("Main", "Backup", ...).
func (e *env) nameProfiles(node string, names ...string) {
	e.t.Helper()
	for i, name := range names {
		e.exec(`UPDATE profile SET name = ? WHERE id = ?`, name, "prf_"+node+"_"+string(rune('0'+i)))
	}
}

// reachGate sends the step, plays the update and the commit and lets the gate window run to its deadline's edge.
func (e *env) reachGate(node string) {
	e.t.Helper()
	e.upgrade(node)
	e.tick()
	e.commit(node)
	e.clk.Advance(2 * time.Minute)
}

func failedList(t *testing.T, params map[string]string) []FailedProfile {
	t.Helper()
	var list []FailedProfile
	if err := json.Unmarshal([]byte(params[KeyFailedProfiles]), &list); err != nil {
		t.Fatalf("failed_profiles %q: %v", params[KeyFailedProfiles], err)
	}
	return list
}

// A profile whose check was failing before the node was touched is not what the update broke: the step passes and says so.
func TestGateDoesNotHoldAProfileThatFailedBeforeTheUpdate(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("de1", nodeOpts{inbounds: 1})
	e.nameProfiles("de1", "Main")
	e.hl.setRounds(a, e.round("inb_de1_0", checkFail, "timeout", time.Minute))
	e.hl.set(a, 1, 0, 1) // still failing after the update
	e.hl.setPending(a, "inb_de1_0")
	e.startRollout()
	e.tick()
	if pre := e.step(a).PreFailed; !slices.Equal(pre, []string{"probe:inb_de1_0"}) {
		t.Fatalf("pre_failed %v", pre)
	}
	e.reachGate(a)
	e.tick()
	x := e.wantStep(a, store.StepPassed, "")
	if x.Params[KeySkippedProfiles] != `["Main"]` {
		t.Fatalf("params %v: the profile that did not work before is not named", x.Params)
	}
}

// A profile that worked before and fails after is the update's fault: the step is rolled back, and the step, the event and the pause
// name the profile and the error of its last round. A profile that was already failing is not named.
func TestGateNamesTheProfileThatFailedAfterTheUpdate(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("de1", nodeOpts{inbounds: 3})
	e.nameProfiles("de1", "Old", "Main", "Backup")
	e.hl.setRounds(a,
		e.round("inb_de1_0", checkFail, "timeout", 2*time.Minute), // sick before
		e.round("inb_de1_1", checkOK, "", time.Minute),
		e.round("inb_de1_2", checkOK, "", time.Minute))
	e.startRollout()
	e.tick()
	if pre := e.step(a).PreFailed; !slices.Equal(pre, []string{"probe:inb_de1_0"}) {
		t.Fatalf("pre_failed %v", pre)
	}
	e.reachGate(a)
	e.hl.set(a, 3, 1, 2)
	e.hl.setPending(a, "inb_de1_0", "inb_de1_1")
	e.hl.setRounds(a,
		e.round("inb_de1_0", checkFail, "timeout", 0),
		e.round("inb_de1_1", checkFail, "refused", 0),
		e.round("inb_de1_2", checkOK, "", 0))
	e.tick()
	e.wantStep(a, store.StepGating, "")
	e.clk.Advance(3*time.Minute + time.Second)
	e.tick()
	x := e.wantStep(a, store.StepRolledBack, "probe_failed")
	want := []FailedProfile{{ID: "inb_de1_1", Name: "Main", Code: "refused"}}
	if got := failedList(t, x.Params); !slices.Equal(got, want) {
		t.Fatalf("step names %+v, want %+v", got, want)
	}
	if got := failedList(t, e.wantEvent(a, "update_step_rolled_back").Params); !slices.Equal(got, want) {
		t.Fatalf("event names %+v, want %+v", got, want)
	}
	if got := failedList(t, e.wantRollout(store.RolloutPaused, "gate_failed").PauseParams); !slices.Equal(got, want) {
		t.Fatalf("pause names %+v, want %+v", got, want)
	}
}

// Every profile was sick before and still is: nothing is left to hold the step for, and it passes at once, saying which were skipped.
func TestGatePassesWhenEveryProfileWasSickBefore(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("nl1", nodeOpts{inbounds: 2})
	e.nameProfiles("nl1", "Main", "Backup")
	e.hl.setRounds(a, e.round("inb_nl1_0", checkFail, "timeout", time.Minute), e.round("inb_nl1_1", checkFail, "timeout", time.Minute))
	e.hl.set(a, 2, 0, 2)
	e.hl.setPending(a, "inb_nl1_0", "inb_nl1_1")
	e.startRollout()
	e.tick()
	e.reachGate(a)
	e.tick()
	x := e.wantStep(a, store.StepPassed, "")
	if x.Params[KeySkippedProfiles] != `["Backup","Main"]` {
		t.Fatalf("params %v", x.Params)
	}
}

// A failing round older than probeSickAge says nothing about the moment of the send: the profile counts as one that worked.
func TestGateIgnoresAnOldFailedRoundAsABaseline(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("de1", nodeOpts{inbounds: 1})
	e.hl.setRounds(a, e.round("inb_de1_0", checkFail, "timeout", probeSickAge+time.Minute))
	e.hl.set(a, 1, 0, 1)
	e.hl.setPending(a, "inb_de1_0")
	e.startRollout()
	e.tick()
	if pre := e.step(a).PreFailed; len(pre) != 0 {
		t.Fatalf("pre_failed %v", pre)
	}
	e.reachGate(a)
	e.tick()
	e.clk.Advance(3*time.Minute + time.Second)
	e.tick()
	e.wantStep(a, store.StepRolledBack, "probe_failed")
}

// The baseline is about the checks only: a profile the node could not start after the update still fails the step at once,
// whatever its checks did before.
func TestGateHardFailureIgnoresTheProbeBaseline(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	a := e.addNode("de1", nodeOpts{inbounds: 1})
	e.hl.setRounds(a, e.round("inb_de1_0", checkFail, "timeout", time.Minute))
	e.hl.set(a, 1, 0, 1)
	e.hl.setPending(a, "inb_de1_0")
	e.startRollout()
	e.tick()
	e.reachGate(a)
	e.exec(`UPDATE inbound SET state = 'failed' WHERE id = 'inb_de1_0'`)
	e.tick()
	e.clk.Advance(31 * time.Second)
	e.tick()
	e.wantStep(a, store.StepRolledBack, "inbound_failed")
}

// The OS update reads the same baseline: a profile whose check failed before the job does not fail its gate.
func TestOsGateIgnoresAProfileWhoseCheckFailedBeforeTheJob(t *testing.T) {
	e := newEnv(t)
	id := e.addNode("de1", nodeOpts{caps: osCaps, inbounds: 1})
	e.osBoot(id, e.clk.Now().Add(-48*time.Hour), kernelOld)
	e.osStatus(id, nil)
	e.hl.setRounds(id, e.round("inb_de1_0", checkFail, "timeout", time.Minute))
	e.hl.set(id, 1, 0, 1)
	e.hl.setPending(id, "inb_de1_0")
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	if !slices.Equal(x.PreFailed, []string{"probe:inb_de1_0"}) {
		t.Fatalf("pre-failed %v", x.PreFailed)
	}
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
	e.osTick()
	e.osTick()
	e.clk.Advance(time.Minute)
	e.osTick()
	e.wantOsStep(id, store.OsStepPassed, "")
}

// When the health module cannot say what the newest rounds were, the baseline is empty (the update is judged as it was before the
// rule), and that is not silent: the warning carries its code.
func TestLastRoundsUnreadableIsLogged(t *testing.T) {
	e := newEnv(t)
	var buf bytes.Buffer
	e.s.log = slog.New(slog.NewTextHandler(&buf, nil))
	e.hl.setError("de1", errors.New("snapshot failed"))
	if got := e.s.lastRounds(e.ctx, "de1"); got != nil {
		t.Fatalf("rounds = %v, want none", got)
	}
	if out := buf.String(); !strings.Contains(out, "code=update_last_rounds_unreadable") || !strings.Contains(out, "level=WARN") {
		t.Fatalf("log = %q, want a warning with code update_last_rounds_unreadable", out)
	}
}

// The OS update names the profile that did not pass on its step, on the event and on the pause, as the agent update does.
func TestOsGateNamesTheProfileThatFailedAfterTheJob(t *testing.T) {
	e := newEnv(t)
	id := e.addNode("de1", nodeOpts{caps: osCaps, inbounds: 2})
	e.nameProfiles("de1", "Main", "Backup")
	e.osBoot(id, e.clk.Now().Add(-48*time.Hour), kernelOld)
	e.osStatus(id, nil)
	e.hl.setRounds(id, e.round("inb_de1_0", checkOK, "", time.Minute), e.round("inb_de1_1", checkOK, "", time.Minute))
	e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
	e.osTick()
	x := e.osStep(id)
	e.hl.set(id, 2, 1, 1)
	e.hl.setPending(id, "inb_de1_1")
	e.hl.setRounds(id, e.round("inb_de1_0", checkOK, "", 0), e.round("inb_de1_1", checkFail, "refused", 0))
	e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
	e.osTick()
	e.osTick()
	e.clk.Advance(9 * time.Minute)
	e.osTick()
	x = e.wantOsStep(id, store.OsStepFailed, "probe_failed")
	want := []FailedProfile{{ID: "inb_de1_1", Name: "Backup", Code: "refused"}}
	if got := failedList(t, x.Params); !slices.Equal(got, want) {
		t.Fatalf("step names %+v, want %+v", got, want)
	}
	if got := failedList(t, e.wantEvent(id, "os_step_failed").Params); !slices.Equal(got, want) {
		t.Fatalf("event names %+v, want %+v", got, want)
	}
	if got := failedList(t, e.wantOsRollout(store.RolloutPaused, "gate_failed").PauseParams); !slices.Equal(got, want) {
		t.Fatalf("pause names %+v, want %+v", got, want)
	}
}

// The keys and the shape of the gate's profile params are read elsewhere: the MCP views use the exported constants, the admin
// (web/src/lib/updates.ts) has its own copy of the words. testdata/gate_params.json is what both are held to; the web test
// (web/src/lib/updates.test.ts) builds the params from it and expects the screens to name the profiles.
func TestGateParamKeysArePinnedForTheWeb(t *testing.T) {
	var fields []string
	for i := range reflect.TypeFor[FailedProfile]().NumField() {
		fields = append(fields, strings.Split(reflect.TypeFor[FailedProfile]().Field(i).Tag.Get("json"), ",")[0])
	}
	want := map[string]any{
		"failed_profiles": KeyFailedProfiles, "failed_more": KeyFailedMore, "skipped_profiles": KeySkippedProfiles, "skipped_more": KeySkippedMore,
		"profile_fields": fields, "shown": GateShown,
	}
	raw, err := os.ReadFile("testdata/gate_params.json")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	wantJSON, _ := json.Marshal(want)
	var wantNorm map[string]any
	_ = json.Unmarshal(wantJSON, &wantNorm)
	if !reflect.DeepEqual(got, wantNorm) {
		t.Fatalf("testdata/gate_params.json = %v, the code says %v: a key or the shape of a gate param changed, so the readers (internal/panel/mcp/views.go, web/src/lib/updates.ts) must change with it", got, wantNorm)
	}
}
