package fleet

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/update"
	"github.com/mistgate/mistgate/internal/release"
)

// An old build observed its state wrong (drift persists, node.applied_hash is the wrong hash). The rollout ships a build
// that observes it right: the new process reconnects with exactly the wanted state in Hello, the panel resends nothing, and
// the gate must pass on that Hello alone.
func TestRolloutPassesForANodeThatDriftedUnderTheOldBuild(t *testing.T) { rolloutAfterDrift(t, 0) }

// The same with the node's clock a minute behind the panel's: update_committed carries the node's time, and the gate
// compares it with when the panel sent the update.
func TestRolloutPassesWithANodeClockBehind(t *testing.T) { rolloutAfterDrift(t, time.Minute) }

func rolloutAfterDrift(t *testing.T, clockBehind time.Duration) {
	const oldBuilt, newBuilt = 1_790_000_000, 1_791_000_000
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	e.run()

	pub, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	dist := filepath.Join(dataDir, "dist")
	if err := os.MkdirAll(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &release.Manifest{Schema: release.Schema, Version: "0.2.0-e2e", Built: newBuilt, Expires: time.Now().Add(30 * 24 * time.Hour).Unix()}
	binary := make([]byte, 4<<10)
	rand.Read(binary)
	for _, name := range []string{"mistgate-node-linux-amd64", "mistgate-node-linux-arm64"} {
		if err := os.WriteFile(filepath.Join(dist, name), binary, 0o755); err != nil {
			t.Fatal(err)
		}
		f, err := release.FileFromPath(filepath.Join(dist, name))
		if err != nil {
			t.Fatal(err)
		}
		m.Files = append(m.Files, f)
	}
	body, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dist, release.ManifestName), body, 0o644)
	os.WriteFile(filepath.Join(dist, release.SignatureName), release.Sign(priv, body), 0o644)

	upd, err := update.New(e.st, e.f, nil, update.Config{
		DataDir: dataDir, Key: pub, PanelVersion: "0.2.0", PanelBuilt: newBuilt, StepUp: func(context.Context) error { return nil },
		Actor:  func(context.Context) string { return "adm_test" },
		Timing: update.Timing{Tick: 20 * time.Millisecond, GateWait: 3 * time.Second, RollbackWait: time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.f.SetUpdates(upd)
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { upd.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	mux := http.NewServeMux()
	path, h := upd.Handler()
	mux.Handle(path, h)
	admin := httptest.NewServer(mux)
	t.Cleanup(admin.Close)
	api := adminv1connect.NewUpdateServiceClient(http.DefaultClient, admin.URL)

	// the old build: applies what it is sent but reports a wrong observed hash, twice -> drift persists
	c := a.open()
	c.send(0, helloWithUpdate("inst1", oldBuilt, "update/1", "update-guard/1"))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	model := newModel()
	ds := c.desired()
	model.apply(ds)
	c.send(0, applied(ds, "deadbeef"))
	again := c.desired()
	model.apply(again)
	c.send(0, applied(again, "deadbeef"))
	c.quiet(300 * time.Millisecond)
	if n := mustNode(e, a.nodeID); n.AppliedHash == n.DesiredHash {
		t.Fatalf("setup: applied hash already matches: %+v", n)
	}
	if row := liveRowsByID(mustLive(t, e))[a.nodeID]; !row.Drift {
		t.Fatalf("setup: no persistent drift: %+v", row)
	}

	got, err := api.GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := api.StartRollout(e.ctx, connect.NewRequest(&adminv1.StartRolloutRequest{ExpectedVersion: got.Msg.Bundle.Version, ExpectedBuilt: got.Msg.Bundle.Built})); err != nil {
		t.Fatal(err)
	}
	msg := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetUpdateAgent() != nil }).GetUpdateAgent()
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
		RequestId: msg.RequestId, Ok: true, Affected: 1}}})
	c.st.CloseRequest() // re-exec
	c.ended()

	// the new build: same applied state, now observed right
	c2 := a.open()
	hl := hello("inst2", again.Revision, model.hash())
	hl.GetHello().Built, hl.GetHello().Capabilities = newBuilt, []string{"update/1", "update-guard/1"}
	c2.send(0, hl)
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	c2.send(1, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Severity: agentv1.Severity_SEVERITY_INFO, Code: "update_committed", TimeUnix: time.Now().Add(-clockBehind).Unix(),
		Params: map[string]string{"from_version": "0.1.0", "from_built": "1790000000", "to_version": "0.2.0-e2e", "to_built": "1791000000"}}}})

	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := api.GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.Msg.Rollout; r != nil && len(r.Steps) == 1 {
			switch r.Steps[0].State {
			case adminv1.StepState_STEP_STATE_PASSED:
				return
			case adminv1.StepState_STEP_STATE_ROLLED_BACK, adminv1.StepState_STEP_STATE_FAILED:
				t.Fatalf("the gate refused a node that runs the wanted state: %+v (node %+v)", r.Steps[0], mustNode(e, a.nodeID))
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the rollout did not finish: %+v", resp.Msg.Rollout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
