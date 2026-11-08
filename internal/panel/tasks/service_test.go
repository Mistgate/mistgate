//go:build !js

package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	tasksv1 "github.com/mistgate/mistgate/gen/mistgate/tasks/v1"
	"github.com/mistgate/mistgate/internal/panel/assistant"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// fakeRuntime is the in-process MCP server of the tests: the operator's tools, and a result per tool the test sets.
type fakeRuntime struct {
	mu         sync.Mutex
	identities []mcp.AssistantIdentity
	calls      []string
	results    map[string]mcp.AssistantToolResult
}

func (r *fakeRuntime) OpenAssistant(_ context.Context, id mcp.AssistantIdentity) (mcp.AssistantSession, error) {
	r.mu.Lock()
	r.identities = append(r.identities, id)
	r.mu.Unlock()
	return &fakeSession{r: r}, nil
}

func (r *fakeRuntime) set(tool, text string, isErr bool) {
	r.mu.Lock()
	r.results[tool] = mcp.AssistantToolResult{Text: text, IsError: isErr}
	r.mu.Unlock()
}

type fakeSession struct{ r *fakeRuntime }

func (*fakeSession) ListTools(context.Context) ([]mcp.AssistantTool, error) {
	obj := func(props string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{` + props + `},"additionalProperties":false}`)
	}
	return []mcp.AssistantTool{
		{Name: "fleet_digest", Description: "digest", InputSchema: obj(`"hours":{"type":"integer"}`)},
		{Name: "path_probe", Description: "probe", InputSchema: obj(`"from":{"type":"string"},"to":{"type":"string"}`)},
		{Name: "user_create_plan", InputSchema: obj(``)},
		{Name: "task_run_now", InputSchema: obj(`"task_id":{"type":"string"}`)},
	}, nil
}

func (s *fakeSession) CallTool(_ context.Context, name string, _ json.RawMessage) (mcp.AssistantToolResult, error) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.r.calls = append(s.r.calls, name)
	if res, ok := s.r.results[name]; ok {
		return res, nil
	}
	return mcp.AssistantToolResult{Text: `{}`}, nil
}

func (*fakeSession) Close() error { return nil }

type fakeAssistant struct {
	mu      sync.Mutex
	runs    []assistant.ScheduledRun
	out     assistant.ScheduledOutcome
	entered chan struct{}
	release chan struct{}
}

func (a *fakeAssistant) RunScheduled(ctx context.Context, run assistant.ScheduledRun) assistant.ScheduledOutcome {
	if a.entered != nil {
		select {
		case a.entered <- struct{}{}:
		default:
		}
	}
	if a.release != nil {
		select {
		case <-a.release:
		case <-ctx.Done():
		}
	}
	if ctx.Err() != nil {
		return assistant.ScheduledOutcome{Status: "failed", Reason: "interrupted"}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.runs = append(a.runs, run)
	return a.out
}

type codeSignalHandler struct {
	want string
	hit  chan struct{}
}

func (*codeSignalHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *codeSignalHandler) Handle(_ context.Context, record slog.Record) error {
	var code string
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "code" {
			code = attr.Value.String()
			return false
		}
		return true
	})
	if code == h.want {
		select {
		case h.hit <- struct{}{}:
		default:
		}
	}
	return nil
}

func (h *codeSignalHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *codeSignalHandler) WithGroup(string) slog.Handler      { return h }

type fixture struct {
	st      *store.Store
	vault   *vault.Vault
	svc     *Service
	rt      *fakeRuntime
	asst    *fakeAssistant
	owner   store.Admin
	helper  store.Admin
	clock   *time.Time
	clockMu *sync.Mutex
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = byte(i + 7)
	}
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{st: st, vault: v, rt: &fakeRuntime{results: map[string]mcp.AssistantToolResult{}}, asst: &fakeAssistant{},
		owner: store.Admin{ID: "adm_owner", DisplayName: "Owner", Role: store.RoleOwner}, helper: store.Admin{ID: "adm_helper", DisplayName: "Helper", Role: store.RoleHelper}}
	for _, a := range []store.Admin{f.owner, f.helper} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO admin(id,display_name,role,user_handle,created_at) VALUES(?,?,?,?,1)`, a.ID, a.DisplayName, a.Role, []byte(a.ID)); err != nil {
			t.Fatal(err)
		}
	}
	// the panel's time zone: UTC+3
	if err := st.SetSettings(ctx, map[string]string{timezoneSetting: "180"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	f.clock, f.clockMu = &now, &sync.Mutex{}
	f.svc = New(Config{Store: st, Vault: v, Assistant: f.asst, Now: f.now, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	f.svc.SetToolRuntime(f.rt)
	return f
}

func (f *fixture) now() time.Time {
	f.clockMu.Lock()
	defer f.clockMu.Unlock()
	return *f.clock
}

func (f *fixture) advance(d time.Duration) {
	f.clockMu.Lock()
	*f.clock = f.clock.Add(d)
	f.clockMu.Unlock()
}

func (f *fixture) as(a store.Admin) context.Context { return auth.WithAdmin(context.Background(), a) }

func checkInput(name string, conds ...*tasksv1.Condition) *tasksv1.TaskInput {
	return &tasksv1.TaskInput{Name: name, Kind: tasksv1.TaskKind_TASK_KIND_CHECK, Enabled: true,
		Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_EVERY, EveryMinutes: 15},
		Steps:    []*tasksv1.CheckStep{{Tool: "fleet_digest", ArgsJson: `{"hours":24}`, Conditions: conds}}}
}

func code(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		msg, _, _ := strings.Cut(ce.Message(), ":")
		return msg
	}
	return ""
}

// runDue starts the due tasks and waits for their runs.
func (f *fixture) runDue(t *testing.T) {
	t.Helper()
	f.svc.dispatch(context.Background())
	f.svc.wg.Wait()
}

func TestCheckToolsAreTheOperatorsReadAndRunTools(t *testing.T) {
	f := newFixture(t)
	r, err := rpc{f.svc}.ListCheckTools(f.as(f.helper), connect.NewRequest(&tasksv1.ListCheckToolsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range r.Msg.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "fleet_digest,path_probe" {
		t.Fatalf("check tools: %v", names)
	}
	if id := f.rt.identities[0]; !id.Scheduled || id.Profile != store.ProfileOperator {
		t.Fatalf("listing identity: %+v", id)
	}
}

// Only the owner writes tasks, and every definition is checked: the schedule's minimum, the tools, their arguments, the
// conditions, no secret; the prompt is sealed at rest.
func TestCreateTaskChecksTheDefinition(t *testing.T) {
	f := newFixture(t)
	r := rpc{f.svc}
	create := func(a store.Admin, in *tasksv1.TaskInput) (*tasksv1.Task, error) {
		resp, err := r.CreateTask(f.as(a), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: in}))
		if err != nil {
			return nil, err
		}
		return resp.Msg.Task, nil
	}
	if _, err := create(f.helper, checkInput("x")); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a helper created a task: %v", err)
	}
	bad := map[string]*tasksv1.TaskInput{
		"schedule_too_often": {Name: "x", Kind: tasksv1.TaskKind_TASK_KIND_ASSISTANT, Prompt: "check", Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_EVERY, EveryMinutes: 10}},
		"tool_not_allowed":   {Name: "x", Kind: tasksv1.TaskKind_TASK_KIND_CHECK, Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY}, Steps: []*tasksv1.CheckStep{{Tool: "task_run_now"}}},
		"invalid_args":       {Name: "x", Kind: tasksv1.TaskKind_TASK_KIND_CHECK, Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY}, Steps: []*tasksv1.CheckStep{{Tool: "fleet_digest", ArgsJson: `{"node":"de1"}`}}},
		"invalid_condition":  checkInput("x", &tasksv1.Condition{Path: "a", Op: "matches"}),
		"secret_in_task":     {Name: "x", Kind: tasksv1.TaskKind_TASK_KIND_ASSISTANT, Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY}, Prompt: "token ghp_" + strings.Repeat("a1B2", 8)},
		"invalid_kind":       {Name: "x", Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY}},
		"empty_prompt":       {Name: "x", Kind: tasksv1.TaskKind_TASK_KIND_ASSISTANT, Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY}},
		"invalid_name":       checkInput(""),
	}
	for want, in := range bad {
		if _, err := create(f.owner, in); code(err) != want {
			t.Errorf("%s: %v", want, err)
		}
	}
	for name, in := range map[string]*tasksv1.TaskInput{
		"prompt": {Name: "x", Kind: tasksv1.TaskKind_TASK_KIND_ASSISTANT, Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY}, Prompt: "password: fixture-value"},
		"args": {Name: "x", Kind: tasksv1.TaskKind_TASK_KIND_CHECK, Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY},
			Steps: []*tasksv1.CheckStep{{Tool: "path_probe", ArgsJson: `{"from":"password: fixture-value","to":"nl1"}`}}},
	} {
		if _, err := create(f.owner, in); code(err) != "secret_in_task" {
			t.Errorf("secret label in task %s: %v", name, err)
		}
	}
	task, err := create(f.owner, &tasksv1.TaskInput{Name: " Morning look ", Kind: tasksv1.TaskKind_TASK_KIND_ASSISTANT, Enabled: true, Prompt: "Check the fleet.",
		Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_DAILY, MinuteOfDay: 7*60 + 30}})
	if err != nil {
		t.Fatal(err)
	}
	// 07:30 at UTC+3 is 04:30 UTC, tomorrow (it is 13:00 there now), plus the task's own delay of at most three minutes
	want := time.Date(2026, 11, 8, 4, 30, 0, 0, time.UTC)
	if next := time.Unix(task.NextRunUnix, 0).UTC(); next.Before(want) || !next.Before(want.Add(3*time.Minute)) || task.Name != "Morning look" || task.Prompt != "Check the fleet." {
		t.Fatalf("created: %+v next %s", task, time.Unix(task.NextRunUnix, 0).UTC())
	}
	var sealed []byte
	_ = f.st.R.QueryRow(`SELECT prompt_enc FROM scheduled_task WHERE id=?`, task.Id).Scan(&sealed)
	if strings.Contains(string(sealed), "Check the fleet") {
		t.Fatal("the prompt is stored in clear")
	}
	var audit int
	_ = f.st.R.QueryRow(`SELECT count(*) FROM audit WHERE action='task_create' AND params NOT LIKE '%Check the fleet%'`).Scan(&audit)
	if audit != 1 {
		t.Fatalf("audit rows: %d", audit)
	}
}

// A check run: its conditions find a problem, which is an alert condition until a clean run; three failures in a row are the
// other alert; the run acts as the owner's scheduled tasks with the operator profile.
func TestCheckRunsRaiseAndCloseTheirAlerts(t *testing.T) {
	f := newFixture(t)
	r := rpc{f.svc}
	in := checkInput("Morning check", &tasksv1.Condition{Path: "totals.nodes_problem_now", Op: "gt", Value: "0"})
	in.NotifyTelegram = true
	resp, err := r.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: in}))
	if err != nil {
		t.Fatal(err)
	}
	id := resp.Msg.Task.Id
	f.rt.set("fleet_digest", `{"totals":{"nodes_problem_now":2},"problem_nodes":[{"name":"nl1"}]}`, false)
	// created at 10:00:00, the task's first slot is 10:00 plus its own delay (under 90 s)
	f.advance(2 * time.Minute)
	f.runDue(t)
	runs, _ := f.st.ScheduledTaskRuns(context.Background(), id, 10)
	if len(runs) != 1 || runs[0].Status != "problem" || runs[0].Trigger != "schedule" || !strings.Contains(runs[0].Summary, "totals.nodes_problem_now = 2") {
		t.Fatalf("runs: %+v", runs)
	}
	f.rt.mu.Lock()
	ident := f.rt.identities[len(f.rt.identities)-1]
	f.rt.mu.Unlock()
	if !ident.Scheduled || ident.Profile != store.ProfileOperator || ident.AdminID != f.owner.ID || ident.TokenID != store.ScheduledTokenID(f.owner.ID) {
		t.Fatalf("run identity: %+v", ident)
	}
	conds := f.svc.Conditions(context.Background())
	if len(conds) != 1 || conds[0].Kind != AlertProblem || conds[0].Subject != id || conds[0].Params["telegram"] != "1" || conds[0].Params["task"] != "Morning check" {
		t.Fatalf("conditions: %+v", conds)
	}
	task, _ := f.st.ScheduledTask(context.Background(), id)
	if !task.NextRunAt.After(f.now()) || task.Running(f.now()) {
		t.Fatalf("after the run: %+v", task)
	}

	// three failed runs in a row
	f.rt.set("fleet_digest", "the panel failed to answer", true)
	for i := 0; i < 3; i++ {
		f.advance(15 * time.Minute)
		f.runDue(t)
	}
	conds = f.svc.Conditions(context.Background())
	if len(conds) != 2 || conds[1].Kind != AlertFailing || conds[1].Params["count"] != "3" || conds[1].Params["reason"] != "tool_failed" {
		t.Fatalf("after three failures: %+v", conds)
	}
	// a clean run closes both
	f.rt.set("fleet_digest", `{"totals":{"nodes_problem_now":0}}`, false)
	f.advance(15 * time.Minute)
	f.runDue(t)
	if conds = f.svc.Conditions(context.Background()); len(conds) != 0 {
		t.Fatalf("a clean run left alerts: %+v", conds)
	}
	runs, _ = f.st.ScheduledTaskRuns(context.Background(), id, 10)
	var d runDetail
	_ = json.Unmarshal([]byte(runs[0].Detail), &d)
	if runs[0].Status != "ok" || len(d.Steps) != 1 || !d.Steps[0].OK || !strings.Contains(d.Steps[0].Excerpt, "nodes_problem_now") {
		t.Fatalf("clean run: %+v %+v", runs[0], d)
	}
	// disabling the task drops its conditions too
	if _, err = f.st.W.Exec(`UPDATE scheduled_task SET problem=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	disabled := checkInput("Morning check")
	disabled.Enabled = false
	if _, err = r.UpdateTask(f.as(f.owner), connect.NewRequest(&tasksv1.UpdateTaskRequest{Id: id, Task: disabled})); err != nil {
		t.Fatal(err)
	}
	if conds = f.svc.Conditions(context.Background()); len(conds) != 0 {
		t.Fatalf("a disabled task holds alerts: %+v", conds)
	}
}

// A step whose tool fails is a problem when the step says so, not a failure.
func TestErrorAsProblem(t *testing.T) {
	f := newFixture(t)
	in := checkInput("Probe")
	in.Steps = []*tasksv1.CheckStep{{Tool: "path_probe", ArgsJson: `{"from":"de1","to":"nl1"}`, ErrorIsProblem: true}}
	resp, err := rpc{f.svc}.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: in}))
	if err != nil {
		t.Fatal(err)
	}
	f.rt.set("path_probe", "failed_precondition: node is offline", true)
	f.advance(16 * time.Minute)
	f.runDue(t)
	runs, _ := f.st.ScheduledTaskRuns(context.Background(), resp.Msg.Task.Id, 1)
	if runs[0].Status != "problem" || !strings.Contains(runs[0].Summary, "node is offline") {
		t.Fatalf("%+v", runs[0])
	}
}

// An assistant task: the runner gets the prompt and the task's conversation, the conversation it reports is kept, and its
// outcome is the run's; without the assistant the run is skipped.
func TestAssistantTaskRuns(t *testing.T) {
	f := newFixture(t)
	r := rpc{f.svc}
	resp, err := r.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: &tasksv1.TaskInput{Name: "Look", Kind: tasksv1.TaskKind_TASK_KIND_ASSISTANT,
		Enabled: true, Prompt: "Check the fleet.", Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_EVERY, EveryMinutes: 15}}}))
	if err != nil {
		t.Fatal(err)
	}
	id := resp.Msg.Task.Id
	f.asst.out = assistant.ScheduledOutcome{Status: "problem", Summary: "nl1 is down", ConversationID: "cnv_task", Provider: "deepseek", USD: 0.02, FellBack: true}
	run, err := r.RunTaskNow(f.as(f.helper), connect.NewRequest(&tasksv1.RunTaskNowRequest{Id: id}))
	if err != nil {
		t.Fatal(err)
	}
	f.svc.wg.Wait()
	if len(f.asst.runs) != 1 || f.asst.runs[0].Prompt != "Check the fleet." || f.asst.runs[0].AdminID != f.owner.ID || f.asst.runs[0].ConversationID != "" {
		t.Fatalf("runner got %+v", f.asst.runs)
	}
	got, _ := f.st.ScheduledTaskRun(context.Background(), run.Msg.RunId)
	p := RunProto(got)
	if p.Status != tasksv1.RunStatus_RUN_STATUS_PROBLEM || p.Trigger != "manual" || p.Actor != "Helper" || p.ConversationId != "cnv_task" || !p.FellBack || p.Summary != "nl1 is down" {
		t.Fatalf("run: %+v", p)
	}
	task, _ := f.st.ScheduledTask(context.Background(), id)
	if task.ConversationID != "cnv_task" {
		t.Fatalf("conversation not kept: %+v", task)
	}
	// a manual run right after another is too soon
	if _, err = r.RunTaskNow(f.as(f.owner), connect.NewRequest(&tasksv1.RunTaskNowRequest{Id: id})); code(err) != "too_soon" {
		t.Fatalf("second manual run: %v", err)
	}
	f.advance(6 * time.Minute)
	f.svc.cfg.Assistant = nil
	f.runDue(t)
	runs, _ := f.st.ScheduledTaskRuns(context.Background(), id, 1)
	if runs[0].Status != "skipped" || runs[0].Reason != "assistant_off" {
		t.Fatalf("without the assistant: %+v", runs[0])
	}
}

func TestManualAssistantRunsUseTheGlobalSemaphore(t *testing.T) {
	f := newFixture(t)
	r := rpc{f.svc}
	resp, err := r.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: &tasksv1.TaskInput{
		Name: "Assistant check", Kind: tasksv1.TaskKind_TASK_KIND_ASSISTANT, Enabled: true, Prompt: "Check the fleet.",
		Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_EVERY, EveryMinutes: 15},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: &tasksv1.TaskInput{
		Name: "Second assistant check", Kind: tasksv1.TaskKind_TASK_KIND_ASSISTANT, Enabled: true, Prompt: "Check the fleet.",
		Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_EVERY, EveryMinutes: 15},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	f.asst.out = assistant.ScheduledOutcome{Status: "ok", Summary: "all clear"}
	f.asst.entered = make(chan struct{}, 2)
	f.asst.release = make(chan struct{})
	first, err := r.RunTaskNow(f.as(f.owner), connect.NewRequest(&tasksv1.RunTaskNowRequest{Id: resp.Msg.Task.Id}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.asst.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first manual run did not start")
	}
	if _, err = r.RunTaskNow(f.as(f.owner), connect.NewRequest(&tasksv1.RunTaskNowRequest{Id: other.Msg.Task.Id})); code(err) != "tasks_busy" {
		t.Fatalf("second manual run while the slot was occupied: %v", err)
	}
	close(f.asst.release)
	f.svc.wg.Wait()
	if _, err = r.RunTaskNow(f.as(f.owner), connect.NewRequest(&tasksv1.RunTaskNowRequest{Id: other.Msg.Task.Id})); err != nil {
		t.Fatalf("manual run after the slot was released: %v", err)
	}
	f.svc.wg.Wait()
	if _, err = f.st.ScheduledTaskRun(context.Background(), first.Msg.RunId); err != nil {
		t.Fatalf("first run was lost: %v", err)
	}
}

func TestRunCancellationIsRegisteredBeforeLeaseClaim(t *testing.T) {
	f := newFixture(t)
	resp, err := rpc{f.svc}.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: checkInput("Cancel while claiming")}))
	if err != nil {
		t.Fatal(err)
	}
	task, err := f.st.ScheduledTask(context.Background(), resp.Msg.Task.Id)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := f.st.W.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE scheduled_task SET updated_at=updated_at WHERE id=?`, task.ID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	type startResult struct {
		id      string
		started bool
		err     error
	}
	started := make(chan startResult, 1)
	go func() {
		id, ok, startErr := f.svc.start(task, "manual", "Owner", f.now().Add(-manualEvery(task.Kind)), nil)
		started <- startResult{id: id, started: ok, err: startErr}
	}()
	active := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.svc.mu.Lock()
		active = f.svc.running[task.ID] != nil
		f.svc.mu.Unlock()
		if active {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if active {
		f.svc.cancelRun(task.ID)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	result := <-started
	if !active {
		f.svc.wg.Wait()
		t.Fatal("start did not register cancellation while its lease claim was blocked")
	}
	if result.err != nil || !result.started {
		t.Fatalf("start after claim: %+v", result)
	}
	f.svc.wg.Wait()
	if len(f.rt.calls) != 0 {
		t.Fatalf("a canceled run called MCP tools: %v", f.rt.calls)
	}
	run, err := f.st.ScheduledTaskRun(ctx, result.id)
	if err != nil || run.Reason != "interrupted" {
		t.Fatalf("canceled run = %+v, %v", run, err)
	}
}

func TestSchedulerRetriesRecoveryUntilTheStoreWorks(t *testing.T) {
	f := newFixture(t)
	input := checkInput("Disabled check")
	input.Enabled = false
	resp, err := rpc{f.svc}.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: input}))
	if err != nil {
		t.Fatal(err)
	}
	now := f.now()
	if ok, err := f.st.ClaimScheduledTask(context.Background(), store.ScheduledTaskRun{ID: "run_recover", TaskID: resp.Msg.Task.Id, Trigger: "schedule", StartedAt: now},
		"lease_recover", now, now.Add(time.Hour), time.Time{}); err != nil || !ok {
		t.Fatalf("claim stale run: %v %v", ok, err)
	}
	if _, err = f.st.W.ExecContext(context.Background(), `ALTER TABLE scheduled_task_run RENAME TO scheduled_task_run_held`); err != nil {
		t.Fatal(err)
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			_, _ = f.st.W.ExecContext(context.Background(), `ALTER TABLE scheduled_task_run_held RENAME TO scheduled_task_run`)
		}
	})
	f.svc.cfg.Tick = 10 * time.Millisecond
	failedRecovery := make(chan struct{}, 1)
	f.svc.cfg.Log = slog.New(&codeSignalHandler{want: "scheduled_recover_failed", hit: failedRecovery})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.svc.Run(ctx); close(done) }()
	select {
	case <-failedRecovery:
	case <-time.After(3 * time.Second):
		stop()
		<-done
		t.Fatal("recovery did not report the temporary store error")
	}
	if _, err = f.st.W.ExecContext(context.Background(), `ALTER TABLE scheduled_task_run_held RENAME TO scheduled_task_run`); err != nil {
		stop()
		<-done
		t.Fatal(err)
	}
	restored = true
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, readErr := f.st.ScheduledTaskRun(context.Background(), "run_recover")
		if readErr == nil && run.Reason == "interrupted" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	<-done
	run, err := f.st.ScheduledTaskRun(context.Background(), "run_recover")
	if err != nil || run.Status != "failed" || run.Reason != "interrupted" {
		t.Fatalf("recovered run = %+v, %v", run, err)
	}
}

func TestRunRetriesFinishAfterTheStoreWorks(t *testing.T) {
	f := newFixture(t)
	r := rpc{f.svc}
	resp, err := r.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: &tasksv1.TaskInput{
		Name: "Assistant check", Kind: tasksv1.TaskKind_TASK_KIND_ASSISTANT, Enabled: true, Prompt: "Check the fleet.",
		Schedule: &tasksv1.Schedule{Kind: tasksv1.ScheduleKind_SCHEDULE_KIND_EVERY, EveryMinutes: 15},
	}}))
	if err != nil {
		t.Fatal(err)
	}
	f.asst.out = assistant.ScheduledOutcome{Status: "ok", Summary: "all clear"}
	f.asst.entered = make(chan struct{}, 1)
	f.asst.release = make(chan struct{})
	failedFinish := make(chan struct{}, 1)
	f.svc.cfg.Log = slog.New(&codeSignalHandler{want: "scheduled_finish_failed", hit: failedFinish})
	run, err := r.RunTaskNow(f.as(f.owner), connect.NewRequest(&tasksv1.RunTaskNowRequest{Id: resp.Msg.Task.Id}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.asst.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("assistant run did not start")
	}
	if _, err = f.st.W.ExecContext(context.Background(), `ALTER TABLE scheduled_task_run RENAME TO scheduled_task_run_held`); err != nil {
		t.Fatal(err)
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			_, _ = f.st.W.ExecContext(context.Background(), `ALTER TABLE scheduled_task_run_held RENAME TO scheduled_task_run`)
		}
	})
	close(f.asst.release)
	select {
	case <-failedFinish:
	case <-time.After(3 * time.Second):
		t.Fatal("finish write did not report the temporary store error")
	}
	if _, err = f.st.W.ExecContext(context.Background(), `ALTER TABLE scheduled_task_run_held RENAME TO scheduled_task_run`); err != nil {
		t.Fatal(err)
	}
	restored = true
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, readErr := f.st.ScheduledTaskRun(context.Background(), run.Msg.RunId)
		if readErr == nil && current.Status != "running" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.svc.wg.Wait()
	current, err := f.st.ScheduledTaskRun(context.Background(), run.Msg.RunId)
	task, taskErr := f.st.ScheduledTask(context.Background(), resp.Msg.Task.Id)
	if err != nil || taskErr != nil || current.Status != "ok" || task.Running(f.now()) {
		t.Fatalf("run after retry = %+v, %v; task = %+v, %v", current, err, task, taskErr)
	}
}

// One run of a task at a time; a run the panel's stop interrupted reads so on start, without counting as a failure; a task
// the panel missed while down runs once, as missed, and its next time is ahead.
func TestOneRunAtATimeAndRecovery(t *testing.T) {
	f := newFixture(t)
	r := rpc{f.svc}
	resp, err := r.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: checkInput("Morning check")}))
	if err != nil {
		t.Fatal(err)
	}
	id := resp.Msg.Task.Id
	ctx := context.Background()
	stale := store.ScheduledTaskRun{ID: "run_stale", TaskID: id, Trigger: "schedule", StartedAt: f.now()}
	if ok, err := f.st.ClaimScheduledTask(ctx, stale, "lease_stale", f.now(), f.now().Add(time.Hour), time.Time{}); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if _, err = r.RunTaskNow(f.as(f.owner), connect.NewRequest(&tasksv1.RunTaskNowRequest{Id: id})); code(err) != "task_running" {
		t.Fatalf("a second run: %v", err)
	}
	// the panel was down for six hours
	f.advance(6 * time.Hour)
	runCtx, stop := context.WithCancel(ctx)
	f.svc.cfg.Tick = time.Hour
	done := make(chan struct{})
	go func() { f.svc.Run(runCtx); close(done) }()
	deadline := time.Now().Add(10 * time.Second)
	var runs []store.ScheduledTaskRun
	for time.Now().Before(deadline) {
		runs, _ = f.st.ScheduledTaskRuns(ctx, id, 10)
		if len(runs) == 2 && runs[0].Status != "running" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	<-done
	if len(runs) != 2 {
		t.Fatalf("runs after the stop: %+v", runs)
	}
	interrupted, missed := runs[1], runs[0]
	if interrupted.ID != "run_stale" || interrupted.Status != "failed" || interrupted.Reason != "interrupted" {
		t.Fatalf("the interrupted run: %+v", interrupted)
	}
	if missed.Trigger != "missed" || missed.Status != "ok" {
		t.Fatalf("the missed run: %+v", missed)
	}
	task, _ := f.st.ScheduledTask(ctx, id)
	if task.FailStreak != 0 || !task.NextRunAt.After(f.now()) || task.NextRunAt.After(f.now().Add(16*time.Minute)) {
		t.Fatalf("after recovery: streak %d, next %s (now %s)", task.FailStreak, task.NextRunAt, f.now())
	}
}

func TestDeleteTask(t *testing.T) {
	f := newFixture(t)
	r := rpc{f.svc}
	resp, err := r.CreateTask(f.as(f.owner), connect.NewRequest(&tasksv1.CreateTaskRequest{Task: checkInput("x")}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.DeleteTask(f.as(f.helper), connect.NewRequest(&tasksv1.DeleteTaskRequest{Id: resp.Msg.Task.Id})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a helper deleted: %v", err)
	}
	if _, err = r.DeleteTask(f.as(f.owner), connect.NewRequest(&tasksv1.DeleteTaskRequest{Id: resp.Msg.Task.Id})); err != nil {
		t.Fatal(err)
	}
	if _, err = r.GetTask(f.as(f.owner), connect.NewRequest(&tasksv1.GetTaskRequest{Id: resp.Msg.Task.Id})); code(err) != "task_not_found" {
		t.Fatalf("after delete: %v", err)
	}
	list, _ := r.ListTasks(f.as(f.helper), connect.NewRequest(&tasksv1.ListTasksRequest{}))
	if len(list.Msg.Tasks) != 0 || list.Msg.TimezoneOffsetMinutes != 180 {
		t.Fatalf("list: %+v", list.Msg)
	}
}
