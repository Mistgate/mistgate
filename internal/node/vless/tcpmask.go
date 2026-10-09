package vless

import (
	"errors"
	"net"
	"sync"

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
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		_ = conn.Close()
		return tracked
	}
	t.conns[tracked] = struct{}{}
	t.mu.Unlock()
	return tracked
}

func (t *tcpConnTracker) remove(conn *trackedTCPConn) {
	t.mu.Lock()
	delete(t.conns, conn)
	t.mu.Unlock()
}

type trackedTCPConn struct {
	net.Conn
	tracker *tcpConnTracker
	once    sync.Once
}

func (c *trackedTCPConn) Close() error {
	var err error
	c.once.Do(func() {
		c.tracker.remove(c)
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

func (*tcpConnMaskConfig) TCP() {}

func (c *tcpConnMaskConfig) WrapConnClient(conn net.Conn) (net.Conn, error) {
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
	_ finalmask.Tcpmask     = (*tcpConnMaskConfig)(nil)
	_ finalmask.TcpMaskConn = (*trackedTCPConn)(nil)
)
