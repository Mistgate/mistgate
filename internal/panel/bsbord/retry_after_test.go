package bsbord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An upstream that answers a retryable error with a huge retry_after (in the body or the header) holds the integration for at
// most maxRetryAfter.
func TestRetryAfterIsCapped(t *testing.T) {
	for name, tc := range map[string]struct {
		header string
		body   string
	}{
		"body number": {"", `{"error":{"code":"x","details":{"retryable":true,"retry_after":4000000000}}}`},
		"body string": {"", `{"error":{"code":"x","details":{"retryable":true,"retry_after":"9223372036854775807"}}}`},
		"header":      {"4000000000", `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.header != "" {
					w.Header().Set("Retry-After", tc.header)
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := &client{hc: srv.Client(), base: srv.URL}
			_, err := c.do(context.Background(), http.MethodGet, "/v1/account", "", "", nil)
			ae, ok := err.(*apiError)
			if !ok {
				t.Fatalf("want an API error, got %v", err)
			}
			if ae.RetryAfter != maxRetryAfter {
				t.Fatalf("RetryAfter = %s, want %s", ae.RetryAfter, maxRetryAfter)
			}
			now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
			if a := classifyError(err, settings{IntervalMin: 60}, now, false); a.hold.Sub(now) > maxRetryAfter {
				t.Fatalf("hold is %s", a.hold.Sub(now))
			}
		})
	}
	if got := retryFor(90); got != 90*time.Second {
		t.Fatalf("retryFor(90) = %s", got)
	}
}
