package provision

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/update"
	"github.com/mistgate/mistgate/internal/release"
	"golang.org/x/crypto/ssh"
)

// drainRelease serves a signed manifest and one node binary like a GitHub release download prefix.
type drainRelease struct {
	server *httptest.Server
	key    ed25519.PublicKey
	body   []byte // what the download returns (tests may change it after signing)
	digest string // of the genuine binary
}

func newDrainRelease(t *testing.T, binary []byte) *drainRelease {
	t.Helper()
	pub, priv, err := release.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binary)
	r := &drainRelease{key: pub, body: binary, digest: hex.EncodeToString(sum[:])}
	manifest := &release.Manifest{Schema: release.Schema, Version: "v1.2.3", Built: 1_000, Expires: time.Now().Add(time.Hour).Unix(),
		Files: []release.File{{OS: "linux", Arch: "amd64", Name: "mistgate-node-linux-amd64", Size: int64(len(binary)), SHA256: r.digest}}}
	raw, err := manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/v1.2.3/manifest.json":
			_, _ = w.Write(raw)
		case "/v1.2.3/manifest.sig":
			_, _ = w.Write(release.Sign(priv, raw))
		case "/v1.2.3/mistgate-node-linux-amd64":
			_, _ = w.Write(r.body)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

func (r *drainRelease) source() *update.ReleaseNodeBinaries {
	return &update.ReleaseNodeBinaries{Version: "v1.2.3", Key: r.key, Client: r.server.Client(), Base: r.server.URL + "/"}
}

// installNode is a node that answers the whole install script as root: preflight, firewall, upload (keeping what cat was
// fed, or only part of it), sha256sum, install, enroll and agent install.
type installNode struct {
	*fakeSSHD
	manager *e2eNodeManager
	keep    func(n int) int
	mu      sync.Mutex
	disk    []byte
	enroll  struct{ command, stdin string }
	cmds    []string
}

const installNodeToken = "one-time-enrollment-secret"

func newInstallNode(t *testing.T, manager *e2eNodeManager, keep func(int) int) *installNode {
	t.Helper()
	n := &installNode{manager: manager, keep: keep}
	n.fakeSSHD = newFakeSSHD(t, loginMethods{password: true}, n.execute)
	return n
}

func (n *installNode) execute(command string, channel ssh.Channel) ([]byte, error) {
	n.mu.Lock()
	n.cmds = append(n.cmds, command)
	n.mu.Unlock()
	switch {
	case strings.Contains(command, "panel_reachable=%s"):
		return []byte(strings.Join([]string{
			"distribution=ubuntu", "version=22.04", "kernel=6.8.0", "architecture=x86_64",
			"cpu_count=4", "memory_bytes=1073741824", "disk_available_bytes=1073741824",
			"systemd=yes", "already_enrolled=no", "existing_node_id=", "panel_reachable=yes", "",
		}, "\n")), nil
	case strings.HasPrefix(command, "sh -s -- "):
		_, err := io.Copy(io.Discard, channel)
		return nil, err
	case strings.HasPrefix(command, "cat > "):
		data, err := io.ReadAll(channel)
		n.mu.Lock()
		n.disk = data[:n.keep(len(data))]
		n.mu.Unlock()
		return nil, err
	case strings.HasPrefix(command, "sha256sum "):
		n.mu.Lock()
		sum := sha256.Sum256(n.disk)
		n.mu.Unlock()
		return []byte(hex.EncodeToString(sum[:]) + "  /root/mistgate-node.new\n"), nil
	case strings.HasPrefix(command, "rm -f "):
		return nil, nil
	case strings.HasPrefix(command, "install -o root"):
		return nil, nil
	case strings.HasPrefix(command, "/root/mistgate-node enroll "):
		data, err := io.ReadAll(channel)
		n.mu.Lock()
		n.enroll.command, n.enroll.stdin = command, string(data)
		n.mu.Unlock()
		return nil, err
	case command == "/root/mistgate-node install":
		n.manager.setConnected()
		return nil, nil
	}
	return nil, io.ErrUnexpectedEOF
}

func (n *installNode) seedJob(t *testing.T, st *store.Store, svc *Service) {
	t.Helper()
	ctx := context.Background()
	sealed, err := svc.sealCredentials("prv_link", credentials{Username: "root", Password: authTestPassword})
	if err != nil {
		t.Fatal(err)
	}
	job := store.NodeProvisionJob{ID: "prv_link", NodeID: "nod_aaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "node-a", Address: "node-a.example.com",
		SSHHost: "node.example.com", SSHPort: 22, HostFingerprint: n.fingerprint(), Secret: sealed, CreatedBy: "adm_test", CreatedAt: drainT0, UpdatedAt: drainT0}
	if err := st.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	// in production CreateProvisionEnrollment creates this fleet row before the install completes
	if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'pending', ?)`,
		job.NodeID, job.Name, job.Address, drainT0.Unix()); err != nil {
		t.Fatal(err)
	}
}

// The edge install path end to end through Drain: the signed binary streams from the release into the SSH upload, the
// node enrols over the panel's link, and the node is judged online.
func TestDrainInstallsOverTheLinkFromAStreamedRelease(t *testing.T) {
	binary := bytes.Repeat([]byte("signed-agent-"), 20_000)
	rel := newDrainRelease(t, binary)
	manager := &e2eNodeManager{}
	node := newInstallNode(t, manager, func(n int) int { return n })
	st := drainStore(t)
	svc := drainService(t, st, Config{
		SSH: node.client(), Nodes: manager, Binaries: rel.source(), WaitOnline: time.Second,
		LinkURL: "wss://panel.example.com/0123456789abcdef0123/",
	})
	node.seedJob(t, st, svc)

	more, busy, err := svc.Drain(context.Background())
	if err != nil || busy || more {
		t.Fatalf("Drain = more %v busy %v err %v", more, busy, err)
	}
	job := jobState(t, st, "prv_link")
	if job.State != "completed" || len(job.Secret) != 0 {
		t.Fatalf("job = state %q code %q, secret %d bytes; events %v", job.State, job.ErrorCode, len(job.Secret), eventCodes(t, st, "prv_link"))
	}
	want := "/root/mistgate-node enroll --link-url 'wss://panel.example.com/0123456789abcdef0123/' --ca-sha256 '" + strings.Repeat("a", 64) + "' --token-stdin --resume-key"
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.enroll.command != want || node.enroll.stdin != installNodeToken+"\n" {
		t.Fatalf("enroll command %q stdin %q; want %q with the token on stdin", node.enroll.command, node.enroll.stdin, want)
	}
	if !bytes.Equal(node.disk, binary) {
		t.Fatalf("the node holds %d bytes, the release has %d", len(node.disk), len(binary))
	}
	probed := false
	for _, command := range node.cmds {
		if strings.Contains(command, installNodeToken) || strings.Contains(command, authTestPassword) {
			t.Fatalf("a secret is in the command line: %q", command)
		}
		if strings.Contains(command, "panel_reachable=%s") && strings.Contains(command, "'panel.example.com' '443'") {
			probed = true
		}
	}
	if !probed {
		t.Fatalf("the reachability probe does not dial the link's host and port: %q", node.cmds)
	}
}

// Bytes that are not the signed ones, or that the node did not keep whole, fail the job as agent_transfer_failed.
func TestDrainFailsTheJobWhenTheTransferIsNotTheSignedBinary(t *testing.T) {
	binary := bytes.Repeat([]byte("signed-agent-"), 20_000)
	for _, tc := range []struct {
		name   string
		tamper func(*drainRelease)
		keep   func(int) int
	}{
		{"the download carries other bytes of the same length", func(r *drainRelease) {
			r.body = bytes.Repeat([]byte("evil-"), len(binary)/5+1)[:len(binary)]
		}, func(n int) int { return n }},
		{"the download ends early", func(r *drainRelease) { r.body = binary[:len(binary)/2] }, func(n int) int { return n }},
		{"the node keeps half of the upload and cat exits 0", func(*drainRelease) {}, func(n int) int { return n / 2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rel := newDrainRelease(t, binary)
			tc.tamper(rel)
			manager := &e2eNodeManager{}
			node := newInstallNode(t, manager, tc.keep)
			st := drainStore(t)
			svc := drainService(t, st, Config{SSH: node.client(), Nodes: manager, Binaries: rel.source(), WaitOnline: time.Second,
				LinkURL: "wss://panel.example.com/0123456789abcdef0123/"})
			node.seedJob(t, st, svc)
			if _, _, err := svc.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			job := jobState(t, st, "prv_link")
			if job.State != "failed" || job.ErrorCode != "agent_transfer_failed" || len(job.Secret) != 0 {
				t.Fatalf("job = state %q code %q secret %d bytes", job.State, job.ErrorCode, len(job.Secret))
			}
			node.mu.Lock()
			defer node.mu.Unlock()
			if node.enroll.command != "" {
				t.Fatalf("the install went on after a bad transfer: %q", node.cmds)
			}
		})
	}
}
