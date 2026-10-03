package linux

import (
	"net/netip"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/node/torrentguard"
)

const (
	maxTrackedFlows          = 8192
	maxTCPOutOfOrderDistance = 64
	maxTCPQueuedSegments     = 4
	maxTCPQueuedBytes        = 64
	tcpStateIdle             = 2 * time.Minute
	blockedFlowIdle          = 15 * time.Minute
	flowPruneInterval        = 5 * time.Second
)

const tcpHandshakePrefix = "\x13BitTorrent protocol"

type Detection struct {
	SourceIP        netip.Addr
	SourcePort      uint16
	DestinationIP   netip.Addr
	DestinationPort uint16
	L4Protocol      string
	Signature       torrentguard.Protocol
	TunnelIface     string
	TunnelIP        netip.Addr
}

type flowTracker struct {
	mu             sync.Mutex
	byKey          map[flowKey]*flowState
	pendingReports map[*flowState]struct{}
	count          int
	pruneAt        time.Time
}

type flowState struct {
	forward     flowKey
	tunnelIface string
	tunnelIP    netip.Addr
	lastSeen    time.Time
	blocked     bool
	reported    bool
	candidate   *Detection
	tcp         [2]tcpDirection
}

type tcpDirection struct {
	started      bool
	nextSequence uint32
	seen         int
	detector     torrentguard.TCPHandshakeDetector
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
	return &flowTracker{
		byKey:          make(map[flowKey]*flowState),
		pendingReports: make(map[*flowState]struct{}),
	}
}

// Process inspects one complete network-layer packet and reports whether its
// exact flow should be dropped. The report callback must be non-blocking and
// returns false when the caller cannot enqueue an event. A confirmed flow is
// still blocked; its event is retried by the runtime timer until accepted.
func (t *flowTracker) Process(raw []byte, tunnelIface string, tunnelIP netip.Addr, now time.Time, report func(Detection) bool) bool {
	packet, ok := parsePacket(raw)
	if !ok {
		return false
	}
	return t.processPacket(packet, tunnelIface, tunnelIP, now, report)
}

func (t *flowTracker) processPacket(packet packetInfo, tunnelIface string, tunnelIP netip.Addr, now time.Time, report func(Detection) bool) bool {
	packet.key.tunnelIface = tunnelIface
	t.mu.Lock()
	defer t.mu.Unlock()
	t.prune(now)

	state := t.byKey[packet.key]
	if state != nil && (state.tunnelIface != tunnelIface || state.tunnelIP != tunnelIP) {
		// A tuple can repeat on separate AWG tunnels (or after a client address change).
		// Never let state from one tunnel classify traffic from another.
		t.delete(state)
		state = nil
	}
	if state != nil && now.Sub(state.lastSeen) >= t.idleFor(state) {
		t.delete(state)
		state = nil
	}

	if state != nil {
		state.lastSeen = now
		if state.blocked {
			t.tryReport(state, report)
			return true
		}
		if state.candidate == nil && packet.key.protocol == protocolTCP {
			index := directionIndex(state.forward, packet.key)
			if state.tcp[index].feed(packet) {
				candidate := detectionFor(packet.key, torrentguard.ProtocolBitTorrentTCP, tunnelIface, tunnelIP)
				state.candidate = &candidate
				state.blocked = true
				t.pendingReports[state] = struct{}{}
				t.tryReport(state, report)
				return true
			}
		}
		return false
	}

	if packet.key.protocol == protocolUDP {
		if tunnelIface == "" || t.count >= maxTrackedFlows {
			return false
		}
		protocol, ok := detectUDPRequest(packet.payload)
		if !ok {
			return false
		}
		candidate := detectionFor(packet.key, protocol, tunnelIface, tunnelIP)
		state = &flowState{forward: packet.key, tunnelIface: tunnelIface, tunnelIP: tunnelIP, lastSeen: now, blocked: true, candidate: &candidate}
		t.insert(state)
		t.pendingReports[state] = struct{}{}
		t.tryReport(state, report)
		return true
	}

	// A TCP stream can be classified only when its initial SYN was observed.
	// This avoids treating a coincidental string in the middle of a connection
	// as a BitTorrent handshake.
	if packet.key.protocol != protocolTCP || packet.tcpFlags&0x02 == 0 || t.count >= maxTrackedFlows {
		return false
	}
	state = &flowState{forward: packet.key, tunnelIface: tunnelIface, tunnelIP: tunnelIP, lastSeen: now}
	t.insert(state)
	index := directionIndex(state.forward, packet.key)
	if state.tcp[index].feed(packet) && tunnelIface != "" {
		candidate := detectionFor(packet.key, torrentguard.ProtocolBitTorrentTCP, tunnelIface, tunnelIP)
		state.candidate = &candidate
		state.blocked = true
		t.pendingReports[state] = struct{}{}
		t.tryReport(state, report)
		return true
	}
	return false
}

func (t *flowTracker) tryReport(state *flowState, report func(Detection) bool) bool {
	if state.reported || state.candidate == nil {
		return true
	}
	if report == nil || !report(*state.candidate) {
		return false
	}
	state.reported = true
	delete(t.pendingReports, state)
	return true
}

// RetryPendingReports re-enqueues detections that could not fit in the
// bounded event channel when the flow was first classified. Callers should
// invoke it from a timer so a one-packet UDP flow still gets reported.
func (t *flowTracker) RetryPendingReports(report func(Detection) bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for state := range t.pendingReports {
		if !t.tryReport(state, report) {
			// A full bounded channel cannot accept any later event in this pass.
			break
		}
	}
}

func (t *flowTracker) insert(state *flowState) {
	t.byKey[state.forward] = state
	reverse := state.forward.reverse()
	t.byKey[reverse] = state
	t.count++
}

func (t *flowTracker) delete(state *flowState) {
	delete(t.byKey, state.forward)
	delete(t.byKey, state.forward.reverse())
	delete(t.pendingReports, state)
	t.count--
}

func (t *flowTracker) idleFor(state *flowState) time.Duration {
	if state.blocked {
		return blockedFlowIdle
	}
	return tcpStateIdle
}

func (t *flowTracker) prune(now time.Time) {
	if !t.pruneAt.IsZero() && now.Before(t.pruneAt) {
		return
	}
	t.pruneAt = now.Add(flowPruneInterval)
	seen := make(map[*flowState]struct{}, t.count)
	for _, state := range t.byKey {
		if _, ok := seen[state]; ok {
			continue
		}
		seen[state] = struct{}{}
		if now.Sub(state.lastSeen) >= t.idleFor(state) {
			t.delete(state)
		}
	}
}

// retainInterfaces removes flow state for AWG interfaces whose identity changed or was removed while
// preserving active blocks on interfaces that remain. Call it under ifaceMu so packet scope and flow state
// change atomically.
func (t *flowTracker) retainInterfaces(keep map[string]struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()
	seen := make(map[*flowState]struct{}, t.count)
	for _, state := range t.byKey {
		if _, ok := seen[state]; ok {
			continue
		}
		seen[state] = struct{}{}
		if _, ok := keep[state.tunnelIface]; !ok {
			t.delete(state)
		}
	}
}

func directionIndex(forward, current flowKey) int {
	if current == forward {
		return 0
	}
	return 1
}

func detectionFor(key flowKey, signature torrentguard.Protocol, tunnelIface string, tunnelIP netip.Addr) Detection {
	protocol := "udp"
	if key.protocol == protocolTCP {
		protocol = "tcp"
	}
	return Detection{
		SourceIP:        key.source,
		SourcePort:      key.sourcePort,
		DestinationIP:   key.destination,
		DestinationPort: key.destPort,
		L4Protocol:      protocol,
		Signature:       signature,
		TunnelIface:     tunnelIface,
		TunnelIP:        tunnelIP,
	}
}

func detectUDPRequest(payload []byte) (torrentguard.Protocol, bool) {
	protocol, ok := torrentguard.DetectUDP(payload)
	if !ok {
		return "", false
	}
	switch protocol {
	case torrentguard.ProtocolBitTorrentTracker:
		// DetectUDP recognizes only complete BEP 15 client requests.
		return protocol, true
	case torrentguard.ProtocolBitTorrentDHT:
		message, valid := torrentguard.ParseKRPCMessage(payload)
		return protocol, valid && message.Type == torrentguard.KRPCQuery
	case torrentguard.ProtocolBitTorrentUTP:
		header, valid := torrentguard.ParseUTPHeader(payload)
		// A valid uTP SYN is stronger flow-start evidence than an arbitrary
		// structurally valid DATA/STATE packet on an unrelated UDP flow.
		return protocol, valid && header.Type == torrentguard.UTPSyn
	default:
		return "", false
	}
}

func (d *tcpDirection) feed(packet packetInfo) bool {
	if d.matched {
		return true
	}
	if d.rejected {
		return false
	}
	sequence := packet.seq
	if packet.tcpFlags&0x02 != 0 {
		if !d.started {
			d.started = true
			d.nextSequence = packet.seq + 1
		}
		sequence++ // SYN consumes one sequence number.
	} else if !d.started {
		return false
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
		length := len(payload)
		if length > remaining-int(delta) {
			length = remaining - int(delta)
		}
		if length <= 0 {
			return false
		}
		segment := tcpSegment{sequence: sequence, data: append([]byte(nil), payload[:length]...)}
		for _, old := range d.pending {
			oldDelta := int32(segment.sequence - old.sequence)
			if oldDelta == 0 && equalBytes(segment.data, old.data) {
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

func (d *tcpDirection) feedOrdered(payload []byte) bool {
	remaining := len(tcpHandshakePrefix) - d.seen
	if remaining <= 0 {
		return d.matched
	}
	length := len(payload)
	if length > remaining {
		length = remaining
	}
	if d.detector.Feed(payload[:length]) {
		d.matched = true
		return true
	}
	d.seen += length
	d.nextSequence += uint32(len(payload))
	return false
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

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
