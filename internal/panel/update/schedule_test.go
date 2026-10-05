package update

import (
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestParseScheduleLocalTimeFixedOffset(t *testing.T) {
	got, err := parseScheduleLocalTime("2026-10-04T15:30", 180)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("parsed time = %s, want %s", got.UTC(), want)
	}
	for _, tc := range []struct {
		local  string
		offset int32
	}{
		{"2026-02-30T10:00", 180},
		{"2026-10-04 15:30", 180},
		{"2026-10-04T15:30", 901},
	} {
		if _, err := parseScheduleLocalTime(tc.local, tc.offset); err == nil {
			t.Errorf("parseScheduleLocalTime(%q, %d) unexpectedly succeeded", tc.local, tc.offset)
		}
	}
}

func scheduleLocalAfter(e *env, d time.Duration, offset int32) string {
	return e.clk.Now().Add(d).In(time.FixedZone("schedule", int(offset)*60)).Format("2006-01-02T15:04")
}

func TestScheduledNodeUpdateSurvivesRestartAndRunsOnlyForSelectedNode(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	selected := e.addNode("selected", nodeOpts{})
	other := e.addNode("other", nodeOpts{})
	local := scheduleLocalAfter(e, 2*time.Hour, defaultScheduleTimezoneOffsetMin)
	b := e.s.current()
	schedule, err := e.s.scheduleNodeUpdate(e.ctx, selected, local, defaultScheduleTimezoneOffsetMin, b.manifest.Version, b.manifest.Built)
	if err != nil {
		t.Fatal(err)
	}
	wantTime, _ := parseScheduleLocalTime(local, defaultScheduleTimezoneOffsetMin)
	if schedule.ScheduledAt != wantTime.Unix() || schedule.TimezoneOffsetMinutes != defaultScheduleTimezoneOffsetMin {
		t.Fatalf("saved fixed-offset schedule: %+v, want unix %d", schedule, wantTime.Unix())
	}
	read := func() *adminv1.GetUpdatesResponse {
		e.t.Helper()
		response, err := (rpc{e.s}).GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
		if err != nil {
			e.t.Fatal(err)
		}
		return response.Msg
	}
	assertScheduled := func() {
		e.t.Helper()
		resp := read()
		if resp.GetScheduleTimezoneOffsetMinutes() != defaultScheduleTimezoneOffsetMin {
			e.t.Fatalf("default schedule timezone = %d, want UTC", resp.GetScheduleTimezoneOffsetMinutes())
		}
		for _, node := range resp.GetNodes() {
			if node.GetNodeId() == selected {
				if node.GetScheduledUnix() != schedule.ScheduledAt || node.GetScheduledVersion() != b.manifest.Version || node.GetScheduledTimezoneOffsetMinutes() != defaultScheduleTimezoneOffsetMin {
					e.t.Fatalf("scheduled node view = %+v", node)
				}
				return
			}
		}
		e.t.Fatal("scheduled node missing from update view")
	}
	assertScheduled()
	e.restart()
	assertScheduled()
	e.clk.Advance(3 * time.Hour)
	e.s.processScheduledNodeUpdates(e.ctx)
	rollout, err := e.st.ActiveRollout(e.ctx)
	if err != nil {
		t.Fatalf("scheduled update did not start after restart: %v", err)
	}
	steps, err := e.st.RolloutSteps(e.ctx, rollout.ID)
	if err != nil || len(steps) != 1 || steps[0].NodeID != selected || steps[0].NodeID == other {
		t.Fatalf("scheduled rollout affected a different node: %+v, %v", steps, err)
	}
	if got, err := e.st.NodeUpdateSchedules(e.ctx); err != nil || len(got) != 0 {
		t.Fatalf("started schedule was not consumed: %+v, %v", got, err)
	}
}

func TestScheduledNodeUpdateWaitsForOnlineAndDoesNotFollowChangedBundle(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	id := e.addNode("offline", nodeOpts{offline: true})
	b := e.s.current()
	local := scheduleLocalAfter(e, 2*time.Minute, defaultScheduleTimezoneOffsetMin)
	if _, err := e.s.scheduleNodeUpdate(e.ctx, id, local, defaultScheduleTimezoneOffsetMin, b.manifest.Version, b.manifest.Built); err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(3 * time.Minute)
	e.s.processScheduledNodeUpdates(e.ctx)
	if _, err := e.st.ActiveRollout(e.ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("offline node started its scheduled update: %v", err)
	}
	if got, err := e.st.NodeUpdateSchedules(e.ctx); err != nil || len(got) != 1 {
		t.Fatalf("offline schedule was lost: %+v, %v", got, err)
	}

	// A newer trusted release arriving while the node is offline must never be substituted for the approved build.
	e.bundle("0.3.0-new", newBuilt+500, map[string][]byte{"mistgate-node-linux-amd64": []byte("newer")})
	e.fl.mu.Lock()
	e.fl.live[id] = true
	e.fl.mu.Unlock()
	e.s.processScheduledNodeUpdates(e.ctx)
	if _, err := e.st.ActiveRollout(e.ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("scheduler silently used a replacement bundle: %v", err)
	}
	if got, err := e.st.NodeUpdateSchedules(e.ctx); err != nil || len(got) != 1 || got[id].ToVersion != "0.2.0-new" {
		t.Fatalf("old release schedule should remain visible for review: %+v, %v", got, err)
	}
}

func TestScheduledNodeUpdateIsConsumedOnceAndManualUpdateClearsItsSchedule(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	id := e.addNode("node", nodeOpts{})
	b := e.s.current()
	local := scheduleLocalAfter(e, 2*time.Hour, defaultScheduleTimezoneOffsetMin)
	if _, err := e.s.scheduleNodeUpdate(e.ctx, id, local, defaultScheduleTimezoneOffsetMin, b.manifest.Version, b.manifest.Built); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.start(e.ctx, []string{id}, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := e.st.NodeUpdateSchedules(e.ctx); err != nil || len(got) != 0 {
		t.Fatalf("manual update did not atomically clear its schedule: %+v, %v", got, err)
	}
	if steps, err := e.st.RolloutSteps(e.ctx, e.rollout().ID); err != nil || len(steps) != 1 || steps[0].NodeID != id {
		t.Fatalf("manual single-node update: %+v, %v", steps, err)
	}
}

func TestScheduleNodeUpdateRejectsChangedTimezoneAndRelease(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	id := e.addNode("node", nodeOpts{})
	b := e.s.current()
	local := scheduleLocalAfter(e, 6*time.Hour, defaultScheduleTimezoneOffsetMin)
	if err := e.st.SetSettings(e.ctx, map[string]string{updateScheduleTimezoneKey: "240"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.scheduleNodeUpdate(e.ctx, id, local, defaultScheduleTimezoneOffsetMin, b.manifest.Version, b.manifest.Built); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("changed timezone: %v", err)
	}
	if _, err := e.s.scheduleNodeUpdate(e.ctx, id, local, 240, "old-version", b.manifest.Built); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("changed release: %v", err)
	}
}

// A schedule cannot outlive its signed bundle: the agent would refuse an expired manifest at that time anyway.
func TestScheduleNodeUpdateRejectsATimeAfterTheBundleExpires(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle() // expires in 30 days
	id := e.addNode("node", nodeOpts{})
	b := e.s.current()
	late := scheduleLocalAfter(e, 31*24*time.Hour, defaultScheduleTimezoneOffsetMin)
	if _, err := e.s.scheduleNodeUpdate(e.ctx, id, late, defaultScheduleTimezoneOffsetMin, b.manifest.Version, b.manifest.Built); connect.CodeOf(err) != connect.CodeInvalidArgument ||
		!strings.Contains(err.Error(), "expires") {
		t.Fatalf("a schedule after the bundle expires: %v", err)
	}
	ok := scheduleLocalAfter(e, 29*24*time.Hour, defaultScheduleTimezoneOffsetMin)
	if _, err := e.s.scheduleNodeUpdate(e.ctx, id, ok, defaultScheduleTimezoneOffsetMin, b.manifest.Version, b.manifest.Built); err != nil {
		t.Fatalf("a schedule before the bundle expires: %v", err)
	}
}

// A schedule that could not start near its time (the node was offline, another update was running) is marked missed
// instead of starting at any hour later; scheduling again clears the mark.
func TestScheduledNodeUpdateIsMarkedMissedAfterItsWindow(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	id := e.addNode("offline", nodeOpts{offline: true})
	b := e.s.current()
	local := scheduleLocalAfter(e, 2*time.Minute, defaultScheduleTimezoneOffsetMin)
	if _, err := e.s.scheduleNodeUpdate(e.ctx, id, local, defaultScheduleTimezoneOffsetMin, b.manifest.Version, b.manifest.Built); err != nil {
		t.Fatal(err)
	}
	missed := func() bool {
		t.Helper()
		resp, err := (rpc{e.s}).GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range resp.Msg.GetNodes() {
			if n.GetNodeId() == id {
				if n.GetScheduledUnix() == 0 {
					t.Fatal("the schedule is gone")
				}
				return n.GetScheduledMissed()
			}
		}
		t.Fatal("node missing")
		return false
	}
	e.clk.Advance(scheduleMissAfter) // still inside the window: it waits for the node
	e.s.processScheduledNodeUpdates(e.ctx)
	if missed() {
		t.Fatal("marked missed inside its window")
	}
	e.clk.Advance(5 * time.Minute)
	e.s.processScheduledNodeUpdates(e.ctx)
	if !missed() {
		t.Fatal("not marked missed after its window")
	}
	e.fl.mu.Lock()
	e.fl.live[id] = true // the node comes back hours late: nothing starts by itself
	e.fl.mu.Unlock()
	e.s.processScheduledNodeUpdates(e.ctx)
	if _, err := e.st.ActiveRollout(e.ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a missed schedule started: %v", err)
	}
	again := scheduleLocalAfter(e, time.Hour, defaultScheduleTimezoneOffsetMin)
	if _, err := e.s.scheduleNodeUpdate(e.ctx, id, again, defaultScheduleTimezoneOffsetMin, b.manifest.Version, b.manifest.Built); err != nil {
		t.Fatal(err)
	}
	if missed() {
		t.Fatal("scheduling again kept the missed mark")
	}
	e.clk.Advance(time.Hour)
	e.s.processScheduledNodeUpdates(e.ctx)
	if _, err := e.st.ActiveRollout(e.ctx); err != nil {
		t.Fatalf("the new schedule did not start: %v", err)
	}
}

func TestUpdateTimezoneDefaultsToUTC(t *testing.T) {
	e := newEnv(t)
	if got, err := e.s.scheduleTimezoneOffset(e.ctx); err != nil || got != 0 {
		t.Fatalf("default offset of a new installation = %d, %v", got, err)
	}
}
