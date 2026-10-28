package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewRepoStarsAcceptsOnlyGitHubRepositoryPages(t *testing.T) {
	for _, sourceURL := range []string{
		"http://github.com/example/panel",
		"https://gitlab.com/example/panel",
		"https://github.com/example",
		"https://github.com/example/panel/tree/main",
		"https://api.github.com/repos/example/panel",
		"",
	} {
		if got := NewRepoStars(sourceURL, nil); got != nil {
			t.Errorf("NewRepoStars(%q) = non-nil, want nil", sourceURL)
		}
	}
	for _, sourceURL := range []string{
		"https://github.com/Mistgate/mistgate",
		"https://github.com/Mistgate/mistgate/",
		"https://github.com/Mistgate/mistgate.git",
		"https://GITHUB.com/Mistgate/mistgate",
	} {
		if got := NewRepoStars(sourceURL, nil); got == nil {
			t.Errorf("NewRepoStars(%q) = nil, want non-nil", sourceURL)
		}
	}
}

func TestRepoStarsReadsCountAndSendsGitHubHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/Mistgate/mistgate" {
			t.Errorf("request path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "Mistgate-panel" {
			t.Errorf("User-Agent = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"stargazers_count":1234}`)
	}))
	defer server.Close()

	r := NewRepoStars("https://github.com/Mistgate/mistgate", nil)
	r.base = server.URL
	if got, ok := r.Stars(); got != 0 || ok {
		t.Fatalf("initial Stars() = (%d, %t), want (0, false)", got, ok)
	}
	r.read(context.Background())
	if got, ok := r.Stars(); got != 1234 || !ok {
		t.Fatalf("Stars() = (%d, %t), want (1234, true)", got, ok)
	}
	var nilStars *RepoStars
	if got, ok := nilStars.Stars(); got != 0 || ok {
		t.Fatalf("nil Stars() = (%d, %t), want (0, false)", got, ok)
	}
}

func TestRepoStarsFailuresKeepLastGoodCount(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			_, _ = fmt.Fprint(w, `{"stargazers_count":823}`)
		case 2:
			http.Error(w, "unavailable", http.StatusInternalServerError)
		default:
			_, _ = fmt.Fprint(w, `{"stargazers_count":"bad"}`)
		}
	}))
	defer server.Close()

	r := NewRepoStars("https://github.com/example/panel", nil)
	r.base = server.URL
	for i := 0; i < 3; i++ {
		r.read(context.Background())
		if got, ok := r.Stars(); got != 823 || !ok {
			t.Fatalf("Stars() after read %d = (%d, %t), want (823, true)", i+1, got, ok)
		}
	}
}

func TestRepoStarsRunReadsOncePerIntervalAndStops(t *testing.T) {
	requests := make(chan struct{}, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests <- struct{}{}
		_, _ = fmt.Fprint(w, `{"stargazers_count":1}`)
	}))
	defer server.Close()

	r := NewRepoStars("https://github.com/example/panel", nil)
	r.base = server.URL
	r.interval = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		r.Run(ctx)
		close(done)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-requests:
		case <-time.After(2 * time.Second):
			cancel()
			<-done
			t.Fatalf("Run made only %d requests", i)
		}
	}
	select {
	case <-requests:
		cancel()
		<-done
		t.Fatal("Run made another request before the next interval")
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if got := len(requests); got != 0 {
		t.Fatalf("requests after Run stopped = %d", got)
	}
}
