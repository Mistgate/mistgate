package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkPublicRefusedRequest(b *testing.B) {
	e := newTestEnv(b, func(c *Config) {
		c.Limits = Limits{
			Public: Rate{PerSecond: 1e-9, Burst: 1},
			Admin:  Rate{PerSecond: -1},
			Agent:  Rate{PerSecond: -1},
		}
	})
	h := e.srv.Public()
	req := httptest.NewRequest(http.MethodGet, "http://example.test/nope", nil)
	req.RemoteAddr = "203.0.113.7:1234"
	prime := httptest.NewRecorder()
	h.ServeHTTP(prime, req) // spend the one initial token
	if prime.Code != http.StatusNotFound {
		b.Fatalf("initial request status = %d, want %d", prime.Code, http.StatusNotFound)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusTooManyRequests {
			b.Fatalf("request %d status = %d, want %d", i, rec.Code, http.StatusTooManyRequests)
		}
	}
}
