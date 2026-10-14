//go:build !js

package health

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"

	"github.com/mistgate/mistgate/internal/panel/health/vlesswire"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
)

const (
	vlessHelperCommand      = "health-vless-helper"
	vlessHelperTestModeEnv  = "MISTGATE_HEALTH_VLESS_TEST_SUBPROCESS"
	vlessHelperStartTimeout = 10 * time.Second
	vlessHelperCloseTimeout = 2 * time.Second
	vlessHelperHardMargin   = 5 * time.Second
	vlessHelperStderrMax    = 8 << 10
)

// dialVLESS resolves the same parameters as a subscription and starts an isolated Xray client helper.
func dialVLESS(ctx context.Context, t Target, o DialOptions) (Tunnel, error) {
	params, ok := panelvless.ResolveClientParams(protocols.RenderInput{
		Settings: t.Settings,
		Inbound: protocols.InboundView{
			ID: t.Inbound.ID, Node: protocols.NodeView{ID: t.Node.ID, Name: t.Node.Name, Address: t.Node.Address,
				CountryCode: t.Node.CountryCode},
			Port: t.Spec.Listen.Port, TLSServerName: t.Spec.TLS.ServerName, TLSMode: t.Spec.TLS.Mode,
			CertPinSHA256: t.Inbound.CertPin,
		},
		DeviceID: t.CredID, Secret: t.Secret, InboundPublic: []byte(t.Inbound.PluginPublic),
	})
	if !ok {
		return nil, errClientUnsupported
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = 8 * time.Second
	}
	username, err := randomSOCKSCredential()
	if err != nil {
		return nil, &ProbeError{Code: "refused", Detail: "cannot prepare the VLESS helper"}
	}
	password, err := randomSOCKSCredential()
	if err != nil {
		return nil, &ProbeError{Code: "refused", Detail: "cannot prepare the VLESS helper"}
	}
	select { // each helper is an Xray process of ~40 MB inside the panel's memory budget
	case vlessHelperSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, &ProbeError{Code: "timeout", Detail: "waited too long for a free VLESS helper"}
	}
	tunnel, err := startVLESSHelper(ctx, vlesswire.Request{
		Client: params, SOCKSUsername: username, SOCKSPassword: password,
		HandshakeTimeout: o.HandshakeTimeout, HardTimeout: roundBudget + vlessHelperHardMargin,
		TestTLSPin: append([]byte(nil), o.testTLSPin...),
	})
	if err != nil {
		<-vlessHelperSlots
		return nil, err
	}
	tunnel.release = func() { <-vlessHelperSlots }
	return tunnel, nil
}

// vlessHelperSlots bounds the helpers running at once. ponytail: a fixed 2; make it follow the panel's memory budget
// if fleets with many VLESS inbounds start to queue here.
var vlessHelperSlots = make(chan struct{}, 2)

func randomSOCKSCredential() (string, error) {
	var bytes [24]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

type readyResult struct {
	ready vlesswire.Ready
	err   error
}

func startVLESSHelper(ctx context.Context, request vlesswire.Request) (*vlessTunnel, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, &ProbeError{Code: "refused", Detail: "cannot locate the VLESS helper"}
	}
	// Linux appends this to the path of a binary replaced while running (a panel update); the new file sits at the same path.
	executable = strings.TrimSuffix(executable, " (deleted)")
	args := []string{vlessHelperCommand}
	env := minimalHelperEnvironment()
	if isTestExecutable(executable) {
		args = nil
		mode := os.Getenv(vlessHelperTestModeEnv)
		if mode == "" {
			mode = "real"
		}
		env = append(env, vlessHelperTestModeEnv+"="+mode)
	}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &ProbeError{Code: "refused", Detail: "cannot start the VLESS client helper"}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &ProbeError{Code: "refused", Detail: "cannot start the VLESS client helper"}
	}
	stderr := &tailBuffer{limit: vlessHelperStderrMax}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, &ProbeError{Code: "refused", Detail: "cannot start the VLESS client helper"}
	}
	tunnel := &vlessTunnel{cmd: cmd, stdin: stdin, stderr: stderr, done: make(chan struct{}), auth: proxy.Auth{}}
	readReady := make(chan readyResult, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(stdout, 1024)).ReadString('\n')
		var ready vlesswire.Ready
		if err == nil {
			err = json.Unmarshal([]byte(strings.TrimSpace(line)), &ready)
		}
		readReady <- readyResult{ready: ready, err: err}
	}()
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		tunnel.reapStartup(readReady, false)
		return nil, tunnel.probeError(err, ctx)
	}
	timer := time.NewTimer(vlessHelperStartTimeout)
	defer timer.Stop()
	var result readyResult
	select {
	case result = <-readReady:
	case <-ctx.Done():
		tunnel.reapStartup(readReady, false)
		return nil, tunnel.probeError(ctx.Err(), ctx)
	case <-timer.C:
		tunnel.reapStartup(readReady, false)
		return nil, tunnel.probeError(context.DeadlineExceeded, ctx)
	}
	if result.err != nil || result.ready.Port == 0 {
		tunnel.reapStartup(readReady, true)
		if result.err == nil {
			result.err = errors.New("invalid helper listener")
		}
		return nil, tunnel.probeError(result.err, ctx)
	}
	tunnel.endpoint = net.JoinHostPort("127.0.0.1", strconv.Itoa(int(result.ready.Port)))
	tunnel.auth = proxy.Auth{User: request.SOCKSUsername, Password: request.SOCKSPassword}
	go tunnel.wait()
	return tunnel, nil
}

func isTestExecutable(path string) bool {
	name := strings.ToLower(filepath.Base(path))
	return strings.HasSuffix(name, ".test") || strings.HasSuffix(name, ".test.exe")
}

func minimalHelperEnvironment() []string {
	var env []string
	for _, key := range []string{"PATH", "SYSTEMROOT", "SystemRoot", "WINDIR", "TEMP", "TMP", "SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

type vlessTunnel struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	endpoint string
	auth     proxy.Auth
	stderr   *tailBuffer
	done     chan struct{}
	waitErr  error

	closeOnce sync.Once
	closeErr  error
	release   func() // frees the helper slot taken by dialVLESS; nil for helpers started directly in tests
}

func (t *vlessTunnel) wait() {
	t.waitErr = t.cmd.Wait()
	close(t.done)
}

func (t *vlessTunnel) reapStartup(readReady <-chan readyResult, readComplete bool) {
	_ = t.stdin.Close()
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
	if !readComplete {
		<-readReady
	}
	t.waitErr = t.cmd.Wait()
	close(t.done)
}

func (t *vlessTunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if network != "tcp" {
		return nil, errClientUnsupported
	}
	if host, portText, err := net.SplitHostPort(addr); err != nil || host == "" {
		return nil, &ProbeError{Code: "refused", Detail: "the VLESS probe target is invalid"}
	} else if port, err := strconv.ParseUint(portText, 10, 16); err != nil || port == 0 {
		return nil, &ProbeError{Code: "refused", Detail: "the VLESS probe target is invalid"}
	}
	select {
	case <-t.done:
		return nil, t.probeError(errors.New("VLESS helper exited"), ctx)
	default:
	}
	dialer, err := proxy.SOCKS5("tcp", t.endpoint, &t.auth, proxy.Direct)
	if err != nil {
		return nil, t.probeError(err, ctx)
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, &ProbeError{Code: "refused", Detail: "the VLESS helper SOCKS client is unavailable"}
	}
	conn, err := contextDialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, t.probeError(err, ctx)
	}
	return &vlessProbeConn{Conn: conn, tunnel: t, ctx: ctx}, nil
}

func (t *vlessTunnel) Close() error {
	t.closeOnce.Do(func() {
		_ = t.stdin.Close()
		timer := time.NewTimer(vlessHelperCloseTimeout)
		defer timer.Stop()
		select {
		case <-t.done:
		case <-timer.C:
			if t.cmd.Process != nil {
				_ = t.cmd.Process.Kill()
			}
			<-t.done
		}
		if t.release != nil {
			t.release()
		}
	})
	return t.closeErr
}

// vlessProbeConn lets a clean io.EOF through (the probe reads bodies to the end) but names a stream that ended before it
// delivered a single byte: that is the tunnel being dropped, and the helper's stderr says why.
type vlessProbeConn struct {
	net.Conn
	tunnel *vlessTunnel
	ctx    context.Context
	gotAny atomic.Bool
}

func (c *vlessProbeConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.gotAny.Store(true)
	}
	if err == nil || (errors.Is(err, io.EOF) && c.gotAny.Load()) {
		return n, err
	}
	return n, c.tunnel.probeError(err, c.ctx)
}

func (c *vlessProbeConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err != nil {
		return n, c.tunnel.probeError(err, c.ctx)
	}
	return n, nil
}

// probeError classifies a failure from the helper's stderr. The helper writes its reason before the stream ends, but the
// pipe is copied by another goroutine, so a failure without a recognised reason is re-read for a moment before it is
// given the generic label.
func (t *vlessTunnel) probeError(err error, ctx context.Context) *ProbeError {
	for i := 0; ; i++ {
		pe, known := classifyVLESSFailure(err, ctx, t.stderr.String())
		if known || i >= 10 {
			return pe
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// classifyVLESSFailure maps an error and the helper's stderr to a probe code: tls, auth, refused or timeout. known is false
// when only the generic fallback applied. Details are static text: nothing from Xray (which may name hosts, paths or the
// UUID) is stored.
func classifyVLESSFailure(err error, ctx context.Context, stderr string) (pe *ProbeError, known bool) {
	var netErr net.Error
	if (ctx != nil && ctx.Err() != nil) || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &ProbeError{Code: "timeout", Detail: "VLESS tunnel handshake timed out"}, true
	}
	// The reason comes from the helper only: err is the loopback SOCKS stream, whose "connection reset" or EOF just says
	// that the helper gave up, not why.
	low := strings.ToLower(stderr)
	switch {
	case strings.Contains(low, "certificate"), strings.Contains(low, "x509"), strings.Contains(low, "tls alert"),
		strings.Contains(low, "tls:"), strings.Contains(low, "handshake failure"):
		return &ProbeError{Code: "tls", Detail: "VLESS TLS verification failed"}, true
	case strings.Contains(low, "invalid user"), strings.Contains(low, "user not found"), strings.Contains(low, "invalid request user id"),
		strings.Contains(low, "reality auth failed"), strings.Contains(low, "processed invalid request"), strings.Contains(low, "authentication"):
		return &ProbeError{Code: "auth", Detail: "the VLESS probe credential was rejected"}, true
	case strings.Contains(low, "refused"), strings.Contains(low, "unreachable"), strings.Contains(low, "connection reset"),
		strings.Contains(low, "no route to host"):
		return &ProbeError{Code: "refused", Detail: "the VLESS server refused the connection"}, true
	case strings.Contains(low, "deadline exceeded"), strings.Contains(low, "i/o timeout"), strings.Contains(low, "timed out"):
		return &ProbeError{Code: "timeout", Detail: "VLESS tunnel handshake timed out"}, true
	case (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) && strings.Contains(low, vlesswire.TunnelMarker):
		// The transport (and REALITY/TLS) went up, the VLESS request went out, and the server hung up without a word: a
		// VLESS server does exactly this for an unknown UUID.
		return &ProbeError{Code: "auth", Detail: "the VLESS server dropped the tunnel after the handshake (credential rejected)"}, true
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return &ProbeError{Code: "refused", Detail: "the VLESS tunnel ended before the probe completed"}, false
	case strings.Contains(strings.ToLower(errorText(err)), "timeout"):
		return &ProbeError{Code: "timeout", Detail: "VLESS tunnel handshake timed out"}, false
	default:
		return &ProbeError{Code: "refused", Detail: "VLESS tunnel connection failed"}, false
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

type tailBuffer struct {
	mu       sync.Mutex
	limit    int
	data     []byte
	markerAt time.Time // when the tunnel marker was first seen in the stream
}

// noteMarker stamps the first time the tunnel marker is in the buffer (b.mu held).
func (b *tailBuffer) noteMarker() {
	if b.markerAt.IsZero() && strings.Contains(string(b.data), vlesswire.TunnelMarker) {
		b.markerAt = time.Now()
	}
}

func (b *tailBuffer) markerTime() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.markerAt
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	defer b.noteMarker()
	if len(p) >= b.limit {
		b.data = append(b.data[:0], p[len(p)-b.limit:]...)
		return len(p), nil
	}
	if overflow := len(b.data) + len(p) - b.limit; overflow > 0 {
		b.data = append(b.data[:0], b.data[overflow:]...)
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// ConnectedAt is when Xray reported the VLESS transport up ("tunneling request": TCP and REALITY/TLS to the node are
// done, the VLESS request goes out next). The SOCKS5 reply of the helper comes before that, so a dial through this
// tunnel says nothing about the node. Zero until the helper has logged it.
func (t *vlessTunnel) ConnectedAt() time.Time { return t.stderr.markerTime() }

// ClassifyRequestError names a failed HTTP request made through the tunnel, including the probe's own deadline.
func (t *vlessTunnel) ClassifyRequestError(err error) *ProbeError { return t.probeError(err, nil) }
