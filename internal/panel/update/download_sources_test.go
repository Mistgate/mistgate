package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/release"
)

type releaseManifestReply struct {
	status int
	body   []byte
}

func signedSourceManifest(t *testing.T, expires int64, files ...release.File) []byte {
	t.Helper()
	m := &release.Manifest{Schema: release.Schema, Version: "v1.2.3", Built: 1_700_000_000, Expires: expires, Files: files}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func sourceManifestFiles(name string) []release.File {
	sum := sha256.Sum256([]byte("signed node binary"))
	base, goos, arch, err := release.ParseBinaryName(name)
	if err != nil || base != "mistgate-node" {
		panic("invalid source test binary name")
	}
	return []release.File{{OS: goos, Arch: arch, Name: name, Size: int64(len("signed node binary")), SHA256: hex.EncodeToString(sum[:])}}
}

func useReleaseManifestServer(t *testing.T, replies map[string]releaseManifestReply, onRequest func(*http.Request)) (*atomic.Int32, *httptest.Server) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if onRequest != nil {
			onRequest(r)
		}
		reply, ok := replies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		status := reply.status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(reply.body)
	}))
	oldBase := releaseDownloadBase
	releaseDownloadBase = server.URL + "/"
	t.Cleanup(func() {
		releaseDownloadBase = oldBase
		server.Close()
	})
	return &requests, server
}

func manifestReplies(raw []byte, signer ed25519.PrivateKey) map[string]releaseManifestReply {
	return map[string]releaseManifestReply{
		"/v1.2.3/manifest.json": {body: raw},
		"/v1.2.3/manifest.sig":  {body: release.Sign(signer, raw)},
	}
}

func newManifestSourceService(key ed25519.PublicKey, now *time.Time) *Service {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Service{cfg: Config{Key: key}, log: logger, now: func() time.Time { return *now }}
}

func TestGitHubManifestMatchRequiresTrustedTagAndSameFiles(t *testing.T) {
	panelPublic, panelPrivate, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	_, otherPrivate, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	localRaw := signedSourceManifest(t, 1_850_000_000, sourceManifestFiles("mistgate-node-linux-amd64")...)
	local, err := release.Parse(localRaw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)

	tests := []struct {
		name        string
		replies     map[string]releaseManifestReply
		want        bool
		wantFetches int32
	}{
		{
			name:        "re-signed manifest with the same files",
			replies:     manifestReplies(signedSourceManifest(t, 1_900_000_000, sourceManifestFiles("mistgate-node-linux-amd64")...), panelPrivate),
			want:        true,
			wantFetches: 2,
		},
		{
			name:        "another signing key",
			replies:     manifestReplies(signedSourceManifest(t, 1_900_000_000, sourceManifestFiles("mistgate-node-linux-amd64")...), otherPrivate),
			wantFetches: 2,
		},
		{
			name:        "missing release tag",
			replies:     map[string]releaseManifestReply{},
			wantFetches: 1,
		},
		{
			name:        "different file list",
			replies:     manifestReplies(signedSourceManifest(t, 1_900_000_000, sourceManifestFiles("mistgate-node-linux-arm64")...), panelPrivate),
			wantFetches: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requests, _ := useReleaseManifestServer(t, tc.replies, nil)
			s := newManifestSourceService(panelPublic, &now)
			if got := s.githubManifestMatches(context.Background(), localRaw, local); got != tc.want {
				t.Fatalf("githubManifestMatches = %v, want %v", got, tc.want)
			}
			if got := requests.Load(); got != tc.wantFetches {
				t.Fatalf("manifest requests = %d, want %d", got, tc.wantFetches)
			}
		})
	}
}

func TestSourcesForStepKeepsPanelFirstAndHonorsSetting(t *testing.T) {
	public, private, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	files := sourceManifestFiles("mistgate-node-linux-amd64")
	localRaw := signedSourceManifest(t, 1_850_000_000, files...)
	now := time.Unix(1_800_000_000, 0)
	useReleaseManifestServer(t, manifestReplies(signedSourceManifest(t, 1_900_000_000, files...), private), nil)

	e := newEnv(t)
	s := newManifestSourceService(public, &now)
	s.st = e.st
	if got := s.sourcesForStep(e.ctx, localRaw, true); !slices.Equal(got, []string{"panel", "github"}) {
		t.Fatalf("sourcesForStep = %v, want panel first then github", got)
	}
	if err := e.st.SetSettings(e.ctx, map[string]string{updateDownloadSourcesKey: "[]"}); err != nil {
		t.Fatal(err)
	}
	if got := s.sourcesForStep(e.ctx, localRaw, true); got != nil {
		t.Fatalf("disabled optional source = %v, want nil for legacy panel-only path", got)
	}
}

func TestRolloutSendsGitHubSourceOnlyToVerifiedCapableAgents(t *testing.T) {
	_, otherPrivate, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name         string
		capable      bool
		settingOff   bool
		badSignature bool
		wantSources  []string
		wantRequests int32
	}{
		{name: "trusted release and capable agent", capable: true, wantSources: []string{"panel", "github"}, wantRequests: 2},
		{name: "agent without source capability", wantRequests: 0},
		{name: "setting disabled", capable: true, settingOff: true, wantRequests: 0},
		{name: "GitHub manifest check fails", capable: true, badSignature: true, wantRequests: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			binary := []byte("rollout binary")
			e.bundle("v1.2.3", newBuilt+100, map[string][]byte{"mistgate-node-linux-amd64": binary})
			bundle := e.s.current()
			signer := e.priv
			if tc.badSignature {
				signer = otherPrivate
			}
			remote := signedSourceManifest(t, bundle.manifest.Expires+int64(time.Hour/time.Second), bundle.manifest.Files...)
			requests, _ := useReleaseManifestServer(t, manifestReplies(remote, signer), nil)
			if tc.settingOff {
				if err := e.st.SetSettings(e.ctx, map[string]string{updateDownloadSourcesKey: "[]"}); err != nil {
					t.Fatal(err)
				}
			}
			caps := []string{"doctor/1", capUpdate}
			if tc.capable {
				caps = append(caps, updateSourcesCapability)
			}
			nodeID := e.addNode("source-check", nodeOpts{caps: caps})
			e.startRollout(nodeID)
			e.tick()

			e.fl.mu.Lock()
			options := e.fl.updateOptions[nodeID]
			e.fl.mu.Unlock()
			if len(options) != 1 {
				t.Fatalf("UpdateAgentOptions calls = %d, want one", len(options))
			}
			if !slices.Equal(options[0].Sources, tc.wantSources) || (options[0].Sources == nil) != (tc.wantSources == nil) {
				t.Fatalf("UpdateAgentOptions.Sources = %#v, want %#v", options[0].Sources, tc.wantSources)
			}
			if got := requests.Load(); got != tc.wantRequests {
				t.Fatalf("release requests = %d, want %d", got, tc.wantRequests)
			}
		})
	}
}

func TestMalformedDownloadSourceSettingFailsClosed(t *testing.T) {
	e := newEnv(t)
	var logs bytes.Buffer
	e.s.log = slog.New(slog.NewTextHandler(&logs, nil))
	if err := e.st.SetSettings(e.ctx, map[string]string{updateDownloadSourcesKey: "{"}); err != nil {
		t.Fatal(err)
	}
	localRaw := signedSourceManifest(t, 1_850_000_000, sourceManifestFiles("mistgate-node-linux-amd64")...)
	requests, _ := useReleaseManifestServer(t, manifestReplies(signedSourceManifest(t, 1_900_000_000,
		sourceManifestFiles("mistgate-node-linux-amd64")...), e.priv), nil)
	if got := e.s.sourcesForStep(e.ctx, localRaw, true); got != nil {
		t.Fatalf("malformed stored setting offered GitHub: %v", got)
	}
	if requests.Load() != 0 {
		t.Fatalf("malformed stored setting made %d release requests, want none", requests.Load())
	}
	if !strings.Contains(logs.String(), "code=update_download_sources_invalid") {
		t.Fatalf("malformed setting warning has no code: %s", logs.String())
	}
}

func TestSourcesForStepSkipsGitHubCheckForNonReleaseTag(t *testing.T) {
	e := newEnv(t)
	local := &release.Manifest{Schema: release.Schema, Version: "0.2.0-dev", Built: 1_800_000_000,
		Expires: 1_850_000_000, Files: sourceManifestFiles("mistgate-node-linux-amd64")}
	raw, err := local.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	requests, _ := useReleaseManifestServer(t, map[string]releaseManifestReply{}, nil)
	if got := e.s.sourcesForStep(e.ctx, raw, true); got != nil {
		t.Fatalf("non-release manifest offered GitHub: %v", got)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("non-release manifest caused %d release requests, want none", got)
	}
}

func TestSourcesForStepLogsSafeCheckResultOnce(t *testing.T) {
	panelPublic, panelPrivate, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	_, otherPrivate, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	files := sourceManifestFiles("mistgate-node-linux-amd64")
	localRaw := signedSourceManifest(t, 1_850_000_000, files...)
	now := time.Unix(1_800_000_000, 0)
	t.Run("offered", func(t *testing.T) {
		e := newEnv(t)
		var logs bytes.Buffer
		e.s.log = slog.New(slog.NewTextHandler(&logs, nil))
		useReleaseManifestServer(t, manifestReplies(signedSourceManifest(t, 1_900_000_000, files...), e.priv), nil)
		if got := e.s.sourcesForStep(e.ctx, localRaw, true); !slices.Equal(got, []string{"panel", "github"}) {
			t.Fatalf("sourcesForStep = %v, want panel and GitHub", got)
		}
		if line := logs.String(); strings.Count(line, "update_github_source_check") != 1 || !strings.Contains(line, "offered=true") || strings.Contains(line, "reason=") {
			t.Fatalf("offered check log = %q", line)
		}
	})
	for _, tc := range []struct {
		name, reason string
		remote       map[string]releaseManifestReply
		localRaw     []byte
	}{
		{name: "signature", reason: "signature", remote: manifestReplies(signedSourceManifest(t, 1_900_000_000, files...), otherPrivate)},
		{name: "files", reason: "files", remote: manifestReplies(signedSourceManifest(t, 1_900_000_000, sourceManifestFiles("mistgate-node-linux-arm64")...), panelPrivate)},
		{name: "unreachable", reason: "unreachable", remote: map[string]releaseManifestReply{}},
		{name: "expired", reason: "expired", remote: manifestReplies(signedSourceManifest(t, 1_750_000_000, files...), panelPrivate)},
		{name: "tag", reason: "tag", localRaw: func() []byte {
			m := &release.Manifest{Schema: release.Schema, Version: "0.2.0-dev", Built: 1_800_000_000, Expires: 1_850_000_000, Files: files}
			b, marshalErr := m.Marshal()
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			return b
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			e := newEnv(t)
			s := newManifestSourceService(panelPublic, &now)
			s.st = e.st
			s.log = slog.New(slog.NewTextHandler(&logs, nil))
			_, server := useReleaseManifestServer(t, tc.remote, nil)
			local := localRaw
			if tc.localRaw != nil {
				local = tc.localRaw
			}
			if got := s.sourcesForStep(context.Background(), local, true); got != nil && tc.reason != "" {
				t.Fatalf("failed check offered GitHub: %v", got)
			}
			line := logs.String()
			if strings.Count(line, "update_github_source_check") != 1 || !strings.Contains(line, "reason="+tc.reason) {
				t.Fatalf("check log = %q, want one result with reason %q", line, tc.reason)
			}
			if strings.Contains(line, server.URL) || strings.Contains(line, "127.0.0.1") || strings.Contains(line, "token") {
				t.Fatalf("check log leaked a URL, host, or token: %q", line)
			}
		})
	}
}

func TestGitHubManifestCheckIsBoundedAndCached(t *testing.T) {
	public, private, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	localRaw := signedSourceManifest(t, 1_850_000_000, sourceManifestFiles("mistgate-node-linux-amd64")...)
	local, err := release.Parse(localRaw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	requests, _ := useReleaseManifestServer(t, manifestReplies(signedSourceManifest(t, 1_900_000_000,
		sourceManifestFiles("mistgate-node-linux-amd64")...), private), nil)
	s := newManifestSourceService(public, &now)
	if !s.githubManifestMatches(context.Background(), localRaw, local) {
		t.Fatal("signed manifest with matching files was not offered")
	}
	if !s.githubManifestMatches(context.Background(), localRaw, local) || requests.Load() != 2 {
		t.Fatalf("cached positive result made %d manifest requests, want 2", requests.Load())
	}

	requests, _ = useReleaseManifestServer(t, map[string]releaseManifestReply{}, nil)
	s = newManifestSourceService(public, &now)
	if s.githubManifestMatches(context.Background(), localRaw, local) || s.githubManifestMatches(context.Background(), localRaw, local) {
		t.Fatal("missing release manifest was offered")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("cached failure made %d requests, want 1", got)
	}
	now = now.Add(githubManifestFailureTTL + time.Second)
	if s.githubManifestMatches(context.Background(), localRaw, local) || requests.Load() != 2 {
		t.Fatalf("expired failure cache: requests=%d", requests.Load())
	}
}

func TestGitHubManifestCheckUsesInjectedBound(t *testing.T) {
	public, _, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	localRaw := signedSourceManifest(t, 1_850_000_000, sourceManifestFiles("mistgate-node-linux-amd64")...)
	local, err := release.Parse(localRaw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	requests, _ := useReleaseManifestServer(t, map[string]releaseManifestReply{}, func(r *http.Request) {
		if r.URL.Path == "/v1.2.3/manifest.json" {
			<-r.Context().Done()
		}
	})
	const bound = 120 * time.Millisecond
	s := newManifestSourceService(public, &now)
	s.cfg.GitHubSourceCheck = bound
	started := time.Now()
	if s.githubManifestMatches(context.Background(), localRaw, local) {
		t.Fatal("a stalled GitHub manifest was offered")
	}
	elapsed := time.Since(started)
	if elapsed < bound-50*time.Millisecond || elapsed > time.Second {
		t.Fatalf("blocked manifest check took %s, want about %s", elapsed, bound)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("stalled manifest check made %d requests, want 1", got)
	}
}

func TestUpdateDownloadSourcesSettingAndEdgeAvailability(t *testing.T) {
	e := newEnv(t)
	if got, err := e.s.updateDownloadSources(e.ctx); err != nil || !slices.Equal(got, []string{"github"}) {
		t.Fatalf("default sources = %v, %v", got, err)
	}
	stepUps := 0
	e.s.cfg.StepUp = func(context.Context) error { stepUps++; return nil }
	resp, err := (rpc{e.s}).SetUpdateDownloadSources(e.ctx,
		connect.NewRequest(&adminv1.SetUpdateDownloadSourcesRequest{Sources: []string{"github", "github"}}))
	if err != nil || !slices.Equal(resp.Msg.GetSources(), []string{"github"}) {
		t.Fatalf("set sources = %+v, %v", resp, err)
	}
	if stepUps != 1 {
		t.Fatalf("step-up calls = %d, want 1", stepUps)
	}
	var action, params string
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT action, params FROM audit ORDER BY rowid DESC LIMIT 1`).Scan(&action, &params); err != nil {
		t.Fatal(err)
	}
	if action != "update_download_sources" || !strings.Contains(params, `"sources":"github"`) {
		t.Fatalf("source audit = %q %q", action, params)
	}
	if _, err := (rpc{e.s}).SetUpdateDownloadSources(e.ctx,
		connect.NewRequest(&adminv1.SetUpdateDownloadSourcesRequest{Sources: []string{"peer"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unsupported source error = %v", err)
	}

	edge := e.newEdgeService(e.pub, "0.1.0-edge-test")
	if _, err := (rpc{edge}).SetUpdateDownloadSources(e.ctx,
		connect.NewRequest(&adminv1.SetUpdateDownloadSourcesRequest{Sources: []string{"github"}})); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("edge source RPC error = %v", err)
	}
	edgeResponse, edgeErr := (rpc{edge}).GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
	if edgeErr != nil || edgeResponse == nil || len(edgeResponse.Msg.GetUpdateDownloadSources()) != 0 {
		t.Fatalf("edge exposed optional source settings: response=%+v error=%v", edgeResponse, edgeErr)
	}
}

func TestSourceResultParamsAreAllowlisted(t *testing.T) {
	if got := safeSourcesUsed("panel,github"); got != "panel,github" {
		t.Fatalf("safeSourcesUsed = %q", got)
	}
	for _, raw := range []string{"https://example.com/x", "panel,github,unknown"} {
		if got := safeSourcesUsed(raw); got != "" {
			t.Errorf("safeSourcesUsed(%q) = %q", raw, got)
		}
	}
	if got := safeSourcesFailed("panel:slow,github:http_404"); got != "panel:slow,github:http_404" {
		t.Fatalf("safeSourcesFailed = %q", got)
	}
	if got := safeSourcesFailed("github:request"); got != "github:request" {
		t.Fatalf("request failure code was dropped: %q", got)
	}
	if got := safeSourcesFailed("panel:stalled,evil:<x>,github:HTTP"); got != "panel:stalled" {
		t.Fatalf("safeSourcesFailed must drop invalid items individually, got %q", got)
	}
	for _, raw := range []string{"github:https://example.com", "github:HTTP"} {
		if got := safeSourcesFailed(raw); got != "" {
			t.Errorf("safeSourcesFailed(%q) = %q", raw, got)
		}
	}
}

func TestDownloadSourceEventContainsOnlySafeSourceNames(t *testing.T) {
	e := newEnv(t)
	nodeID := e.addNode("de1", nodeOpts{})
	p := &pass{s: e.s, ctx: e.ctx, ro: store.RolloutRow{ToVersion: "v1.2.3"}, now: e.clk.Now()}
	p.recordDownloadSourceEvent(&store.StepRow{NodeID: nodeID}, &agentv1.CommandResult{Params: map[string]string{"sources_used": "panel,github"}})
	event := e.wantEvent(nodeID, "update_download_source")
	if event.Severity != 1 || event.Source != "panel" || event.Params["to_version"] != "v1.2.3" || event.Params["sources_used"] != "panel,github" {
		t.Fatalf("download source event = %+v", event)
	}
	p.recordDownloadSourceEvent(&store.StepRow{NodeID: nodeID}, &agentv1.CommandResult{Params: map[string]string{"sources_used": "https://example.com/token"}})
	rows, _, err := e.st.Events(e.ctx, store.EventFilter{NodeID: nodeID, Limit: 50})
	if err != nil || len(rows) != 1 {
		t.Fatalf("invalid source result added an event: rows=%+v err=%v", rows, err)
	}
}

func TestApplyUpdateAnswerSanitizesFailureSourcesAndRecordsFallbackEvent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failed     string
		wantFailed string
	}{
		{name: "valid source failures", failed: "panel:slow,github:http_404", wantFailed: "panel:slow,github:http_404"},
		{name: "garbage source failures", failed: "evil:<x>,github:HTTP"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.defaultBundle()
			nodeID := e.addNode("source-failure", nodeOpts{})
			e.fl.onUpdate = func(string) (*agentv1.CommandResult, error) {
				return &agentv1.CommandResult{Error: "download_failed", Params: map[string]string{
					"sources_failed": tc.failed,
					"sources_used":   "panel,github",
				}}, nil
			}
			e.startRollout(nodeID)
			e.tick()
			step := e.wantStep(nodeID, store.StepFailed, "download_failed")
			if got := step.Params["sources_failed"]; got != tc.wantFailed {
				t.Fatalf("step sources_failed = %q, want %q (params=%v)", got, tc.wantFailed, step.Params)
			}
			event := e.wantEvent(nodeID, "update_download_source")
			if event.Params["sources_used"] != "panel,github" || event.Params["to_version"] != "0.2.0-new" {
				t.Fatalf("fallback event = %+v", event)
			}
		})
	}
}
