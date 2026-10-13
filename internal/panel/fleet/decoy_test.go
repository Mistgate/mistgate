package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
