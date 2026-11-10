package spec

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseScheduleRoundTrips(t *testing.T) {
	for in, want := range map[string]Schedule{
		"every 15m":          {Kind: Every, EveryMinutes: 15},
		"Every 2 hours":      {Kind: Every, EveryMinutes: 120},
		"every 1d":           {Kind: Every, EveryMinutes: 1440},
		"daily 07:30":        {Kind: Daily, MinuteOfDay: 450},
		"weekly mon 09:05":   {Kind: Weekly, Weekday: 1, MinuteOfDay: 545},
		"weekly sunday 0:00": {Kind: Weekly, Weekday: 0, MinuteOfDay: 0},
	} {
		got, err := ParseSchedule(in)
		if err != nil || got != want {
			t.Errorf("%q: %+v %v, want %+v", in, got, err, want)
			continue
		}
		again, err := ParseSchedule(got.String())
		if err != nil || again != got {
			t.Errorf("%q does not read back: %q -> %+v %v", in, got.String(), again, err)
		}
	}
	for _, bad := range []string{"", "every", "every 0m", "every 8d", "daily 24:00", "daily 7", "weekly xyz 07:00", "hourly", "every 15s"} {
		if _, err := ParseSchedule(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestScheduleMinimumsPerKind(t *testing.T) {
	every := func(n int) Schedule { return Schedule{Kind: Every, EveryMinutes: n} }
	if err := every(5).Validate(KindCheck); err != nil {
		t.Errorf("a check every 5 minutes: %v", err)
	}
	var e *Error
	if err := every(4).Validate(KindCheck); !errors.As(err, &e) || e.Error() != "schedule_too_often: min=5" {
		t.Errorf("a check every 4 minutes: %v", err)
	}
	if err := every(10).Validate(KindAssistant); !errors.As(err, &e) || e.Error() != "schedule_too_often: min=15" {
		t.Errorf("an assistant task every 10 minutes: %v", err)
	}
	if err := every(15).Validate(KindAssistant); err != nil {
		t.Errorf("an assistant task every 15 minutes: %v", err)
	}
	for _, s := range []Schedule{{Kind: Daily, MinuteOfDay: 1440}, {Kind: Weekly, Weekday: 7}, {Kind: "hourly"}, every(MaxEvery + 1)} {
		if s.Validate(KindCheck) == nil {
			t.Errorf("%+v accepted", s)
		}
	}
}

func TestNextRunsOnTheWallClockOfThePanelZone(t *testing.T) {
	// 10:07:30 UTC is 13:07:30 at UTC+3
	at := time.Date(2026, 11, 7, 10, 7, 30, 0, time.UTC)
	const msk = 180
	cases := []struct {
		s      Schedule
		offset int
		want   time.Time
	}{
		{Schedule{Kind: Every, EveryMinutes: 15}, msk, time.Date(2026, 11, 7, 10, 15, 0, 0, time.UTC)},
		{Schedule{Kind: Every, EveryMinutes: 120}, msk, time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)}, // 14:00 local, an even hour
		{Schedule{Kind: Every, EveryMinutes: 120}, 0, time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)},
		{Schedule{Kind: Daily, MinuteOfDay: 7*60 + 30}, msk, time.Date(2026, 11, 8, 4, 30, 0, 0, time.UTC)}, // 07:30 tomorrow, local
		{Schedule{Kind: Daily, MinuteOfDay: 14 * 60}, msk, time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC)},   // 14:00 today
		// 2026-11-07 is a Saturday: the next Monday 09:00 at UTC+3
		{Schedule{Kind: Weekly, Weekday: 1, MinuteOfDay: 9 * 60}, msk, time.Date(2026, 11, 9, 6, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := c.s.Next(at, c.offset, 0); !got.Equal(c.want) {
			t.Errorf("%s at UTC%+d: %s, want %s", c.s, c.offset/60, got, c.want)
		}
	}
}

func TestNextWithJitterNeverRepeatsASlotAndNeverCatchesUp(t *testing.T) {
	s := Schedule{Kind: Every, EveryMinutes: 15}
	jitter := 40 * time.Second
	slot := time.Date(2026, 11, 7, 10, 15, 0, 0, time.UTC)
	// before the jittered start of the slot, the slot is next
	if got := s.Next(slot.Add(10*time.Second), 0, jitter); !got.Equal(slot.Add(jitter)) {
		t.Errorf("inside the jitter window: %s", got)
	}
	// a run that started at slot+jitter and ended 30 s later goes to the next slot, not the same one
	if got := s.Next(slot.Add(jitter+30*time.Second), 0, jitter); !got.Equal(slot.Add(15*time.Minute + jitter)) {
		t.Errorf("after the run: %s", got)
	}
	// a panel down for three hours runs once, at the next slot from now
	later := slot.Add(3*time.Hour + 2*time.Minute)
	if got := s.Next(later, 0, jitter); !got.Equal(slot.Add(3*time.Hour + 15*time.Minute + jitter)) {
		t.Errorf("after a long stop: %s", got)
	}
}

func TestJitterIsStableAndBounded(t *testing.T) {
	five := Schedule{Kind: Every, EveryMinutes: 5}
	daily := Schedule{Kind: Daily}
	seen := map[time.Duration]bool{}
	for _, id := range []string{"tsk_a", "tsk_b", "tsk_c", "tsk_d", "tsk_e", "tsk_f"} {
		if Jitter(id, five) != Jitter(id, five) {
			t.Fatal("jitter changes between calls")
		}
		if j := Jitter(id, five); j < 0 || j >= 30*time.Second {
			t.Errorf("every 5 minutes: %s", j)
		}
		j := Jitter(id, daily)
		if j < 0 || j >= MaxJitter {
			t.Errorf("daily: %s", j)
		}
		seen[j] = true
	}
	if len(seen) < 3 {
		t.Errorf("tasks start together: %v", seen)
	}
}

func TestConditions(t *testing.T) {
	result := []byte(`{
		"totals": {"nodes_problem_now": 2, "alerts_critical": 0},
		"nodes": [{"name": "de1", "status": "online"}, {"name": "nl1", "status": "down"}, {"name": "fi1", "status": "updating"}],
		"findings": [{"code": "x", "severity": "info"}, {"code": "y", "severity": "warning", "detail": {"severity": "error"}}],
		"empty": [],
		"loss_pct": "12.5"
	}`)
	cases := []struct {
		c    Condition
		want []string
	}{
		{Condition{Path: "totals.nodes_problem_now", Op: OpGt, Value: "0"}, []string{"totals.nodes_problem_now = 2"}},
		{Condition{Path: "totals.alerts_critical", Op: OpGt, Value: "0"}, nil},
		{Condition{Path: "nodes[].status", Op: OpNotIn, Value: "online, updating"}, []string{"nodes[1].status = down"}},
		{Condition{Path: "**.severity", Op: OpSeverityAtLeast, Value: "warning"}, []string{"findings[1].severity = warning", "findings[1].detail.severity = error"}},
		{Condition{Path: "**.severity", Op: OpSeverityAtLeast, Value: "critical"}, []string{"findings[1].detail.severity = error"}},
		{Condition{Path: "empty", Op: OpNonempty}, nil},
		{Condition{Path: "findings", Op: OpNonempty}, []string{"findings = 2 items"}},
		{Condition{Path: "loss_pct", Op: OpGte, Value: "10"}, []string{"loss_pct = 12.5"}},
		{Condition{Path: "nodes[].name", Op: OpEq, Value: "NL1"}, []string{"nodes[1].name = nl1"}},
		{Condition{Path: "nodes[].name", Op: OpContains, Value: "1"}, []string{"nodes[0].name = de1", "nodes[1].name = nl1", "nodes[2].name = fi1"}},
		{Condition{Path: "missing.field", Op: OpNe, Value: "x"}, nil},
	}
	for _, c := range cases {
		if err := c.c.Validate(); err != nil {
			t.Errorf("%+v: %v", c.c, err)
			continue
		}
		got, err := c.c.Match(result, 5)
		if err != nil {
			t.Errorf("%+v: unexpected match error: %v", c.c, err)
			continue
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%+v: %q, want %q", c.c, got, c.want)
		}
	}
	if _, err := (Condition{Op: OpNonempty}).Match([]byte("not json"), 5); err == nil || err.Error() != "tool result is not valid JSON" {
		t.Errorf("invalid JSON error = %v", err)
	}
	for _, bad := range []Condition{
		{Path: "a", Op: "like"}, {Path: "a", Op: OpGt, Value: "many"}, {Path: "**", Op: OpNonempty}, {Path: "a..b", Op: OpNonempty},
		{Path: "a b", Op: OpNonempty}, {Op: OpSeverityAtLeast, Value: "loud"}, {Op: OpIn}, {Op: OpContains},
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v accepted", bad)
		}
	}
}

func TestStepsAreCheckedAgainstTheToolsAndTheirSchemas(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"hours":{"type":"integer"},"node":{"type":"string"}},"additionalProperties":false}`)
	allowed := func(name string) bool { return name == "fleet_digest" }
	schemaOf := func(string) json.RawMessage { return schema }
	steps, err := Steps([]Step{{Tool: "fleet_digest", Args: json.RawMessage(` { "hours" : 24 } `), Conditions: []Condition{{Path: "totals.nodes_problem_now", Op: OpGt, Value: "0"}}}}, allowed, schemaOf)
	if err != nil || string(steps[0].Args) != `{"hours":24}` {
		t.Fatalf("%+v %v", steps, err)
	}
	if steps, err := Steps([]Step{{Tool: "fleet_digest"}}, allowed, schemaOf); err != nil || string(steps[0].Args) != "{}" {
		t.Fatalf("no arguments: %+v %v", steps, err)
	}
	code := func(err error) string {
		var e *Error
		if errors.As(err, &e) {
			return e.Code
		}
		return ""
	}
	for want, steps := range map[string][]Step{
		"tool_not_allowed":  {{Tool: "user_create_plan"}},
		"invalid_args":      {{Tool: "fleet_digest", Args: json.RawMessage(`{"hours":"a day"}`)}},
		"invalid_condition": {{Tool: "fleet_digest", Conditions: []Condition{{Op: "regex"}}}},
		"too_many_steps":    make([]Step, MaxSteps+1),
	} {
		if _, err := Steps(steps, allowed, schemaOf); code(err) != want {
			t.Errorf("%s: %v", want, err)
		}
	}
	if _, err := Steps([]Step{{Tool: "fleet_digest", Args: json.RawMessage(`[1]`)}}, allowed, schemaOf); code(err) != "invalid_args" {
		t.Errorf("a list as arguments: %v", err)
	}
	if _, err := Steps([]Step{{Tool: "fleet_digest", Args: json.RawMessage(`{"a":1}{"b":2}`)}}, allowed, func(string) json.RawMessage { return nil }); code(err) != "invalid_args" {
		t.Errorf("two objects: %v", err)
	}
}

func TestNameAndPrompt(t *testing.T) {
	if Name("Morning check") != nil || Name("") == nil || Name(strings.Repeat("x", MaxNameRunes+1)) == nil || Name("a\nb") == nil {
		t.Error("name rules")
	}
	if Prompt("Check the fleet") != nil || Prompt("  ") == nil || Prompt(strings.Repeat("я", MaxPromptRunes+1)) == nil {
		t.Error("prompt rules")
	}
}
