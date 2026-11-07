package agent

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// PATH SPEED (agent.proto "PATH SPEED"): a bounded throughput test between two fleet nodes. The target opens a TCP listener
// for one connection that starts with a one-time token; the sender moves mb MiB through it and measures. Nothing is stored.

// capSpeed is listed in Hello.capabilities: the panel sends SpeedTest only to agents that have it.
const capSpeed = "speed/1"

const (
	speedTokenSize  = 16
	speedMinMB      = 8
	speedMaxMB      = 32
	speedMaxSeconds = 30
	// speedTokenWait is how long an accepted connection may take to present the token.
	speedTokenWait = 5 * time.Second
	speedDialWait  = 5 * time.Second
	speedChunk     = 64 << 10
)

// speedState is the agent's bookkeeping for SpeedTest: one test at a time (a listener, or a run), and the armed listener.
type speedState struct {
	mu       sync.Mutex
	busy     bool
	token    []byte
	listener net.Listener
	// resolve is nil in production (tcpcheck.ResolveDestination).
	resolve func(ctx context.Context, host string) (netip.Addr, error)
}

func (st *speedState) acquire() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.busy {
		return false
	}
	st.busy = true
	return true
}

func (st *speedState) release() {
	st.mu.Lock()
	st.busy, st.token, st.listener = false, nil, nil
	st.mu.Unlock()
}

// speedCmd is one speed command with the meaning of the UdpSend fields it travels in (agent.proto "PATH SPEED").
type speedCmd struct {
	RequestId, Role, Direction, Host string
	Port, Mb, MaxS                   uint32
	Token                            []byte
}

func speedCommand(u *pb.UdpSend) *speedCmd {
	role := "" // anything but the three steps is refused as bad_params
	switch u.Size {
	case 1:
		role = "serve"
	case 2:
		role = "run"
	case 3:
		role = "stop"
	}
	c := &speedCmd{RequestId: u.RequestId, Role: role, Direction: "down", Host: u.Host, Mb: u.Count, MaxS: u.Pps, Token: u.Tag}
	if u.Df {
		c.Direction = "up"
	}
	if len(u.Ports) == 1 {
		c.Port = u.Ports[0]
	}
	return c
}

// speedTest handles one speed_test command.
func (a *Agent) speedTest(s *session, u *pb.UdpSend) {
	r := speedCommand(u)
	reply := func(res *pb.CommandResult) {
		res.RequestId = r.RequestId
		s.send(cmdResult(res))
	}
	if r.Role == "stop" {
		a.speed.stop(r.Token)
		reply(&pb.CommandResult{Ok: true})
		return
	}
	if (r.Role != "serve" && r.Role != "run") || (r.Direction != "down" && r.Direction != "up") ||
		len(r.Token) != speedTokenSize || r.Mb < speedMinMB || r.Mb > speedMaxMB || r.MaxS < 1 || r.MaxS > speedMaxSeconds ||
		(r.Role == "run" && (r.Host == "" || r.Port == 0 || r.Port > 65535)) {
		reply(&pb.CommandResult{Error: "bad_params"})
		return
	}
	if a.measuring.Load() || !a.speed.acquire() {
		reply(&pb.CommandResult{Error: "busy"})
		return
	}
	limit := time.Duration(r.MaxS) * time.Second
	if r.Role == "serve" {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			a.speed.release()
			reply(&pb.CommandResult{Error: "failed: " + shortUDPError(err)})
			return
		}
		a.speed.mu.Lock()
		a.speed.token, a.speed.listener = append([]byte(nil), r.Token...), ln
		a.speed.mu.Unlock()
		// the listener answers one connection, or closes by itself after the limit; the agent stays busy until then
		go func() {
			defer a.speed.release()
			if err := serveSpeed(ln, r.Token, r.Direction == "down", int64(r.Mb)<<20, limit); err != nil {
				a.log.Debug("speed test: serve ended", "err", err)
			}
		}()
		reply(&pb.CommandResult{Ok: true, Params: map[string]string{"port": strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)}})
		return
	}
	defer a.speed.release()
	ctx, cancel := context.WithTimeout(s.ctx, limit)
	defer cancel()
	resolve := a.speed.resolve
	if resolve == nil {
		resolve = tcpcheck.ResolveDestination
	}
	ip, err := resolve(ctx, r.Host)
	if err != nil {
		reply(&pb.CommandResult{Error: speedError(err)})
		return
	}
	res, err := runSpeed(ctx, net.JoinHostPort(ip.String(), strconv.Itoa(int(r.Port))), r.Token, r.Direction == "down", int64(r.Mb)<<20)
	if s.ctx.Err() != nil {
		return
	}
	if err != nil {
		reply(&pb.CommandResult{Error: speedError(err)})
		return
	}
	reply(&pb.CommandResult{Ok: true, Params: map[string]string{
		"mbit_s":        strconv.FormatFloat(res.mbit(), 'f', 1, 64),
		"connect_ms":    strconv.FormatInt(res.connect.Milliseconds(), 10),
		"first_byte_ms": strconv.FormatInt(res.firstByte.Milliseconds(), 10),
		"duration_ms":   strconv.FormatInt(res.duration.Milliseconds(), 10),
		"bytes":         strconv.FormatInt(res.bytes, 10),
	}})
}

// stop closes the armed listener when the token is its own.
func (st *speedState) stop(token []byte) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.listener != nil && subtle.ConstantTimeCompare(st.token, token) == 1 {
		st.listener.Close()
	}
}

func speedError(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, tcpcheck.ErrUnsupportedHost):
		return "unsupported_host"
	case errors.Is(err, tcpcheck.ErrNoRoute):
		return "no_route"
	case errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, errSpeedRefused):
		return "refused"
	}
	return "failed: " + shortUDPError(err)
}

var errSpeedRefused = errors.New("connection refused")

// speedBlock is the bytes that are sent: random, so that nothing on the path can compress them away.
var speedBlock = func() []byte {
	b := make([]byte, speedChunk)
	rand.Read(b)
	return b
}()

// serveSpeed accepts one connection that starts with token and moves total bytes: down sends them, up reads them and
// answers one byte. The listener is closed after the connection (or at the limit), so it never serves a second one.
func serveSpeed(ln net.Listener, token []byte, down bool, total int64, limit time.Duration) error {
	defer ln.Close()
	deadline := time.Now().Add(limit)
	if tl, ok := ln.(*net.TCPListener); ok {
		tl.SetDeadline(deadline)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		conn.SetDeadline(time.Now().Add(speedTokenWait))
		got := make([]byte, speedTokenSize)
		if _, err := io.ReadFull(conn, got); err != nil || subtle.ConstantTimeCompare(got, token) != 1 {
			conn.Close() // somebody else: the listener stays for the real sender until its limit
			continue
		}
		ln.Close()
		conn.SetDeadline(deadline)
		defer conn.Close()
		if down {
			return writeSpeed(conn, total)
		}
		if _, err := io.CopyN(io.Discard, conn, total); err != nil {
			return err
		}
		_, err = conn.Write([]byte{1})
		return err
	}
}

func writeSpeed(w io.Writer, total int64) error {
	for total > 0 {
		n := min(total, int64(len(speedBlock)))
		if _, err := w.Write(speedBlock[:n]); err != nil {
			return err
		}
		total -= n
	}
	return nil
}

type speedResult struct {
	connect, firstByte, duration time.Duration
	bytes                        int64
}

func (r speedResult) mbit() float64 {
	if r.duration <= 0 {
		return 0
	}
	return float64(r.bytes) * 8 / r.duration.Seconds() / 1e6
}

// runSpeed is the sender: connect, present the token and move total bytes. The time is from the token to the last byte (down)
// or to the target's one-byte answer (up), so what the target has not received yet is not counted as sent.
func runSpeed(ctx context.Context, addr string, token []byte, down bool, total int64) (speedResult, error) {
	var res speedResult
	d := net.Dialer{Timeout: speedDialWait}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return res, err
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return res, err
		}
		return res, fmt.Errorf("%w: %v", errSpeedRefused, err)
	}
	defer conn.Close()
	res.connect = time.Since(start)
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	begin := time.Now()
	if _, err := conn.Write(token); err != nil {
		return res, err
	}
	if down {
		buf := make([]byte, speedChunk)
		for res.bytes < total {
			n, err := conn.Read(buf[:min(int64(len(buf)), total-res.bytes)])
			if n > 0 {
				if res.bytes == 0 {
					res.firstByte = time.Since(begin)
				}
				res.bytes += int64(n)
			}
			if err != nil {
				if res.bytes >= total {
					break
				}
				return res, ctxErr(ctx, err)
			}
		}
		res.duration = time.Since(begin)
		return res, nil
	}
	if err := writeSpeed(conn, total); err != nil {
		return res, ctxErr(ctx, err)
	}
	var ack [1]byte
	if _, err := io.ReadFull(conn, ack[:]); err != nil {
		return res, ctxErr(ctx, err)
	}
	res.bytes, res.duration = total, time.Since(begin)
	return res, nil
}

// ctxErr says a timeout when the context ran out (the socket was closed under the transfer).
func ctxErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}
