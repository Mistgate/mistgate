package provision

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	sshTimeout     = 8 * time.Second
	maxTargetAddrs = 8
)

var (
	errHostKeySeen = errors.New("provision: host key captured")
	// ErrHostKey reports that the server key changed after the owner confirmed it.
	ErrHostKey = errors.New("provision: SSH host key does not match the confirmed fingerprint")
)

type contextDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

// Client performs password-free host-key discovery and pinned SSH connections.
// The production constructor uses the system resolver and a standard TCP dialer.
type Client struct {
	resolver resolver
	dialer   contextDialer
	timeout  time.Duration
}

// NewClient builds a production SSH client with bounded timeouts.
func NewClient() *Client {
	return &Client{resolver: net.DefaultResolver, dialer: &net.Dialer{Timeout: sshTimeout}, timeout: sshTimeout}
}

// Fingerprint reads one reachable server key without authenticating. The SSH
// handshake is stopped as soon as the key is received, before an auth request.
func (c *Client) Fingerprint(ctx context.Context, target Target) (string, error) {
	resolveCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	addrs, err := target.publicAddresses(resolveCtx, c.resolver)
	if err != nil {
		return "", err
	}
	if len(addrs) > maxTargetAddrs {
		return "", ErrUnsafeTarget
	}

	var lastErr error
	for _, addr := range addrs {
		fingerprint, err := c.fingerprintAt(resolveCtx, target, addr)
		if err == nil {
			return fingerprint, nil
		}
		if resolveCtx.Err() != nil {
			return "", resolveCtx.Err()
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrNoAddress
	}
	return "", fmt.Errorf("provision: read SSH host key: %w", lastErr)
}

// Dial authenticates as root only after the host presents the fingerprint the
// owner confirmed in the UI. A changed or unexpected key never receives the
// password.
func (c *Client) Dial(ctx context.Context, target Target, password, confirmedFingerprint string) (*Connection, error) {
	return c.DialAs(ctx, target, "root", password, confirmedFingerprint)
}

// DialAs authenticates with the supplied account after the server presents the confirmed host key.
// Non-root accounts are accepted only when the remote preflight proves passwordless sudo works.
func (c *Client) DialAs(ctx context.Context, target Target, username, password, confirmedFingerprint string) (*Connection, error) {
	if !validSSHUsername(username) {
		return nil, errors.New("provision: invalid SSH username")
	}
	if password == "" || !validFingerprint(confirmedFingerprint) {
		return nil, errors.New("provision: password and confirmed SHA-256 fingerprint are required")
	}
	resolveCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	addrs, err := target.publicAddresses(resolveCtx, c.resolver)
	if err != nil {
		return nil, err
	}
	if len(addrs) > maxTargetAddrs {
		return nil, ErrUnsafeTarget
	}

	var lastErr error
	for _, addr := range addrs {
		conn, err := c.dialPinned(resolveCtx, ctx, target, addr, username, password, confirmedFingerprint)
		if err == nil {
			if err := ctx.Err(); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return conn, nil
		}
		if errors.Is(err, ErrHostKey) {
			lastErr = ErrHostKey
			continue // another public address may be the pinned member of a load-balanced host
		}
		if resolveCtx.Err() != nil {
			return nil, resolveCtx.Err()
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrNoAddress
	}
	return nil, fmt.Errorf("provision: SSH connection failed: %w", lastErr)
}

// Connection is an authenticated SSH client whose lifetime follows the context
// passed to Dial. Close it when the provisioning phase ends.
type Connection struct {
	mu     sync.Mutex
	client *ssh.Client
	stop   func() bool
	user   string
}

// PrivilegedCommand runs a command with root privileges without prompting for a second password.
func (c *Connection) PrivilegedCommand(command string) string {
	if c == nil || c.user == "" || c.user == "root" {
		return command
	}
	return "sudo -n sh -c " + shellQuote(command)
}

// NewSession opens one SSH session on the verified connection.
func (c *Connection) NewSession() (*ssh.Session, error) {
	if c == nil {
		return nil, errors.New("provision: SSH connection is closed")
	}
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, errors.New("provision: SSH connection is closed")
	}
	return client.NewSession()
}

// Close ends the SSH connection and stops its cancellation hook.
func (c *Connection) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	client, stop := c.client, c.stop
	c.client, c.stop = nil, nil
	c.mu.Unlock()
	if client == nil {
		return nil
	}
	if stop != nil {
		stop()
	}
	return client.Close()
}

func (c *Client) fingerprintAt(ctx context.Context, target Target, addr netip.Addr) (string, error) {
	conn, err := c.dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr.String(), strconv.Itoa(int(target.port))))
	if err != nil {
		return "", err
	}
	defer conn.Close()
	stopHandshake, err := setHandshakeDeadline(ctx, conn, c.timeout)
	if err != nil {
		return "", err
	}
	defer stopHandshake()

	var fingerprint string
	config := &ssh.ClientConfig{
		User: "root",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fingerprint = ssh.FingerprintSHA256(key)
			return errHostKeySeen
		},
	}
	_, _, _, err = ssh.NewClientConn(conn, target.Address(), config)
	if errors.Is(err, errHostKeySeen) && fingerprint != "" {
		return fingerprint, nil
	}
	if err == nil {
		return "", errors.New("provision: SSH handshake unexpectedly continued past host-key discovery")
	}
	return "", err
}

func (c *Client) dialPinned(handshakeParent, lifetime context.Context, target Target, addr netip.Addr, username, password, expected string) (*Connection, error) {
	handshakeCtx, cancel := context.WithTimeout(handshakeParent, c.timeout)
	defer cancel()
	conn, err := c.dialer.DialContext(handshakeCtx, "tcp", net.JoinHostPort(addr.String(), strconv.Itoa(int(target.port))))
	if err != nil {
		return nil, err
	}
	stopHandshake, err := setHandshakeDeadline(handshakeCtx, conn, c.timeout)
	if err != nil {
		conn.Close()
		return nil, err
	}
	defer stopHandshake()

	config := &ssh.ClientConfig{
		User: username,
		Auth: []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if ssh.FingerprintSHA256(key) != expected {
				return ErrHostKey
			}
			return nil
		},
	}
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, target.Address(), config)
	if err != nil {
		conn.Close()
		return nil, err
	}
	stopHandshake()
	if err := conn.SetDeadline(time.Time{}); err != nil {
		clientConn.Close()
		return nil, err
	}
	client := ssh.NewClient(clientConn, chans, reqs)
	stop := context.AfterFunc(lifetime, func() { _ = client.Close() })
	return &Connection{client: client, stop: stop, user: username}, nil
}

func validSSHUsername(username string) bool {
	if len(username) < 1 || len(username) > 32 {
		return false
	}
	for i, r := range username {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' || i > 0 && (r >= '0' && r <= '9' || r == '-' || r == '.') {
			continue
		}
		return false
	}
	return true
}

func setHandshakeDeadline(ctx context.Context, conn net.Conn, timeout time.Duration) (func() bool, error) {
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	if ctx.Err() != nil {
		stop()
		return nil, ctx.Err()
	}
	return stop, nil
}

func validFingerprint(value string) bool {
	if !strings.HasPrefix(value, "SHA256:") {
		return false
	}
	fingerprint := strings.TrimPrefix(value, "SHA256:")
	decoded, err := base64.RawStdEncoding.DecodeString(fingerprint)
	if err != nil {
		return false
	}
	return len(decoded) == 32 && len(fingerprint) == 43
}
