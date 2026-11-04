package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"golang.org/x/crypto/ssh"
)

func TestCheckSSHUsesOwnersPrivateKeyAndMapsPassphraseErrors(t *testing.T) {
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
	_, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(ownerPrivate)
	ownerSigner, err := ssh.NewSignerFromKey(ownerPrivate)
	if err != nil {
		t.Fatal(err)
	}
	const passphrase = "owner-key-passphrase"
	block, err := ssh.MarshalPrivateKeyWithPassphrase(ownerPrivate, "owner laptop", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(block))
	exec := func(command string, _ ssh.Channel) ([]byte, error) {
		if !strings.Contains(command, "panel_reachable=%s") {
			return nil, errors.New("unexpected preflight command")
		}
		return []byte(strings.Join([]string{
			"distribution=ubuntu", "version=22.04", "kernel=6.8.0", "architecture=x86_64",
			"cpu_count=4", "memory_bytes=1073741824", "disk_available_bytes=1073741824",
			"systemd=yes", "already_enrolled=no", "existing_node_id=", "panel_reachable=yes", "",
		}, "\n")), nil
	}
	server := newFakeSSHD(t, loginMethods{key: true}, exec)
	server.authorizeKey(ownerSigner.PublicKey())
	sshClient := server.client()
	sshClient.timeout = 2 * time.Second
	sshClient.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
	svc, err := NewService(st, vlt, Config{
		PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com", Nodes: testNodeManager{}, Binaries: testBinarySource{}, SSH: sshClient,
		StepUp: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(pass string) *adminv1.CheckSSHRequest {
		return &adminv1.CheckSSHRequest{
			Host: "node.example.com", Port: 22, Fingerprint: server.fingerprint(), Username: "root",
			PrivateKey: privatePEM, PrivateKeyPassphrase: pass,
		}
	}
	missing := request("")
	if _, err := svc.CheckSSH(ctx, connect.NewRequest(missing)); connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "ssh_key_passphrase_required") {
		t.Fatalf("missing private-key passphrase: %v", err)
	}
	if missing.GetPrivateKey() != "" || missing.GetPrivateKeyPassphrase() != "" {
		t.Fatal("CheckSSH retained the encrypted private key after parse failure")
	}
	wrong := request("incorrect")
	if _, err := svc.CheckSSH(ctx, connect.NewRequest(wrong)); connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "ssh_key_passphrase_invalid") {
		t.Fatalf("wrong private-key passphrase: %v", err)
	}
	if wrong.GetPrivateKey() != "" || wrong.GetPrivateKeyPassphrase() != "" {
		t.Fatal("CheckSSH retained the encrypted private key after passphrase failure")
	}
	valid := request(passphrase)
	checked, err := svc.CheckSSH(ctx, connect.NewRequest(valid))
	if err != nil || checked.Msg.GetPreflight().GetDistribution() != "ubuntu" {
		t.Fatalf("CheckSSH with the owner key: response %+v, err %v", checked, err)
	}
	if valid.GetPrivateKey() != "" || valid.GetPrivateKeyPassphrase() != "" {
		t.Fatal("CheckSSH retained the owner key after success")
	}
	if attempts := server.keyAttemptCount(); attempts != 1 {
		t.Fatalf("SSH public-key login attempts = %d, want one successful attempt", attempts)
	}
}
