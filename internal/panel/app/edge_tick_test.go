//go:build !js

package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

type edgeTickRemote struct {
	mu    sync.Mutex
	pokes [][]string
}

func (r *edgeTickRemote) Ask(context.Context, string, string, *agentv1.ConnectResponse, time.Time) (*agentv1.ConnectRequest, error) {
	return nil, nil
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

func buildEdgeTickPanel(t *testing.T, remote fleet.Remote) *Panel {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

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
	p, err := Build(Config{
		Store: st, Vault: vlt, Auth: authSvc, Clock: time.Now, Logger: log,
		Instance: InstanceConfig{
			PublicURL: "https://example.com", AdminPrefix: "/admin/", RPID: "example.com",
			RPOrigins: []string{"https://example.com"}, AgentSNI: "agent.example.com", SubPrefix: "/sub/",
		},
		DataDir: t.TempDir(), MasterKey: key, Remote: remote,
		AfterResponse: func(work func()) { work() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
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
