package httpserver

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// The MCP endpoint exists exactly where the admin API exists: behind the secret prefix, host or separate listener. On the
// public side (the decoy, a wrong prefix, the public mounts) it is the decoy's own 404.
func TestMCPEndpointOnlyOnTheAdminSurface(t *testing.T) {
	var gotAPI http.Handler
	e := newTestEnv(t, func(c *Config) {
		c.MCP = func(api http.Handler) http.Handler {
			gotAPI = api
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, "mcp "+r.Method+" "+r.URL.Path)
			})
		}
	})
	if gotAPI == nil {
		t.Fatal("the MCP builder was not given the API")
	}
	post := func(url, host string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for name, c := range map[string]struct{ url, host string }{
		"secret prefix":  {e.public.URL + testPrefix + "mcp", ""},
		"admin host":     {e.public.URL + "/mcp", testAdminHst},
		"admin listener": {e.admin.URL + "/mcp", ""},
	} {
		if code, body := post(c.url, c.host); code != 200 || body != "mcp POST /mcp" {
			t.Errorf("%s: %d %q", name, code, body)
		}
	}
	for name, c := range map[string]struct{ url, host string }{
		"bare path on the public host": {e.public.URL + "/mcp", ""},
		"wrong prefix":                 {e.public.URL + "/wrongprefix/mcp", ""},
		"under the sub path":           {e.public.URL + "/sub/mcp", ""},
		"prefix without the slash":     {e.public.URL + strings.TrimSuffix(testPrefix, "/") + "mcp", ""},
	} {
		if code, body := post(c.url, c.host); strings.Contains(body, "mcp POST") || code == 200 && strings.Contains(body, "jsonrpc") {
			t.Errorf("%s reached the MCP endpoint: %d %q", name, code, body)
		}
	}
	// the endpoint is the API's neighbour, not part of it: /api/mcp is not it
	if code, body := post(e.admin.URL+"/api/mcp", ""); strings.Contains(body, "mcp POST") {
		t.Errorf("/api/mcp: %d %q", code, body)
	}
}

// Without a builder there is no endpoint and the path is just another SPA route.
func TestNoMCPWithoutBuilder(t *testing.T) {
	e := newTestEnv(t)
	req, _ := http.NewRequest(http.MethodPost, e.admin.URL+"/mcp", strings.NewReader("{}"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "jsonrpc") {
		t.Errorf("%d %q", resp.StatusCode, b)
	}
}
