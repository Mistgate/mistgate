package countryreach

import (
	"context"
	"testing"
	"time"
)

func TestRunGuardStopCancelsRunsAndRefusesOlderOnes(t *testing.T) {
	var g RunGuard
	gen := g.Generation()
	ctx, done, ok := g.Track(context.Background(), gen, 0)
	if !ok || ctx.Err() != nil {
		t.Fatalf("a run of the current generation: ok=%v err=%v", ok, ctx.Err())
	}
	stored := false
	if !g.Do(gen, func() { stored = true }) || !stored {
		t.Fatal("a run of the current generation could not store")
	}
	g.Stop()
	if ctx.Err() == nil {
		t.Fatal("Stop left a run in flight")
	}
	done()
	if g.Current(gen) {
		t.Fatal("the generation did not move")
	}
	if g.Do(gen, func() { t.Error("a stale run stored") }) {
		t.Fatal("Do admitted a stale run")
	}
	if _, _, ok := g.Track(context.Background(), gen, 0); ok {
		t.Fatal("Track admitted a stale run")
	}
	if _, done, ok := g.Track(context.Background(), g.Generation(), 0); !ok {
		t.Fatal("a run of the new generation was refused")
	} else {
		done()
	}
}

func TestRunGuardTimeoutBoundsTheRun(t *testing.T) {
	var g RunGuard
	ctx, done, ok := g.Track(context.Background(), g.Generation(), time.Millisecond)
	defer done()
	if !ok {
		t.Fatal("refused")
	}
	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
		t.Fatal("the timeout did not end the run")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("err = %v", ctx.Err())
	}
}

// A Stop that comes while a write is under way waits for the write: the owner's change is saved after it.
func TestRunGuardStopWaitsForAWriteInProgress(t *testing.T) {
	var g RunGuard
	gen := g.Generation()
	inside, release := make(chan struct{}), make(chan struct{})
	wrote := make(chan struct{})
	go func() {
		g.Do(gen, func() { close(inside); <-release; close(wrote) })
	}()
	<-inside
	stopped := make(chan struct{})
	go func() { g.Stop(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("Stop did not wait for the write")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-wrote
	<-stopped
}
