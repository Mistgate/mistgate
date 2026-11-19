package bridge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"

	"github.com/mistgate/mistgate/internal/plugin"
)

var (
	errPanelBridgeUnavailable = errors.New("panel bridge unavailable")
	errPanelBridgeBusy        = errors.New("panel bridge control stream is already active")
)

// DialPanel opens one authenticated control stream through an already connected bridge. The entry chooses its panel target
// from its own configuration; this method does not accept a target address from the exit.
func (d *Dialer) DialPanel(ctx context.Context, entryNodeID string) (net.Conn, error) {
	d.mu.Lock()
	run := d.runs[entryNodeID]
	state := d.states[entryNodeID]
	if d.closed || run == nil || run.ctx.Err() != nil || !run.dial.PanelVia || state.Conns == 0 {
		d.mu.Unlock()
		return nil, errPanelBridgeUnavailable
	}
	if d.panelActive {
		d.mu.Unlock()
		return nil, errPanelBridgeBusy
	}
	d.panelActive = true
	dial := run.dial
	dial.Targets = append([]plugin.BridgeTarget(nil), run.dial.Targets...)
	d.mu.Unlock()

	release := func() {
		d.mu.Lock()
		d.panelActive = false
		d.mu.Unlock()
	}
	identity := d.identity()
	conn, proof, err := dialTLSWithTimeout(ctx, dial, identity, d.timing.dialTimeout, d.timing.keepAlive, nil)
	if err != nil {
		release()
		return nil, err
	}
	transport := newBridgeTransport()
	cc, err := transport.NewClientConn(conn)
	if err != nil {
		_ = conn.Close()
		release()
		return nil, err
	}
	pairID, err := newPairID()
	if err != nil {
		_ = cc.Close()
		_ = conn.Close()
		release()
		return nil, err
	}
	panelCtx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-run.ctx.Done():
			cancel()
		case <-panelCtx.Done():
		}
	}()
	responses, code, err := d.openPair(panelCtx, cancel, dial, cc, proof, pairID, StreamKindPanel)
	if err != nil || code != "" {
		cancel()
		_ = cc.Close()
		_ = conn.Close()
		release()
		if code != "" { // the entry's reason is more useful than the cancellation it caused on the other half
			return nil, errors.New(code)
		}
		return nil, err
	}
	stream := &panelStreamConn{read: responses.up.Body, down: responses.down.Body, write: responses.downWriter,
		cc: cc, conn: conn, cancel: cancel, release: release, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-stream.done:
		}
	}()
	return stream, nil
}

type panelStreamConn struct {
	read  io.ReadCloser // response body of the up half: entry to exit
	down  io.ReadCloser // response body of the down half; closed with the pair
	write *io.PipeWriter
	cc    *http2.ClientConn
	conn  net.Conn

	cancel  context.CancelFunc
	release func()
	done    chan struct{}
	once    sync.Once

	mu            sync.Mutex
	readDeadline  time.Time
	writeDeadline time.Time
	deadlineTimer *time.Timer
	expired       atomic.Bool
}

func (c *panelStreamConn) Read(p []byte) (int, error) {
	n, err := c.read.Read(p)
	if err != nil && c.expired.Load() {
		return n, os.ErrDeadlineExceeded
	}
	return n, plainConnError(err)
}

func (c *panelStreamConn) Write(p []byte) (int, error) {
	n, err := c.write.Write(p)
	if err != nil && c.expired.Load() {
		return n, os.ErrDeadlineExceeded
	}
	return n, plainConnError(err)
}

// plainConnError keeps an HTTP/2 error of the bridge's own streams from reaching the caller as it is. The control session runs an
// HTTP/2 client of its own over this conn, and that client takes a StreamError returned by its connection's read for one of its own
// stream errors (a type assertion, not errors.As): it logs it, skips it and reads again, and a conn whose error is final hands it
// out again at once, so the client spun on the reset of the bridge's stream and never saw the control session end.
func plainConnError(err error) error {
	var stream http2.StreamError
	if errors.As(err, &stream) {
		return fmt.Errorf("panel bridge stream: %w", err)
	}
	return err
}

func (c *panelStreamConn) Close() error {
	var err error
	c.once.Do(func() {
		close(c.done)
		c.mu.Lock()
		if c.deadlineTimer != nil {
			c.deadlineTimer.Stop()
			c.deadlineTimer = nil
		}
		c.mu.Unlock()
		c.cancel()
		_ = c.write.Close()
		_ = c.read.Close()
		_ = c.down.Close()
		_ = c.cc.Close()
		err = c.conn.Close()
		c.release()
	})
	return err
}

func (c *panelStreamConn) CloseWrite() error { return c.write.Close() }
func (c *panelStreamConn) CloseRead() error  { return c.read.Close() }
func (c *panelStreamConn) LocalAddr() net.Addr {
	return panelBridgeAddr("local")
}
func (c *panelStreamConn) RemoteAddr() net.Addr {
	return panelBridgeAddr("panel")
}
func (c *panelStreamConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline, c.writeDeadline = t, t
	c.resetDeadlineLocked()
	c.mu.Unlock()
	return nil
}
func (c *panelStreamConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.resetDeadlineLocked()
	c.mu.Unlock()
	return nil
}
func (c *panelStreamConn) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.resetDeadlineLocked()
	c.mu.Unlock()
	return nil
}

func (c *panelStreamConn) resetDeadlineLocked() {
	if c.deadlineTimer != nil {
		c.deadlineTimer.Stop()
		c.deadlineTimer = nil
	}
	var next time.Time
	for _, deadline := range []time.Time{c.readDeadline, c.writeDeadline} {
		if !deadline.IsZero() && (next.IsZero() || deadline.Before(next)) {
			next = deadline
		}
	}
	if next.IsZero() {
		return
	}
	delay := time.Until(next)
	c.deadlineTimer = time.AfterFunc(max(delay, 0), func() {
		c.expired.Store(true)
		_ = c.Close()
	})
}

type panelBridgeAddr string

func (a panelBridgeAddr) Network() string { return "bridge" }
func (a panelBridgeAddr) String() string  { return strings.Trim(string(a), " ") }

var _ net.Conn = (*panelStreamConn)(nil)
