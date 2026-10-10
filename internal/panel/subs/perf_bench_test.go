package subs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/access"
)

func BenchmarkUncachedSubscriptionRender(b *testing.B) {
	token := strings.Repeat("v", 43)
	servers := make([]access.SubServer, 8)
	for i := range servers {
		servers[i] = srv("de"+string(rune('1'+i)), "DE", "Hysteria2")
	}
	source := &fakeSrc{valid: map[string]access.SubView{
		token: {Status: access.StatusActive, Servers: servers},
	}}
	h := Handler(source, decoyHandler, Config{
		Title:            "Example VPN",
		MinInterval:      -1,
		MaxPerHour:       -1,
		MissLimit:        -1,
		SharedNets:       -1,
		MaxWritesPerHour: -1,
	})
	req := httptest.NewRequest(http.MethodGet, "/"+token, nil)
	req.Header.Set("User-Agent", "Happ/3.1")
	rec := httptest.NewRecorder()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clear(rec.HeaderMap)
		rec.Body.Reset()
		rec.Code = http.StatusOK
		rec.Flushed = false
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("request %d status = %d, want %d", i, rec.Code, http.StatusOK)
		}
	}
}

func BenchmarkMihomoGzip(b *testing.B) {
	input := []byte(strings.Repeat("profile-name: de1 · Hysteria2\n", 2048))
	b.SetBytes(int64(len(input)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = gzipped(input)
	}
}
