//go:build js && wasm

package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"syscall/js"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/auth"
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
	if got := counts.Get("sequentialQueries").Int(); got != 4 {
		t.Fatalf("cron used %d sequential D1 calls, want 4", got)
	}
	if got := remote.pokeCount(); got != 1 {
		t.Fatalf("minute 10 remote fan-outs = %d, want 1", got)
	}

	retentionArgs := js.Global().Get("Object").New()
	retentionArgs.Set("at", time.Date(2026, 10, 9, 12, 17, 0, 0, time.UTC).UnixMilli())
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
	if got := retentionCounts.Get("sequentialQueries").Int(); got != 9 {
		t.Fatalf("minute 17 cron used %d sequential D1 calls, want 9 (3 tick + 6 retention)", got)
	}
	// Two batches: the node-down sweep's live read (every tick) and the daily rollup; the prunes are single statements.
	if got := retentionCounts.Get("batchCalls").Int(); got != 2 {
		t.Fatalf("minute 17 cron used %d D1 batches, want 2 (the live read and the rollup)", got)
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
