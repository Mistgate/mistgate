package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

const nodeBundleSyncTimeout = 2 * time.Minute

// NodeBundleSource fetches and verifies the latest signed node bundle from its release source. The current built
// value is zero when there is no trusted local bundle.
type NodeBundleSource interface {
	Sync(context.Context, int64) (changed bool, err error)
	BundleStatus() (available bool, manifestSHA256 string)
}

// syncNodeBundle reserves the bundle directory against rollout and panel-update starts while a trusted GitHub
// release is downloaded and atomically installed. Existing rollouts keep their pinned bundle until they finish.
func (s *Service) syncNodeBundle(ctx context.Context) bool {
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
	s.bundleSyncing = true
	s.mu.Unlock()
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
	syncCtx, cancel := context.WithTimeout(ctx, nodeBundleSyncTimeout)
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
