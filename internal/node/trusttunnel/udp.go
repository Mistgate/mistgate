package trusttunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/ratelimit"
	wire "github.com/mistgate/mistgate/internal/trusttunnel"
)

// The memory a credential can hold in _udp2 tunnels is bounded for the credential as a whole, not per stream: its
// connections (at most maxCredentialConnections, one _udp2 stream each) share maxUDPFlows sockets with a udpReadBuffer
// each, and udpAccountReplyBytes of queued replies. That is 4 MiB + 4 MiB, however many connections and streams it opens.
const (
	// maxUDPFlows is how many NAT flows all the _udp2 streams of one credential keep together; the least recently active
	// one, on whichever stream, makes room for a new one.
	maxUDPFlows = 256
	// udpReadBuffer is the receive buffer of one flow. A datagram that fills it may have been cut, so it is dropped: real
	// traffic stays far below it (a path MTU is 1500, a QUIC datagram 1350), and the 64 KiB a datagram can have in theory
	// would cost 16 MiB per stream in buffers alone.
	udpReadBuffer = 16 << 10
	// The replies of one stream wait in a queue of at most udpReplyFrames frames and udpReplyBytes bytes for the client to
	// take them, and the queues of all the streams of a credential together hold at most udpAccountReplyBytes. A client
	// that does not read loses datagrams (UDP does that anyway) instead of holding the sockets' readers.
	udpReplyFrames       = 256
	udpReplyBytes        = 1 << 20
	udpAccountReplyBytes = 4 << 20
	dnsPort              = 53
)

// serveUDP serves one _udp2 stream: datagrams of any number of client sources, each with its own egress socket (a NAT
// keyed by the client's source address). A reply comes back as a frame from the address it came from to that source,
// which is what the official client looks its flow up by.
//
// A connection carries one _udp2 stream per credential, as the official client opens it: a newer one replaces the older (a
// client that reopens its stream while the old one is still being torn down, or whose old FIN arrives late over a lossy
// path, must not be refused), and the older one is reset.
func (in *inbound) serveUDP(w http.ResponseWriter, r *http.Request, stream *streamState) {
	if previous := stream.conn.claimUDP(stream); previous != nil {
		previous.abort()
	}
	w.WriteHeader(http.StatusOK)
	if err := stream.startHTTP3(); err != nil {
		return
	}
	if stream.flush() != nil {
		return
	}

	tunnel := newUDPTunnel(in, stream)
	stream.setCleanup(tunnel.closeAll)
	writerDone := make(chan struct{})
	go tunnel.writeLoop(writerDone)
	defer func() {
		// Flows first, then their readers, then the writer: nothing may use the ResponseWriter after this returns.
		tunnel.closeAll()
		tunnel.readers.Wait()
		close(tunnel.out)
		select {
		case <-writerDone:
		case <-time.After(in.e.timing.udpDrain):
			stream.abort() // the client does not read what is left: reset the stream, which frees the stuck write
			<-writerDone
		}
	}()

	decoder := wire.NewUDPDecoder(wire.UDPClientToServer)
	buffer := tunnelBuffers.Get().([]byte)
	defer tunnelBuffers.Put(buffer)
	for {
		n, readErr := r.Body.Read(buffer)
		if n > 0 {
			// A frame the decoder drops (too short or too long) is skipped in step with the stream; the rest still flows.
			frames, _ := decoder.Feed(buffer[:n])
			for _, frame := range frames {
				if stream.cred.expired(in.e.now()) || !ratelimit.WaitUp(
					stream.ctx, stream.cred.limit.Load(), stream.cred.person.Load(), uint64(len(frame.Payload)),
				) {
					return
				}
				flow, err := tunnel.flowFor(frame.Source, frame.Target)
				if err != nil {
					continue // a forbidden or unreachable destination loses its datagram, not the stream
				}
				flow.sending(frame.Target)
				written, err := flow.conn.WriteTo(frame.Payload, frame.Target.String())
				if err != nil {
					flow.notSent(frame.Target)
					continue
				}
				stream.cred.up.Add(uint64(written))
			}
		}
		if readErr != nil {
			return
		}
	}
}

type udpOut struct {
	frame   []byte
	payload int
}

type udpTunnel struct {
	in     *inbound
	stream *streamState
	ctx    context.Context // ends with the tunnel: frees readers waiting on a rate limit
	cancel context.CancelFunc
	idle   time.Duration

	mu     sync.Mutex
	closed bool
	next   uint64
	flows  map[netip.AddrPort]*udpFlow

	readers sync.WaitGroup
	out     chan udpOut
	queued  atomic.Int64
}

func newUDPTunnel(in *inbound, stream *streamState) *udpTunnel {
	ctx, cancel := context.WithCancel(stream.ctx)
	return &udpTunnel{
		in: in, stream: stream, ctx: ctx, cancel: cancel, idle: in.e.timing.udpFlowIdle,
		flows: make(map[netip.AddrPort]*udpFlow), out: make(chan udpOut, udpReplyFrames),
	}
}

type udpFlow struct {
	tunnel *udpTunnel
	source netip.AddrPort
	order  uint64 // among the flows of its stream
	seq    uint64 // among the flows of its credential; guarded by credState.udpMu
	conn   engine.EgressUDP

	closed  atomic.Bool
	last    atomic.Int64 // unix nanoseconds of the last datagram either way
	dnsOnly atomic.Bool  // only port 53 so far: the socket is done when every query has its answer
	pending atomic.Int32 // queries to port 53 still waiting for an answer
	mu      sync.Mutex
	timer   *time.Timer
}

// flowFor returns the flow of a client source, opening an egress socket for the target on the first datagram. Only the
// stream's reading goroutine opens flows; expiry and eviction close them from elsewhere.
func (tunnel *udpTunnel) flowFor(source, target netip.AddrPort) (*udpFlow, error) {
	tunnel.mu.Lock()
	if tunnel.closed {
		tunnel.mu.Unlock()
		return nil, net.ErrClosed
	}
	if current := tunnel.flows[source]; current != nil {
		if !current.closed.Load() {
			tunnel.mu.Unlock()
			return current, nil
		}
		delete(tunnel.flows, source)
	}
	tunnel.mu.Unlock()

	conn, err := tunnel.in.out.UDP(target.String())
	if err != nil {
		return nil, err
	}
	flow := &udpFlow{tunnel: tunnel, source: source, conn: conn}
	flow.dnsOnly.Store(target.Port() == dnsPort)
	flow.last.Store(time.Now().UnixNano())
	tunnel.mu.Lock()
	if tunnel.closed {
		tunnel.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	tunnel.next++
	flow.order = tunnel.next
	flow.timer = time.AfterFunc(tunnel.idle, flow.expire)
	tunnel.flows[source] = flow
	tunnel.readers.Add(1)
	tunnel.mu.Unlock()
	// The credential's other flows make room, on this stream or another one. A flow is counted from its birth, so the
	// number of sockets is at most maxUDPFlows plus the flows being opened at this very moment.
	for _, victim := range tunnel.stream.cred.trackUDPFlow(flow) {
		victim.tunnel.remove(victim)
		victim.close()
	}
	go flow.readLoop()
	return flow, nil
}

// sending notes a datagram that is about to go out: it keeps the flow alive and counts the DNS queries that await an answer.
// It runs before the write, because the answer may be back before the write returns.
func (flow *udpFlow) sending(target netip.AddrPort) {
	flow.last.Store(time.Now().UnixNano())
	if target.Port() == dnsPort {
		flow.pending.Add(1)
	} else {
		flow.dnsOnly.Store(false)
	}
}

// notSent takes back the count of a DNS query whose write failed: no answer will come.
func (flow *udpFlow) notSent(target netip.AddrPort) {
	if target.Port() == dnsPort {
		flow.pending.Add(-1)
	}
}

// expire closes the flow once it has been silent for the idle time, in either direction.
func (flow *udpFlow) expire() {
	if flow.closed.Load() {
		return
	}
	silent := time.Since(time.Unix(0, flow.last.Load()))
	if remaining := flow.tunnel.idle - silent; remaining > 0 {
		flow.mu.Lock()
		if flow.timer != nil && !flow.closed.Load() {
			flow.timer.Reset(remaining)
		}
		flow.mu.Unlock()
		return
	}
	flow.tunnel.remove(flow)
	flow.close()
}

// close frees the socket (once); its reader then ends.
func (flow *udpFlow) close() {
	if !flow.closed.CompareAndSwap(false, true) {
		return
	}
	flow.mu.Lock()
	if flow.timer != nil {
		flow.timer.Stop()
		flow.timer = nil
	}
	flow.mu.Unlock()
	flow.tunnel.stream.cred.forgetUDPFlow(flow)
	_ = flow.conn.Close()
}

func (tunnel *udpTunnel) remove(flow *udpFlow) {
	tunnel.mu.Lock()
	if tunnel.flows[flow.source] == flow {
		delete(tunnel.flows, flow.source)
	}
	tunnel.mu.Unlock()
}

// closeAll closes every flow and refuses new ones; it is safe to call from any goroutine, any number of times.
func (tunnel *udpTunnel) closeAll() {
	tunnel.mu.Lock()
	if tunnel.closed {
		tunnel.mu.Unlock()
		return
	}
	tunnel.closed = true
	flows := make([]*udpFlow, 0, len(tunnel.flows))
	for _, flow := range tunnel.flows {
		flows = append(flows, flow)
	}
	tunnel.flows = nil
	tunnel.mu.Unlock()
	tunnel.cancel()
	for _, flow := range flows {
		flow.close()
	}
}

func (flow *udpFlow) readLoop() {
	defer flow.tunnel.readers.Done()
	tunnel, stream := flow.tunnel, flow.tunnel.stream
	buffer := make([]byte, udpReadBuffer)
	for {
		n, remote, err := flow.conn.ReadFrom(buffer)
		if n > 0 && n < len(buffer) {
			if stream.cred.expired(tunnel.in.e.now()) {
				stream.abort()
				return
			}
			flow.last.Store(time.Now().UnixNano())
			source, parseErr := netip.ParseAddrPort(remote)
			if parseErr == nil && ratelimit.WaitDown(
				tunnel.ctx, stream.cred.limit.Load(), stream.cred.person.Load(), uint64(n),
			) {
				// The payload is copied into the frame, so the buffer is free for the next read at once.
				if frame, encodeErr := wire.EncodeUDPServerFrame(wire.UDPFrame{
					Source: source, Target: flow.source, Payload: buffer[:n],
				}); encodeErr == nil {
					tunnel.enqueue(frame, n)
				}
				flow.answered(source)
			}
		}
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) && tunnel.ctx.Err() == nil {
				tunnel.remove(flow)
				flow.close()
			}
			return
		}
	}
}

// answered closes a flow that only talked to port 53 once every query has its answer, as the official server does: a
// resolver uses a new source port per query, and a socket for each would crowd out the flows that matter.
func (flow *udpFlow) answered(remote netip.AddrPort) {
	if remote.Port() != dnsPort || !flow.dnsOnly.Load() {
		return
	}
	if flow.pending.Add(-1) <= 0 {
		flow.pending.Store(0)
		flow.tunnel.remove(flow)
		flow.close()
	}
}

// enqueue puts a reply in the stream's queue without waiting: when the client is not taking replies fast enough, the
// datagram is dropped.
func (tunnel *udpTunnel) enqueue(frame []byte, payload int) {
	size := int64(len(frame))
	account := &tunnel.stream.cred.udpQueued
	if tunnel.queued.Add(size) > udpReplyBytes || account.Add(size) > udpAccountReplyBytes {
		tunnel.queued.Add(-size)
		account.Add(-size)
		return
	}
	select {
	case tunnel.out <- udpOut{frame: frame, payload: payload}:
	default:
		tunnel.queued.Add(-size)
		account.Add(-size)
	}
}

// writeLoop is the only writer of the response. After a failed write it keeps draining the queue, so the readers never
// find it full, until the tunnel closes the queue.
func (tunnel *udpTunnel) writeLoop(done chan<- struct{}) {
	defer close(done)
	stream := tunnel.stream
	failed := false
	for item := range tunnel.out {
		tunnel.queued.Add(-int64(len(item.frame)))
		stream.cred.udpQueued.Add(-int64(len(item.frame)))
		if failed {
			continue
		}
		n, err := stream.out.Write(item.frame)
		if err == nil && n != len(item.frame) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = stream.flush()
		}
		if err != nil {
			failed = true
			stream.abort()
			continue
		}
		stream.cred.down.Add(uint64(item.payload))
	}
}
