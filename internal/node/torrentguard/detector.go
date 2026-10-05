// Package torrentguard contains bounded, dependency-free parsers for plaintext
// BitTorrent protocol signatures. A positive result is based on a validated
// protocol signature or wire structure; ports are never used as evidence.
//
// These parsers do not identify MSE/PE-encrypted BitTorrent, BitTorrent inside
// another VPN or proxy, or HTTPS web seeds. Unknown and malformed traffic must
// be allowed by callers. A validated uTP header alone is structural evidence
// only: DetectClientUDPRequest accepts nothing but a standalone SYN, and only
// what the client sends may be classified (a remote peer must not be able to
// frame a user with a crafted packet).
package torrentguard

import (
	"bytes"
	"encoding/binary"
	"strconv"
)

const (
	bitTorrentHandshakeSignature = "\x13BitTorrent protocol"
	maxUDPDatagramSize           = 65507
	maxBencodeDepth              = 16
	maxBencodeNodes              = 4096
	maxUTPExtensions             = 16
)

// HasBitTorrentHandshake reports whether data begins with the exact 20-byte
// plaintext BitTorrent handshake signature. data must start at the beginning
// of a TCP byte stream; it may contain only the signature or the rest of the
// handshake and subsequent stream data as well.
func HasBitTorrentHandshake(data []byte) bool {
	return len(data) >= len(bitTorrentHandshakeSignature) &&
		bytes.Equal(data[:len(bitTorrentHandshakeSignature)], []byte(bitTorrentHandshakeSignature))
}

// TCPHandshakeDetector incrementally checks the beginning of one reassembled
// TCP byte stream for the plaintext BitTorrent handshake signature. Feed each
// in-order, non-duplicate payload chunk once, starting at stream offset zero.
// It retains no packet data. A mismatch permanently rejects that stream; a
// match remains true on later calls. Use a fresh value for each TCP stream.
type TCPHandshakeDetector struct {
	seen     int
	matched  bool
	rejected bool
}

// Feed adds the next ordered TCP payload chunk and reports whether the stream
// has begun with the exact plaintext BitTorrent handshake signature.
func (d *TCPHandshakeDetector) Feed(chunk []byte) bool {
	if d.matched {
		return true
	}
	if d.rejected {
		return false
	}
	for _, b := range chunk {
		if b != bitTorrentHandshakeSignature[d.seen] {
			d.rejected = true
			return false
		}
		d.seen++
		if d.seen == len(bitTorrentHandshakeSignature) {
			d.matched = true
			return true
		}
	}
	return false
}

// UDPTrackerAction is a BEP 15 UDP tracker action code.
type UDPTrackerAction uint32

const (
	UDPTrackerConnect  UDPTrackerAction = 0
	UDPTrackerAnnounce UDPTrackerAction = 1
	UDPTrackerScrape   UDPTrackerAction = 2
	UDPTrackerError    UDPTrackerAction = 3
)

// Protocol identifies a positively parsed plaintext BitTorrent transport or
// UDP protocol. The values are stable event labels.
type Protocol string

const (
	ProtocolBitTorrentTCP     Protocol = "bittorrent_tcp"
	ProtocolBitTorrentDHT     Protocol = "bittorrent_dht"
	ProtocolBitTorrentUTP     Protocol = "bittorrent_utp"
	ProtocolBitTorrentTracker Protocol = "bittorrent_tracker"
)

// DetectClientUDPRequest recognizes client-to-network BitTorrent requests: call it only on datagrams the
// client sent. It avoids treating ordinary DHT replies or arbitrary uTP DATA/STATE packets as proof (every
// WireGuard handshake initiation, "01 00 00 00", is a structurally valid uTP DATA header). A new uTP flow
// is recognized only by its standalone SYN, whose timestamp_difference is zero (nothing was received yet):
// four zero bytes a QUIC short header or other random-looking datagram almost never has.
func DetectClientUDPRequest(packet []byte) (Protocol, bool) {
	if _, ok := ParseUDPTrackerRequest(packet); ok {
		return ProtocolBitTorrentTracker, true
	}
	if message, ok := ParseKRPCMessage(packet); ok && message.Type == KRPCQuery {
		return ProtocolBitTorrentDHT, true
	}
	if header, ok := ParseUTPHeader(packet); ok && header.Type == UTPSyn && header.TimestampDiff == 0 {
		return ProtocolBitTorrentUTP, true
	}
	return "", false
}

// UDPTrackerPacket contains the validated action and transaction ID from a
// BEP 15 packet. IsError is set only for a correlated error response.
type UDPTrackerPacket struct {
	Action        UDPTrackerAction
	TransactionID uint32
	IsError       bool
}

const udpTrackerConnectionMagic uint64 = 0x41727101980

// ParseUDPTrackerRequest recognizes structurally valid BEP 15 client requests.
// Connect requests require the protocol's 64-bit magic value. Announce and
// scrape requests have no fixed magic, so they are accepted only with their
// exact specified lengths and valid action-specific fields.
func ParseUDPTrackerRequest(packet []byte) (UDPTrackerPacket, bool) {
	if len(packet) < 16 || len(packet) > maxUDPDatagramSize {
		return UDPTrackerPacket{}, false
	}
	if len(packet) == 16 && binary.BigEndian.Uint64(packet[:8]) == udpTrackerConnectionMagic &&
		binary.BigEndian.Uint32(packet[8:12]) == uint32(UDPTrackerConnect) {
		return UDPTrackerPacket{
			Action:        UDPTrackerConnect,
			TransactionID: binary.BigEndian.Uint32(packet[12:16]),
		}, true
	}

	// Non-connect requests carry an opaque connection ID, so their evidence is
	// the complete BEP 15 layout rather than a magic prefix.
	action := UDPTrackerAction(binary.BigEndian.Uint32(packet[8:12]))
	txID := binary.BigEndian.Uint32(packet[12:16])
	switch action {
	case UDPTrackerAnnounce:
		if len(packet) != 98 {
			return UDPTrackerPacket{}, false
		}
		event := binary.BigEndian.Uint32(packet[80:84])
		if event > 3 {
			return UDPTrackerPacket{}, false
		}
		numWant := int32(binary.BigEndian.Uint32(packet[92:96]))
		if numWant < -1 {
			return UDPTrackerPacket{}, false
		}
		return UDPTrackerPacket{Action: action, TransactionID: txID}, true
	case UDPTrackerScrape:
		if len(packet) < 36 || (len(packet)-16)%20 != 0 {
			return UDPTrackerPacket{}, false
		}
		return UDPTrackerPacket{Action: action, TransactionID: txID}, true
	default:
		return UDPTrackerPacket{}, false
	}
}

// ParseUDPTrackerResponse validates a BEP 15 response against the action and
// transaction ID of a request previously observed on the same UDP flow. This
// correlation is required because response headers alone are ambiguous.
// Error responses may correspond to any supported request action.
func ParseUDPTrackerResponse(packet []byte, requestAction UDPTrackerAction, transactionID uint32) (UDPTrackerPacket, bool) {
	if len(packet) < 8 || len(packet) > maxUDPDatagramSize {
		return UDPTrackerPacket{}, false
	}
	if requestAction != UDPTrackerConnect && requestAction != UDPTrackerAnnounce && requestAction != UDPTrackerScrape {
		return UDPTrackerPacket{}, false
	}
	action := UDPTrackerAction(binary.BigEndian.Uint32(packet[:4]))
	txID := binary.BigEndian.Uint32(packet[4:8])
	if txID != transactionID {
		return UDPTrackerPacket{}, false
	}
	if action == UDPTrackerError {
		if len(packet) < 9 || len(packet) > 520 {
			return UDPTrackerPacket{}, false
		}
		return UDPTrackerPacket{Action: action, TransactionID: txID, IsError: true}, true
	}
	if action != requestAction {
		return UDPTrackerPacket{}, false
	}
	switch action {
	case UDPTrackerConnect:
		if len(packet) != 16 {
			return UDPTrackerPacket{}, false
		}
	case UDPTrackerAnnounce:
		if len(packet) < 20 || (len(packet)-20)%6 != 0 {
			return UDPTrackerPacket{}, false
		}
	case UDPTrackerScrape:
		if len(packet) < 20 || (len(packet)-8)%12 != 0 {
			return UDPTrackerPacket{}, false
		}
	default:
		return UDPTrackerPacket{}, false
	}
	return UDPTrackerPacket{Action: action, TransactionID: txID}, true
}

// KRPCMessageType identifies the validated BEP 5 KRPC message kind.
type KRPCMessageType uint8

const (
	KRPCQuery KRPCMessageType = iota + 1
	KRPCResponse
	KRPCError
)

// KRPCMessage contains the identifying fields of a validated BEP 5 message.
// TransactionID and Method are copied from the packet into strings.
type KRPCMessage struct {
	Type          KRPCMessageType
	TransactionID string
	Method        string
	ErrorCode     int64
}

type bencodeKind uint8

const (
	bencodeBytes bencodeKind = iota + 1
	bencodeInteger
	bencodeList
	bencodeDictionary
)

type bencodePair struct {
	key   []byte
	value *bencodeValue
}

type bencodeValue struct {
	kind bencodeKind
	data []byte
	int  int64
	list []*bencodeValue
	dict []bencodePair
}

type bencodeParser struct {
	data  []byte
	off   int
	nodes int
}

// ParseKRPCMessage validates a complete, canonical bencoded BEP 5 query,
// response, or error. It requires the two-byte transaction ID, a recognized
// query method and method-specific required fields, or a correctly shaped
// response/error. Arbitrary bencoded dictionaries and bare d/e prefixes are
// not classified as KRPC.
func ParseKRPCMessage(packet []byte) (KRPCMessage, bool) {
	if len(packet) == 0 || len(packet) > maxUDPDatagramSize {
		return KRPCMessage{}, false
	}
	p := bencodeParser{data: packet}
	root, ok := p.value(0)
	if !ok || p.off != len(packet) || root.kind != bencodeDictionary {
		return KRPCMessage{}, false
	}
	y, ok := dictGet(root, "y")
	if !ok || y.kind != bencodeBytes || len(y.data) != 1 {
		return KRPCMessage{}, false
	}
	t, ok := dictGet(root, "t")
	if !ok || t.kind != bencodeBytes || len(t.data) != 2 {
		return KRPCMessage{}, false
	}
	message := KRPCMessage{TransactionID: string(t.data)}
	switch y.data[0] {
	case 'q':
		if hasDictKey(root, "r") || hasDictKey(root, "e") {
			return KRPCMessage{}, false
		}
		q, qOK := dictGet(root, "q")
		a, aOK := dictGet(root, "a")
		if !qOK || q.kind != bencodeBytes || !aOK || a.kind != bencodeDictionary {
			return KRPCMessage{}, false
		}
		method := string(q.data)
		if !validKRPCQuery(method, a) {
			return KRPCMessage{}, false
		}
		message.Type = KRPCQuery
		message.Method = method
		return message, true
	case 'r':
		if hasDictKey(root, "a") || hasDictKey(root, "q") || hasDictKey(root, "e") {
			return KRPCMessage{}, false
		}
		r, rOK := dictGet(root, "r")
		if !rOK || r.kind != bencodeDictionary || !validKRPCResponse(r) {
			return KRPCMessage{}, false
		}
		message.Type = KRPCResponse
		return message, true
	case 'e':
		if hasDictKey(root, "a") || hasDictKey(root, "q") || hasDictKey(root, "r") {
			return KRPCMessage{}, false
		}
		e, eOK := dictGet(root, "e")
		if !eOK || e.kind != bencodeList || len(e.list) != 2 ||
			e.list[0].kind != bencodeInteger || e.list[1].kind != bencodeBytes ||
			e.list[0].int < 201 || e.list[0].int > 204 || len(e.list[1].data) == 0 || len(e.list[1].data) > 256 {
			return KRPCMessage{}, false
		}
		message.Type = KRPCError
		message.ErrorCode = e.list[0].int
		return message, true
	default:
		return KRPCMessage{}, false
	}
}

func validKRPCQuery(method string, args *bencodeValue) bool {
	id, ok := dictGet(args, "id")
	if !ok || id.kind != bencodeBytes || len(id.data) != 20 {
		return false
	}
	switch method {
	case "ping":
		return true
	case "find_node":
		target, ok := dictGet(args, "target")
		return ok && target.kind == bencodeBytes && len(target.data) == 20
	case "get_peers":
		infoHash, ok := dictGet(args, "info_hash")
		return ok && infoHash.kind == bencodeBytes && len(infoHash.data) == 20
	case "announce_peer":
		infoHash, hasInfoHash := dictGet(args, "info_hash")
		token, hasToken := dictGet(args, "token")
		port, hasPort := dictGet(args, "port")
		if !hasInfoHash || infoHash.kind != bencodeBytes || len(infoHash.data) != 20 ||
			!hasToken || token.kind != bencodeBytes || len(token.data) == 0 || !hasPort ||
			port.kind != bencodeInteger || port.int < 0 || port.int > 65535 {
			return false
		}
		implied, hasImplied := dictGet(args, "implied_port")
		return !hasImplied || implied.kind == bencodeInteger && (implied.int == 0 || implied.int == 1)
	default:
		return false
	}
}

func validKRPCResponse(response *bencodeValue) bool {
	id, ok := dictGet(response, "id")
	if !ok || id.kind != bencodeBytes || len(id.data) != 20 {
		return false
	}
	if nodes, ok := dictGet(response, "nodes"); ok &&
		(nodes.kind != bencodeBytes || len(nodes.data)%26 != 0) {
		return false
	}
	if nodes6, ok := dictGet(response, "nodes6"); ok &&
		(nodes6.kind != bencodeBytes || len(nodes6.data)%38 != 0) {
		return false
	}
	if token, ok := dictGet(response, "token"); ok &&
		(token.kind != bencodeBytes || len(token.data) == 0 || len(token.data) > 32) {
		return false
	}
	if values, ok := dictGet(response, "values"); ok {
		if values.kind != bencodeList || len(values.list) == 0 {
			return false
		}
		for _, value := range values.list {
			if value.kind != bencodeBytes || (len(value.data) != 6 && len(value.data) != 18) {
				return false
			}
		}
	}
	if ip, ok := dictGet(response, "ip"); ok &&
		(ip.kind != bencodeBytes || (len(ip.data) != 6 && len(ip.data) != 18)) {
		return false
	}
	return true
}

func (p *bencodeParser) value(depth int) (*bencodeValue, bool) {
	if depth > maxBencodeDepth || p.off >= len(p.data) {
		return nil, false
	}
	p.nodes++
	if p.nodes > maxBencodeNodes {
		return nil, false
	}
	switch p.data[p.off] {
	case 'i':
		return p.integer()
	case 'l':
		p.off++
		value := &bencodeValue{kind: bencodeList}
		for {
			if p.off >= len(p.data) {
				return nil, false
			}
			if p.data[p.off] == 'e' {
				p.off++
				return value, true
			}
			child, ok := p.value(depth + 1)
			if !ok {
				return nil, false
			}
			value.list = append(value.list, child)
		}
	case 'd':
		p.off++
		value := &bencodeValue{kind: bencodeDictionary}
		var previous []byte
		for {
			if p.off >= len(p.data) {
				return nil, false
			}
			if p.data[p.off] == 'e' {
				p.off++
				return value, true
			}
			key, ok := p.byteString()
			if !ok || previous != nil && bytes.Compare(previous, key) >= 0 {
				return nil, false
			}
			child, ok := p.value(depth + 1)
			if !ok {
				return nil, false
			}
			value.dict = append(value.dict, bencodePair{key: key, value: child})
			previous = key
		}
	default:
		if p.data[p.off] < '0' || p.data[p.off] > '9' {
			return nil, false
		}
		data, ok := p.byteString()
		if !ok {
			return nil, false
		}
		return &bencodeValue{kind: bencodeBytes, data: data}, true
	}
}

func (p *bencodeParser) integer() (*bencodeValue, bool) {
	p.off++ // i
	start := p.off
	for p.off < len(p.data) && p.data[p.off] != 'e' {
		p.off++
	}
	if p.off >= len(p.data) || p.off == start {
		return nil, false
	}
	text := p.data[start:p.off]
	p.off++ // e
	if text[0] == '-' {
		if len(text) == 1 || text[1] == '0' {
			return nil, false
		}
	} else if text[0] == '0' && len(text) > 1 {
		return nil, false
	}
	for i, c := range text {
		if i == 0 && c == '-' {
			continue
		}
		if c < '0' || c > '9' {
			return nil, false
		}
	}
	n, err := strconv.ParseInt(string(text), 10, 64)
	if err != nil {
		return nil, false
	}
	return &bencodeValue{kind: bencodeInteger, int: n}, true
}

func (p *bencodeParser) byteString() ([]byte, bool) {
	start := p.off
	for p.off < len(p.data) && p.data[p.off] != ':' {
		if p.data[p.off] < '0' || p.data[p.off] > '9' {
			return nil, false
		}
		p.off++
	}
	if p.off == start || p.off >= len(p.data) {
		return nil, false
	}
	lengthText := p.data[start:p.off]
	if lengthText[0] == '0' && len(lengthText) > 1 {
		return nil, false
	}
	length, err := strconv.Atoi(string(lengthText))
	if err != nil {
		return nil, false
	}
	p.off++ // :
	if length > len(p.data)-p.off {
		return nil, false
	}
	result := p.data[p.off : p.off+length]
	p.off += length
	return result, true
}

func dictGet(value *bencodeValue, key string) (*bencodeValue, bool) {
	if value == nil || value.kind != bencodeDictionary {
		return nil, false
	}
	for _, pair := range value.dict {
		if string(pair.key) == key {
			return pair.value, true
		}
	}
	return nil, false
}

func hasDictKey(value *bencodeValue, key string) bool {
	_, ok := dictGet(value, key)
	return ok
}

// UTPType is the packet type from the high four bits of a BEP 29 version-1
// uTP header.
type UTPType uint8

const (
	UTPData UTPType = iota
	UTPFin
	UTPState
	UTPReset
	UTPSyn
)

// UTPHeader contains fields parsed from a structurally valid BEP 29 v1 header.
type UTPHeader struct {
	Type           UTPType
	ConnectionID   uint16
	SequenceNumber uint16
	AckNumber      uint16
	Timestamp      uint32
	TimestampDiff  uint32
	WindowSize     uint32
	ExtensionCount uint8
	HeaderLength   int
}

// ParseUTPHeader validates a BEP 29 version-1 header and its chained extension
// records. DATA may have payload after the parsed header; FIN, STATE, RESET and
// SYN packets may not. This validates wire structure, not peer identity, so
// callers should use the result in flow context where false positives matter.
//
// The layout is BEP 29's (and libutp's): type|ver, extension, connection_id,
// timestamp_microseconds, timestamp_difference_microseconds, wnd_size, seq_nr, ack_nr.
func ParseUTPHeader(packet []byte) (UTPHeader, bool) {
	if len(packet) < 20 || len(packet) > maxUDPDatagramSize {
		return UTPHeader{}, false
	}
	version := packet[0] & 0x0f
	typ := UTPType(packet[0] >> 4)
	if version != 1 || typ > UTPSyn {
		return UTPHeader{}, false
	}
	header := UTPHeader{
		Type:           typ,
		ConnectionID:   binary.BigEndian.Uint16(packet[2:4]),
		Timestamp:      binary.BigEndian.Uint32(packet[4:8]),
		TimestampDiff:  binary.BigEndian.Uint32(packet[8:12]),
		WindowSize:     binary.BigEndian.Uint32(packet[12:16]),
		SequenceNumber: binary.BigEndian.Uint16(packet[16:18]),
		AckNumber:      binary.BigEndian.Uint16(packet[18:20]),
		HeaderLength:   20,
	}
	extension := packet[1]
	offset := 20
	for extension != 0 {
		if header.ExtensionCount >= maxUTPExtensions || len(packet)-offset < 2 {
			return UTPHeader{}, false
		}
		nextExtension := packet[offset]
		length := int(packet[offset+1])
		offset += 2
		if length > len(packet)-offset {
			return UTPHeader{}, false
		}
		if extension == 1 && (length == 0 || length > 32 || length%4 != 0) {
			return UTPHeader{}, false
		}
		offset += length
		header.ExtensionCount++
		extension = nextExtension
	}
	header.HeaderLength = offset
	if typ != UTPData && offset != len(packet) {
		return UTPHeader{}, false
	}
	return header, true
}
