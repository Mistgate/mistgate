package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"golang.org/x/crypto/ssh"
)

func TestServerKeyManagementAndAttachNodeServerAccess(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	keyStore := &Service{st: st, vault: vlt, cfg: Config{Now: time.Now}}
	panelKey, _, err := keyStore.ensurePanelSSHKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	panelPublic, _, _, _, err := ssh.ParseAuthorizedKey([]byte(panelKey.PublicKey))
	if err != nil {
		t.Fatal(err)
	}

	var remoteMu sync.Mutex
	var authorized []byte
	var authorizedExists bool
	preflightNodeID := "nod_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	preflight := func() []byte {
		return []byte(strings.Join([]string{
			"distribution=ubuntu", "version=22.04", "kernel=6.8.0", "architecture=x86_64",
			"cpu_count=4", "memory_bytes=1073741824", "disk_available_bytes=1073741824",
			"systemd=yes", "already_enrolled=yes", "existing_node_id=" + preflightNodeID, "panel_reachable=yes", "",
		}, "\n"))
	}
	server := newFakeSSHD(t, loginMethods{password: true, key: true}, func(command string, channel ssh.Channel) ([]byte, error) {
		switch {
		case strings.Contains(command, "panel_reachable=%s"):
			return preflight(), nil
		case strings.Contains(command, "sha256:%s"): // the read script; the write script prints no digest
			remoteMu.Lock()
			defer remoteMu.Unlock()
			if !authorizedExists {
				return []byte("none\n"), nil
			}
			sum := sha256.Sum256(authorized)
			return append([]byte("sha256:"+hex.EncodeToString(sum[:])+"\n"), authorized...), nil
		case strings.Contains(command, "cur=none"):
			parts := strings.Fields(command)
			if len(parts) == 0 {
				return nil, errors.New("authorized_keys script has no CAS value")
			}
			expected := strings.Trim(parts[len(parts)-1], "'")
			remoteMu.Lock()
			defer remoteMu.Unlock()
			current := "none"
			if authorizedExists {
				sum := sha256.Sum256(authorized)
				current = hex.EncodeToString(sum[:])
			}
			if current != expected {
				return nil, errors.New("authorized_keys compare-and-swap mismatch")
			}
			contents, err := io.ReadAll(channel)
			if err != nil {
				return nil, err
			}
			authorized, authorizedExists = contents, true
			return nil, nil
		default:
			return nil, errors.New("unexpected remote command")
		}
	})
	server.authorizeKey(panelPublic)

	const managedNodeID = "nod_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	const attachedNodeID = "nod_bbbbbbbbbbbbbbbbbbbbbbbbbb"
	now := time.Unix(1_800_000_000, 0).UTC()
	for _, node := range []struct{ id, name string }{{managedNodeID, "keys-1"}, {attachedNodeID, "attach-1"}} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', ?)`, node.id, node.name, "edge.example.com", now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	passwordCipher := vlt.Seal([]byte(authTestPassword), "node-access:"+managedNodeID)
	if err := st.InsertNodeServerAccess(ctx, store.NodeServerAccess{
		NodeID: managedNodeID, SSHHost: "keys.example.com", SSHPort: 22, SSHUser: "root",
		HostFingerprint: server.fingerprint(), Password: passwordCipher, PasswordLogin: true, ConfiguredAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	sshClient := server.client()
	sshClient.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
	svc, err := NewService(st, vlt, Config{
		PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com", Nodes: testNodeManager{}, Binaries: testBinarySource{}, SSH: sshClient,
		StepUp: func(context.Context) error { return nil }, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	accessTarget := &adminv1.AddNodeServerKeyRequest{NodeId: managedNodeID, PanelKey: true, Confirm: true}
	added, err := svc.AddNodeServerKey(ctx, connect.NewRequest(accessTarget))
	if err != nil || added.Msg.GetKey().GetFingerprint() != panelKey.Fingerprint || !added.Msg.GetKey().GetPanelKey() {
		t.Fatalf("add panel key: response %+v, err %v", added, err)
	}
	listed, err := svc.ListNodeServerKeys(ctx, connect.NewRequest(&adminv1.ListNodeServerKeysRequest{NodeId: managedNodeID}))
	if err != nil || len(listed.Msg.GetKeys()) != 1 || !listed.Msg.GetKeys()[0].GetPanelKey() {
		t.Fatalf("list panel key: response %+v, err %v", listed, err)
	}
	storedPanelAccess, err := st.NodeServerAccess(ctx, managedNodeID)
	if err != nil {
		t.Fatal(err)
	} else if storedPanelAccess.PanelKeyFingerprint != panelKey.Fingerprint {
		t.Fatalf("verified panel fingerprint was not saved: %q", storedPanelAccess.PanelKeyFingerprint)
	}
	if err := st.SetNodeServerPasswordLogin(ctx, managedNodeID, false); err != nil {
		t.Fatal(err)
	}

	removed, err := svc.RemoveNodeServerKey(ctx, connect.NewRequest(&adminv1.RemoveNodeServerKeyRequest{
		NodeId: managedNodeID, Fingerprint: panelKey.Fingerprint, Confirm: true, ConfirmPanelKey: true,
	}))
	if err != nil || removed.Msg == nil {
		t.Fatalf("remove panel key after proving saved password: response %+v, err %v", removed, err)
	}
	stored, err := st.NodeServerAccess(ctx, managedNodeID)
	if err != nil || stored.PanelKeyFingerprint != "" || !stored.PasswordLogin {
		t.Fatalf("password-only state after panel-key removal: %+v, err %v", stored, err)
	}
	listed, err = svc.ListNodeServerKeys(ctx, connect.NewRequest(&adminv1.ListNodeServerKeysRequest{NodeId: managedNodeID}))
	if err != nil || len(listed.Msg.GetKeys()) != 0 {
		t.Fatalf("list after removing panel key: response %+v, err %v", listed, err)
	}

	_, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(ownerPrivate)
	ownerPublic, err := ssh.NewPublicKey(ownerPrivate.Public())
	if err != nil {
		t.Fatal(err)
	}
	ownerLine := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(ownerPublic)), "\n") + " owner-laptop"
	ownerKey, err := svc.AddNodeServerKey(ctx, connect.NewRequest(&adminv1.AddNodeServerKeyRequest{NodeId: managedNodeID, PublicKey: ownerLine, Confirm: true}))
	if err != nil || ownerKey.Msg.GetKey().GetPanelKey() {
		t.Fatalf("add owner public key: response %+v, err %v", ownerKey, err)
	}
	listed, err = svc.ListNodeServerKeys(ctx, connect.NewRequest(&adminv1.ListNodeServerKeysRequest{NodeId: managedNodeID}))
	if err != nil || len(listed.Msg.GetKeys()) != 1 || listed.Msg.GetKeys()[0].GetFingerprint() != ownerKey.Msg.GetKey().GetFingerprint() {
		t.Fatalf("list owner key: response %+v, err %v", listed, err)
	}
	if _, err := svc.RemoveNodeServerKey(ctx, connect.NewRequest(&adminv1.RemoveNodeServerKeyRequest{
		NodeId: managedNodeID, Fingerprint: ownerKey.Msg.GetKey().GetFingerprint(), Confirm: true,
	})); err != nil {
		t.Fatalf("remove owner key: %v", err)
	}

	attachReq := &adminv1.AttachNodeServerAccessRequest{
		NodeId: attachedNodeID, SshHost: "keys.example.com", SshPort: 22, Fingerprint: server.fingerprint(),
		Username: "root", Password: authTestPassword, InstallPanelKey: true, Confirm: true,
	}
	attached, err := svc.AttachNodeServerAccess(ctx, connect.NewRequest(attachReq))
	if err != nil {
		t.Fatalf("attach access after matching preflight identity: %v", err)
	}
	if attachReq.GetPassword() != "" || attached.Msg.GetAccess().GetNodeId() != attachedNodeID || !attached.Msg.GetAccess().GetPasswordSaved() ||
		!attached.Msg.GetAccess().GetPasswordLogin() || attached.Msg.GetAccess().GetPanelKeyFingerprint() != panelKey.Fingerprint || attached.Msg.GetPanelKeyError() != "" {
		t.Fatalf("attached access response or request cleanup: response %+v, password cleared %t", attached.Msg, attachReq.GetPassword() == "")
	}
	if _, err := st.NodeServerAccess(ctx, attachedNodeID); err != nil {
		t.Fatalf("attached access was not persisted: %v", err)
	}

	badAttach := &adminv1.AttachNodeServerAccessRequest{
		NodeId: "nod_cccccccccccccccccccccccccc", SshHost: "keys.example.com", SshPort: 22, Fingerprint: server.fingerprint(),
		Username: "root", Password: authTestPassword, Confirm: true,
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, 'attach-2', 'edge.example.com', 'active', ?)`, badAttach.GetNodeId(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	preflightNodeID = attachedNodeID
	if _, err := svc.AttachNodeServerAccess(ctx, connect.NewRequest(badAttach)); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "node_identity_mismatch") {
		t.Fatalf("attach whose remote identity did not match: %v", err)
	}
	if badAttach.GetPassword() != "" {
		t.Fatal("failed attach retained the request password")
	}
	if _, err := st.NodeServerAccess(ctx, badAttach.GetNodeId()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("identity mismatch persisted access: %v", err)
	}
}
