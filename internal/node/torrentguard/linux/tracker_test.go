package linux

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/torrentguard"
)

func TestParsePacketIPv4AndIPv6(t *testing.T) {
	t.Run("IPv4 TCP", func(t *testing.T) {
		packet := makeTCP4("10.0.0.2", "198.51.100.7", 51000, 443, 1, 0x02, nil)
		got, ok := parsePacket(packet)
		if !ok || got.key.protocol != protocolTCP || got.key.source.String() != "10.0.0.2" || got.key.destination.String() != "198.51.100.7" || got.key.sourcePort != 51000 || got.key.destPort != 443 {
			t.Fatalf("parsePacket() = %#v, %v", got, ok)
		}
	})

	t.Run("IPv6 extension plus UDP", func(t *testing.T) {
		payload := []byte("hello")
		udp := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint16(udp[:2], 60000)
		binary.BigEndian.PutUint16(udp[2:4], 53)
		binary.BigEndian.PutUint16(udp[4:6], uint16(len(udp)))
		copy(udp[8:], payload)
		packet := make([]byte, 40+8+len(udp))
		packet[0] = 0x60
		binary.BigEndian.PutUint16(packet[4:6], uint16(8+len(udp)))
		packet[6] = 60 // Destination options header.
		copy(packet[8:24], netip.MustParseAddr("2001:db8::2").AsSlice())
		copy(packet[24:40], netip.MustParseAddr("2001:db8::53").AsSlice())
		packet[40] = protocolUDP
		packet[41] = 0 // Eight-byte extension header.
		copy(packet[48:], udp)
		got, ok := parsePacket(packet)
		if !ok || got.key.protocol != protocolUDP || got.key.source.String() != "2001:db8::2" || string(got.payload) != "hello" {
			t.Fatalf("parsePacket() = %#v, %v", got, ok)
		}
	})
}

func TestMalformedAndFragmentedPacketsPass(t *testing.T) {
	valid := makeTCP4("10.0.0.2", "198.51.100.7", 51000, 443, 1, 0x02, nil)
	malformed := [][]byte{
		nil,
		valid[:19],
		append([]byte(nil), valid[:len(valid)-1]...),
	}
	badLength := append([]byte(nil), valid...)
	binary.BigEndian.PutUint16(badLength[2:4], 19)
	malformed = append(malformed, badLength)
	fragment := append([]byte(nil), valid...)
	binary.BigEndian.PutUint16(fragment[6:8], 0x2000)
	malformed = append(malformed, fragment)
	badTCPHeader := append([]byte(nil), valid...)
	badTCPHeader[32] = 0x40 // Data offset below the minimum.
	malformed = append(malformed, badTCPHeader)

	for i, raw := range malformed {
		if _, ok := parsePacket(raw); ok {
			t.Errorf("malformed packet %d was parsed", i)
		}
	}
}

// The queue copies only the start of a packet: a long TCP segment keeps its first payload bytes, a UDP datagram that
// did not fit is not inspected at all.
func TestParsePacketUsesTheStartOfACutPacket(t *testing.T) {
	payload := append([]byte("\x13BitTorrent protocol"), make([]byte, 1300)...)
	tcp, ok := parsePacket(makeTCP4("10.0.0.2", "198.51.100.7", 51000, 6881, 1, 0x18, payload)[:queueCopyRange])
	if !ok || len(tcp.payload) != queueCopyRange-40 || string(tcp.payload[:20]) != "\x13BitTorrent protocol" {
		t.Fatalf("cut TCP segment = %v, %d payload bytes", ok, len(tcp.payload))
	}
	if _, ok := parsePacket(makeUDP4("10.0.0.2", "198.51.100.7", 51000, 443, make([]byte, 1200))[:queueCopyRange]); ok {
		t.Fatal("a cut UDP datagram was parsed")
	}
}

const (
	clientIP   = "10.0.0.2"
	peerIP     = "198.51.100.7"
	clientPort = 51000
	peerPort   = 51413
)

var handshake = []byte("\x13BitTorrent protocol")

// classifyRaw is what the runtime does with a packet a client sent through iface.
func classifyRaw(tracker *flowTracker, raw []byte, iface string, at time.Time) (Detection, bool) {
	packet, ok := parsePacket(raw)
	if !ok {
		return Detection{}, false
	}
	return tracker.classify(packet, iface, packet.key.source, at)
}

func TestTrackerDetectsTheInitiatorsHandshakeAndForgetsTheFlow(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(1000, 0)
	send := func(seq uint32, flags uint8, payload []byte) (Detection, bool) {
		return classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort, peerPort, seq, flags, payload), "mgawg51820", now)
	}
	if _, hit := send(100, 0x02, nil); hit {
		t.Fatal("SYN without signature was classified")
	}
	if _, hit := send(101, 0x10, nil); hit {
		t.Fatal("ACK was classified")
	}
	if _, hit := send(101, 0x18, handshake[:9]); hit {
		t.Fatal("partial handshake was classified")
	}
	d, hit := send(110, 0x18, handshake[9:])
	if !hit || d.Signature != torrentguard.ProtocolBitTorrentTCP || d.L4Protocol != "tcp" || d.TunnelIface != "mgawg51820" || d.TunnelIP.String() != clientIP {
		t.Fatalf("handshake = %#v, %v", d, hit)
	}
	if d.Evidence != torrentguard.EvidenceTCPHandshake || d.DstPort != peerPort {
		t.Fatalf("handshake evidence = %q, destination port %d", d.Evidence, d.DstPort)
	}
	// Decided: the kernel blocks the rest by the connection's ct mark, nothing stays in userspace.
	if len(tracker.flows) != 0 {
		t.Fatalf("decided flow kept: %d flows", len(tracker.flows))
	}

	// An ordinary stream is decided by its first bytes and forgotten as well.
	if _, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort+1, 443, 500, 0x02, nil), "mgawg51820", now); hit {
		t.Fatal("SYN classified")
	}
	if _, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort+1, 443, 501, 0x18, []byte("\x16\x03\x01 TLS")), "mgawg51820", now); hit {
		t.Fatal("TLS classified")
	}
	if len(tracker.flows) != 0 {
		t.Fatalf("ordinary flow kept after its first bytes: %d flows", len(tracker.flows))
	}
}

// Only a connection the client opened is classified: one the client accepted (its first packet is a SYN/ACK), or one
// joined in the middle, never is, so a remote peer cannot make the client look like a torrent client.
func TestTrackerIgnoresConnectionsTheClientDidNotOpen(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(1100, 0)
	for _, flags := range []uint8{0x12, 0x10, 0x18} {
		if _, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort, peerPort, 700, flags, nil), "mgawg51820", now); hit {
			t.Fatalf("flags %#x classified", flags)
		}
		if _, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort, peerPort, 701, 0x18, handshake), "mgawg51820", now); hit {
			t.Fatalf("handshake after flags %#x classified", flags)
		}
	}
	if len(tracker.flows) != 0 {
		t.Fatalf("flows without a client SYN: %d", len(tracker.flows))
	}
}

func TestTrackerSeparatesAWGTunnels(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(1500, 0)
	if _, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort, peerPort, 100, 0x02, nil), "mgawg51820", now); hit {
		t.Fatal("SYN classified")
	}
	// The same five-tuple on another AWG tunnel is a distinct flow without a SYN.
	if _, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort, peerPort, 101, 0x18, handshake), "mgawg51821", now); hit {
		t.Fatal("a flow on the second tunnel used the first tunnel's state")
	}
	tracker.retainInterfaces(map[string]struct{}{"mgawg51821": {}})
	if len(tracker.flows) != 0 {
		t.Fatal("state of a removed interface survived")
	}
}

func TestTCPReassemblyHandlesOrderedAndBoundedOutOfOrderSegments(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(2000, 0)
	send := func(seq uint32, flags uint8, payload []byte) bool {
		_, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, 52000, peerPort, seq, flags, payload), "mgawg10000", now)
		return hit
	}
	if send(300, 0x02, nil) {
		t.Fatal("SYN was classified")
	}
	if send(301, 0x18, handshake[:10]) {
		t.Fatal("first handshake chunk was classified")
	}
	if send(317, 0x18, handshake[16:]) {
		t.Fatal("out-of-order chunk was classified before the gap arrived")
	}
	if !send(311, 0x18, handshake[10:16]) {
		t.Fatal("filling the gap did not complete the handshake")
	}
}

func TestTCPGapBeyondBoundPasses(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(3000, 0)
	send := func(seq uint32, flags uint8, payload []byte) bool {
		_, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, 53000, peerPort, seq, flags, payload), "mgawg10000", now)
		return hit
	}
	_ = send(500, 0x02, nil)
	if send(1000, 0x18, handshake) {
		t.Fatal("packet after a large TCP gap was classified")
	}
	if send(501, 0x18, handshake) {
		t.Fatal("ambiguous stream was detected after a gap")
	}
}

// At the flow limit the oldest state gives way: new connections are still inspected.
func TestTrackerEvictsInsteadOfRefusingNewFlows(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(3500, 0)
	for i := range maxTrackedFlows {
		raw := makeTCP4(fmt.Sprintf("10.1.%d.%d", i/250, i%250+1), peerIP, 40000, 443, 1, 0x02, nil)
		classifyRaw(tracker, raw, "mgawg51820", now)
	}
	if len(tracker.flows) != maxTrackedFlows {
		t.Fatalf("flows = %d", len(tracker.flows))
	}
	classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort, peerPort, 100, 0x02, nil), "mgawg51820", now)
	if _, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort, peerPort, 101, 0x18, handshake), "mgawg51820", now); !hit {
		t.Fatal("a new flow at the limit was not inspected")
	}
	if len(tracker.flows) > maxTrackedFlows {
		t.Fatalf("flows = %d, above the limit", len(tracker.flows))
	}
}

func TestTCPStateExpiresAfterIdleRetention(t *testing.T) {
	tracker := newFlowTracker()
	start := time.Unix(5000, 0)
	classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort, peerPort, 100, 0x02, nil), "mgawg51820", start)
	if _, hit := classifyRaw(tracker, makeTCP4(clientIP, peerIP, clientPort, peerPort, 101, 0x18, handshake), "mgawg51820", start.Add(tcpStateIdle+time.Second)); hit {
		t.Fatal("an expired flow was still classified")
	}
	if len(tracker.flows) != 0 {
		t.Fatalf("expired flows = %d", len(tracker.flows))
	}
}

func TestUDPClassificationIsPerDatagram(t *testing.T) {
	quic := []byte{0x41, 0x00, 0x9c, 0x3e, 0x71, 0x0d, 0xa2, 0x55, 0x18, 0xe4, 0x6b, 0x30, 0xc7, 0x02, 0x8f, 0x99, 0x24, 0xd1, 0x6e, 0x0b}
	wireguard := make([]byte, 148)
	wireguard[0] = 1
	for _, test := range []struct {
		name      string
		payload   []byte
		signature torrentguard.Protocol
		evidence  torrentguard.Evidence
	}{
		{name: "tracker", payload: trackerConnectRequest(), signature: torrentguard.ProtocolBitTorrentTracker, evidence: torrentguard.EvidenceTrackerConnect},
		{name: "DHT query", payload: []byte("d1:ad2:id20:01234567890123456789e1:q4:ping1:t2:aa1:y1:qe"), signature: torrentguard.ProtocolBitTorrentDHT, evidence: torrentguard.EvidenceDHTQuery},
		{name: "DHT response", payload: []byte("d1:rd2:id20:01234567890123456789e1:t2:aa1:y1:re")},
		{name: "uTP SYN", payload: utpPacket(torrentguard.UTPSyn), signature: torrentguard.ProtocolBitTorrentUTP, evidence: torrentguard.EvidenceUTPSyn},
		{name: "uTP STATE", payload: utpPacket(torrentguard.UTPState)},
		{name: "QUIC short header", payload: quic},
		{name: "WireGuard initiation", payload: wireguard},
		{name: "unknown", payload: []byte("ordinary UDP data")},
	} {
		t.Run(test.name, func(t *testing.T) {
			tracker := newFlowTracker()
			d, hit := classifyRaw(tracker, makeUDP4(clientIP, peerIP, 55000, 51413, test.payload), "mgawg51820", time.Unix(4000, 0))
			if hit != (test.signature != "") {
				t.Fatalf("classified = %v, want %v", hit, test.signature != "")
			}
			if hit && (d.Signature != test.signature || d.L4Protocol != "udp" || d.TunnelIP.String() != clientIP) {
				t.Fatalf("detection = %#v", d)
			}
			if hit && (d.Evidence != test.evidence || d.DstPort != 51413) {
				t.Fatalf("evidence = %q, destination port %d", d.Evidence, d.DstPort)
			}
			if len(tracker.flows) != 0 {
				t.Fatal("UDP left flow state")
			}
		})
	}
}

// DNS is never classified: a query is arbitrary bytes that can have the shape of a tracker announce or a uTP SYN.
func TestDNSPortsAreNeverClassified(t *testing.T) {
	syn := utpPacket(torrentguard.UTPSyn)
	for _, test := range []struct {
		name    string
		payload []byte
		port    uint16
		hit     bool
	}{
		{"EDNS query to DNS", dnsQueryWithEDNSCookie(), 53, false},
		{"EDNS query to a high port", dnsQueryWithEDNSCookie(), 51413, false},
		{"uTP SYN shape to DNS", syn, 53, false},
		{"uTP SYN shape to mDNS", syn, 5353, false},
		{"uTP SYN to a tracker port", syn, 51413, true},
		{"tracker connect to DNS", trackerConnectRequest(), 53, false},
		{"tracker connect to a tracker port", trackerConnectRequest(), 6969, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, hit := classifyRaw(newFlowTracker(), makeUDP4(clientIP, peerIP, 55000, test.port, test.payload), "mgawg51820", time.Unix(4000, 0))
			if hit != test.hit {
				t.Fatalf("classified = %v, want %v", hit, test.hit)
			}
		})
	}
}

// dnsQueryWithEDNSCookie is a plain 98-byte DNS query (ID 0x1234, one question, an EDNS OPT record with a COOKIE option).
// Its bytes have the exact layout of a BEP 15 announce.
func dnsQueryWithEDNSCookie() []byte {
	packet := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 1}
	for _, label := range []int{20, 20, 15} {
		packet = append(packet, byte(label))
		for i := 0; i < label; i++ {
			packet = append(packet, 'a'+byte(i%26))
		}
	}
	packet = append(packet, 0, 0, 1, 0, 1)
	packet = append(packet, 0, 0, 41, 0x04, 0xd0, 0, 0, 0, 0, 0, 12)
	return append(packet, 0, 10, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8)
}

func trackerConnectRequest() []byte {
	packet := make([]byte, 16)
	binary.BigEndian.PutUint64(packet[:8], 0x41727101980)
	binary.BigEndian.PutUint32(packet[8:12], uint32(torrentguard.UDPTrackerConnect))
	binary.BigEndian.PutUint32(packet[12:16], 7)
	return packet
}

func utpPacket(packetType torrentguard.UTPType) []byte {
	packet := make([]byte, 20)
	packet[0] = byte(packetType<<4) | 1
	if packetType == torrentguard.UTPSyn {
		binary.BigEndian.PutUint32(packet[4:8], 1) // A client SYN carries its send timestamp.
	}
	return packet
}

func makeTCP4(source, destination string, sourcePort, destinationPort uint16, seq uint32, flags uint8, payload []byte) []byte {
	packet := make([]byte, 20+20+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = protocolTCP
	copy(packet[12:16], netip.MustParseAddr(source).AsSlice())
	copy(packet[16:20], netip.MustParseAddr(destination).AsSlice())
	tcp := packet[20:]
	binary.BigEndian.PutUint16(tcp[:2], sourcePort)
	binary.BigEndian.PutUint16(tcp[2:4], destinationPort)
	binary.BigEndian.PutUint32(tcp[4:8], seq)
	tcp[12] = 5 << 4
	tcp[13] = flags
	copy(tcp[20:], payload)
	return packet
}

func makeUDP4(source, destination string, sourcePort, destinationPort uint16, payload []byte) []byte {
	packet := make([]byte, 20+8+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8] = 64
	packet[9] = protocolUDP
	copy(packet[12:16], netip.MustParseAddr(source).AsSlice())
	copy(packet[16:20], netip.MustParseAddr(destination).AsSlice())
	udp := packet[20:]
	binary.BigEndian.PutUint16(udp[:2], sourcePort)
	binary.BigEndian.PutUint16(udp[2:4], destinationPort)
	binary.BigEndian.PutUint16(udp[4:6], uint16(len(udp)))
	copy(udp[8:], payload)
	return packet
}
