package linux

import (
	"encoding/binary"
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

func TestTrackerBlocksOnlyDetectedExactFlowAndReverse(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(1000, 0)
	const (
		clientIP   = "10.0.0.2"
		peerIP     = "198.51.100.7"
		clientPort = 51000
		peerPort   = 51413
	)
	detections := 0
	report := func(d Detection) bool {
		detections++
		if d.Signature != torrentguard.ProtocolBitTorrentTCP || d.L4Protocol != "tcp" || d.SourceIP.String() != clientIP || d.DestinationIP.String() != peerIP || d.SourcePort != clientPort || d.DestinationPort != peerPort || d.TunnelIface != "mgawg51820" || d.TunnelIP.String() != clientIP {
			t.Errorf("detection = %#v", d)
		}
		return true
	}
	process := func(raw []byte, at time.Time) bool {
		return tracker.Process(raw, "mgawg51820", netip.MustParseAddr(clientIP), at, report)
	}

	if process(makeTCP4(clientIP, peerIP, clientPort, peerPort, 100, 0x02, nil), now) {
		t.Fatal("SYN without signature was dropped")
	}
	if process(makeTCP4(peerIP, clientIP, peerPort, clientPort, 700, 0x12, nil), now) {
		t.Fatal("SYN/ACK without signature was dropped")
	}
	signature := []byte("\x13BitTorrent protocol")
	if process(makeTCP4(clientIP, peerIP, clientPort, peerPort, 101, 0x18, signature[:9]), now) {
		t.Fatal("partial handshake was dropped")
	}
	if !process(makeTCP4(clientIP, peerIP, clientPort, peerPort, 110, 0x18, signature[9:]), now) {
		t.Fatal("positive handshake was not dropped")
	}
	if got := detections; got != 1 {
		t.Fatalf("detection count = %d, want one", got)
	}
	if !process(makeTCP4(peerIP, clientIP, peerPort, clientPort, 701, 0x10, []byte("reply")), now.Add(time.Second)) {
		t.Fatal("reverse direction was not dropped")
	}
	if process(makeTCP4(clientIP, peerIP, clientPort, peerPort+1, 110, 0x18, signature), now.Add(2*time.Second)) {
		t.Fatal("different destination port was collateral-blocked")
	}
}

func TestTrackerPreservesBlocksAndSeparatesAWGTunnels(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(1500, 0)
	client, peer := "10.0.0.2", "198.51.100.7"
	clientIP := netip.MustParseAddr(client)
	const clientPort, peerPort = 51000, 51413
	report := func(Detection) bool { return true }
	process := func(raw []byte, iface string) bool {
		return tracker.Process(raw, iface, clientIP, now, report)
	}
	if process(makeTCP4(client, peer, clientPort, peerPort, 100, 0x02, nil), "mgawg51820") {
		t.Fatal("SYN without signature was dropped")
	}
	signature := []byte("\x13BitTorrent protocol")
	if process(makeTCP4(client, peer, clientPort, peerPort, 101, 0x18, signature), "mgawg51820") != true {
		t.Fatal("torrent handshake was not blocked on the first tunnel")
	}
	tracker.retainInterfaces(map[string]struct{}{"mgawg51820": {}, "mgawg51821": {}})
	reverse := makeTCP4(peer, client, peerPort, clientPort, 700, 0x10, []byte("reply"))
	if !process(reverse, "mgawg51820") {
		t.Fatal("an active block was lost when another AWG interface was added")
	}

	// The same five-tuple on another AWG tunnel is a distinct flow and must not inherit the block.
	if process(makeTCP4(client, peer, clientPort, peerPort, 100, 0x02, nil), "mgawg51821") {
		t.Fatal("SYN on the second tunnel inherited the first tunnel's block")
	}
	if process(makeTCP4(peer, client, peerPort, clientPort, 700, 0x10, []byte("ordinary")), "mgawg51821") {
		t.Fatal("ordinary flow on the second tunnel inherited the first tunnel's block")
	}
}

func TestTCPReassemblyHandlesOrderedAndBoundedOutOfOrderSegments(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(2000, 0)
	client, peer := "10.0.0.2", "198.51.100.7"
	const clientPort, peerPort = 52000, 51413
	reported := false
	report := func(Detection) bool { reported = true; return true }
	process := func(raw []byte) bool {
		return tracker.Process(raw, "mgawg10000", netip.MustParseAddr(client), now, report)
	}
	if process(makeTCP4(client, peer, clientPort, peerPort, 300, 0x02, nil)) {
		t.Fatal("SYN was dropped")
	}
	signature := []byte("\x13BitTorrent protocol")
	if process(makeTCP4(client, peer, clientPort, peerPort, 301, 0x18, signature[:10])) {
		t.Fatal("first handshake chunk was dropped")
	}
	if process(makeTCP4(client, peer, clientPort, peerPort, 317, 0x18, signature[16:])) {
		t.Fatal("out-of-order chunk was dropped before the gap arrived")
	}
	if !process(makeTCP4(client, peer, clientPort, peerPort, 311, 0x18, signature[10:16])) {
		t.Fatal("filling the gap did not complete the handshake")
	}
	if !reported {
		t.Fatal("reassembled handshake did not report")
	}
}

func TestTCPGapBeyondBoundPasses(t *testing.T) {
	tracker := newFlowTracker()
	now := time.Unix(3000, 0)
	client, peer := "10.0.0.2", "198.51.100.7"
	reports := 0
	report := func(Detection) bool { reports++; return true }
	process := func(raw []byte) bool {
		return tracker.Process(raw, "mgawg10000", netip.MustParseAddr(client), now, report)
	}
	_ = process(makeTCP4(client, peer, 53000, 51413, 500, 0x02, nil))
	if process(makeTCP4(client, peer, 53000, 51413, 1000, 0x18, []byte("\x13BitTorrent protocol"))) {
		t.Fatal("packet after a large TCP gap was dropped")
	}
	if process(makeTCP4(client, peer, 53000, 51413, 501, 0x18, []byte("\x13BitTorrent protocol"))) {
		t.Fatal("ambiguous stream was detected after a gap")
	}
	if reports != 0 {
		t.Fatalf("reported %d ambiguous streams", reports)
	}
}

func TestUDPTrackerDHTQueryAndUTPSynAreFlowScoped(t *testing.T) {
	client, peer := "10.0.0.2", "198.51.100.7"
	clientAddr := netip.MustParseAddr(client)
	now := time.Unix(4000, 0)
	tests := []struct {
		name      string
		payload   []byte
		signature torrentguard.Protocol
		wantDrop  bool
	}{
		{name: "tracker", payload: trackerConnectRequest(), signature: torrentguard.ProtocolBitTorrentTracker, wantDrop: true},
		{name: "DHT query", payload: []byte("d1:ad2:id20:01234567890123456789e1:q4:ping1:t2:aa1:y1:qe"), signature: torrentguard.ProtocolBitTorrentDHT, wantDrop: true},
		{name: "DHT response", payload: []byte("d1:rd2:id20:01234567890123456789e1:t2:aa1:y1:re"), wantDrop: false},
		{name: "uTP SYN", payload: utpPacket(torrentguard.UTPSyn), signature: torrentguard.ProtocolBitTorrentUTP, wantDrop: true},
		{name: "uTP STATE", payload: utpPacket(torrentguard.UTPState), wantDrop: false},
		{name: "unknown", payload: []byte("ordinary UDP data"), wantDrop: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tracker := newFlowTracker()
			var detected Detection
			report := func(d Detection) bool { detected = d; return true }
			packet := makeUDP4(client, peer, 55000, 51413, test.payload)
			got := tracker.Process(packet, "mgawg51820", clientAddr, now, report)
			if got != test.wantDrop {
				t.Fatalf("first packet drop = %v, want %v", got, test.wantDrop)
			}
			if test.wantDrop {
				if detected.Signature != test.signature || detected.TunnelIP != clientAddr {
					t.Fatalf("detection = %#v", detected)
				}
				reverse := makeUDP4(peer, client, 51413, 55000, []byte("response"))
				if !tracker.Process(reverse, "mgawg51820", clientAddr, now.Add(time.Second), report) {
					t.Fatal("reverse UDP packet was not dropped")
				}
			}
		})
	}
}

func TestFlowStateExpiresAfterIdleRetention(t *testing.T) {
	tracker := newFlowTracker()
	start := time.Unix(5000, 0)
	client, peer := "10.0.0.2", "198.51.100.7"
	report := func(Detection) bool { return true }
	request := makeUDP4(client, peer, 55000, 6969, trackerConnectRequest())
	if !tracker.Process(request, "mgawg51820", netip.MustParseAddr(client), start, report) {
		t.Fatal("valid tracker request was not dropped")
	}
	if got := tracker.count; got != 1 {
		t.Fatalf("active flow count = %d, want 1", got)
	}
	reverse := makeUDP4(peer, client, 6969, 55000, []byte("response"))
	if tracker.Process(reverse, "mgawg51820", netip.MustParseAddr(client), start.Add(blockedFlowIdle+time.Second), report) {
		t.Fatal("expired reverse flow remained blocked")
	}
	if got := tracker.count; got != 0 {
		t.Fatalf("expired flow count = %d, want 0", got)
	}
}

func TestConfirmedFlowStaysBlockedWhenEventQueueIsFull(t *testing.T) {
	tracker := newFlowTracker()
	start := time.Unix(6000, 0)
	client, peer := "10.0.0.2", "198.51.100.7"
	clientIP := netip.MustParseAddr(client)
	calls := 0
	report := func(Detection) bool {
		calls++
		return calls > 1
	}
	request := makeUDP4(client, peer, 55000, 6969, trackerConnectRequest())
	if !tracker.Process(request, "mgawg51820", clientIP, start, report) {
		t.Fatal("confirmed tracker flow was passed when its event queue was full")
	}
	reverse := makeUDP4(peer, client, 6969, 55000, []byte("response"))
	if !tracker.Process(reverse, "mgawg51820", clientIP, start.Add(time.Second), report) {
		t.Fatal("reverse packet was passed while retrying the event")
	}
	if !tracker.Process(reverse, "mgawg51820", clientIP, start.Add(2*time.Second), report) {
		t.Fatal("reverse packet was passed after the event was enqueued")
	}
	if calls != 2 {
		t.Fatalf("event attempts = %d, want the initial attempt and one retry", calls)
	}
}

func TestUnreportedFlowIsRetriedWithoutAnotherPacket(t *testing.T) {
	tracker := newFlowTracker()
	start := time.Unix(7000, 0)
	client, peer := "10.0.0.2", "198.51.100.7"
	clientIP := netip.MustParseAddr(client)
	attempts := 0
	report := func(Detection) bool {
		attempts++
		return attempts > 1
	}
	request := makeUDP4(client, peer, 55000, 6969, trackerConnectRequest())
	if !tracker.Process(request, "mgawg51820", clientIP, start, report) {
		t.Fatal("confirmed tracker flow was passed")
	}
	if attempts != 1 || len(tracker.pendingReports) != 1 {
		t.Fatalf("initial report attempts=%d pending=%d, want 1 and 1", attempts, len(tracker.pendingReports))
	}

	tracker.RetryPendingReports(report)
	if attempts != 2 || len(tracker.pendingReports) != 0 {
		t.Fatalf("retry attempts=%d pending=%d, want 2 and 0", attempts, len(tracker.pendingReports))
	}
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
