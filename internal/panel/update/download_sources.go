package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/release"
)

const (
	updateDownloadSourcesKey = "update_download_sources"
	githubManifestFailureTTL = 10 * time.Minute
	githubManifestSuccessTTL = 10 * time.Minute
)

var defaultUpdateDownloadSources = []string{"github"}

func reportedDownloadSource(source string) string {
	switch source {
	case "panel", "github", "peer", "cloudflare":
		return source
	default:
		return ""
	}
}

func safeSourcesUsed(raw string) string {
	if raw == "" || len(raw) > 200 {
		return ""
	}
	parts := strings.Split(raw, ",")
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		if reportedDownloadSource(part) == "" || seen[part] {
			return ""
		}
		seen[part] = true
	}
	return raw
}

func safeSourcesFailed(raw string) string {
	return release.SanitizeSourceFailures(raw)
}

// updateDownloadSources returns optional sources enabled for new VPS node updates. The panel is implicit and always first.
func (s *Service) updateDownloadSources(ctx context.Context) ([]string, error) {
	raw, err := s.st.Setting(ctx, updateDownloadSourcesKey)
	if errors.Is(err, store.ErrNotFound) {
		return slices.Clone(defaultUpdateDownloadSources), nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "null" {
		s.log.Warn("load update download sources", "code", "update_download_sources_invalid")
		return nil, nil
	}
	var configured []string
	if err := json.Unmarshal([]byte(raw), &configured); err != nil {
		s.log.Warn("load update download sources", "code", "update_download_sources_invalid")
		return nil, nil
	}
	return normalizeOptionalSources(configured), nil
}

func normalizeOptionalSources(sources []string) []string {
	var out []string
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		if source != "github" || seen[source] {
			continue
		}
		seen[source] = true
		out = append(out, source)
	}
	return out
}

func (s *Service) setUpdateDownloadSources(ctx context.Context, sources []string) ([]string, error) {
	for _, source := range sources {
		if source != "github" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("only github is currently supported as an optional download source"))
		}
	}
	configured := normalizeOptionalSources(sources)
	data, err := json.Marshal(configured)
	if err != nil {
		return nil, s.internal("encode update download sources", err)
	}
	if err := s.st.SetSettings(ctx, map[string]string{updateDownloadSourcesKey: string(data)}); err != nil {
		return nil, s.internal("save update download sources", err)
	}
	s.audit(ctx, "update_download_sources", map[string]string{"sources": strings.Join(configured, ",")})
	return configured, nil
}

const updateSourcesCapability = "update-sources/1"

// sourcesForStep checks the optional GitHub source outside Service.mu. An empty result keeps the legacy panel-only path.
func (s *Service) sourcesForStep(ctx context.Context, manifestRaw []byte, supportsSources bool) []string {
	if s.cfg.Edge || !supportsSources {
		return nil
	}
	manifest, err := release.Parse(manifestRaw)
	if err != nil || !validReleaseTag(manifest.Version) {
		s.log.Info("update: GitHub download source check", "code", "update_github_source_check", "offered", false, "reason", "tag")
		return nil
	}
	configured, err := s.updateDownloadSources(ctx)
	if err != nil {
		s.log.Warn("load update download sources", "code", "update_download_sources_unreadable")
		return nil
	}
	if !slices.Contains(configured, "github") {
		return nil
	}
	available, reason, checked := s.checkGitHubManifest(ctx, manifestRaw, manifest)
	if checked {
		if available {
			s.log.Info("update: GitHub download source check", "code", "update_github_source_check", "offered", true)
		} else {
			s.log.Info("update: GitHub download source check", "code", "update_github_source_check", "offered", false, "reason", reason)
		}
	}
	if !available {
		return nil
	}
	return []string{"panel", "github"}
}

type releaseSourceCheck struct {
	key   [32]byte
	ok    bool
	until time.Time
}

// githubManifestMatches is retained as a small predicate for callers that only need the trust decision.
func (s *Service) githubManifestMatches(ctx context.Context, raw []byte, local *release.Manifest) bool {
	ok, _, _ := s.checkGitHubManifest(ctx, raw, local)
	return ok
}

// checkGitHubManifest checks the official release tag against the rollout manifest. The single cache entry is protected
// by a mutex held across the bounded request; one rollout is active at a time, so a map and per-key waiters are unnecessary.
func (s *Service) checkGitHubManifest(ctx context.Context, raw []byte, local *release.Manifest) (bool, string, bool) {
	if local == nil {
		return false, "tag", false
	}
	if !validReleaseTag(local.Version) {
		return false, "tag", false
	}
	key := sha256.Sum256(raw)
	now := s.now()
	s.sourceMu.Lock()
	defer s.sourceMu.Unlock()
	if check := s.sourceCheck; check.key == key && now.Before(check.until) {
		return check.ok, "", false
	}

	checkCtx, cancel := context.WithTimeout(ctx, s.githubSourceCheckTimeout())
	githubManifest, _, _, err := fetchReleaseManifest(checkCtx, s.cfg.Key, local.Version, "", nil, s.now())
	cancel()
	if ctx.Err() != nil {
		return false, "unreachable", false
	}
	available := err == nil && sameReleaseFiles(githubManifest.Files, local.Files)
	reason := ""
	if err != nil {
		reason = githubSourceFailureClass(err)
	} else if !available {
		reason = "files"
	}
	until := now.Add(githubManifestFailureTTL)
	if available {
		until = now.Add(githubManifestSuccessTTL)
		if expires := time.Unix(githubManifest.Expires, 0); expires.Before(until) {
			until = expires
		}
	}
	s.sourceCheck = releaseSourceCheck{key: key, ok: available, until: until}
	return available, reason, true
}

func (s *Service) githubSourceCheckTimeout() time.Duration {
	if s.cfg.GitHubSourceCheck > 0 {
		return s.cfg.GitHubSourceCheck
	}
	return 10 * time.Second
}

func githubSourceFailureClass(err error) string {
	switch {
	case errors.Is(err, release.ErrExpired):
		return "expired"
	case errors.Is(err, errFetchReleaseMetadata):
		return "unreachable"
	case errors.Is(err, errReleaseManifestTagMismatch):
		return "tag"
	default:
		return "signature"
	}
}
