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

func TestWorkerInstallsNodeOverPinnedSSHAndRedactsSecrets(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	manager := &e2eNodeManager{}
	program := []byte("trusted-agent-binary")
	binaryPath := filepath.Join(t.TempDir(), "mistgate-node")
	if err := os.WriteFile(binaryPath, program, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(program)
	binaries := e2eBinarySource{path: binaryPath, digest: hex.EncodeToString(digest[:])}

	password := "root-password-secret"
	token := "one-time-enrollment-secret"
	commands := make(chan string, 8)
	uploaded := make(chan []byte, 1)
	enrolled := make(chan string, 1)
	passwords := make(chan string, 1)
	signer := testSigner(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverConfig := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, got []byte) (*ssh.Permissions, error) {
		passwords <- string(got)
		if string(got) != password {
			return nil, errors.New("bad test password")
		}
		return nil, nil
	}}
	serverConfig.AddHostKey(signer)
	go acceptProvisionSSH(t, listener, serverConfig, func(command string, channel ssh.Channel) ([]byte, error) {
		commands <- command
		switch {
		case strings.Contains(command, "panel_reachable=%s"):
			return []byte(strings.Join([]string{
				"distribution=ubuntu", "version=22.04", "kernel=6.8.0", "architecture=x86_64",
				"cpu_count=4", "memory_bytes=1073741824", "disk_available_bytes=1073741824",
				"systemd=yes", "already_enrolled=no", "existing_node_id=", "panel_reachable=yes", "",
			}, "\n")), nil
		case command == "cat > /root/mistgate-node.new":
			data, err := io.ReadAll(channel)
			if err == nil {
				uploaded <- data
			}
			return nil, err
		case strings.HasPrefix(command, "/root/mistgate-node enroll "):
			data, err := io.ReadAll(channel)
			if err == nil {
				enrolled <- string(data)
			}
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
		Nodes: manager, Binaries: binaries, PanelAddr: "panel.example.com:443", AgentSNI: "agent.example.com",
		SSH: sshClient, StepUp: func(context.Context) error { return nil }, WaitOnline: time.Second,
		Now: func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	job := store.NodeProvisionJob{
		ID: "prv_e2e", NodeID: "nod_aaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "edge-e2e",
		Address: "edge.example.com", SSHHost: "node.example.com", SSHPort: 22,
		HostFingerprint: ssh.FingerprintSHA256(signer.PublicKey()), CreatedBy: "adm_test",
		CreatedAt: time.Unix(1_800_000_000, 0).UTC(), UpdatedAt: time.Unix(1_800_000_000, 0).UTC(),
	}
	job.Secret, err = svc.sealCredentials(job.ID, credentials{Password: password})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}

	workerCtx, cancel := context.WithCancel(ctx)
	workerDone := make(chan error, 1)
	go func() { workerDone <- svc.Run(workerCtx) }()
	deadline := time.After(5 * time.Second)
	for {
		got, err := st.NodeProvisionJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == "completed" {
			if len(got.Secret) != 0 {
				t.Fatal("completed job retained sealed credentials")
			}
			break
		}
		if got.State == "failed" {
			var seen []string
			for len(commands) > 0 {
				seen = append(seen, <-commands)
			}
			t.Fatalf("provisioning failed with code %q; remote commands: %q", got.ErrorCode, seen)
		}
		select {
		case <-deadline:
			t.Fatalf("provisioning did not complete; last state was %q/%q", got.State, got.Phase)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-workerDone; err != nil {
		t.Fatalf("worker returned an error after cancellation: %v", err)
	}

	if got := <-passwords; got != password {
		t.Fatalf("SSH password = %q", got)
	}
	if got := <-uploaded; string(got) != string(program) {
		t.Fatalf("uploaded agent = %q", got)
	}
	if got := <-enrolled; got != token+"\n" {
		t.Fatalf("enrollment token stdin = %q", got)
	}
	manager.mu.Lock()
	if manager.enrollmentToken != token || !manager.connected {
		t.Fatalf("node manager state = token %q, connected %v", manager.enrollmentToken, manager.connected)
	}
	manager.mu.Unlock()

	close(commands)
	commandCount := 0
	for command := range commands {
		commandCount++
		if strings.Contains(command, password) || strings.Contains(command, token) {
			t.Fatalf("credential appeared in SSH command: %q", command)
		}
	}
	if commandCount != 5 {
		t.Fatalf("remote command count = %d, want 5", commandCount)
	}
	events, _, err := st.NodeProvisionEvents(ctx, job.ID, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 8 || events[0].Code != "created" || events[len(events)-1].Code != "agent_connected" {
		t.Fatalf("provisioning journal = %+v", events)
	}
	for _, event := range events {
		if strings.Contains(event.Code, password) || strings.Contains(event.Code, token) {
			t.Fatalf("credential appeared in journal: %+v", event)
		}
	}
}

type e2eNodeManager struct {
	mu              sync.Mutex
	state           string
	enrollmentToken string
	connected       bool
}

func (m *e2eNodeManager) CreateProvisionEnrollment(_ context.Context, _ NodeSpec, _ string, _, _ time.Time) (string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state, m.enrollmentToken = "pending", "one-time-enrollment-secret"
	return m.enrollmentToken, strings.Repeat("a", 64), nil
}

func (m *e2eNodeManager) ProvisionNodeState(context.Context, string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == "" {
		return "", store.ErrNotFound
	}
	return m.state, nil
}

func (m *e2eNodeManager) ProvisionNodeConnected(string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

func (m *e2eNodeManager) setConnected() {
	m.mu.Lock()
	m.connected = true
	m.mu.Unlock()
}

type e2eBinarySource struct {
	path, digest string
}

func (b e2eBinarySource) OpenNodeBinary(_, _ string) (*os.File, int64, string, error) {
	file, err := os.Open(b.path)
	if err != nil {
		return nil, 0, "", err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, 0, "", err
	}
	return file, info.Size(), b.digest, nil
}

type provisionSSHExecutor func(command string, channel ssh.Channel) ([]byte, error)

func acceptProvisionSSH(t *testing.T, listener net.Listener, config *ssh.ServerConfig, execute provisionSSHExecutor) {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		t.Logf("test SSH listener ended: %v", err)
		return
	}
	go func() {
		server, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			t.Logf("test SSH handshake failed: %v", err)
			conn.Close()
			return
		}
		defer server.Close()
		go ssh.DiscardRequests(requests)
		for newChannel := range channels {
			channel, channelRequests, err := newChannel.Accept()
			if err != nil {
				t.Logf("test SSH channel accept failed: %v", err)
				continue
			}
			go func() {
				defer channel.Close()
				for request := range channelRequests {
					if request.Type != "exec" {
						_ = request.Reply(false, nil)
						continue
					}
					var execRequest struct{ Command string }
					if err := ssh.Unmarshal(request.Payload, &execRequest); err != nil {
						_ = request.Reply(false, nil)
						return
					}
					_ = request.Reply(true, nil)
					output, err := execute(execRequest.Command, channel)
					if len(output) > 0 {
						_, _ = channel.Write(output)
					}
					status := uint32(0)
					if err != nil {
						status = 1
					}
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
					return
				}
			}()
		}
	}()
}
