package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	starsInterval    = time.Hour
	starsTimeout     = 15 * time.Second
	starsMaxResponse = 1 << 20
)

var githubRepoPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// RepoStars polls the GitHub API for one public repository's star count.
type RepoStars struct {
	mu    sync.RWMutex
	stars int
	ok    bool

	base     string
	owner    string
	repo     string
	client   *http.Client
	interval time.Duration
	log      *slog.Logger
}

// NewRepoStars returns nil unless sourceURL is exactly a GitHub repository page.
func NewRepoStars(sourceURL string, log *slog.Logger) *RepoStars {
	u, err := url.Parse(sourceURL)
	if err != nil || !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Host, "github.com") || u.User != nil ||
		u.Opaque != "" || strings.ContainsAny(sourceURL, "?#") {
		return nil
	}
	path := u.EscapedPath()
	if strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}
	if !strings.HasPrefix(path, "/") {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 2 || !githubRepoPart.MatchString(parts[0]) {
		return nil
	}
	repo := strings.TrimSuffix(parts[1], ".git")
	if repo == "" || repo == "." || repo == ".." || !githubRepoPart.MatchString(repo) {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &RepoStars{
		base: "https://api.github.com", owner: parts[0], repo: repo,
		client:   &http.Client{Timeout: starsTimeout, CheckRedirect: githubReleaseRedirect},
		interval: starsInterval, log: log,
	}
}

// Stars returns the last good count. It is false until the first successful read.
func (r *RepoStars) Stars() (int, bool) {
	if r == nil {
		return 0, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stars, r.ok
}

// Run reads immediately, then once per interval until ctx is done.
func (r *RepoStars) Run(ctx context.Context) {
	if r == nil {
		return
	}
	r.read(ctx)
	interval := r.interval
	if interval <= 0 {
		interval = starsInterval
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.read(ctx)
		}
	}
}

func (r *RepoStars) read(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/repos/"+r.owner+"/"+r.repo, nil)
	if err != nil {
		r.log.Debug("read GitHub repository stars", "err", err)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Mistgate-panel")
	resp, err := r.client.Do(req)
	if err != nil {
		r.log.Debug("read GitHub repository stars", "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.log.Debug("read GitHub repository stars", "status", resp.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, starsMaxResponse+1))
	if err != nil {
		r.log.Debug("read GitHub repository stars", "err", err)
		return
	}
	if len(body) > starsMaxResponse {
		r.log.Debug("read GitHub repository stars", "err", errors.New("response exceeds size limit"))
		return
	}
	var payload struct {
		Count *int `json:"stargazers_count"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		r.log.Debug("read GitHub repository stars", "err", err)
		return
	}
	if payload.Count == nil || *payload.Count < 0 || uint64(*payload.Count) > math.MaxUint32 {
		r.log.Debug("read GitHub repository stars", "err", errors.New("invalid stargazers_count"))
		return
	}
	r.mu.Lock()
	r.stars, r.ok = *payload.Count, true
	r.mu.Unlock()
}
