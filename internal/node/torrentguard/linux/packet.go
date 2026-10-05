package linux

import (
	"encoding/binary"
	"net/netip"
)

const (
	protocolTCP = 6
	protocolUDP = 17
)

type flowKey struct {
	source      netip.Addr
	destination netip.Addr
	sourcePort  uint16
	destPort    uint16
	protocol    uint8
	tunnelIface string
}

func (k flowKey) reverse() flowKey {
	return flowKey{
		source:      k.destination,
		destination: k.source,
		sourcePort:  k.destPort,
		destPort:    k.sourcePort,
		protocol:    k.protocol,
		tunnelIface: k.tunnelIface,
	}
}

type packetInfo struct {
	key      flowKey
	payload  []byte
	seq      uint32
	tcpFlags uint8
}

func parsePacket(raw []byte) (packetInfo, bool) {
	if len(raw) == 0 {
		return packetInfo{}, false
	}
	switch raw[0] >> 4 {
	case 4:
		return parseIPv4(raw)
	case 6:
		return parseIPv6(raw)
	default:
		return packetInfo{}, false
	}
}

// queueCopyRange is how much of each packet the queue copies to userspace: the headers and the first payload bytes.
// A packet longer than that is parsed from what arrived: a TCP payload keeps its first bytes (all the handshake check
// needs), while a UDP datagram whose length runs past the copy is not parsed at all (every UDP signature is a short,
// complete datagram: tracker requests, DHT queries, uTP SYN).
const queueCopyRange = 512

func parseIPv4(raw []byte) (packetInfo, bool) {
	if len(raw) < 20 || raw[0]>>4 != 4 {
		return packetInfo{}, false
	}
	headerLen := int(raw[0]&0x0f) * 4
	totalLen := int(binary.BigEndian.Uint16(raw[2:4]))
	if headerLen < 20 || headerLen > len(raw) || totalLen < headerLen {
		return packetInfo{}, false
	}
	totalLen = min(totalLen, len(raw))
	// Any fragment needs packet-level reassembly before transport inspection.
	// Unknown fragments pass unchanged rather than being inspected partially.
	if binary.BigEndian.Uint16(raw[6:8])&0x3fff != 0 {
		return packetInfo{}, false
	}
	var sourceBytes, destinationBytes [4]byte
	copy(sourceBytes[:], raw[12:16])
	copy(destinationBytes[:], raw[16:20])
	return parseTransport(raw[headerLen:totalLen], raw[9], netip.AddrFrom4(sourceBytes), netip.AddrFrom4(destinationBytes))
}

func parseIPv6(raw []byte) (packetInfo, bool) {
	if len(raw) < 40 || raw[0]>>4 != 6 {
		return packetInfo{}, false
	}
	payloadLen := int(binary.BigEndian.Uint16(raw[4:6]))
	packetLen := min(40+payloadLen, len(raw))
	var sourceBytes, destinationBytes [16]byte
	copy(sourceBytes[:], raw[8:24])
	copy(destinationBytes[:], raw[24:40])
	source, destination := netip.AddrFrom16(sourceBytes), netip.AddrFrom16(destinationBytes)
	next := raw[6]
	offset := 40
	end := packetLen
	for extensions := 0; ; extensions++ {
		if extensions >= 8 {
			return packetInfo{}, false
		}
		switch next {
		case 0, 43, 60, 135, 139, 140: // Hop-by-hop, routing, destination, mobility, HIP, Shim6.
			if end-offset < 2 {
				return packetInfo{}, false
			}
			length := (int(raw[offset+1]) + 1) * 8
			if length > end-offset {
				return packetInfo{}, false
			}
			next = raw[offset]
			offset += length
		case 44:
			// Fragment headers, including atomic fragments, are conservatively
			// passed through instead of inspecting potentially partial L4 data.
			return packetInfo{}, false
		case 51: // Authentication header.
			if end-offset < 2 {
				return packetInfo{}, false
			}
			length := (int(raw[offset+1]) + 2) * 4
			if length > end-offset {
				return packetInfo{}, false
			}
			next = raw[offset]
			offset += length
		default:
			return parseTransport(raw[offset:end], next, source, destination)
		}
	}
}

func parseTransport(raw []byte, protocol uint8, source, destination netip.Addr) (packetInfo, bool) {
	key := flowKey{source: source, destination: destination, protocol: protocol}
	switch protocol {
	case protocolTCP:
		if len(raw) < 20 {
			return packetInfo{}, false
		}
		headerLen := int(raw[12]>>4) * 4
		if headerLen < 20 || headerLen > len(raw) {
			return packetInfo{}, false
		}
		key.sourcePort = binary.BigEndian.Uint16(raw[0:2])
		key.destPort = binary.BigEndian.Uint16(raw[2:4])
		return packetInfo{key: key, seq: binary.BigEndian.Uint32(raw[4:8]), tcpFlags: raw[13], payload: raw[headerLen:]}, true
	case protocolUDP:
		if len(raw) < 8 {
			return packetInfo{}, false
		}
		length := int(binary.BigEndian.Uint16(raw[4:6]))
		if length < 8 || length > len(raw) {
			return packetInfo{}, false
		}
		key.sourcePort = binary.BigEndian.Uint16(raw[0:2])
		key.destPort = binary.BigEndian.Uint16(raw[2:4])
		return packetInfo{key: key, payload: raw[8:length]}, true
	default:
		return packetInfo{}, false
	}
}
