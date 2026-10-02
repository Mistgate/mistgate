package httpserver

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
)

func TestAdminPageRequiresOwnerSessionAndPreservesPrefix(t *testing.T) {
	page := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin, ok := auth.AdminFrom(r.Context())
		if !ok {
			http.Error(w, "missing admin context", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "install page for %s", admin.Role)
	})
	e := newTestEnv(t, func(c *Config) {
		c.AdminPages = []AdminPage{{Path: "/nodes/install", Handler: page}}
	})
	path := testPrefix + "nodes/install"
	if got := do(t, http.MethodGet, e.public.URL, path, nil); got.status != http.StatusUnauthorized {
		t.Fatalf("page without a session: %d %q", got.status, got.body)
	}

	browser := newBrowser(t, e.admin.URL+"/", "")
	if err := browser.register(e.setupToken(t, time.Now()), "Owner"); err != nil {
		t.Fatal(err)
	}
	if browser.jar.cookie == "" {
		t.Fatal("login did not create a session cookie")
	}

	ownerHeaders := map[string]string{"Cookie": browser.jar.cookie}
	for _, route := range []string{path, path + "/"} {
		got := do(t, http.MethodGet, e.public.URL, route, ownerHeaders)
		if got.status != http.StatusOK || got.body != "install page for owner" {
			t.Fatalf("owner page %q: %d %q", route, got.status, got.body)
		}
		if got.header.Get("Cache-Control") != "no-store" || got.header.Get("Content-Security-Policy") == "" {
			t.Errorf("page %q security headers: %v", route, got.header)
		}
	}

	// A regular HTML form from another origin cannot use the owner's session cookie.
	req, err := http.NewRequest(http.MethodPost, e.public.URL+path, strings.NewReader("action=fingerprint"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cookie", browser.jar.cookie)
	req.Header.Set("Origin", "https://attacker.example")
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin form returned %d, want 403", resp.StatusCode)
	}

}
