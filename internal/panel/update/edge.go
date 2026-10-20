package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/release"
	"google.golang.org/protobuf/proto"
)

const (
	edgeTickTimeout       = 20 * time.Second
	edgeRollbackTimeout   = 2 * time.Minute
	releaseBundleTTL      = 10 * time.Minute
	releaseBundleErrorTTL = time.Minute
	updatingTTL           = 10 * time.Second
)

const capLinkUpdate = "update-link/1"

// updateCap is the capability the node must advertise for this edition's update transport.
func (s *Service) updateCap() string {
	if s.cfg.Edge {
		return capLinkUpdate
	}
	return capUpdate
}

func (s *Service) bundleFor(ctx context.Context) *bundleState {
	if s.cfg.Edge {
		return s.releaseBundle(ctx)
	}
	return s.current()
}

// EdgeTick runs one bounded edge pass. Every send is synchronous, so its context is also the upper bound on the
// service mutex held around that send. The cron's scheduled time is used only for the prune schedule.
func (s *Service) EdgeTick(ctx context.Context, now time.Time) {
	if now.IsZero() {
		now = s.now()
	}
	ctx, cancel := context.WithTimeout(ctx, edgeTickTimeout)
	defer cancel()

	s.tick(ctx)
	s.processScheduledNodeUpdates(ctx)
	if now.UTC().Minute() == 17 {
		s.pruneOld(ctx)
	}
}

// releaseBundle returns this edge panel build's signed node release, cached per isolate. Network I/O is outside bmu;
// a cancelled caller does not poison the cache for the next request.
func (s *Service) releaseBundle(ctx context.Context) *bundleState {
	return s.releaseBundleAt(ctx, s.now(), false)
}

func (s *Service) refreshReleaseBundle(ctx context.Context) *bundleState {
	return s.releaseBundleAt(ctx, s.now(), true)
}

func (s *Service) releaseBundleAt(ctx context.Context, now time.Time, force bool) *bundleState {
	if !force {
		s.bmu.Lock()
		cached, until := s.edgeBundle, s.releaseCacheUntil
		if cached != nil && cached.trusted && cached.manifest != nil && now.Unix() > cached.manifest.Expires {
			cached = expiredReleaseBundle(cached, now)
			s.edgeBundle = cached
			s.releaseCacheUntil = now.Add(releaseBundleErrorTTL)
			s.bmu.Unlock()
			return cached
		}
		if cached != nil && now.Before(until) {
			s.bmu.Unlock()
			return cached
		}
		s.bmu.Unlock()
	}

	cached := s.fetchReleaseBundle(ctx, now)
	if ctx.Err() != nil {
		return cached
	}
	ttl := releaseBundleTTL
	if !cached.trusted {
		ttl = releaseBundleErrorTTL
	}
	s.bmu.Lock()
	s.edgeBundle = cached
	s.releaseCacheUntil = now.Add(ttl)
	s.bmu.Unlock()
	return cached
}

func (s *Service) fetchReleaseBundle(ctx context.Context, now time.Time) *bundleState {
	b := &bundleState{view: &adminv1.Bundle{ScannedUnix: now.Unix()}}
	if !validReleaseTag(s.cfg.PanelVersion) {
		bundleErr(b.view, adminv1.BundleStatus_BUNDLE_STATUS_MISSING, "no_release", nil)
		return b
	}
	if len(s.cfg.Key) != ed25519.PublicKeySize {
		bundleErr(b.view, adminv1.BundleStatus_BUNDLE_STATUS_NO_KEY, "", nil)
		return b
	}
	manifest, raw, sig, err := fetchReleaseManifest(ctx, s.cfg.Key, s.cfg.PanelVersion, "", nil, now)
	if err != nil {
		code := releaseBundleErrorKey(err)
		bundleErr(b.view, adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, code, nil)
		return b
	}
	if len(manifest.Files) == 0 {
		bundleErr(b.view, adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, "bad_manifest", nil)
		return b
	}
	b.manifest, b.raw, b.sig, b.trusted = manifest, raw, sig, true
	b.view.Status = adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED
	b.view.Version, b.view.Built, b.view.ExpiresUnix = manifest.Version, manifest.Built, manifest.Expires
	b.view.DownloadBase = releaseVersionBase(manifest.Version)
	for _, f := range manifest.Files {
		b.view.Files = append(b.view.Files, &adminv1.BundleFile{Os: f.OS, Arch: f.Arch, Name: f.Name, Size: uint64(f.Size), Sha256: f.SHA256})
	}
	return b
}

func releaseBundleErrorKey(err error) string {
	switch {
	case errors.Is(err, release.ErrBadSignature):
		return "bad_signature"
	case errors.Is(err, release.ErrExpired):
		return "expired"
	case errors.Is(err, release.ErrUnsupportedSchema):
		return "unsupported_schema"
	case errors.Is(err, errFetchReleaseMetadata):
		return "release_unreachable"
	default:
		return "bad_manifest"
	}
}

func expiredReleaseBundle(b *bundleState, now time.Time) *bundleState {
	out := *b
	if b.view != nil {
		out.view = proto.Clone(b.view).(*adminv1.Bundle)
		bundleErr(out.view, adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, "expired", nil)
		out.view.ScannedUnix = now.Unix()
	}
	out.trusted = false
	return &out
}

func releaseVersionBase(version string) string {
	return strings.TrimRight(releaseDownloadBase, "/") + "/" + url.PathEscape(version) + "/"
}

// LinkDownload authorizes one file of the manifest pinned to a SENT step. The manifest was verified before the rollout
// was created; the token digest is the only per-request secret stored in D1.
func (s *Service) LinkDownload(ctx context.Context, nodeID, name, token string) (string, error) {
	if token == "" {
		return "", notFound("update asset")
	}
	manifestRaw, digest, err := s.st.DownloadStep(ctx, nodeID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", notFound("update asset")
		}
		return "", s.internal("load link update step", err)
	}
	if digest == "" {
		return "", notFound("update asset")
	}
	want, err := hex.DecodeString(digest)
	if err != nil || len(want) != sha256.Size {
		return "", notFound("update asset")
	}
	got := sha256.Sum256([]byte(token))
	if subtle.ConstantTimeCompare(want, got[:]) != 1 {
		return "", notFound("update asset")
	}
	manifest, err := release.Parse(manifestRaw)
	if err != nil || s.now().Unix() > manifest.Expires || !validReleaseTag(manifest.Version) {
		return "", notFound("update asset")
	}
	file, ok := manifest.FindName(name)
	if !ok {
		return "", notFound("update asset")
	}
	return releaseFileURL(releaseDownloadBase, manifest.Version, file.Name), nil
}

func newDownloadToken() (token, digest string, err error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(secret)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}
