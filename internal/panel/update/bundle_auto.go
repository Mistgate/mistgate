package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// NodeBundleSource fetches and verifies the latest signed node bundle from its release source. The current built
// value is zero when there is no trusted local bundle.
type NodeBundleSource interface {
	Sync(context.Context, int64) (changed bool, err error)
	BundleStatus() (available bool, manifestSHA256 string)
}

// Why a bundle sync cannot start now (bundleSyncBlock); "" = it can.
const (
	syncBlockOff        = "off"          // no source, no release key or no data directory: the bundle comes from the folder only
	syncBlockSyncing    = "syncing"      // another sync is on its way
	syncBlockPanel      = "panel_update" // the panel is being updated
	syncBlockRollout    = "rollout"      // an agent rollout is active, paused ones included
	syncBlockOsRollout  = "os_rollout"   // an OS update of a node is running
	syncBlockUnreadable = "unreadable"   // the rollout state could not be read
)

// bundleSyncBlock says why a sync of the GitHub bundle cannot start now, "" when it can. The caller holds s.mu.
func (s *Service) bundleSyncBlock(ctx context.Context) string {
	switch {
	case s.cfg.NodeBundleSource == nil || s.cfg.Key == nil || s.dist == "":
		return syncBlockOff
	case s.bundleSyncing:
		return syncBlockSyncing
	case s.panelBusy():
		return syncBlockPanel
	}
	if _, err := s.st.ActiveRollout(ctx); err == nil {
		return syncBlockRollout
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Warn("update: check active rollout before GitHub bundle sync", "err", err)
		return syncBlockUnreadable
	}
	if active, err := s.osRolloutRunning(ctx); err != nil {
		return syncBlockUnreadable
	} else if active {
		return syncBlockOsRollout // an OS update of a node is going: the bundle waits
	}
	return ""
}

// reserveNodeBundleSyncLocked reserves the bundle directory against rollout and panel-update starts while a trusted
// GitHub release is downloaded and atomically installed (s.mu is held). Existing rollouts keep their pinned bundle until they
// finish. It returns what stops the sync, "" when the directory is reserved.
func (s *Service) reserveNodeBundleSyncLocked(ctx context.Context) string {
	why := s.bundleSyncBlock(ctx)
	if why == "" {
		s.bundleSyncing = true
	}
	return why
}

func (s *Service) reserveNodeBundleSync(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserveNodeBundleSyncLocked(ctx) == ""
}

// syncNodeBundle is the synchronous form used by tests and callers that need to wait for the result.
func (s *Service) syncNodeBundle(ctx context.Context) bool {
	if !s.reserveNodeBundleSync(ctx) {
		return false
	}
	changed, _ := s.runNodeBundleSync(ctx)
	return changed
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

// startNodeBundleSyncLocked is startNodeBundleSync for a caller that holds s.mu: a rollout has just ended, and the bundle that
// waited for it is fetched now, not at the next poll.
func (s *Service) startNodeBundleSyncLocked() {
	ctx := s.runCtx
	if s.reserveNodeBundleSyncLocked(ctx) != "" {
		return
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.runNodeBundleSync(ctx)
	}()
}

// What a RescanBundle answers about the GitHub bundle (Bundle.params "sync", and "sync_wait" for the third):
//
//	fresh        the source has nothing newer than the bundle in the folder
//	installed    a newer bundle was downloaded and installed during the call
//	downloading  the check or the download is still running (this call or an earlier one started it)
//	failed       the source could not be reached, or what it gave did not verify
//	waiting      a rollout (also a paused one), an OS update or the panel's own update holds the folder; sync_wait says which
//
// No "sync" at all: this panel has no GitHub source, the folder is all there is.
const (
	syncFresh       = "fresh"
	syncInstalled   = "installed"
	syncDownloading = "downloading"
	syncFailed      = "failed"
	syncWaiting     = "waiting"
)

// syncBundleOnRequest starts the GitHub bundle sync for the owner's "read the folder again" and says how it went, waiting up to
// BundleSyncWait for the answer of the source (a long download goes on in the background).
func (s *Service) syncBundleOnRequest(ctx context.Context) (outcome, wait string) {
	s.mu.Lock()
	runCtx := s.runCtx
	why := s.reserveNodeBundleSyncLocked(runCtx)
	s.mu.Unlock()
	switch why {
	case "":
	case syncBlockOff:
		return "", ""
	case syncBlockSyncing:
		return syncDownloading, ""
	case syncBlockUnreadable:
		return syncFailed, ""
	default:
		return syncWaiting, why
	}
	type result struct{ changed, ok bool }
	done := make(chan result, 1)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		changed, ok := s.runNodeBundleSync(runCtx)
		done <- result{changed, ok}
	}()
	timer := time.NewTimer(s.cfg.BundleSyncWait)
	defer timer.Stop()
	select {
	case r := <-done:
		switch {
		case !r.ok:
			return syncFailed, ""
		case r.changed:
			return syncInstalled, ""
		}
		return syncFresh, ""
	case <-timer.C:
	case <-ctx.Done():
	}
	return syncDownloading, ""
}

// runNodeBundleSync runs the sync that was reserved. changed: a newer bundle was installed; ok: the source answered and what it
// gave verified (false when it could not be reached or its bundle did not pass).
func (s *Service) runNodeBundleSync(ctx context.Context) (changed, ok bool) {
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
			return false, true // nothing newer is on offer
		}
		s.log.Warn("update: sync signed node bundle from GitHub", "err", err)
		return false, false
	}
	available, remoteManifestHash := s.cfg.NodeBundleSource.BundleStatus()
	bs := s.current()
	if changed {
		bs = s.rescan()
	}
	if !available || bs == nil || !bs.trusted || remoteManifestHash == "" {
		if changed && bs != nil {
			s.log.Error("update: downloaded GitHub node bundle did not pass local verification", "status", bs.view.Status.String(), "error_key", bs.view.ErrorKey)
			return false, false
		}
		return false, true // nothing on offer is newer than what the folder holds
	}
	manifestHash := sha256.Sum256(bs.raw)
	if hex.EncodeToString(manifestHash[:]) != remoteManifestHash {
		s.log.Error("update: installed node bundle does not match the GitHub release manifest")
		return false, false
	}
	if changed {
		s.log.Info("update: trusted node bundle downloaded from GitHub", "version", bs.manifest.Version, "built", bs.manifest.Built)
	}
	return changed, true
}
