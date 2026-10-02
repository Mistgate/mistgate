package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestNewTargetNormalizesHostAndPort(t *testing.T) {
	got, err := NewTarget("  NODE.Example.COM. ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host() != "node.example.com" || got.Port() != DefaultSSHPort || got.Address() != "node.example.com:22" {
		t.Fatalf("normalized target = %#v (%q)", got, got.Address())
	}

	ipv6, err := NewTarget("2606:4700:4700::1111", 2222)
	if err != nil {
		t.Fatal(err)
	}
	if got := ipv6.Address(); got != "[2606:4700:4700::1111]:2222" {
		t.Fatalf("IPv6 address = %q", got)
	}
}

func TestNewTargetRejectsMalformedHostsAndPorts(t *testing.T) {
	for _, tc := range []struct {
		host string
		port uint32
	}{
		{host: "localhost"},
		{host: "node.example.com/path"},
		{host: "user@node.example.com"},
		{host: "node.example.com:22"},
		{host: "node..example.com"},
		{host: "-node.example.com"},
		{host: "node.example.com", port: 65536},
		{host: "fe80::1%eth0"},
	} {
		if _, err := NewTarget(tc.host, tc.port); err == nil {
			t.Errorf("NewTarget(%q, %d) unexpectedly succeeded", tc.host, tc.port)
		}
	}
}

func TestPublicUnicastRejectsPrivateAndSpecialPurposeRanges(t *testing.T) {
	for _, raw := range []string{
		"10.1.2.3", "100.64.1.2", "127.0.0.1", "169.254.169.254", "172.16.0.1", "192.168.1.10",
		"192.0.2.1", "198.18.0.1", "198.51.100.2", "203.0.113.1", "224.0.0.1", "240.0.0.1",
		"::1", "fc00::1", "fe80::1", "ff02::1", "64:ff9b::a9fe:a9fe", "64:ff9b:1::1", "2001:db8::1", "2002::1", "3fff::1", "5f00::1",
	} {
		addr := netip.MustParseAddr(raw)
		if publicUnicast(addr) {
			t.Errorf("%s unexpectedly passed the public-address check", raw)
		}
	}

	for _, raw := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111", "2001:4860:4860::8888"} {
		addr := netip.MustParseAddr(raw)
		if !publicUnicast(addr) {
			t.Errorf("%s was rejected as non-public", raw)
		}
	}
}

func TestResolveRejectsMixedPublicAndPrivateDNSAnswers(t *testing.T) {
	target, err := NewTarget("node.example.com", 22)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient()
	client.resolver = testResolver{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("169.254.169.254")}
	if _, err := target.publicAddresses(context.Background(), client.resolver); !errors.Is(err, ErrUnsafeTarget) {
		t.Fatalf("publicAddresses() error = %v, want ErrUnsafeTarget", err)
	}
}

func TestFingerprintDoesNotSendAuthentication(t *testing.T) {
	signer := testSigner(t)
	passwords := make(chan string, 1)
	client := testSSHClient(t, signer, passwords)
	got, err := client.Fingerprint(context.Background(), mustTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := ssh.FingerprintSHA256(signer.PublicKey()); got != want {
		t.Fatalf("fingerprint = %q, want %q", got, want)
	}
	select {
	case got := <-passwords:
		t.Fatalf("fingerprint probe sent a password: %q", got)
	default:
	}
}

func TestDialRejectsChangedHostKeyBeforeSendingPassword(t *testing.T) {
	signer := testSigner(t)
	other := testSigner(t)
	passwords := make(chan string, 1)
	client := testSSHClient(t, signer, passwords)
	_, err := client.Dial(context.Background(), mustTarget(t), "secret", ssh.FingerprintSHA256(other.PublicKey()))
	if !errors.Is(err, ErrHostKey) {
		t.Fatalf("Dial() error = %v, want ErrHostKey", err)
	}
	select {
	case got := <-passwords:
		t.Fatalf("host-key mismatch sent a password: %q", got)
	default:
	}
}

func TestDialAuthenticatesOnlyAfterConfirmedHostKey(t *testing.T) {
	signer := testSigner(t)
	passwords := make(chan string, 1)
	client := testSSHClient(t, signer, passwords)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := client.Dial(ctx, mustTarget(t), "ssh-secret", ssh.FingerprintSHA256(signer.PublicKey()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	session, err := conn.NewSession()
	if err != nil {
		t.Fatalf("connection closed when Dial returned: %v", err)
	}
	_ = session.Close()
	select {
	case got := <-passwords:
		if got != "ssh-secret" {
			t.Fatalf("password sent to SSH server = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH server did not receive password after the host key was confirmed")
	}
}

func TestDialConnectionClosesWhenCallerContextEnds(t *testing.T) {
	signer := testSigner(t)
	passwords := make(chan string, 1)
	client := testSSHClient(t, signer, passwords)
	ctx, cancel := context.WithCancel(context.Background())
	conn, err := client.Dial(ctx, mustTarget(t), "ssh-secret", ssh.FingerprintSHA256(signer.PublicKey()))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer conn.Close()
	cancel()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		session, err := conn.NewSession()
		if err != nil {
			return
		}
		_ = session.Close()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("SSH connection stayed open after its context ended")
}

func TestValidFingerprintRequiresSHA256Digest(t *testing.T) {
	signer := testSigner(t)
	if got := ssh.FingerprintSHA256(signer.PublicKey()); !validFingerprint(got) {
		t.Fatalf("valid SHA-256 fingerprint rejected: %q", got)
	}
	for _, invalid := range []string{"", "SHA256:short", "MD5:aa:bb", "SHA256://///////////////////////////////////////////"} {
		if validFingerprint(invalid) {
			t.Errorf("invalid fingerprint accepted: %q", invalid)
		}
	}
}

type testResolver []netip.Addr

func (r testResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r, nil
}

type testDialer func(context.Context, string, string) (net.Conn, error)

func (d testDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d(ctx, network, address)
}

func testSSHClient(t *testing.T, signer ssh.Signer, passwords chan<- string) *Client {
	t.Helper()
	server := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		passwords <- string(password)
		return nil, nil
	}}
	server.AddHostKey(signer)
	client := NewClient()
	client.timeout = time.Second
	client.resolver = testResolver{netip.MustParseAddr("8.8.8.8")}
	client.dialer = testDialer(func(ctx context.Context, _, _ string) (net.Conn, error) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		go func() {
			serverConn, err := listener.Accept()
			if err == nil {
				serveTestSSH(serverConn, server)
			}
		}()
		clientConn, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
		listener.Close()
		return clientConn, nil
	})
	return client
}

func serveTestSSH(conn net.Conn, config *ssh.ServerConfig) {
	defer conn.Close()
	serverConn, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer serverConn.Close()
	go ssh.DiscardRequests(requests)
	for channel := range channels {
		accepted, requests, err := channel.Accept()
		if err != nil {
			continue
		}
		go ssh.DiscardRequests(requests)
		accepted.Close()
	}
}

func testSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func mustTarget(t *testing.T) Target {
	t.Helper()
	target, err := NewTarget("node.example.com", 22)
	if err != nil {
		t.Fatal(err)
	}
	return target
}
