//go:build !js

package mcp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	tasksv1 "github.com/mistgate/mistgate/gen/mistgate/tasks/v1"
	"github.com/mistgate/mistgate/gen/mistgate/tasks/v1/tasksv1connect"
)

// fakeTasks is the TasksService of the test world: one check task, the tools a check may run, and what was asked of it.
type fakeTasks struct {
	tasksv1connect.UnimplementedTasksServiceHandler
	mu      sync.Mutex
	tasks   map[string]*tasksv1.Task
	created []*tasksv1.TaskInput
	updated []*tasksv1.UpdateTaskRequest
	deleted []string
	runs    []string
	tooSoon bool
}

func newFakeTasks() *fakeTasks {
	return &fakeTasks{tasks: map[string]*tasksv1.Task{
		"tsk_morning": {Id: "tsk_morning", Name: "Morning check", Kind: tasksv1.TaskKind_TASK_KIND_CHECK, Enabled: true, UpdatedUnix: 1700000000, Version: 1,
			Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY, MinuteOfDay: 450}, NextRunUnix: 1700050000, Problem: true,
			ProblemSummary: "fleet_digest: totals.nodes_problem_now = 2",
			Steps:          []*tasksv1.CheckStep{{Tool: "fleet_digest", ArgsJson: `{"hours":24}`, Conditions: []*tasksv1.Condition{{Path: "totals.nodes_problem_now", Op: "gt", Value: "0"}}}},
			LastRun:        &tasksv1.TaskRun{Id: "run_1", Trigger: "schedule", Status: tasksv1.RunStatus_RUN_STATUS_PROBLEM, Summary: "fleet_digest: totals.nodes_problem_now = 2", StartedUnix: 1700000100}},
	}}
}

func (f *fakeTasks) ListTasks(context.Context, *connect.Request[tasksv1.ListTasksRequest]) (*connect.Response[tasksv1.ListTasksResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := &tasksv1.ListTasksResponse{TimezoneOffsetMinutes: 180, NowUnix: 1700000200, MaxTasks: 50}
	for _, t := range f.tasks {
		resp.Tasks = append(resp.Tasks, t)
	}
	return connect.NewResponse(resp), nil
}

func (f *fakeTasks) GetTask(_ context.Context, r *connect.Request[tasksv1.GetTaskRequest]) (*connect.Response[tasksv1.GetTaskResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[r.Msg.Id]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("task_not_found"))
	}
	return connect.NewResponse(&tasksv1.GetTaskResponse{Task: t, TimezoneOffsetMinutes: 180}), nil
}

func (f *fakeTasks) ListTaskRuns(_ context.Context, r *connect.Request[tasksv1.ListTaskRunsRequest]) (*connect.Response[tasksv1.ListTaskRunsResponse], error) {
	return connect.NewResponse(&tasksv1.ListTaskRunsResponse{Runs: []*tasksv1.TaskRun{{Id: "run_1", TaskId: r.Msg.TaskId, Trigger: "missed", Status: tasksv1.RunStatus_RUN_STATUS_FAILED,
		Reason: "tool_failed", Summary: "path_probe: agent too old", StartedUnix: 1700000100, FinishedUnix: 1700000130,
		Steps: []*tasksv1.StepResult{{Tool: "path_probe", Error: "agent too old", DurationMs: 12}}}}}), nil
}

func (f *fakeTasks) RunTaskNow(_ context.Context, r *connect.Request[tasksv1.RunTaskNowRequest]) (*connect.Response[tasksv1.RunTaskNowResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.tooSoon {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("too_soon"))
	}
	f.runs = append(f.runs, r.Msg.Id)
	return connect.NewResponse(&tasksv1.RunTaskNowResponse{RunId: "run_now"}), nil
}

func (f *fakeTasks) ListCheckTools(context.Context, *connect.Request[tasksv1.ListCheckToolsRequest]) (*connect.Response[tasksv1.ListCheckToolsResponse], error) {
	return connect.NewResponse(&tasksv1.ListCheckToolsResponse{Tools: []*tasksv1.CheckTool{
		{Name: "fleet_digest", InputSchemaJson: `{"type":"object","properties":{"hours":{"type":"integer"}},"additionalProperties":false}`},
		{Name: "block_diagnosis", InputSchemaJson: `{"type":"object","properties":{"node":{"type":"string"},"country":{"type":"string"}},"additionalProperties":false}`},
	}}), nil
}

func (f *fakeTasks) CreateTask(_ context.Context, r *connect.Request[tasksv1.CreateTaskRequest]) (*connect.Response[tasksv1.CreateTaskResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, r.Msg.Task)
	return connect.NewResponse(&tasksv1.CreateTaskResponse{Task: &tasksv1.Task{Id: "tsk_new", Name: r.Msg.Task.Name, NextRunUnix: 1700090000}}), nil
}

func (f *fakeTasks) UpdateTask(_ context.Context, r *connect.Request[tasksv1.UpdateTaskRequest]) (*connect.Response[tasksv1.UpdateTaskResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, r.Msg)
	return connect.NewResponse(&tasksv1.UpdateTaskResponse{Task: f.tasks[r.Msg.Id]}), nil
}

func (f *fakeTasks) DeleteTask(_ context.Context, r *connect.Request[tasksv1.DeleteTaskRequest]) (*connect.Response[tasksv1.DeleteTaskResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, r.Msg.Id)
	return connect.NewResponse(&tasksv1.DeleteTaskResponse{}), nil
}

func TestTaskToolsPerProfile(t *testing.T) {
	e := newTestEnv(t)
	names := map[Profile][]string{}
	for _, p := range []Profile{ProfileReadonly, ProfileOperator, ProfileAdmin} {
		_, secret := e.token(p)
		names[p] = toolNames(t, e.session(secret))
	}
	reads := []string{"tasks_list", "task_get", "task_runs", "task_run_now"}
	plans := []string{"task_create_plan", "task_create_apply", "task_update_plan", "task_update_apply", "task_delete_plan", "task_delete_apply"}
	for _, n := range append(reads, plans...) {
		if slices.Contains(names[ProfileReadonly], n) {
			t.Errorf("readonly sees %s", n)
		}
		if !slices.Contains(names[ProfileAdmin], n) {
			t.Errorf("admin lacks %s", n)
		}
	}
	for _, n := range reads {
		if !slices.Contains(names[ProfileOperator], n) {
			t.Errorf("operator lacks %s", n)
		}
	}
	for _, n := range plans {
		if slices.Contains(names[ProfileOperator], n) {
			t.Errorf("operator sees %s", n)
		}
	}
}

func TestTaskReadsAndRunNow(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	s := e.session(secret)
	list := decode[TasksV](t, mustOK(t, s, "tasks_list", map[string]any{}))
	if list.TimezoneOffsetMinutes != 180 || len(list.Tasks) != 1 {
		t.Fatalf("list: %+v", list)
	}
	task := list.Tasks[0]
	if task.Schedule != "daily 07:30" || !task.Problem || task.LastRun == nil || task.LastRun.Status != "problem" || task.Steps != nil {
		t.Fatalf("task: %+v", task)
	}
	got := decode[TaskGetV](t, mustOK(t, s, "task_get", map[string]any{"task_id": "tsk_morning"}))
	if len(got.Task.Steps) != 1 || got.Task.Steps[0].Args["hours"] != float64(24) || got.Task.Steps[0].Conditions[0].Op != "gt" || len(got.Runs) != 1 || got.Runs[0].Trigger != "missed" {
		t.Fatalf("get: %+v", got)
	}
	runs := decode[TaskRunsV](t, mustOK(t, s, "task_runs", map[string]any{"task_id": "tsk_morning", "limit": 3}))
	if len(runs.Runs) != 1 || runs.Runs[0].Reason != "tool_failed" || runs.Runs[0].Steps[0].Error != "agent too old" {
		t.Fatalf("runs: %+v", runs)
	}
	mustFail(t, s, "task_runs", map[string]any{"task_id": "tsk_morning", "limit": 99})
	now := decode[taskRunNowV](t, mustOK(t, s, "task_run_now", map[string]any{"task_id": "tsk_morning"}))
	if now.RunID != "run_now" || len(e.w.tasks.runs) != 1 {
		t.Fatalf("run now: %+v %v", now, e.w.tasks.runs)
	}
	e.w.tasks.tooSoon = true
	if out := mustFail(t, s, "task_run_now", map[string]any{"task_id": "tsk_morning"}); !strings.Contains(out, "too_soon") {
		t.Fatalf("too soon: %s", out)
	}
}

// A task is created only through a plan the owner approved, checked in the plan by the rules the panel applies on save.
func TestTaskCreatePlanWaitsForTheOwner(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	refused := func(args map[string]any, want string) {
		t.Helper()
		if out := mustFail(t, s, "task_create_plan", args); !strings.Contains(out, want) {
			t.Errorf("%v: %s, want %s", args, out, want)
		}
	}
	check := func(steps any, schedule string) map[string]any {
		return map[string]any{"name": "Morning check", "kind": "check", "schedule": schedule, "steps": steps}
	}
	refused(check([]any{map[string]any{"tool": "fleet_digest"}}, "every 2m"), "schedule_too_often")
	refused(check([]any{map[string]any{"tool": "fleet_digest"}}, "hourly"), "schedule")
	refused(check([]any{map[string]any{"tool": "user_create_plan"}}, "daily 07:30"), "tool_not_allowed")
	refused(check([]any{map[string]any{"tool": "fleet_digest", "args": map[string]any{"hours": "a day"}}}, "daily 07:30"), "invalid_args")
	refused(check([]any{map[string]any{"tool": "fleet_digest", "conditions": []any{map[string]any{"path": "x", "op": "regex"}}}}, "daily 07:30"), "invalid_condition")
	refused(map[string]any{"name": "Ask", "kind": "assistant", "schedule": "every 10m", "prompt": "check"}, "schedule_too_often")
	refused(map[string]any{"name": "Ask", "kind": "assistant", "schedule": "daily 08:00", "prompt": "use sk-ant-api03-" + strings.Repeat("Zx9", 12)}, "secret_in_task")

	p := planOf(t, s, "task_create", map[string]any{"name": "Morning check", "kind": "check", "schedule": "daily 07:30", "notify_telegram": true, "reason": "the owner asked",
		"steps": []any{map[string]any{"tool": "fleet_digest", "args": map[string]any{"hours": 24}, "conditions": []any{map[string]any{"path": "totals.nodes_problem_now", "op": "gt", "value": "0"}}}}})
	if !p.NeedsApproval || !slices.Contains(p.Danger, dangerSchedule) {
		t.Fatalf("the plan does not wait for the owner: %+v", p)
	}
	facts := map[string]string{}
	for _, f := range p.Facts {
		facts[f.Key] = f.Value
	}
	if facts["schedule"] != "daily 07:30" || !strings.Contains(facts["step"], "fleet_digest") || !strings.Contains(facts["step"], "totals.nodes_problem_now gt 0") || facts["telegram"] != "yes" {
		t.Fatalf("facts: %v", facts)
	}
	if out := applyError(t, s, "task_create", p.ConfirmToken); !strings.Contains(out, "waiting for the owner") || len(e.w.tasks.created) != 0 {
		t.Fatalf("applied before the owner: %s", out)
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	out := applyOf(t, s, "task_create", p.ConfirmToken)
	if out.Status != "applied" || !strings.Contains(out.Result, "tsk_new") || len(e.w.tasks.created) != 1 {
		t.Fatalf("apply: %+v", out)
	}
	in := e.w.tasks.created[0]
	if in.Kind != tasksv1.TaskKind_TASK_KIND_CHECK || !in.Enabled || !in.NotifyTelegram || in.Schedule.Kind != tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY || in.Schedule.MinuteOfDay != 450 ||
		in.Steps[0].ArgsJson != `{"hours":24}` || in.Steps[0].Conditions[0].Path != "totals.nodes_problem_now" {
		t.Fatalf("created: %+v", in)
	}
	calls := e.w.calls(tasksv1connect.TasksServiceCreateTaskProcedure)
	if len(calls) != 1 || calls[0].Approved != p.PlanID {
		t.Fatalf("CreateTask without the owner's grant: %+v", calls)
	}

	// an assistant task, from the words of the person
	pa := planOf(t, s, "task_create", map[string]any{"name": "Morning look", "kind": "assistant", "schedule": "daily 08:00", "prompt": "Check the fleet every morning and tell me if something is wrong."})
	if !pa.NeedsApproval {
		t.Fatal("an assistant task plan does not wait for the owner")
	}
}

func TestTaskUpdateAndDeletePlans(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)
	p := planOf(t, s, "task_update", map[string]any{"task_id": "tsk_morning", "schedule": "every 30m", "enabled": false})
	if !p.NeedsApproval {
		t.Fatal("an update does not wait for the owner")
	}
	if err := e.plans.decide(p.PlanID, true); err != nil {
		t.Fatal(err)
	}
	if out := applyOf(t, s, "task_update", p.ConfirmToken); out.Status != "applied" || len(e.w.tasks.updated) != 1 {
		t.Fatalf("update: %+v", out)
	}
	up := e.w.tasks.updated[0]
	if up.Id != "tsk_morning" || up.Task.Enabled || up.Task.Schedule.EveryMinutes != 30 || up.Task.Name != "Morning check" || len(up.Task.Steps) != 1 {
		t.Fatalf("the update did not keep the other fields: %+v", up)
	}
	// changed in the panel after the plan: the apply refuses
	p = planOf(t, s, "task_update", map[string]any{"task_id": "tsk_morning", "name": "Evening check"})
	_ = e.plans.decide(p.PlanID, true)
	e.w.tasks.mu.Lock()
	e.w.tasks.tasks["tsk_morning"].Version++
	e.w.tasks.mu.Unlock()
	if out := applyError(t, s, "task_update", p.ConfirmToken); !strings.Contains(out, "changed since the plan") {
		t.Fatalf("stale update: %s", out)
	}
	mustFail(t, s, "task_update_plan", map[string]any{"task_id": "tsk_none", "name": "x"})

	d := planOf(t, s, "task_delete", map[string]any{"task_id": "tsk_morning"})
	_ = e.plans.decide(d.PlanID, true)
	if out := applyOf(t, s, "task_delete", d.ConfirmToken); out.Status != "applied" || len(e.w.tasks.deleted) != 1 || e.w.tasks.deleted[0] != "tsk_morning" {
		t.Fatalf("delete: %+v %v", out, e.w.tasks.deleted)
	}
}
