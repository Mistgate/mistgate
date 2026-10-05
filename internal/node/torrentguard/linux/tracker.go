package linux

import (
	"bytes"
	"net/netip"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/node/torrentguard"
)

// BlockMark is the packet mark of the runtime's block verdict (NF_REPEAT): on the repeat the guard's nft chain stores it
// as the connection's ct mark and drops the packet, and the kernel drops every later packet of that connection, in both
// directions, without queueing it. An unusual constant, so no other software's marks match it.
const BlockMark = 0x4d475442 // "MGTB"

const (
	maxTrackedFlows          = 8192
	maxTCPOutOfOrderDistance = 64
	maxTCPQueuedSegments     = 4
	maxTCPQueuedBytes        = 64
	tcpStateIdle             = 2 * time.Minute
	flowPruneInterval        = 5 * time.Second

	tcpFlagSYN = 0x02
	tcpFlagACK = 0x10
)

const tcpHandshakePrefix = "\x13BitTorrent protocol"

// Detection is one BitTorrent request a tunnel client sent. It names who (the tunnel address, which the agent maps to a
// user and does not report), never where to.
type Detection struct {
	L4Protocol  string
	Signature   torrentguard.Protocol
	TunnelIface string
	TunnelIP    netip.Addr
}

// flowTracker holds the start of the TCP connections tunnel clients open, until their first payload bytes decide
// them; a decided connection is forgotten (the kernel enforces a block by its ct mark). UDP needs no state: every
// datagram the kernel queues (conntrack state new) is classified on its own.
type flowTracker struct {
	mu      sync.Mutex
	flows   map[flowKey]*tcpFlow
	pruneAt time.Time
}

type tcpFlow struct {
	lastSeen time.Time
	dir      tcpDirection
}

type tcpDirection struct {
	nextSequence uint32
	seen         int
	matched      bool
	rejected     bool
	pending      []tcpSegment
	pendingBytes int
}

type tcpSegment struct {
	sequence uint32
	data     []byte
}

func newFlowTracker() *flowTracker {
	return &flowTracker{flows: make(map[flowKey]*tcpFlow)}
}

// classify inspects one packet a tunnel client sent to the outside and reports whether it carries a BitTorrent request.
// The caller must give it nothing else: a packet from the remote side is never evidence, so a remote server or peer
// cannot frame a user with a crafted datagram or banner.
func (t *flowTracker) classify(packet packetInfo, tunnelIface string, tunnelIP netip.Addr, now time.Time) (Detection, bool) {
	d := Detection{TunnelIface: tunnelIface, TunnelIP: tunnelIP}
	switch packet.key.protocol {
	case protocolUDP:
		protocol, ok := torrentguard.DetectClientUDPRequest(packet.payload)
		d.L4Protocol, d.Signature = "udp", protocol
		return d, ok
	case protocolTCP:
		packet.key.tunnelIface = tunnelIface
		if t.feedTCP(packet, now) {
			d.L4Protocol, d.Signature = "tcp", torrentguard.ProtocolBitTorrentTCP
			return d, true
		}
	}
	return Detection{}, false
}

// feedTCP follows the client's side of a connection the client opened: only its SYN starts a flow, so a stream joined
// in the middle, or one the client accepted, is never classified.
func (t *flowTracker) feedTCP(packet packetInfo, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)

	flow := t.flows[packet.key]
	if flow == nil {
		if packet.tcpFlags&(tcpFlagSYN|tcpFlagACK) != tcpFlagSYN {
			return false
		}
		if len(t.flows) >= maxTrackedFlows {
			// ponytail: evicts an arbitrary flow (map order), not the least recently used one; a flow lives here for a
			// handful of packets at the start of a connection, so a real LRU would buy little.
			for k := range t.flows {
				delete(t.flows, k)
				break
			}
		}
		flow = &tcpFlow{dir: tcpDirection{nextSequence: packet.seq + 1}}
		t.flows[packet.key] = flow
	}
	flow.lastSeen = now
	matched := flow.dir.feed(packet)
	if matched || flow.dir.rejected {
		delete(t.flows, packet.key)
	}
	return matched
}

func (t *flowTracker) prune(now time.Time) {
	if !t.pruneAt.IsZero() && now.Before(t.pruneAt) {
		return
	}
	t.pruneAt = now.Add(flowPruneInterval)
	for k, flow := range t.flows {
		if now.Sub(flow.lastSeen) >= tcpStateIdle {
			delete(t.flows, k)
		}
	}
}

// retainInterfaces removes flow state for AWG interfaces whose identity changed or was removed. Call it under ifaceMu
// so packet scope and flow state change atomically.
func (t *flowTracker) retainInterfaces(keep map[string]struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for k := range t.flows {
		if _, ok := keep[k.tunnelIface]; !ok {
			delete(t.flows, k)
		}
	}
}

func (d *tcpDirection) feed(packet packetInfo) bool {
	sequence := packet.seq
	if packet.tcpFlags&tcpFlagSYN != 0 {
		sequence++ // SYN consumes one sequence number (a Fast Open SYN carries data after it).
	}
	if len(packet.payload) == 0 {
		return false
	}
	if d.consume(sequence, packet.payload) {
		return true
	}
	return d.drain()
}

func (d *tcpDirection) consume(sequence uint32, payload []byte) bool {
	if d.matched || d.rejected || len(payload) == 0 {
		return d.matched
	}
	delta := int32(sequence - d.nextSequence)
	if delta < 0 {
		// A wholly old segment is a retransmission. A partial overlap is
		// ambiguous and permanently gives up on this direction.
		if int64(delta)+int64(len(payload)) <= 0 {
			return false
		}
		d.reject()
		return false
	}
	if delta > 0 {
		remaining := len(tcpHandshakePrefix) - d.seen
		if delta >= int32(remaining) || delta > maxTCPOutOfOrderDistance {
			d.reject()
			return false
		}
		length := min(len(payload), remaining-int(delta))
		segment := tcpSegment{sequence: sequence, data: append([]byte(nil), payload[:length]...)}
		for _, old := range d.pending {
			oldDelta := int32(segment.sequence - old.sequence)
			if oldDelta == 0 && bytes.Equal(segment.data, old.data) {
				return false // identical retransmission of a queued segment
			}
			if int64(oldDelta) < int64(len(old.data)) && int64(oldDelta)+int64(len(segment.data)) > 0 {
				d.reject()
				return false
			}
		}
		if len(d.pending) >= maxTCPQueuedSegments || d.pendingBytes+length > maxTCPQueuedBytes {
			d.reject()
			return false
		}
		d.pending = append(d.pending, segment)
		d.pendingBytes += length
		return false
	}

	return d.feedOrdered(payload)
}

// feedOrdered compares the next in-order bytes with the handshake prefix: a mismatch decides the stream at once. A
// payload cut short by the queue's copy range still holds far more than the 20 prefix bytes.
func (d *tcpDirection) feedOrdered(payload []byte) bool {
	remaining := tcpHandshakePrefix[d.seen:]
	n := min(len(payload), len(remaining))
	if string(payload[:n]) != remaining[:n] {
		d.reject()
		return false
	}
	d.seen += n
	d.nextSequence += uint32(len(payload))
	d.matched = d.seen == len(tcpHandshakePrefix)
	return d.matched
}

func (d *tcpDirection) drain() bool {
	for !d.matched && !d.rejected {
		index := -1
		for i, segment := range d.pending {
			if segment.sequence == d.nextSequence {
				index = i
				break
			}
		}
		if index < 0 {
			break
		}
		segment := d.pending[index]
		d.pendingBytes -= len(segment.data)
		d.pending = append(d.pending[:index], d.pending[index+1:]...)
		if d.feedOrdered(segment.data) {
			return true
		}
	}
	return d.matched
}

func (d *tcpDirection) reject() {
	d.rejected = true
	d.pending = nil
	d.pendingBytes = 0
}
