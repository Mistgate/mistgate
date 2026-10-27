//go:build !js

package app

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A node's download request carries its token in the Authorization header. Whatever the panel answers, no logger of the
// request path (httpserver, fleet, the updates service: one logger in the app) may write the token or the header value.
// The broken store makes the updates service fail for real and log, so the check does not pass on a silent path.
func TestEdgeLinkDownloadNeverLogsTheBearerToken(t *testing.T) {
	const token = "tok-7f3a91c25be84d10a6e2c4f8"
	var logs bytes.Buffer
	remote := &edgeTickRemote{}
	c := roleConfig(t, remote)
	c.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	link, err := BuildLink(c)
	if err != nil {
		t.Fatal(err)
	}
	get := func() int {
		req := httptest.NewRequest(http.MethodGet, "https://example.com"+roleLinkPrefix+"update/nod_x/mistgate-node-linux-amd64", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		link.Handler.ServeHTTP(rec, req)
		return rec.Code
	}
	assertClean := func(when string) {
		t.Helper()
		out := logs.String()
		if strings.Contains(out, token) || strings.Contains(strings.ToLower(out), "bearer") {
			t.Fatalf("%s: the download credential reached the logs:\n%s", when, out)
		}
	}

	if code := get(); code != http.StatusNotFound { // no step for the node: the 404 the decoy shows
		t.Fatalf("download for a node without an update step = %d, want 404", code)
	}
	assertClean("unknown node")

	if _, err := c.Store.W.ExecContext(t.Context(), `DROP TABLE update_step`); err != nil {
		t.Fatal(err)
	}
	logs.Reset()
	if code := get(); code != http.StatusServiceUnavailable {
		t.Fatalf("download with a broken store = %d, want 503", code)
	}
	if !strings.Contains(logs.String(), "load link update step") {
		t.Fatalf("the store failure was not logged, so the check proves nothing: %q", logs.String())
	}
	assertClean("broken store")
}
