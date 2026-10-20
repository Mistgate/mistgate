//go:build !js

package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/telegram"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/release"
)

type edgeTickSend struct {
	nodeID, requestID string
	frame             *agentv1.ConnectResponse
	deadline          time.Time
}

type edgeTickRemote struct {
	mu    sync.Mutex
	pokes [][]string
	sends []edgeTickSend
}

func (r *edgeTickRemote) Ask(context.Context, string, string, *agentv1.ConnectResponse, time.Time) (*agentv1.ConnectRequest, error) {
	return nil, nil
}

func (r *edgeTickRemote) Send(_ context.Context, nodeID, requestID string, frame *agentv1.ConnectResponse, deadline time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sends = append(r.sends, edgeTickSend{nodeID: nodeID, requestID: requestID, frame: frame, deadline: deadline})
	return nil
}

func (r *edgeTickRemote) Close(context.Context, string, string) error { return nil }

func (r *edgeTickRemote) Poke(_ context.Context, nodeIDs []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pokes = append(r.pokes, append([]string(nil), nodeIDs...))
	return nil
}

func (r *edgeTickRemote) pokeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pokes)
}

func (r *edgeTickRemote) sentFrames() []edgeTickSend {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]edgeTickSend(nil), r.sends...)
}

func buildEdgeTickPanel(t *testing.T, remote fleet.Remote) *Panel {
	t.Helper()
	p, _ := buildPanel(t, remote)
	return p
}

// buildPanel assembles a panel on a fresh store: the edge edition when remote is set, the VPS one when it is nil.
func buildPanel(t *testing.T, remote fleet.Remote) (*Panel, *auth.Service) {
	t.Helper()
	return buildPanelWith(t, remote, nil)
}

// buildPanelWith is buildPanel with a last look at the config before Build.
func buildPanelWith(t *testing.T, remote fleet.Remote, tweak func(*Config)) (*Panel, *auth.Service) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return buildPanelOn(t, st, remote, tweak)
}

// buildPanelOn assembles a panel on a store that is already open: two of them on one store are two isolates of the edge.
func buildPanelOn(t *testing.T, st *store.Store, remote fleet.Remote, tweak func(*Config)) (*Panel, *auth.Service) {
	p, authSvc, _ := buildPanelOnWithVault(t, st, remote, tweak)
	return p, authSvc
}

func buildPanelOnWithVault(t *testing.T, st *store.Store, remote fleet.Remote, tweak func(*Config)) (*Panel, *auth.Service, *vault.Vault) {
	t.Helper()
	key := make([]byte, vault.KeySize)
	for i := range key {
		key[i] = byte(i + 1)
	}
	vlt, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	authSvc, err := auth.New(st, auth.Config{RPID: "example.com", Origins: []string{"https://example.com"}, Vault: vlt}, log)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		Store: st, Vault: vlt, Auth: authSvc, Clock: time.Now, Logger: log,
		Instance: InstanceConfig{
			PublicURL: "https://example.com", AdminPrefix: "/admin/", RPID: "example.com",
			RPOrigins: []string{"https://example.com"}, AgentSNI: "agent.example.com", SubPrefix: "/sub/",
		},
		DataDir: t.TempDir(), MasterKey: key, Remote: remote,
		AfterResponse: func(work func()) { work() },
	}
	if tweak != nil {
		tweak(&cfg)
	}
	p, err := Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p, authSvc, vlt
}

func addExpiredAlice(t *testing.T, p *Panel, now time.Time) {
	t.Helper()
	ctx := context.Background()
	groupID := store.NewID("grp_")
	if err := p.Store.Access().CreateGroup(ctx, store.AccessGroup{ID: groupID, Name: "alice", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	user := store.AccessUser{
		ID: store.NewID("usr_"), Name: "alice", GroupID: groupID, Status: "active", AppHapp: true, AppAmnezia: true,
		AllNodes: true, QuotaReset: "none", PeriodStart: now, ExpiresAt: now.Add(-time.Hour), DeviceLimit: 5,
		SubTokenHash: []byte("alice-token-hash"), SubTokenEnc: []byte("sealed"), CreatedAt: now,
	}
	if err := p.Store.Access().CreateUser(ctx, user, store.AccessDevice{}, nil); err != nil {
		t.Fatal(err)
	}
}

func addSilentNodeA(t *testing.T, p *Panel, now time.Time) string {
	t.Helper()
	ctx := context.Background()
	nodeID := store.NewID("nod_")
	silentAt := now.Add(-time.Hour)
	if _, err := p.Store.CreateEnrollment(ctx, &store.NodeRow{ID: nodeID, Name: "node-a", Address: "203.0.113.10"}, "",
		[]byte("node-a-enrollment-hash"), "adm_test", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.W.ExecContext(ctx, `UPDATE node SET state = 'active', last_seen_at = ?, last_disconnected_at = ? WHERE id = ?`,
		silentAt.Unix(), silentAt.Unix(), nodeID); err != nil {
		t.Fatal(err)
	}
	return nodeID
}

func TestEdgeTickSweepsUsersAndNodeDownEachTick(t *testing.T) {
	remote := &edgeTickRemote{}
	p := buildEdgeTickPanel(t, remote)
	now := time.Now().UTC().Truncate(time.Minute)
	addExpiredAlice(t, p, now)
	nodeID := addSilentNodeA(t, p, now)

	tickAt := time.Date(2026, 10, 9, 12, 3, 0, 0, time.UTC)
	if err := p.EdgeTick(context.Background(), tickAt); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := p.Store.R.QueryRowContext(context.Background(), `SELECT status FROM user WHERE name = 'alice'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "expired" {
		t.Fatalf("alice status = %q, want expired", status)
	}
	if got := remote.pokeCount(); got != 1 {
		t.Fatalf("status sweep requested %d fan-outs, want 1", got)
	}
	assertNodeDownCount(t, p.Store, nodeID, 1)

	if err := p.EdgeTick(context.Background(), tickAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := remote.pokeCount(); got != 1 {
		t.Fatalf("unchanged status requested %d fan-outs, want 1 total", got)
	}
	assertNodeDownCount(t, p.Store, nodeID, 1)
}

// seedEdgeUpdateRollout stores a running rollout with one step for a live link-only node and returns both.
func seedEdgeUpdateRollout(t *testing.T, p *Panel, now time.Time, step store.StepRow) (string, store.RolloutRow) {
	t.Helper()
	ctx := context.Background()
	nodeID := addSilentNodeA(t, p, now)
	if _, _, err := p.Store.NodeHello(ctx, nodeID, 1, store.HelloInfo{
		AgentVersion: "0.1.0-old", APIVersion: 1, Instance: "edge-update-test", Built: 1, Caps: []string{"update-link/1"},
	}, now); err != nil {
		t.Fatalf("seed live link-only node: %v", err)
	}
	releaseManifest := release.Manifest{
		Schema: release.Schema, Version: buildinfo.Version, Built: now.Unix(), Expires: now.Add(24 * time.Hour).Unix(),
		Files: []release.File{{OS: "linux", Arch: "amd64", Name: "mistgate-node-linux-amd64", Size: 1,
			SHA256: strings.Repeat("a", 64)}},
	}
	manifest, err := releaseManifest.Marshal()
	if err != nil {
		t.Fatalf("marshal pinned release: %v", err)
	}
	rollout := store.RolloutRow{
		ID: "rol_edge_tick", Status: store.RolloutRunning, ToVersion: buildinfo.Version, ToBuilt: now.Unix(),
		Manifest: manifest, Signature: []byte("pinned-signature"), BatchSize: 1, CreatedBy: "adm_test", CreatedAt: now,
	}
	step.NodeID, step.NodeName = nodeID, "node-a"
	if err := p.Store.CreateRollout(ctx, rollout, []store.StepRow{step}); err != nil {
		t.Fatalf("seed rollout: %v", err)
	}
	return nodeID, rollout
}

func TestEdgeTickAdvancesNodeUpdateRollout(t *testing.T) {
	ctx := context.Background()
	remote := &edgeTickRemote{}
	p := buildEdgeTickPanel(t, remote)
	now := time.Now().UTC().Truncate(time.Second)
	nodeID, rollout := seedEdgeUpdateRollout(t, p, now, store.StepRow{State: store.StepPending})

	if err := p.EdgeTick(ctx, now); err != nil {
		t.Fatalf("edge tick: %v", err)
	}
	steps, err := p.Store.RolloutSteps(ctx, rollout.ID)
	if err != nil || len(steps) != 1 {
		t.Fatalf("rollout steps = %+v, %v", steps, err)
	}
	if steps[0].State != store.StepSent || steps[0].RequestID == "" || steps[0].DownloadSHA == "" {
		t.Fatalf("edge tick step = %+v, want SENT with a link token digest", steps[0])
	}
	sends := remote.sentFrames()
	if len(sends) != 1 || sends[0].nodeID != nodeID || sends[0].requestID != steps[0].RequestID || sends[0].deadline.IsZero() || sends[0].frame.GetUpdateAgent() == nil {
		t.Fatalf("recorded update sends = %+v, want one UpdateAgent for %s", sends, nodeID)
	}
}

// The update_failed alert is derived by the health evaluator from the rollout's state. The updates job runs first in the
// tick, so a step that fails its wait and pauses the rollout raises the alert in that same tick, not one minute later.
func TestEdgeTickRaisesUpdateFailedAlertInTheSameTick(t *testing.T) {
	ctx := context.Background()
	p := buildEdgeTickPanel(t, &edgeTickRemote{})
	now := time.Now().UTC().Truncate(time.Second)
	// SENT long ago, never acknowledged and never back: past the answer and reconnect waits, so the tick fails the step.
	_, rollout := seedEdgeUpdateRollout(t, p, now, store.StepRow{State: store.StepSent, SentAt: now.Add(-time.Hour)})
	updateFailed := func() int {
		alerts, err := p.Store.ActiveAlerts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, a := range alerts {
			if a.Kind == "update_failed" {
				n++
			}
		}
		return n
	}
	if got := updateFailed(); got != 0 {
		t.Fatalf("update_failed alerts before the tick = %d", got)
	}

	if err := p.EdgeTick(ctx, now); err != nil {
		t.Fatalf("edge tick: %v", err)
	}
	ro, err := p.Store.ActiveRollout(ctx)
	if err != nil || ro.ID != rollout.ID || ro.Status != store.RolloutPaused {
		t.Fatalf("rollout after the tick = %+v, %v; want it paused by the failed step", ro, err)
	}
	if got := updateFailed(); got != 1 {
		t.Fatalf("active update_failed alerts after the same tick = %d, want 1", got)
	}
}

func TestEdgeTickEvaluatesAndResolvesNodeDown(t *testing.T) {
	ctx := context.Background()
	p := buildEdgeTickPanel(t, &edgeTickRemote{})
	nodeID := addSilentNodeA(t, p, time.Now().UTC())
	tickAt := time.Now().UTC().Truncate(time.Minute)

	if err := p.EdgeTick(ctx, tickAt); err != nil {
		t.Fatal(err)
	}
	assertActiveNodeDown(t, p.Store, nodeID, true)

	returnedAt := time.Now().UTC()
	if _, err := p.Store.W.ExecContext(ctx, `UPDATE node SET last_seen_at = ?, last_connected_at = ? WHERE id = ?`, returnedAt.Unix(), returnedAt.Unix(), nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.W.ExecContext(ctx, `INSERT OR REPLACE INTO node_live (node_id, session) VALUES (?, 1)`, nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.W.ExecContext(ctx, `UPDATE job_lease SET expires_at = 0 WHERE name = 'health-eval'`); err != nil {
		t.Fatal(err)
	}
	if err := p.EdgeTick(ctx, tickAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertActiveNodeDown(t, p.Store, nodeID, false)
}

// Two edge isolates tick while the first is inside its evaluator pass, after it read the alerts. The D1 lease keeps the
// second from reconciling, so the alert is announced once when it opens and once when it resolves; without the lease the
// first isolate, resuming with the alerts it had read, opens the same alert a second time.
func TestConcurrentEdgeTicksAnnounceNodeDownOnceOnOpenAndOnResolve(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var opened, resolved atomic.Int32
	count := func(c *Config) {
		c.OnAlertTransition = func(tr health.Transition) {
			if tr.Alert.Kind != "node_down" {
				return
			}
			if tr.Resolved {
				resolved.Add(1)
			} else {
				opened.Add(1)
			}
		}
	}
	first, _ := buildPanelOn(t, st, &edgeTickRemote{}, count)
	second, _ := buildPanelOn(t, st, &edgeTickRemote{}, count)
	nodeID := addSilentNodeA(t, first, time.Now().UTC())

	type gate struct {
		once            sync.Once
		entered, unlock chan struct{}
	}
	var current atomic.Pointer[gate]
	first.Health.AddConditionSource(func(context.Context) []health.ExtCond {
		if g := current.Load(); g != nil {
			g.once.Do(func() { close(g.entered) })
			<-g.unlock
		}
		return nil
	})
	overlap := func(tickAt time.Time) {
		t.Helper()
		g := &gate{entered: make(chan struct{}), unlock: make(chan struct{})}
		var unlockOnce sync.Once
		unlock := func() { unlockOnce.Do(func() { close(g.unlock) }) }
		defer unlock()
		current.Store(g)
		defer current.Store(nil)
		if _, err := st.W.ExecContext(ctx, `UPDATE job_lease SET expires_at = 0 WHERE name = 'health-eval'`); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- first.EdgeTick(ctx, tickAt) }()
		select {
		case <-g.entered:
		case <-time.After(10 * time.Second):
			t.Fatal("the first isolate never reached its evaluator pass")
		}
		if err := second.EdgeTick(ctx, tickAt); err != nil {
			t.Fatal(err)
		}
		unlock()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}

	tickAt := time.Now().UTC().Truncate(time.Minute)
	overlap(tickAt)
	assertActiveNodeDown(t, st, nodeID, true)
	if opened.Load() != 1 || resolved.Load() != 0 {
		t.Fatalf("after the overlapping ticks: node_down opened %d, resolved %d; want 1, 0", opened.Load(), resolved.Load())
	}

	returnedAt := time.Now().UTC()
	if _, err := st.W.ExecContext(ctx, `UPDATE node SET last_seen_at = ?, last_connected_at = ? WHERE id = ?`, returnedAt.Unix(), returnedAt.Unix(), nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT OR REPLACE INTO node_live (node_id, session) VALUES (?, 1)`, nodeID); err != nil {
		t.Fatal(err)
	}
	overlap(tickAt.Add(time.Minute))
	assertActiveNodeDown(t, st, nodeID, false)
	if opened.Load() != 1 || resolved.Load() != 1 {
		t.Fatalf("after the node returned: node_down opened %d, resolved %d; want 1, 1", opened.Load(), resolved.Load())
	}
}

func TestConcurrentEdgeTicksSendOneTelegramNodeDown(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const token = "123456789:AAFakeFakeFakeFakeFakeFakeFakeFake_x"
	type message struct {
		chat int64
		text string
	}
	var mu sync.Mutex
	var sent []message
	fakeTelegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		method := strings.TrimPrefix(r.URL.Path, "/bot"+token+"/")
		switch method {
		case "sendMessage":
			var body struct {
				ChatID int64  `json:"chat_id"`
				Text   string `json:"text"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode fake Telegram send: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			mu.Lock()
			sent = append(sent, message{chat: body.ChatID, text: body.Text})
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 1}})
		default:
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 404, "description": "Not Found"})
		}
	}))
	t.Cleanup(fakeTelegram.Close)

	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_alice', 'alice', 'owner', x'01', 1)`); err != nil {
		t.Fatal(err)
	}
	var opened atomic.Int32
	configure := func(c *Config) {
		c.TelegramAPIBase = fakeTelegram.URL
		c.OnAlertTransition = func(tr health.Transition) {
			if tr.Alert.Kind == "node_down" && !tr.Resolved {
				opened.Add(1)
			}
		}
	}
	first, _, vlt := buildPanelOnWithVault(t, st, &edgeTickRemote{}, configure)
	second, _ := buildPanelOn(t, st, &edgeTickRemote{}, configure)
	if _, err := st.SetTelegramBot(ctx, store.TelegramBotRow{
		Token: vlt.Seal([]byte(token), "telegram_bot.token"), BotID: 123456789, Username: "mist_alert_bot",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.BindTelegramChat(ctx, "adm_alice", 901, time.Now()); err != nil {
		t.Fatal(err)
	}
	nodeID := addSilentNodeA(t, first, time.Now().UTC())
	tickAt := time.Date(2026, 10, 9, 12, 3, 0, 0, time.UTC)

	type gate struct {
		once            sync.Once
		entered, unlock chan struct{}
	}
	g := &gate{entered: make(chan struct{}), unlock: make(chan struct{})}
	first.Health.AddConditionSource(func(context.Context) []health.ExtCond {
		g.once.Do(func() { close(g.entered) })
		<-g.unlock
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- first.EdgeTick(ctx, tickAt) }()
	select {
	case <-g.entered:
	case <-time.After(10 * time.Second):
		close(g.unlock)
		t.Fatal("the first isolate never reached its evaluator pass")
	}
	if err := second.EdgeTick(ctx, tickAt); err != nil {
		close(g.unlock)
		t.Fatal(err)
	}
	close(g.unlock)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertActiveNodeDown(t, st, nodeID, true)
	mu.Lock()
	got := append([]message(nil), sent...)
	mu.Unlock()
	if opened.Load() != 1 || len(got) != 1 || got[0].chat != 901 {
		t.Fatalf("overlapping ticks opened %d alerts and sent %+v; want one alert and one message to chat 901", opened.Load(), got)
	}
}

func TestEdgeTickRunsWatcherWhenTelegramPollFails(t *testing.T) {
	ctx := context.Background()
	const token = "123456789:AAFakeFakeFakeFakeFakeFakeFakeFake_x"
	fakeTelegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			t.Errorf("unexpected Telegram method %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 503, "description": "Temporary failure"})
	}))
	t.Cleanup(fakeTelegram.Close)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_alice', 'alice', 'owner', x'01', 1)`); err != nil {
		t.Fatal(err)
	}
	p, _, vlt := buildPanelOnWithVault(t, st, &edgeTickRemote{}, func(c *Config) { c.TelegramAPIBase = fakeTelegram.URL })
	if _, err := st.SetTelegramBot(ctx, store.TelegramBotRow{
		Token: vlt.Seal([]byte(token), "telegram_bot.token"), BotID: 123456789, Username: "mist_alert_bot",
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.BindTelegramChat(ctx, "adm_alice", 901, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTelegramLinkCode(ctx, strings.Repeat("a", 64), "adm_alice", time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int32
	p.Telegram.SetSources(telegram.Sources{PanelRelease: func() (string, bool) {
		checks.Add(1)
		return "", false
	}})

	err = p.EdgeTick(ctx, time.Date(2026, 10, 9, 12, 10, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("EdgeTick returned nil after Telegram's poll failed")
	}
	if got := checks.Load(); got != 1 {
		t.Fatalf("watcher checks after poll failure = %d, want one", got)
	}
}

func assertActiveNodeDown(t *testing.T, st *store.Store, nodeID string, want bool) {
	t.Helper()
	alerts, err := st.ActiveAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, alert := range alerts {
		if alert.Kind == "node_down" && alert.NodeID == nodeID {
			count++
		}
	}
	if count != map[bool]int{true: 1, false: 0}[want] {
		t.Fatalf("active NODE_DOWN alerts for %s = %d, want %t", nodeID, count, want)
	}
}

func TestEdgeTickRunsRetentionAtMinute17(t *testing.T) {
	p := buildEdgeTickPanel(t, nil)
	ctx := context.Background()
	if err := p.Store.InsertEvent(ctx, store.EventRow{
		Time: time.Now().AddDate(-2, 0, 0), Severity: 1, Code: "retention_test", Source: "panel",
	}); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		t.Helper()
		var n int
		if err := p.Store.R.QueryRowContext(ctx, `SELECT count(*) FROM event WHERE code = 'retention_test'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	tickAt := time.Date(2026, 10, 9, 12, 16, 0, 0, time.UTC)
	if err := p.EdgeTick(ctx, tickAt); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 1 {
		t.Fatalf("event count before minute 17 = %d, want 1", got)
	}
	if err := p.EdgeTick(ctx, tickAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := count(); got != 0 {
		t.Fatalf("event count after minute 17 = %d, want 0", got)
	}
}

func TestEdgeTickContinuesAfterAStatusSweepError(t *testing.T) {
	p := buildEdgeTickPanel(t, nil)
	now := time.Now().UTC().Truncate(time.Minute)
	nodeID := addSilentNodeA(t, p, now)
	if err := p.Store.InsertEvent(context.Background(), store.EventRow{
		Time: time.Now().AddDate(-2, 0, 0), Severity: 1, Code: "retention_test", Source: "panel",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.W.ExecContext(context.Background(), `DROP TABLE user`); err != nil {
		t.Fatal(err)
	}

	tickAt := time.Date(2026, 10, 9, 12, 17, 0, 0, time.UTC)
	if err := p.EdgeTick(context.Background(), tickAt); err == nil {
		t.Fatal("EdgeTick returned nil after the status sweep failed")
	}
	assertNodeDownCount(t, p.Store, nodeID, 1)
	var retentionRows int
	if err := p.Store.R.QueryRowContext(context.Background(), `SELECT count(*) FROM event WHERE code = 'retention_test'`).Scan(&retentionRows); err != nil {
		t.Fatal(err)
	}
	if retentionRows != 0 {
		t.Fatalf("retention event count after minute 17 = %d, want 0", retentionRows)
	}
}

func TestEdgeTickRunsDesiredStateSafetyNetEveryTenthMinute(t *testing.T) {
	remote := &edgeTickRemote{}
	p := buildEdgeTickPanel(t, remote)
	ctx := context.Background()
	tickAt := time.Date(2026, 10, 9, 12, 9, 0, 0, time.UTC)
	if err := p.EdgeTick(ctx, tickAt); err != nil {
		t.Fatal(err)
	}
	if got := remote.pokeCount(); got != 0 {
		t.Fatalf("minute 9 fan-outs = %d, want 0", got)
	}
	if err := p.EdgeTick(ctx, tickAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := remote.pokeCount(); got != 1 {
		t.Fatalf("minute 10 fan-outs = %d, want 1", got)
	}
}

func TestEdgeBackgroundJobParity(t *testing.T) {
	p := buildEdgeTickPanel(t, nil)
	seen := make(map[string]bool, len(p.BackgroundJobs))
	for _, job := range p.BackgroundJobs {
		seen[job.Name] = true
		if edgeTickJobs[job.Name] == "" && edgeDeferred[job.Name] == "" {
			t.Errorf("background job %q has no edge phase", job.Name)
		}
	}
	for name := range edgeTickJobs {
		if !seen[name] {
			t.Errorf("edge tick phase %q has no VPS background job", name)
		}
	}
	for name := range edgeDeferred {
		if !seen[name] {
			t.Errorf("deferred edge phase %q has no VPS background job", name)
		}
	}
}

func assertNodeDownCount(t *testing.T, st *store.Store, nodeID string, want int) {
	t.Helper()
	var got int
	if err := st.R.QueryRowContext(context.Background(), `SELECT count(*) FROM event WHERE node_id = ? AND code = 'node_down'`, nodeID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("node_down event count = %d, want %d", got, want)
	}
}
