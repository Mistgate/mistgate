package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
)

type blockingNodeBundleSource struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
	once    sync.Once
	hash    string
}

func (s *blockingNodeBundleSource) Sync(ctx context.Context, _ int64) (bool, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.started) })
	select {
	case <-s.release:
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (s *blockingNodeBundleSource) BundleStatus() (bool, string) { return true, s.hash }

func TestBundleSyncRunsOffLoopSingleFlightsAndClearsRefusal(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	nodeID := e.addNode("de1", nodeOpts{})
	current := e.s.current()
	hash := sha256.Sum256(current.raw)
	source := &blockingNodeBundleSource{started: make(chan struct{}), release: make(chan struct{}), hash: hex.EncodeToString(hash[:])}
	e.s.cfg.NodeBundleSource = source
	e.s.cfg.Tick = 10 * time.Millisecond
	e.s.cfg.BundlePoll = time.Hour
	e.s.cfg.Rescan = time.Hour
	e.s.cfg.Prune = time.Hour

	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() {
		e.s.Run(ctx)
		close(done)
	}()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("bundle sync did not start")
	}
	e.s.startNodeBundleSync(ctx) // a second poll while the first source call is still blocked

	// Run's worker tick clears the in-memory updating projection when no rollout is active.
	e.s.setUpdating(map[string]bool{nodeID: true})
	if !e.s.Updating(nodeID) {
		t.Fatal("test setup: sentinel updating state was not set")
	}
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for e.s.Updating(nodeID) {
		select {
		case <-ticker.C:
		case <-deadline.C:
			cancel()
			t.Fatal("the slow bundle sync prevented the update loop from processing a tick")
		}
	}

	if got := source.calls.Load(); got != 1 {
		t.Fatalf("overlapping bundle polls started %d syncs, want one", got)
	}
	if _, err := e.s.start(e.ctx, []string{nodeID}, 0); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "the GitHub node bundle is being installed") {
		t.Fatalf("rollout start during sync = %v, want the bundle-installing refusal", err)
	}

	close(source.release)
	waitForBundleSync(t, e.s, false)
	if err := e.s.beginPanelUpdate(e.ctx); err != nil {
		t.Fatalf("panel update remained refused after the bundle sync finished: %v", err)
	}
	e.s.finishPanelUpdate()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("update loop did not stop")
	}
}

func waitForBundleSync(t *testing.T, s *Service, want bool) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer deadline.Stop()
	defer ticker.Stop()
	for {
		s.mu.Lock()
		got := s.bundleSyncing
		s.mu.Unlock()
		if got == want {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("bundleSyncing did not become %v", want)
		}
	}
}
