package awgprep

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Status states, as the job writes them.
const (
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

const (
	// JobTimeout is the hard limit of one run. A build takes 1-5 minutes; a dpkg lock held by unattended-upgrades or a slow
	// mirror may add some; nothing legitimate takes a quarter of an hour.
	JobTimeout = 15 * time.Minute
	// LockWait bounds the wait for a foreign dpkg lock before the first command.
	LockWait = 5 * time.Minute
	// Stale is how long a "running" status may go without a heartbeat before the agent calls the job dead.
	Stale = 2 * time.Minute

	heartbeatEvery = 15 * time.Second
)

// Status is the file the job writes in the agent's state directory (mode 0600) and the agent reads. The job is the only
// writer until it ends; after that only the agent writes, once, to set Reported.
type Status struct {
	State    string `json:"state"`
	Kernel   string `json:"kernel,omitempty"` // the release the module is built for
	Started  int64  `json:"started"`          // Unix seconds, the host clock
	Updated  int64  `json:"updated"`          // the last heartbeat
	Finished int64  `json:"finished,omitempty"`
	Step     int    `json:"step,omitempty"`
	Steps    int    `json:"steps,omitempty"`
	Code     string `json:"code,omitempty"`   // failed: a Code* constant
	Reason   string `json:"reason,omitempty"` // failed: one short English sentence
	Reported bool   `json:"reported,omitempty"`
}

// ReadStatus returns the status at path; ok is false when there is none (never ran, or unreadable garbage).
func ReadStatus(path string) (s Status, ok bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, false, nil
	}
	if err != nil {
		return Status{}, false, err
	}
	if json.Unmarshal(b, &s) != nil || (s.State != StateRunning && s.State != StateDone && s.State != StateFailed) {
		return Status{}, false, nil // a half-written or foreign file is "nothing", never a reason to block a new run
	}
	return s, true, nil
}

// WriteStatus replaces the file atomically.
func WriteStatus(path string, s Status) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Job is one run of the plan: what `awg prepare-kernel --yes` does, plus a hard timeout, a bounded wait for the apt lock
// and, when StatusPath is set, the status file the agent follows.
type Job struct {
	StatusPath string        // "" = no status file (a run by hand)
	Timeout    time.Duration // 0 = JobTimeout
	Out        io.Writer
	Env        Env
	Auto       bool // PlanAuto instead of Plan (a run the panel asked for)
	Run        Runner
	Sys        Sys
	Src        string
	Now        func() time.Time // nil = time.Now
	Heartbeat  time.Duration    // 0 = 15 s
}

// Do runs it. The error is a *Failure (Short gives its code); a status file is left behind for it either way.
func (j Job) Do(ctx context.Context) error {
	now, limit, beat := j.Now, j.Timeout, j.Heartbeat
	if now == nil {
		now = time.Now
	}
	if limit <= 0 {
		limit = JobTimeout
	}
	if beat <= 0 {
		beat = heartbeatEvery
	}
	st := Status{State: StateRunning, Kernel: j.Env.Kernel, Started: now().Unix(), Updated: now().Unix()}
	var mu sync.Mutex
	write := func(change func(*Status)) {
		if j.StatusPath == "" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if change != nil {
			change(&st)
		}
		st.Updated = now().Unix()
		_ = WriteStatus(j.StatusPath, st) // the agent calls a status that stops moving dead; nothing better to do here
	}
	fail := func(err error) error {
		code, detail := Short(err)
		write(func(s *Status) { s.State, s.Code, s.Reason, s.Finished = StateFailed, code, detail, now().Unix() })
		return err
	}

	steps, _, err := j.Steps()
	if err != nil {
		code, msg := CodeStepFailed, err.Error()
		var u *Unsupported
		if errors.As(err, &u) {
			code = u.Code
		}
		write(func(s *Status) {
			s.State, s.Code, s.Reason, s.Finished = StateFailed, code, clip(msg, 200), now().Unix()
		})
		return &Failure{Code: code, Detail: clip(msg, 200), Long: msg}
	}
	write(func(s *Status) { s.Steps = len(steps) })

	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	hb := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(beat)
		defer t.Stop()
		for {
			select {
			case <-hb:
				return
			case <-t.C:
				write(nil)
			}
		}
	}()
	err = WaitAptLock(ctx, j.Out, steps, j.Sys, LockWait)
	if err == nil {
		err = Run(ctx, j.Out, steps, j.Src, j.Run, j.Sys, limit, func(i, n int) { write(func(s *Status) { s.Step, s.Steps = i, n }) })
	}
	close(hb)
	wg.Wait()
	if err != nil {
		return fail(err)
	}
	write(func(s *Status) { s.State, s.Finished = StateDone, now().Unix() })
	return nil
}

// Steps is the plan this job would run (and the notes to read first), or why there is none.
func (j Job) Steps() ([]Step, []string, error) {
	if j.Auto {
		return PlanAuto(j.Env)
	}
	return Plan(j.Env)
}
