package httpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// unauthBody is the session middleware's answer.
const unauthBody = "{\"code\":\"unauthenticated\",\"message\":\"not signed in\"}"

const (
	dummyPath   = "/mistgate.admin.v1.DummyService/"
	subSecret   = "fakesubsecret23456723456" // 24 chars
	subPrefix   = "/" + subSecret + "/"
	otherSecret = "fakeothersecret234567234"
)

// dummyAdmin is an admin service added through Config.AdminHandlers; it reports who the
// session middleware said the caller is and counts how often it is reached.
func dummyAdmin(hits *atomic.Int32) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		a, ok := auth.AdminFrom(r.Context())
		fmt.Fprintf(w, "%v|%s|%s", ok, a.DisplayName, r.URL.Path)
	})
}

func withDummy(hits *atomic.Int32) func(*Config) {
	return func(c *Config) { c.AdminHandlers = []AdminHandler{{dummyPath, dummyAdmin(hits)}} }
}

// allProcedures lists every admin RPC the generated code knows, whether or not a handler
// for it is mounted.
func allProcedures(t *testing.T) []string {
	t.Helper()
	var out []string
	protoregistry.GlobalFiles.RangeFilesByPackage("mistgate.admin.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			for j := 0; j < svc.Methods().Len(); j++ {
				out = append(out, "/"+string(svc.FullName())+"/"+string(svc.Methods().Get(j).Name()))
			}
		}
		return true
	})
	if len(out) < 20 {
		t.Fatalf("only %d procedures registered", len(out))
	}
	return out
}

func rawDo(t *testing.T, method, url, contentType, cookie string, body string) response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return response{resp.StatusCode, resp.Header, string(b)}
}

// The session check sits in an HTTP middleware over everything under api/: only the
// listed public procedures pass without a session, for every service, every RPC kind
// and even paths nothing is mounted at.
func TestSessionMiddlewareProtectsEverythingButPublicProcedures(t *testing.T) {
	var hits atomic.Int32
	e := newTestEnv(t, withDummy(&hits))
	public := map[string]bool{}
	for _, p := range []string{"GetLoginInfo", "BeginSetup", "FinishSetup", "BeginLogin", "FinishLogin", "PasswordLogin", "Logout"} {
		public["/mistgate.admin.v1.AuthService/"+p] = true
	}
	for _, base := range []string{e.admin.URL + "/api", e.public.URL + testPrefix + "api"} {
		for _, proc := range append(allProcedures(t), dummyPath+"Anything", dummyPath+"Stream", "/mistgate.admin.v1.FutureService/Call", "/mistgate.admin.v1.AuthService/NoSuchMethod", "/nonsense", "/") {
			for _, ct := range []string{"", "application/json", "application/proto", "application/connect+json", "application/connect+proto", "application/grpc", "application/grpc-web+proto"} {
				if public[proc] {
					continue
				}
				r := rawDo(t, http.MethodPost, base+proc, ct, "", "{}")
				if r.status != http.StatusUnauthorized || r.body != unauthBody || r.header.Get("Content-Type") != "application/json" {
					t.Errorf("POST %s (%s) without a session: %d %q", proc, ct, r.status, r.body)
				}
			}
			if r := rawDo(t, http.MethodGet, base+proc, "", "", ""); !public[proc] && r.status != http.StatusUnauthorized {
				t.Errorf("GET %s without a session: %d", proc, r.status)
			}
		}
	}
	// The public procedures are reached. They may answer 401 themselves for bad credentials
	// but never with the middleware's message. One call each: they are rate limited.
	for proc := range public {
		r := rawDo(t, http.MethodPost, e.admin.URL+"/api"+proc, "application/json", "", "{}")
		if r.status == http.StatusNotFound || strings.Contains(r.body, "not signed in") {
			t.Errorf("public %s was blocked: %d %q", proc, r.status, r.body)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("a service handler ran without a session")
	}
	// Bad and forged cookies are no session.
	for _, c := range []string{auth.CookieName + "=", auth.CookieName + "=forged", "sid=1"} {
		if r := rawDo(t, http.MethodPost, e.admin.URL+"/api"+dummyPath+"X", "application/json", c, "{}"); r.status != 401 {
			t.Errorf("cookie %q: %d", c, r.status)
		}
	}

	// With a session the request reaches the service with the admin in its context.
	b := newBrowser(t, e.admin.URL+"/", "")
	if err := b.register(e.setupToken(t, time.Now()), "Ada"); err != nil {
		t.Fatal(err)
	}
	cookie := b.jar.cookie
	if r := rawDo(t, http.MethodPost, e.admin.URL+"/api"+dummyPath+"Foo", "application/connect+json", cookie, "{}"); r.status != 200 || r.body != "true|Ada|"+dummyPath+"Foo" {
		t.Errorf("with a session: %d %q", r.status, r.body)
	}
	// Unknown services and methods are a 404 once signed in, never the app shell.
	for _, p := range []string{"/mistgate.admin.v1.FutureService/Call", "/nonsense", "/"} {
		if r := rawDo(t, http.MethodPost, e.admin.URL+"/api"+p, "application/json", cookie, "{}"); r.status != 404 || strings.Contains(r.body, "<base") {
			t.Errorf("unknown %s: %d %q", p, r.status, r.body)
		}
	}
	if hits.Load() != 1 {
		t.Errorf("service hit %d times", hits.Load())
	}
}

func TestAdminHandlerConfigIsValidated(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, _ := auth.New(st, auth.Config{RPID: testRPID, Origins: []string{testOrigin}}, quietLog)
	ok := http.NotFoundHandler()
	for name, hs := range map[string][]AdminHandler{
		"no slash at the end":     {{"/svc.X", ok}},
		"no slash at the start":   {{"svc.X/", ok}},
		"empty":                   {{"", ok}},
		"nil handler":             {{dummyPath, nil}},
		"duplicate":               {{dummyPath, ok}, {dummyPath, ok}},
		"collides with auth":      {{adminv1connect.AuthServiceName, ok}},
		"collides with auth path": {{"/mistgate.admin.v1.AuthService/", ok}},
		"collides with instance":  {{"/mistgate.admin.v1.InstanceService/", ok}},
	} {
		if _, err := New(Config{AdminHandlers: hs, Dist: fstest.MapFS{}, Log: quietLog}, a, st); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := New(Config{AdminHandlers: []AdminHandler{{dummyPath, ok}, {"/mistgate.admin.v1.OtherService/", ok}}, Dist: fstest.MapFS{}, Log: quietLog}, a, st); err != nil {
		t.Errorf("valid handlers refused: %v", err)
	}
}

// A redirect from the admin would drop the secret prefix (the mux under StripPrefix
// redirects "/x" to "/x/"): the admin never answers with one, in any mode.
func TestAdminNeverRedirects(t *testing.T) {
	var hits atomic.Int32
	e := newTestEnv(t, withDummy(&hits))
	paths := []string{
		"api", "api/", "api/mistgate.admin.v1.AuthService", "api/mistgate.admin.v1.AuthService/", "api/mistgate.admin.v1.InstanceService",
		"api" + dummyPath[:len(dummyPath)-1], "api/mistgate.admin.v1.AuthService/Me/", "api/x", "brand", "brand/", "brand/logo.svg/", "assets",
		"assets/", "login/", "setup/", "nodes/abc/", "index.html", "index.html/",
	}
	for mode, tc := range map[string]struct {
		base string
		host string
	}{
		"prefix":   {e.public.URL + testPrefix, ""},
		"host":     {e.public.URL + "/", testAdminHst},
		"listener": {e.admin.URL + "/", ""},
	} {
		for _, p := range paths {
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodHead} {
				hdr := map[string]string{}
				if tc.host != "" {
					hdr["Host"] = tc.host
				}
				r := do(t, method, tc.base, p, hdr)
				if r.status >= 300 && r.status < 400 || r.header.Get("Location") != "" {
					t.Errorf("%s %s%s: %d Location %q", method, mode, p, r.status, r.header.Get("Location"))
				}
			}
		}
	}
}

func TestPublicMounts(t *testing.T) {
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "sub|%s|%s|%s", r.Method, r.URL.Path, r.URL.RawQuery)
	})
	e := newTestEnv(t, func(c *Config) { c.PublicMounts = map[string]http.Handler{subPrefix: echo} })
	base := e.public.URL
	random := do(t, http.MethodGet, base, "/nothing-here", nil)

	for p, want := range map[string]string{
		subSecret + "/":               "sub|GET|/|",
		subSecret + "/abc":            "sub|GET|/abc|",
		subSecret + "/abc/def?x=1":    "sub|GET|/abc/def|x=1",
		subSecret + "/brand/logo.svg": "sub|GET|/brand/logo.svg|",
	} {
		if r := do(t, http.MethodGet, base, "/"+p, nil); r.status != 200 || r.body != want {
			t.Errorf("/%s: %d %q, want %q", p, r.status, r.body, want)
		}
	}
	if r := do(t, http.MethodPost, base, "/"+subSecret+"/x", nil); r.body != "sub|POST|/x|" {
		t.Errorf("POST: %q", r.body)
	}
	// Anything that is not exactly the prefix is the decoy.
	for name, p := range map[string]string{
		"prefix without the slash": "/" + subSecret,
		"one char short":           "/" + subSecret[:23] + "/x",
		"one char more":            "/" + subSecret + "x/x",
		"upper case":               "/" + strings.ToUpper(subSecret) + "/x",
		"other secret":             "/" + otherSecret + "/x",
		"as a suffix":              "/x/" + subSecret + "/x",
		"double slash":             "//" + subSecret + "/x",
		"dot segment":              "/./" + subSecret + "/x",
		"encoded slash":            "/" + subSecret + "%2fx",
		"encoded dot-dot":          "/%2e%2e/" + subSecret + "/x",
	} {
		sameResponse(t, name, do(t, http.MethodGet, base, p, nil), random)
	}
	// It is a public-listener feature only, and the admin prefix still works beside it.
	if r := do(t, http.MethodGet, e.admin.URL, "/"+subSecret+"/abc", nil); strings.HasPrefix(r.body, "sub|") {
		t.Error("mount reachable on the admin listener")
	}
	if r := postMe(t, base, testPrefix, nil); !isAdminAPI(r) {
		t.Errorf("admin next to a mount: %+v", r)
	}
	if r := do(t, http.MethodGet, base, "/"+subSecret+"/x", map[string]string{"Host": testAdminHst}); r.body == "sub|GET|/x|" {
		t.Error("the secret host must serve the admin, not the mount")
	}
}

func TestPublicMountConfigIsValidated(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, _ := auth.New(st, auth.Config{RPID: testRPID, Origins: []string{testOrigin}}, quietLog)
	h := http.NotFoundHandler()
	try := func(adminPrefix string, mounts map[string]http.Handler) error {
		_, err := New(Config{AdminPrefix: adminPrefix, PublicMounts: mounts, Dist: fstest.MapFS{}, Log: quietLog}, a, st)
		return err
	}
	for name, tc := range map[string]struct {
		admin  string
		mounts map[string]http.Handler
	}{
		"no slashes":                  {"", map[string]http.Handler{"secret": h}},
		"no trailing slash":           {"", map[string]http.Handler{"/secret": h}},
		"too short":                   {"", map[string]http.Handler{"/a/": h}},
		"nil handler":                 {"", map[string]http.Handler{subPrefix: nil}},
		"same as the admin prefix":    {subPrefix, map[string]http.Handler{subPrefix: h}},
		"inside the admin prefix":     {"/abcd/", map[string]http.Handler{"/abcd/efgh/": h}},
		"admin prefix inside a mount": {"/abcd/efgh/", map[string]http.Handler{"/abcd/": h}},
		"mounts nested":               {"", map[string]http.Handler{"/abcd/": h, "/abcd/efgh/": h}},
	} {
		if err := try(tc.admin, tc.mounts); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if err := try(testPrefix, map[string]http.Handler{subPrefix: h, "/" + otherSecret + "/": h}); err != nil {
		t.Errorf("valid mounts refused: %v", err)
	}
}
