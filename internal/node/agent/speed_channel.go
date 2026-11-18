package agent

import (
	"context"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// CHANNEL MEASURE (agent.proto "CHANNEL MEASURE"): the same speed_test command, in a mode that moves data over 8 parallel TCP
// streams for a fixed time and not a fixed volume, so a link that one stream cannot fill shows its real width. It is the
// heavy mode, so every limit below holds on this node whatever the panel asks, and on both ends independently:
//   - a stream gets in only with the one-time token and its own index (0-7); a token with an index already used, a ninth
//     stream, or anything else is closed at once, and the listener closes after the eighth stream or at its limit;
//   - the listener lives at most channelServeMaxS seconds and the transfer at most channelHardS from the first stream, the
//     volume of all streams together is capped at channelMaxMB (the panel asks for less or equal, never more is served);
//   - one test per agent at a time (the same gate as speed/1 and the bandwidth test) and at most channelBudget steps in
//     channelBudgetFor, so a panel that loops cannot keep a node busy;
//   - the end of the panel's session (the link is lost) and a "stop" with the token close every stream.

// capSpeedChannel is listed in Hello.capabilities: the panel sends the channel steps (size 4 and 5) only to agents that have it.
const capSpeedChannel = "speed-channel/1"

const (
	// the size field of the UdpSend that carries the step (1 serve, 2 run, 3 stop belong to speed/1)
	speedStepServeChannel = 4
	speedStepRunChannel   = 5

	channelStreams     = 8
	channelMinMB       = 64
	channelMaxMB       = 2048
	channelWindow      = 10 * time.Second // how long the sender moves data
	channelHardS       = 15               // the longest a transfer lives, from the first stream (serve) or the start (run)
	channelServeMaxS   = 20               // the longest the listener waits for its streams
	channelRunMinS     = 12               // the window and the drain of the last bytes have to fit
	channelDialWait    = 3 * time.Second
	channelPending     = 16 // streams that have connected but not yet shown the token
	channelBudget      = 12 // steps (serve or run) in channelBudgetFor
	channelBudgetFor   = 10 * time.Minute
	channelHelloLength = speedTokenSize + 1 // token, stream index
)

// channelLimits is what a test may change: production uses the constants.
type channelLimits struct {
	window time.Duration // sender: how long the streams move data
	hard   time.Duration // both: the longest a transfer lives
}

func (st *speedState) limits() channelLimits {
	if st.testLimits != nil {
		return *st.testLimits
	}
	return channelLimits{window: channelWindow, hard: channelHardS * time.Second}
}

// spend takes one step of the budget; false = the node has done enough channel steps for now.
func (st *speedState) spend(now time.Time) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	kept := st.steps[:0]
	for _, at := range st.steps {
		if now.Sub(at) < channelBudgetFor {
			kept = append(kept, at)
		}
	}
	st.steps = kept
	if len(st.steps) >= channelBudget {
		return false
	}
	st.steps = append(st.steps, now)
	return true
}

// speedChannel handles a channel serve or run (the stop is the same as speed/1's).
func (a *Agent) speedChannel(s *session, r *speedCmd, reply func(*pb.CommandResult)) {
	serve := r.Role == "serve-channel"
	if len(r.Token) != speedTokenSize || r.Mb < channelMinMB || r.Mb > channelMaxMB ||
		(serve && (r.MaxS < 1 || r.MaxS > channelServeMaxS)) ||
		(!serve && (r.MaxS < channelRunMinS || r.MaxS > channelHardS || r.Host == "" || r.Port == 0 || r.Port > 65535)) {
		reply(&pb.CommandResult{Error: "bad_params"})
		return
	}
	if a.measuring.Load() || !a.speed.acquire() {
		reply(&pb.CommandResult{Error: "busy"})
		return
	}
	if !a.speed.spend(time.Now()) {
		a.speed.release()
		reply(&pb.CommandResult{Error: "too_soon"})
		return
	}
	lim := a.speed.limits()
	capBytes := int64(r.Mb) << 20
	down := r.Direction == "down"
	// the session's end (the panel link is lost) and a stop with the token cancel the whole test
	ctx, cancel := context.WithCancel(s.ctx)
	a.speed.mu.Lock()
	a.speed.token = append([]byte(nil), r.Token...)
	a.speed.abort = cancel
	a.speed.mu.Unlock()

	if serve {
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			cancel()
			a.speed.release()
			reply(&pb.CommandResult{Error: "failed: " + shortUDPError(err)})
			return
		}
		a.speed.mu.Lock()
		a.speed.listener = ln
		a.speed.mu.Unlock()
		go func() {
			defer a.speed.release()
			defer cancel()
			if _, err := serveChannel(ctx, ln, r.Token, down, capBytes, time.Duration(r.MaxS)*time.Second, lim.hard); err != nil {
				a.log.Debug("channel measure: serve ended", "code", speedServeLogCode(err))
			}
		}()
		reply(&pb.CommandResult{Ok: true, Params: map[string]string{"port": strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)}})
		return
	}

	defer a.speed.release()
	defer cancel()
	runCtx, stop := context.WithTimeout(ctx, time.Duration(r.MaxS)*time.Second)
	defer stop()
	resolve := a.speed.resolve
	if resolve == nil {
		resolve = tcpcheck.ResolveDestination
	}
	ip, err := resolve(runCtx, r.Host)
	if err != nil {
		reply(&pb.CommandResult{Error: speedError(err)})
		return
	}
	res, err := runChannel(runCtx, net.JoinHostPort(ip.String(), strconv.Itoa(int(r.Port))), r.Token, down, capBytes, lim)
	if s.ctx.Err() != nil {
		return
	}
	if err != nil {
		reply(&pb.CommandResult{Error: speedError(err)})
		return
	}
	capped := "0"
	if res.capped {
		capped = "1"
	}
	reply(&pb.CommandResult{Ok: true, Params: map[string]string{
		"mbit_s":        strconv.FormatFloat(res.mbit(), 'f', 1, 64),
		"connect_ms":    strconv.FormatInt(res.connect.Milliseconds(), 10),
		"first_byte_ms": strconv.FormatInt(res.firstByte.Milliseconds(), 10),
		"duration_ms":   strconv.FormatInt(res.duration.Milliseconds(), 10),
		"bytes":         strconv.FormatInt(res.bytes, 10),
		"streams":       strconv.Itoa(res.streams),
		"capped":        capped,
	}})
}

// byteBudget is the volume all the streams of one transfer share: take hands out at most what is left, never more.
type byteBudget struct{ left atomic.Int64 }

func newByteBudget(total int64) *byteBudget {
	b := &byteBudget{}
	b.left.Store(total)
	return b
}

func (b *byteBudget) take(n int64) int64 {
	for {
		cur := b.left.Load()
		if cur <= 0 {
			return 0
		}
		m := min(n, cur)
		if b.left.CompareAndSwap(cur, cur-m) {
			return m
		}
	}
}

func (b *byteBudget) giveBack(n int64) { b.left.Add(n) }

// connSet closes every connection it holds when the transfer is over, and any added after that.
type connSet struct {
	mu     sync.Mutex
	conns  []net.Conn
	closed bool
}

func (c *connSet) add(conn net.Conn) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		conn.Close()
		return false
	}
	c.conns = append(c.conns, conn)
	return true
}

// remove forgets a connection that is over, so a flood of strangers does not grow the set.
func (c *connSet) remove(conn net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, x := range c.conns {
		if x == conn {
			c.conns = append(c.conns[:i], c.conns[i+1:]...)
			return
		}
	}
}

func (c *connSet) closeAll() {
	c.mu.Lock()
	c.closed = true
	conns := c.conns
	c.conns = nil
	c.mu.Unlock()
	for _, conn := range conns {
		conn.Close()
	}
}

// serveChannel is the target: it accepts up to 8 streams, each with the token and its own index, and moves at most total bytes
// over all of them, for at most hard after the first stream. down: it sends; up: it reads until the sender closes its side and
// answers each stream with the 8 bytes it received. The listener closes after the eighth stream, at listenFor, or when ctx ends.
func serveChannel(ctx context.Context, ln net.Listener, token []byte, down bool, total int64, listenFor, hard time.Duration) (moved int64, err error) {
	defer ln.Close()
	if tl, ok := ln.(*net.TCPListener); ok {
		tl.SetDeadline(time.Now().Add(listenFor))
	}
	var (
		set     connSet
		wg      sync.WaitGroup
		budget  = newByteBudget(total)
		pending = make(chan struct{}, channelPending)
		mu      sync.Mutex
		used    [channelStreams]bool
		joined  int
		endsAt  time.Time // set by the first stream: the transfer's own limit
	)
	defer func() { moved = total - budget.left.Load() }() // runs last: after the streams are over
	stop := context.AfterFunc(ctx, func() { ln.Close(); set.closeAll() })
	defer stop()
	defer set.closeAll() // runs after the transfers are over (wg.Wait below runs first)
	defer wg.Wait()

	// admit says whether the stream shows the token and an index nobody used; the first one starts the clock of the transfer
	admit := func(conn net.Conn) (time.Time, bool) {
		conn.SetDeadline(time.Now().Add(speedTokenWait))
		var hello [channelHelloLength]byte
		if _, err := io.ReadFull(conn, hello[:]); err != nil || subtle.ConstantTimeCompare(hello[:speedTokenSize], token) != 1 {
			return time.Time{}, false
		}
		idx := int(hello[speedTokenSize])
		mu.Lock()
		defer mu.Unlock()
		if idx >= channelStreams || used[idx] || ctx.Err() != nil {
			return time.Time{}, false
		}
		used[idx] = true
		joined++
		if endsAt.IsZero() {
			endsAt = time.Now().Add(hard)
		}
		if joined == channelStreams {
			ln.Close() // one token, eight streams: nothing else gets in
		}
		return endsAt, true
	}

	for {
		conn, aerr := ln.Accept()
		if aerr != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			mu.Lock()
			streamed := joined > 0
			mu.Unlock()
			if streamed || errors.Is(aerr, net.ErrClosed) {
				return 0, nil
			}
			return 0, aerr
		}
		select {
		case pending <- struct{}{}:
		default: // too many strangers waiting for their turn to show the token
			conn.Close()
			continue
		}
		if !set.add(conn) {
			<-pending
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conn.Close()
			defer set.remove(conn)
			endsAtConn, ok := admit(conn)
			<-pending
			if !ok {
				return
			}
			conn.SetDeadline(endsAtConn)
			if down {
				writeBudget(conn, budget)
				return
			}
			got := readBudget(conn, budget)
			var ack [8]byte
			binary.BigEndian.PutUint64(ack[:], uint64(got))
			conn.Write(ack[:])
		}()
	}
}

// writeBudget sends random bytes until the shared budget is spent, the deadline passes or the other side closes.
func writeBudget(w io.Writer, b *byteBudget) int64 {
	var sent int64
	for {
		n := b.take(int64(len(speedBlock)))
		if n == 0 {
			return sent
		}
		m, err := w.Write(speedBlock[:n])
		sent += int64(m)
		if err != nil {
			return sent
		}
	}
}

// readBudget reads until EOF, an error, the deadline or the end of the shared budget (the sender then gets no answer).
func readBudget(r io.Reader, b *byteBudget) int64 {
	buf := make([]byte, speedChunk)
	var got int64
	for {
		n := b.take(int64(len(buf)))
		if n == 0 {
			return got
		}
		m, err := r.Read(buf[:n])
		got += int64(m)
		if int64(m) < n {
			b.giveBack(n - int64(m))
		}
		if err != nil {
			return got
		}
	}
}

type channelResult struct {
	connect, firstByte, duration time.Duration
	bytes                        int64
	streams                      int
	capped                       bool
}

func (r channelResult) mbit() float64 {
	if r.duration <= 0 {
		return 0
	}
	return float64(r.bytes) * 8 / r.duration.Seconds() / 1e6
}

// runChannel is the sender: it opens up to 8 streams, presents the token and an index on each, and moves data for lim.window
// (or until total bytes), all streams together. down: it counts what it reads; up: it writes, closes its side and counts what the
// target says it received, so bytes still in flight are not counted as sent. Everything ends with ctx.
func runChannel(ctx context.Context, addr string, token []byte, down bool, total int64, lim channelLimits) (channelResult, error) {
	var res channelResult
	type dialed struct {
		conn net.Conn
		took time.Duration
		err  error
	}
	out := make([]dialed, channelStreams)
	var wg sync.WaitGroup
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := net.Dialer{Timeout: channelDialWait}
			start := time.Now()
			conn, err := d.DialContext(ctx, "tcp", addr)
			out[i] = dialed{conn: conn, took: time.Since(start), err: err}
		}()
	}
	wg.Wait()
	// a stream keeps the index of the slot it was dialed in: the target wants each of 0-7 once
	var conns []net.Conn
	var slots []int
	var firstErr error
	for slot, d := range out {
		if d.err != nil {
			if firstErr == nil {
				firstErr = d.err
			}
			continue
		}
		conns, slots = append(conns, d.conn), append(slots, slot)
		res.connect = max(res.connect, d.took)
	}
	var set connSet
	defer set.closeAll()
	for _, c := range conns {
		set.add(c)
	}
	if len(conns) == 0 {
		var ne net.Error
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if errors.As(firstErr, &ne) && ne.Timeout() {
			return res, firstErr
		}
		return res, errors.Join(errSpeedRefused, firstErr)
	}
	stopCtx := context.AfterFunc(ctx, set.closeAll)
	defer stopCtx()

	budget := newByteBudget(total)
	begin := time.Now()
	end := begin.Add(lim.window)
	hardEnd := begin.Add(lim.hard)
	var moved atomic.Int64
	var first atomic.Int64 // nanoseconds from begin to the first byte, 0 = none yet
	acked := make([]int64, len(conns))
	ackOK := make([]bool, len(conns))
	var done sync.WaitGroup
	for i, conn := range conns {
		done.Add(1)
		go func() {
			defer done.Done()
			hello := make([]byte, 0, channelHelloLength)
			hello = append(append(hello, token...), byte(slots[i]))
			conn.SetDeadline(hardEnd)
			if _, err := conn.Write(hello); err != nil {
				return
			}
			if down {
				buf := make([]byte, speedChunk)
				conn.SetReadDeadline(end)
				for {
					n := budget.take(int64(len(buf)))
					if n == 0 {
						return
					}
					m, err := conn.Read(buf[:n])
					if m > 0 {
						first.CompareAndSwap(0, int64(max(time.Since(begin), 1)))
						moved.Add(int64(m))
					}
					if int64(m) < n {
						budget.giveBack(n - int64(m))
					}
					if err != nil {
						return
					}
				}
			}
			conn.SetWriteDeadline(end)
			for {
				n := budget.take(int64(len(speedBlock)))
				if n == 0 {
					break
				}
				m, err := conn.Write(speedBlock[:n])
				if int64(m) < n {
					budget.giveBack(n - int64(m))
				}
				if err != nil {
					break
				}
			}
			if tc, ok := conn.(*net.TCPConn); ok {
				tc.CloseWrite()
			}
			conn.SetReadDeadline(hardEnd)
			var ack [8]byte
			if _, err := io.ReadFull(conn, ack[:]); err != nil {
				return
			}
			acked[i], ackOK[i] = int64(binary.BigEndian.Uint64(ack[:])), true
		}()
	}
	done.Wait()
	elapsed := time.Since(begin)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	res.streams = len(conns)
	res.capped = budget.left.Load() <= 0
	if down {
		res.bytes = moved.Load()
		res.duration = min(elapsed, lim.window)
		res.firstByte = time.Duration(first.Load())
	} else {
		for i := range acked {
			if ackOK[i] {
				res.bytes += acked[i]
			}
		}
		res.duration = elapsed
	}
	if res.bytes <= 0 {
		return res, errors.New("no data moved")
	}
	return res, nil
}
