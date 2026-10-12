package provision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/crypto/ssh"
)

const authTestPassword = "correct horse battery staple"

// fakeSSHD is an x/crypto SSH server whose login methods the test picks, like the PasswordAuthentication and
// KbdInteractiveAuthentication switches of OpenSSH: a method without a callback is not offered. Nothing in it logs, so a
// connection that ends after the test does not touch a finished *testing.T.
type fakeSSHD struct {
	listener    net.Listener
	signer      ssh.Signer
	mu          sync.Mutex
	passwords   []string // every password received, by either method
	viaPassword int
	viaKeyboard int
	exec        provisionSSHExecutor
}

type loginMethods struct{ password, keyboard, key bool }

func newFakeSSHD(t *testing.T, methods loginMethods, exec provisionSSHExecutor) *fakeSSHD {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	d := &fakeSSHD{listener: listener, signer: testSigner(t), exec: exec}
	config := &ssh.ServerConfig{}
	config.AddHostKey(d.signer)
	if methods.password {
		config.PasswordCallback = func(_ ssh.ConnMetadata, got []byte) (*ssh.Permissions, error) {
			d.record(string(got), true)
			return nil, d.check(string(got))
		}
	}
	if methods.keyboard {
		config.KeyboardInteractiveCallback = func(_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			// two prompts, as PAM stacks with a confirmation do; the client must answer every one
			answers, err := challenge("", "", []string{"Password: ", "Password again: "}, []bool{false, false})
			if err != nil || len(answers) != 2 || answers[0] != answers[1] {
				return nil, errors.New("keyboard-interactive answers are not the same password")
			}
			d.record(answers[0], false)
			return nil, d.check(answers[0])
		}
	}
	if methods.key {
		config.PublicKeyCallback = func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, errors.New("no authorized key")
		}
	}
	go d.serve(config)
	return d
}

func (d *fakeSSHD) record(password string, viaPassword bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.passwords = append(d.passwords, password)
	if viaPassword {
		d.viaPassword++
	} else {
		d.viaKeyboard++
	}
}

func (d *fakeSSHD) check(got string) error {
	if got != authTestPassword {
		return errors.New("wrong password")
	}
	return nil
}

func (d *fakeSSHD) received() (passwords []string, viaPassword, viaKeyboard int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.passwords...), d.viaPassword, d.viaKeyboard
}

func (d *fakeSSHD) serve(config *ssh.ServerConfig) {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			return
		}
		go func() {
			server, channels, requests, err := ssh.NewServerConn(conn, config)
			if err != nil {
				conn.Close()
				return
			}
			defer server.Close()
			go ssh.DiscardRequests(requests)
			for newChannel := range channels {
				channel, channelRequests, err := newChannel.Accept()
				if err != nil {
					continue
				}
				go func() {
					defer channel.Close()
					for request := range channelRequests {
						if request.Type != "exec" || d.exec == nil {
							_ = request.Reply(false, nil)
							continue
						}
						var execRequest struct{ Command string }
						if err := ssh.Unmarshal(request.Payload, &execRequest); err != nil {
							_ = request.Reply(false, nil)
							return
						}
						_ = request.Reply(true, nil)
						output, err := d.exec(execRequest.Command, channel)
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
}

func (d *fakeSSHD) client() *Client {
	c := NewClient()
	c.timeout = 2 * time.Second
	c.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
	c.dialer = testDialer(func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, d.listener.Addr().String())
	})
	return c
}

func (d *fakeSSHD) fingerprint() string { return ssh.FingerprintSHA256(d.signer.PublicKey()) }

// The server decides which login methods exist; the panel has to use whichever one takes the owner's password.
func TestDialLogsInWithPasswordKeyboardInteractiveOrEither(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		methods                loginMethods
		wantViaPassword, wantK int
	}{
		{"password only", loginMethods{password: true}, 1, 0},
		{"keyboard-interactive only", loginMethods{keyboard: true}, 0, 1},
		{"both offered: the password method goes first", loginMethods{password: true, keyboard: true}, 1, 0},
		{"keyboard-interactive and a key method", loginMethods{keyboard: true, key: true}, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeSSHD(t, tc.methods, nil)
			conn, err := d.client().Dial(context.Background(), mustTarget(t), authTestPassword, d.fingerprint())
			if err != nil {
				t.Fatalf("Dial: %v", err)
			}
			defer conn.Close()
			passwords, viaPassword, viaKeyboard := d.received()
			if viaPassword != tc.wantViaPassword || viaKeyboard != tc.wantK || len(passwords) != 1 || passwords[0] != authTestPassword {
				t.Fatalf("server received %d password(s), %d via the password method, %d via keyboard-interactive",
					len(passwords), viaPassword, viaKeyboard)
			}
		})
	}
}

// A refused password is a wrong login for the owner to fix, whichever way the server asked for it.
func TestDialWrongPasswordIsAuthenticationFailed(t *testing.T) {
	for _, tc := range []struct {
		name         string
		methods      loginMethods
		wantAttempts int
	}{
		{"password only", loginMethods{password: true}, 1},
		{"keyboard-interactive only", loginMethods{keyboard: true}, 1},
		// ponytail: with both methods offered a wrong password is tried once per method (2 of OpenSSH's 6 MaxAuthTries);
		// avoiding that needs the server's method list before choosing, which x/crypto/ssh offers only via AuthCallback.
		{"both offered", loginMethods{password: true, keyboard: true}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newFakeSSHD(t, tc.methods, nil)
			_, err := d.client().Dial(context.Background(), mustTarget(t), "not-the-password", d.fingerprint())
			if !errors.Is(err, ErrAuthRejected) || errors.Is(err, ErrNoPasswordAuth) {
				t.Fatalf("Dial error = %v, want ErrAuthRejected", err)
			}
			if got := publicSSHCode(err); got != "ssh_authentication_failed" {
				t.Fatalf("public code = %q", got)
			}
			if got := connect.CodeOf(sshConnectError(err)); got != connect.CodeUnauthenticated {
				t.Fatalf("Connect code = %v", got)
			}
			if strings.Contains(err.Error(), "not-the-password") {
				t.Fatalf("the error repeats the password: %v", err)
			}
			if passwords, _, _ := d.received(); len(passwords) != tc.wantAttempts {
				t.Fatalf("server saw %d attempts, want %d", len(passwords), tc.wantAttempts)
			}
		})
	}
}

// A key-only server never gets the password, and the error says so instead of blaming the password.
func TestDialServerWithoutPasswordLoginIsDistinguishedFromAWrongPassword(t *testing.T) {
	d := newFakeSSHD(t, loginMethods{key: true}, nil)
	_, err := d.client().Dial(context.Background(), mustTarget(t), authTestPassword, d.fingerprint())
	if !errors.Is(err, ErrNoPasswordAuth) || errors.Is(err, ErrAuthRejected) {
		t.Fatalf("Dial error = %v, want ErrNoPasswordAuth", err)
	}
	if got := publicSSHCode(err); got != "ssh_password_auth_unavailable" {
		t.Fatalf("public code = %q", got)
	}
	if got := connect.CodeOf(sshConnectError(err)); got != connect.CodeFailedPrecondition {
		t.Fatalf("Connect code = %v", got)
	}
	if passwords, _, _ := d.received(); len(passwords) != 0 {
		t.Fatalf("the password was sent to a server that does not take one: %q", passwords)
	}
}

// Everything that is not a spent authentication keeps its own classification.
func TestClassifyAuthErrorLeavesOtherFailuresAlone(t *testing.T) {
	for _, err := range []error{ErrHostKey, context.DeadlineExceeded, errors.New("ssh: handshake failed: EOF"), nil} {
		if got := classifyAuthError(err, true); got != err {
			t.Errorf("classifyAuthError(%v) = %v", err, got)
		}
	}
	// The pinned host key still wins over everything: its failure comes before any password is asked.
	d := newFakeSSHD(t, loginMethods{password: true}, nil)
	_, err := d.client().Dial(context.Background(), mustTarget(t), authTestPassword, ssh.FingerprintSHA256(testSigner(t).PublicKey()))
	if !errors.Is(err, ErrHostKey) || publicSSHCode(err) != "ssh_host_key_changed" {
		t.Fatalf("Dial error = %v, want ErrHostKey", err)
	}
	if passwords, _, _ := d.received(); len(passwords) != 0 {
		t.Fatalf("host-key mismatch sent a password: %q", passwords)
	}
}

func TestNewClientWithDialsThroughTheCallersSocketsAndKeepsThePublicAddressRule(t *testing.T) {
	d := newFakeSSHD(t, loginMethods{password: true}, nil)
	var dialed []string
	var looked []string
	answers := map[string][]netip.Addr{
		"node.example.com":    {netip.MustParseAddr("8.8.8.8")},
		"private.example.com": {netip.MustParseAddr("10.1.2.3")},
		"mixed.example.com":   {netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")},
	}
	client := NewClientWith(
		func(ctx context.Context, network, address string) (net.Conn, error) {
			dialed = append(dialed, address)
			return (&net.Dialer{}).DialContext(ctx, network, d.listener.Addr().String())
		},
		func(_ context.Context, host string) ([]netip.Addr, error) {
			looked = append(looked, host)
			addrs, ok := answers[host]
			if !ok {
				return nil, ErrNoAddress
			}
			return addrs, nil
		})
	target := func(host string) Target {
		tg, err := NewTarget(host, 2222)
		if err != nil {
			t.Fatal(err)
		}
		return tg
	}

	fingerprint, algorithm, err := client.Fingerprint(context.Background(), target("node.example.com"))
	if err != nil || fingerprint != d.fingerprint() || algorithm != d.signer.PublicKey().Type() {
		t.Fatalf("Fingerprint = %q %q, %v", fingerprint, algorithm, err)
	}
	conn, err := client.DialAs(context.Background(), target("node.example.com"), "root", authTestPassword, d.fingerprint())
	if err != nil {
		t.Fatalf("DialAs: %v", err)
	}
	conn.Close()
	for _, address := range dialed {
		if address != "8.8.8.8:2222" {
			t.Fatalf("dialed %q, want the looked-up address with the target port", address)
		}
	}
	if len(dialed) != 2 || len(looked) != 2 {
		t.Fatalf("dialed %v after looking up %v", dialed, looked)
	}

	dialed = nil
	for _, host := range []string{"private.example.com", "mixed.example.com"} {
		if _, _, err := client.Fingerprint(context.Background(), target(host)); !errors.Is(err, ErrUnsafeTarget) {
			t.Fatalf("Fingerprint(%s) error = %v, want ErrUnsafeTarget", host, err)
		}
		if _, err := client.DialAs(context.Background(), target(host), "root", authTestPassword, d.fingerprint()); !errors.Is(err, ErrUnsafeTarget) {
			t.Fatalf("DialAs(%s) error = %v, want ErrUnsafeTarget", host, err)
		}
	}
	if _, _, err := client.Fingerprint(context.Background(), target("gone.example.com")); !errors.Is(err, ErrNoAddress) {
		t.Fatalf("Fingerprint of an unknown name: %v", err)
	}
	if len(dialed) != 0 {
		t.Fatalf("a non-public or unresolved target was dialed: %v", dialed)
	}
}

// truncatingNode is a node whose disk keeps only keep(n) bytes of what `cat > file` is given and still exits 0, as `cat`
// does when the connection ends or the sending Worker is replaced halfway through an upload.
type truncatingNode struct {
	mu       sync.Mutex
	keep     func(n int) int
	disk     []byte
	commands []string
	sumFails bool
}

func (n *truncatingNode) execute(wire string, channel ssh.Channel) ([]byte, error) {
	n.mu.Lock()
	n.commands = append(n.commands, wire)
	n.mu.Unlock()
	switch {
	case strings.HasPrefix(wire, "cat > "):
		data, err := io.ReadAll(channel)
		n.mu.Lock()
		n.disk = data[:n.keep(len(data))]
		n.mu.Unlock()
		return nil, err
	case strings.HasPrefix(wire, "sha256sum "):
		n.mu.Lock()
		defer n.mu.Unlock()
		if n.sumFails {
			return nil, errors.New("sha256sum: not found")
		}
		sum := sha256.Sum256(n.disk)
		return []byte(hex.EncodeToString(sum[:]) + "  /root/mistgate-node.new\n"), nil
	case strings.HasPrefix(wire, "rm -f "):
		n.mu.Lock()
		n.disk = nil
		n.mu.Unlock()
		return nil, nil
	}
	return nil, errors.New("unexpected remote command")
}

func (n *truncatingNode) ran(prefix string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.commands {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func uploadTo(t *testing.T, node *truncatingNode, payload []byte) error {
	t.Helper()
	d := newFakeSSHD(t, loginMethods{password: true}, node.execute)
	conn, err := d.client().Dial(context.Background(), mustTarget(t), authTestPassword, d.fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sum := sha256.Sum256(payload)
	return uploadBinary(context.Background(), conn, strings.NewReader(string(payload)), int64(len(payload)), hex.EncodeToString(sum[:]))
}

// `cat` exits 0 on a truncated file; only checking the file on the node catches it.
func TestUploadBinaryCatchesATruncatedFileThatExitedZero(t *testing.T) {
	payload := []byte(strings.Repeat("agent-binary-", 4096))
	truncated := &truncatingNode{keep: func(n int) int { return n / 2 }}
	err := uploadTo(t, truncated, payload)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch on the node") {
		t.Fatalf("uploadBinary on a truncated file = %v, want a digest mismatch on the node", err)
	}
	if !truncated.ran("rm -f /root/mistgate-node.new") {
		t.Fatalf("the truncated file was left on the node: %q", truncated.commands)
	}

	whole := &truncatingNode{keep: func(n int) int { return n }}
	if err := uploadTo(t, whole, payload); err != nil {
		t.Fatalf("uploadBinary on an intact file = %v", err)
	}
	if whole.ran("rm -f ") {
		t.Fatal("the verified file was removed")
	}
}

// If the node cannot check the file, the upload is not trusted either.
func TestUploadBinaryFailsWhenTheNodeCannotCheckTheFile(t *testing.T) {
	node := &truncatingNode{keep: func(n int) int { return n }, sumFails: true}
	if err := uploadTo(t, node, []byte("agent-binary")); err == nil {
		t.Fatal("an unverifiable upload was accepted")
	}
	if !node.ran("rm -f /root/mistgate-node.new") {
		t.Fatal("the unverified file was left on the node")
	}
}
