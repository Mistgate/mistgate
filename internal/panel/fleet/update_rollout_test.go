package fleet

import (
	"bytes"
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
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
	"github.com/mistgate/mistgate/internal/panel/update"
	"github.com/mistgate/mistgate/internal/release"
)

// The whole panel side over real sockets, with a fake agent that does what agent.proto "UPDATE" says: a rollout is
// started through the admin UpdateService, the agent receives UpdateAgent over its mTLS stream, verifies the
// signature, downloads its file through FetchUpdate (and resumes after a dropped download), answers, re-executes
// (the stream ends), comes back with the new build, raises update_committed, and the rollout ends DONE, with the
// node UPDATING in between and no blip recorded.
func TestRolloutOverTheAgentEndpoint(t *testing.T) {
	const oldBuilt, newBuilt = 1_790_000_000, 1_791_000_000
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	e.run()

	// the owner's bundle: one binary per platform, big enough for several chunks
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
	binary := make([]byte, 700<<10)
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

	// the updates module on the real fleet (*Fleet is its update.Fleet), its admin service on a test server
	upd, err := update.New(e.st, e.f, nil, update.Config{
		DataDir: dataDir, Key: pub, PanelVersion: "0.2.0", PanelBuilt: newBuilt, StepUp: func(context.Context) error { return nil },
		Actor:  func(context.Context) string { return "adm_test" },
		Timing: update.Timing{Tick: 20 * time.Millisecond, GateWait: 20 * time.Second},
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

	// the agent is connected with its old build and the capability
	c := a.open()
	c.send(0, helloWithUpdate("inst1", oldBuilt, "update/1", "update-guard/1"))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	ds := c.desired()
	model := newModel()
	model.apply(ds)
	c.send(0, applied(ds, model.hash()))

	got, err := api.GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	nodeOf := func(r *adminv1.GetUpdatesResponse) *adminv1.NodeUpdate {
		for _, n := range r.Nodes {
			if n.NodeId == a.nodeID {
				return n
			}
		}
		t.Fatalf("node %s is not listed: %+v", a.nodeID, r.Nodes)
		return nil
	}
	if n := nodeOf(got.Msg); got.Msg.Bundle.Status != adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED ||
		n.State != adminv1.NodeUpdateState_NODE_UPDATE_STATE_OUTDATED || !n.SupportsUpdate {
		t.Fatalf("before the rollout: %+v", got.Msg)
	}

	started, err := api.StartRollout(e.ctx, connect.NewRequest(&adminv1.StartRolloutRequest{}))
	if err != nil || started.Msg.Rollout.Status != adminv1.RolloutStatus_ROLLOUT_STATUS_RUNNING || len(started.Msg.Rollout.Steps) != 1 {
		t.Fatalf("start: %+v %v", started, err)
	}

	// --- the agent's side of the update
	msg := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetUpdateAgent() != nil }).GetUpdateAgent()
	man, err := release.Verify(pub, msg.Manifest, msg.Signature)
	if err != nil {
		t.Fatalf("the manifest the panel relays does not verify: %v", err)
	}
	file, err := man.FileFor("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	cli := agentv1connect.NewAgentServiceClient(e.httpClient(&a.cert, testSNI), e.srv.URL)
	var blob []byte
	fetch := func(offset uint64, stopAfter int) {
		t.Helper()
		st, err := cli.FetchUpdate(e.ctx, connect.NewRequest(&agentv1.FetchUpdateRequest{Name: file.Name, Offset: offset}))
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for st.Receive() {
			if st.Msg().TotalSize != uint64(file.Size) {
				t.Fatalf("total_size %d, manifest says %d", st.Msg().TotalSize, file.Size)
			}
			if len(st.Msg().Data) > 256<<10 {
				t.Fatalf("a chunk of %d bytes", len(st.Msg().Data))
			}
			blob = append(blob, st.Msg().Data...)
			if n++; stopAfter > 0 && n == stopAfter {
				st.Close() // the download drops
				return
			}
		}
		if err := st.Err(); err != nil {
			t.Fatal(err)
		}
	}
	fetch(0, 1)                 // one chunk, then the connection dies
	fetch(uint64(len(blob)), 0) // resume from the byte it got to
	if err := release.VerifyFile(file, bytes.NewReader(blob)); err != nil {
		t.Fatalf("the downloaded file does not match the manifest: %v", err)
	}
	c.send(0, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
		RequestId: msg.RequestId, Ok: true, Affected: 1}}})
	c.st.CloseRequest() // re-exec
	c.ended()

	// between the two processes the node is UPDATING, not DOWN or BLIP
	deadline := time.Now().Add(5 * time.Second)
	for e.f.NodeStatus(e.ctx, mustNode(e, a.nodeID)) != adminv1.NodeStatus_NODE_STATUS_UPDATING {
		if time.Now().After(deadline) {
			t.Fatalf("node status during the re-exec: %v", e.f.NodeStatus(e.ctx, mustNode(e, a.nodeID)))
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.exec(`UPDATE node SET last_seen_at = last_seen_at - 180, last_disconnected_at = last_disconnected_at - 180, last_connected_at = last_connected_at - 180 WHERE id = ?`, a.nodeID)

	// the new process: new build, same applied state, commits
	c2 := a.open()
	hl := hello("inst2", ds.Revision, model.hash())
	hl.GetHello().Built, hl.GetHello().Capabilities = newBuilt, []string{"update/1", "update-guard/1"}
	c2.send(0, hl)
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	c2.send(1, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Severity: agentv1.Severity_SEVERITY_INFO, Code: "update_committed", TimeUnix: time.Now().Unix(),
		Params: map[string]string{"from_version": "0.1.0", "from_built": "1790000000", "to_version": "0.2.0-e2e", "to_built": "1791000000"}}}})

	deadline = time.Now().Add(10 * time.Second)
	for {
		resp, err := api.GetUpdates(e.ctx, connect.NewRequest(&adminv1.GetUpdatesRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if r := resp.Msg.Rollout; r != nil && r.Status == adminv1.RolloutStatus_ROLLOUT_STATUS_DONE {
			if len(r.Steps) != 1 || r.Steps[0].State != adminv1.StepState_STEP_STATE_PASSED {
				t.Fatalf("steps %+v", r.Steps)
			}
			n := nodeOf(resp.Msg)
			if n.State != adminv1.NodeUpdateState_NODE_UPDATE_STATE_UP_TO_DATE || n.Built != newBuilt {
				t.Fatalf("node after the rollout: %+v", n)
			}
			// the commit event alone records the outcome: no Hello has carried it yet
			if lu := n.LastUpdate; lu == nil || lu.Outcome != "ok" || lu.ToBuilt != newBuilt || lu.FromBuilt != 1_790_000_000 || lu.ToVersion != "0.2.0-e2e" {
				t.Fatalf("last_update right after the commit: %+v", lu)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the rollout did not finish: %+v", resp.Msg.Rollout)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := e.count(`SELECT count(*) FROM event WHERE code IN ('node_blip', 'node_recovered', 'node_down')`); n != 0 {
		t.Fatalf("%d blip or outage events for a planned re-exec", n)
	}
	if st := e.f.NodeStatus(e.ctx, mustNode(e, a.nodeID)); st != adminv1.NodeStatus_NODE_STATUS_ONLINE {
		t.Fatalf("node status after the rollout: %v", st)
	}
}
