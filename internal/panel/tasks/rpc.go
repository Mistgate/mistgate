//go:build !js

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"connectrpc.com/connect"

	tasksv1 "github.com/mistgate/mistgate/gen/mistgate/tasks/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/tasks/spec"
)

type rpc struct{ s *Service }

// coded is a refusal whose message is the code the admin words ("code" or "code: k=v").
func coded(c connect.Code, code string) error { return connect.NewError(c, errors.New(code)) }

func specError(err error) error {
	var e *spec.Error
	if errors.As(err, &e) {
		return connect.NewError(connect.CodeInvalidArgument, e)
	}
	return coded(connect.CodeInvalidArgument, "invalid_step")
}

func (s *Service) internal(what string, err error) error {
	s.cfg.Log.Error("tasks: "+what, "code", "tasks_failed", "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// caller is the signed-in admin or the token's synthetic one; owner reports whether it may change tasks.
func caller(ctx context.Context) (store.Admin, bool, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok {
		return store.Admin{}, false, connect.NewError(connect.CodeUnauthenticated, errors.New("sign in required"))
	}
	return a, a.Role == store.RoleOwner, nil
}

func requireOwner(ctx context.Context) (store.Admin, error) {
	a, owner, err := caller(ctx)
	if err != nil {
		return a, err
	}
	if !owner {
		return a, connect.NewError(connect.CodePermissionDenied, errors.New("owner required"))
	}
	return a, nil
}

func requireWriter(ctx context.Context) (store.Admin, error) {
	a, _, err := caller(ctx)
	if err != nil {
		return a, err
	}
	if a.Role != store.RoleOwner && a.Role != store.RoleHelper {
		return a, connect.NewError(connect.CodePermissionDenied, errors.New("owner or helper required"))
	}
	return a, nil
}

// actorName is who started a manual run, as the run shows it: the admin's name, or the token's.
func actorName(ctx context.Context, a store.Admin) string {
	if p := auth.PrincipalFrom(ctx); p.Token {
		if p.Name != "" {
			return store.Clip(p.Name, 64)
		}
		return store.Clip(p.ActorID(), 64)
	}
	return store.Clip(a.DisplayName, 64)
}

// audit writes one row for a change of a task; params never hold a prompt or arguments.
func (s *Service) audit(ctx context.Context, a store.Admin, action string, params map[string]string) {
	b, _ := json.Marshal(params)
	if err := s.cfg.Store.Audit(ctx, s.now(), store.AuditEntry{Actor: a.ID, Action: action, Params: string(b), Result: "ok"}); err != nil {
		s.cfg.Log.Warn("tasks: audit", "code", "audit_unavailable", "action", action, "err", err)
	}
}

func kindName(k tasksv1.TaskKind) string {
	switch k {
	case tasksv1.TaskKind_TASK_KIND_CHECK:
		return spec.KindCheck
	case tasksv1.TaskKind_TASK_KIND_ASSISTANT:
		return spec.KindAssistant
	}
	return ""
}

func kindProto(k string) tasksv1.TaskKind {
	if k == spec.KindAssistant {
		return tasksv1.TaskKind_TASK_KIND_ASSISTANT
	}
	return tasksv1.TaskKind_TASK_KIND_CHECK
}

func scheduleFrom(p *tasksv1.Schedule) spec.Schedule {
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

// ScheduleProto is a schedule as the API carries it.
func ScheduleProto(sc spec.Schedule) *tasksv1.Schedule {
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

func scheduleOf(t store.ScheduledTask) spec.Schedule {
	return spec.Schedule{Kind: t.ScheduleKind, EveryMinutes: t.EveryMinutes, MinuteOfDay: t.MinuteOfDay, Weekday: t.Weekday}
}

func stepsOf(t store.ScheduledTask) []spec.Step {
	var steps []spec.Step
	_ = json.Unmarshal([]byte(t.Steps), &steps)
	return steps
}

func stepsProto(steps []spec.Step) []*tasksv1.CheckStep {
	out := make([]*tasksv1.CheckStep, 0, len(steps))
	for _, st := range steps {
		p := &tasksv1.CheckStep{Tool: st.Tool, ArgsJson: string(st.Args), ErrorIsProblem: st.ErrorIsProblem}
		if p.ArgsJson == "" {
			p.ArgsJson = "{}"
		}
		for _, c := range st.Conditions {
			p.Conditions = append(p.Conditions, &tasksv1.Condition{Path: c.Path, Op: c.Op, Value: c.Value})
		}
		out = append(out, p)
	}
	return out
}

// runDetail is what a run keeps besides its status: the steps of a check, the provider of an assistant run.
type runDetail struct {
	Steps          []stepResult `json:"steps,omitempty"`
	ConversationID string       `json:"conversation_id,omitempty"`
	Provider       string       `json:"provider,omitempty"`
	Model          string       `json:"model,omitempty"`
	USD            float64      `json:"usd,omitempty"`
	FellBack       bool         `json:"fell_back,omitempty"`
}

type stepResult struct {
	Tool       string   `json:"tool"`
	OK         bool     `json:"ok"`
	Problem    bool     `json:"problem,omitempty"`
	Error      string   `json:"error,omitempty"`
	Matches    []string `json:"matches,omitempty"`
	Excerpt    string   `json:"excerpt,omitempty"`
	DurationMs uint32   `json:"duration_ms"`
}

var runStatus = map[string]tasksv1.RunStatus{
	"running": tasksv1.RunStatus_RUN_STATUS_RUNNING, "ok": tasksv1.RunStatus_RUN_STATUS_OK, "problem": tasksv1.RunStatus_RUN_STATUS_PROBLEM,
	"failed": tasksv1.RunStatus_RUN_STATUS_FAILED, "skipped": tasksv1.RunStatus_RUN_STATUS_SKIPPED,
}

// RunProto is a run as the API carries it.
func RunProto(r store.ScheduledTaskRun) *tasksv1.TaskRun {
	p := &tasksv1.TaskRun{Id: r.ID, TaskId: r.TaskID, Trigger: r.Trigger, Status: runStatus[r.Status], Reason: r.Reason, Summary: r.Summary,
		StartedUnix: r.StartedAt.Unix(), Actor: r.Actor}
	if !r.FinishedAt.IsZero() {
		p.FinishedUnix = r.FinishedAt.Unix()
	}
	var d runDetail
	if json.Unmarshal([]byte(r.Detail), &d) == nil {
		for _, st := range d.Steps {
			p.Steps = append(p.Steps, &tasksv1.StepResult{Tool: st.Tool, Ok: st.OK, Problem: st.Problem, Error: st.Error, Matches: st.Matches, Excerpt: st.Excerpt, DurationMs: st.DurationMs})
		}
		p.ConversationId, p.Provider, p.Model, p.Usd, p.FellBack = d.ConversationID, d.Provider, d.Model, d.USD, d.FellBack
	}
	return p
}

func (s *Service) taskProto(t store.ScheduledTask, last *store.ScheduledTaskRun) (*tasksv1.Task, error) {
	p := &tasksv1.Task{Id: t.ID, Name: t.Name, Kind: kindProto(t.Kind), Enabled: t.Enabled, Schedule: ScheduleProto(scheduleOf(t)),
		NotifyTelegram: t.NotifyTelegram, ConversationId: t.ConversationID, CreatedUnix: t.CreatedAt.Unix(), UpdatedUnix: t.UpdatedAt.Unix(),
		FailStreak: uint32(max(t.FailStreak, 0)), Problem: t.Problem, ProblemSummary: t.ProblemSummary, Running: t.Running(s.now())}
	if t.Enabled && !t.NextRunAt.IsZero() {
		p.NextRunUnix = t.NextRunAt.Unix()
	}
	if t.Kind == spec.KindCheck {
		p.Steps = stepsProto(stepsOf(t))
	} else {
		prompt, err := s.openPrompt(t)
		if err != nil {
			return nil, err
		}
		p.Prompt = prompt
	}
	if last != nil {
		p.LastRun = RunProto(*last)
	}
	return p, nil
}

// looksSecret: the text changes under the MCP scrubber, so it holds something shaped like a key, a token or a link.
func looksSecret(text string) bool { return text != "" && mcp.ScrubText(text) != text }

// LooksSecret is the secret test of a task's text (spec.Check): the MCP scrubber would change it.
func LooksSecret(text string) bool { return looksSecret(text) }

// DefinitionOf is a TaskInput as the spec package checks it.
func DefinitionOf(in *tasksv1.TaskInput) spec.Definition {
	if in == nil {
		return spec.Definition{}
	}
	d := spec.Definition{Name: in.Name, Kind: kindName(in.Kind), Enabled: in.Enabled, Schedule: scheduleFrom(in.Schedule), Prompt: in.Prompt, NotifyTelegram: in.NotifyTelegram}
	for _, st := range in.Steps {
		if st == nil {
			d.Steps = append(d.Steps, spec.Step{})
			continue
		}
		step := spec.Step{Tool: st.Tool, Args: json.RawMessage(st.ArgsJson), ErrorIsProblem: st.ErrorIsProblem}
		for _, c := range st.Conditions {
			if c != nil {
				step.Conditions = append(step.Conditions, spec.Condition{Path: c.Path, Op: c.Op, Value: c.Value})
			}
		}
		d.Steps = append(d.Steps, step)
	}
	return d
}

// InputOf is a definition as the API carries it.
func InputOf(d spec.Definition) *tasksv1.TaskInput {
	return &tasksv1.TaskInput{Name: d.Name, Kind: kindProto(d.Kind), Enabled: d.Enabled, Schedule: ScheduleProto(d.Schedule), Steps: stepsProto(d.Steps),
		Prompt: d.Prompt, NotifyTelegram: d.NotifyTelegram}
}

func validate(in *tasksv1.TaskInput, tools func() (spec.Tools, error)) (spec.Definition, error) {
	d := DefinitionOf(in)
	var t spec.Tools
	if d.Kind == spec.KindCheck {
		var err error
		if t, err = tools(); err != nil {
			return d, coded(connect.CodeUnavailable, "tools_unavailable")
		}
	}
	d, err := spec.Check(d, t, looksSecret)
	if err != nil {
		var e *spec.Error
		if errors.As(err, &e) && e.Code == "tools_unavailable" {
			return d, coded(connect.CodeUnavailable, "tools_unavailable")
		}
		return d, specError(err)
	}
	return d, nil
}

// toolsFor gives validate the tools a check may run.
func (s *Service) toolsFor(ctx context.Context) func() (spec.Tools, error) {
	return func() (spec.Tools, error) {
		tools, err := s.checkTools(ctx)
		if err != nil {
			return spec.Tools{}, err
		}
		allowed, schemaOf := checkSpec(tools)
		return spec.Tools{Allowed: allowed, SchemaOf: schemaOf}, nil
	}
}

func (r rpc) ListTasks(ctx context.Context, _ *connect.Request[tasksv1.ListTasksRequest]) (*connect.Response[tasksv1.ListTasksResponse], error) {
	s := r.s
	if _, err := requireWriter(ctx); err != nil {
		return nil, err
	}
	list, err := s.cfg.Store.ScheduledTasks(ctx)
	if err != nil {
		return nil, s.internal("list tasks", err)
	}
	last, err := s.cfg.Store.LastScheduledRuns(ctx)
	if err != nil {
		return nil, s.internal("list runs", err)
	}
	resp := &tasksv1.ListTasksResponse{TimezoneOffsetMinutes: int32(s.timezone(ctx)), NowUnix: s.now().Unix(), MaxTasks: spec.MaxTasks, Tasks: []*tasksv1.Task{}}
	for _, t := range list {
		var lr *store.ScheduledTaskRun
		if run, ok := last[t.ID]; ok {
			lr = &run
		}
		p, err := s.taskProto(t, lr)
		if err != nil {
			return nil, s.internal("read task", err)
		}
		resp.Tasks = append(resp.Tasks, p)
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) loadTask(ctx context.Context, id string) (store.ScheduledTask, error) {
	if id == "" || len(id) > 64 {
		return store.ScheduledTask{}, coded(connect.CodeNotFound, "task_not_found")
	}
	t, err := s.cfg.Store.ScheduledTask(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return t, coded(connect.CodeNotFound, "task_not_found")
	}
	if err != nil {
		return t, s.internal("read task", err)
	}
	return t, nil
}

func (s *Service) taskWithLast(ctx context.Context, t store.ScheduledTask) (*tasksv1.Task, error) {
	runs, err := s.cfg.Store.ScheduledTaskRuns(ctx, t.ID, 1)
	if err != nil {
		return nil, s.internal("read runs", err)
	}
	var last *store.ScheduledTaskRun
	if len(runs) > 0 {
		last = &runs[0]
	}
	p, err := s.taskProto(t, last)
	if err != nil {
		return nil, s.internal("read task", err)
	}
	return p, nil
}

func (r rpc) GetTask(ctx context.Context, req *connect.Request[tasksv1.GetTaskRequest]) (*connect.Response[tasksv1.GetTaskResponse], error) {
	s := r.s
	if _, err := requireWriter(ctx); err != nil {
		return nil, err
	}
	t, err := s.loadTask(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	p, err := s.taskWithLast(ctx, t)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&tasksv1.GetTaskResponse{Task: p, TimezoneOffsetMinutes: int32(s.timezone(ctx))}), nil
}

// row fills the stored row of a definition: the next time (none when disabled) and the sealed prompt.
func (s *Service) row(ctx context.Context, t store.ScheduledTask, d spec.Definition) (store.ScheduledTask, error) {
	t.Name, t.Kind, t.Enabled, t.NotifyTelegram = d.Name, d.Kind, d.Enabled, d.NotifyTelegram
	t.ScheduleKind, t.EveryMinutes, t.MinuteOfDay, t.Weekday = d.Schedule.Kind, d.Schedule.EveryMinutes, d.Schedule.MinuteOfDay, d.Schedule.Weekday
	t.JitterS = int(spec.Jitter(t.ID, d.Schedule).Seconds())
	t.Steps = "[]"
	if len(d.Steps) > 0 {
		b, err := json.Marshal(d.Steps)
		if err != nil {
			return t, err
		}
		t.Steps = string(b)
	}
	t.PromptEnc = nil
	if d.Prompt != "" {
		t.PromptEnc = s.cfg.Vault.Seal([]byte(d.Prompt), PromptAAD(t.ID))
	}
	t.NextRunAt = timeZero
	if d.Enabled {
		t.NextRunAt = d.Schedule.Next(s.now(), s.timezone(ctx), spec.Jitter(t.ID, d.Schedule))
	}
	return t, nil
}

func (r rpc) CreateTask(ctx context.Context, req *connect.Request[tasksv1.CreateTaskRequest]) (*connect.Response[tasksv1.CreateTaskResponse], error) {
	s := r.s
	admin, err := requireOwner(ctx)
	if err != nil {
		return nil, err
	}
	d, err := validate(req.Msg.Task, s.toolsFor(ctx))
	if err != nil {
		return nil, err
	}
	ownerID, err := s.ownerOf(ctx, admin)
	if err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("owner required"))
	}
	now := s.now()
	t, err := s.row(ctx, store.ScheduledTask{ID: store.NewID("tsk_"), CreatedBy: ownerID, CreatedAt: now, UpdatedAt: now}, d)
	if err != nil {
		return nil, s.internal("encode task", err)
	}
	if err := s.cfg.Store.InsertScheduledTask(ctx, t, spec.MaxTasks); errors.Is(err, store.ErrScheduledTaskLimit) {
		return nil, coded(connect.CodeResourceExhausted, "too_many_tasks: max=50")
	} else if err != nil {
		return nil, s.internal("create task", err)
	}
	s.audit(ctx, admin, "task_create", map[string]string{"task_id": t.ID, "kind": t.Kind, "schedule": scheduleOf(t).String(), "enabled": boolWord(t.Enabled)})
	s.kick()
	p, err := s.taskWithLast(ctx, t)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&tasksv1.CreateTaskResponse{Task: p}), nil
}

// ownerOf is the admin a task belongs to: its runs act as that admin's scheduled tasks, and an assistant task's answers go into
// that admin's conversations. The assistant acts as its admin; an API token (an approved MCP plan) is not a person, so the task
// is the owner's who created the token.
func (s *Service) ownerOf(ctx context.Context, a store.Admin) (string, error) {
	p := auth.PrincipalFrom(ctx)
	if !p.Token || p.Assistant {
		return a.ID, nil
	}
	tok, err := s.cfg.Store.GetAPIToken(ctx, p.TokenID)
	if err != nil {
		return "", err
	}
	owner, err := s.cfg.Store.Admin(ctx, tok.CreatedBy)
	if err != nil {
		return "", err
	}
	if owner.Role != store.RoleOwner {
		return "", errors.New("the token's creator is not the owner")
	}
	return owner.ID, nil
}

var timeZero time.Time

func boolWord(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func (r rpc) UpdateTask(ctx context.Context, req *connect.Request[tasksv1.UpdateTaskRequest]) (*connect.Response[tasksv1.UpdateTaskResponse], error) {
	s := r.s
	admin, err := requireOwner(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.loadTask(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	d, err := validate(req.Msg.Task, s.toolsFor(ctx))
	if err != nil {
		return nil, err
	}
	if d.Kind != t.Kind {
		return nil, coded(connect.CodeInvalidArgument, "invalid_kind")
	}
	t.UpdatedAt = s.now()
	if t, err = s.row(ctx, t, d); err != nil {
		return nil, s.internal("encode task", err)
	}
	if err := s.cfg.Store.UpdateScheduledTask(ctx, t); errors.Is(err, store.ErrNotFound) {
		return nil, coded(connect.CodeNotFound, "task_not_found")
	} else if err != nil {
		return nil, s.internal("update task", err)
	}
	s.audit(ctx, admin, "task_update", map[string]string{"task_id": t.ID, "kind": t.Kind, "schedule": scheduleOf(t).String(), "enabled": boolWord(t.Enabled)})
	s.kick()
	if t, err = s.loadTask(ctx, t.ID); err != nil {
		return nil, err
	}
	p, err := s.taskWithLast(ctx, t)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&tasksv1.UpdateTaskResponse{Task: p}), nil
}

func (r rpc) DeleteTask(ctx context.Context, req *connect.Request[tasksv1.DeleteTaskRequest]) (*connect.Response[tasksv1.DeleteTaskResponse], error) {
	s := r.s
	admin, err := requireOwner(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.loadTask(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	ok, err := s.cfg.Store.DeleteScheduledTask(ctx, t.ID)
	if err != nil {
		return nil, s.internal("delete task", err)
	}
	if !ok {
		return nil, coded(connect.CodeNotFound, "task_not_found")
	}
	s.cancelRun(t.ID)
	s.audit(ctx, admin, "task_delete", map[string]string{"task_id": t.ID, "kind": t.Kind})
	return connect.NewResponse(&tasksv1.DeleteTaskResponse{}), nil
}

func (r rpc) RunTaskNow(ctx context.Context, req *connect.Request[tasksv1.RunTaskNowRequest]) (*connect.Response[tasksv1.RunTaskNowResponse], error) {
	s := r.s
	admin, err := requireWriter(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.loadTask(ctx, req.Msg.Id)
	if err != nil {
		return nil, err
	}
	now := s.now()
	if t.Running(now) {
		return nil, coded(connect.CodeFailedPrecondition, "task_running")
	}
	runID, started, err := s.start(t, "manual", actorName(ctx, admin), now.Add(-manualEvery(t.Kind)), nil)
	if err != nil {
		return nil, s.internal("start run", err)
	}
	if !started {
		if cur, e := s.cfg.Store.ScheduledTask(ctx, t.ID); e == nil && cur.Running(s.now()) {
			return nil, coded(connect.CodeFailedPrecondition, "task_running")
		}
		return nil, coded(connect.CodeFailedPrecondition, "too_soon")
	}
	s.audit(ctx, admin, "task_run_now", map[string]string{"task_id": t.ID, "run_id": runID})
	return connect.NewResponse(&tasksv1.RunTaskNowResponse{RunId: runID}), nil
}

func (r rpc) ListTaskRuns(ctx context.Context, req *connect.Request[tasksv1.ListTaskRunsRequest]) (*connect.Response[tasksv1.ListTaskRunsResponse], error) {
	s := r.s
	if _, err := requireWriter(ctx); err != nil {
		return nil, err
	}
	t, err := s.loadTask(ctx, req.Msg.TaskId)
	if err != nil {
		return nil, err
	}
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, store.ScheduledRunsKept)
	runs, err := s.cfg.Store.ScheduledTaskRuns(ctx, t.ID, limit)
	if err != nil {
		return nil, s.internal("list runs", err)
	}
	resp := &tasksv1.ListTaskRunsResponse{Runs: []*tasksv1.TaskRun{}}
	for _, run := range runs {
		resp.Runs = append(resp.Runs, RunProto(run))
	}
	return connect.NewResponse(resp), nil
}

func (r rpc) ListCheckTools(ctx context.Context, _ *connect.Request[tasksv1.ListCheckToolsRequest]) (*connect.Response[tasksv1.ListCheckToolsResponse], error) {
	s := r.s
	if _, err := requireWriter(ctx); err != nil {
		return nil, err
	}
	tools, err := s.checkTools(ctx)
	if err != nil {
		return nil, coded(connect.CodeUnavailable, "tools_unavailable")
	}
	resp := &tasksv1.ListCheckToolsResponse{Tools: make([]*tasksv1.CheckTool, 0, len(tools))}
	for _, t := range tools {
		resp.Tools = append(resp.Tools, &tasksv1.CheckTool{Name: t.name, Description: t.description, InputSchemaJson: string(t.schema)})
	}
	return connect.NewResponse(resp), nil
}
