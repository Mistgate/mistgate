package auth

import (
	"testing"
	"time"
)

// On the edge the hook must run inside an after-response task the Worker waits for: a bare goroutine can start after the
// request has computed what to wait for, and its work is then cut off.
func TestEmitRunsTheHookThroughTheEventRunner(t *testing.T) {
	s, _, _ := newTestService(t)
	got := make(chan Event, 2)
	s.SetEventHook(func(e Event) { got <- e })

	var tasks []func()
	s.SetEventRunner(func(work func()) { tasks = append(tasks, work) })
	s.emit(Event{Kind: EventLockout, Login: "alice"})
	if len(tasks) != 1 {
		t.Fatalf("emit handed the runner %d tasks, want one", len(tasks))
	}
	select {
	case e := <-got:
		t.Fatalf("the hook ran before the runner started it: %+v", e)
	case <-time.After(50 * time.Millisecond):
	}
	tasks[0]()
	select {
	case e := <-got:
		if e.Kind != EventLockout || e.Login != "alice" || e.Time.IsZero() {
			t.Fatalf("hook event = %+v", e)
		}
	default:
		t.Fatal("running the task did not run the hook")
	}
}

// Without a runner (the VPS) emit keeps its goroutine.
func TestEmitWithoutRunnerUsesAGoroutine(t *testing.T) {
	s, _, _ := newTestService(t)
	got := make(chan Event, 1)
	s.SetEventHook(func(e Event) { got <- e })
	s.emit(Event{Kind: EventSignIn})
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the hook did not run")
	}
}
