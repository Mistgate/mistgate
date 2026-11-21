package webproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestBootstrapReportsBridgeAndSessionStages(t *testing.T) {
	capability := strings.Repeat("A", 43)
	for _, tc := range []struct {
		name  string
		check func(*http.Request, int) *http.Response
		stage BootstrapStage
		calls int
	}{
		{
			name: "site instead of bridge",
			check: func(_ *http.Request, _ int) *http.Response {
				return testClientResponse(http.StatusOK, nil, "site")
			},
			stage: BootstrapStageBridge,
			calls: 1,
		},
		{
			name: "invalid session response",
			check: func(_ *http.Request, call int) *http.Response {
				header := make(http.Header)
				if call == 1 {
					header.Set(headerBootstrap, capability)
				}
				return testClientResponse(http.StatusOK, header, "")
			},
			stage: BootstrapStageSession,
			calls: 2,
		},
		{
			name: "bridge redirect is not followed",
			check: func(req *http.Request, _ int) *http.Response {
				return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{"https://other.example.com/"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}
			},
			stage: BootstrapStageBridge,
			calls: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			client, err := NewClient("https://tg.example.com", capability, roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return tc.check(req, calls), nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			err = client.Bootstrap(context.Background())
			var bootstrapErr *BootstrapError
			if !errors.As(err, &bootstrapErr) || bootstrapErr.Stage != tc.stage {
				t.Fatalf("Bootstrap error = %v, want stage %q", err, tc.stage)
			}
			if calls != tc.calls {
				t.Fatalf("round trip calls = %d, want %d", calls, tc.calls)
			}
			if strings.Contains(err.Error(), "bridge=") {
				t.Fatalf("bootstrap error exposed its request URL: %v", err)
			}
		})
	}
}

func testClientResponse(status int, header http.Header, body string) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body))}
}
