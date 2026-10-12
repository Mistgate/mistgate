//go:build js && wasm

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall/js"
	"time"

	"github.com/mistgate/mistgate/edge/d1driver"
	"github.com/mistgate/mistgate/internal/panel/provision"
)

// The SSH transport of the edge edition: a net.Conn over a socket object the Worker hands out. Go never touches a stream;
// the Worker's sshConnect owns the cloudflare:sockets Socket and Go calls three functions on the object it returns.
//
//	sshConnect(host, port, timeoutMs) -> Promise<
//	    { ok: true, read(): Promise<Uint8Array | null>, write(Uint8Array): Promise<void>, close(): void }
//	  | { ok: false, error: "refused" | "failed" | "timeout" } >
//
// "refused" is the platform saying no (a Cloudflare, private or loopback address) or a closed port: the sockets do not
// tell the two apart, so Go words it as "refused or unreachable" (provision.ErrPlatformRefused). "timeout" is the
// Worker's own limit on opening the socket, timeoutMs, which Go derives from its context so a socket that Go gave up
// on does not hold one of the Worker's six outgoing connections for long. read resolves null at the end of the stream.
//
// The socket belongs to the I/O context it was opened in, so this must only run in the isolate of the Provisioner
// object (edge/worker/src/provisioner.ts), where Go serves that one object and nothing else.

var errConnLost = errors.New("ssh socket: connection lost")

const (
	sshOpenDefault = 30 * time.Second
	sshOpenMax     = 60 * time.Second
)

// sshSockets dials through the Worker's sshConnect.
type sshSockets struct{ connect js.Value }

// DialContext opens a TCP socket to address (host:port). Any other network is refused.
func (s sshSockets) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" {
		return nil, fmt.Errorf("sshConnect: network %q is not supported", network)
	}
	host, portText, err := net.SplitHostPort(address)
	port, portErr := strconv.Atoi(portText)
	if err != nil || portErr != nil || host == "" || port < 1 || port > 65535 {
		return nil, fmt.Errorf("sshConnect: %q is not a host:port", address)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("sshConnect: %w", err)
	}
	limit := sshOpenDefault
	if deadline, ok := ctx.Deadline(); ok {
		limit = min(time.Until(deadline), sshOpenMax)
		if limit <= 0 {
			return nil, fmt.Errorf("sshConnect: %w", context.DeadlineExceeded)
		}
	}
	promise, err := safeInvoke(s.connect, host, port, limit.Milliseconds()+1)
	if err != nil {
		return nil, fmt.Errorf("sshConnect: %w", err)
	}
	outcome := watch(promise)
	select {
	case result := <-outcome:
		return sshSocketResult(result, address)
	case <-ctx.Done():
		// The socket may still open: whoever gets it after Go gave up closes it at once.
		go func() {
			if result := <-outcome; result.err == nil && result.value.Type() == js.TypeObject && result.value.Get("ok").Truthy() {
				_, _ = safeCall(result.value, "close")
			}
		}()
		return nil, fmt.Errorf("sshConnect: %w", ctx.Err())
	}
}

func sshSocketResult(result settled, address string) (net.Conn, error) {
	if result.err != nil || result.value.Type() != js.TypeObject || result.value.IsNull() {
		return nil, fmt.Errorf("sshConnect: %w", errConnLost)
	}
	v := result.value
	if v.Get("ok").Truthy() {
		return newJSConn(v, address), nil
	}
	switch kind := v.Get("error"); {
	case kind.Type() == js.TypeString && kind.String() == "refused":
		return nil, fmt.Errorf("sshConnect: %w", provision.ErrPlatformRefused)
	case kind.Type() == js.TypeString && kind.String() == "timeout":
		return nil, fmt.Errorf("sshConnect: %w", os.ErrDeadlineExceeded)
	}
	return nil, errors.New("sshConnect: could not connect")
}

type settled struct {
	value js.Value
	err   error
}

// watch reports how a promise settles on the returned channel (once), whoever is still waiting for it.
func watch(promise js.Value) <-chan settled {
	out := make(chan settled, 1)
	var once sync.Once
	var onValue, onError js.Func
	finish := func(s settled) {
		once.Do(func() {
			out <- s
			go func() { onValue.Release(); onError.Release() }()
		})
	}
	onValue = js.FuncOf(func(_ js.Value, args []js.Value) any {
		v := js.Undefined()
		if len(args) > 0 {
			v = args[0]
		}
		finish(settled{value: v})
		return nil
	})
	onError = js.FuncOf(func(js.Value, []js.Value) any {
		finish(settled{err: errConnLost})
		return nil
	})
	promise.Call("then", onValue, onError)
	return out
}

// safeInvoke and safeCall turn the exception of a JS function that is not what it should be into an error.
func safeInvoke(fn js.Value, args ...any) (value js.Value, err error) {
	defer func() {
		if recover() != nil {
			value, err = js.Undefined(), errConnLost
		}
	}()
	return fn.Invoke(args...), nil
}

func safeCall(object js.Value, method string, args ...any) (value js.Value, err error) {
	defer func() {
		if recover() != nil {
			value, err = js.Undefined(), errConnLost
		}
	}()
	return object.Call(method, args...), nil
}

type readResult struct {
	data []byte
	err  error
}

// jsConn is a net.Conn over the socket object. Reads reuse one JS read() until it settles, so a read deadline that
// fires does not lose the chunk that arrives later; deadlines can be moved or cleared while a Read is waiting (x/crypto/ssh
// clears the handshake deadline after its reader is already blocked); Close wakes every Read and Write.
type jsConn struct {
	sock   js.Value
	remote net.Addr
	ctx    context.Context
	cancel context.CancelFunc

	wmu sync.Mutex // one Write at a time

	mu       sync.Mutex
	buf      []byte
	pending  chan readResult // the read() in flight, if any
	rerr     error           // io.EOF or the failure that ended the stream; returned once the buffer is drained
	rdl      time.Time
	wdl      time.Time
	rdlMoved chan struct{} // closed and replaced whenever the read deadline changes
}

func newJSConn(sock js.Value, address string) *jsConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &jsConn{sock: sock, remote: sshAddr(address), ctx: ctx, cancel: cancel, rdlMoved: make(chan struct{})}
}

// startRead returns the read in flight, starting one if there is none. Call with c.mu held.
func (c *jsConn) startRead() chan readResult {
	if c.pending != nil {
		return c.pending
	}
	ch := make(chan readResult, 1)
	c.pending = ch
	go func() {
		promise, err := safeCall(c.sock, "read")
		if err != nil {
			ch <- readResult{err: err}
			return
		}
		v, err := d1driver.Await(c.ctx, promise)
		switch {
		case err != nil:
			ch <- readResult{err: errConnLost}
		case v.IsNull() || v.IsUndefined():
			ch <- readResult{err: io.EOF}
		default:
			data := make([]byte, v.Get("byteLength").Int())
			js.CopyBytesToGo(data, v)
			ch <- readResult{data: data}
		}
	}()
	return ch
}

func (c *jsConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		c.mu.Lock()
		if c.ctx.Err() != nil {
			c.mu.Unlock()
			return 0, net.ErrClosed
		}
		if len(c.buf) > 0 {
			n := copy(p, c.buf)
			c.buf = c.buf[n:]
			c.mu.Unlock()
			return n, nil
		}
		if c.rerr != nil {
			err := c.rerr
			c.mu.Unlock()
			return 0, err
		}
		deadline := c.rdl
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			c.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		ch, moved := c.startRead(), c.rdlMoved
		c.mu.Unlock()

		var expired <-chan time.Time
		var timer *time.Timer
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			expired = timer.C
		}
		select {
		case r := <-ch:
			if timer != nil {
				timer.Stop()
			}
			c.mu.Lock()
			if c.pending == ch {
				c.pending = nil
			}
			// A failure that arrives after Close is the socket saying it was closed: not news.
			if r.err != nil && c.ctx.Err() == nil {
				c.rerr = r.err
			} else if r.err == nil {
				c.buf = r.data
			}
			c.mu.Unlock()
		case <-expired:
			return 0, os.ErrDeadlineExceeded
		case <-moved:
			if timer != nil {
				timer.Stop()
			}
		case <-c.ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return 0, net.ErrClosed
		}
	}
}

func (c *jsConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.ctx.Err() != nil {
		return 0, net.ErrClosed
	}
	c.mu.Lock()
	deadline := c.wdl
	c.mu.Unlock()
	ctx := c.ctx
	if !deadline.IsZero() {
		if !time.Now().Before(deadline) {
			return 0, os.ErrDeadlineExceeded
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	chunk := js.Global().Get("Uint8Array").New(len(p))
	js.CopyBytesToJS(chunk, p)
	promise, err := safeCall(c.sock, "write", chunk)
	if err == nil {
		_, err = d1driver.Await(ctx, promise)
	}
	switch {
	case err == nil:
		return len(p), nil
	case c.ctx.Err() != nil:
		return 0, net.ErrClosed
	case errors.Is(err, context.DeadlineExceeded):
		return 0, os.ErrDeadlineExceeded
	}
	return 0, errConnLost
}

// Close ends the socket and wakes every waiting Read and Write; it is safe to call more than once.
func (c *jsConn) Close() error {
	c.mu.Lock()
	already := c.ctx.Err() != nil
	c.cancel()
	c.mu.Unlock()
	if !already {
		_, _ = safeCall(c.sock, "close")
	}
	return nil
}

type sshAddr string

func (a sshAddr) Network() string { return "tcp" }
func (a sshAddr) String() string  { return string(a) }

func (c *jsConn) LocalAddr() net.Addr  { return sshAddr("worker") }
func (c *jsConn) RemoteAddr() net.Addr { return c.remote }

func (c *jsConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.wdl = t
	c.moveReadDeadline(t)
	c.mu.Unlock()
	return nil
}

func (c *jsConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.moveReadDeadline(t)
	c.mu.Unlock()
	return nil
}

func (c *jsConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.wdl = t
	c.mu.Unlock()
	return nil
}

// moveReadDeadline stores t and wakes a Read that is waiting on the old one. Call with c.mu held.
func (c *jsConn) moveReadDeadline(t time.Time) {
	c.rdl = t
	close(c.rdlMoved)
	c.rdlMoved = make(chan struct{})
}
