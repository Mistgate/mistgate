//go:build js && wasm

package bridge

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall/js"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

type cronTestRemote struct {
	mu     sync.Mutex
	called int
}

func (*cronTestRemote) Ask(context.Context, string, string, *agentv1.ConnectResponse, time.Time) (*agentv1.ConnectRequest, error) {
	return nil, nil
}

func (*cronTestRemote) Close(context.Context, string, string) error { return nil }

func (*cronTestRemote) Send(context.Context, string, string, *agentv1.ConnectResponse, time.Time) error {
	return nil
}

func (r *cronTestRemote) Poke(context.Context, []string) error {
	r.mu.Lock()
	r.called++
	r.mu.Unlock()
	return nil
}

func (r *cronTestRemote) pokeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.called
}

func TestCronOpRejectsInvalidRequestAndArgs(t *testing.T) {
	if _, err := cron(nil); err != errCronRequest {
		t.Fatalf("cron(nil) error = %v, want %v", err, errCronRequest)
	}
	if _, err := cron([]js.Value{js.Null()}); err != errCronRequest {
		t.Fatalf("cron(null) error = %v, want %v", err, errCronRequest)
	}
	args := js.Global().Get("Object").New()
	if _, err := cron([]js.Value{args}); err != errCronArgs {
		t.Fatalf("cron without an integer at error = %v, want %v", err, errCronArgs)
	}
	args.Set("at", "not a timestamp")
	if _, err := cron([]js.Value{args}); err != errCronArgs {
		t.Fatalf("cron with a non-integer at error = %v, want %v", err, errCronArgs)
	}
}

func TestCronOpRunsEdgeTickAndReleasesFanout(t *testing.T) {
	ctx := context.Background()
	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	st, err := store.OpenD1(ctx, binding)
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
	runner := newEdgeTaskRunner()
	remote := &cronTestRemote{}
	built, err := app.Build(app.Config{
		Store: st, Vault: vlt, Auth: authSvc, Clock: time.Now, Logger: log, DataDir: edgeNoFilesystemDataDir, MasterKey: key,
		Instance: app.InstanceConfig{
			PublicURL: "https://example.com", AdminPrefix: "/admin/", RPID: "example.com",
			RPOrigins: []string{"https://example.com"}, AgentSNI: "agent.example.com", SubPrefix: "/sub/",
		},
		Remote: remote, AfterResponse: runner.Run,
	})
	if err != nil {
		t.Fatal(err)
	}
	stateMu.Lock()
	previous := state
	state = &edgeState{store: st, fleet: built.Fleet, panel: built, handler: built.Handler, afterResponse: runner}
	stateMu.Unlock()
	t.Cleanup(func() {
		stateMu.Lock()
		state = previous
		stateMu.Unlock()
	})

	args := js.Global().Get("Object").New()
	args.Set("at", time.Date(2026, 10, 9, 12, 10, 0, 0, time.UTC).UnixMilli())
	binding.Call("__beginQueryCount", "cron tick")
	ended := false
	defer func() {
		if !ended {
			binding.Call("__endQueryCount")
		}
	}()
	out, err := cron([]js.Value{args})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil := out.Get("waitUntil")
	if waitUntil.Type() != js.TypeObject || waitUntil.Get("then").Type() != js.TypeFunction {
		t.Fatal("cron did not return a waitUntil promise")
	}
	awaitCronPromise(t, waitUntil)
	counts := binding.Call("__endQueryCount")
	ended = true
	// The Telegram pending-code check adds one read and the updates job one more (the active rollout); otherwise this tick
	// costs the four sweeps, the two country settings and eight calls for the evaluator pass on an empty fleet.
	if got := counts.Get("sequentialQueries").Int(); got != 17 {
		t.Fatalf("cron used %d sequential D1 calls, want 17 (6 tick + updates read + Telegram code check + empty-owner watcher + 8 evaluator on an empty fleet)", got)
	}
	if got := remote.pokeCount(); got != 1 {
		t.Fatalf("minute 10 remote fan-outs = %d, want 1", got)
	}

	retentionArgs := js.Global().Get("Object").New()
	retentionArgs.Set("at", time.Date(2026, 10, 9, 12, 17, 0, 0, time.UTC).UnixMilli())
	if _, err := st.W.ExecContext(ctx, `UPDATE job_lease SET expires_at = 0 WHERE name = 'health-eval'`); err != nil {
		t.Fatal(err)
	}
	binding.Call("__beginQueryCount", "cron minute 17")
	retentionEnded := false
	defer func() {
		if !retentionEnded {
			binding.Call("__endQueryCount")
		}
	}()
	retentionOut, err := cron([]js.Value{retentionArgs})
	if err != nil {
		t.Fatal(err)
	}
	awaitCronPromise(t, retentionOut.Get("waitUntil"))
	retentionCounts := binding.Call("__endQueryCount")
	retentionEnded = true
	// The nodes are still warm in the isolate and the empty summary is unchanged, so the evaluator pass is six calls: its lease,
	// the signal batch, the active alerts, the interval setting, the update rollout and the country settings. Telegram's pending
	// code check adds one read on every tick. The updates job adds the active rollout read, and its history prune at minute 17.
	if got := retentionCounts.Get("sequentialQueries").Int(); got != 24 {
		t.Fatalf("minute 17 cron used %d sequential D1 calls, want 24 (5 tick + 2 updates + Telegram code check + 10 retention + 6 evaluator)", got)
	}
	// Three batches: the node-down sweep's live read (every tick), the evaluator's signal batch and the daily rollup; the
	// prunes are single statements.
	if got := retentionCounts.Get("batchCalls").Int(); got != 3 {
		t.Fatalf("minute 17 cron used %d D1 batches, want 3 (the live read, the signals and the rollup)", got)
	}

	if _, err := st.W.ExecContext(ctx, `DROP TABLE user`); err != nil {
		t.Fatal(err)
	}
	errorArgs := js.Global().Get("Object").New()
	errorArgs.Set("at", time.Date(2026, 10, 9, 12, 3, 0, 0, time.UTC).UnixMilli())
	errorOut, err := cron([]js.Value{errorArgs})
	if err != nil {
		t.Fatalf("cron returned EdgeTick's error: %v", err)
	}
	waitUntil = errorOut.Get("waitUntil")
	if waitUntil.Type() != js.TypeObject || waitUntil.Get("then").Type() != js.TypeFunction {
		t.Fatal("cron omitted waitUntil after EdgeTick returned an error")
	}
	awaitCronPromise(t, waitUntil)
}

type blockingCronCountrySource struct {
	calls   atomic.Int32
	started chan struct{}
	release <-chan struct{}
}

func (*blockingCronCountrySource) Name() string     { return "alice" }
func (*blockingCronCountrySource) HourlyLimit() int { return 250 }

func (s *blockingCronCountrySource) Measure(ctx context.Context, _ health.CountryRound) (health.CountryObserved, error) {
	if s.calls.Add(1) == 1 {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return health.CountryObserved{}, ctx.Err()
		}
	}
	return health.CountryObserved{Eyeball: true}, nil
}

func TestCronConcurrentTicksShareOneCountryRound(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	st, err := store.OpenD1(ctx, binding)
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
	panel, err := app.Build(app.Config{
		Store: st, Vault: vlt, Auth: authSvc, Clock: time.Now, Logger: log, DataDir: edgeNoFilesystemDataDir, MasterKey: key,
		Instance: app.InstanceConfig{
			PublicURL: "https://example.com", AdminPrefix: "/admin/", RPID: "example.com",
			RPOrigins: []string{"https://example.com"}, AgentSNI: "agent.example.com", SubPrefix: "/sub/",
		},
		Remote: &cronTestRemote{},
	})
	if err != nil {
		t.Fatal(err)
	}

	tickAt := time.Now().UTC()
	if tickAt.Minute() == 10 || tickAt.Minute() == 17 {
		tickAt = tickAt.Add(time.Minute)
	}
	const nodeID = "nod_country_lease"
	if _, err := st.CreateEnrollment(ctx, &store.NodeRow{ID: nodeID, Name: "de1.example.com", Address: "de1.example.com"}, "",
		[]byte("alice-enrollment-token"), "alice", tickAt, tickAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.NodeHello(ctx, nodeID, 1, store.HelloInfo{Instance: "alice"}, tickAt); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseRound := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseRound()
	source := &blockingCronCountrySource{started: make(chan struct{}), release: release}
	newHealth := func() *health.Service {
		return health.New(st, vlt, nil, panel.Fleet, health.Config{
			Edge: true, CountryRoundTimeout: 55 * time.Second, Now: func() time.Time { return tickAt }, RoundLease: app.EdgeRoundLease(st, log),
			Country: func(context.Context) (health.CountryConfig, health.CountrySource) {
				return health.CountryConfig{Enabled: true, Country: "RU", Interval: 10 * time.Minute}, source
			},
			Resolve: func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("203.0.113.10")}, nil
			},
		})
	}
	panel.Health = newHealth()
	// A second panel models another Worker isolate: it shares D1 but has its own health service and countryMu.
	otherPanel := *panel
	otherPanel.Health = newHealth()

	firstDone := make(chan error, 1)
	go func() { firstDone <- panel.EdgeTick(ctx, tickAt) }()
	select {
	case <-source.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first country round did not start")
	}

	secondDone := make(chan error, 1)
	go func() { secondDone <- otherPanel.EdgeTick(ctx, tickAt) }()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second EdgeTick: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second EdgeTick did not finish while the first round was blocked")
	}
	gotRounds := source.calls.Load()
	releaseRound()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first EdgeTick: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first EdgeTick did not finish after releasing its country round")
	}
	if gotRounds != 1 {
		t.Fatalf("concurrent EdgeTicks started %d country rounds, want 1", gotRounds)
	}
}

func awaitCronPromise(t *testing.T, promise js.Value) {
	t.Helper()
	done := make(chan string, 1)
	resolve := js.FuncOf(func(js.Value, []js.Value) any {
		done <- ""
		return nil
	})
	reject := js.FuncOf(func(_ js.Value, args []js.Value) any {
		message := "promise rejected"
		if len(args) > 0 {
			message = args[0].Get("message").String()
		}
		done <- message
		return nil
	})
	defer resolve.Release()
	defer reject.Release()
	promise.Call("then", resolve, reject)
	select {
	case message := <-done:
		if message != "" {
			t.Fatalf("cron waitUntil: %s", message)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cron waitUntil did not resolve")
	}
}
