package torrentguard

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

func TestFalsePositiveHarness(t *testing.T) {
	rng := rand.New(rand.NewPCG(0x6d69737467617465, 0x746f7272656e74))
	counts := map[string]int{}
	check := func(generator string, packet []byte, ports ...uint16) {
		counts[generator]++
		for _, port := range ports {
			if protocol, evidence, ok := ClassifyClientUDPRequest(packet, port); ok {
				t.Fatalf("%s sample %d matched dstPort=%d as %s/%s: %x", generator, counts[generator], port, protocol, evidence, packet)
			}
		}
	}
	fill := func(packet []byte) {
		for i := range packet {
			packet[i] = byte(rng.Uint32())
		}
	}

	for size := 1; size <= 1500; size++ {
		for range 40 {
			packet := make([]byte, size)
			fill(packet)
			check("random", packet, 0, 6881)
		}
	}
	for size := 20; size <= 1350; size++ {
		for range 40 {
			packet := make([]byte, size)
			fill(packet)
			packet[0] = 0x40 | byte(rng.Uint32()&0x3f)
			check("quic-short", packet, 0, 443)
		}
	}
	for size := 1200; size <= 1350; size++ {
		check("quic-initial", quicInitial(rng, size), 0, 443)
	}

	for channel := uint16(0x4000); channel <= 0x4fff; channel++ {
		check("turn-rtp", turnChannelData(channel, rtpPacket(rng)), 0, 3478)
		if channel&1 == 0 {
			check("turn-dtls-clienthello", turnChannelData(channel, dtlsClientHello(rng)), 0, 3478)
		} else {
			check("turn-dtls-hello-verify", turnChannelData(channel, dtlsHelloVerify(rng)), 0, 3478)
		}
	}

	for i := 0; i < 128; i++ {
		ike := ikeSAInit(rng)
		check("ike-sa-init", ike, 0, 500)
		check("ike-natt-sa-init", append([]byte{0, 0, 0, 0}, ike...), 0, 4500)
	}
	for size := 17; size <= 512; size++ {
		id := uint16(0x4100 | (size-17)&0xff)
		for _, answer := range []bool{false, true} {
			check("dns-no-edns", dnsMessage(size, id, answer, false), 0, 853, 5355)
		}
		if size == 40 || size >= 44 {
			for _, answer := range []bool{false, true} {
				check("dns-edns-cookie", dnsMessage(size, id, answer, true), 0, 853, 5355)
			}
		}
	}

	for sample := 0; sample < 64; sample++ {
		for typ, size := range []int{148, 92, 64, 32} {
			packet := make([]byte, size)
			binary.LittleEndian.PutUint32(packet[:4], uint32(typ+1))
			fill(packet[4:])
			packet[1], packet[2], packet[3] = 0, 0, 0
			check("wireguard", packet, 0, 51820)
		}
	}
	for i := 0; i < 128; i++ {
		packet := stunBindingRequest(rng)
		check("stun", packet, 0, 3478)
	}
	for _, first := range []byte{0x23, 0x1b, 0xe3} {
		for i := 0; i < 32; i++ {
			packet := make([]byte, 48)
			fill(packet)
			packet[0] = first
			check("ntp", packet, 0, 123)
		}
	}
	for _, first := range []byte{0x38, 0x48, 0x49, 0x4a, 0x4b, 0x4c, 0x4d, 0x4e, 0x4f, 0x50} {
		for i := 0; i < 16; i++ {
			packet := make([]byte, 40)
			packet[0] = first
			fill(packet[1:]) // includes a varied OpenVPN session ID
			check("openvpn", packet, 0, 1194)
		}
	}
	for _, size := range []int{20, 30, 40, 64, 96} {
		packet := make([]byte, size)
		packet[0] = 0x41
		if size == 30 {
			packet[1], packet[20], packet[21] = 2, 0, 8
		}
		check("game-zero-runs", packet, 0, 19132)
	}

	t.Logf("false-positive harness samples: %s", formatSampleCounts(counts))
}

func TestPositiveTorrentFixtures(t *testing.T) {
	tracker := make([]byte, 16)
	binary.BigEndian.PutUint64(tracker[:8], udpTrackerConnectionMagic)
	for _, port := range []uint16{0, 6969} {
		if protocol, evidence, ok := ClassifyClientUDPRequest(tracker, port); !ok || protocol != ProtocolBitTorrentTracker || evidence != EvidenceTrackerConnect {
			t.Fatalf("tracker connect to port %d = %q/%q, %v", port, protocol, evidence, ok)
		}
	}

	for _, packet := range [][]byte{libtorrentSyn(), libutpSyn()} {
		for _, port := range []uint16{0, 6881} {
			if protocol, evidence, ok := ClassifyClientUDPRequest(packet, port); !ok || protocol != ProtocolBitTorrentUTP || evidence != EvidenceUTPSyn {
				t.Fatalf("%d-byte uTP SYN to port %d = %q/%q, %v", len(packet), port, protocol, evidence, ok)
			}
		}
	}

	for _, method := range []string{"ping", "find_node", "get_peers", "announce_peer"} {
		for _, tx := range []string{"aa", "pn\x00\x01"} {
			packet := dhtQuery(method, tx)
			for _, port := range []uint16{0, 6881} {
				if protocol, evidence, ok := ClassifyClientUDPRequest(packet, port); !ok || protocol != ProtocolBitTorrentDHT || evidence != EvidenceDHTQuery {
					t.Fatalf("DHT %s with %d-byte transaction ID to port %d = %q/%q, %v; packet=%x", method, len(tx), port, protocol, evidence, ok, packet)
				}
			}
		}
	}
	if !HasBitTorrentHandshake([]byte("\x13BitTorrent protocol")) {
		t.Fatal("TCP BitTorrent handshake was not recognized")
	}
}

func TestClassifyRejectsOtherUTPSynShapes(t *testing.T) {
	valid := libtorrentSyn()
	cases := map[string][]byte{
		"ack number":            mutatePacket(valid, 18, 0, 1),
		"zero timestamp":        append([]byte{0x41}, make([]byte, 19)...),
		"extension 1":           appendUTPExtension(1, 4),
		"two extension records": appendTwoUTPExtensions(),
		"uTP DATA extension bits": func() []byte {
			packet := append([]byte(nil), valid...)
			packet[0] = byte(UTPData<<4) | 1
			return packet
		}(),
	}
	badLength := libutpSyn()
	badLength[21] = 7
	cases["wrong extension length"] = badLength
	for name, packet := range cases {
		if protocol, evidence, ok := ClassifyClientUDPRequest(packet, 6881); ok {
			t.Errorf("%s was classified as %s/%s", name, protocol, evidence)
		}
	}
}

// libtorrentSyn is a SYN as libtorrent sends it: no extension, a send timestamp, a zero window, ack_nr 0.
func libtorrentSyn() []byte {
	packet := make([]byte, 20)
	packet[0] = byte(UTPSyn<<4) | 1
	binary.BigEndian.PutUint16(packet[2:4], 0x1234)
	binary.BigEndian.PutUint32(packet[4:8], 1)
	binary.BigEndian.PutUint16(packet[16:18], 7)
	return packet
}

// libutpSyn is a SYN as libutp sends it: one 8-byte extension-bits record and a receive window.
func libutpSyn() []byte {
	packet := make([]byte, 30)
	copy(packet, libtorrentSyn())
	packet[1] = 2
	binary.BigEndian.PutUint32(packet[12:16], 1<<20)
	packet[20], packet[21] = 0, 8
	return packet
}

func mutatePacket(packet []byte, offset, old, value byte) []byte {
	copyOfPacket := append([]byte(nil), packet...)
	if copyOfPacket[offset] == old {
		copyOfPacket[offset] = value
	}
	return copyOfPacket
}

func appendUTPExtension(extension byte, length byte) []byte {
	packet := make([]byte, 20+2+int(length))
	copy(packet, libtorrentSyn())
	packet[1] = extension
	packet[20], packet[21] = 0, length
	return packet
}

func appendTwoUTPExtensions() []byte {
	packet := make([]byte, 24)
	copy(packet, libtorrentSyn())
	packet[1] = 2
	packet[20], packet[21] = 3, 2
	packet[22], packet[23] = 0, 0
	return packet
}

func dhtQuery(method, tx string) []byte {
	args := "2:id20:01234567890123456789"
	switch method {
	case "find_node":
		args += "6:target20:01234567890123456789"
	case "get_peers":
		args += "9:info_hash20:01234567890123456789"
	case "announce_peer":
		args += "9:info_hash20:012345678901234567894:porti6881e5:token4:abcd"
	}
	return []byte("d1:ad" + args + "e1:q" + strconv.Itoa(len(method)) + ":" + method + "1:t" + strconv.Itoa(len(tx)) + ":" + tx + "1:v4:TR\x03\x001:y1:qe")
}

func quicInitial(rng *rand.Rand, size int) []byte {
	packet := make([]byte, size)
	packet[0] = 0xc0 | byte(rng.Uint32()&0x0f) // long-header Initial, with randomized low reserved/PN-length bits
	binary.BigEndian.PutUint32(packet[1:5], 1)
	offset := 5
	packet[offset] = 8
	offset++
	fillRand(rng, packet[offset:offset+8])
	offset += 8
	packet[offset] = 8
	offset++
	fillRand(rng, packet[offset:offset+8])
	offset += 8
	packet[offset] = 0 // empty token, encoded as a QUIC varint
	offset++
	payloadLength := len(packet) - offset - 2
	binary.BigEndian.PutUint16(packet[offset:offset+2], uint16(0x4000|payloadLength))
	offset += 2
	packet[offset] = byte(rng.Uint32()) // packet number and protected payload are opaque here
	fillRand(rng, packet[offset+1:])
	return packet
}

func rtpPacket(rng *rand.Rand) []byte {
	packet := make([]byte, 12+64)
	packet[0], packet[1] = 0x80, 96
	binary.BigEndian.PutUint16(packet[2:4], uint16(rng.Uint32()))
	// Leave the RTP timestamp at zero, a common value in packet captures and fixtures.
	binary.BigEndian.PutUint32(packet[8:12], rng.Uint32())
	fillRand(rng, packet[12:])
	return packet
}

func turnChannelData(channel uint16, payload []byte) []byte {
	packet := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint16(packet[:2], channel)
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(payload)))
	copy(packet[4:], payload)
	return packet
}

func dtlsClientHello(rng *rand.Rand) []byte {
	body := []byte{0xfe, 0xfd}
	random := make([]byte, 32)
	fillRand(rng, random)
	body = append(body, random...)
	body = append(body, 0, 0, 0, 2, 0xc0, 0x2f, 1, 0) // session, cookie, cipher suites, compression
	return dtlsRecord(1, body)
}

func dtlsHelloVerify(rng *rand.Rand) []byte {
	cookie := make([]byte, 20)
	fillRand(rng, cookie)
	body := append([]byte{0xfe, 0xfd, byte(len(cookie))}, cookie...)
	return dtlsRecord(3, body)
}

func dtlsRecord(messageType byte, body []byte) []byte {
	handshake := make([]byte, 12+len(body))
	handshake[0] = messageType
	putUint24(handshake[1:4], uint32(len(body)))
	binary.BigEndian.PutUint16(handshake[4:6], 1) // small handshake sequence number
	putUint24(handshake[9:12], uint32(len(body)))
	copy(handshake[12:], body)
	record := make([]byte, 13+len(handshake))
	record[0], record[1], record[2] = 22, 0xfe, 0xfd
	record[10] = 1 // small record sequence number, with epoch zero
	binary.BigEndian.PutUint16(record[11:13], uint16(len(handshake)))
	copy(record[13:], handshake)
	return record
}

func putUint24(dst []byte, value uint32) {
	dst[0], dst[1], dst[2] = byte(value>>16), byte(value>>8), byte(value)
}

func ikeSAInit(rng *rand.Rand) []byte {
	packet := make([]byte, 160)
	fillRand(rng, packet[:8]) // random initiator SPI
	packet[16], packet[17], packet[18], packet[19] = 33, 0x20, 34, 0x08
	binary.BigEndian.PutUint32(packet[20:24], 0) // message ID
	binary.BigEndian.PutUint32(packet[24:28], uint32(len(packet)))
	packet[28], packet[29] = 40, 0 // SA payload is followed by KE.
	binary.BigEndian.PutUint16(packet[30:32], 24)
	packet[32], packet[33] = 0, 0 // last proposal
	binary.BigEndian.PutUint16(packet[34:36], 20)
	packet[36], packet[37], packet[38], packet[39] = 1, 1, 0, 1
	packet[40], packet[41] = 0, 0 // last transform
	binary.BigEndian.PutUint16(packet[42:44], 12)
	packet[44], packet[45] = 1, 0                 // encryption transform
	binary.BigEndian.PutUint16(packet[46:48], 12) // AES-CBC
	binary.BigEndian.PutUint16(packet[48:50], 0x800e)
	binary.BigEndian.PutUint16(packet[50:52], 128) // key length
	packet[52], packet[53] = 40, 0                 // KE payload is followed by Nonce.
	binary.BigEndian.PutUint16(packet[54:56], 72)
	binary.BigEndian.PutUint16(packet[56:58], 19) // ECP-256 DH group
	fillRand(rng, packet[60:124])
	packet[124], packet[125] = 0, 0
	binary.BigEndian.PutUint16(packet[126:128], 36)
	fillRand(rng, packet[128:])
	return packet
}

func dnsMessage(size int, id uint16, answer, edns bool) []byte {
	packet := make([]byte, size)
	binary.BigEndian.PutUint16(packet[:2], id)
	if answer {
		binary.BigEndian.PutUint16(packet[2:4], 0x8180)
	} else {
		binary.BigEndian.PutUint16(packet[2:4], 0x0100)
	}
	if edns {
		binary.BigEndian.PutUint16(packet[10:12], 1)
		packet[12], packet[13] = 0, 0 // root question, type A and class IN
		binary.BigEndian.PutUint16(packet[14:16], 1)
		binary.BigEndian.PutUint16(packet[16:18], 1)
		cursor := 17
		packet[cursor] = 0
		binary.BigEndian.PutUint16(packet[cursor+1:cursor+3], 41)
		binary.BigEndian.PutUint16(packet[cursor+3:cursor+5], 1232)
		rdataLength := size - 28
		binary.BigEndian.PutUint16(packet[cursor+9:cursor+11], uint16(rdataLength))
		cursor += 11
		binary.BigEndian.PutUint16(packet[cursor:cursor+2], 10)
		binary.BigEndian.PutUint16(packet[cursor+2:cursor+4], 8)
		for i := 0; i < 8; i++ {
			packet[cursor+4+i] = byte(i + 1)
		}
		cursor += 12
		if size > 40 {
			binary.BigEndian.PutUint16(packet[cursor:cursor+2], 12)
			binary.BigEndian.PutUint16(packet[cursor+2:cursor+4], uint16(size-44))
		}
		return packet
	}

	// Build valid root/name questions to fill each packet. A length-18 DNS message cannot hold a complete question, so that
	// one sample is a deliberately truncated header with its question count set.
	questionCount, firstNameLength := 0, 0
	for count := 1; count <= 100; count++ {
		nameLength := size - 11 - 5*count
		if nameLength == 1 || nameLength >= 3 && nameLength <= 255 {
			questionCount, firstNameLength = count, nameLength
			break
		}
	}
	if questionCount == 0 {
		binary.BigEndian.PutUint16(packet[4:6], 1)
		packet[12] = 0
		return packet
	}
	binary.BigEndian.PutUint16(packet[4:6], uint16(questionCount))
	cursor := 12
	for i := 0; i < questionCount; i++ {
		if i == 0 {
			name := dnsName(firstNameLength)
			copy(packet[cursor:], name)
			cursor += len(name)
		} else {
			packet[cursor] = 0
			cursor++
		}
		binary.BigEndian.PutUint16(packet[cursor:cursor+2], 1)
		binary.BigEndian.PutUint16(packet[cursor+2:cursor+4], 1)
		cursor += 4
	}
	return packet
}

func dnsName(wireLength int) []byte {
	if wireLength == 1 {
		return []byte{0}
	}
	labelCount := (wireLength - 2 + 62) / 63
	remainingData := wireLength - labelCount - 1
	name := make([]byte, 0, wireLength)
	for i := 0; i < labelCount; i++ {
		labelsLeft := labelCount - i - 1
		labelLength := remainingData - labelsLeft
		if labelLength > 63 {
			labelLength = 63
		}
		name = append(name, byte(labelLength))
		for j := 0; j < labelLength; j++ {
			name = append(name, byte('a'+(i+j)%26))
		}
		remainingData -= labelLength
	}
	return append(name, 0)
}

func stunBindingRequest(rng *rand.Rand) []byte {
	packet := make([]byte, 20)
	binary.BigEndian.PutUint16(packet[:2], 1)
	binary.BigEndian.PutUint32(packet[4:8], 0x2112a442)
	fillRand(rng, packet[8:])
	return packet
}

func formatSampleCounts(counts map[string]int) string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

func fillRand(rng *rand.Rand, packet []byte) {
	for i := range packet {
		packet[i] = byte(rng.Uint32())
	}
}
