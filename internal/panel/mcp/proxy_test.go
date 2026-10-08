package mcp

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type proxyRun struct {
	sess   *sdk.ClientSession
	done   chan error
	stderr *lockedBuffer
	out    *lockedBuffer // everything the proxy wrote to stdout
}

// lockedBuffer can be read while the still running proxy writes to it.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuffer) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// startProxy runs RunProxy over in-memory pipes and connects the SDK's own client to its stdio side.
func startProxy(t *testing.T, url, tokenFile string) *proxyRun {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	p := &proxyRun{done: make(chan error, 1), stderr: &lockedBuffer{}, out: &lockedBuffer{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); inW.Close() })
	go func() { p.done <- RunProxy(ctx, url, tokenFile, inR, outW, p.stderr); outW.Close() }()
	tee := io.TeeReader(outR, p.out)
	c := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "1"}, nil)
	cctx, ccancel := context.WithTimeout(ctx, 10*time.Second)
	defer ccancel()
	s, err := c.Connect(cctx, &sdk.IOTransport{Reader: io.NopCloser(tee), Writer: inW}, nil)
	if err != nil {
		t.Fatalf("connect through the proxy: %v (stderr: %s)", err, p.stderr)
	}
	p.sess = s
	t.Cleanup(func() { s.Close() })
	return p
}

func writeTokenFile(t *testing.T, content string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(f, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

// The proxy against the real endpoint: the client sees the profile's tools, reads, and plans and applies, and the token
// shows up nowhere the proxy writes.
func TestProxyEndToEnd(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileOperator)
	base := strings.TrimSuffix(e.url, "mcp") // like the admin URL: ends with a slash
	p := startProxy(t, base, writeTokenFile(t, string(rune(0xFEFF))+secret+"\r\nignored second line\n"))

	names := toolNames(t, p.sess)
	if len(names) != 35 {
		t.Errorf("%d tools through the proxy: %v", len(names), names)
	}
	out := mustOK(t, p.sess, "user_get", map[string]any{"user_id": "usr_alice"})
	if !strings.Contains(out, "usr_alice") {
		t.Errorf("read: %s", out)
	}
	plan := decode[PlanOut](t, mustOK(t, p.sess, "user_enable_plan", map[string]any{"user_ids": []string{"usr_alice"}}))
	ap := decode[ApplyOut](t, mustOK(t, p.sess, "user_enable_apply", map[string]any{"confirm_token": plan.ConfirmToken}))
	if ap.Status != "applied" || len(e.w.disableReq) != 1 {
		t.Errorf("apply through the proxy: %+v", ap)
	}
	// a hidden tool is as unknown as on the endpoint itself
	if _, err := p.sess.CallTool(context.Background(), &sdk.CallToolParams{Name: "node_fix_plan", Arguments: map[string]any{}}); err == nil {
		t.Error("a hidden tool answered")
	}
	mustFail(t, p.sess, "user_get", map[string]any{"user_id": "usr_nobody"}) // a tool error passes through as one

	if strings.Contains(p.out.String(), secret) || strings.Contains(p.stderr.String(), secret) {
		t.Error("the token appears in the proxy's output")
	}
}

func TestProxyRefusedToken(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	e.revoke(secret)
	inR, inW := io.Pipe()
	var out, errb bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- RunProxy(context.Background(), strings.TrimSuffix(e.url, "mcp"), writeTokenFile(t, secret+"\n"), inR, &out, &errb)
	}()
	io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`+"\n")
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "refused the token") {
			t.Errorf("err: %v", err)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(out.String()+errb.String(), secret) {
			t.Error("the token leaked")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy kept running with a refused token")
	}
	inW.Close()
}

func TestProxyRefusesBadSetups(t *testing.T) {
	good := writeTokenFile(t, "tk1_"+strings.Repeat("Q", 43)+"\n")
	for _, c := range []struct{ name, url, file, want string }{
		{"plain http to a public host", "http://panel.example.com/secret/", good, "in the clear"},
		{"plain http, private address", "http://10.1.2.3/secret/", good, "in the clear"},
		{"credentials in the url", "https://u:p@panel.example.com/x/", good, "credentials"},
		{"a query", "https://panel.example.com/x/?a=b", good, "query"},
		{"no scheme", "panel.example.com/x/", good, "admin URL"},
		{"ftp", "ftp://panel.example.com/", good, "https"},
		{"no token file", "https://panel.example.com/x/", "", "--token-file"},
		{"missing file", "https://panel.example.com/x/", filepath.Join(t.TempDir(), "none"), "cannot open"},
		{"not a token", "https://panel.example.com/x/", writeTokenFile(t, "hunter2\n"), "does not start with an API token"},
		{"empty file", "https://panel.example.com/x/", writeTokenFile(t, ""), "does not start with an API token"},
	} {
		err := RunProxy(context.Background(), c.url, c.file, strings.NewReader(""), io.Discard, io.Discard)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// loopback over plain http is allowed (the endpoint of a local panel, and these tests)
	for in, want := range map[string]string{
		"http://127.0.0.1:8081/":         "http://127.0.0.1:8081/mcp",
		"http://localhost:8081":          "http://localhost:8081/mcp",
		"http://[::1]:8081/secret/":      "http://[::1]:8081/secret/mcp",
		"https://panel.example.com/a/b/": "https://panel.example.com/a/b/mcp",
	} {
		if got, err := endpointURL(in); err != nil || got != want {
			t.Errorf("%s -> %q, %v", in, got, err)
		}
	}
	// the token never shows in an error about its own file
	bad := writeTokenFile(t, "tk1_short\n")
	if err := RunProxy(context.Background(), "https://panel.example.com/", bad, strings.NewReader(""), io.Discard, io.Discard); err == nil || strings.Contains(err.Error(), "tk1_short") {
		t.Errorf("err: %v", err)
	}
}
