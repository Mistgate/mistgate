//go:build !js

// Package tasks is the owner's scheduled tasks (mistgate.tasks.v1, design/adr/0017-agent-operable-fleet.md section 5): the
// TasksService, the scheduler that runs the tasks, and the two runners.
//
//   - A check task runs its steps, MCP tools of the operator profile, in process through the same MCP server as an outside
//     agent (scrubbed results, the panel's rate limits, the audit), with the hidden identity of the owner's scheduled tasks
//     (store.ScheduledTokenID). No model is involved: each step's conditions are tested on the tool's JSON result.
//   - An assistant task gives its prompt to the assistant (assistant.Service.RunScheduled), same identity and profile.
//
// The same scheduler runs the assistant's follow-ups (follow_up.go): one-time continuations of a conversation, held under a
// lease of their own so each runs once.
//
// The scheduler wakes every 20 seconds (and at once after a change), takes the due tasks, and starts a run of each that no run
// holds: a lease in the task's row means one run of a task at a time, and the run row is written with it. Runs are bounded (a
// check run 5 minutes, an assistant run assistant.ScheduledRunLimit) and at most two check runs and one assistant run go at
// a time. A run that ends gives the lease back with the next time from now, so a task the panel missed while it was down runs
// once on start, not once per missed time. A run a stopped panel left behind reads as interrupted when it starts again.
//
// A run that found a problem, and a task that failed three runs in a row, are alert conditions the health evaluator reads
// (Conditions): SCHEDULED_CHECK and SCHEDULED_TASK_FAILING, which close themselves with a clean run.
//
// The Cloudflare edition has none of it: the package is not linked into its Workers, and the app mounts a stub that answers
// UNIMPLEMENTED (internal/panel/app/tasks_wiring_js.go).
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/tasks/v1/tasksv1connect"
	"github.com/mistgate/mistgate/internal/panel/assistant"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/tasks/spec"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// Assistant runs a scheduled assistant task (assistant.Service.RunScheduled) and a follow-up the assistant set in a
// conversation (RunFollowUp).
type Assistant interface {
	RunScheduled(context.Context, assistant.ScheduledRun) assistant.ScheduledOutcome
	RunFollowUp(context.Context, store.AssistantFollowUp) assistant.FollowUpOutcome
}

// Config configures Service.
type Config struct {
	Store *store.Store
	Vault *vault.Vault
	// Assistant runs the assistant tasks and the follow-ups; nil: the tasks are skipped (assistant_off), no follow-up runs.
	Assistant Assistant
	Now       func() time.Time
	Log       *slog.Logger
	// Tick is how often the scheduler looks for due tasks (20 s).
	Tick time.Duration
}

const (
	defaultTick = 20 * time.Second
	// checkRunLimit bounds a check run: at most 8 steps, the slowest tool waits about 80 s (checks_run_now).
	checkRunLimit = 5 * time.Minute
	// missedAfter: a run that starts this late after its time is a missed one (the panel was down).
	missedAfter = 2 * time.Minute
	// manual runs of a task: at most one a minute for a check task, one in five minutes for an assistant task
	checkManualEvery     = time.Minute
	assistantManualEvery = 5 * time.Minute
	// the most runs at a time
	checkRunsAtOnce     = 2
	assistantRunsAtOnce = 1
	// toolsFresh is how long the list of tools a check may run is kept
	toolsFresh = 10 * time.Minute
	// excerptRunes bounds the start of a tool's result kept with a run
	excerptRunes = 2000
	// timezoneSetting is the panel's time zone, the one of the update schedules (internal/panel/update/schedule.go)
	timezoneSetting = "update_schedule_timezone_offset_minutes"
)

// Service is the TasksService and the scheduler.
type Service struct {
	cfg Config

	mu      sync.Mutex
	runtime mcp.AssistantRuntime
	tools   []checkTool
	toolsAt time.Time
	base    context.Context // the scheduler's context: manual runs end with the panel too
	running map[string]*activeRun

	conds     conditions
	wake      chan struct{}
	checkSem  chan struct{}
	assistSem chan struct{}
	followSem chan struct{}
	wg        sync.WaitGroup
}

type checkTool struct {
	name, description string
	schema            json.RawMessage
}

// New returns the service. Run starts the scheduler.
func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Tick <= 0 {
		cfg.Tick = defaultTick
	}
	return &Service{cfg: cfg, running: map[string]*activeRun{}, wake: make(chan struct{}, 1),
		checkSem: make(chan struct{}, checkRunsAtOnce), assistSem: make(chan struct{}, assistantRunsAtOnce), followSem: make(chan struct{}, followUpsAtOnce),
		base: context.Background()}
}

type activeRun struct{ cancel context.CancelFunc }

// SetToolRuntime installs the in-process MCP server once the admin API is assembled (the assistant's, internal/panel/app).
func (s *Service) SetToolRuntime(r mcp.AssistantRuntime) {
	s.mu.Lock()
	s.runtime = r
	s.tools = nil
	s.mu.Unlock()
}

// Handler is the Connect handler, mounted at /api/mistgate.tasks.v1.TasksService/ behind the session middleware.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return tasksv1connect.NewTasksServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(64 << 10)}, opts...)...)
}

func (s *Service) now() time.Time { return s.cfg.Now() }

// kick wakes the scheduler for a change it should see at once.
func (s *Service) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// timezone is the panel's offset east of UTC in minutes; a missing or unusable value is UTC.
func (s *Service) timezone(ctx context.Context) int {
	raw, err := s.cfg.Store.Setting(ctx, timezoneSetting)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < -12*60 || n > 14*60 || n%15 != 0 {
		return 0
	}
	return n
}

// PromptAAD is the vault's associated data of a task's prompt.
func PromptAAD(id string) string { return "scheduled-task-prompt:" + id }

func (s *Service) openPrompt(t store.ScheduledTask) (string, error) {
	if len(t.PromptEnc) == 0 {
		return "", nil
	}
	plain, err := s.cfg.Vault.Open(t.PromptEnc, PromptAAD(t.ID))
	if err != nil {
		return "", errors.New("the task's prompt is unreadable")
	}
	return string(plain), nil
}

// session opens the in-process MCP session of an admin's scheduled tasks: the operator profile.
func (s *Service) session(ctx context.Context, adminID string) (mcp.AssistantSession, error) {
	s.mu.Lock()
	runtime := s.runtime
	s.mu.Unlock()
	if runtime == nil {
		return nil, errors.New("tools_unavailable")
	}
	admin, err := s.cfg.Store.Admin(ctx, adminID)
	if err != nil {
		return nil, errors.New("not_allowed")
	}
	token, err := s.cfg.Store.EnsureScheduledToken(ctx, admin.ID, s.now())
	if err != nil {
		return nil, err
	}
	return runtime.OpenAssistant(ctx, mcp.AssistantIdentity{TokenID: token.ID, Profile: store.ProfileOperator, AdminID: admin.ID, AdminName: admin.DisplayName, Scheduled: true})
}

// taskTools are the MCP tools about the tasks themselves: a check never runs one (a task does not start or read tasks).
var taskTools = map[string]bool{"tasks_list": true, "task_get": true, "task_runs": true, "task_run_now": true}

// checkTools lists the tools a check step may run: the operator profile's tools that are neither a plan nor an apply nor a
// task tool, with their input schemas, sorted. The list is kept for toolsFresh.
func (s *Service) checkTools(ctx context.Context) ([]checkTool, error) {
	s.mu.Lock()
	runtime, cached, at := s.runtime, s.tools, s.toolsAt
	s.mu.Unlock()
	if cached != nil && s.now().Sub(at) < toolsFresh {
		return cached, nil
	}
	if runtime == nil {
		return nil, errors.New("tools_unavailable")
	}
	// listing calls no panel procedure: an identity of its own, never stored, is enough to open the operator's server
	session, err := runtime.OpenAssistant(ctx, mcp.AssistantIdentity{TokenID: "sct_listing", Profile: store.ProfileOperator, AdminID: "tasks", Scheduled: true})
	if err != nil {
		return nil, errors.New("tools_unavailable")
	}
	defer session.Close()
	list, err := session.ListTools(ctx)
	if err != nil {
		return nil, errors.New("tools_unavailable")
	}
	var out []checkTool
	for _, t := range list {
		if !CheckToolName(t.Name) {
			continue
		}
		out = append(out, checkTool{name: t.Name, description: t.Description, schema: t.InputSchema})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	s.mu.Lock()
	s.tools, s.toolsAt = out, s.now()
	s.mu.Unlock()
	return out, nil
}

// CheckToolName reports whether a tool of the operator profile may be a step of a check: not a plan, not an apply, not a task tool.
func CheckToolName(name string) bool {
	return name != "" && !taskTools[name] && !hasSuffix(name, "_plan") && !hasSuffix(name, "_apply")
}

func hasSuffix(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// checkSpec turns the tool list into the two functions spec.Steps checks steps with.
func checkSpec(tools []checkTool) (func(string) bool, func(string) json.RawMessage) {
	byName := make(map[string]json.RawMessage, len(tools))
	for _, t := range tools {
		byName[t.name] = t.schema
	}
	return func(n string) bool { _, ok := byName[n]; return ok }, func(n string) json.RawMessage { return byName[n] }
}

// limitFor is how long a run of a task of this kind may take.
func limitFor(kind string) time.Duration {
	if kind == spec.KindAssistant {
		return assistant.ScheduledRunLimit
	}
	return checkRunLimit
}

func manualEvery(kind string) time.Duration {
	if kind == spec.KindAssistant {
		return assistantManualEvery
	}
	return checkManualEvery
}

func (s *Service) runSemaphore(kind string) chan struct{} {
	if kind == spec.KindAssistant {
		return s.assistSem
	}
	return s.checkSem
}
