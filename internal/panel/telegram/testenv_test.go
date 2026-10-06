package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// testToken is shaped like a bot token and is the only one the fake Telegram accepts. It is not a real token.
const testToken = "123456789:AAFakeFakeFakeFakeFakeFakeFakeFake_x"

type sentMsg struct {
	Chat int64
	Text string
}

// fakeTG is a stand-in for api.telegram.org: it records what the panel sends and serves the updates a test queues.
type fakeTG struct {
	srv *httptest.Server

	mu               sync.Mutex
	sent             []sentMsg
	updates          []tgUpdate
	nextID           int64
	offsets          []int64        // the offset of every getUpdates call
	sendPlan         []planned      // consumed one per sendMessage; empty = OK
	pollPlan         []planned      // consumed one per getUpdates; empty = serve normally
	calls            map[string]int // method -> count
	blockSendCall    int
	blockSendStarted chan struct{}
	username         string
	botID            int64
	webhooks         int
	badToken         bool // every call answers 401
}

type planned struct {
	status     int // 0 = 200
	retryAfter int
	desc       string
}

func newFakeTG(t *testing.T) *fakeTG {
	t.Helper()
	f := &fakeTG{calls: map[string]int{}, username: "mist_alert_bot", botID: 123456789, nextID: 100}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTG) reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeTG) fail(w http.ResponseWriter, p planned) {
	status := p.status
	if status == 0 {
		status = 500
	}
	body := map[string]any{"ok": false, "error_code": status, "description": p.desc}
	if p.retryAfter > 0 {
		body["parameters"] = map[string]any{"retry_after": p.retryAfter}
	}
	f.reply(w, status, body)
}

func (f *fakeTG) serve(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/bot")
	tok, method, _ := strings.Cut(rest, "/")
	raw, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls[method]++
	bad := f.badToken || tok != testToken
	f.mu.Unlock()
	if bad {
		f.reply(w, 401, map[string]any{"ok": false, "error_code": 401, "description": "Unauthorized"})
		return
	}
	switch method {
	case "getMe":
		f.reply(w, 200, map[string]any{"ok": true, "result": map[string]any{"id": f.botID, "is_bot": true, "username": f.username}})
	case "deleteWebhook":
		f.mu.Lock()
		f.webhooks++
		f.mu.Unlock()
		f.reply(w, 200, map[string]any{"ok": true, "result": true})
	case "sendMessage":
		var m struct {
			ChatID    int64  `json:"chat_id"`
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		_ = json.Unmarshal(raw, &m)
		f.mu.Lock()
		var plan *planned
		if len(f.sendPlan) > 0 {
			plan, f.sendPlan = &f.sendPlan[0], f.sendPlan[1:]
		}
		block := f.blockSendCall != 0 && f.blockSendCall == f.calls["sendMessage"]
		started := f.blockSendStarted
		if block {
			f.blockSendCall = 0
			f.blockSendStarted = nil
		}
		f.mu.Unlock()
		if block {
			if started != nil {
				close(started)
			}
			<-r.Context().Done()
			return
		}
		if plan != nil && plan.status != 0 && plan.status != 200 {
			f.fail(w, *plan)
			return
		}
		f.mu.Lock()
		f.sent = append(f.sent, sentMsg{m.ChatID, m.Text})
		f.mu.Unlock()
		f.reply(w, 200, map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
	case "getUpdates":
		var p struct {
			Offset int64 `json:"offset"`
		}
		_ = json.Unmarshal(raw, &p)
		f.mu.Lock()
		f.offsets = append(f.offsets, p.Offset)
		var plan *planned
		if len(f.pollPlan) > 0 {
			plan, f.pollPlan = &f.pollPlan[0], f.pollPlan[1:]
		}
		var out []tgUpdate
		for _, u := range f.updates {
			if u.UpdateID >= p.Offset {
				out = append(out, u)
			}
		}
		f.mu.Unlock()
		if plan != nil {
			f.fail(w, *plan)
			return
		}
		if len(out) == 0 {
			select { // a long poll that finds nothing
			case <-time.After(25 * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		f.reply(w, 200, map[string]any{"ok": true, "result": append([]tgUpdate{}, out...)})
	default:
		f.reply(w, 404, map[string]any{"ok": false, "error_code": 404, "description": "Not Found"})
	}
}

// say queues a message from a private chat.
func (f *fakeTG) say(chat int64, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	f.updates = append(f.updates, tgUpdate{UpdateID: f.nextID, Message: &tgMessage{Chat: tgChat{ID: chat, Type: "private"}, Text: text}})
}

func (f *fakeTG) sentTo(chat int64) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, m := range f.sent {
		if m.Chat == chat {
			out = append(out, m.Text)
		}
	}
	return out
}

func (f *fakeTG) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

func (f *fakeTG) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// env is a panel store and vault, a fake Telegram and a Service pointed at it.
type env struct {
	t   *testing.T
	st  *store.Store
	tg  *fakeTG
	svc *Service
	clk *clock
}

type clock struct{ ns atomic.Int64 }

func (c *clock) now() time.Time          { return time.Unix(0, c.ns.Load()).UTC() }
func (c *clock) advance(d time.Duration) { c.ns.Add(int64(d)) }

type option func(*Config)

func newEnv(t *testing.T, opts ...option) *env {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, tg: newFakeTG(t), clk: &clock{}}
	e.clk.ns.Store(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC).UnixNano())
	cfg := Config{Store: st, Vault: vlt, StepUp: func(context.Context) error { return nil }, APIBase: e.tg.srv.URL,
		Now: e.clk.now, Window: 40 * time.Millisecond, PollTimeout: 1, RetryBase: 20 * time.Millisecond, WatchEvery: time.Hour, WatchFirst: time.Hour}
	for _, o := range opts {
		o(&cfg)
	}
	e.svc, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) addAdmin(id, name, role string) store.Admin {
	e.t.Helper()
	if _, err := e.st.W.Exec(`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, 1)`, id, name, role, []byte(id)); err != nil {
		e.t.Fatal(err)
	}
	return store.Admin{ID: id, DisplayName: name, Role: role}
}

// setBot stores the fake bot directly (the RPC path has its own test).
func (e *env) setBot() {
	e.t.Helper()
	if _, err := e.st.SetTelegramBot(context.Background(), store.TelegramBotRow{Token: e.svc.vault.Seal([]byte(testToken), tokenRecordID), BotID: e.tg.botID, Username: e.tg.username}, e.clk.now()); err != nil {
		e.t.Fatal(err)
	}
}

// link binds a chat to an admin directly.
func (e *env) link(a store.Admin, chat int64) {
	e.t.Helper()
	if err := e.st.BindTelegramChat(context.Background(), a.ID, chat, e.clk.now()); err != nil {
		e.t.Fatal(err)
	}
}

// run starts the service; it is stopped (and awaited) when the test ends.
func (e *env) run() context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.svc.Run(ctx) }()
	e.t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			e.t.Error("Run did not return after its context ended")
		}
	})
	return cancel
}

func (e *env) ctxAs(a store.Admin) context.Context { return auth.WithAdmin(context.Background(), a) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// quiet fails when the fake received more than n messages in the next d.
func (e *env) quiet(n int, d time.Duration) {
	e.t.Helper()
	time.Sleep(d)
	if got := e.tg.total(); got != n {
		e.t.Fatalf("%d messages sent, want %d: %v", got, n, e.tg.sent)
	}
}

func alert(kind, node string, sev int) store.HealthAlert {
	return store.HealthAlert{ID: "alr_" + kind + node, Kind: kind, Severity: sev, NodeID: node, Params: map[string]string{}}
}
