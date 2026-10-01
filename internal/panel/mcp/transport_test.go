package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func rawPost(t *testing.T, url, secret, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestEndpointRefusals(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)

	if resp, _ := rawPost(t, e.url, "", initBody, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	if resp, _ := rawPost(t, e.url, "tk1_"+strings.Repeat("Z", 43), initBody, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown token: %d", resp.StatusCode)
	}
	// no session, no standalone stream: GET and DELETE are 405 (stateless)
	for _, m := range []string{http.MethodGet, http.MethodDelete} {
		req, _ := http.NewRequest(m, e.url, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("Accept", "text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d", m, resp.StatusCode)
		}
	}
	// a body over 256 KiB
	resp, _ := rawPost(t, e.url, secret, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"fleet_status","arguments":{"x":"`+strings.Repeat("a", maxBodyBytes)+`"}}}`, nil)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("big body: %d", resp.StatusCode)
	}
	// the server hands out no session id, and does not insist on a loopback Host header (a proxy forwards the public one)
	resp, body := rawPost(t, e.url, secret, initBody, map[string]string{"Mcp-Protocol-Version": "2025-06-18"})
	req, _ := http.NewRequest(http.MethodPost, e.url, strings.NewReader(initBody))
	req.Host = "panel.example.com"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+secret)
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Mcp-Session-Id") != "" || !strings.Contains(body, `"tools"`) || r2.StatusCode != http.StatusOK {
		t.Errorf("initialize: %d session=%q host-status=%d body=%s", resp.StatusCode, resp.Header.Get("Mcp-Session-Id"), r2.StatusCode, body)
	}
	if !strings.Contains(body, "untrusted") {
		t.Errorf("the server instructions do not warn about untrusted data: %s", body)
	}
}

// Without the bearer layer in front, the handler refuses everything.
func TestHandlerWithoutPrincipalRefuses(t *testing.T) {
	e := newTestEnv(t)
	h, err := New(Config{Plans: e.plans, Auth: e.auth, API: e.w.api(e.auth)})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initBody))
	req.Header.Set("Authorization", "Bearer x")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("got %d", rec.Code)
	}
	if _, err := New(Config{}); err == nil {
		t.Error("an empty config was accepted")
	}
}

// At most four tool calls of one token at a time.
func TestConcurrencyLimitPerToken(t *testing.T) {
	e := newTestEnv(t)
	e.w.block = make(chan struct{})
	e.w.entered = make(chan struct{}, 10)
	_, secret := e.token(ProfileReadonly)
	_, other := e.token(ProfileReadonly)
	call := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"users_search","arguments":{}}}`
	done := make(chan int, maxConcurrent)
	for i := 0; i < maxConcurrent; i++ {
		go func() {
			resp, _ := rawPost(t, e.url, secret, call, nil)
			done <- resp.StatusCode
		}()
	}
	for i := 0; i < maxConcurrent; i++ {
		select {
		case <-e.w.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("calls did not start")
		}
	}
	resp, _ := rawPost(t, e.url, secret, call, nil)
	if resp.StatusCode != http.StatusTooManyRequests || resp.Header.Get("Retry-After") == "" {
		t.Errorf("fifth call: %d", resp.StatusCode)
	}
	// another token is not held up by it
	go func() { rawPost(t, e.url, other, call, nil) }()
	select {
	case <-e.w.entered:
	case <-time.After(5 * time.Second):
		t.Error("another token was blocked")
	}
	close(e.w.block)
	for i := 0; i < maxConcurrent; i++ {
		if code := <-done; code != http.StatusOK {
			t.Errorf("blocked call finished with %d", code)
		}
	}
}
