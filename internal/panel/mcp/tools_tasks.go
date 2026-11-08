//go:build !js

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	tasksv1 "github.com/mistgate/mistgate/gen/mistgate/tasks/v1"
	"github.com/mistgate/mistgate/gen/mistgate/tasks/v1/tasksv1connect"
	"github.com/mistgate/mistgate/internal/panel/tasks/spec"
)

// The owner's scheduled tasks (mistgate.tasks.v1, ADR 0017 section 5): tasks_list, task_get, task_runs and task_run_now for the
// operator profile; creating, changing and deleting a task are plans that always wait for the owner (task_create, task_update,
// task_delete; admin profile), because a task makes the panel run tools or the assistant on its own, again and again. The
// definition is checked in the plan by the same rules (internal/panel/tasks/spec) the TasksService applies when it is saved.
// Not on the Cloudflare edition.

const dangerSchedule = "schedule" // it makes the panel run tools or the assistant on its own, on a schedule

const taskKinds = "A check task runs MCP tools of the operator profile (reads and probes such as fleet_digest, checks_run_now, block_diagnosis, path_probe) on a schedule, no model involved; " +
	"each step may carry conditions on the tool's JSON result, and a condition that holds means the run found a problem. An assistant task gives a prompt to the panel's assistant on a schedule; " +
	"it runs with the operator profile (it reads, runs probes and may prepare plans, never applies one), writes its answer into a conversation of the task's own and ends with a verdict. " +
	"A problem opens the alert scheduled_check (it closes when a later run is clean); three failed runs in a row open scheduled_task_failing; notify_telegram sends both to Telegram too. "

const taskScheduleWords = "schedule is \"every 15m\" (m, h or d), \"daily 07:30\" or \"weekly mon 07:30\", in the panel's time zone (tasks_list says it); a check task runs at most every 5 minutes, an assistant task every 15. " +
	"Each run starts up to a few minutes after its time; a time the panel missed while down runs once when it starts. "

const taskConditionWords = "A condition is {path, op, value}: path is dotted names into the result, name[] walks a list, **.name finds a name at any depth (\"totals.nodes_problem_now\", \"nodes[].status\", \"**.severity\"); " +
	"op is severity_at_least (info, warning, error, critical), gt, gte, lt, lte (a number), eq, ne, in, not_in (comma-separated), nonempty or contains. "

func taskTools() []toolDef {
	out := []toolDef{
		readTool("tasks_list", ProfileOperator, procs(tasksv1connect.TasksServiceListTasksProcedure),
			"The owner's scheduled tasks: per task its id, name, kind (check or assistant), whether it is enabled, the schedule, the next run, whether a run is going on, "+
				"the last run (status ok, problem, failed, skipped or running, with its reason code and one-line summary), problem and its summary (the scheduled_check alert is open) and fail_streak (three opens scheduled_task_failing). "+
				"timezone_offset_minutes is the panel's time zone. "+taskKinds,
			func(c *call, _ noArgs) (any, error) { return c.tasksList() }),
		readTool("task_get", ProfileOperator, procs(tasksv1connect.TasksServiceGetTaskProcedure, tasksv1connect.TasksServiceListTaskRunsProcedure),
			"One scheduled task in full: a check task's steps (tool, args, conditions, error_is_problem), an assistant task's prompt and its conversation id, notify_telegram, and its last 5 runs. "+
				"Read the runs further back with task_runs.",
			func(c *call, in taskIDArg) (any, error) { return c.taskGet(in) }),
		readTool("task_runs", ProfileOperator, procs(tasksv1connect.TasksServiceListTaskRunsProcedure),
			"The runs of one scheduled task, newest first (limit 1 to 50, default 10; the panel keeps the last 50, at most 30 days): trigger (schedule, manual, or missed when the panel was down at the time), "+
				"status (ok, problem, failed, skipped, running), reason (a code such as tool_failed, provider_unreachable, budget_reached, no_verdict, interrupted), summary, "+
				"for a check the steps (tool, ok, problem, error, the matches of its conditions and the start of the result), for an assistant task the conversation, provider, model, cost and whether it fell back to the fallback provider.",
			func(c *call, in taskRunsArg) (any, error) { return c.taskRuns(in) }),
		probeTool("task_run_now", readTimeout, procs(tasksv1connect.TasksServiceRunTaskNowProcedure),
			"Start a run of a scheduled task now, outside its schedule, and return its run_id at once; read the outcome with task_runs (a check takes seconds to a minute or two, an assistant task a few minutes). "+
				"One run of a task at a time (task_running), and a manual run at most once a minute for a check task and once in five minutes for an assistant task (too_soon). "+
				"An assistant task's run is paid from the assistant's monthly budget.",
			func(c *call, in taskIDArg) (any, error) { return c.taskRunNow(in) }),
	}
	out = append(out, change(taskCreate)...)
	out = append(out, change(taskUpdate)...)
	return append(out, change(taskDelete)...)
}

type taskIDArg struct {
	TaskID string `json:"task_id" jsonschema:"the task id from tasks_list"`
}

type taskRunsArg struct {
	TaskID string `json:"task_id" jsonschema:"the task id from tasks_list"`
	Limit  int    `json:"limit,omitempty" jsonschema:"1 to 50; default 10"`
}

// TaskV is one task as the tools show it.
type TaskV struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	Enabled        bool       `json:"enabled"`
	Schedule       string     `json:"schedule"`
	NextRunUnix    int64      `json:"next_run_unix,omitempty"`
	Running        bool       `json:"running,omitempty"`
	Problem        bool       `json:"problem,omitempty"`
	ProblemSummary string     `json:"problem_summary,omitempty"`
	FailStreak     uint32     `json:"fail_streak,omitempty"`
	NotifyTelegram bool       `json:"notify_telegram"`
	Steps          []taskStep `json:"steps,omitempty"`
	Prompt         string     `json:"prompt,omitempty"`
	ConversationID string     `json:"conversation_id,omitempty"`
	LastRun        *TaskRunV  `json:"last_run,omitempty"`
}

// TaskRunV is one run as the tools show it.
type TaskRunV struct {
	ID             string      `json:"id"`
	Trigger        string      `json:"trigger"`
	Status         string      `json:"status"`
	Reason         string      `json:"reason,omitempty"`
	Summary        string      `json:"summary,omitempty"`
	StartedUnix    int64       `json:"started_unix"`
	FinishedUnix   int64       `json:"finished_unix,omitempty"`
	Actor          string      `json:"started_by,omitempty"`
	Steps          []taskStepV `json:"steps,omitempty"`
	ConversationID string      `json:"conversation_id,omitempty"`
	Provider       string      `json:"provider,omitempty"`
	Model          string      `json:"model,omitempty"`
	USD            float64     `json:"usd,omitempty"`
	FellBack       bool        `json:"fell_back,omitempty"`
}

type taskStepV struct {
	Tool       string   `json:"tool"`
	OK         bool     `json:"ok"`
	Problem    bool     `json:"problem,omitempty"`
	Error      string   `json:"error,omitempty"`
	Matches    []string `json:"matches,omitempty"`
	Excerpt    string   `json:"excerpt,omitempty"`
	DurationMs uint32   `json:"duration_ms"`
}

// TasksV is the tasks_list result.
type TasksV struct {
	TimezoneOffsetMinutes int32   `json:"timezone_offset_minutes"`
	NowUnix               int64   `json:"now_unix"`
	Tasks                 []TaskV `json:"tasks"`
	Truncated             bool    `json:"truncated,omitempty"`
}

func (v *TasksV) shrink() bool {
	v.Truncated = true
	return cut(&v.Tasks)
}

// TaskGetV is the task_get result.
type TaskGetV struct {
	TimezoneOffsetMinutes int32      `json:"timezone_offset_minutes"`
	Task                  TaskV      `json:"task"`
	Runs                  []TaskRunV `json:"runs"`
	Truncated             bool       `json:"truncated,omitempty"`
}

func (v *TaskGetV) shrink() bool {
	v.Truncated = true
	return cut(&v.Runs)
}

// TaskRunsV is the task_runs result.
type TaskRunsV struct {
	TaskID    string     `json:"task_id"`
	Runs      []TaskRunV `json:"runs"`
	Truncated bool       `json:"truncated,omitempty"`
}

func (v *TaskRunsV) shrink() bool {
	v.Truncated = true
	return cut(&v.Runs)
}

var runStatusWords = map[tasksv1.RunStatus]string{
	tasksv1.RunStatus_RUN_STATUS_RUNNING: "running", tasksv1.RunStatus_RUN_STATUS_OK: "ok", tasksv1.RunStatus_RUN_STATUS_PROBLEM: "problem",
	tasksv1.RunStatus_RUN_STATUS_FAILED: "failed", tasksv1.RunStatus_RUN_STATUS_SKIPPED: "skipped",
}

func taskKindWord(k tasksv1.TaskKind) string {
	if k == tasksv1.TaskKind_TASK_KIND_ASSISTANT {
		return spec.KindAssistant
	}
	return spec.KindCheck
}

func scheduleOfProto(p *tasksv1.Schedule) spec.Schedule {
	if p == nil {
		return spec.Schedule{}
	}
	sc := spec.Schedule{EveryMinutes: int(min(p.EveryMinutes, 1<<20)), MinuteOfDay: int(min(p.MinuteOfDay, 1<<20)), Weekday: int(min(p.Weekday, 1<<20))}
	switch p.Kind {
	case tasksv1.ScheduleKind_SCHEDULE_KIND_EVERY:
		sc.Kind = spec.Every
	case tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY:
		sc.Kind = spec.Daily
	case tasksv1.ScheduleKind_SCHEDULE_KIND_WEEKLY:
		sc.Kind = spec.Weekly
	}
	return sc
}

func scheduleProto(sc spec.Schedule) *tasksv1.Schedule {
	p := &tasksv1.Schedule{EveryMinutes: uint32(max(sc.EveryMinutes, 0)), MinuteOfDay: uint32(max(sc.MinuteOfDay, 0)), Weekday: uint32(max(sc.Weekday, 0))}
	switch sc.Kind {
	case spec.Every:
		p.Kind = tasksv1.ScheduleKind_SCHEDULE_KIND_EVERY
	case spec.Daily:
		p.Kind = tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY
	case spec.Weekly:
		p.Kind = tasksv1.ScheduleKind_SCHEDULE_KIND_WEEKLY
	}
	return p
}

func taskRunView(r *tasksv1.TaskRun) TaskRunV {
	v := TaskRunV{ID: clean(r.GetId(), 64), Trigger: clean(r.GetTrigger(), 16), Status: runStatusWords[r.GetStatus()], Reason: clean(r.GetReason(), 64),
		Summary: clean(r.GetSummary(), 300), StartedUnix: r.GetStartedUnix(), FinishedUnix: r.GetFinishedUnix(), Actor: clean(r.GetActor(), 64),
		ConversationID: clean(r.GetConversationId(), 64), Provider: clean(r.GetProvider(), 32), Model: clean(r.GetModel(), 80), USD: r.GetUsd(), FellBack: r.GetFellBack()}
	for _, st := range r.GetSteps() {
		sv := taskStepV{Tool: clean(st.GetTool(), 64), OK: st.GetOk(), Problem: st.GetProblem(), Error: clean(st.GetError(), 300), Excerpt: clean(st.GetExcerpt(), 600), DurationMs: st.GetDurationMs()}
		for _, m := range st.GetMatches() {
			sv.Matches = append(sv.Matches, clean(m, 160))
		}
		v.Steps = append(v.Steps, sv)
	}
	return v
}

func taskView(t *tasksv1.Task, full bool) TaskV {
	v := TaskV{ID: clean(t.GetId(), 64), Name: nm(t.GetName()), Kind: taskKindWord(t.GetKind()), Enabled: t.GetEnabled(), Schedule: scheduleOfProto(t.GetSchedule()).String(),
		NextRunUnix: t.GetNextRunUnix(), Running: t.GetRunning(), Problem: t.GetProblem(), ProblemSummary: clean(t.GetProblemSummary(), 300),
		FailStreak: t.GetFailStreak(), NotifyTelegram: t.GetNotifyTelegram()}
	if t.GetLastRun() != nil {
		lr := taskRunView(t.GetLastRun())
		lr.Steps = nil
		v.LastRun = &lr
	}
	if full {
		v.Prompt, v.ConversationID = clean(t.GetPrompt(), spec.MaxPromptRunes), clean(t.GetConversationId(), 64)
		for _, st := range t.GetSteps() {
			step := taskStep{Tool: clean(st.GetTool(), 64), ErrorIsProblem: st.GetErrorIsProblem()}
			if a := strings.TrimSpace(st.GetArgsJson()); a != "" && a != "{}" {
				_ = json.Unmarshal([]byte(a), &step.Args)
			}
			for _, cnd := range st.GetConditions() {
				step.Conditions = append(step.Conditions, taskCondition{Path: clean(cnd.GetPath(), 200), Op: clean(cnd.GetOp(), 32), Value: clean(cnd.GetValue(), 200)})
			}
			v.Steps = append(v.Steps, step)
		}
	}
	return v
}

func (c *call) tasksList() (any, error) {
	r, err := c.cl.Tasks.ListTasks(c.ctx, connect.NewRequest(&tasksv1.ListTasksRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	v := &TasksV{TimezoneOffsetMinutes: r.Msg.GetTimezoneOffsetMinutes(), NowUnix: r.Msg.GetNowUnix(), Tasks: []TaskV{}}
	for _, t := range r.Msg.GetTasks() {
		v.Tasks = append(v.Tasks, taskView(t, false))
	}
	return v, nil
}

func (c *call) taskGet(in taskIDArg) (any, error) {
	if err := validID("task_id", in.TaskID); err != nil {
		return nil, err
	}
	r, err := c.cl.Tasks.GetTask(c.ctx, connect.NewRequest(&tasksv1.GetTaskRequest{Id: in.TaskID}))
	if err != nil {
		return nil, apiError(err)
	}
	runs, err := c.cl.Tasks.ListTaskRuns(c.ctx, connect.NewRequest(&tasksv1.ListTaskRunsRequest{TaskId: in.TaskID, Limit: 5}))
	if err != nil {
		return nil, apiError(err)
	}
	v := &TaskGetV{TimezoneOffsetMinutes: r.Msg.GetTimezoneOffsetMinutes(), Task: taskView(r.Msg.GetTask(), true), Runs: []TaskRunV{}}
	for _, run := range runs.Msg.GetRuns() {
		v.Runs = append(v.Runs, taskRunView(run))
	}
	return v, nil
}

func (c *call) taskRuns(in taskRunsArg) (any, error) {
	if err := validID("task_id", in.TaskID); err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 50 {
		return nil, errors.New("limit must be 1 to 50")
	}
	r, err := c.cl.Tasks.ListTaskRuns(c.ctx, connect.NewRequest(&tasksv1.ListTaskRunsRequest{TaskId: in.TaskID, Limit: uint32(limit)}))
	if err != nil {
		return nil, apiError(err)
	}
	v := &TaskRunsV{TaskID: in.TaskID, Runs: []TaskRunV{}}
	for _, run := range r.Msg.GetRuns() {
		v.Runs = append(v.Runs, taskRunView(run))
	}
	return v, nil
}

type taskRunNowV struct {
	RunID string `json:"run_id"`
	Next  string `json:"next"`
}

func (c *call) taskRunNow(in taskIDArg) (any, error) {
	if err := validID("task_id", in.TaskID); err != nil {
		return nil, err
	}
	r, err := c.cl.Tasks.RunTaskNow(c.ctx, connect.NewRequest(&tasksv1.RunTaskNowRequest{Id: in.TaskID}))
	if err != nil {
		return nil, apiError(err)
	}
	return &taskRunNowV{RunID: clean(r.Msg.GetRunId(), 64), Next: "The run started. Read its outcome with task_runs in a minute (an assistant task takes a few minutes); do not start it again meanwhile."}, nil
}

// ---------------------------------------------------------------------------------------------------------------------
// plans

type taskCondition struct {
	Path  string `json:"path,omitempty" jsonschema:"dotted names into the tool's result; name[] walks a list; **.name finds a name at any depth; empty = the whole result"`
	Op    string `json:"op" jsonschema:"severity_at_least, gt, gte, lt, lte, eq, ne, in, not_in, nonempty or contains"`
	Value string `json:"value,omitempty" jsonschema:"the value to compare with: a severity, a number, a text or a comma-separated list"`
}

type taskStep struct {
	Tool           string          `json:"tool" jsonschema:"an MCP tool of the operator profile that is neither a plan nor an apply, such as fleet_digest, checks_run_now, block_diagnosis or path_probe"`
	Args           map[string]any  `json:"args,omitempty" jsonschema:"the tool's arguments, as you would call it; ids and names, never a secret"`
	Conditions     []taskCondition `json:"conditions,omitempty" jsonschema:"at most 5; any that holds means the run found a problem"`
	ErrorIsProblem bool            `json:"error_is_problem,omitempty" jsonschema:"the tool failing counts as a problem instead of a failed run"`
}

func (st taskStep) spec() spec.Step {
	s := spec.Step{Tool: st.Tool, ErrorIsProblem: st.ErrorIsProblem}
	if len(st.Args) > 0 {
		s.Args, _ = json.Marshal(st.Args)
	}
	for _, c := range st.Conditions {
		s.Conditions = append(s.Conditions, spec.Condition(c))
	}
	return s
}

func stepOfSpec(s spec.Step) taskStep {
	st := taskStep{Tool: s.Tool, ErrorIsProblem: s.ErrorIsProblem}
	if a := bytes.TrimSpace(s.Args); len(a) > 0 && string(a) != "{}" {
		_ = json.Unmarshal(a, &st.Args)
	}
	for _, c := range s.Conditions {
		st.Conditions = append(st.Conditions, taskCondition(c))
	}
	return st
}

// taskDefArgs are the fields of a task's definition a plan names.
type taskDefArgs struct {
	Name           string     `json:"name" jsonschema:"1 to 80 characters"`
	Kind           string     `json:"kind" jsonschema:"check or assistant"`
	Schedule       string     `json:"schedule" jsonschema:"every 15m, every 2h, daily 07:30 or weekly mon 07:30 (the panel's time zone)"`
	Steps          []taskStep `json:"steps,omitempty" jsonschema:"check: 1 to 8 steps, run in order"`
	Prompt         string     `json:"prompt,omitempty" jsonschema:"assistant: what to do, up to 4000 characters; the panel asks for the verdict itself"`
	NotifyTelegram bool       `json:"notify_telegram,omitempty" jsonschema:"also send a problem and a task that keeps failing to Telegram"`
	Disabled       bool       `json:"disabled,omitempty" jsonschema:"create it switched off"`
}

func (a taskDefArgs) definition() (spec.Definition, error) {
	sc, err := spec.ParseSchedule(a.Schedule)
	if err != nil {
		return spec.Definition{}, errors.New("schedule: write every 15m, every 2h, daily 07:30 or weekly mon 07:30")
	}
	d := spec.Definition{Name: a.Name, Kind: strings.ToLower(strings.TrimSpace(a.Kind)), Enabled: !a.Disabled, Schedule: sc, Prompt: a.Prompt, NotifyTelegram: a.NotifyTelegram}
	for _, st := range a.Steps {
		d.Steps = append(d.Steps, st.spec())
	}
	return d, nil
}

func defArgsOf(d spec.Definition) taskDefArgs {
	a := taskDefArgs{Name: d.Name, Kind: d.Kind, Schedule: d.Schedule.String(), Prompt: d.Prompt, NotifyTelegram: d.NotifyTelegram, Disabled: !d.Enabled}
	for _, s := range d.Steps {
		a.Steps = append(a.Steps, stepOfSpec(s))
	}
	return a
}

func inputOf(d spec.Definition) *tasksv1.TaskInput {
	in := &tasksv1.TaskInput{Name: d.Name, Enabled: d.Enabled, Schedule: scheduleProto(d.Schedule), Prompt: d.Prompt, NotifyTelegram: d.NotifyTelegram,
		Kind: tasksv1.TaskKind_TASK_KIND_CHECK}
	if d.Kind == spec.KindAssistant {
		in.Kind = tasksv1.TaskKind_TASK_KIND_ASSISTANT
	}
	for _, s := range d.Steps {
		st := &tasksv1.CheckStep{Tool: s.Tool, ArgsJson: string(s.Args), ErrorIsProblem: s.ErrorIsProblem}
		if st.ArgsJson == "" {
			st.ArgsJson = "{}"
		}
		for _, c := range s.Conditions {
			st.Conditions = append(st.Conditions, &tasksv1.Condition{Path: c.Path, Op: c.Op, Value: c.Value})
		}
		in.Steps = append(in.Steps, st)
	}
	return in
}

// checkTaskDefinition checks a definition by the TasksService's rules, the tools of a check read from it.
func (c *call) checkTaskDefinition(d spec.Definition) (spec.Definition, error) {
	var tools spec.Tools
	if d.Kind == spec.KindCheck {
		r, err := c.cl.Tasks.ListCheckTools(c.ctx, connect.NewRequest(&tasksv1.ListCheckToolsRequest{}))
		if err != nil {
			return d, apiError(err)
		}
		schemas := map[string]json.RawMessage{}
		for _, t := range r.Msg.GetTools() {
			schemas[t.GetName()] = json.RawMessage(t.GetInputSchemaJson())
		}
		tools = spec.Tools{Allowed: func(n string) bool { _, ok := schemas[n]; return ok }, SchemaOf: func(n string) json.RawMessage { return schemas[n] }}
	}
	out, err := spec.Check(d, tools, func(s string) bool { return s != "" && scrub(s) != s })
	if err != nil {
		return d, failure(errCode(err), "the task is not valid: "+err.Error())
	}
	return out, nil
}

func errCode(err error) string {
	var e *spec.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// taskFacts are the lines of a task's definition the owner reads: the name and the prompt are the agent's words (untrusted).
func taskFacts(d spec.Definition) []Fact {
	enabled := "on"
	if !d.Enabled {
		enabled = "off"
	}
	telegram := "no"
	if d.NotifyTelegram {
		telegram = "yes"
	}
	facts := []Fact{
		{Key: "task", Value: clean(d.Name, 80), Untrusted: true},
		{Key: "task_kind", Value: d.Kind},
		{Key: "schedule", Value: d.Schedule.String()},
		{Key: "enabled", Value: enabled},
		{Key: "telegram", Value: telegram},
	}
	if d.Kind == spec.KindCheck {
		for i, st := range d.Steps {
			line := st.Tool
			if a := string(st.Args); a != "" && a != "{}" {
				line += " " + clean(a, 200)
			}
			for _, cnd := range st.Conditions {
				line += "; problem when " + clean(strings.TrimSpace(cnd.Path+" "+cnd.Op+" "+cnd.Value), 160)
			}
			if st.ErrorIsProblem {
				line += "; a failure is a problem"
			}
			facts = append(facts, Fact{Key: "step", Value: strconv.Itoa(i+1) + ". " + line, Untrusted: true})
		}
	} else {
		facts = append(facts, Fact{Key: "prompt", Value: clean(d.Prompt, 600), Untrusted: true})
	}
	return facts
}

type taskCreateArgs struct {
	ReasonField
	taskDefArgs
}

var taskCreate = changeSpec[taskCreateArgs]{
	name: "task_create", min: ProfileAdmin, danger: true,
	procs: procs(tasksv1connect.TasksServiceListCheckToolsProcedure, tasksv1connect.TasksServiceCreateTaskProcedure),
	desc: "Create a scheduled task of the owner: a periodic check of the fleet. " + taskKinds + taskScheduleWords + taskConditionWords +
		"For example: kind check, schedule daily 07:30, steps [{tool fleet_digest, args {hours 24}, conditions [{path totals.nodes_problem_now, op gt, value 0}]}]; or kind assistant with a prompt such as " +
		"\"Check the fleet and tell me whether something is wrong\". The plan always waits for the owner. At most 50 tasks.",
	plan: func(c *call, a taskCreateArgs) (*planned, error) {
		d, err := a.taskDefArgs.definition()
		if err != nil {
			return nil, err
		}
		if d, err = c.checkTaskDefinition(d); err != nil {
			return nil, err
		}
		return &planned{
			Summary: "Create a scheduled task. The panel runs it on its own at the times below, with the operator profile, and opens an alert when a run finds a problem or keeps failing.",
			Facts:   taskFacts(d), Danger: []string{dangerSchedule}, Params: taskCreateArgs{taskDefArgs: defArgsOf(d)},
			AuditParams: map[string]any{"kind": d.Kind, "schedule": d.Schedule.String()},
		}, nil
	},
	apply: func(c *call, a taskCreateArgs, _ Plan) (done, error) {
		d, err := a.taskDefArgs.definition()
		if err != nil {
			return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
		}
		r, err := c.cl.Tasks.CreateTask(c.ctx, connect.NewRequest(&tasksv1.CreateTaskRequest{Task: inputOf(d)}))
		if err != nil {
			return done{}, apiError(err)
		}
		t := r.Msg.GetTask()
		next := ""
		if t.GetNextRunUnix() > 0 {
			next = " Its first run is at " + time.Unix(t.GetNextRunUnix(), 0).UTC().Format(time.RFC3339) + "."
		}
		return doneWith("task_created", "Task "+clean(t.GetId(), 64)+" created."+next+" Read its runs with task_runs.", "task_id", clean(t.GetId(), 64)), nil
	},
}

type taskUpdateArgs struct {
	ReasonField
	TaskID         string     `json:"task_id" jsonschema:"the task id from tasks_list"`
	Name           *string    `json:"name,omitempty" jsonschema:"a new name"`
	Schedule       *string    `json:"schedule,omitempty" jsonschema:"a new schedule: every 15m, every 2h, daily 07:30 or weekly mon 07:30"`
	Steps          []taskStep `json:"steps,omitempty" jsonschema:"check: the new steps, all of them (they replace the old ones)"`
	Prompt         *string    `json:"prompt,omitempty" jsonschema:"assistant: the new prompt"`
	NotifyTelegram *bool      `json:"notify_telegram,omitempty" jsonschema:"send problems to Telegram too, or stop"`
	Enabled        *bool      `json:"enabled,omitempty" jsonschema:"switch the task on or off"`
}

// taskUpdateStored is what a task_update plan keeps: the whole new definition and the version it was planned from.
type taskUpdateStored struct {
	TaskID  string      `json:"task_id"`
	Version uint64      `json:"version"`
	Def     taskDefArgs `json:"definition"`
}

func (c *call) currentTask(id string) (*tasksv1.Task, error) {
	if err := validID("task_id", id); err != nil {
		return nil, err
	}
	r, err := c.cl.Tasks.GetTask(c.ctx, connect.NewRequest(&tasksv1.GetTaskRequest{Id: id}))
	if err != nil {
		return nil, apiError(err)
	}
	return r.Msg.GetTask(), nil
}

func definitionOfTask(t *tasksv1.Task) spec.Definition {
	d := spec.Definition{Name: t.GetName(), Kind: taskKindWord(t.GetKind()), Enabled: t.GetEnabled(), Schedule: scheduleOfProto(t.GetSchedule()),
		Prompt: t.GetPrompt(), NotifyTelegram: t.GetNotifyTelegram()}
	for _, st := range t.GetSteps() {
		s := spec.Step{Tool: st.GetTool(), Args: json.RawMessage(st.GetArgsJson()), ErrorIsProblem: st.GetErrorIsProblem()}
		for _, cnd := range st.GetConditions() {
			s.Conditions = append(s.Conditions, spec.Condition{Path: cnd.GetPath(), Op: cnd.GetOp(), Value: cnd.GetValue()})
		}
		d.Steps = append(d.Steps, s)
	}
	return d
}

var taskUpdate = changeSpec[taskUpdateArgs]{
	name: "task_update", min: ProfileAdmin, danger: true,
	procs: procs(tasksv1connect.TasksServiceGetTaskProcedure, tasksv1connect.TasksServiceListCheckToolsProcedure, tasksv1connect.TasksServiceUpdateTaskProcedure),
	desc: "Change a scheduled task: give task_id and only the fields to change (name, schedule, steps, prompt, notify_telegram, enabled; steps replace all the old ones). Its kind stays. " +
		taskScheduleWords + taskConditionWords + "A changed task starts with a clean state: its open alerts close and the next run tells again. The plan always waits for the owner; " +
		"if the task changes in the panel before the apply, the apply fails with changed_since_plan.",
	plan: func(c *call, a taskUpdateArgs) (*planned, error) {
		t, err := c.currentTask(a.TaskID)
		if err != nil {
			return nil, err
		}
		d := definitionOfTask(t)
		if a.Name != nil {
			d.Name = *a.Name
		}
		if a.Schedule != nil {
			sc, err := spec.ParseSchedule(*a.Schedule)
			if err != nil {
				return nil, errors.New("schedule: write every 15m, every 2h, daily 07:30 or weekly mon 07:30")
			}
			d.Schedule = sc
		}
		if a.Steps != nil {
			d.Steps = nil
			for _, st := range a.Steps {
				d.Steps = append(d.Steps, st.spec())
			}
		}
		if a.Prompt != nil {
			d.Prompt = *a.Prompt
		}
		if a.NotifyTelegram != nil {
			d.NotifyTelegram = *a.NotifyTelegram
		}
		if a.Enabled != nil {
			d.Enabled = *a.Enabled
		}
		if d, err = c.checkTaskDefinition(d); err != nil {
			return nil, err
		}
		return &planned{
			Summary: "Change a scheduled task. The lines below are the task as it will be; its open alerts close and the next run tells again.",
			Facts:   append([]Fact{{Key: "task_id", Value: clean(t.GetId(), 64)}}, taskFacts(d)...), Danger: []string{dangerSchedule},
			Params:      taskUpdateStored{TaskID: t.GetId(), Version: t.GetVersion(), Def: defArgsOf(d)},
			AuditParams: map[string]any{"task_id": t.GetId(), "schedule": d.Schedule.String(), "enabled": d.Enabled},
		}, nil
	},
	applyStored: func(c *call, pl Plan) (done, error) {
		var st taskUpdateStored
		if err := strictJSON([]byte(pl.ParamsJSON), &st); err != nil {
			return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
		}
		t, err := c.currentTask(st.TaskID)
		if err != nil {
			return done{}, err
		}
		if t.GetVersion() != st.Version {
			return done{}, failure("changed_since_plan", "the task changed since the plan was made: make a new plan")
		}
		d, err := st.Def.definition()
		if err != nil {
			return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
		}
		if _, err := c.cl.Tasks.UpdateTask(c.ctx, connect.NewRequest(&tasksv1.UpdateTaskRequest{Id: st.TaskID, Task: inputOf(d)})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("task_updated", "Task "+clean(st.TaskID, 64)+" changed.", "task_id", clean(st.TaskID, 64)), nil
	},
}

type taskDeleteArgs struct {
	ReasonField
	TaskID string `json:"task_id" jsonschema:"the task id from tasks_list"`
}

var taskDelete = changeSpec[taskDeleteArgs]{
	name: "task_delete", min: ProfileAdmin, danger: true,
	procs: procs(tasksv1connect.TasksServiceGetTaskProcedure, tasksv1connect.TasksServiceDeleteTaskProcedure),
	desc:  "Delete a scheduled task and its runs; its open alerts close. An assistant task's conversation stays with the owner. The plan always waits for the owner.",
	plan: func(c *call, a taskDeleteArgs) (*planned, error) {
		t, err := c.currentTask(a.TaskID)
		if err != nil {
			return nil, err
		}
		return &planned{
			Summary: "Delete a scheduled task and its run history.",
			Facts:   []Fact{{Key: "task_id", Value: clean(t.GetId(), 64)}, {Key: "task", Value: nm(t.GetName()), Untrusted: true}, {Key: "task_kind", Value: taskKindWord(t.GetKind())}, {Key: "schedule", Value: scheduleOfProto(t.GetSchedule()).String()}},
			Danger:  []string{dangerSchedule}, Params: taskDeleteArgs{TaskID: t.GetId()}, AuditParams: map[string]any{"task_id": t.GetId()},
		}, nil
	},
	apply: func(c *call, a taskDeleteArgs, _ Plan) (done, error) {
		if _, err := c.cl.Tasks.DeleteTask(c.ctx, connect.NewRequest(&tasksv1.DeleteTaskRequest{Id: a.TaskID})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("task_deleted", "Task "+clean(a.TaskID, 64)+" deleted.", "task_id", clean(a.TaskID, 64)), nil
	},
}
