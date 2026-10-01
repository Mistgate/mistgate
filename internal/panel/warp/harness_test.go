package warp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// fakeCF is a stand-in for the Cloudflare client API: it checks the request the way the real one is picky about
// (path version, user agent, key, terms timestamp) and hands out registrations whose secrets all contain "SECRET",
// so a test can look for them everywhere. The real API is never contacted by any test.
type fakeCF struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	version    string // the path version it accepts
	regs       map[string]*fakeReg
	calls      []string // "POST /reg", "GET /reg/<id>", "DELETE /reg/<id>"
	posts      []http.Header
	bodies     []map[string]any
	n          int
	rate429    int    // the next registrations answer 429 this many times
	endpoint   string // what peers[0].endpoint.v4 says
	onRegister func() // runs inside a POST, before the answer
	issued     []string
}

type fakeReg struct {
	id, token, license string
	deleted            bool
	endpoint           string
}

func newFakeCF(t *testing.T) *fakeCF {
	f := &fakeCF{t: t, version: "v0a5641", regs: map[string]*fakeReg{}, endpoint: "162.159.192.1:0"}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeCF) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := strings.TrimPrefix(r.URL.Path, "/"+f.version)
	if !strings.HasPrefix(r.URL.Path, "/"+f.version+"/") {
		http.Error(w, `{"success":false,"errors":[{"code":1006,"message":"Registration not found"}]}`, http.StatusNotFound)
		return
	}
	f.calls = append(f.calls, r.Method+" "+path)
	switch {
	case r.Method == http.MethodPost && path == "/reg":
		f.posts = append(f.posts, r.Header.Clone())
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.bodies = append(f.bodies, body)
		if f.rate429 > 0 {
			f.rate429--
			http.Error(w, `{"success":false,"errors":[{"code":10000,"message":"Too many requests"}]}`, http.StatusTooManyRequests)
			return
		}
		if f.onRegister != nil {
			f.mu.Unlock()
			f.onRegister()
			f.mu.Lock()
		}
		f.n++
		reg := &fakeReg{id: fmt.Sprintf("dev-SECRET-%d", f.n), token: fmt.Sprintf("tok-SECRET-%d", f.n), license: fmt.Sprintf("LIC-SECRET-%d", f.n), endpoint: f.endpoint}
		f.regs[reg.id] = reg
		f.issued = append(f.issued, reg.id, reg.token, reg.license)
		f.answer(w, reg, true)
	case strings.HasPrefix(path, "/reg/"):
		id := strings.TrimPrefix(path, "/reg/")
		reg := f.regs[id]
		if reg == nil || reg.deleted {
			http.Error(w, `{"success":false,"errors":[{"code":1006,"message":"Registration not found"}]}`, http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+reg.token {
			http.Error(w, `{"success":false,"errors":[{"code":1001,"message":"Unauthorized"}]}`, http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodDelete {
			reg.deleted = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		f.answer(w, reg, false)
	default:
		http.NotFound(w, r)
	}
}

// answer is the device JSON. Like the real API, only the registration answer carries "token" (checked live,
// 2026-10-01: GET /reg/{id} has none), so a refresh must not need it.
func (f *fakeCF) answer(w http.ResponseWriter, reg *fakeReg, withToken bool) {
	w.Header().Set("Content-Type", "application/json")
	token := ""
	if withToken {
		token = fmt.Sprintf(`"token":%q,`, reg.token)
	}
	_, _ = fmt.Fprintf(w, `{"id":%q,%s"account":{"id":"acc","account_type":"free","license":%q},
	"config":{"client_id":"AbCd","interface":{"addresses":{"v4":"172.16.0.2","v6":"2001:db8:110::2"}},
	"peers":[{"public_key":"bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=","endpoint":{"host":"engage.cloudflareclient.com:2408","v4":%q,"v6":"[2606:4700:d0::a29f:c001]:0","ports":[2408,500,1701,4500]}}]}}`,
		reg.id, token, reg.license, reg.endpoint)
}

func (f *fakeCF) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeCF) count(prefix string) int {
	n := 0
	for _, c := range f.callLog() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeCF) reg(i int) *fakeReg {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.regs[fmt.Sprintf("dev-SECRET-%d", i)]
}

// env is a Service on a real store with a fake Cloudflare, a fake clock and a captured log.
type env struct {
	t      *testing.T
	ctx    context.Context
	st     *store.Store
	cf     *fakeCF
	s      *Service
	log    *bytes.Buffer
	now    time.Time
	sleeps []time.Duration
	stepUp func(context.Context) error
	steps  int
	kicks  int
	onKick func() // runs at every StateChanged (a test plays the node applying the new state)
	online map[string]int
	live   map[string]bool
	dns    map[string][]netip.Addr
	secret []string // secrets the test knows besides what the fake issued (private keys)
	paused bool     // the node confirmed a state with WARP paused (WarpPauseApplied); under mu
	mu     sync.Mutex
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	v, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, st: st, cf: newFakeCF(t), log: &bytes.Buffer{}, now: time.Unix(1_791_100_000, 0),
		live: map[string]bool{}, dns: map[string][]netip.Addr{}}
	e.stepUp = func(context.Context) error { e.steps++; return nil }
	e.s, err = New(st, v, (*envFleet)(e), Config{
		StepUp: func(ctx context.Context) error { return e.stepUp(ctx) },
		Actor:  func(context.Context) string { return "adm_test" },
		NewClient: func(Params) (*Client, error) {
			return NewClientFor(e.cf.srv.URL, e.cf.srv.Client()), nil
		},
		Resolver: func(_ context.Context, host string) ([]netip.Addr, error) {
			if a, ok := e.dns[host]; ok {
				return a, nil
			}
			return nil, fmt.Errorf("no such host")
		},
		Backoff: []time.Duration{time.Second, 4 * time.Second},
		Pause:   func() time.Duration { return 3 * time.Second },
		Sleep: func(_ context.Context, d time.Duration) error {
			e.mu.Lock()
			e.sleeps = append(e.sleeps, d)
			e.mu.Unlock()
			return nil
		},
		Now: func() time.Time { return e.now },
		Log: slog.New(slog.NewTextHandler(e.log, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// envFleet is the fake fleet: live nodes and a count of StateChanged calls.
type envFleet env

func (f *envFleet) Live(id string) (bool, []string, bool) { return f.live[id], nil, false }
func (f *envFleet) OnlineByInbound() map[string]int       { return f.online }
func (f *envFleet) WarpPauseApplied(context.Context, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.paused
}
func (f *envFleet) StateChanged() {
	f.kicks++
	if f.onKick != nil {
		f.onKick()
	}
}

// addNode inserts an active node whose last Hello listed caps (default warp/1).
func (e *env) addNode(name string, caps ...string) string {
	e.t.Helper()
	if caps == nil {
		caps = []string{"doctor/1", CapWarp}
	}
	id := "nod_" + name
	if _, err := e.st.W.ExecContext(e.ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', 1)`, id, name, name+".example.com"); err != nil {
		e.t.Fatal(err)
	}
	if _, _, err := e.st.NodeHello(e.ctx, id, store.HelloInfo{AgentVersion: "0.3.0", Instance: "i-1", Caps: caps}, e.now); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// code is the Connect code of err, and its message.
func code(err error) (connect.Code, string) {
	if err == nil {
		return 0, ""
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code(), ce.Message()
	}
	return connect.CodeUnknown, err.Error()
}

func (e *env) wantErr(err error, c connect.Code, msg string) {
	e.t.Helper()
	gotC, gotM := code(err)
	if err == nil || gotC != c || gotM != msg {
		e.t.Fatalf("error = %v (%v %q), want %v %q", err, gotC, gotM, c, msg)
	}
}

func (e *env) audits() []store.AuditRow {
	e.t.Helper()
	rows, err := e.st.ListAudit(e.ctx, "", 0, 100)
	if err != nil {
		e.t.Fatal(err)
	}
	return rows
}

func (e *env) auditActions() []string {
	var out []string
	for _, r := range e.audits() {
		out = append(out, r.Action)
	}
	return out
}

// noSecrets fails when a secret the fake issued, or a private key the test recorded, shows up in the captured log,
// in an audit row, or in any of the given proto messages (JSON and wire form).
func (e *env) noSecrets(msgs ...proto.Message) {
	e.t.Helper()
	e.cf.mu.Lock()
	secrets := append(append([]string(nil), e.cf.issued...), e.secret...)
	e.cf.mu.Unlock()
	if len(secrets) == 0 {
		e.t.Fatal("test bug: no secrets to look for")
	}
	var hay []string
	hay = append(hay, e.log.String())
	for _, r := range e.audits() {
		hay = append(hay, r.Params, r.Action, r.Result)
	}
	for _, m := range msgs {
		j, _ := protojson.Marshal(m)
		w, _ := proto.Marshal(m)
		hay = append(hay, string(j), string(w))
	}
	for _, sec := range secrets {
		for _, h := range hay {
			if strings.Contains(h, sec) {
				e.t.Fatalf("secret %q leaked into %q", sec[:8]+"...", truncate(h))
			}
		}
	}
}

func truncate(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func (e *env) rpc() rpc { return rpc{e.s} }
