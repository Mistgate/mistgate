package osupdate

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/node/unit"
)

// Answers of Controller.Start: the params.state of the CommandResult of UpdateOs (agent.proto "OS UPDATES").
const (
	AnswerStarted     = "started"
	AnswerRunning     = "running"
	AnswerUnsupported = "unsupported"
	// AnswerFinished: the request repeats the job id of the last job, which is over. Nothing is started; Result, Code and
	// Reason are that job's.
	AnswerFinished = "finished"
)

// SettleMax is how long the agent waits, after a reboot, for the engines to be restored before it reports the result.
const SettleMax = 5 * time.Minute

// Answer says what happened to a request. Code and Reason are set for "unsupported"; JobID and Since (Unix seconds) for
// "started", "running" and "finished".
type Answer struct {
	State        string
	Code, Reason string
	JobID        string
	Since        int64
	Result       string // finished: done or failed
}

// Config is what the agent hands the Controller: the real machine and launcher, or fakes in a test.
type Config struct {
	StatusPath string
	Host       Host
	// Launch starts the job detached from the caller (a transient systemd unit); the job writes StatusPath.
	Launch func(ctx context.Context, spec Spec) error
	// Excluded names the other host work that is going on (an agent update, a unit refresh, the AmneziaWG module build,
	// the security-updates task), "" when there is none. A job never runs next to one of them.
	Excluded func(ctx context.Context) string
	// Settled is asked after a reboot: whether the node restored its engines, and how many inbounds failed to come up.
	// Nil = nothing to wait for.
	Settled func() (settled bool, failedInbounds int)
	Now     func() time.Time
}

// Controller is the agent side of OS jobs: it checks and starts one, allows only one at a time, follows it through its status
// file (also across an agent restart and a reboot) and reports how each job ended. It installs nothing itself.
type Controller struct {
	cfg Config
	mu  sync.Mutex
	// reported remembers the jobs whose end was handed to onEnd, so that a status file that cannot be written (a full or
	// read-only /var) does not make the same end be reported every few seconds.
	reported map[string]bool
}

func New(cfg Config) *Controller {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Host = cfg.Host.defaults()
	return &Controller{cfg: cfg, reported: map[string]bool{}}
}

// SetSettled sets the after-reboot question (the agent that owns the engines exists only after the Controller does).
func (c *Controller) SetSettled(f func() (bool, int)) {
	c.mu.Lock()
	c.cfg.Settled = f
	c.mu.Unlock()
}

func (c *Controller) alive(st Status) bool {
	return st.State == StateRunning && c.cfg.Now().Unix()-st.Updated <= int64(Stale/time.Second)
}

// Active says whether an OS job is going right now (its heartbeat is fresh). Other host work asks it before it starts.
func (c *Controller) Active() bool {
	st, ok, _ := ReadStatus(c.cfg.StatusPath)
	return ok && c.alive(st)
}

// Current is the job OsStatus.job carries: the running one, or the last one not reported yet.
func (c *Controller) Current() (Status, bool) {
	st, ok, _ := ReadStatus(c.cfg.StatusPath)
	if !ok || (st.State != StateRunning && st.Reported) {
		return Status{}, false
	}
	return st, true
}

// Supported is the cheap part of the preflight the agent can answer without root work: a system the job runs on, with
// systemd. The job checks it again.
func (c *Controller) Supported() *Refusal {
	_, ref := checkSystem(c.cfg.Host)
	return ref
}

// Start begins a job. Anything that is not a start is a no-op that says why, so a repeated request while a job runs changes
// nothing. A job that cannot even be launched is "unsupported" with code launch_failed.
func (c *Controller) Start(ctx context.Context, spec Spec) Answer {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ref := ParseSpec(spec.Action, spec.Mode, spec.Reboot, spec.JobID); ref != nil {
		return Answer{State: AnswerUnsupported, Code: ref.Code, Reason: ref.Reason}
	}
	now := c.cfg.Now().Unix()
	if spec.JobID == "" {
		spec.JobID = "osj_n" + strconv.FormatInt(now, 36)
	}
	prev, ok, _ := ReadStatus(c.cfg.StatusPath)
	if ok && spec.JobID == prev.JobID {
		// The same job id again (a panel that asks twice after a reconnect): the answer is the job's, nothing is started. A
		// "reboot always" must not become a second reboot.
		if prev.State == StateRunning {
			return Answer{State: AnswerRunning, JobID: prev.JobID, Since: prev.Started}
		}
		return Answer{State: AnswerFinished, JobID: prev.JobID, Since: prev.Started, Result: prev.State, Code: prev.Code, Reason: prev.Reason}
	}
	if ok && c.alive(prev) {
		return Answer{State: AnswerRunning, JobID: prev.JobID, Since: prev.Started}
	}
	if ok && prev.State != StateRunning && !prev.Reported {
		// The agent has not told the panel how the last job ended yet; Poll does that within seconds.
		return Answer{State: AnswerUnsupported, Code: CodeBusy, Reason: "the result of the previous OS job is not reported yet"}
	}
	if ref := c.Supported(); ref != nil {
		return Answer{State: AnswerUnsupported, Code: ref.Code, Reason: ref.Reason}
	}
	if c.cfg.Excluded != nil {
		if why := c.cfg.Excluded(ctx); why != "" {
			return Answer{State: AnswerUnsupported, Code: CodeBusy, Reason: clip(why, 200)}
		}
	}
	st := Status{
		JobID: spec.JobID, Action: spec.Action, Mode: spec.Mode, RebootPolicy: spec.Reboot, State: StateRunning, Phase: PhasePreflight,
		Started: now, Updated: now, BootID: c.cfg.Host.BootID(), AwgRequired: spec.AwgRequired,
	}
	// The status is written BEFORE the launch: the unit may take a moment to start, and a second request must see a job.
	if err := WriteStatus(c.cfg.StatusPath, st); err != nil {
		return Answer{State: AnswerUnsupported, Code: CodeLaunchFailed, Reason: "the status file cannot be written"}
	}
	if err := c.cfg.Launch(ctx, spec); err != nil {
		code, reason := CodeLaunchFailed, "systemd-run could not start the job"
		if errors.Is(err, unit.ErrBusy) {
			code, reason = CodeBusy, "the OS job unit exists already"
		}
		st.State, st.Code, st.Reason, st.Finished, st.Reported = StateFailed, code, reason, now, true // told to the caller right now
		_ = WriteStatus(c.cfg.StatusPath, st)
		return Answer{State: AnswerUnsupported, Code: code, Reason: reason}
	}
	return Answer{State: AnswerStarted, JobID: spec.JobID, Since: now}
}

// Poll looks at the status once. A job whose heartbeat stopped is finished here: done when the host rebooted as the job said
// it would (and the node came back as expected), failed otherwise. A finished job that was not reported yet is handed to
// onEnd, then marked reported. Call it every few seconds.
func (c *Controller) Poll(onEnd func(Status)) {
	c.mu.Lock()
	st, ok, _ := ReadStatus(c.cfg.StatusPath)
	if !ok {
		c.mu.Unlock()
		return
	}
	if st.State == StateRunning {
		st = c.settle(st)
	}
	c.mu.Unlock()
	key := fmt.Sprintf("%s/%d/%s", st.JobID, st.Started, st.State)
	c.mu.Lock()
	done := c.reported[key]
	c.mu.Unlock()
	if st.State == StateRunning || st.Reported || done {
		return
	}
	onEnd(st)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reported[key] = true
	if cur, ok, _ := ReadStatus(c.cfg.StatusPath); ok && cur.JobID == st.JobID && cur.Started == st.Started && cur.State == st.State {
		cur.Reported = true
		_ = WriteStatus(c.cfg.StatusPath, cur)
	}
}

// settle decides the fate of a "running" status that may have been orphaned by a reboot or a dead unit.
func (c *Controller) settle(st Status) Status {
	now := c.cfg.Now().Unix()
	boot := c.cfg.Host.BootID()
	rebooted := st.Phase == PhaseRebooting && st.BootID != "" && boot != "" && boot != st.BootID
	switch {
	case rebooted:
		return c.afterReboot(st, now)
	case st.BootID != "" && boot != "" && boot != st.BootID:
		// A job of an earlier boot that was not rebooting: the host restarted under it, it will never write again.
		st.State, st.Code = StateFailed, CodeInterrupted
		st.Reason = "the host restarted while the job was running"
	case c.alive(st):
		return st
	case st.Phase == PhaseRebooting:
		// Silent, and the host did not restart: the reboot never happened.
		st.State, st.Code, st.Reboot, st.HeldReason = StateFailed, CodeRebootFailed, RebootHeld, CodeRebootFailed
		st.Reason = "the host did not restart after the reboot command"
	default:
		st.State, st.Code = StateFailed, CodeInterrupted
		st.Reason = "the job stopped without a result (it never started or its unit was killed)"
	}
	st.Finished = now
	_ = WriteStatus(c.cfg.StatusPath, st)
	return st
}

// afterReboot is the first look of the node at itself after the reboot a job did: the expected kernel runs, the AmneziaWG
// module is loaded where it is needed and the engines came back. Until they did, the job stays "running" for at most
// SettleMax.
func (c *Controller) afterReboot(st Status, now int64) Status {
	if st.Back == 0 {
		st.Back = now
	}
	st.KernelNow = c.cfg.Host.KernelRunning()
	st.Phase = ""
	code, reason := "", ""
	switch {
	case st.KernelTo != "" && st.KernelNow != st.KernelTo:
		code, reason = CodeKernelNotSwitched, "the host restarted on another kernel than the one installed"
	case st.AwgRequired && !c.cfg.Host.ModuleLoaded():
		code, reason = CodeAwgNotLoaded, "the AmneziaWG kernel module is not loaded after the restart"
	}
	if code == "" && c.cfg.Settled != nil {
		settled, failed := c.cfg.Settled()
		switch {
		case !settled && now-st.Back < int64(SettleMax/time.Second):
			st.Phase = PhaseRebooting
			st.Updated = now // the node is alive and looking: not a silent job
			_ = WriteStatus(c.cfg.StatusPath, st)
			return st
		case failed > 0:
			code, reason = CodeInboundFailed, "some inbounds did not come back up after the restart"
		}
	}
	st.Reboot = RebootDone
	st.Finished = now
	if code == "" {
		st.State = StateDone
	} else {
		st.State, st.Code, st.Reason = StateFailed, code, reason
	}
	_ = WriteStatus(c.cfg.StatusPath, st)
	return st
}

// Watch polls until ctx ends.
func (c *Controller) Watch(ctx context.Context, every time.Duration, onEnd func(Status)) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		c.Poll(onEnd)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
