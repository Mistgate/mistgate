package update

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/release"
)

type sourceTestSource struct {
	fetchers []Fetcher
	now      time.Time
	mu       sync.Mutex
	progress []*pb.UpdateProgress
}

func (s *sourceTestSource) Fetchers(*release.Manifest, release.File) []Fetcher { return s.fetchers }
func (*sourceTestSource) Fetch(context.Context, string, uint64, io.Writer) (uint64, error) {
	return 0, errors.New("unexpected legacy fetch")
}
func (*sourceTestSource) Failed() []string      { return nil }
func (s *sourceTestSource) Now() time.Time      { return s.now }
func (s *sourceTestSource) LocalNow() time.Time { return s.now }
func (s *sourceTestSource) Progress(p *pb.UpdateProgress) {
	s.mu.Lock()
	s.progress = append(s.progress, p)
	s.mu.Unlock()
}
func (s *sourceTestSource) frames() []*pb.UpdateProgress {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*pb.UpdateProgress(nil), s.progress...)
}

func applySources(t *testing.T, e *env, u *Updater, content []byte, kinds []string, src *sourceTestSource) (*pb.CommandResult, Finish) {
	t.Helper()
	src.now = e.now
	manifest, signature := e.manifest(content, newBuilt, nil)
	req := e.req(manifest, signature)
	req.Sources = kinds
	return u.Apply(context.Background(), req, src)
}

func sourceUpdater(e *env, mut func(*Config)) *Updater {
	return e.updater(func(c *Config) {
		c.SourceBackoff = []time.Duration{}
		if mut != nil {
			mut(c)
		}
	})
}

func TestSourceModeUsesTheLegacyTimeoutDefault(t *testing.T) {
	e := newEnv(t)
	u := e.updater(nil)
	if u.cfg.Timeout != 10*time.Minute {
		t.Fatalf("timeout = %s, want legacy default", u.cfg.Timeout)
	}
}

func TestApplySourcesResumesAtPanelOffsetAfterStall(t *testing.T) {
	e := newEnv(t)
	content := bytes.Repeat([]byte("signed bytes "), 90000)
	const prefix = 1 << 20
	var githubOffset uint64
	src := &sourceTestSource{fetchers: []Fetcher{
		{Kind: "panel", Fetch: func(ctx context.Context, offset uint64, w io.Writer) (uint64, error) {
			if offset != 0 {
				return uint64(len(content)), errors.New("unexpected panel resume")
			}
			if _, err := w.Write(content[:prefix]); err != nil {
				return uint64(len(content)), err
			}
			<-ctx.Done()
			return uint64(len(content)), ctx.Err()
		}},
		{Kind: "github", Fetch: func(_ context.Context, offset uint64, w io.Writer) (uint64, error) {
			githubOffset = offset
			_, err := w.Write(content[offset:])
			return uint64(len(content)), err
		}},
	}}
	u := sourceUpdater(e, func(c *Config) {
		c.Timeout = 2 * time.Second
		c.SourceStallTimeout = 30 * time.Millisecond
		c.SourceSlowAfter = time.Hour
	})
	res, finish := applySources(t, e, u, content, []string{"panel", "github"}, src)
	if !res.Ok || finish == nil {
		t.Fatalf("Apply = %+v, finish %v", res, finish != nil)
	}
	if githubOffset != prefix {
		t.Errorf("github offset = %d, want %d", githubOffset, prefix)
	}
	if got := res.Params["sources_used"]; got != "panel,github" {
		t.Errorf("sources_used = %q", got)
	}
	if !bytes.Equal(readFile(t, e.exe), content) {
		t.Error("installed file differs from the signed bytes")
	}
	var last uint64
	sawPanel, sawGitHub := false, false
	for _, frame := range src.frames() {
		if frame.BytesDownloaded < last {
			t.Fatalf("progress decreased from %d to %d at source %q", last, frame.BytesDownloaded, frame.Source)
		}
		last = frame.BytesDownloaded
		if frame.Source == "panel" {
			sawPanel = true
		}
		if frame.Source == "github" {
			sawGitHub = true
		}
	}
	if !sawPanel || !sawGitHub {
		t.Errorf("progress did not report both sources: %+v", src.frames())
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySourcesBusyIsImmediateAndDoesNotConsumeRetryBudget(t *testing.T) {
	e := newEnv(t)
	content := []byte("signed source bytes")
	panelCalls, githubCalls := 0, 0
	src := &sourceTestSource{fetchers: []Fetcher{
		{Kind: "panel", Fetch: func(_ context.Context, _ uint64, w io.Writer) (uint64, error) {
			panelCalls++
			if panelCalls <= 3 {
				return 0, BusySourceFailure("busy", errors.New("panel busy"))
			}
			_, err := w.Write(content)
			return uint64(len(content)), err
		}},
		{Kind: "github", Fetch: func(context.Context, uint64, io.Writer) (uint64, error) {
			githubCalls++
			return 0, PermanentSourceFailure("http_404", errors.New("asset missing"))
		}},
	}}
	u := sourceUpdater(e, nil)
	res, finish := applySources(t, e, u, content, []string{"panel", "github"}, src)
	if !res.Ok || finish == nil || panelCalls != 4 || githubCalls != 1 {
		t.Fatalf("Apply = %+v; calls panel=%d github=%d", res, panelCalls, githubCalls)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySourcesMarksSlowAndReturnsToFastestWhenAllAreSlow(t *testing.T) {
	e := newEnv(t)
	content := bytes.Repeat([]byte("x"), 64)
	panelCalls, githubCalls := 0, 0
	trickle := func(calls *int, firstBytes uint64) func(context.Context, uint64, io.Writer) (uint64, error) {
		return func(ctx context.Context, offset uint64, w io.Writer) (uint64, error) {
			*calls++
			if *calls == 1 {
				if _, err := w.Write(content[offset : offset+firstBytes]); err != nil {
					return uint64(len(content)), err
				}
				<-ctx.Done()
				return uint64(len(content)), ctx.Err()
			}
			_, err := w.Write(content[offset:])
			return uint64(len(content)), err
		}
	}
	src := &sourceTestSource{fetchers: []Fetcher{
		{Kind: "panel", Fetch: trickle(&panelCalls, 3)},
		{Kind: "github", Fetch: trickle(&githubCalls, 1)},
	}}
	u := sourceUpdater(e, func(c *Config) {
		c.Timeout = 2 * time.Second
		c.SourceStallTimeout = 300 * time.Millisecond
		c.SourceSlowAfter = 60 * time.Millisecond
		c.SourceSlowWindow = 60 * time.Millisecond
		c.SourceSlowInterval = 20 * time.Millisecond
		c.SourceSlowBytes = 10
	})
	res, finish := applySources(t, e, u, content, []string{"panel", "github"}, src)
	if !res.Ok || finish == nil || panelCalls != 2 || githubCalls != 1 {
		t.Fatalf("Apply = %+v; calls panel=%d github=%d", res, panelCalls, githubCalls)
	}
	if got := res.Params["sources_used"]; got != "panel,github" {
		t.Errorf("sources_used = %q", got)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySourcesRechecksSlowSourceAfterOtherSourceFails(t *testing.T) {
	e := newEnv(t)
	content := bytes.Repeat([]byte("x"), 1<<20)
	panelCalls, githubCalls := 0, 0
	transferSlowly := func(ctx context.Context, offset uint64, w io.Writer) error {
		for offset < uint64(len(content)) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			// Twenty bytes per millisecond approximates the reported 20 KiB/s panel trickle.
			end := min(offset+20, uint64(len(content)))
			if _, err := w.Write(content[offset:end]); err != nil {
				return err
			}
			offset = end
			time.Sleep(time.Millisecond)
		}
		return nil
	}
	src := &sourceTestSource{fetchers: []Fetcher{
		{Kind: "panel", Fetch: func(ctx context.Context, offset uint64, w io.Writer) (uint64, error) {
			panelCalls++
			if err := transferSlowly(ctx, offset, w); err != nil {
				return uint64(len(content)), err
			}
			return uint64(len(content)), nil
		}},
		{Kind: "github", Fetch: func(_ context.Context, offset uint64, w io.Writer) (uint64, error) {
			githubCalls++
			if githubCalls == 1 {
				return 0, TransientSourceFailure("reset", errors.New("connection reset"))
			}
			_, err := w.Write(content[offset:])
			return uint64(len(content)), err
		}},
	}}
	u := sourceUpdater(e, func(c *Config) {
		c.Timeout = 250 * time.Millisecond
		c.SourceBackoff = []time.Duration{time.Millisecond}
		c.SourceStallTimeout = 500 * time.Millisecond
		c.SourceSlowAfter = 20 * time.Millisecond
		c.SourceSlowWindow = 20 * time.Millisecond
		c.SourceSlowInterval = 2 * time.Millisecond
		c.SourceSlowBytes = 1024
	})
	res, finish := applySources(t, e, u, content, []string{"panel", "github"}, src)
	if !res.Ok || finish == nil || panelCalls != 2 || githubCalls != 2 {
		t.Fatalf("Apply = %+v; calls panel=%d github=%d", res, panelCalls, githubCalls)
	}
	if got := res.Params["sources_used"]; got != "panel,github" {
		t.Errorf("sources_used = %q", got)
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
}

func TestApplySourcesDropsWrongTotalAndOverflow(t *testing.T) {
	content := []byte("signed file")
	for _, tc := range []struct {
		name string
		bad  func(io.Writer) error
	}{
		{"wrong total", func(w io.Writer) error { _, err := w.Write(content[:4]); return err }},
		{"overflow", func(w io.Writer) error { _, err := w.Write(append(bytes.Clone(content), '!')); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			var githubOffset uint64
			src := &sourceTestSource{fetchers: []Fetcher{
				{Kind: "panel", Fetch: func(_ context.Context, _ uint64, w io.Writer) (uint64, error) {
					if err := tc.bad(w); err != nil {
						return uint64(len(content)), err
					}
					if tc.name == "wrong total" {
						return uint64(len(content) + 1), nil
					}
					return uint64(len(content)), nil
				}},
				{Kind: "github", Fetch: func(_ context.Context, offset uint64, w io.Writer) (uint64, error) {
					githubOffset = offset
					_, err := w.Write(content[offset:])
					return uint64(len(content)), err
				}},
			}}
			u := sourceUpdater(e, nil)
			res, finish := applySources(t, e, u, content, []string{"panel", "github"}, src)
			if !res.Ok || finish == nil {
				t.Fatalf("Apply = %+v", res)
			}
			wantOffset := uint64(0)
			if tc.name == "wrong total" {
				wantOffset = 4
			}
			if githubOffset != wantOffset {
				t.Errorf("github offset = %d, want %d", githubOffset, wantOffset)
			}
			if err := finish(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestApplySourcesFailureParamsContainOnlyShortCodes(t *testing.T) {
	e := newEnv(t)
	content := []byte("signed bytes")
	src := &sourceTestSource{fetchers: []Fetcher{
		{Kind: "panel", Fetch: func(context.Context, uint64, io.Writer) (uint64, error) {
			return 0, PermanentSourceFailure("http_404", errors.New("file missing"))
		}},
		{Kind: "github", Fetch: func(context.Context, uint64, io.Writer) (uint64, error) {
			return 0, TransientSourceFailure("refused", errors.New("connection refused"))
		}},
	}}
	u := sourceUpdater(e, nil)
	res, finish := applySources(t, e, u, content, []string{"panel", "github"}, src)
	if finish != nil || res.Ok || res.Error != "download_failed" {
		t.Fatalf("Apply = %+v, finish %v", res, finish != nil)
	}
	if got := res.Params["sources_failed"]; got != "panel:http_404,github:refused" {
		t.Errorf("sources_failed = %q", got)
	}
	if got := res.Params["sources_used"]; got != "" {
		t.Errorf("sources_used = %q, want empty", got)
	}
	if strings.Contains(res.Detail, "connection refused") {
		t.Errorf("detail exposed source error text: %q", res.Detail)
	}
}

func TestApplySourcesWithNoSupportedKindDoesNotFallBack(t *testing.T) {
	e := newEnv(t)
	content := []byte("signed bytes")
	src := &sourceTestSource{}
	u := sourceUpdater(e, nil)
	res, finish := applySources(t, e, u, content, []string{"peer"}, src)
	if finish != nil || res.Ok || res.Error != "download_failed" || res.Params["sources_used"] != "" {
		t.Fatalf("Apply = %+v, finish %v", res, finish != nil)
	}
	if exists(e.exe + ".new") {
		t.Error("temporary update file was left behind")
	}
}

func TestApplySourcesWindowExpiryStopsCurrentFetcher(t *testing.T) {
	e := newEnv(t)
	content := []byte("signed file")
	called := 0
	src := &sourceTestSource{fetchers: []Fetcher{
		{Kind: "panel", Fetch: func(ctx context.Context, _ uint64, _ io.Writer) (uint64, error) {
			called++
			<-ctx.Done()
			return 0, ctx.Err()
		}},
		{Kind: "github", Fetch: func(context.Context, uint64, io.Writer) (uint64, error) {
			t.Error("window expiry should not start the next source")
			return 0, nil
		}},
	}}
	u := sourceUpdater(e, func(c *Config) {
		c.Timeout = 30 * time.Millisecond
		c.SourceStallTimeout = time.Second
		c.SourceSlowAfter = time.Hour
	})
	res, finish := applySources(t, e, u, content, []string{"panel", "github"}, src)
	if finish != nil || res.Ok || res.Error != "download_failed" || called != 1 {
		t.Fatalf("Apply = %+v, finish %v, calls=%d", res, finish != nil, called)
	}
	if got := res.Params["sources_failed"]; got != "panel:timeout" {
		t.Errorf("sources_failed = %q", got)
	}
}

func TestApplySourcesHashMismatchRemovesNewFile(t *testing.T) {
	e := newEnv(t)
	content := []byte("signed bytes")
	lying := bytes.Repeat([]byte("x"), len(content))
	src := &sourceTestSource{fetchers: []Fetcher{{Kind: "panel", Fetch: func(_ context.Context, _ uint64, w io.Writer) (uint64, error) {
		_, err := w.Write(lying)
		return uint64(len(content)), err
	}}}}
	u := sourceUpdater(e, nil)
	res, finish := applySources(t, e, u, content, []string{"panel"}, src)
	if finish != nil || res.Ok || res.Error != "hash_mismatch" {
		t.Fatalf("Apply = %+v, finish %v", res, finish != nil)
	}
	if !bytes.Equal(readFile(t, e.exe), oldBin) || exists(e.exe+".new") {
		t.Error("hash mismatch changed the installed binary or left .new behind")
	}
}

func TestApplySingleSourceKeepsSizeMismatchCode(t *testing.T) {
	e := newEnv(t)
	content := []byte("signed bytes")
	src := &sourceTestSource{fetchers: []Fetcher{{Kind: "panel", Fetch: func(context.Context, uint64, io.Writer) (uint64, error) {
		return uint64(len(content) + 1), nil
	}}}}
	u := sourceUpdater(e, nil)
	res, finish := applySources(t, e, u, content, []string{"panel"}, src)
	if finish != nil || res.Ok || res.Error != "size_mismatch" {
		t.Fatalf("Apply = %+v, finish %v", res, finish != nil)
	}
	if got := res.Params["sources_failed"]; got != "panel:size" {
		t.Errorf("sources_failed = %q, want panel:size", got)
	}
}
