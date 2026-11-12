package vless

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mistgate/mistgate/internal/node/nets"
	xnet "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/transport/internet/finalmask"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

const tcpConnMaskMessageName = "mistgate.node.vless.ConnectionTracker"

var (
	tcpConnMaskDescriptor protoreflect.MessageDescriptor
	tcpConnMaskType       protoreflect.MessageType
	tcpConnMaskFields     struct {
		inboundID  protoreflect.FieldDescriptor
		generation protoreflect.FieldDescriptor
	}
	tcpConnTrackersMu sync.Mutex
	tcpConnTrackers   = make(map[tcpConnTrackerKey]*tcpConnTracker)
)

type tcpConnTrackerKey struct {
	inboundID  string
	generation uint64
}

type tcpConnTracker struct {
	key tcpConnTrackerKey

	// links/3 (links.go): the inbound's counters; reality says Xray runs REALITY on these connections (their closes before a
	// key are kept for its verdict, and their first bytes are the client's ClientHello); stalls says a connection's key
	// gets the TCP stall it ended with (REALITY over RAW: the connection is the client's TCP connection and one session).
	counters *inboundCounters
	nets     *nets.Aggregator
	reality  bool
	stalls   bool
	// byAddr finds the connection under an XHTTP request, which carries the TCP peer's address and not the connection (the
	// dispatch of an XHTTP session; kexOfAddr). It lives in this process's memory only, like recentCloses. nil when the inbound
	// is not REALITY over XHTTP.
	byAddr map[netip.AddrPort]*trackedTCPConn

	mu     sync.Mutex
	conns  map[*trackedTCPConn]struct{}
	closed bool
}

func newTCPConnTracker(inboundID string, generation uint64) *tcpConnTracker {
	return &tcpConnTracker{key: tcpConnTrackerKey{inboundID: inboundID, generation: generation}, conns: make(map[*trackedTCPConn]struct{})}
}

func (t *tcpConnTracker) register() {
	tcpConnTrackersMu.Lock()
	tcpConnTrackers[t.key] = t
	tcpConnTrackersMu.Unlock()
}

func (t *tcpConnTracker) stop() {
	t.mu.Lock()
	t.closed = true
	conns := make([]*trackedTCPConn, 0, len(t.conns))
	for conn := range t.conns {
		conns = append(conns, conn)
	}
	t.mu.Unlock()

	tcpConnTrackersMu.Lock()
	if tcpConnTrackers[t.key] == t {
		delete(tcpConnTrackers, t.key)
	}
	tcpConnTrackersMu.Unlock()

	for _, conn := range conns {
		_ = conn.Close()
	}
}

func (t *tcpConnTracker) track(conn net.Conn) net.Conn {
	tracked := &trackedTCPConn{Conn: conn, tracker: t}
	if t.stalls && t.nets != nil && t.nets.Enabled() {
		tracked.asn = nets.LookupAddr(t.nets, conn.RemoteAddr())
		t.nets.Add(t.key.inboundID, tracked.asn, nets.Attempt, "", "")
	}
	if !t.reality {
		tracked.peeked.Store(true) // only a REALITY connection starts with the client's ClientHello
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = conn.Close()
		return tracked
	}
	t.conns[tracked] = struct{}{}
	if t.byAddr != nil {
		if ap, ok := addrPortOf(conn.RemoteAddr()); ok {
			tracked.addr = ap
			t.byAddr[ap] = tracked
		}
	}
	t.mu.Unlock()
	return tracked
}

func (t *tcpConnTracker) remove(conn *trackedTCPConn) {
	t.mu.Lock()
	delete(t.conns, conn)
	if t.byAddr != nil && t.byAddr[conn.addr] == conn {
		delete(t.byAddr, conn.addr)
	}
	t.mu.Unlock()
}

// kexOfAddr is the hello classification of the live connection from addr, and claims it for one key: the first session of an
// XHTTP connection counts (an h2 connection carries many sessions, a TCP connection has one ClientHello). found is false
// when no live connection has that address (a forwarded address, or the connection is gone).
func (t *tcpConnTracker) kexOfAddr(addr netip.AddrPort) (kex helloKex, first, found bool) {
	t.mu.Lock()
	c := t.byAddr[addr]
	t.mu.Unlock()
	if c == nil {
		return kexUnknown, false, false
	}
	return c.kexKind(), c.kexClaimed.CompareAndSwap(false, true), true
}

// addrPortOf reads a TCP peer address without allocating.
func addrPortOf(a net.Addr) (netip.AddrPort, bool) {
	if tcp, ok := a.(*net.TCPAddr); ok {
		ap := tcp.AddrPort()
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()), ap.IsValid()
	}
	return netip.AddrPort{}, false
}

type trackedTCPConn struct {
	net.Conn
	tracker *tcpConnTracker
	asn     uint32
	once    sync.Once
	addr    netip.AddrPort // the key in tracker.byAddr, when it has one

	// links/3. The start of the connection (hello), read by one reader at a time (REALITY, then the VLESS handler) until
	// peeked; session is the key of the first request on the connection (dispatch). kex is the ClientHello classification
	// (hellokex.go), final once peeked; kexClaimed says a key has counted it.
	hello      *helloBuf
	peeked     atomic.Bool
	kex        atomic.Uint32
	kexClaimed atomic.Bool
	session    atomic.Pointer[credState]
}

// kexKind is the classification of the connection's ClientHello: unknown for a connection that is not (or not yet) read.
func (c *trackedTCPConn) kexKind() helloKex {
	if k := helloKex(c.kex.Load()); k != kexPending {
		return k
	}
	return kexUnknown
}

// Read passes through; until the connection's first TLS record is in it notes whether the connection opened with a ClientHello
// and which key exchange it offers. Vision reads the raw socket past the handshake (RawConn), so this costs one atomic load per
// read of the handshake at most.
func (c *trackedTCPConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && !c.peeked.Load() {
		c.peek(b[:n])
	}
	return n, err
}

// peek copies the bytes REALITY has just read (never more, never earlier) until the first record is whole or hellokexCap is
// full, counts the hello at its sixth byte, and classifies it.
func (c *trackedTCPConn) peek(b []byte) {
	if c.hello == nil {
		if b[0] != 0x16 {
			c.finishPeek(kexUnknown) // not TLS
			return
		}
		h := helloBufs.Get().(*helloBuf)
		h.n, h.counted = 0, false
		c.hello = h
	}
	h := c.hello
	h.n += copy(h.b[h.n:], b)
	if h.n < 6 {
		return
	}
	if !h.counted {
		h.counted = true
		if !isClientHello([6]byte(h.b[:6])) {
			c.finishPeek(kexUnknown) // TLS, but no ClientHello
			return
		}
		if c.tracker.reality && c.tracker.counters != nil {
			c.tracker.counters.hellos.Add(1)
		}
	}
	want := min(tlsRecordHeaderLen+int(binary.BigEndian.Uint16(h.b[3:5])), len(h.b))
	if h.n < want {
		return
	}
	c.finishPeek(classifyHello(h.b[:want]))
}

func (c *trackedTCPConn) finishPeek(k helloKex) {
	if h := c.hello; h != nil {
		c.hello = nil
		helloBufs.Put(h)
	}
	c.kex.Store(uint32(k))
	c.peeked.Store(true)
}

// countKex adds the connection's hello classification to the key that has just claimed it.
func (cs *credState) countKex(k helloKex) {
	switch k {
	case kexPQ:
		cs.helloPQ.Add(1)
	case kexClassic:
		cs.helloClassic.Add(1)
	default:
		cs.helloUnknown.Add(1)
	}
}

// markSession records the key of the connection's first request; it reports whether this was the first.
func (c *trackedTCPConn) markSession(cs *credState) bool {
	if !c.session.CompareAndSwap(nil, cs) {
		return false
	}
	if c.tracker.reality && c.kexClaimed.CompareAndSwap(false, true) {
		cs.countKex(c.kexKind())
	}
	if c.tracker.stalls && c.tracker.nets != nil {
		userID := ""
		if owner := cs.owner.Load(); owner != nil {
			userID = owner.userID
		}
		c.tracker.nets.Add(c.tracker.key.inboundID, c.asn, nets.Connect, cs.id, userID)
	}
	return true
}

func (c *trackedTCPConn) Close() error {
	var err error
	c.once.Do(func() {
		c.tracker.remove(c)
		if cs := c.session.Load(); cs != nil {
			if c.tracker.stalls {
				stalled, first16K := tcpStall(c.Conn)
				cs.stall(stalled, first16K)
				if stalled && first16K && c.tracker.nets != nil {
					userID := ""
					if owner := cs.owner.Load(); owner != nil {
						userID = owner.userID
					}
					c.tracker.nets.Add(c.tracker.key.inboundID, c.asn, nets.Broken, cs.id, userID)
				}
			}
		} else if addr := c.Conn.RemoteAddr(); addr != nil && c.tracker.reality && c.tracker.counters != nil {
			// no key reached the engine: REALITY may be about to log why (links.go, realityVerdict)
			agg := c.tracker.nets
			if !c.tracker.stalls {
				agg = nil
			}
			rememberNetworkClose(addr.String(), c.tracker.counters, c.asn, c.tracker.key.inboundID, agg, time.Now())
		}
		err = c.Conn.Close()
	})
	return err
}

// TcpMaskConn, RawConn and Splice let xray unwrap the tracked connection (finalmask.UnwrapTcpMask), so Vision's copy
// keeps readv/writev on the raw socket. Close still goes through the wrapper, so tracking is unchanged.
func (*trackedTCPConn) TcpMaskConn()        {}
func (c *trackedTCPConn) RawConn() net.Conn { return c.Conn }
func (*trackedTCPConn) Splice() bool        { return true }

func (c *trackedTCPConn) CloseWrite() error {
	closer, ok := c.Conn.(interface{ CloseWrite() error })
	if !ok {
		return errors.New("close-write is unsupported")
	}
	return closer.CloseWrite()
}

type tcpConnMaskConfig struct {
	message *dynamicpb.Message
}

func newTCPConnMaskConfig(inboundID string, generation uint64) *tcpConnMaskConfig {
	message := dynamicpb.NewMessage(tcpConnMaskDescriptor)
	message.Set(tcpConnMaskFields.inboundID, protoreflect.ValueOfString(inboundID))
	message.Set(tcpConnMaskFields.generation, protoreflect.ValueOfUint64(generation))
	return &tcpConnMaskConfig{message: message}
}

func (c *tcpConnMaskConfig) ProtoReflect() protoreflect.Message {
	if c == nil || c.message == nil {
		return nil
	}
	return &tcpConnMaskReflection{Message: c.message, owner: c}
}

func (c *tcpConnMaskConfig) WrapConnClient(conn net.Conn, _ *xnet.Destination, _ *finalmask.Dialer) (net.Conn, error) {
	return conn, nil
}

func (c *tcpConnMaskConfig) WrapConnServer(conn net.Conn) (net.Conn, error) {
	if c == nil || c.message == nil {
		_ = conn.Close()
		return conn, nil
	}
	key := tcpConnTrackerKey{
		inboundID:  c.message.Get(tcpConnMaskFields.inboundID).String(),
		generation: c.message.Get(tcpConnMaskFields.generation).Uint(),
	}
	tcpConnTrackersMu.Lock()
	tracker := tcpConnTrackers[key]
	tcpConnTrackersMu.Unlock()
	if tracker == nil {
		_ = conn.Close()
		return conn, nil
	}
	return tracker.track(conn), nil
}

type tcpConnMaskMessageType struct{}

func (tcpConnMaskMessageType) New() protoreflect.Message {
	return newTCPConnMaskConfig("", 0).ProtoReflect()
}

func (tcpConnMaskMessageType) Zero() protoreflect.Message {
	return newTCPConnMaskConfig("", 0).ProtoReflect()
}

func (tcpConnMaskMessageType) Descriptor() protoreflect.MessageDescriptor {
	return tcpConnMaskDescriptor
}

type tcpConnMaskReflection struct {
	protoreflect.Message
	owner *tcpConnMaskConfig
}

func (m *tcpConnMaskReflection) Type() protoreflect.MessageType {
	return tcpConnMaskType
}

func (m *tcpConnMaskReflection) New() protoreflect.Message {
	return tcpConnMaskType.New()
}

func (m *tcpConnMaskReflection) Interface() protoreflect.ProtoMessage {
	return m.owner
}

func init() {
	optional := descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL
	stringType := descriptorpb.FieldDescriptorProto_TYPE_STRING
	uint64Type := descriptorpb.FieldDescriptorProto_TYPE_UINT64
	file, err := protodesc.NewFile(&descriptorpb.FileDescriptorProto{
		Name:    proto.String("internal/node/vless/tcpmask.proto"),
		Package: proto.String("mistgate.node.vless"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{{
			Name: proto.String("ConnectionTracker"),
			Field: []*descriptorpb.FieldDescriptorProto{
				{Name: proto.String("inbound_id"), JsonName: proto.String("inboundId"), Number: proto.Int32(1), Label: &optional, Type: &stringType},
				{Name: proto.String("generation"), Number: proto.Int32(2), Label: &optional, Type: &uint64Type},
			},
		}},
	}, nil)
	if err != nil {
		panic(err)
	}
	tcpConnMaskDescriptor = file.Messages().ByName("ConnectionTracker")
	tcpConnMaskFields.inboundID = tcpConnMaskDescriptor.Fields().ByName("inbound_id")
	tcpConnMaskFields.generation = tcpConnMaskDescriptor.Fields().ByName("generation")
	tcpConnMaskType = tcpConnMaskMessageType{}
	if err := protoregistry.GlobalTypes.RegisterMessage(tcpConnMaskType); err != nil {
		panic(err)
	}
}

var (
	_ finalmask.TCPMask     = (*tcpConnMaskConfig)(nil)
	_ finalmask.TcpMaskConn = (*trackedTCPConn)(nil)
)

// sessionKex counts the ClientHello of the XHTTP request that opened a session for its key: the request's source is the TCP
// peer (the engine never sets trustedXForwardedFor, so a forwarded address has port 0 and finds nothing: unknown).
func (t *tcpConnTracker) sessionKex(cs *credState, src xnet.Destination) {
	var ap netip.AddrPort
	if src.Address != nil && src.Address.Family().IsIP() {
		if ip, ok := netip.AddrFromSlice(src.Address.IP()); ok {
			ap = netip.AddrPortFrom(ip.Unmap(), uint16(src.Port))
		}
	}
	kex, first, found := t.kexOfAddr(ap)
	if !found {
		cs.countKex(kexUnknown)
	} else if first {
		cs.countKex(kex)
	}
}
