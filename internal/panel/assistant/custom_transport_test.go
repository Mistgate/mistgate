//go:build !js

package assistant

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestCustomProviderAddressPolicy(t *testing.T) {
	for _, tc := range []struct {
		ip                  string
		httpOnly, local, ok bool
	}{
		{"8.8.8.8", false, false, true},
		{"8.8.8.8", true, true, false},
		{"100.100.100.200", false, false, false},
		{"100.100.100.200", false, true, true},
		{"100.64.0.1", false, false, false},
		{"100.64.0.1", false, true, true},
		{"198.18.0.1", false, false, false},
		{"192.0.0.8", false, false, false},
		{"240.0.0.1", false, false, false},
		{"0.0.0.1", false, false, false},
		{"127.0.0.1", true, true, true},
		{"192.168.1.2", true, true, true},
		{"127.0.0.1", false, false, false},
		{"169.254.169.254", false, true, false},
		{"::ffff:169.254.169.254", false, true, false},
		{"::ffff:100.64.0.1", false, true, true},
		{"::808:808", false, false, true},
		{"::a00:1", false, false, false},
		{"::a00:1", false, true, true},
		{"64:ff9b::a9fe:a9fe", false, false, false},
		{"2002:a9fe:a9fe::1", false, false, false},
		{"fe80::1", false, true, false},
		{"::1", true, true, true},
	} {
		if got := allowedProviderIP(netip.MustParseAddr(tc.ip), tc.httpOnly, tc.local); got != tc.ok {
			t.Errorf("%s: got %v, want %v", tc.ip, got, tc.ok)
		}
	}
}

func TestCustomProviderDoesNotFollowRedirectToAnotherHost(t *testing.T) {
	contacted := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	h, err := customProviderHTTP(source.URL, true)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h.request(context.Background(), http.MethodGet, "models", "secret", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if contacted || resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect followed: contacted=%v status=%d", contacted, resp.StatusCode)
	}
}

func TestCustomProviderEndpointPolicy(t *testing.T) {
	for _, tc := range []struct {
		url   string
		local bool
		ok    bool
	}{
		{"https://example.com/v1", false, true},
		{"http://127.0.0.1:11434/v1", false, false},
		{"http://127.0.0.1:11434/v1", true, true},
		{"https://user:secret@example.com/v1", false, false},
		{"https://example.com/v1?key=secret", false, false},
		{"file:///etc/passwd", true, false},
	} {
		_, err := customProviderHTTP(tc.url, tc.local)
		if (err == nil) != tc.ok {
			t.Errorf("%q: error = %v", tc.url, err)
		}
	}
}
