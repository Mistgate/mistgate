package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"golang.org/x/crypto/ssh"
)

// nftablesRun installs a node over a fake SSH server whose preflight says it has no nft command, and answers the
// install script with installErr. It returns the job, its journal and the commands the server saw.
func nftablesRun(t *testing.T, installErr error) (store.NodeProvisionJob, []store.NodeProvisionEvent, []string, []byte) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	program := []byte("trusted-agent-binary")
	binaryPath := filepath.Join(t.TempDir(), "mistgate-node")
	if err := os.WriteFile(binaryPath, program, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(program)
	manager := &e2eNodeManager{}

	var mu sync.Mutex
	var commands []string
	var script []byte
	var stored []byte
	signer := testSigner(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil }}
	serverConfig.AddHostKey(signer)
	go acceptProvisionSSH(t, listener, serverConfig, func(wireCommand string, channel ssh.Channel) ([]byte, error) {
		command, privileged := unwrapPrivilegedCommand(wireCommand)
		if !privileged {
			return nil, errors.New("remote command did not use noninteractive sudo")
		}
		mu.Lock()
		commands = append(commands, command)
		mu.Unlock()
		switch {
		case strings.Contains(command, "panel_reachable=%s"):
			return []byte(strings.Join([]string{
				"distribution=debian", "version=13", "kernel=6.12.73+deb13-cloud-amd64", "architecture=x86_64",
				"cpu_count=2", "memory_bytes=2147483648", "disk_available_bytes=10737418240",
				"systemd=yes", "nftables=no", "already_enrolled=no", "existing_node_id=", "panel_reachable=yes", "",
			}, "\n")), nil
		case command == "sh -s":
			data, _ := io.ReadAll(channel)
			mu.Lock()
			script = data
			mu.Unlock()
			return nil, installErr
		case strings.HasPrefix(command, "sh -s -- "):
			_, err := io.ReadAll(channel)
			return nil, err
		case command == "cat > /root/mistgate-node.new":
			data, err := io.ReadAll(channel)
			mu.Lock()
			stored = data
			mu.Unlock()
			return nil, err
		case strings.HasPrefix(command, "sha256sum "):
			mu.Lock()
			sum := sha256.Sum256(stored)
			mu.Unlock()
			return []byte(hex.EncodeToString(sum[:]) + "  /root/mistgate-node.new\n"), nil
		case strings.HasPrefix(command, "/root/mistgate-node enroll "):
			_, err := io.ReadAll(channel)
			return nil, err
		case command == "/root/mistgate-node install":
			manager.setConnected()
			return nil, nil
		case strings.HasPrefix(command, "install -o root -g root -m 0755 "):
			return nil, nil
		default:
			return nil, errors.New("unexpected remote command")
		}
	})

	sshClient := NewClient()
	sshClient.timeout = 2 * time.Second
	sshClient.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
	sshClient.dialer = testDialer(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, listener.Addr().String())
	})
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(st, vlt, Config{
		Nodes: manager, Binaries: e2eBinarySource{path: binaryPath, digest: hex.EncodeToString(digest[:])},
		PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com",
		SSH: sshClient, StepUp: func(context.Context) error { return nil }, WaitOnline: time.Second,
		Now: func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	job := store.NodeProvisionJob{
		ID: "prv_nft", NodeID: "nod_aaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "de1",
		Address: "de1.example.com", SSHHost: "node.example.com", SSHPort: 22,
		HostFingerprint: ssh.FingerprintSHA256(signer.PublicKey()), CreatedBy: "adm_test",
		CreatedAt: time.Unix(1_800_000_000, 0).UTC(), UpdatedAt: time.Unix(1_800_000_000, 0).UTC(),
	}
	job.Secret, err = svc.sealCredentials(job.ID, credentials{Username: "deploy", Password: "deploy-password-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'pending', ?)`,
		job.NodeID, job.Name, job.Address, job.CreatedAt.Unix()); err != nil {
		t.Fatal(err)
	}

	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- svc.Run(workerCtx) }()
	var got store.NodeProvisionJob
	deadline := time.After(10 * time.Second)
	for got.State != "completed" && got.State != "failed" {
		if got, err = st.NodeProvisionJob(ctx, job.ID); err != nil {
			t.Fatal(err)
		}
		select {
		case <-deadline:
			t.Fatalf("the job did not end: %q/%q", got.State, got.Phase)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
	events, _, err := st.NodeProvisionEvents(ctx, job.ID, 0, 30)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return got, events, append([]string(nil), commands...), script
}

func journalCodes(events []store.NodeProvisionEvent) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Code)
	}
	return out
}

func indexOf(list []string, want string) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

// A server without nft: the install adds its own step, named in the journal before the firewall step, runs exactly the
// install script, and goes on to the agent.
func TestInstallAddsNftablesAsAStepOfItsOwn(t *testing.T) {
	job, events, commands, script := nftablesRun(t, nil)
	if job.State != "completed" {
		t.Fatalf("job %q/%q code %q, commands %q", job.State, job.Phase, job.ErrorCode, commands)
	}
	if string(script) != remoteNftablesInstallScript {
		t.Fatalf("the server got a different script: %q", script)
	}
	codes := journalCodes(events)
	step, firewall := indexOf(codes, "installing_nftables"), indexOf(codes, "preparing_host_firewall")
	if step < 0 || firewall < 0 || step > firewall || indexOf(codes, "checking_host") > step {
		t.Fatalf("the package step must come after the server check and before the firewall: %v", codes)
	}
	for _, e := range events {
		if e.Code == "installing_nftables" && e.Phase != "nftables" {
			t.Fatalf("the step is in phase %q", e.Phase)
		}
	}
	if i, j := indexOf(commands, "sh -s"), indexOf(commands, "sh -s -- 22"); i < 0 || j < i || indexOf(commands, "/root/mistgate-node install") < j {
		t.Fatalf("commands %q", commands)
	}
}

// A server where the package cannot be installed stops at that step with its own code: nothing is uploaded, enrolled
// or started.
func TestInstallStopsWhereNftablesCannotBeInstalled(t *testing.T) {
	job, events, commands, _ := nftablesRun(t, errors.New("exit status 21"))
	if job.State != "failed" || job.ErrorCode != "nftables_install_failed" || job.Phase != "failed" {
		t.Fatalf("job %q/%q code %q", job.State, job.Phase, job.ErrorCode)
	}
	codes := journalCodes(events)
	if codes[len(codes)-1] != "nftables_install_failed" || indexOf(codes, "installing_nftables") < 0 || indexOf(codes, "preparing_host_firewall") >= 0 {
		t.Fatalf("journal %v", codes)
	}
	for _, c := range commands {
		if strings.HasPrefix(c, "cat >") || strings.HasPrefix(c, "/root/mistgate-node") || strings.HasPrefix(c, "sh -s -- ") {
			t.Fatalf("the install went on after the package step failed: %q", commands)
		}
	}
}
