package httpserver

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

type oneToken struct{ token string }

func (o oneToken) Subscription(_ context.Context, token string) (access.SubView, error) {
	if token != o.token {
		return access.SubView{}, access.ErrUnknownToken
	}
	return access.SubView{Status: access.StatusActive, Lines: []string{"hysteria2://u@de1.example.com:443/"}}, nil
}

// The real subscription handler behind the public listener: an unknown token, a token of the wrong
// shape and a wrong method under the secret prefix answer exactly like an unknown path outside it,
// and a client that guessed too often gets the decoy for a valid token as well.
func TestSubscriptionMountIsIndistinguishableFromTheDecoy(t *testing.T) {
	valid := strings.Repeat("v", 43)
	var e *testEnv
	var once sync.Once
	mux := http.NewServeMux()
	mount := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { // e exists by the first request
			mux.Handle("GET /brand/logo.svg", NewLogoHandler(e.st, quietLog))
			mux.Handle("/", subs.Handler(oneToken{valid}, NewDecoy(""), subs.Config{Title: "t", MissLimit: 6}))
		})
		mux.ServeHTTP(w, r)
	})
	e = newTestEnvAuth(t, func(c *auth.Config) { c.TrustedProxies = loopback() }, func(c *Config) { c.PublicMounts = map[string]http.Handler{subPrefix: mount} })
	ip := xff("203.0.113.44")
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
		want := do(t, method, e.public.URL, "/"+strings.Repeat("u", 43), ip) // a token-shaped path outside the prefix
		for name, path := range map[string]string{
			"unknown token": subPrefix + strings.Repeat("u", 43),
			"short token":   subPrefix + "abc",
			"bare prefix":   subPrefix,
		} {
			sameResponse(t, method+" "+name, want, do(t, method, e.public.URL, path, ip))
		}
	}
	if r := do(t, http.MethodGet, e.public.URL, subPrefix+valid, xff("203.0.113.45")); r.status != 200 || r.header.Get("Profile-Title") == "" {
		t.Fatalf("valid token: %d %v", r.status, r.header)
	}
	// 203.0.113.44 made 9 misses above: over MissLimit 6, so even its valid token gets the decoy.
	want := do(t, http.MethodGet, e.public.URL, "/nothing", ip)
	sameResponse(t, "blocked client, valid token", want, do(t, http.MethodGet, e.public.URL, subPrefix+valid, ip))
}

// limited returns an env whose clients are told apart by X-Forwarded-For (127.0.0.1 is a trusted proxy)
// and whose three limits are the given buckets.
func limited(t *testing.T, l Limits, mutate ...func(*Config)) *testEnv {
	t.Helper()
	return newTestEnvAuth(t, func(c *auth.Config) { c.TrustedProxies = loopback() },
		append([]func(*Config){func(c *Config) { c.Limits = l }}, mutate...)...)
}

func xff(ip string) map[string]string { return map[string]string{"X-Forwarded-For": ip} }

// A client over its limit gets a 429 that looks like the decoy's 404 (same header set, styled page),
// with Retry-After; other clients are not affected; IPv6 clients share their /64.
func TestPublicListenerRateLimit(t *testing.T) {
	slow := Rate{PerSecond: 0.1, Burst: 3} // no refill worth mentioning inside a test
	e := limited(t, Limits{Public: slow, Admin: Rate{PerSecond: -1}, Agent: Rate{PerSecond: -1}})
	ref := do(t, http.MethodGet, e.public.URL, "/nothing-here", xff("198.51.100.1")) // a 404 of another client
	for i := 0; i < 2; i++ {
		if r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.9")); r.status != 200 {
			t.Fatalf("request %d inside the burst: %d", i, r.status)
		}
	}
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.9")); r.status != 200 {
		t.Fatalf("third request: %d", r.status)
	}
	r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.9"))
	if r.status != http.StatusTooManyRequests {
		t.Fatalf("over the limit: %d %q", r.status, r.body)
	}
	if secs, err := strconv.Atoi(r.header.Get("Retry-After")); err != nil || secs < 1 || secs > 20 {
		t.Errorf("Retry-After = %q", r.header.Get("Retry-After"))
	}
	if ct := r.header.Get("Content-Type"); ct != "text/html; charset=utf-8" || !strings.Contains(r.body, "429 Too Many Requests") || strings.Contains(strings.ToLower(r.body), "mistgate") {
		t.Errorf("429 page: %q %q", ct, r.body)
	}
	want := ref.header.Clone()
	want.Set("Retry-After", r.header.Get("Retry-After"))
	got := r.header.Clone()
	got.Del("Content-Length")
	want.Del("Content-Length")
	if len(got) != len(want) {
		t.Errorf("429 header set %v differs from a decoy reply's %v plus Retry-After", got, want)
	}
	// The other limits and other clients are not touched; the limit covers the mounts and unknown paths too.
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.10")); r.status != 200 {
		t.Errorf("another client: %d", r.status)
	}
	if r := do(t, http.MethodGet, e.public.URL, "/some/path", xff("203.0.113.9")); r.status != 429 {
		t.Errorf("unknown path while limited: %d", r.status)
	}
	if r := do(t, http.MethodGet, e.public.URL, subPrefix+"tok", xff("203.0.113.9")); r.status != 429 {
		t.Errorf("subscription mount while limited: %d", r.status)
	}
	// IPv6: one /64 is one client, however many addresses the client has.
	for i := 0; i < 3; i++ {
		do(t, http.MethodGet, e.public.URL, "/", xff("2001:db8:1:2::"+strconv.Itoa(i+1)))
	}
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("2001:db8:1:2:ffff::9")); r.status != 429 {
		t.Errorf("same /64, fresh address: %d", r.status)
	}
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("2001:db8:1:3::1")); r.status != 200 {
		t.Errorf("neighbouring /64: %d", r.status)
	}
}

// The 429 page follows the decoy dir's own 429.html, like the 404 follows 404.html.
func TestRateLimitPageFromDecoyDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "429.html"), []byte("<p>slow down</p>"), 0o600)
	e := limited(t, Limits{Public: Rate{PerSecond: 0.1, Burst: 1}}, func(c *Config) { c.DecoyDir = dir })
	do(t, http.MethodGet, e.public.URL, "/", nil)
	if r := do(t, http.MethodGet, e.public.URL, "/", nil); r.status != 429 || r.body != "<p>slow down</p>" {
		t.Errorf("%d %q", r.status, r.body)
	}
}

// The admin surface has its own bucket, separate from the public one (the separate admin listener draws on
// the admin bucket); exhausting one does not close the other.
func TestAdminAndPublicLimitsAreSeparate(t *testing.T) {
	e := limited(t, Limits{Public: Rate{PerSecond: 0.1, Burst: 2}, Admin: Rate{PerSecond: 0.1, Burst: 4}, Agent: Rate{PerSecond: -1}})
	me := func(base, prefix string) response {
		return postMe(t, base, prefix, xff("203.0.113.50"))
	}
	for i := 0; i < 4; i++ {
		if r := me(e.public.URL, testPrefix); !isAdminAPI(r) {
			t.Fatalf("admin request %d: %d %q", i, r.status, r.body)
		}
	}
	if r := me(e.public.URL, testPrefix); r.status != 429 {
		t.Fatalf("admin over its limit: %d", r.status)
	}
	// Same client, same bucket through the secret host...
	if r := postMe(t, e.public.URL, "/", map[string]string{"Host": testAdminHst, "X-Forwarded-For": "203.0.113.50"}); r.status != 429 {
		t.Errorf("admin by host shares the admin bucket: %d", r.status)
	}
	// ...but the decoy is still open to that client, and the admin to another client.
	if r := do(t, http.MethodGet, e.public.URL, "/", xff("203.0.113.50")); r.status != 200 {
		t.Errorf("public bucket was drained by the admin: %d", r.status)
	}
	if r := me(e.public.URL, testPrefix); r.status != 429 {
		t.Errorf("still limited: %d", r.status)
	}
	if r := postMe(t, e.public.URL, testPrefix, xff("203.0.113.51")); !isAdminAPI(r) {
		t.Errorf("another client on the admin: %d", r.status)
	}
	// The separate admin listener draws on the same admin bucket (one admin, one budget per client).
	if r := postMe(t, e.admin.URL, "/", xff("203.0.113.50")); r.status != 429 {
		t.Errorf("admin listener, same client: %d", r.status)
	}
	if r := postMe(t, e.admin.URL, "/", xff("203.0.113.52")); !isAdminAPI(r) {
		t.Errorf("admin listener, another client: %d", r.status)
	}
}

// The agent endpoint is limited per client as well, with a bucket of its own.
func TestAgentEndpointRateLimit(t *testing.T) {
	ae := newAgentEnv(t, nil, func(c *Config) {
		c.Limits = Limits{Public: Rate{PerSecond: -1}, Admin: Rate{PerSecond: -1}, Agent: Rate{PerSecond: 0.1, Burst: 2}}
	})
	clientCert := selfSigned(t, "node")
	c := tlsClient(ae.ts.Listener.Addr().String(), &clientCert)
	for i := 0; i < 2; i++ {
		if code, _, _, err := get(t, c, "https://"+testAgentSNI+"/mistgate.agent.v1.AgentService/Connect", ""); err != nil || code != 200 {
			t.Fatalf("agent call %d: %d %v", i, code, err)
		}
	}
	code, body, _, err := get(t, c, "https://"+testAgentSNI+"/mistgate.agent.v1.AgentService/Connect", "")
	if err != nil || code != 429 || !strings.Contains(body, "429 Too Many Requests") {
		t.Fatalf("agent over its limit: %d %q %v", code, body, err)
	}
	if ae.agentHits.Load() != 2 {
		t.Errorf("the handler saw %d calls, want 2", ae.agentHits.Load())
	}
}

func TestRobotsTxt(t *testing.T) {
	aiBots := []string{"GPTBot", "ChatGPT-User", "OAI-SearchBot", "ClaudeBot", "Claude-Web", "anthropic-ai", "CCBot", "Google-Extended",
		"Bytespider", "PerplexityBot", "Amazonbot", "Applebot-Extended", "meta-externalagent", "cohere-ai", "Diffbot"}
	e := newTestEnv(t)
	r := do(t, http.MethodGet, e.public.URL, "/robots.txt", nil)
	if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/plain") {
		t.Fatalf("built-in robots.txt: %d %v", r.status, r.header)
	}
	for _, ua := range aiBots {
		if !strings.Contains(r.body, "User-agent: "+ua+"\n") {
			t.Errorf("%s is not listed", ua)
		}
	}
	// The AI group disallows everything; everyone else is allowed; nothing names the panel.
	groups := strings.Split(strings.TrimSpace(r.body), "\n\n")
	if len(groups) != 2 || !strings.HasSuffix(groups[0], "Disallow: /") || groups[1] != "User-agent: *\nAllow: /" {
		t.Errorf("groups: %q", groups)
	}
	for _, secret := range []string{testSecret, subSecret, testAdminHst, "admin", "api", "sub", "mistgate"} {
		if strings.Contains(strings.ToLower(r.body), strings.ToLower(secret)) {
			t.Errorf("robots.txt mentions %q", secret)
		}
	}
	if h := do(t, http.MethodHead, e.public.URL, "/robots.txt", nil); h.status != 200 || h.body != "" {
		t.Errorf("HEAD: %d %q", h.status, h.body)
	}
	if p := do(t, http.MethodPost, e.public.URL, "/robots.txt", nil); p.status != 404 {
		t.Errorf("POST: %d", p.status)
	}

	// A decoy dir with its own robots.txt wins; one without gets the built-in file.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<p>studio</p>"), 0o600)
	e = newTestEnv(t, func(c *Config) { c.DecoyDir = dir })
	if r := do(t, http.MethodGet, e.public.URL, "/robots.txt", nil); r.status != 200 || !strings.Contains(r.body, "GPTBot") {
		t.Errorf("decoy dir without robots.txt: %d %q", r.status, r.body)
	}
	os.WriteFile(filepath.Join(dir, "robots.txt"), []byte("User-agent: *\nDisallow: /private\n"), 0o600)
	if r := do(t, http.MethodGet, e.public.URL, "/robots.txt", nil); r.status != 200 || r.body != "User-agent: *\nDisallow: /private\n" {
		t.Errorf("own robots.txt: %d %q", r.status, r.body)
	}
	// Only at the root: under a secret prefix it is an unknown path like any other.
	if r := do(t, http.MethodGet, e.public.URL, subPrefix+"robots.txt", nil); r.status != 404 {
		t.Errorf("robots.txt under the mount: %d", r.status)
	}
}

// panelMount is what cmd/mistgate mounts under the subscription prefix: the brand logo, and the rest
// to the subscription handler (here: NewDecoy, what it answers for anything that is not a token).
func panelMount(e *testEnv) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /brand/logo.svg", NewLogoHandler(e.st, quietLog))
	mux.Handle("/", NewDecoy(""))
	return mux
}

// F6: nothing under the secret prefix that is not a real resource differs from what the public listener
// says for any unknown path: status, headers, body. That includes the bare prefix, the ServeMux's own 405,
// and the logo handler's own 404 (no custom logo) and error.
func TestSecretMountAnswersLikeTheDecoy(t *testing.T) {
	var e *testEnv
	e = newTestEnv(t, func(c *Config) {
		c.PublicMounts = map[string]http.Handler{subPrefix: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panelMount(e).ServeHTTP(w, r) })}
	})
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		want := do(t, method, e.public.URL, "/no/such/page", nil)
		if want.status != 404 {
			t.Fatalf("%s reference: %d", method, want.status)
		}
		for name, path := range map[string]string{
			"bare prefix":      subPrefix,
			"unknown path":     subPrefix + "x/y",
			"short token":      subPrefix + "abc",
			"token-like":       subPrefix + strings.Repeat("a", 43),
			"logo, no custom":  subPrefix + "brand/logo.svg",
			"logo, dir":        subPrefix + "brand/",
			"logo, other case": subPrefix + "brand/LOGO.svg",
		} {
			sameResponse(t, method+" "+name, want, do(t, method, e.public.URL, path, nil))
		}
	}
}

// The decoy answers an unknown path no faster than a lookup takes: every 404 has the same floor (here
// measured from outside, the floor is a lower bound).
func TestDecoyNotFoundHasAFloor(t *testing.T) {
	e := newTestEnv(t, func(c *Config) { c.PublicMounts = map[string]http.Handler{subPrefix: NewDecoy("")} })
	for name, path := range map[string]string{"unknown path": "/zzz", "unknown token": subPrefix + strings.Repeat("a", 43), "bare prefix": subPrefix} {
		start := time.Now()
		if r := do(t, http.MethodGet, e.public.URL, path, nil); r.status != 404 {
			t.Fatalf("%s: %d", name, r.status)
		}
		if took := time.Since(start); took < notFoundFloor {
			t.Errorf("%s answered in %v, under the %v floor", name, took, notFoundFloor)
		}
	}
}
