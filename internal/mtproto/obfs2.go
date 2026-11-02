package mtproto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net/netip"
)

// headerLen is the obfuscated2 header: 8 random bytes, the 32-byte key and 16-byte IV of the sender's direction, then
// the protocol tag (56..60) and the DC (60..62, little-endian int16), the last 8 bytes sent encrypted. The other
// direction's key and IV are bytes 8..56 read backwards. With a proxy key both keys are SHA-256(key || proxy key); a
// Telegram DC takes them as they are.
const headerLen = 64

var errBadHeader = errors.New("mtproto: obfuscated2 header has no known protocol tag")

// openHeader reads a client's header with its credential key. It returns the cipher of the client's stream (already
// past the header), the cipher of the stream back to the client, the tag and the DC. A wrong key gives a random tag,
// which is the only check obfuscated2 has.
func openHeader(h *[headerLen]byte, key *[KeyLen]byte) (fromClient, toClient cipher.Stream, tag Tag, dc int16, err error) {
	rev := reversed(h)
	fromClient = ctr(proxyKey(h[8:40], key), h[40:56])
	toClient = ctr(proxyKey(rev[0:32], key), rev[32:48])
	var plain [headerLen]byte
	fromClient.XORKeyStream(plain[:], h[:])
	switch tag = Tag(binary.LittleEndian.Uint32(plain[56:60])); tag {
	case TagAbridged, TagIntermediate, TagPadded:
	default:
		return nil, nil, 0, 0, errBadHeader
	}
	return fromClient, toClient, tag, int16(binary.LittleEndian.Uint16(plain[60:62])), nil
}

// newHeader makes the header of a new connection to a DC with fresh random keys: the client's keys never leave the
// proxy, so the DC leg cannot be decrypted with anything the client (or anyone who learns its key) holds.
func newHeader(random io.Reader, tag Tag, dc int16) (out [headerLen]byte, toDC, fromDC cipher.Stream, err error) {
	for {
		if _, err = io.ReadFull(random, out[:]); err != nil {
			return out, nil, nil, err
		}
		if !reservedStart(&out) {
			break
		}
	}
	binary.LittleEndian.PutUint32(out[56:60], uint32(tag))
	binary.LittleEndian.PutUint16(out[60:62], uint16(dc))
	rev := reversed(&out)
	toDC = ctr(out[8:40], out[40:56])
	fromDC = ctr(rev[0:32], rev[32:48])
	var sealed [headerLen]byte
	toDC.XORKeyStream(sealed[:], out[:])
	copy(out[56:], sealed[56:])
	return out, toDC, fromDC, nil
}

// reservedStart reports a header a DC would read as another transport: the abridged marker, the intermediate and padded
// tags, an HTTP method, a TLS record, or the zero sequence number of the full transport (Telegram's transport
// obfuscation spec).
func reservedStart(h *[headerLen]byte) bool {
	if h[0] == 0xef {
		return true
	}
	switch binary.LittleEndian.Uint32(h[0:4]) {
	case 0x44414548, 0x54534f50, 0x20544547, 0x4954504f, 0x02010316, 0xdddddddd, 0xeeeeeeee: // HEAD POST "GET " OPTI, TLS
		return true
	}
	return binary.LittleEndian.Uint32(h[4:8]) == 0
}

// reversed is bytes 8..56 of h backwards: the key (0..32) and IV (32..48) of the opposite direction.
func reversed(h *[headerLen]byte) (r [48]byte) {
	for i := range r {
		r[i] = h[55-i]
	}
	return r
}

func proxyKey(k []byte, key *[KeyLen]byte) []byte {
	sum := sha256.New()
	sum.Write(k)
	sum.Write(key[:])
	return sum.Sum(nil)
}

func ctr(key, iv []byte) cipher.Stream {
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err) // the key is always 32 bytes
	}
	return cipher.NewCTR(block, iv)
}

// dcAddrs are Telegram's published production DC addresses, port 443. A negative DC is the media side of the same DC:
// without the middle proxies a direct connection reaches it at the main address. DC 203 is the one extra DC clients
// ask for outside 1-5.
var dcAddrs = map[int16][]string{
	1:   {"149.154.175.50:443", "[2001:b28:f23d:f001::a]:443"},
	2:   {"149.154.167.51:443", "95.161.76.100:443", "[2001:67c:4e8:f002::a]:443"},
	3:   {"149.154.175.100:443", "[2001:b28:f23d:f003::a]:443"},
	4:   {"149.154.167.91:443", "[2001:67c:4e8:f004::a]:443"},
	5:   {"149.154.171.5:443", "[2001:b28:f23f:f005::a]:443"},
	203: {"91.105.192.100:443", "[2a0a:f280:203:a:5000::100]:443"},
}

// defaultDC is where a header without a DC (0) goes, as other proxies do.
const defaultDC = 2

// dcTargets is the list of addresses to try for a client's DC, in order: the DCAddress override, else the table with
// IPv4 first (IPv6 first with PreferIPv6). Nil for a DC Telegram does not publish (test DCs are 10000+).
func (s *Server) dcTargets(dc int16) []string {
	if s.cfg.DCAddress != nil {
		if addr, ok := s.cfg.DCAddress(dc); ok {
			return []string{addr}
		}
	}
	n := dc
	if n < 0 {
		n = -n // -32768 stays negative and is unknown
	}
	if n == 0 {
		n = defaultDC
	}
	var v4, v6 []string
	for _, a := range dcAddrs[n] {
		if ap := netip.MustParseAddrPort(a); ap.Addr().Is6() {
			v6 = append(v6, a)
		} else {
			v4 = append(v4, a)
		}
	}
	if s.cfg.PreferIPv6 {
		return append(v6, v4...)
	}
	return append(v4, v6...)
}
