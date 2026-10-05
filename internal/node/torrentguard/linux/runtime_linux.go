//go:build linux

package linux

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
)

const (
	nfnlSubsystemQueue = 3
	nfqMessagePacket   = 0
	nfqMessageVerdict  = 1
	nfqMessageConfig   = 2

	nfqCommandBind        = 1
	nfqCommandUnbind      = 2
	nfqAttributePacket    = 1
	nfqAttributeVerdict   = 2
	nfqAttributeMark      = 3
	nfqAttributeInDevice  = 5
	nfqAttributeOutDevice = 6
	nfqAttributePayload   = 10

	nfqConfigCommand    = 1
	nfqConfigParams     = 2
	nfqConfigMaxLen     = 3
	nfqConfigMask       = 4
	nfqConfigFlags      = 5
	nfqConfigFailOpen   = 1
	nfqCopyPacket       = 2
	nfqAccept           = 1
	nfqRepeat           = 4
	nfqMaxQueueLength   = 512
	nfqReadBufferSize   = 4 << 20
	nfqEventQueueLength = 256
)

// Runtime owns one kernel queue and a bounded userspace flow table. The nft
// rules that reference it must always use queue bypass.
type Runtime struct {
	queue uint16
	conn  *netlink.Conn

	tracker *flowTracker

	ifaceMu      sync.RWMutex
	ifaceByIndex map[int]string

	callbackMu sync.RWMutex
	callback   func(Detection)
	events     chan Detection
	eventsDone chan struct{}
	loopDone   chan struct{}
	stopping   atomic.Bool
	closeOnce  sync.Once
	eventsOnce sync.Once
	closeErr   error
	sequence   atomic.Uint32
}

type queuedPacket struct {
	id       uint32
	payload  []byte
	inIndex  int
	outIndex int
}

// Start binds a fail-open netfilter queue and starts its packet and event
// loops. The caller must install nft rules with queue bypass after this
// succeeds and remove those rules before Close.
func Start(queue uint16, ifaces []string, callback func(Detection)) (*Runtime, error) {
	if queue == 0 {
		return nil, errors.New("torrent guard queue number must be non-zero")
	}
	conn, err := netlink.Dial(unix.NETLINK_NETFILTER, nil)
	if err != nil {
		return nil, fmt.Errorf("open netfilter queue socket: %w", err)
	}
	runtime := &Runtime{
		queue:        queue,
		conn:         conn,
		tracker:      newFlowTracker(),
		ifaceByIndex: make(map[int]string),
		callback:     callback,
		events:       make(chan Detection, nfqEventQueueLength),
		eventsDone:   make(chan struct{}),
		loopDone:     make(chan struct{}),
	}
	if err := runtime.SetInterfaces(ifaces); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := conn.SetOption(netlink.NoENOBUFS, true); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("configure netfilter queue socket: %w", err)
	}
	_ = conn.SetReadBuffer(nfqReadBufferSize)
	if err := runtime.bind(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bind netfilter queue: %w", err)
	}
	go runtime.dispatchEvents()
	go runtime.receiveLoop()
	return runtime, nil
}

// Stopped reports whether the packet receive loop has exited.
func (r *Runtime) Stopped() bool {
	select {
	case <-r.loopDone:
		return true
	default:
		return false
	}
}

// SetCallback replaces the current event callback without interrupting packet
// inspection. Callback execution is isolated from the NFQUEUE receive loop.
func (r *Runtime) SetCallback(callback func(Detection)) {
	r.callbackMu.Lock()
	r.callback = callback
	r.callbackMu.Unlock()
}

// SetInterfaces refreshes the exact active interface-index mapping and drops
// flow state only for interfaces whose identity changed or was removed.
func (r *Runtime) SetInterfaces(ifaces []string) error {
	next := make(map[int]string, len(ifaces))
	for _, name := range ifaces {
		iface, err := net.InterfaceByName(name)
		if err != nil {
			// The queue can be installed before the AWG engine creates links.
			// A later SetInterfaces call resolves them after Apply.
			continue
		}
		next[iface.Index] = name
	}
	r.ifaceMu.Lock()
	if equalInterfaceMap(r.ifaceByIndex, next) {
		r.ifaceMu.Unlock()
		return nil
	}
	oldIndexByName := make(map[string]int, len(r.ifaceByIndex))
	for index, name := range r.ifaceByIndex {
		oldIndexByName[name] = index
	}
	keep := make(map[string]struct{}, len(next))
	for index, name := range next {
		if oldIndexByName[name] == index {
			keep[name] = struct{}{}
		}
	}
	r.ifaceByIndex = next
	// Keep the writer lock through scope pruning: the packet path acquires ifaceMu
	// before tracker.mu, so it cannot reuse a flow across tunnel remapping.
	r.tracker.retainInterfaces(keep)
	r.ifaceMu.Unlock()
	return nil
}

func equalInterfaceMap(a, b map[int]string) bool {
	if len(a) != len(b) {
		return false
	}
	for index, name := range a {
		if b[index] != name {
			return false
		}
	}
	return true
}

// Close unbinds the owned queue and closes its netlink socket. Callers should
// remove the nft table first; its queue statements use bypass as a second
// fail-open safeguard.
func (r *Runtime) Close() error {
	r.closeOnce.Do(func() {
		r.stopping.Store(true)
		// Expire the blocking Receive call without closing the socket so the
		// owned queue can be cleanly unbound after its reader has stopped.
		if err := r.conn.SetReadDeadline(time.Now()); err != nil {
			// Closing also wakes Receive; queue bypass and NFQA_CFG_F_FAIL_OPEN
			// preserve traffic if clean unbind is unavailable.
			_ = r.conn.Close()
		}
		<-r.loopDone
		var queueErr error
		if err := r.conn.SetReadDeadline(time.Time{}); err == nil {
			if err := r.setCommand(nfqCommandUnbind, r.queue); err != nil {
				queueErr = err
			}
		}
		_ = r.conn.Close()
		r.stopEvents()
		select {
		case <-r.eventsDone:
		case <-time.After(time.Second):
		}
		r.closeErr = queueErr
	})
	return r.closeErr
}

func (r *Runtime) bind() error {
	if err := r.setCommand(nfqCommandBind, r.queue); err != nil {
		return err
	}
	flags := make([]byte, 4)
	binary.BigEndian.PutUint32(flags, nfqConfigFailOpen)
	maxLen := make([]byte, 4)
	binary.BigEndian.PutUint32(maxLen, nfqMaxQueueLength)
	params := make([]byte, 5)
	binary.BigEndian.PutUint32(params[:4], queueCopyRange)
	params[4] = nfqCopyPacket
	if err := r.configure(r.queue, []netlink.Attribute{
		{Type: nfqConfigParams, Data: params},
		{Type: nfqConfigFlags, Data: flags},
		{Type: nfqConfigMask, Data: append([]byte(nil), flags...)},
		{Type: nfqConfigMaxLen, Data: maxLen},
	}); err != nil {
		_ = r.setCommand(nfqCommandUnbind, r.queue)
		return err
	}
	return nil
}

func (r *Runtime) setCommand(command uint8, queue uint16) error {
	commandData := make([]byte, 4)
	commandData[0] = command
	binary.BigEndian.PutUint16(commandData[2:], unix.AF_UNSPEC)
	return r.configure(queue, []netlink.Attribute{{Type: nfqConfigCommand, Data: commandData}})
}

func (r *Runtime) configure(queue uint16, attributes []netlink.Attribute) error {
	encoded, err := netlink.MarshalAttributes(attributes)
	if err != nil {
		return err
	}
	data := make([]byte, 4, 4+len(encoded))
	data[0] = unix.AF_UNSPEC
	data[1] = 0 // NFNETLINK_V0
	binary.BigEndian.PutUint16(data[2:4], queue)
	data = append(data, encoded...)
	sequence := r.sequence.Add(1)
	req := netlink.Message{
		Header: netlink.Header{
			Type:     netlink.HeaderType(nfnlSubsystemQueue<<8 | nfqMessageConfig),
			Flags:    netlink.Request | netlink.Acknowledge,
			Sequence: sequence,
		},
		Data: data,
	}
	_, err = r.conn.Execute(req)
	return err
}

func (r *Runtime) receiveLoop() {
	defer func() {
		if !r.stopping.Load() {
			// A dead userspace listener must release its queue so nft's bypass
			// rule can accept subsequent packets immediately.
			_ = r.conn.Close()
		}
		close(r.loopDone)
		r.stopEvents()
	}()
	backoff := time.Millisecond
	for {
		messages, err := r.conn.Receive()
		if err != nil {
			if r.stopping.Load() || errors.Is(err, net.ErrClosed) || errors.Is(err, unix.EBADF) {
				return
			}
			if errors.Is(err, unix.ENOBUFS) || isTimeout(err) {
				time.Sleep(backoff)
				if backoff < 100*time.Millisecond {
					backoff *= 2
				}
				continue
			}
			return
		}
		backoff = time.Millisecond
		for _, message := range messages {
			if uint16(message.Header.Type)&0xff != nfqMessagePacket {
				continue
			}
			r.handleMessage(message)
		}
	}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (r *Runtime) handleMessage(message netlink.Message) {
	queued, ok := decodeQueuedPacket(message.Data)
	if !ok {
		return
	}
	var detection Detection
	block := false
	if packet, parsed := parsePacket(queued.payload); parsed {
		r.ifaceMu.RLock()
		if iface, fromClient := r.clientIfaceLocked(queued.inIndex, queued.outIndex); fromClient {
			detection, block = r.tracker.classify(packet, iface, packet.key.source, time.Now())
		}
		r.ifaceMu.RUnlock()
	}
	if !block {
		_ = r.setVerdict(queued.id, nfqAccept, 0)
		return
	}
	// Repeat with the block mark: the guard's chain stores it as the connection's ct mark and drops the packet; the rest
	// of the connection never reaches userspace again.
	if err := r.setVerdict(queued.id, nfqRepeat, BlockMark); err != nil {
		// A lost verdict must not turn an identified flow into an accidental
		// queue stall when an accept verdict can still be written.
		_ = r.setVerdict(queued.id, nfqAccept, 0)
	}
	r.enqueueEvent(detection) // best effort: the agent reports one event per user every few minutes anyway
}

// clientIfaceLocked returns the AWG interface a packet came in through when it leaves through another interface: only
// what a tunnel client sends to the outside is classified. ifaceMu must be held for reading so interface remapping and
// flow-state reset are atomic from the packet path's perspective.
func (r *Runtime) clientIfaceLocked(inIndex, outIndex int) (string, bool) {
	in := r.ifaceByIndex[inIndex]
	return in, in != "" && r.ifaceByIndex[outIndex] == ""
}

// setVerdict sends the verdict for one queued packet; a non-zero mark becomes the packet's mark.
func (r *Runtime) setVerdict(id, verdict, mark uint32) error {
	message, err := verdictMessage(r.queue, id, verdict, mark)
	if err != nil {
		return err
	}
	_, err = r.conn.Send(message)
	return err
}

func verdictMessage(queue uint16, id, verdict, mark uint32) (netlink.Message, error) {
	verdictData := make([]byte, 8)
	binary.BigEndian.PutUint32(verdictData[:4], verdict)
	binary.BigEndian.PutUint32(verdictData[4:], id)
	list := []netlink.Attribute{{Type: nfqAttributeVerdict, Data: verdictData}}
	if mark != 0 {
		list = append(list, netlink.Attribute{Type: nfqAttributeMark, Data: binary.BigEndian.AppendUint32(nil, mark)})
	}
	attributes, err := netlink.MarshalAttributes(list)
	if err != nil {
		return netlink.Message{}, err
	}
	data := make([]byte, 4, 4+len(attributes))
	data[0] = unix.AF_UNSPEC
	data[1] = 0
	binary.BigEndian.PutUint16(data[2:4], queue)
	data = append(data, attributes...)
	return netlink.Message{
		Header: netlink.Header{Type: netlink.HeaderType(nfnlSubsystemQueue<<8 | nfqMessageVerdict), Flags: netlink.Request},
		Data:   data,
	}, nil
}

func decodeQueuedPacket(data []byte) (queuedPacket, bool) {
	if len(data) < 4 {
		return queuedPacket{}, false
	}
	attributes, err := netlink.UnmarshalAttributes(data[4:])
	if err != nil {
		return queuedPacket{}, false
	}
	var packet queuedPacket
	var hasID bool
	for _, attribute := range attributes {
		switch attribute.Type {
		case nfqAttributePacket:
			if len(attribute.Data) < 4 {
				return queuedPacket{}, false
			}
			packet.id = binary.BigEndian.Uint32(attribute.Data[:4])
			hasID = true
		case nfqAttributePayload:
			packet.payload = attribute.Data
		case nfqAttributeInDevice:
			if len(attribute.Data) >= 4 {
				packet.inIndex = int(binary.BigEndian.Uint32(attribute.Data[:4]))
			}
		case nfqAttributeOutDevice:
			if len(attribute.Data) >= 4 {
				packet.outIndex = int(binary.BigEndian.Uint32(attribute.Data[:4]))
			}
		}
	}
	return packet, hasID && packet.payload != nil
}

func (r *Runtime) dispatchEvents() {
	defer close(r.eventsDone)
	for detection := range r.events {
		r.callbackMu.RLock()
		callback := r.callback
		r.callbackMu.RUnlock()
		if callback != nil {
			invokeCallback(callback, detection)
		}
	}
}

func (r *Runtime) enqueueEvent(d Detection) {
	select {
	case r.events <- d:
	default:
	}
}

func (r *Runtime) stopEvents() {
	r.eventsOnce.Do(func() { close(r.events) })
}

func invokeCallback(callback func(Detection), detection Detection) {
	defer func() { _ = recover() }()
	callback(detection)
}
