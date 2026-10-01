package awgprep

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Answers of Controller.Check and Start: the "state" of the CommandResult of PrepareAwgKernel (agent.proto).
const (
	AnswerReady        = "ready"         // the module is loaded and usable
	AnswerNeedsPrepare = "needs_prepare" // it is not, and a run is possible
	AnswerStarted      = "started"       // a run began just now
	AnswerRunning      = "running"       // one is going already
	AnswerUnsupported  = "unsupported"   // it cannot be done here
)

// Answer says what is the case on this node. Code and Reason are set for "unsupported"; Kernel for "started" and
// "needs_prepare"; Since (Unix seconds) for "running" and "started".
type Answer struct {
	State        string
	Code, Reason string
	Kernel       string
	Since        int64
}

// Config is what the agent hands the Controller: the real machine, or a fake in a test.
type Config struct {
	StatusPath string
	// Env reads the machine (os-release, kernel, container, Secure Boot, systemd).
	Env func(ctx context.Context) Env
	// Ready says whether the module is loaded and usable (the same probe the engine uses to take it).
	Ready func() bool
	// Launch starts the job detached from the caller (a transient systemd unit); the job writes StatusPath.
	Launch func(ctx context.Context) error
	Now    func() time.Time // nil = time.Now (the host clock: the job writes the host clock too)
}

// Controller is the agent side of the automatic preparation: it decides whether a run is needed and possible, starts
// one, allows only one at a time, and reports how each run ended. It installs nothing itself.
type Controller struct {
	cfg Config
	mu  sync.Mutex
}

func New(cfg Config) *Controller {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Controller{cfg: cfg}
}

// alive says whether st is a run that is still going: running, and its heartbeat is not older than Stale.
func (c *Controller) alive(st Status) bool {
	return st.State == StateRunning && c.cfg.Now().Unix()-st.Updated <= int64(Stale/time.Second)
}

// Check answers without changing anything (the dry run).
func (c *Controller) Check(ctx context.Context) Answer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.check(ctx)
}

func (c *Controller) check(ctx context.Context) Answer {
	if st, ok, _ := ReadStatus(c.cfg.StatusPath); ok && c.alive(st) {
		return Answer{State: AnswerRunning, Kernel: st.Kernel, Since: st.Started}
	}
	if c.cfg.Ready() {
		return Answer{State: AnswerReady}
	}
	env := c.cfg.Env(ctx)
	if _, _, err := PlanAuto(env); err != nil {
		var u *Unsupported
		if errors.As(err, &u) {
			return Answer{State: AnswerUnsupported, Code: u.Code, Reason: u.Msg}
		}
		return Answer{State: AnswerUnsupported, Code: CodeDistro, Reason: clip(err.Error(), 200)}
	}
	return Answer{State: AnswerNeedsPrepare, Kernel: env.Kernel}
}

// Start begins a run when one is needed and possible. Anything else is a no-op that says why (ready, running,
// unsupported), so a repeated request while a run is going changes nothing. A job that cannot even be launched is
// "unsupported" with code launch_failed: the automatic way does not work on this node, the manual one still does.
func (c *Controller) Start(ctx context.Context) Answer {
	c.mu.Lock()
	defer c.mu.Unlock()
	a := c.check(ctx)
	if a.State != AnswerNeedsPrepare {
		return a
	}
	now := c.cfg.Now().Unix()
	// The status is written BEFORE the launch: the unit may take a moment to start, and a second request must see a run.
	if err := WriteStatus(c.cfg.StatusPath, Status{State: StateRunning, Kernel: a.Kernel, Started: now, Updated: now}); err != nil {
		return Answer{State: AnswerUnsupported, Code: CodeLaunchFailed, Reason: clip("cannot write the status file: "+err.Error(), 200)}
	}
	if err := c.cfg.Launch(ctx); err != nil {
		reason := clip(err.Error(), 200)
		_ = WriteStatus(c.cfg.StatusPath, Status{State: StateFailed, Kernel: a.Kernel, Started: now, Updated: now, Finished: now,
			Code: CodeLaunchFailed, Reason: reason, Reported: true}) // told to the caller right now: no event for it
		return Answer{State: AnswerUnsupported, Code: CodeLaunchFailed, Reason: reason}
	}
	return Answer{State: AnswerStarted, Kernel: a.Kernel, Since: now}
}

// Poll looks at the status once. A run whose heartbeat stopped is declared failed (the host rebooted, the unit was killed);
// a finished run that was not reported yet is handed to onEnd, then marked reported. Call it every few seconds.
func (c *Controller) Poll(onEnd func(Status)) {
	c.mu.Lock()
	st, ok, _ := ReadStatus(c.cfg.StatusPath)
	if !ok {
		c.mu.Unlock()
		return
	}
	if st.State == StateRunning && !c.alive(st) {
		now := c.cfg.Now().Unix()
		st.State, st.Code, st.Finished = StateFailed, CodeInterrupted, now
		st.Reason = "the job stopped without a result (it never started, the host rebooted or its unit was killed)"
		_ = WriteStatus(c.cfg.StatusPath, st)
	}
	c.mu.Unlock()
	if st.State == StateRunning || st.Reported {
		return
	}
	onEnd(st)
	c.mu.Lock()
	defer c.mu.Unlock()
	if cur, ok, _ := ReadStatus(c.cfg.StatusPath); ok && cur.Started == st.Started && cur.State == st.State {
		cur.Reported = true
		_ = WriteStatus(c.cfg.StatusPath, cur)
	}
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
