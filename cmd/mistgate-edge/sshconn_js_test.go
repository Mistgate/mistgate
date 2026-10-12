//go:build js && wasm

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math/rand"
	"net"
	"net/netip"
	"os"
	"strings"
	"syscall/js"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/provision"
)

// The tests of the SSH transport run against a scripted JS socket (jsConn alone) and against the Node stand-in of the
// Worker's sshConnect, which connects to the fake SSH server of cmd/mistgate-edge/testdata/fakesshd. Both are set up by
// scripts/test-edge-store.ps1; the Node streams are not workerd's, which the rounds that run workerd check.

func scriptedSocket() js.Value { return js.Global().Get("__scriptedSocket").Invoke() }

func jsBytes(b []byte) js.Value {
	out := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(out, b)
	return out
}

func connOver(sock js.Value) *jsConn { return newJSConn(sock, "45.67.89.10:22") }

type readOutcome struct {
	n   int
	err error
}

// readAsync starts a Read of size bytes and reports it on the channel.
func readAsync(c net.Conn, size int) <-chan readOutcome {
	out := make(chan readOutcome, 1)
	go func() {
		n, err := c.Read(make([]byte, size))
		out <- readOutcome{n, err}
	}()
	return out
}

func mustPend(t *testing.T, out <-chan readOutcome, why string) {
	t.Helper()
	select {
	case r := <-out:
		t.Fatalf("Read returned (%d, %v), but %s", r.n, r.err, why)
	case <-time.After(150 * time.Millisecond):
	}
}

func mustReturn(t *testing.T, out <-chan readOutcome, why string) readOutcome {
	t.Helper()
	select {
	case r := <-out:
		return r
	case <-time.After(2 * time.Second):
		t.Fatalf("Read did not return: %s", why)
		return readOutcome{}
	}
}

func TestJSConnChunksAndRemainder(t *testing.T) {
	sock := scriptedSocket()
	c := connOver(sock)
	defer c.Close()
	sock.Call("push", jsBytes([]byte("0123456789")))
	sock.Call("push", jsBytes([]byte("abc")))
	var got []string
	buf := make([]byte, 4)
	for range 4 { // a chunk larger than the caller's buffer is handed out in pieces
		n, err := c.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, string(buf[:n]))
	}
	if strings.Join(got, "|") != "0123|4567|89|abc" {
		t.Fatalf("reads = %v", got)
	}
	sock.Call("end")
	for range 2 { // the end of the stream stays the end
		if n, err := c.Read(buf); n != 0 || err != io.EOF {
			t.Fatalf("after the end Read = (%d, %v), want EOF", n, err)
		}
	}
}

func TestJSConnSocketFailureIsNotEOF(t *testing.T) {
	sock := scriptedSocket()
	c := connOver(sock)
	defer c.Close()
	sock.Call("fail", "connection reset by peer")
	_, err := c.Read(make([]byte, 8))
	if err == nil || err == io.EOF || !errors.Is(err, errConnLost) {
		t.Fatalf("Read = %v, want the lost-connection error", err)
	}
	if strings.Contains(err.Error(), "reset by peer") {
		t.Fatalf("the JS error text leaked into the Go error: %v", err)
	}
}

func TestJSConnDeadlineIsATimeoutAndTheHangingReadIsReused(t *testing.T) {
	sock := scriptedSocket()
	c := connOver(sock)
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(60 * time.Millisecond))
	_, err := c.Read(make([]byte, 8))
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read = %v, want a timeout net.Error", err)
	}
	// A deadline already past times out without asking the socket again.
	if _, err := c.Read(make([]byte, 8)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("second Read = %v", err)
	}
	_ = c.SetReadDeadline(time.Time{})
	out := readAsync(c, 8)
	sock.Call("push", jsBytes([]byte("late")))
	if r := mustReturn(t, out, "the chunk that arrived after the timeout"); r.err != nil || r.n != 4 {
		t.Fatalf("Read = (%d, %v), want the late chunk", r.n, r.err)
	}
	if calls := sock.Get("readCalls").Int(); calls != 1 {
		t.Fatalf("the socket was asked to read %d times, want the one hanging read reused", calls)
	}
}

// x/crypto/ssh clears the handshake deadline after its reader is already blocked in Read: that Read must not time out.
func TestJSConnDeadlineMovedWhileReading(t *testing.T) {
	sock := scriptedSocket()
	c := connOver(sock)
	defer c.Close()

	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	out := readAsync(c, 8)
	time.Sleep(40 * time.Millisecond)
	_ = c.SetDeadline(time.Time{})
	mustPend(t, out, "the deadline was cleared before it passed")
	time.Sleep(200 * time.Millisecond) // beyond the cleared deadline
	mustPend(t, out, "the cleared deadline still fired")

	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond)) // and a deadline set later fires on the same Read
	if r := mustReturn(t, out, "a deadline set while the Read waits"); !errors.Is(r.err, os.ErrDeadlineExceeded) {
		t.Fatalf("Read = (%d, %v), want a timeout", r.n, r.err)
	}
}

func TestJSConnCloseUnblocksReadAndWrite(t *testing.T) {
	sock := scriptedSocket()
	c := connOver(sock)
	reading := readAsync(c, 8)

	sock.Call("hold") // the next write never finishes on its own
	writing := make(chan error, 1)
	go func() { _, err := c.Write([]byte("stuck")); writing <- err }()
	mustPend(t, reading, "nothing was sent")
	select {
	case err := <-writing:
		t.Fatalf("Write returned %v while the socket holds it", err)
	case <-time.After(100 * time.Millisecond):
	}

	_ = c.Close()
	if r := mustReturn(t, reading, "Close must wake a hanging Read"); !errors.Is(r.err, net.ErrClosed) {
		t.Fatalf("Read after Close = %v, want net.ErrClosed (and not the socket's own rejection)", r.err)
	}
	select {
	case err := <-writing:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Write after Close = %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake the held Write")
	}
	if _, err := c.Write([]byte("more")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Write on a closed conn = %v", err)
	}
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Read on a closed conn = %v", err)
	}
	_ = c.Close()
	if calls := sock.Get("closeCalls").Int(); calls != 1 {
		t.Fatalf("the socket was closed %d times, want once", calls)
	}
	sock.Call("release")
}

// The socket rejects its pending read when it is closed; once the conn is closed that is not an error to report.
func TestJSConnSwallowsTheReadErrorAfterClose(t *testing.T) {
	sock := scriptedSocket()
	c := connOver(sock)
	out := readAsync(c, 8)
	mustPend(t, out, "nothing was sent")
	_ = c.Close() // the scripted socket's close() rejects the read in flight
	time.Sleep(50 * time.Millisecond)
	if r := mustReturn(t, out, "after Close"); !errors.Is(r.err, net.ErrClosed) {
		t.Fatalf("Read = %v, want net.ErrClosed", r.err)
	}
}

func TestJSConnWriteDeadline(t *testing.T) {
	sock := scriptedSocket()
	c := connOver(sock)
	defer c.Close()
	if n, err := c.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("Write = (%d, %v)", n, err)
	}
	if got := sock.Get("written").Index(0); got.Length() != 3 {
		t.Fatalf("the socket got %d bytes in the first write", got.Length())
	}
	sock.Call("hold")
	_ = c.SetWriteDeadline(time.Now().Add(60 * time.Millisecond))
	_, err := c.Write([]byte("blocked"))
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Write = %v, want a timeout net.Error", err)
	}
	sock.Call("release")
}

// sshConnect and the connections it opens, through the same Client the Provisioner uses.

func fakeSSHD(t *testing.T) (user, password, fingerprint string) {
	t.Helper()
	if os.Getenv("MG_FAKESSHD_ADDR") == "" {
		t.Skip("MG_FAKESSHD_ADDR is not set: run this through scripts/test-edge-store.ps1, which starts the fake SSH server")
	}
	return os.Getenv("MG_FAKESSHD_USER"), os.Getenv("MG_FAKESSHD_PASS"), os.Getenv("MG_FAKESSHD_FP")
}

func testSSHClient() *provision.Client {
	dial := sshSockets{connect: js.Global().Get("__sshConnect")}
	return provision.NewClientWith(dial.DialContext, func(context.Context, string) ([]netip.Addr, error) {
		return nil, errors.New("these tests dial addresses, not names")
	})
}

func target(t *testing.T, host string) provision.Target {
	t.Helper()
	target, err := provision.NewTarget(host, 22)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func TestFingerprintOverTheSocket(t *testing.T) {
	_, _, fingerprint := fakeSSHD(t)
	got, algorithm, err := testSSHClient().Fingerprint(context.Background(), target(t, "45.67.89.10"))
	if err != nil {
		t.Fatal(err)
	}
	if got != fingerprint || algorithm != "ssh-ed25519" {
		t.Fatalf("fingerprint = %q (%s), want %q", got, algorithm, fingerprint)
	}
}

func TestDialAsUploadsAndChecksTheBytes(t *testing.T) {
	user, password, fingerprint := fakeSSHD(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := testSSHClient().DialAs(ctx, target(t, "45.67.89.10"), user, password, fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	run := func(command string, stdin io.Reader) string {
		t.Helper()
		session, err := conn.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		var out bytes.Buffer
		session.Stdin, session.Stdout = stdin, &out
		if err := session.Run(command); err != nil {
			t.Fatalf("%q: %v", command, err)
		}
		return out.String()
	}
	if got := run("uname -a", nil); !strings.HasPrefix(got, "Linux fakesshd") {
		t.Fatalf("uname -a = %q", got)
	}
	// An idle login stays up between two commands.
	time.Sleep(300 * time.Millisecond)

	payload := make([]byte, 8<<20)
	rand.New(rand.NewSource(7)).Read(payload)
	run("cat > /root/mistgate-node.new", bytes.NewReader(payload))
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:]) + "  /root/mistgate-node.new\n"
	if got := run("sha256sum /root/mistgate-node.new", nil); got != want {
		t.Fatalf("the server holds %q, want %q", got, want)
	}
}

func TestDialAsRefusesAWrongPasswordAndAChangedHostKey(t *testing.T) {
	user, password, fingerprint := fakeSSHD(t)
	ctx := context.Background()
	client := testSSHClient()
	if _, err := client.DialAs(ctx, target(t, "45.67.89.10"), user, password+"x", fingerprint); !errors.Is(err, provision.ErrAuthRejected) {
		t.Fatalf("a wrong password: %v, want ErrAuthRejected", err)
	}
	other := "SHA256:" + strings.Repeat("B", 43)
	if _, err := client.DialAs(ctx, target(t, "45.67.89.10"), user, password, other); !errors.Is(err, provision.ErrHostKey) {
		t.Fatalf("a changed host key: %v, want ErrHostKey", err)
	}
}

func TestSocketOpenFailuresKeepTheirKind(t *testing.T) {
	fakeSSHD(t)
	client := testSSHClient()
	ctx := context.Background()
	if _, _, err := client.Fingerprint(ctx, target(t, "45.67.89.11")); !errors.Is(err, provision.ErrPlatformRefused) {
		t.Fatalf("refused: %v, want ErrPlatformRefused", err)
	}
	_, _, err := client.Fingerprint(ctx, target(t, "45.67.89.12"))
	if err == nil || errors.Is(err, provision.ErrPlatformRefused) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("failed: %v, want a plain failure", err)
	}
}

// A server that accepts and never speaks ends with the caller's deadline, in time, and its socket is closed.
func TestASilentServerEndsWithTheContext(t *testing.T) {
	fakeSSHD(t)
	closedBefore := js.Global().Get("__sshStats").Get("closed").Int()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := testSSHClient().Fingerprint(ctx, target(t, "45.67.89.13"))
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Fingerprint = %v, want a timeout (the handshake deadline is the context's)", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("took %v: the deadline did not end the handshake", took)
	}
	deadline := time.Now().Add(2 * time.Second)
	for js.Global().Get("__sshStats").Get("closed").Int() == closedBefore {
		if time.Now().After(deadline) {
			t.Fatal("the socket of the abandoned handshake was never closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// sshConnect is told how long it may take, and a socket that opens after Go stopped waiting is closed.
func TestASocketThatOpensLateIsClosed(t *testing.T) {
	fakeSSHD(t)
	stats := js.Global().Get("__sshStats")
	openedBefore, closedBefore := stats.Get("opened").Int(), stats.Get("closed").Int()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(60 * time.Millisecond); cancel() }()
	dial := sshSockets{connect: js.Global().Get("__sshConnect")}
	if _, err := dial.DialContext(ctx, "tcp", "45.67.89.14:22"); !errors.Is(err, context.Canceled) {
		t.Fatalf("DialContext = %v, want the cancellation", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for stats.Get("opened").Int() == openedBefore || stats.Get("closed").Int() == closedBefore {
		if time.Now().After(deadline) {
			t.Fatalf("opened %d->%d, closed %d->%d: the late socket was kept", openedBefore, stats.Get("opened").Int(), closedBefore, stats.Get("closed").Int())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The Worker's own limit comes back as a timeout.
	short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	_, err := dial.DialContext(short, "tcp", "45.67.89.14:22")
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("DialContext with a short limit = %v, want a timeout", err)
	}
}

func TestDialContextRefusesWhatIsNotAHostPort(t *testing.T) {
	dial := sshSockets{connect: js.Global().Get("__sshConnect")}
	for _, address := range []string{"45.67.89.10", "45.67.89.10:0", "45.67.89.10:70000", ":22"} {
		if _, err := dial.DialContext(context.Background(), "tcp", address); err == nil {
			t.Errorf("DialContext(%q) = nil error", address)
		}
	}
	if _, err := dial.DialContext(context.Background(), "udp", "45.67.89.10:22"); err == nil {
		t.Error("DialContext(udp) = nil error")
	}
	// a sshConnect that is not a function is an error, not a panic
	if _, err := (sshSockets{connect: js.Undefined()}).DialContext(context.Background(), "tcp", "45.67.89.10:22"); err == nil {
		t.Error("an undefined sshConnect dialed")
	}
}
