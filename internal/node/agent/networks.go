package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/asndb"
	"github.com/mistgate/mistgate/internal/node/update"
)

const (
	networksBundleName = "mistgate-asndb-data-any"
	networksFileName   = "asn.db"
	maxNetworksFile    = 64 << 20
)

var (
	errNetworksSHA     = errors.New("network table digest mismatch")
	errNetworksName    = errors.New("network table name is invalid")
	errNetworksDigest  = errors.New("network table digest is invalid")
	errNetworksInvalid = errors.New("network table file is invalid")
)

type networkTableTarget struct {
	mode uint32
	name string
	sha  string
}

type limitedNetworkBuffer struct {
	bytes.Buffer
}

func (b *limitedNetworkBuffer) Write(p []byte) (int, error) {
	if len(p) > maxNetworksFile-b.Len() {
		return 0, errors.New("network table file exceeds size limit")
	}
	return b.Buffer.Write(p)
}

// configureNetworks updates the target and wakes the table loader; file I/O stays on networkTableLoop.
func (a *Agent) configureNetworks(st *pb.NodeSettings) {
	if a == nil || a.nets == nil {
		return
	}
	if st == nil {
		st = &pb.NodeSettings{}
	}
	target := &networkTableTarget{mode: st.GetNetworks(), name: st.GetNetworksDbName(), sha: st.GetNetworksDbSha256()}
	if target.mode > 2 || a.linkOnly() {
		target.mode = 0
	}
	var cancel context.CancelFunc
	a.networkFetchMu.Lock()
	previous := a.networkTarget.Load()
	changed := !equalNetworkTableTarget(previous, target)
	a.networkTarget.Store(target)
	a.nets.SetMode(target.mode)
	if (a.linkOnly() || target.mode == 0 || target.sha == "") && a.nets.LoadedSHA() != "" {
		a.nets.ClearTable()
	}
	if changed {
		cancel = a.networkFetchCancel
		a.networkFetchCancel = nil
	}
	a.networkFetchMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if changed && a.networkWake != nil {
		select {
		case a.networkWake <- struct{}{}:
		default:
		}
	}
}

// loadCachedNetworkTable accepts only the exact file digest in NodeSettings. A malformed matching file is permanent.
func (a *Agent) loadCachedNetworkTable(target *networkTableTarget) (bool, error) {
	if a == nil || a.nets == nil || target == nil || target.sha == "" || a.linkOnly() {
		return false, nil
	}
	path := filepath.Join(a.cfg.StateDir, networksFileName)
	info, err := os.Stat(path)
	if err != nil || info.Size() < 0 || info.Size() > maxNetworksFile {
		return false, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil || fileSHA(raw) != target.sha {
		return false, nil
	}
	db, err := asndb.Parse(bytes.NewReader(raw))
	if err != nil {
		return false, fmt.Errorf("%w: %w: %v", update.ErrPermanent, errNetworksInvalid, err)
	}
	if !a.sameNetworkTableTarget(target) {
		return false, context.Canceled
	}
	return a.installNetworkTable(target, db), nil
}

// networkTableLoop fetches a missing or changed table over the existing mTLS FetchUpdate route and retries failures.
func (a *Agent) networkTableLoop(ctx context.Context) {
	var activeTarget, failedTarget *networkTableTarget
	minDelay := a.backoffMin
	if minDelay <= 0 {
		minDelay = time.Second
	}
	maxDelay := a.backoffMax
	if maxDelay < minDelay {
		maxDelay = minDelay
	}
	delay := time.Duration(0)

waitForTarget:
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.networkWake:
		}
		target := a.networkTarget.Load()
		if !equalNetworkTableTarget(activeTarget, target) {
			activeTarget = cloneNetworkTableTarget(target)
			delay = minDelay
			failedTarget = nil
		}
		if target == nil || target.mode == 0 || target.sha == "" || a.linkOnly() {
			continue
		}
		if failedTarget != nil && equalNetworkTableTarget(failedTarget, target) {
			continue
		}
		if a.nets.LoadedSHA() == target.sha {
			continue
		}
		if target.name != networksBundleName {
			err := fmt.Errorf("%w: %w", update.ErrPermanent, errNetworksName)
			failedTarget = cloneNetworkTableTarget(target)
			a.logNetworkTableUnavailable(err)
			continue
		}
		if !validNetworkSHA(target.sha) {
			err := fmt.Errorf("%w: %w", update.ErrPermanent, errNetworksDigest)
			failedTarget = cloneNetworkTableTarget(target)
			a.logNetworkTableUnavailable(err)
			continue
		}

		loaded, err := a.loadCachedNetworkTable(target)
		if ctx.Err() != nil {
			return
		}
		if !a.sameNetworkTableTarget(target) {
			goto waitForTarget
		}
		if err != nil && errors.Is(err, update.ErrPermanent) {
			failedTarget = cloneNetworkTableTarget(target)
			a.logNetworkTableUnavailable(err)
			continue
		}
		if loaded {
			continue
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			a.log.Warn("network table cache unavailable", "err", err)
		}

		for {
			err = a.fetchNetworkTable(ctx, target)
			if ctx.Err() != nil {
				return
			}
			if !a.sameNetworkTableTarget(target) {
				goto waitForTarget
			}
			if err == nil {
				break
			}
			if errors.Is(err, update.ErrPermanent) {
				failedTarget = cloneNetworkTableTarget(target)
				a.logNetworkTableUnavailable(err)
				break
			}
			if !errors.Is(err, update.ErrRetryLater) && !errors.Is(err, context.Canceled) {
				a.log.Warn("network table unavailable", "code", "networks_table_unavailable", "reason", networkFailureReason(err))
			}
			if delay <= 0 {
				delay = minDelay
			}
			timer := time.NewTimer(jitter(delay))
			if delay < maxDelay {
				delay = min(delay*2, maxDelay)
			}
		waitForRetry:
			for {
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-a.networkWake:
					if equalNetworkTableTarget(a.networkTarget.Load(), target) {
						continue waitForRetry
					}
					timer.Stop()
					goto waitForTarget
				case <-timer.C:
				}
				break waitForRetry
			}
		}
	}
}

func (a *Agent) logNetworkTableUnavailable(err error) {
	a.log.Warn("network table unavailable", "code", "networks_table_unavailable", "reason", networkFailureReason(err))
}

func cloneNetworkTableTarget(target *networkTableTarget) *networkTableTarget {
	if target == nil {
		return nil
	}
	return &networkTableTarget{mode: target.mode, name: target.name, sha: target.sha}
}

func equalNetworkTableTarget(a, b *networkTableTarget) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.name == b.name && a.sha == b.sha && (a.mode != 0) == (b.mode != 0)
}

func (a *Agent) sameNetworkTableTarget(target *networkTableTarget) bool {
	current := a.networkTarget.Load()
	return current != nil && current.mode != 0 && equalNetworkTableTarget(current, target)
}

func (a *Agent) fetchNetworkTable(runCtx context.Context, target *networkTableTarget) error {
	if target.name != networksBundleName {
		return fmt.Errorf("%w: %w", update.ErrPermanent, errNetworksName)
	}
	if !validNetworkSHA(target.sha) {
		return fmt.Errorf("%w: %w", update.ErrPermanent, errNetworksDigest)
	}
	if !a.sameNetworkTableTarget(target) {
		return context.Canceled
	}
	fetchCtx, cancel := context.WithCancel(runCtx)
	a.networkFetchMu.Lock()
	if !a.sameNetworkTableTarget(target) {
		a.networkFetchMu.Unlock()
		cancel()
		return context.Canceled
	}
	a.networkFetchCancel = cancel
	a.networkFetchMu.Unlock()
	defer func() {
		a.networkFetchMu.Lock()
		if a.networkFetchCancel != nil {
			a.networkFetchCancel = nil
		}
		a.networkFetchMu.Unlock()
		cancel()
	}()

	var body limitedNetworkBuffer
	_, err := (updSource{a: a}).Fetch(fetchCtx, target.name, 0, &body)
	if err != nil {
		return fmt.Errorf("network table fetch failed: %w", err)
	}
	raw := body.Bytes()
	if fileSHA(raw) != target.sha {
		return errNetworksSHA
	}
	db, err := asndb.Parse(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: %w: %v", update.ErrPermanent, errNetworksInvalid, err)
	}
	if !a.sameNetworkTableTarget(target) {
		return context.Canceled
	}
	if err := writeFileAtomic(filepath.Join(a.cfg.StateDir, networksFileName), raw, 0o600); err != nil {
		return fmt.Errorf("network table could not be stored: %w", err)
	}
	if !a.installNetworkTable(target, db) {
		return context.Canceled
	}
	return nil
}

func (a *Agent) installNetworkTable(target *networkTableTarget, db *asndb.DB) bool {
	if a == nil || target == nil || db == nil {
		return false
	}
	a.networkFetchMu.Lock()
	defer a.networkFetchMu.Unlock()
	if !a.sameNetworkTableTarget(target) {
		return false
	}
	a.nets.SetTable(db, target.sha)
	return true
}

func networkFailureReason(err error) string {
	switch {
	case errors.Is(err, errNetworksName):
		return "invalid_bundle_name"
	case errors.Is(err, errNetworksDigest):
		return "invalid_digest"
	case errors.Is(err, errNetworksInvalid):
		return "invalid_table"
	case errors.Is(err, errNetworksSHA):
		return "sha_mismatch"
	case errors.Is(err, context.Canceled):
		return "settings_changed"
	default:
		return "fetch_or_validation_failed"
	}
}

func validNetworkSHA(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(s)
	return err == nil && hex.EncodeToString(decoded) == s
}

func fileSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
