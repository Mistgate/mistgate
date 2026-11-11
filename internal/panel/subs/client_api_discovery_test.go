package subs_test

import (
	"bytes"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

const discoveryHeader = "Mistgate-Client-Api"

// headersWithout is the header map of a response without the discovery header (a copy, to compare maps).
func headersWithout(rec *httptest.ResponseRecorder, name string) http.Header {
	h := maps.Clone(rec.Header())
	delete(h, http.CanonicalHeaderKey(name))
	return h
}

// The discovery header is the only thing the client API changes in a subscription answer: a request without caps gets exactly
// what it got before the switch existed (body and every header), switch on or off, and a request with caps and a valid token
// gets that plus the one header. Everything that is not an app's answer to a valid link stays the decoy, header and all.
func TestClientAPIDiscoveryHeaderAndLegacyAnswers(t *testing.T) {
	r := newRig(t, "/k3xq8")
	_, token := r.user("alice", nil)
	h, cache := r.handler(nil)
	formats := []struct {
		name, path, ua string
	}{
		{"base64 list", "/" + token, happUA},
		{"plain URI list", "/" + token, curlUA},
		{"Mihomo YAML", "/" + token, "mihomo/1.19"},
		{"Xray JSON", "/" + token + "?format=xray-json", happUA},
		{"sing-box JSON", "/" + token, sfaUA},
	}
	type answer struct {
		header http.Header
		body   []byte
	}
	baseline := make(map[string]answer, len(formats))
	for _, format := range formats {
		resp := fetch(h, format.path, format.ua)
		if resp.Code != 200 {
			t.Fatalf("%s baseline: %d %s", format.name, resp.Code, resp.Body.String())
		}
		if got := resp.Header().Get(discoveryHeader); got != "" {
			t.Fatalf("%s baseline carries %q", format.name, got)
		}
		baseline[format.name] = answer{maps.Clone(resp.Header()), append([]byte(nil), resp.Body.Bytes()...)}
	}
	same := func(what string, name string, resp *httptest.ResponseRecorder, extra bool) {
		t.Helper()
		want := baseline[name]
		if !bytes.Equal(resp.Body.Bytes(), want.body) {
			t.Errorf("%s %s: the body changed", name, what)
		}
		if got := headersWithout(resp, discoveryHeader); !reflect.DeepEqual(got, want.header) {
			t.Errorf("%s %s: headers changed\n got %v\nwant %v", name, what, got, want.header)
		}
		if got, want := resp.Header().Get(discoveryHeader), map[bool]string{true: "1"}[extra]; got != want {
			t.Errorf("%s %s: %s = %q, want %q", name, what, discoveryHeader, got, want)
		}
	}

	// Switch off: caps change nothing either.
	for _, format := range formats {
		same("without caps, switch off", format.name, fetch(h, format.path, format.ua), false)
		same("with caps, switch off", format.name, fetch(h, format.path, format.ua, "X-Mistgate-Caps", "v=1; hysteria2=2"), false)
	}
	if err := r.st.SetSettings(r.ctx, map[string]string{store.SettingClientAPI: "1"}); err != nil {
		t.Fatal(err)
	}
	cache.Invalidate()
	for _, format := range formats {
		same("without caps, switch on", format.name, fetch(h, format.path, format.ua), false)
		same("with caps, switch on", format.name, fetch(h, format.path, format.ua, "X-Mistgate-Caps", "v=1; hysteria2=2"), true)
	}
	if resp := fetch(h, "/"+token, happUA, "X-Mistgate-Caps", " \t "); resp.Header().Get(discoveryHeader) != "" {
		t.Errorf("empty caps header got discovery value %q", resp.Header().Get(discoveryHeader))
	}
	if resp := fetch(h, "/"+token, chrome, "X-Mistgate-Caps", "v=1"); resp.Code != 200 || resp.Header().Get("Content-Type") != "text/html; charset=utf-8" || resp.Header().Get(discoveryHeader) != "" {
		t.Errorf("user page: status %d, content type %q, header %q", resp.Code, resp.Header().Get("Content-Type"), resp.Header().Get(discoveryHeader))
	}

	// Back off: the answers are the baseline again.
	if err := r.st.SetSettings(r.ctx, map[string]string{store.SettingClientAPI: "0"}); err != nil {
		t.Fatal(err)
	}
	cache.Invalidate()
	for _, format := range formats {
		same("without caps, switch back off", format.name, fetch(h, format.path, format.ua), false)
		same("with caps, switch back off", format.name, fetch(h, format.path, format.ua, "X-Mistgate-Caps", "v=1"), false)
	}
}

// The decoy is the decoy, with or without caps: link-preview bots, rules of the owner that send a User-Agent to the decoy, and
// links nobody has. The header never reaches them and their whole answer is the one a request without caps gets.
func TestClientAPIDiscoveryNeverReachesTheDecoy(t *testing.T) {
	r := newRig(t, "/k3xq8")
	_, token := r.user("alice", nil)
	h, cache := r.handler(nil)
	if err := r.st.SetSettings(r.ctx, map[string]string{store.SettingClientAPI: "1"}); err != nil {
		t.Fatal(err)
	}
	set := subsettings.Defaults()
	set.Rules = append([]*adminv1.ServeRule{{UaContains: "curl", Format: adminv1.SubFormat_SUB_FORMAT_DECOY}}, set.Rules...)
	if _, err := cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	cache.Invalidate()
	for _, c := range []struct {
		name, path, ua string
	}{
		{"a link-preview bot on a valid link", "/" + token, "Twitterbot/1.0"},
		{"another preview bot", "/" + token, "TelegramBot (like TwitterBot)"},
		{"a User-Agent the owner's rule sends to the decoy", "/" + token, curlUA},
		{"the same with the hidden format parameter", "/" + token + "?format=xray-json", curlUA},
		{"an unknown link", "/" + unknown, happUA},
		{"an unknown link from the decoy's User-Agent", "/" + unknown, curlUA},
	} {
		plain := fetch(h, c.path, c.ua)
		caps := fetch(h, c.path, c.ua, "X-Mistgate-Caps", "v=1; hysteria2=2")
		if plain.Code != 404 || plain.Body.String() != decoyBody {
			t.Errorf("%s: the answer without caps is %d %q, not the decoy", c.name, plain.Code, plain.Body.String())
		}
		if caps.Code != plain.Code || caps.Body.String() != plain.Body.String() || !reflect.DeepEqual(caps.Header(), plain.Header()) {
			t.Errorf("%s: caps changed the answer\n got %d %v %q\nwant %d %v %q", c.name, caps.Code, caps.Header(), caps.Body.String(), plain.Code, plain.Header(), plain.Body.String())
		}
		if got := caps.Header().Get(discoveryHeader); got != "" {
			t.Errorf("%s: discovery header %q", c.name, got)
		}
	}
	// The rule is only for that User-Agent: the app next to it is still found.
	if resp := fetch(h, "/"+token, happUA, "X-Mistgate-Caps", "v=1"); resp.Code != 200 || resp.Header().Get(discoveryHeader) != "1" {
		t.Errorf("an app beside the decoy rule: %d, header %q", resp.Code, resp.Header().Get(discoveryHeader))
	}
}
