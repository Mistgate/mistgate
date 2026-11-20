package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// scriptedSource is a GitHub bundle source the test drives: what it answers, and whether it waits to be released.
type scriptedSource struct {
	mu      sync.Mutex
	calls   int
	changed bool
	err     error
	block   chan struct{} // nil: answers at once
	started chan struct{} // closed by the first call
	hash    string
}

func (s *scriptedSource) Sync(ctx context.Context, _ int64) (bool, error) {
	s.mu.Lock()
	s.calls++
	if s.started != nil && s.calls == 1 {
		close(s.started)
	}
	changed, err, block := s.changed, s.err, s.block
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return changed, err
}

func (s *scriptedSource) BundleStatus() (bool, string) { return true, s.hash }

func (s *scriptedSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// withSource gives the service a source that vouches for the bundle in the folder.
func (e *env) withSource(src *scriptedSource) *scriptedSource {
	e.t.Helper()
	sum := sha256.Sum256(e.s.current().raw)
	src.hash = hex.EncodeToString(sum[:])
	src.started = make(chan struct{})
	e.s.cfg.NodeBundleSource = src
	e.s.cfg.BundleSyncWait = time.Minute // answered by the source, not by the clock
	return src
}

// rescan plays the owner's "read the folder again" and returns the sync words of the answer.
func (e *env) rescan() (sync, wait string) {
	e.t.Helper()
	resp, err := (rpc{e.s}).RescanBundle(e.ctx, connect.NewRequest(&adminv1.RescanBundleRequest{}))
	if err != nil {
		e.t.Fatal(err)
	}
	p := resp.Msg.GetBundle().GetParams()
	return p["sync"], p["sync_wait"]
}

func (e *env) wantSyncStarted(src *scriptedSource) {
	e.t.Helper()
	select {
	case <-src.started:
	case <-time.After(5 * time.Second):
		e.t.Fatal("the bundle sync did not start")
	}
}

// A panel with no source of its own bundle only reads the folder, and says nothing about a download.
func TestRescanWithoutASourceSaysNothingAboutDownloads(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	if sync, wait := e.rescan(); sync != "" || wait != "" {
		t.Fatalf("sync %q wait %q", sync, wait)
	}
}

func TestRescanStartsTheDownloadAndSaysHowItWent(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  *scriptedSource
		want string
	}{
		{"nothing newer", &scriptedSource{}, "fresh"},
		{"a newer bundle installed", &scriptedSource{changed: true}, "installed"},
		{"the source cannot be reached", &scriptedSource{err: errors.New("network is down")}, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.defaultBundle()
			src := e.withSource(tc.src)
			if sync, wait := e.rescan(); sync != tc.want || wait != "" {
				t.Fatalf("sync %q wait %q, want %q", sync, wait, tc.want)
			}
			if src.callCount() != 1 {
				t.Fatalf("the source was asked %d times", src.callCount())
			}
			e.s.mu.Lock()
			syncing := e.s.bundleSyncing
			e.s.mu.Unlock()
			if syncing {
				t.Fatal("the folder stays reserved after the sync")
			}
		})
	}
}

// A download that is still on its way is said to be going on, and a second click does not start another.
func TestRescanSaysTheDownloadGoesOn(t *testing.T) {
	e := newEnv(t)
	e.defaultBundle()
	src := e.withSource(&scriptedSource{block: make(chan struct{})})
	e.s.cfg.BundleSyncWait = time.Millisecond
	if sync, _ := e.rescan(); sync != "downloading" {
		t.Fatalf("sync %q, want downloading", sync)
	}
	e.wantSyncStarted(src)
	if sync, _ := e.rescan(); sync != "downloading" || src.callCount() != 1 {
		t.Fatalf("second click: sync %q after %d calls", sync, src.callCount())
	}
	close(src.block)
	waitForBundleSync(t, e.s, false)
}

// Something that holds the folder (a rollout, also a paused one; an OS update; the panel's own update) is named, and nothing is fetched.
func TestRescanSaysWhatTheBundleWaitsFor(t *testing.T) {
	t.Run("a rollout", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		src := e.withSource(&scriptedSource{})
		e.addNode("de1", nodeOpts{})
		e.startRollout()
		if sync, wait := e.rescan(); sync != "waiting" || wait != "rollout" {
			t.Fatalf("sync %q wait %q", sync, wait)
		}
		if _, err := e.s.pauseByOwner(e.ctx, ""); err != nil {
			t.Fatal(err)
		}
		if sync, wait := e.rescan(); sync != "waiting" || wait != "rollout" {
			t.Fatalf("paused: sync %q wait %q", sync, wait)
		}
		if src.callCount() != 0 {
			t.Fatalf("the source was asked %d times", src.callCount())
		}
	})
	t.Run("an OS update", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		src := e.withSource(&scriptedSource{})
		id := e.osNode("de1")
		e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
		if sync, wait := e.rescan(); sync != "waiting" || wait != "os_rollout" || src.callCount() != 0 {
			t.Fatalf("sync %q wait %q", sync, wait)
		}
	})
	t.Run("the panel's own update", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		src := e.withSource(&scriptedSource{})
		e.s.mu.Lock()
		e.s.panelInstalling = true
		e.s.mu.Unlock()
		if sync, wait := e.rescan(); sync != "waiting" || wait != "panel_update" || src.callCount() != 0 {
			t.Fatalf("sync %q wait %q", sync, wait)
		}
	})
}

// The bundle that kept waiting for a rollout is fetched the moment the rollout ends, whatever its end.
func TestTheBundleIsFetchedWhenARolloutEnds(t *testing.T) {
	t.Run("done", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		a := e.addNode("de1", nodeOpts{})
		e.hl.set(a, 0, 0, 0)
		src := e.withSource(&scriptedSource{})
		e.startRollout()
		e.tick()
		e.upgrade(a)
		e.tick()
		if src.callCount() != 0 {
			t.Fatal("the source was asked while the rollout ran")
		}
		e.commit(a)
		e.tick()
		e.wantRollout(store.RolloutDone, "")
		e.wantSyncStarted(src)
	})
	t.Run("cancelled while paused", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		e.addNode("de1", nodeOpts{})
		src := e.withSource(&scriptedSource{})
		e.startRollout()
		if _, err := e.s.pauseByOwner(e.ctx, ""); err != nil {
			t.Fatal(err)
		}
		if src.callCount() != 0 {
			t.Fatal("the source was asked while the rollout was paused")
		}
		if _, err := e.s.cancel(e.ctx, ""); err != nil {
			t.Fatal(err)
		}
		e.wantRollout(store.RolloutCancelled, "")
		e.wantSyncStarted(src)
	})
	t.Run("an OS update done", func(t *testing.T) {
		e := newEnv(t)
		e.defaultBundle()
		id := e.addNode("de1", nodeOpts{caps: osCaps, inbounds: 1})
		e.osBoot(id, e.clk.Now().Add(-48*time.Hour), kernelOld)
		e.osStatus(id, nil)
		src := e.withSource(&scriptedSource{})
		e.startOs(id, store.OsModeSecurity, store.OsRebootNever)
		e.osTick()
		x := e.osStep(id)
		e.osEvent(id, "os_update_done", map[string]string{"job_id": x.JobID, "upgraded": "2", "reboot": "not_asked"})
		e.osTick()
		e.osTick()
		e.clk.Advance(time.Minute)
		e.osTick()
		e.wantOsStep(id, store.OsStepPassed, "")
		e.wantSyncStarted(src)
	})
}
