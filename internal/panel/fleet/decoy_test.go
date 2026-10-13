package fleet

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/decoy"
)

func TestDecoyCommandOmitsAnUnchangedBundle(t *testing.T) {
	bundle, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("site")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := decoyFilesFor(bundle, bundle.Hash()); len(got) != 0 {
		t.Fatalf("unchanged site sent %d files", len(got))
	}
	got := decoyFilesFor(bundle, "different")
	if len(got) != 1 || got[0].Path != "index.html" || string(got[0].Contents) != "site" {
		t.Fatalf("changed site payload = %+v", got)
	}
}

func TestOldAgentKeepsBuiltinAndReportsDecoyReason(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("panel site"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.f.cfg.DecoyDir = dir
	a := e.enroll("nodea")
	n, err := e.st.Node(e.ctx, a.nodeID)
	if err != nil || n.DecoyMode != decoy.ModePanel {
		t.Fatalf("new node mode = %q, err=%v; want panel when the panel site exists", n.DecoyMode, err)
	}
	c := a.open()
	c.send(0, hello("old-agent", 0, ""))
	c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	got, err := nodeService{e.f}.GetNode(e.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Msg.Node.DecoyReasonCode != "agent_too_old" || got.Msg.Node.DecoyAgentMode != decoy.ModeBuiltin || got.Msg.Node.DecoySupported {
		t.Fatalf("old agent decoy state = mode %q applied %q supported=%t reason=%q", got.Msg.Node.DecoyMode,
			got.Msg.Node.DecoyAgentMode, got.Msg.Node.DecoySupported, got.Msg.Node.DecoyReasonCode)
	}
}

func TestSetDecoyRequestIsCapabilityGated(t *testing.T) {
	frame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SetDecoy{SetDecoy: &agentv1.SetDecoy{Mode: decoy.ModeBuiltin}}}
	capability, message := requestCapability(frame)
	if capability != capDecoy || message != "agent too old" {
		t.Fatalf("SetDecoy capability = %q, %q", capability, message)
	}
}

func TestAdminDecoyUploadModeAndBulkChangesAreAudited(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	b := e.enroll("nodeb")
	bundle, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("own site")}})
	if err != nil {
		t.Fatal(err)
	}
	service := nodeService{e.f}
	for _, node := range []*agent{a, b} {
		if _, err := service.UploadNodeDecoy(e.ctx, connect.NewRequest(&adminv1.UploadNodeDecoyRequest{
			NodeId: node.nodeID, BundleArchive: bundle.Archive(),
		})); err != nil {
			t.Fatalf("upload node bundle: %v", err)
		}
	}
	bulk, err := service.SetNodesDecoyMode(e.ctx, connect.NewRequest(&adminv1.SetNodesDecoyModeRequest{
		NodeIds: []string{a.nodeID, b.nodeID}, Mode: decoy.ModeBuiltin,
	}))
	if err != nil || len(bulk.Msg.Nodes) != 2 {
		t.Fatalf("bulk set decoy mode = %v, %v", bulk, err)
	}
	rows, err := e.st.ListAudit(e.ctx, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	uploads, modes := map[string]bool{}, map[string]bool{}
	for _, row := range rows {
		var params map[string]string
		if err := json.Unmarshal([]byte(row.Params), &params); err != nil {
			continue
		}
		switch row.Action {
		case "node.decoy_upload":
			if params["mode"] != decoy.ModeOwn || params["bundle_sha256"] != bundle.Hash() {
				t.Errorf("upload audit params = %v", params)
			}
			uploads[params["node_id"]] = true
		case "node.decoy_mode":
			if params["mode"] != decoy.ModeBuiltin {
				t.Errorf("mode audit params = %v", params)
			}
			modes[params["node_id"]] = true
		}
	}
	if len(uploads) != 2 || len(modes) != 2 || !uploads[a.nodeID] || !uploads[b.nodeID] || !modes[a.nodeID] || !modes[b.nodeID] {
		t.Fatalf("decoy audit upload=%v mode=%v", uploads, modes)
	}
}

func writeSite(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func nextSetDecoy(c *conn) *agentv1.SetDecoy {
	c.t.Helper()
	return c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetSetDecoy() != nil }).GetSetDecoy()
}

// A node on the panel's site gets it once; a reconnect that reports the same hash gets nothing; an own bundle replaces
// it; a node that lost its copy (bad_params without files) is sent the whole tree once more.
func TestDecoySyncPushesOnlyWhatTheNodeLacks(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	writeSite(t, dir, "panel site")
	e.f.cfg.DecoyDir = dir
	a := e.enroll("nodea")
	panel, err := decoy.FromDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	c, _, _ := connectCaps(a, "inst-1", capDecoy)
	set := nextSetDecoy(c)
	if set.Mode != decoy.ModePanel || set.BundleSha256 != panel.Hash() || len(set.Files) != 1 || string(set.Files[0].Contents) != "panel site" {
		t.Fatalf("first push = %+v", set)
	}
	replyPortCommand(c, set.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"mode": set.Mode, "sha256": set.BundleSha256}})
	waitFor(t, func() bool {
		n, err := e.st.Node(e.ctx, a.nodeID)
		return err == nil && n.DecoyAgentMode == decoy.ModePanel && n.DecoyAgentHash == panel.Hash()
	})

	// Reconnect reporting the active site: nothing is pushed.
	c2 := a.open()
	h := hello("inst-1", 0, "")
	h.GetHello().Capabilities = []string{capDecoy}
	h.GetHello().DecoyMode, h.GetHello().DecoySha256 = decoy.ModePanel, panel.Hash()
	c2.send(0, h)
	c2.wait(func(m *agentv1.ConnectResponse) bool { return m.GetHelloAck() != nil })
	noSetDecoy(t, c2)

	// The same files uploaded as the node's own site: the node already has them, so none are sent. If it has lost them
	// it refuses with bad_params, and gets the whole tree once more.
	same, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("panel site")}})
	if err != nil || same.Hash() != panel.Hash() {
		t.Fatalf("same site hash %v, err %v", same, err)
	}
	if _, err := (nodeService{e.f}).UploadNodeDecoy(e.ctx, connect.NewRequest(&adminv1.UploadNodeDecoyRequest{NodeId: a.nodeID, BundleArchive: same.Archive()})); err != nil {
		t.Fatal(err)
	}
	set = nextSetDecoy(c2)
	if set.Mode != decoy.ModeOwn || set.BundleSha256 != panel.Hash() || len(set.Files) != 0 {
		t.Fatalf("same-hash push = %+v", set)
	}
	replyPortCommand(c2, set.RequestId, &agentv1.CommandResult{Error: "bad_params"})
	retry := nextSetDecoy(c2)
	if retry.BundleSha256 != panel.Hash() || len(retry.Files) != 1 {
		t.Fatalf("retry = %+v", retry)
	}
	replyPortCommand(c2, retry.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"mode": retry.Mode, "sha256": retry.BundleSha256}})

	// A different own bundle replaces it, and only that bundle is sent.
	own, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("own site")}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		n, err := e.st.Node(e.ctx, a.nodeID)
		return err == nil && n.DecoyAgentMode == decoy.ModeOwn && n.DecoyAgentHash == panel.Hash()
	})
	if _, err := (nodeService{e.f}).UploadNodeDecoy(e.ctx, connect.NewRequest(&adminv1.UploadNodeDecoyRequest{NodeId: a.nodeID, BundleArchive: own.Archive()})); err != nil {
		t.Fatal(err)
	}
	set = nextSetDecoy(c2)
	if set.Mode != decoy.ModeOwn || set.BundleSha256 != own.Hash() || len(set.Files) != 1 || string(set.Files[0].Contents) != "own site" {
		t.Fatalf("own push = %+v", set)
	}
	replyPortCommand(c2, set.RequestId, &agentv1.CommandResult{Ok: true, Params: map[string]string{"mode": set.Mode, "sha256": set.BundleSha256}})
	waitFor(t, func() bool {
		n, err := e.st.Node(e.ctx, a.nodeID)
		return err == nil && n.DecoyAgentMode == decoy.ModeOwn && n.DecoyAgentHash == own.Hash()
	})

	// Back to the built-in page: no files, no hash.
	if _, err := (nodeService{e.f}).SetNodeDecoyMode(e.ctx, connect.NewRequest(&adminv1.SetNodeDecoyModeRequest{NodeId: a.nodeID, Mode: decoy.ModeBuiltin})); err != nil {
		t.Fatal(err)
	}
	set = nextSetDecoy(c2)
	if set.Mode != decoy.ModeBuiltin || set.BundleSha256 != "" || len(set.Files) != 0 {
		t.Fatalf("builtin push = %+v", set)
	}
}

func noSetDecoy(t *testing.T, c *conn) {
	t.Helper()
	deadline := time.After(700 * time.Millisecond)
	for {
		select {
		case m := <-c.in:
			if m.GetSetDecoy() != nil {
				t.Fatalf("unexpected SetDecoy: %+v", m.GetSetDecoy())
			}
		case <-deadline:
			return
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

// The reason a panel site cannot be used is told apart, and the same 2 MiB cap as an own bundle applies to it.
func TestPanelDecoyReasonCodes(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	if _, err := (nodeService{e.f}).SetNodeDecoyMode(e.ctx, connect.NewRequest(&adminv1.SetNodeDecoyModeRequest{NodeId: a.nodeID, Mode: decoy.ModePanel})); err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("panel mode without a --decoy-dir = %v", err)
	}
	for _, tc := range []struct {
		name  string
		setup func(dir string)
		want  string
	}{
		{"no index", func(dir string) { _ = os.WriteFile(filepath.Join(dir, "about.html"), []byte("x"), 0o600) }, "panel_no_index"},
		{"over 2 MiB", func(dir string) {
			writeSite(t, dir, "x")
			_ = os.WriteFile(filepath.Join(dir, "big.bin"), make([]byte, decoy.MaxBundleBytes), 0o600)
		}, "panel_too_large"},
	} {
		dir := t.TempDir()
		tc.setup(dir)
		e.f.cfg.DecoyDir = dir
		e.f.panelDecoyAt = time.Time{}
		_, err := (nodeService{e.f}).SetNodeDecoyMode(e.ctx, connect.NewRequest(&adminv1.SetNodeDecoyModeRequest{NodeId: a.nodeID, Mode: decoy.ModePanel}))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: SetNodeDecoyMode = %v, want %s", tc.name, err, tc.want)
		}
	}
}

// An upload is refused with a coded reason the admin words.
func TestUploadNodeDecoyRefusalIsCoded(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	_ = tw.WriteHeader(&tar.Header{Name: "../x.html", Mode: 0o644, Size: 1})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	_, err := (nodeService{e.f}).UploadNodeDecoy(e.ctx, connect.NewRequest(&adminv1.UploadNodeDecoyRequest{NodeId: a.nodeID, BundleArchive: tarBuf.Bytes()}))
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeInvalidArgument || !strings.HasPrefix(ce.Message(), "decoy_bundle_bad_path: path=") {
		t.Fatalf("UploadNodeDecoy(../x.html) = %v", err)
	}
}
