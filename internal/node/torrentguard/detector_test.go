package torrentguard

import (
	"encoding/binary"
	"testing"
)

func TestTCPHandshakeDetection(t *testing.T) {
	const signature = "\x13BitTorrent protocol"

	if !HasBitTorrentHandshake([]byte(signature)) {
		t.Fatal("signature-only data was not recognized")
	}
	if HasBitTorrentHandshake([]byte("\x13BitTorrent protocoX")) {
		t.Fatal("near-match was recognized")
	}
	if HasBitTorrentHandshake([]byte("ordinary TCP data")) {
		t.Fatal("ordinary TCP data was recognized")
	}
}

func TestParseUDPTrackerRequest(t *testing.T) {
	connect := make([]byte, 16)
	binary.BigEndian.PutUint64(connect[:8], udpTrackerConnectionMagic)
	binary.BigEndian.PutUint32(connect[8:12], uint32(UDPTrackerConnect))
	binary.BigEndian.PutUint32(connect[12:16], 0x01020304)
	parsed, ok := ParseUDPTrackerRequest(connect)
	if !ok || parsed.Action != UDPTrackerConnect || parsed.TransactionID != 0x01020304 {
		t.Fatalf("connect request parse = %#v, %v", parsed, ok)
	}
	wrongMagic := append([]byte(nil), connect...)
	wrongMagic[0] ^= 1
	if _, ok := ParseUDPTrackerRequest(wrongMagic); ok {
		t.Fatal("connect request with wrong magic was recognized")
	}
	if _, ok := ParseUDPTrackerRequest(connect[:15]); ok {
		t.Fatal("truncated connect request was recognized")
	}

	announce := make([]byte, 98)
	binary.BigEndian.PutUint32(announce[8:12], uint32(UDPTrackerAnnounce))
	binary.BigEndian.PutUint32(announce[12:16], 17)
	binary.BigEndian.PutUint32(announce[80:84], 2)
	binary.BigEndian.PutUint32(announce[92:96], ^uint32(0)) // num_want = -1
	binary.BigEndian.PutUint16(announce[96:98], 6881)
	parsed, ok = ParseUDPTrackerRequest(announce)
	if !ok || parsed.Action != UDPTrackerAnnounce || parsed.TransactionID != 17 {
		t.Fatalf("announce request parse = %#v, %v", parsed, ok)
	}
	badEvent := append([]byte(nil), announce...)
	binary.BigEndian.PutUint32(badEvent[80:84], 4)
	if _, ok := ParseUDPTrackerRequest(badEvent); ok {
		t.Fatal("announce request with invalid event was recognized")
	}
	if _, ok := ParseUDPTrackerRequest(append(announce, 0)); ok {
		t.Fatal("announce request with an invalid length was recognized")
	}

	scrape := make([]byte, 36)
	binary.BigEndian.PutUint32(scrape[8:12], uint32(UDPTrackerScrape))
	binary.BigEndian.PutUint32(scrape[12:16], 19)
	parsed, ok = ParseUDPTrackerRequest(scrape)
	if !ok || parsed.Action != UDPTrackerScrape || parsed.TransactionID != 19 {
		t.Fatalf("scrape request parse = %#v, %v", parsed, ok)
	}
	if _, ok := ParseUDPTrackerRequest(scrape[:35]); ok {
		t.Fatal("scrape request without a complete info hash was recognized")
	}
	if _, ok := ParseUDPTrackerRequest([]byte("ordinary UDP payload")); ok {
		t.Fatal("ordinary UDP payload was recognized as a tracker request")
	}
}

func TestParseUDPTrackerResponseRequiresCorrelation(t *testing.T) {
	response := make([]byte, 16)
	binary.BigEndian.PutUint32(response[:4], uint32(UDPTrackerConnect))
	binary.BigEndian.PutUint32(response[4:8], 7)
	parsed, ok := ParseUDPTrackerResponse(response, UDPTrackerConnect, 7)
	if !ok || parsed.Action != UDPTrackerConnect || parsed.TransactionID != 7 {
		t.Fatalf("connect response parse = %#v, %v", parsed, ok)
	}
	if _, ok := ParseUDPTrackerResponse(response, UDPTrackerConnect, 8); ok {
		t.Fatal("response with a different transaction ID was recognized")
	}
	if _, ok := ParseUDPTrackerResponse(response, UDPTrackerAnnounce, 7); ok {
		t.Fatal("response with a different request action was recognized")
	}

	announce := make([]byte, 26) // action + transaction + three counters + one compact peer
	binary.BigEndian.PutUint32(announce[:4], uint32(UDPTrackerAnnounce))
	binary.BigEndian.PutUint32(announce[4:8], 12)
	if _, ok := ParseUDPTrackerResponse(announce, UDPTrackerAnnounce, 12); !ok {
		t.Fatal("valid announce response was rejected")
	}
	if _, ok := ParseUDPTrackerResponse(append(announce, 0), UDPTrackerAnnounce, 12); ok {
		t.Fatal("announce response with partial compact peer was recognized")
	}

	errorResponse := make([]byte, 9)
	binary.BigEndian.PutUint32(errorResponse[:4], uint32(UDPTrackerError))
	binary.BigEndian.PutUint32(errorResponse[4:8], 12)
	errorResponse[8] = 'x'
	parsed, ok = ParseUDPTrackerResponse(errorResponse, UDPTrackerAnnounce, 12)
	if !ok || !parsed.IsError {
		t.Fatalf("correlated error response parse = %#v, %v", parsed, ok)
	}
}

func TestParseKRPCMessage(t *testing.T) {
	const query = "d1:ad2:id20:01234567890123456789e1:q4:ping1:t2:aa1:y1:qe"
	parsed, ok := ParseKRPCMessage([]byte(query))
	if !ok || parsed.Type != KRPCQuery || parsed.Method != "ping" || parsed.TransactionID != "aa" {
		t.Fatalf("query parse = %#v, %v", parsed, ok)
	}

	const response = "d1:rd2:id20:01234567890123456789e1:t2:aa1:y1:re"
	parsed, ok = ParseKRPCMessage([]byte(response))
	if !ok || parsed.Type != KRPCResponse {
		t.Fatalf("response parse = %#v, %v", parsed, ok)
	}

	const failure = "d1:eli201e13:generic errore1:t2:aa1:y1:ee"
	parsed, ok = ParseKRPCMessage([]byte(failure))
	if !ok || parsed.Type != KRPCError || parsed.ErrorCode != 201 {
		t.Fatalf("error parse = %#v, %v", parsed, ok)
	}

	const announce = "d1:ad2:id20:012345678901234567899:info_hash20:012345678901234567894:porti6881e5:token4:abcde1:q13:announce_peer1:t2:bb1:y1:qe"
	if _, ok := ParseKRPCMessage([]byte(announce)); !ok {
		t.Fatal("valid announce_peer query was rejected")
	}

	for _, malformed := range []string{
		"d1:y1:qe", // broad dictionary prefix without KRPC fields
		"d1:ad2:id19:short e1:q4:ping1:t2:aa1:y1:qe",
		"d1:ad2:id20:01234567890123456789e1:q4:ping1:t1:a1:y1:qe",           // wrong tx length
		"d1:ad2:id20:01234567890123456789e1:q4:ping1:t2:aa1:y1:q",           // truncated
		"d1:ad2:id20:01234567890123456789e1:q4:ping1:q4:ping1:t2:aa1:y1:qe", // duplicate key
		"d1:ad2:id20:01234567890123456789e1:q4:ping1:t2:aa1:y1:qeX",         // trailing bytes
		"d1:eli200e13:generic errore1:t2:aa1:y1:ee",                         // unknown error code
	} {
		if _, ok := ParseKRPCMessage([]byte(malformed)); ok {
			t.Errorf("malformed KRPC payload was recognized: %q", malformed)
		}
	}
}

func TestParseUTPHeader(t *testing.T) {
	packet := make([]byte, 21)
	packet[0] = byte(UTPData<<4) | 1
	packet[2], packet[3] = 0x12, 0x34
	binary.BigEndian.PutUint32(packet[4:8], 1000)    // timestamp_microseconds
	binary.BigEndian.PutUint32(packet[8:12], 77)     // timestamp_difference_microseconds
	binary.BigEndian.PutUint32(packet[12:16], 65536) // wnd_size
	packet[16], packet[17] = 0x00, 0x09              // seq_nr
	packet[18], packet[19] = 0x00, 0x05              // ack_nr
	packet[20] = 0xaa                                // DATA payload
	header, ok := ParseUTPHeader(packet)
	if !ok || header.Type != UTPData || header.ConnectionID != 0x1234 || header.Timestamp != 1000 || header.TimestampDiff != 77 ||
		header.WindowSize != 65536 || header.SequenceNumber != 9 || header.AckNumber != 5 || header.HeaderLength != 20 {
		t.Fatalf("DATA header parse = %#v, %v", header, ok)
	}

	state := make([]byte, 26)
	state[0] = byte(UTPState<<4) | 1
	state[1] = 1 // selective-ack extension
	state[20] = 0
	state[21] = 4
	state[22] = 1
	header, ok = ParseUTPHeader(state)
	if !ok || header.Type != UTPState || header.ExtensionCount != 1 || header.HeaderLength != 26 {
		t.Fatalf("STATE header parse = %#v, %v", header, ok)
	}

	badExtension := append([]byte(nil), state...)
	badExtension[21] = 3 // selective-ack extension length must be a non-zero multiple of four
	if _, ok := ParseUTPHeader(badExtension); ok {
		t.Fatal("malformed extension was recognized")
	}
	badVersion := append([]byte(nil), packet...)
	badVersion[0] = byte(UTPData << 4)
	if _, ok := ParseUTPHeader(badVersion); ok {
		t.Fatal("uTP header with unsupported version was recognized")
	}
	badType := append([]byte(nil), packet...)
	badType[0] = 0x51
	if _, ok := ParseUTPHeader(badType); ok {
		t.Fatal("uTP header with unsupported type was recognized")
	}
	stateWithPayload := append([]byte(nil), state[:20]...)
	stateWithPayload[0] = byte(UTPState<<4) | 1
	stateWithPayload = append(stateWithPayload, 0xaa)
	if _, ok := ParseUTPHeader(stateWithPayload); ok {
		t.Fatal("STATE packet payload was accepted")
	}
	if _, ok := ParseUTPHeader(packet[:19]); ok {
		t.Fatal("truncated uTP header was recognized")
	}
}

func TestClassifyClientUDPRequestNamesWhatMatched(t *testing.T) {
	connect := make([]byte, 16)
	binary.BigEndian.PutUint64(connect[:8], udpTrackerConnectionMagic)
	syn := make([]byte, 20)
	syn[0] = byte(UTPSyn<<4) | 1
	for name, want := range map[string]struct {
		packet   []byte
		protocol Protocol
		evidence Evidence
	}{
		"connect": {connect, ProtocolBitTorrentTracker, EvidenceTrackerConnect},
		"DHT":     {[]byte("d1:ad2:id20:01234567890123456789e1:q4:ping1:t2:aa1:y1:qe"), ProtocolBitTorrentDHT, EvidenceDHTQuery},
		"uTP SYN": {syn, ProtocolBitTorrentUTP, EvidenceUTPSyn},
	} {
		for _, port := range []uint16{0, 6881, 6969, 80, 443} {
			protocol, evidence, ok := ClassifyClientUDPRequest(want.packet, port)
			if !ok || protocol != want.protocol || evidence != want.evidence {
				t.Errorf("%s to port %d = %q, %q, %v; want %q, %q", name, port, protocol, evidence, ok, want.protocol, want.evidence)
			}
		}
		for _, port := range []uint16{53, 5353} {
			if protocol, evidence, ok := ClassifyClientUDPRequest(want.packet, port); ok {
				t.Errorf("%s to DNS port %d was classified: %q, %q", name, port, protocol, evidence)
			}
		}
	}
	if protocol, evidence, ok := ClassifyClientUDPRequest([]byte("ordinary UDP payload"), 6881); ok || protocol != "" || evidence != "" {
		t.Errorf("ordinary payload = %q, %q, %v", protocol, evidence, ok)
	}
}

// dnsQueryWithEDNSCookie is a plain 98-byte DNS query: ID 0x1234, RD, one question (a 59-byte name, A/IN) and an EDNS OPT
// record (UDP size 1232, no flags) with a COOKIE option. Its NSCOUNT/ARCOUNT read as a BEP 15 announce action (1), its OPT
// TTL as the event (0) and the cookie as num_want: the exact 98-byte announce layout, by chance.
func dnsQueryWithEDNSCookie() []byte {
	packet := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 1}
	for _, label := range []int{20, 20, 15} {
		packet = append(packet, byte(label))
		for i := 0; i < label; i++ {
			packet = append(packet, 'a'+byte(i%26))
		}
	}
	packet = append(packet, 0, 0, 1, 0, 1)                           // root, type A, class IN
	packet = append(packet, 0, 0, 41, 0x04, 0xd0, 0, 0, 0, 0, 0, 12) // OPT: root name, type, size 1232, TTL 0, rdlen 12
	packet = append(packet, 0, 10, 0, 8, 1, 2, 3, 4, 5, 6, 7, 8)     // COOKIE: code 10, length 8, 8 bytes
	return packet
}

// A DNS query without EDNS and an ID that starts with 0x41: version 1, type SYN, no extension, and NSCOUNT/ARCOUNT zero
// as the timestamp difference. "ab." is 20 bytes, the size of a bare uTP header.
func dnsQueryReadingAsUTPSyn() []byte {
	return []byte{0x41, 0x00, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 2, 'a', 'b', 0, 0, 1, 0, 1}
}

func TestDNSIsNeverClassifiedAsATorrent(t *testing.T) {
	dns := dnsQueryWithEDNSCookie()
	if len(dns) != 98 {
		t.Fatalf("test DNS query has %d bytes, want 98", len(dns))
	}
	// The layout alone is a valid announce: that is the false alarm. It is no longer evidence.
	if parsed, ok := ParseUDPTrackerRequest(dns); !ok || parsed.Action != UDPTrackerAnnounce {
		t.Fatalf("the test query no longer has the announce layout: %#v, %v", parsed, ok)
	}
	syn := dnsQueryReadingAsUTPSyn()
	if header, ok := ParseUTPHeader(syn); !ok || header.Type != UTPSyn || header.TimestampDiff != 0 {
		t.Fatalf("the test query no longer reads as a uTP SYN: %#v, %v", header, ok)
	}
	// The announce shape is no evidence on any port. The SYN shape is, so the DNS ports are what keep it out.
	for _, port := range []uint16{0, 53, 5353, 6881} {
		if protocol, evidence, ok := ClassifyClientUDPRequest(dns, port); ok {
			t.Errorf("EDNS query to port %d was classified: %q, %q", port, protocol, evidence)
		}
	}
	if protocol, ok := DetectClientUDPRequest(dns); ok {
		t.Errorf("EDNS query was detected without a port: %q", protocol)
	}
	for _, port := range []uint16{53, 5353} {
		if protocol, evidence, ok := ClassifyClientUDPRequest(syn, port); ok {
			t.Errorf("query with ID 0x41xx to port %d was classified: %q, %q", port, protocol, evidence)
		}
	}
	// A real tracker announce is not enough alone either: only the connect handshake is evidence.
	announce := make([]byte, 98)
	binary.BigEndian.PutUint32(announce[8:12], uint32(UDPTrackerAnnounce))
	scrape := make([]byte, 36)
	binary.BigEndian.PutUint32(scrape[8:12], uint32(UDPTrackerScrape))
	for name, packet := range map[string][]byte{"announce": announce, "scrape": scrape} {
		if protocol, evidence, ok := ClassifyClientUDPRequest(packet, 6969); ok {
			t.Errorf("%s alone was classified: %q, %q", name, protocol, evidence)
		}
	}
}

func TestDetectClientUDPRequestRequiresOutboundSignatures(t *testing.T) {
	tracker := make([]byte, 16)
	binary.BigEndian.PutUint64(tracker[:8], udpTrackerConnectionMagic)
	query := []byte("d1:ad2:id20:01234567890123456789e1:q4:ping1:t2:aa1:y1:qe")
	response := []byte("d1:rd2:id20:01234567890123456789e1:t2:aa1:y1:re")
	state := make([]byte, 20)
	state[0] = byte(UTPState<<4) | 1
	// A SYN as libutp sends it: a timestamp and a window, nothing received yet (timestamp_difference 0), ack_nr 0.
	syn := make([]byte, 20)
	syn[0] = byte(UTPSyn<<4) | 1
	binary.BigEndian.PutUint16(syn[2:4], 0x5a5a)
	binary.BigEndian.PutUint32(syn[4:8], 0x01020304)
	binary.BigEndian.PutUint32(syn[12:16], 1<<20)
	binary.BigEndian.PutUint16(syn[16:18], 0x4242)

	for name, want := range map[string]struct {
		packet   []byte
		protocol Protocol
	}{
		"tracker":   {tracker, ProtocolBitTorrentTracker},
		"DHT query": {query, ProtocolBitTorrentDHT},
		"uTP SYN":   {syn, ProtocolBitTorrentUTP},
	} {
		if got, ok := DetectClientUDPRequest(want.packet); !ok || got != want.protocol {
			t.Errorf("%s = %q, %v; want %q", name, got, ok, want.protocol)
		}
	}

	// A QUIC short header (0b01xxxxxx, a random connection ID) that happens to read as "SYN, version 1, no extension".
	quic := []byte{0x41, 0x00, 0x9c, 0x3e, 0x71, 0x0d, 0xa2, 0x55, 0x18, 0xe4, 0x6b, 0x30, 0xc7, 0x02, 0x8f, 0x99, 0x24, 0xd1, 0x6e, 0x0b}
	// A WireGuard handshake initiation: type 1 and three zero bytes, a structurally valid uTP DATA header.
	wireguard := make([]byte, 148)
	wireguard[0] = 1
	binary.BigEndian.PutUint32(wireguard[4:8], 0xdeadbeef)
	synAfterReply := append([]byte(nil), syn...)
	binary.BigEndian.PutUint32(synAfterReply[8:12], 1500)
	for name, packet := range map[string][]byte{
		"DHT response":                    response,
		"uTP state":                       state,
		"QUIC short header":               quic,
		"WireGuard initiation":            wireguard,
		"SYN with a timestamp difference": synAfterReply,
		"ordinary":                        []byte("ordinary UDP payload"),
	} {
		if got, ok := DetectClientUDPRequest(packet); ok {
			t.Errorf("%s was classified as an outbound torrent request %q", name, got)
		}
	}
}
