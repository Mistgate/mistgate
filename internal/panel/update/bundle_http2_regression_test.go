package update

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
)

type rb2Svc struct {
	agentv1connect.UnimplementedAgentServiceHandler
	s    *Service
	node string
	ret  chan error
}

// rb2Svc adapts the update service to the Connect handler used by these transport regressions.
func (a rb2Svc) FetchUpdate(ctx context.Context, req *connect.Request[agentv1.FetchUpdateRequest], stream *connect.ServerStream[agentv1.FetchUpdateResponse]) error {
	err := a.s.ServeWithWriteDeadline(ctx, a.node, req.Msg.Name, req.Msg.Offset, WriteDeadlineFromContext(ctx), func(chunk []byte, total uint64) error {
		return stream.Send(&agentv1.FetchUpdateResponse{Data: chunk, TotalSize: total})
	})
	select {
	case a.ret <- err:
	default:
	}
	return err
}

type rb2Server struct {
	srv      *httptest.Server
	in, out  atomic.Int32
	newConns atomic.Int32
	svc      rb2Svc
}

// newRB2Server wires the update stream with the agent listener's production timeouts.
func newRB2Server(t *testing.T, s *Service, h2 bool, ln net.Listener, h2conf *http.HTTP2Config) *rb2Server {
	r := &rb2Server{svc: rb2Svc{s: s, node: "nod_de1", ret: make(chan error, 64)}}
	path, rpc := agentv1connect.NewAgentServiceHandler(r.svc)
	mux := http.NewServeMux()
	mux.Handle(path, rpc)
	mux.HandleFunc("/slow", func(w http.ResponseWriter, req *http.Request) {
		rc := http.NewResponseController(w)
		_ = rc.SetWriteDeadline(time.Time{})
		for i := 0; i < 8; i++ {
			if _, err := w.Write([]byte("x")); err != nil {
				return
			}
			_ = rc.Flush()
			time.Sleep(200 * time.Millisecond)
		}
	})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.in.Add(1)
		defer r.out.Add(1)
		if req.URL.Path != "/slow" {
			rc := http.NewResponseController(w)
			_ = rc.SetReadDeadline(time.Time{})
			_ = rc.SetWriteDeadline(time.Time{})
			req = req.WithContext(WithWriteDeadline(req.Context(), rc.SetWriteDeadline))
		}
		mux.ServeHTTP(w, req)
	}))
	srv.Config.ReadTimeout, srv.Config.WriteTimeout, srv.Config.IdleTimeout = 30*time.Second, 60*time.Second, 2*time.Minute
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			r.newConns.Add(1)
		}
	}
	srv.Config.HTTP2 = h2conf
	if ln != nil {
		srv.Listener.Close()
		srv.Listener = ln
	}
	srv.EnableHTTP2 = h2
	srv.StartTLS()
	t.Cleanup(srv.Close)
	r.srv = srv
	return r
}

func (r *rb2Server) client() agentv1connect.AgentServiceClient {
	return agentv1connect.NewAgentServiceClient(r.srv.Client(), r.srv.URL)
}

func rb2Wait(ch <-chan error, d time.Duration) (error, bool) {
	select {
	case err := <-ch:
		return err, true
	case <-time.After(d):
		return nil, false
	}
}

const rb2Name = "mistgate-node-linux-amd64"

// The crash scenario of round 1: the client stops reading, the stall fires, and the client resets the stream later.
func TestBundleHTTP2StallThenResetRepeated(t *testing.T) {
	for _, h2 := range []bool{true, false} {
		name := "http1"
		if h2 {
			name = "h2"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.bundle("1.0", 10, map[string][]byte{rb2Name: randBytes(24 << 20)})
			e.s.sendTimeout = 300 * time.Millisecond
			r := newRB2Server(t, e.s, h2, nil, nil)
			cl := r.client()
			for round := 0; round < 6; round++ {
				ctx, cancel := context.WithCancel(context.Background())
				stream, err := cl.FetchUpdate(ctx, connect.NewRequest(&agentv1.FetchUpdateRequest{Name: rb2Name}))
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				serveErr, ok := rb2Wait(r.svc.ret, 10*time.Second)
				if !ok {
					cancel()
					t.Fatalf("round %d: Serve did not return after the write deadline", round)
				}
				if connect.CodeOf(serveErr) != connect.CodeUnavailable || !errors.Is(serveErr, os.ErrDeadlineExceeded) {
					t.Errorf("round %d: serve error %v", round, serveErr)
				}
				time.Sleep(200 * time.Millisecond)
				cancel() // the peer resets after the deadline fired
				_ = stream.Close()
				var sink chunkSink
				if err := e.s.Serve(e.ctx, "nod_de1", rb2Name, 0, sink.send); err != nil {
					t.Fatalf("round %d: slot not free: %v", round, err)
				}
			}
			time.Sleep(time.Second)
			if r.in.Load() != r.out.Load() {
				t.Errorf("handlers entered=%d returned=%d", r.in.Load(), r.out.Load())
			}
			t.Logf("6 stall+reset rounds: no panic, handlers entered=%d returned=%d, conns=%d", r.in.Load(), r.out.Load(), r.newConns.Load())
		})
	}
}

// Another stream on the same HTTP/2 connection: a stalled download's deadline must not touch it, and a finished
// download must not leave a deadline behind.
func TestBundleHTTP2OtherStreamsOnSameConnection(t *testing.T) {
	e := newEnv(t)
	e.bundle("1.0", 10, map[string][]byte{rb2Name: randBytes(24 << 20), "mistgate-node-linux-arm64": randBytes(300 << 10)})
	e.s.sendTimeout = 300 * time.Millisecond
	r := newRB2Server(t, e.s, true, nil, nil)
	hc := r.srv.Client()
	cl := r.client()

	st, err := cl.FetchUpdate(context.Background(), connect.NewRequest(&agentv1.FetchUpdateRequest{Name: "mistgate-node-linux-arm64"}))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for st.Receive() {
		n += len(st.Msg().Data)
	}
	if st.Err() != nil || n != 300<<10 {
		t.Fatalf("small download: %d bytes, %v", n, st.Err())
	}
	<-r.svc.ret

	slowDone := make(chan error, 1)
	go func() {
		resp, err := hc.Get(r.srv.URL + "/slow")
		if err != nil {
			slowDone <- err
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err == nil && len(b) != 8 {
			err = errors.New("short body")
		}
		slowDone <- err
	}()
	sctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stalled, err := cl.FetchUpdate(sctx, connect.NewRequest(&agentv1.FetchUpdateRequest{Name: rb2Name}))
	if err != nil {
		t.Fatal(err)
	}
	_ = stalled
	if serveErr, ok := rb2Wait(r.svc.ret, 10*time.Second); !ok || !errors.Is(serveErr, os.ErrDeadlineExceeded) {
		t.Fatalf("stalled download: %v %v", serveErr, ok)
	}
	if err, ok := rb2Wait(slowDone, 10*time.Second); !ok || err != nil {
		t.Fatalf("the other stream on the same connection: %v (finished=%v)", err, ok)
	}
	t.Logf("the /slow stream (1.6 s, gaps of 200 ms > deadline 300 ms after a finished download) survived the stalled stream's reset; TCP connections opened: %d", r.newConns.Load())
}

// A slow but live reader: the whole download takes many times the stall timeout, each chunk takes less.
func TestBundleHTTP2SlowReaderThatKeepsMakingProgress(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pause time.Duration
	}{{"progress-before-timeout", 250 * time.Millisecond}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			data := randBytes(6 << 20)
			e.bundle("1.0", 10, map[string][]byte{rb2Name: data})
			e.s.sendTimeout = 400 * time.Millisecond
			r := newRB2Server(t, e.s, true, nil, nil)
			start := time.Now()
			st, err := r.client().FetchUpdate(context.Background(), connect.NewRequest(&agentv1.FetchUpdateRequest{Name: rb2Name}))
			if err != nil {
				t.Fatal(err)
			}
			got := 0
			for st.Receive() {
				got += len(st.Msg().Data)
				time.Sleep(tc.pause)
			}
			serveErr, _ := rb2Wait(r.svc.ret, 5*time.Second)
			t.Logf("pause %s per 256 KiB message: received %d of %d in %s; client err=%v; serve err=%v",
				tc.pause, got, len(data), time.Since(start).Round(time.Millisecond), st.Err(), serveErr)
			if got != len(data) || serveErr != nil {
				t.Errorf("a live reader was cut: received %d of %d, server error %v", got, len(data), serveErr)
			}
			if time.Since(start) <= e.s.sendTimeout {
				t.Errorf("download took %s, want longer than one no-progress timeout", time.Since(start))
			}
		})
	}
}

// blackholeConn: once the flag is set, writes block until the conn is closed or its write deadline passes,
// the way a TCP send buffer to a vanished peer behaves.
type blackholeLn struct {
	net.Listener
	mu    sync.Mutex
	conns []*blackholeConn
}

func (l *blackholeLn) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	bc := &blackholeConn{Conn: c, closed: make(chan struct{})}
	l.mu.Lock()
	l.conns = append(l.conns, bc)
	l.mu.Unlock()
	return bc, nil
}

func (l *blackholeLn) hole() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, c := range l.conns {
		c.hole.Store(true)
	}
}

type blackholeConn struct {
	net.Conn
	hole        atomic.Bool
	writeFailed atomic.Bool
	deadline    atomic.Int64
	once        sync.Once
	closed      chan struct{}
}

func (c *blackholeConn) Write(b []byte) (int, error) {
	if !c.hole.Load() {
		return c.Conn.Write(b)
	}
	if c.writeFailed.Load() {
		return 0, net.ErrClosed
	}
	for {
		if d := c.deadline.Load(); d != 0 && time.Now().UnixNano() > d {
			// The timeout makes this simulated TCP path terminal so TLS can finish closing the connection.
			c.writeFailed.Store(true)
			return 0, os.ErrDeadlineExceeded
		}
		select {
		case <-c.closed:
			return 0, net.ErrClosed
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (c *blackholeConn) SetWriteDeadline(t time.Time) error {
	if t.IsZero() {
		c.deadline.Store(0)
	} else {
		c.deadline.Store(t.UnixNano())
	}
	return c.Conn.SetWriteDeadline(t)
}

func (c *blackholeConn) SetDeadline(t time.Time) error {
	_ = c.SetWriteDeadline(t)
	return c.Conn.SetDeadline(t)
}

func (c *blackholeConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

// A vanished TCP peer makes no write progress; HTTP/2 WriteByteTimeout must release its node's download slot.
func TestBundleHTTP2BlackholedPeerReleasesNodeSlot(t *testing.T) {
	e := newEnv(t)
	e.bundle("1.0", 10, map[string][]byte{rb2Name: randBytes(24 << 20)})
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := &blackholeLn{Listener: inner}
	done := make(chan error, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		controller := http.NewResponseController(w)
		err := e.s.Serve(req.Context(), "nod_de1", rb2Name, 0, func(chunk []byte, _ uint64) error {
			if _, err := w.Write(chunk); err != nil {
				return err
			}
			return controller.Flush()
		})
		done <- err
	}))
	server.Config.HTTP2 = &http.HTTP2Config{WriteByteTimeout: 500 * time.Millisecond}
	server.EnableHTTP2 = true
	server.Listener.Close()
	server.Listener = ln
	server.StartTLS()
	t.Cleanup(server.Close)
	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(io.Discard, resp.Body, 64<<10); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	readerDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, resp.Body)
		readerDone <- err
	}()
	ln.hole()
	if serveErr, returned := rb2Wait(done, 4*time.Second); !returned || serveErr == nil {
		t.Fatalf("blackholed HTTP/2 handler returned=%v err=%v proto=%s, want a write failure within the configured timeout", returned, serveErr, resp.Proto)
	}
	var sink chunkSink
	if err := e.s.Serve(e.ctx, "nod_de1", rb2Name, 0, sink.send); err != nil {
		t.Fatalf("the blackholed stream kept the node's update slot: %v", err)
	}
	resp.Body.Close()
	select {
	case <-readerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP/2 reader did not finish after the server closed the blackholed connection")
	}
}
