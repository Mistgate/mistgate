//go:build js

package warp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"syscall/js"
	"testing"
	"time"

	"connectrpc.com/connect"
)

// These tests run only under GOOS=js (scripts/test-edge-store.ps1 with cmd/mistgate-edge/testdata/warpapi.cjs): the
// registration goes through newTransport, which is Go's fetch transport, to a fake WARP API on 127.0.0.1 served by Node.
// A request that arrives there can only have come through fetch. The real API is never contacted.

func warpAPI(t *testing.T) js.Value {
	t.Helper()
	api := js.Global().Get("__warpApi")
	if api.IsUndefined() {
		t.Skip("the fake WARP API is loaded by scripts/test-edge-store.ps1 (--require warpapi.cjs)")
	}
	return api
}

func warpAPIBase(t *testing.T, api js.Value) string {
	t.Helper()
	done := make(chan string, 1)
	ok := js.FuncOf(func(_ js.Value, args []js.Value) any { done <- args[0].String(); return nil })
	defer ok.Release()
	api.Call("start").Call("then", ok)
	select {
	case base := <-done:
		return base
	case <-time.After(10 * time.Second):
		t.Fatal("the fake WARP API did not start")
		return ""
	}
}

func fetchClient(t *testing.T, base string) *Client {
	t.Helper()
	tr, err := newTransport(tlsSpecWgcf230, nil)
	if err != nil {
		t.Fatal(err)
	}
	return NewClientFor(base, &http.Client{Transport: tr, Timeout: 10 * time.Second})
}

type seenRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

func seenBy(t *testing.T, api js.Value) []seenRequest {
	t.Helper()
	var out []seenRequest
	if err := json.Unmarshal([]byte(js.Global().Get("JSON").Call("stringify", api.Call("seen")).String()), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestFetchTransportRegisters(t *testing.T) {
	api := warpAPI(t)
	base := warpAPIBase(t, api)
	before := len(seenBy(t, api))
	api.Call("reply", 201, js.Undefined()) // the default registration answer

	p := Defaults()
	reg, err := fetchClient(t, base).Register(context.Background(), p, "cHVibGljLWtleS1vZi10aGlydHktdHdvLWJ5dGVzISE=", time.Now())
	if err != nil {
		t.Fatalf("Register through fetch: %v", err)
	}
	if reg.ID != "dev-0001" || reg.Token != "tok-0001" || reg.EndpointV4 != "162.159.192.1" || reg.AddressV4 != "172.16.0.2/32" || len(reg.Ports) != 2 {
		t.Errorf("registration = %+v", reg)
	}

	seen := seenBy(t, api)
	if len(seen) != before+1 {
		t.Fatalf("the fake API saw %d new requests, want 1", len(seen)-before)
	}
	r := seen[before]
	if r.Method != http.MethodPost || r.URL != "/"+p.APIVersion+"/reg" {
		t.Errorf("request = %s %s", r.Method, r.URL)
	}
	if r.Headers["user-agent"] != p.UserAgent || r.Headers["cf-client-version"] != p.CFClientVersion ||
		r.Headers["content-type"] != "application/json; charset=UTF-8" {
		t.Errorf("headers = %v", r.Headers)
	}
	if r.Headers["authorization"] != "" {
		t.Error("a registration carries no token")
	}
	var body struct {
		Key        string `json:"key"`
		KeyType    string `json:"key_type"`
		TunnelType string `json:"tunnel_type"`
	}
	if err := json.Unmarshal([]byte(r.Body), &body); err != nil || body.Key == "" || body.KeyType != "curve25519" || body.TunnelType != "wireguard" {
		t.Errorf("body = %q (%v)", r.Body, err)
	}
}

// What Cloudflare answers reaches the RPC as a coded message with the status, never as "internal error".
func TestFetchTransportErrorsAreHonest(t *testing.T) {
	api := warpAPI(t)
	base := warpAPIBase(t, api)
	s := &Service{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	register := func(base string) error {
		_, err := fetchClient(t, base).Register(context.Background(), Defaults(), "a2V5", time.Now())
		return s.cloudflareErr("nod_x", "register", err)
	}
	for _, c := range []struct {
		status int
		code   connect.Code
		msg    string
	}{
		{429, connect.CodeResourceExhausted, "cloudflare_rate_limited: status=429"},
		{403, connect.CodeUnavailable, "cloudflare_rejected: status=403"},
		{500, connect.CodeUnavailable, "cloudflare_rejected: status=500"},
	} {
		api.Call("reply", c.status, `{"success":false,"errors":[{"code":1015,"message":"rate limited"}]}`)
		gotCode, gotMsg := code(register(base))
		if gotCode != c.code || gotMsg != c.msg {
			t.Errorf("HTTP %d: %v %q, want %v %q", c.status, gotCode, gotMsg, c.code, c.msg)
		}
	}
	// A dead address: fetch fails, which is "unreachable", not an internal error.
	gotCode, gotMsg := code(register("http://127.0.0.1:1"))
	if gotCode != connect.CodeUnavailable || gotMsg != "cloudflare_unreachable" {
		t.Errorf("network error: %v %q", gotCode, gotMsg)
	}
	if strings.Contains(gotMsg, "127.0.0.1") {
		t.Error("the message leaks the address")
	}
}

// The default client, the one the panel builds, no longer fails on this edition (it is not called: it points at the real API).
func TestNewClientOnEdition(t *testing.T) {
	if _, err := NewClient(Defaults()); err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := newTransport("no-such-spec", nil); err == nil {
		t.Error("an unknown tls spec id must be refused as on a VPS")
	}
}
