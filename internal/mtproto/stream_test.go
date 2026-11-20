package mtproto

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mtclient "github.com/mistgate/mistgate/internal/mtproto/client"
	"github.com/mistgate/mistgate/internal/mtproto/mtprototest"
)

const streamTestCredential = "alice"

var streamTestKey = testKey(3)

type streamTestObserver struct {
	mu     sync.Mutex
	up     map[string]uint64
	down   map[string]uint64
	opened chan SessionInfo
	closed chan SessionInfo
}

func newStreamTestObserver() *streamTestObserver {
	return &streamTestObserver{
		up: make(map[string]uint64), down: make(map[string]uint64),
		opened: make(chan SessionInfo, 8), closed: make(chan SessionInfo, 8),
	}
}

func (o *streamTestObserver) Opened(info SessionInfo) {
	select {
	case o.opened <- info:
	default:
	}
}

func (o *streamTestObserver) Closed(info SessionInfo) {
	select {
	case o.closed <- info:
	default:
	}
}

func (o *streamTestObserver) AddTraffic(info SessionInfo, up, down uint64) {
	o.mu.Lock()
	o.up[info.CredentialID] += up
	o.down[info.CredentialID] += down
	o.mu.Unlock()
}

func (o *streamTestObserver) traffic(id string) (uint64, uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.up[id], o.down[id]
}

type streamTestHarness struct {
	srv    *Server
	dc     *mtprototest.DC
	dcSeen chan int16
	obs    *streamTestObserver
	dialed atomic.Int32
}

func newStreamTestHarness(t *testing.T, maxSessions int, handshakeTimeout time.Duration) *streamTestHarness {
	t.Helper()
	h := &streamTestHarness{dcSeen: make(chan int16, 8), obs: newStreamTestObserver()}
	h.dc = mtprototest.NewDC(t, func(c *mtprototest.DCConn) {
		select {
		case h.dcSeen <- c.DC:
		default:
		}
		mtprototest.Echo(c)
	})
	cfg := Config{
		Credentials:      []Credential{{ID: streamTestCredential, Key: streamTestKey}},
		Domains:          []string{"de1.example.com"},
		Observer:         h.obs,
		HandshakeTimeout: handshakeTimeout,
		StatsInterval:    5 * time.Millisecond,
		Dialer: dialerFunc(func(ctx context.Context, network, addr string) (net.Conn, error) {
			h.dialed.Add(1)
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		}),
		DCAddress: func(dc int16) (string, bool) {
			return h.dc.Addr, dc == 2 || dc == -2
		},
	}
	if maxSessions > 0 {
		cfg.MaxSessionsPerCredential = maxSessions
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	h.srv = srv
	t.Cleanup(srv.CloseAll)
	return h
}

type streamTestRun struct {
	raw  net.Conn
	done chan struct{}
	err  error
}

func (r *streamTestRun) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(5 * time.Second):
		t.Fatal("MTProto stream did not finish")
		return nil
	}
}

func (h *streamTestHarness) run(t *testing.T, serve func(net.Conn) error) *streamTestRun {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	r := &streamTestRun{raw: clientConn, done: make(chan struct{})}
	go func() {
		r.err = serve(serverConn)
		close(r.done)
	}()
	t.Cleanup(func() {
		_ = clientConn.Close()
		select {
		case <-r.done:
		case <-time.After(5 * time.Second):
			t.Error("MTProto test connection goroutine did not finish")
		}
	})
	return r
}

func (h *streamTestHarness) open(t *testing.T, key [KeyLen]byte, tag uint32, dc int16) (*streamTestRun, *mtclient.StreamClient) {
	t.Helper()
	r := h.run(t, func(conn net.Conn) error {
		return h.srv.ServeStream(context.Background(), conn, streamTestCredential)
	})
	c, err := mtclient.StartStreamClient(r.raw, key, tag, dc)
	if err != nil {
		t.Fatalf("start plain obfuscated2 client: %v", err)
	}
	return r, c
}

func (h *streamTestHarness) openFakeTLS(t *testing.T) (*streamTestRun, *mtprototest.Client) {
	t.Helper()
	r := h.run(t, func(conn net.Conn) error { return h.srv.Serve(context.Background(), conn) })
	hello := mtprototest.Hello(streamTestKey, "de1.example.com", uint32(time.Now().Unix()))
	c, err := mtprototest.Start(r.raw, streamTestKey, hello)
	if err != nil {
		t.Fatalf("start Fake TLS client: %v", err)
	}
	return r, c
}

func receiveSession(t *testing.T, ch <-chan SessionInfo) SessionInfo {
	t.Helper()
	select {
	case info := <-ch:
		return info
	case <-time.After(5 * time.Second):
		t.Fatal("Observer did not receive a session event")
		return SessionInfo{}
	}
}

func TestServeStreamRoundTripAndObserver(t *testing.T) {
	h := newStreamTestHarness(t, 0, 2*time.Second)
	r, c := h.open(t, streamTestKey, mtprototest.TagPadded, -2)
	opened := receiveSession(t, h.obs.opened)
	if opened.CredentialID != streamTestCredential || opened.DC != -2 || opened.Tag != TagPadded {
		t.Fatalf("opened session = %+v", opened)
	}
	if got := h.dialed.Load(); got != 1 {
		t.Fatalf("datacenter dial count = %d, want 1", got)
	}

	if _, err := c.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len("dc:hello"))
	if _, err := io.ReadFull(c, got); err != nil {
		t.Fatalf("read echoed traffic: %v", err)
	}
	if string(got) != "dc:hello" {
		t.Fatalf("echo = %q", got)
	}
	select {
	case dc := <-h.dcSeen:
		if dc != -2 {
			t.Fatalf("DC saw id %d, want media DC -2", dc)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fake DC did not receive a stream")
	}

	_ = r.raw.Close()
	if err := r.wait(t); err != nil {
		t.Fatalf("ServeStream = %v", err)
	}
	closed := receiveSession(t, h.obs.closed)
	if closed.ID != opened.ID || closed.CredentialID != streamTestCredential {
		t.Fatalf("closed session = %+v, opened = %+v", closed, opened)
	}
	up, down := h.obs.traffic(streamTestCredential)
	if up == 0 || down == 0 {
		t.Fatalf("Observer traffic for alice = up %d, down %d", up, down)
	}
}

func TestServeStreamRejectsBadHeadersWithoutDial(t *testing.T) {
	tests := []struct {
		name  string
		key   [KeyLen]byte
		tag   uint32
		dc    int16
		short bool
	}{
		{name: "wrong key", key: testKey(9), tag: mtprototest.TagPadded, dc: 2},
		{name: "malformed tag", key: streamTestKey, tag: 0, dc: 2},
		{name: "unknown DC", key: streamTestKey, tag: mtprototest.TagPadded, dc: 10002},
		{name: "short header", key: streamTestKey, short: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newStreamTestHarness(t, 0, 2*time.Second)
			var r *streamTestRun
			if tc.short {
				r = h.run(t, func(conn net.Conn) error {
					return h.srv.ServeStream(context.Background(), conn, streamTestCredential)
				})
				if _, err := r.raw.Write([]byte{1, 2, 3}); err != nil {
					t.Fatal(err)
				}
				_ = r.raw.Close()
			} else {
				r, _ = h.open(t, tc.key, tc.tag, tc.dc)
			}
			if err := r.wait(t); err == nil {
				t.Fatal("ServeStream succeeded for an invalid header")
			}
			if got := h.dialed.Load(); got != 0 {
				t.Fatalf("datacenter dial count = %d", got)
			}
			if opened, closed := len(h.obs.opened), len(h.obs.closed); opened != 0 || closed != 0 {
				t.Fatalf("Observer sessions opened=%d closed=%d", opened, closed)
			}
		})
	}
}

func TestServeStreamDeadlineForStalledHeader(t *testing.T) {
	h := newStreamTestHarness(t, 0, 100*time.Millisecond)
	r := h.run(t, func(conn net.Conn) error {
		return h.srv.ServeStream(context.Background(), conn, streamTestCredential)
	})
	err := r.wait(t)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("stalled header error = %v, want deadline exceeded", err)
	}
}

func TestServeStreamRejectsUnknownCredential(t *testing.T) {
	h := newStreamTestHarness(t, 0, 2*time.Second)
	r := h.run(t, func(conn net.Conn) error {
		return h.srv.ServeStream(context.Background(), conn, "unknown")
	})
	if err := r.wait(t); !errors.Is(err, errUnknownCredential) {
		t.Fatalf("ServeStream = %v, want unknown credential", err)
	}
	if got := h.dialed.Load(); got != 0 {
		t.Fatalf("datacenter dial count = %d", got)
	}
}

func TestServeStreamFailedHeadersReleaseHandshakeSlots(t *testing.T) {
	h := newStreamTestHarness(t, 0, 2*time.Second)
	for i := 0; i < 40; i++ {
		r, _ := h.open(t, testKey(9), mtprototest.TagPadded, 2)
		if err := r.wait(t); err == nil {
			t.Fatalf("failed stream %d unexpectedly succeeded", i)
		} else if errors.Is(err, ErrBusy) {
			t.Fatalf("failed stream %d exhausted handshake slots: %v", i, err)
		}
	}
}

func TestServeStreamClosesOnCredentialRemovalAndKick(t *testing.T) {
	for _, action := range []string{"remove", "kick"} {
		t.Run(action, func(t *testing.T) {
			h := newStreamTestHarness(t, 0, 2*time.Second)
			r, c := h.open(t, streamTestKey, mtprototest.TagPadded, 2)
			_ = receiveSession(t, h.obs.opened)
			if _, err := c.Write([]byte("ping")); err != nil {
				t.Fatal(err)
			}
			if _, err := io.ReadFull(c, make([]byte, len("dc:ping"))); err != nil {
				t.Fatalf("establish running stream: %v", err)
			}

			if action == "remove" {
				if err := h.srv.SetCredentials(nil); err != nil {
					t.Fatal(err)
				}
			} else if n := h.srv.CloseCredential(streamTestCredential); n != 1 {
				t.Fatalf("CloseCredential = %d, want 1", n)
			}
			if _, err := c.Read(make([]byte, 1)); err == nil {
				t.Fatal("the running stream remained open")
			}
			if err := r.wait(t); err != nil {
				t.Fatalf("ServeStream = %v", err)
			}
			closed := receiveSession(t, h.obs.closed)
			if closed.CredentialID != streamTestCredential {
				t.Fatalf("closed credential = %q", closed.CredentialID)
			}
		})
	}
}

func TestServeStreamAndFakeTLSShareSessionLimit(t *testing.T) {
	t.Run("stream counts toward Fake TLS", func(t *testing.T) {
		h := newStreamTestHarness(t, 1, 2*time.Second)
		r, _ := h.open(t, streamTestKey, mtprototest.TagPadded, 2)
		_ = receiveSession(t, h.obs.opened)
		fakeRun, fakeClient := h.openFakeTLS(t)
		if err := fakeClient.SendHeader(mtprototest.TagPadded, 2); err != nil {
			t.Fatal(err)
		}
		if err := fakeRun.wait(t); !errors.Is(err, errSessionLimit) {
			t.Fatalf("Fake TLS Serve = %v, want shared session limit", err)
		}
		if got := h.dialed.Load(); got != 1 {
			t.Fatalf("datacenter dial count = %d, want only the stream", got)
		}
		_ = r.raw.Close()
		if err := r.wait(t); err != nil {
			t.Fatalf("ServeStream = %v", err)
		}
	})
	t.Run("Fake TLS counts toward stream", func(t *testing.T) {
		h := newStreamTestHarness(t, 1, 2*time.Second)
		fakeRun, fakeClient := h.openFakeTLS(t)
		if err := fakeClient.SendHeader(mtprototest.TagPadded, 2); err != nil {
			t.Fatal(err)
		}
		_ = receiveSession(t, h.obs.opened)
		streamRun, _ := h.open(t, streamTestKey, mtprototest.TagPadded, 2)
		if err := streamRun.wait(t); !errors.Is(err, errSessionLimit) {
			t.Fatalf("ServeStream = %v, want shared session limit", err)
		}
		if got := h.dialed.Load(); got != 1 {
			t.Fatalf("datacenter dial count = %d, want only Fake TLS", got)
		}
		_ = fakeRun.raw.Close()
		if err := fakeRun.wait(t); err != nil {
			t.Fatalf("Fake TLS Serve = %v", err)
		}
	})
}

type gatedStreamWriter struct {
	net.Conn
	first       chan struct{}
	proceed     chan struct{}
	firstOnce   sync.Once
	proceedOnce sync.Once
}

func (c *gatedStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	n, err := c.Conn.Write(p[:1])
	if err != nil {
		return n, err
	}
	c.firstOnce.Do(func() { close(c.first) })
	<-c.proceed
	m, err := c.Conn.Write(p[n:])
	return n + m, err
}

func (c *gatedStreamWriter) release() {
	c.proceedOnce.Do(func() { close(c.proceed) })
}

func TestServeStreamRegistrationRechecksCredentialAndKick(t *testing.T) {
	for _, action := range []string{"remove", "kick"} {
		t.Run(action, func(t *testing.T) {
			h := newStreamTestHarness(t, 0, 2*time.Second)
			r := h.run(t, func(conn net.Conn) error {
				return h.srv.ServeStream(context.Background(), conn, streamTestCredential)
			})
			gated := &gatedStreamWriter{Conn: r.raw, first: make(chan struct{}), proceed: make(chan struct{})}
			startDone := make(chan struct{})
			var startErr error
			go func() {
				_, startErr = mtclient.StartStreamClient(gated, streamTestKey, mtprototest.TagPadded, 2)
				close(startDone)
			}()
			t.Cleanup(func() {
				gated.release()
				select {
				case <-startDone:
				case <-time.After(5 * time.Second):
					t.Error("plain client goroutine did not finish")
				}
			})
			select {
			case <-gated.first:
			case <-time.After(5 * time.Second):
				t.Fatal("the server did not read the first header byte")
			}

			if action == "remove" {
				if err := h.srv.SetCredentials(nil); err != nil {
					t.Fatal(err)
				}
			} else if n := h.srv.CloseCredential(streamTestCredential); n != 0 {
				t.Fatalf("CloseCredential before registration = %d, want 0", n)
			}
			gated.release()
			select {
			case <-startDone:
			case <-time.After(5 * time.Second):
				t.Fatal("plain header writer did not finish")
			}
			if startErr != nil {
				t.Fatalf("plain header write: %v", startErr)
			}
			if err := r.wait(t); !errors.Is(err, errRevoked) {
				t.Fatalf("ServeStream = %v, want revoked credential", err)
			}
			if got := h.dialed.Load(); got != 0 {
				t.Fatalf("datacenter dial count = %d", got)
			}
		})
	}
}
