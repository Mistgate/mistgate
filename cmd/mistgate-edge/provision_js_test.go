//go:build js && wasm

package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall/js"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/buildinfo"
)

func jsFunction(body string) js.Value { return js.Global().Get("Function").New(body) }

func jsObject(fields map[string]any) js.Value {
	out := js.Global().Get("Object").New()
	for k, v := range fields {
		out.Set(k, v)
	}
	return out
}

func TestParseProvisionOptions(t *testing.T) {
	fn := jsFunction("return Promise.resolve()")
	options := func(provision any) js.Value {
		o := js.Global().Get("Object").New()
		if provision != nil {
			o.Set("provision", provision)
		}
		return o
	}
	for _, tc := range []struct {
		name    string
		options js.Value
		serve   bool
		kick    bool
		bad     bool
	}{
		{name: "absent: a forwarder with no kick", options: options(nil)},
		{name: "undefined", options: options(js.Undefined())},
		{name: "null", options: options(js.Null())},
		{name: "empty object", options: options(jsObject(nil))},
		{name: "forward with a kick", options: options(jsObject(map[string]any{"role": "forward", "kick": fn})), kick: true},
		{name: "forward without a role name", options: options(jsObject(map[string]any{"kick": fn})), kick: true},
		{name: "forward must not have sshConnect", options: options(jsObject(map[string]any{"role": "forward", "sshConnect": fn})), bad: true},
		{name: "serve", options: options(jsObject(map[string]any{"role": "serve", "kick": fn, "sshConnect": fn})), serve: true, kick: true},
		{name: "serve needs sshConnect", options: options(jsObject(map[string]any{"role": "serve", "kick": fn})), bad: true},
		{name: "serve needs kick", options: options(jsObject(map[string]any{"role": "serve", "sshConnect": fn})), bad: true},
		{name: "unknown role", options: options(jsObject(map[string]any{"role": "worker"})), bad: true},
		{name: "role is not a string", options: options(jsObject(map[string]any{"role": 7})), bad: true},
		{name: "kick is not a function", options: options(jsObject(map[string]any{"kick": "yes"})), bad: true},
		{name: "provision is not an object", options: options("serve"), bad: true},
	} {
		got, err := parseProvisionOptions(tc.options)
		if tc.bad {
			if !errors.Is(err, errInvalidProvision) {
				t.Errorf("%s: %v, want errInvalidProvision", tc.name, err)
			}
			continue
		}
		if err != nil || got.serve != tc.serve || (got.kick.Type() == js.TypeFunction) != tc.kick {
			t.Errorf("%s: %+v, %v", tc.name, got, err)
		}
	}
}

func TestWakeWaitsForTheKickAndReportsItsFailure(t *testing.T) {
	ctx := context.Background()
	waits := jsFunction("globalThis.__kicks = (globalThis.__kicks || 0) + 1; return new Promise((r) => setTimeout(r, 50))")
	start := time.Now()
	if err := (provisionOptions{kick: waits}).wake(ctx); err != nil || time.Since(start) < 40*time.Millisecond {
		t.Fatalf("a kick that takes 50 ms: %v after %v", err, time.Since(start))
	}
	if js.Global().Get("__kicks").Int() != 1 {
		t.Fatalf("kick ran %d times", js.Global().Get("__kicks").Int())
	}
	for name, kick := range map[string]js.Value{
		"rejects": jsFunction("return Promise.reject(new Error('no object'))"),
		"throws":  jsFunction("throw new Error('no binding')"),
	} {
		if err := (provisionOptions{kick: kick}).wake(ctx); err == nil {
			t.Errorf("a kick that %s reported no error", name)
		}
	}
	if err := (provisionOptions{kick: jsFunction("return 1")}).wake(ctx); err != nil {
		t.Errorf("a kick that is not async: %v", err)
	}
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err := (provisionOptions{kick: jsFunction("return new Promise(() => {})")}).wake(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a kick that never answers: %v, want the context's deadline", err)
	}
}

func TestRequireBuild(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "served") })
	handler := requireBuild(next)
	for name, build := range map[string]*string{"no header": nil, "an older build": ptr("0.0.1-old"), "an empty one": ptr(""), "a longer one": ptr(buildinfo.Version + "x")} {
		request := httptest.NewRequest(http.MethodPost, "https://example.com/admin/api/x", nil)
		if build != nil {
			request.Header.Set(buildHeader, *build)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "panel_updating") || recorder.Header().Get("Retry-After") == "" {
			t.Errorf("%s: %d %q", name, recorder.Code, recorder.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "https://example.com/admin/api/x", nil)
	request.Header.Set(buildHeader, buildinfo.Version)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "served" {
		t.Errorf("this build: %d %q", recorder.Code, recorder.Body.String())
	}
}

func ptr(s string) *string { return &s }

func TestProvisionPassNeedsTheServeRole(t *testing.T) {
	stateMu.Lock()
	previous := state
	state = nil
	stateMu.Unlock()
	t.Cleanup(func() { stateMu.Lock(); state = previous; stateMu.Unlock() })

	if _, err := provisionPass(); !errors.Is(err, errNotInitialized) {
		t.Fatalf("before init: %v", err)
	}
	stateMu.Lock()
	state = &edgeState{afterResponse: newEdgeTaskRunner()}
	stateMu.Unlock()
	if _, err := provisionPass(); !errors.Is(err, errNotProvisioner) {
		t.Fatalf("a forwarder: %v, want errNotProvisioner", err)
	}
}

// The Provisioner never makes an instance: on an empty database it stops and stores nothing, whether or not the options
// carry a public URL.
func TestProvisionerRefusesAnEmptyDatabase(t *testing.T) {
	st := openFakeD1(t)
	fn := jsFunction("return Promise.resolve()")
	for _, publicURL := range []string{"", "https://example.com"} {
		options := jsObject(map[string]any{
			"d1": js.Global().Get("__d1"), "masterKey": strings.Repeat("ab", 32),
			"adminPrefix": "/test-admin/",
			"provision":   jsObject(map[string]any{"role": "serve", "kick": fn, "sshConnect": fn}),
		})
		if publicURL != "" {
			options.Set("publicURL", publicURL)
		}
		if err := initPanel(options); !errors.Is(err, errProvisionerNoPanel) {
			t.Fatalf("publicURL %q: initPanel = %v, want errProvisionerNoPanel", publicURL, err)
		}
		if values, err := st.SettingValues(context.Background(), edgeInstanceKeys); err != nil || len(values) != 0 {
			t.Fatalf("publicURL %q: the Provisioner stored %v (%v)", publicURL, values, err)
		}
	}
}
