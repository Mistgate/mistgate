package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// NodeBundleSource fetches and verifies the latest signed node bundle from its release source. The current built
// value is zero when there is no trusted local bundle.
type NodeBundleSource interface {
	Sync(context.Context, int64) (changed bool, err error)
	BundleStatus() (available bool, manifestSHA256 string)
}

// reserveNodeBundleSync reserves the bundle directory against rollout and panel-update starts while a trusted
// GitHub release is downloaded and atomically installed. Existing rollouts keep their pinned bundle until they finish.
func (s *Service) reserveNodeBundleSync(ctx context.Context) bool {
	if s.cfg.NodeBundleSource == nil || s.cfg.Key == nil || s.dist == "" {
		return false
	}

	s.mu.Lock()
	if s.bundleSyncing || s.panelBusy() {
		s.mu.Unlock()
		return false
	}
	if _, err := s.st.ActiveRollout(ctx); err == nil {
		s.mu.Unlock()
		return false
	} else if !errors.Is(err, store.ErrNotFound) {
		s.mu.Unlock()
		s.log.Warn("update: check active rollout before GitHub bundle sync", "err", err)
		return false
	}
	if active, err := s.osRolloutRunning(ctx); err != nil || active {
		s.mu.Unlock()
		return false // an OS update of a node is going: the bundle waits
	}
	s.bundleSyncing = true
	s.mu.Unlock()
	return true
}

// syncNodeBundle is the synchronous form used by tests and callers that need to wait for the result.
func (s *Service) syncNodeBundle(ctx context.Context) bool {
	if !s.reserveNodeBundleSync(ctx) {
		return false
	}
	return s.runNodeBundleSync(ctx)
}

// startNodeBundleSync reserves synchronously so rollout starts see the refusal before the worker leaves the loop,
// then runs network and disk work outside the update loop.
func (s *Service) startNodeBundleSync(ctx context.Context) {
	if !s.reserveNodeBundleSync(ctx) {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runNodeBundleSync(ctx)
	}()
}

func (s *Service) runNodeBundleSync(ctx context.Context) bool {
	defer func() {
		s.mu.Lock()
		s.bundleSyncing = false
		s.mu.Unlock()
		s.kick()
	}()

	currentBuilt := int64(0)
	if b := s.current(); b != nil && b.trusted {
		currentBuilt = b.manifest.Built
	}
	syncCtx, cancel := context.WithTimeout(ctx, s.downloadTimeoutOrDefault(ctx))
	defer cancel()
	changed, err := s.cfg.NodeBundleSource.Sync(syncCtx, currentBuilt)
	if err != nil {
		if errors.Is(err, ErrGitHubNodeBundleMissing) {
			s.log.Info("update: latest GitHub release has no signed node bundle yet")
		} else {
			s.log.Warn("update: sync signed node bundle from GitHub", "err", err)
		}
		return false
	}
	available, remoteManifestHash := s.cfg.NodeBundleSource.BundleStatus()
	bs := s.current()
	if changed {
		bs = s.rescan()
	}
	if !available || bs == nil || !bs.trusted || remoteManifestHash == "" {
		if changed && bs != nil {
			s.log.Error("update: downloaded GitHub node bundle did not pass local verification", "status", bs.view.Status.String(), "error_key", bs.view.ErrorKey)
		}
		return false
	}
	manifestHash := sha256.Sum256(bs.raw)
	if hex.EncodeToString(manifestHash[:]) != remoteManifestHash {
		s.log.Error("update: installed node bundle does not match the GitHub release manifest")
		return false
	}
	if changed {
		s.log.Info("update: trusted node bundle downloaded from GitHub", "version", bs.manifest.Version, "built", bs.manifest.Built)
	}
	return changed
}
