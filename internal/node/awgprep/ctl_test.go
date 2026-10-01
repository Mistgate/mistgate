package awgprep

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeNode struct {
	env      Env
	ready    atomic.Bool
	launches atomic.Int32
	launchEr error
	at       *atomic.Int64 // the clock, Unix seconds
	path     string
	sawState string // the status the job would have seen when launched
}

func newFake(t *testing.T) (*fakeNode, *Controller) {
	t.Helper()
	f := &fakeNode{env: Env{OSRelease: osr("ubuntu", "debian"), Kernel: "6.8.0-142-generic", CPUs: 2, Systemd: true},
		at: &atomic.Int64{}, path: filepath.Join(t.TempDir(), "awg-prepare.json")}
	f.at.Store(1_000_000)
	c := New(Config{
		StatusPath: f.path,
		Env:        func(context.Context) Env { return f.env },
		Ready:      f.ready.Load,
		Launch: func(context.Context) error {
			f.launches.Add(1)
			if st, ok, _ := ReadStatus(f.path); ok {
				f.sawState = st.State
			}
			return f.launchEr
		},
		Now: func() time.Time { return time.Unix(f.at.Load(), 0) },
	})
	return f, c
}

func TestCheckSaysWhatIsTheCase(t *testing.T) {
	f, c := newFake(t)
	if a := c.Check(context.Background()); a.State != AnswerNeedsPrepare || a.Kernel != "6.8.0-142-generic" {
		t.Errorf("a plain node = %+v", a)
	}
	f.ready.Store(true)
	if a := c.Check(context.Background()); a.State != AnswerReady {
		t.Errorf("a loaded module = %+v", a)
	}
	if f.launches.Load() != 0 {
		t.Error("a check launched something")
	}
	if _, ok, _ := ReadStatus(f.path); ok {
		t.Error("a check wrote a status")
	}
}

func TestUnsupportedHosts(t *testing.T) {
	for name, c := range map[string]struct {
		mut  func(*Env)
		code string
	}{
		"container":   {func(e *Env) { e.Container = "openvz" }, CodeContainer},
		"alpine":      {func(e *Env) { e.OSRelease = osr("alpine", "") }, CodeDistro},
		"secure boot": {func(e *Env) { e.SecureBoot = true }, CodeSecureBoot},
		"no systemd":  {func(e *Env) { e.Systemd = false }, CodeNoSystemd},
		"no kernel":   {func(e *Env) { e.Kernel = "" }, CodeUnknownKernel},
	} {
		f, ctl := newFake(t)
		c.mut(&f.env)
		a := ctl.Check(context.Background())
		if a.State != AnswerUnsupported || a.Code != c.code || a.Reason == "" {
			t.Errorf("%s: check = %+v", name, a)
		}
		if a := ctl.Start(context.Background()); a.State != AnswerUnsupported || a.Code != c.code {
			t.Errorf("%s: start = %+v", name, a)
		}
		if f.launches.Load() != 0 {
			t.Errorf("%s: launched on a host that cannot do it", name)
		}
		if _, ok, _ := ReadStatus(f.path); ok {
			t.Errorf("%s: left a status behind", name)
		}
	}
}

func TestStartWritesTheStatusBeforeItLaunchesAndStartsOnce(t *testing.T) {
	f, c := newFake(t)
	a := c.Start(context.Background())
	if a.State != AnswerStarted || a.Kernel != "6.8.0-142-generic" || a.Since != 1_000_000 {
		t.Fatalf("start = %+v", a)
	}
	if f.sawState != StateRunning {
		t.Errorf("the status was %q when the job launched: a second request in that moment would start a second run", f.sawState)
	}
	// a repeated request while it runs is a no-op that says so
	f.at.Add(30)
	if b := c.Start(context.Background()); b.State != AnswerRunning || b.Since != 1_000_000 {
		t.Errorf("repeat = %+v", b)
	}
	if b := c.Check(context.Background()); b.State != AnswerRunning {
		t.Errorf("check while running = %+v", b)
	}
	if f.launches.Load() != 1 {
		t.Errorf("launches = %d", f.launches.Load())
	}
}

func TestConcurrentRequestsStartOneRun(t *testing.T) {
	f, c := newFake(t)
	var wg sync.WaitGroup
	var started atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.Start(context.Background()).State == AnswerStarted {
				started.Add(1)
			}
		}()
	}
	wg.Wait()
	if f.launches.Load() != 1 || started.Load() != 1 {
		t.Errorf("launches = %d, started answers = %d", f.launches.Load(), started.Load())
	}
}

func TestStartWhenReadyDoesNothing(t *testing.T) {
	f, c := newFake(t)
	f.ready.Store(true)
	if a := c.Start(context.Background()); a.State != AnswerReady || f.launches.Load() != 0 {
		t.Errorf("start = %+v, launches %d", a, f.launches.Load())
	}
}

// A job that cannot even be launched is "unsupported / launch_failed", is not reported as an event, and does not block a retry.
func TestALaunchThatFailsIsUnsupportedAndCanBeRetried(t *testing.T) {
	f, c := newFake(t)
	f.launchEr = errors.New("systemd-run: Failed to start transient service unit: Access denied")
	a := c.Start(context.Background())
	if a.State != AnswerUnsupported || a.Code != CodeLaunchFailed || a.Reason == "" {
		t.Fatalf("start = %+v", a)
	}
	st, _, _ := ReadStatus(f.path)
	if st.State != StateFailed || st.Code != CodeLaunchFailed || !st.Reported {
		t.Errorf("status = %+v", st)
	}
	var ended int
	c.Poll(func(Status) { ended++ })
	if ended != 0 {
		t.Error("a launch failure was reported as an event on top of the answer")
	}
	f.launchEr = nil
	if b := c.Start(context.Background()); b.State != AnswerStarted {
		t.Errorf("retry = %+v", b)
	}
}

func TestPollReportsEachEndOnce(t *testing.T) {
	f, c := newFake(t)
	var got []Status
	onEnd := func(s Status) { got = append(got, s) }
	c.Poll(onEnd)
	if len(got) != 0 {
		t.Fatal("nothing ran, yet an end was reported")
	}
	c.Start(context.Background())
	c.Poll(onEnd)
	if len(got) != 0 {
		t.Fatal("a running job was reported as ended")
	}
	// the job finishes (it writes the file; the controller only reads)
	if err := WriteStatus(f.path, Status{State: StateDone, Kernel: "6.8.0-142-generic", Started: 1_000_000, Updated: 1_000_200, Finished: 1_000_200}); err != nil {
		t.Fatal(err)
	}
	c.Poll(onEnd)
	c.Poll(onEnd)
	if len(got) != 1 || got[0].State != StateDone {
		t.Fatalf("ends = %+v", got)
	}
	if st, _, _ := ReadStatus(f.path); !st.Reported {
		t.Error("the end was not marked reported")
	}
}

func TestPollReportsAFailureWithItsCode(t *testing.T) {
	f, c := newFake(t)
	_ = WriteStatus(f.path, Status{State: StateFailed, Started: 1, Updated: 2, Finished: 2, Code: CodeStepFailed, Reason: "step 2 of 5 failed: install"})
	var got []Status
	c.Poll(func(s Status) { got = append(got, s) })
	if len(got) != 1 || got[0].Code != CodeStepFailed || got[0].Reason == "" {
		t.Fatalf("ends = %+v", got)
	}
}

// A job whose heartbeat stopped (the host rebooted mid-build, its unit was killed) is failed, once.
func TestAStalledJobIsDeclaredInterrupted(t *testing.T) {
	f, c := newFake(t)
	c.Start(context.Background())
	var got []Status
	onEnd := func(s Status) { got = append(got, s) }
	f.at.Add(int64(Stale/time.Second) - 1)
	c.Poll(onEnd)
	if len(got) != 0 {
		t.Fatal("a job within the heartbeat window was declared dead")
	}
	f.at.Add(2)
	c.Poll(onEnd)
	c.Poll(onEnd)
	if len(got) != 1 || got[0].State != StateFailed || got[0].Code != CodeInterrupted {
		t.Fatalf("ends = %+v", got)
	}
	// and the dead run does not block a new one
	if b := c.Start(context.Background()); b.State != AnswerStarted {
		t.Errorf("start after a dead run = %+v", b)
	}
}

func TestWatchPollsUntilTheContextEnds(t *testing.T) {
	f, c := newFake(t)
	_ = WriteStatus(f.path, Status{State: StateDone, Started: 1, Updated: 2, Finished: 2})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ended := make(chan Status, 4)
	go func() { c.Watch(ctx, 5*time.Millisecond, func(s Status) { ended <- s }); close(done) }()
	select {
	case s := <-ended:
		if s.State != StateDone {
			t.Errorf("state = %q", s.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no end reported")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not stop")
	}
}
