package trusttunnel

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"unicode/utf8"
)

const (
	DeepLinkPrefix = "tt://?"
	// MaxDeepLinkBytes bounds the links this package writes (QR-friendly).
	MaxDeepLinkBytes = 8 * 1024
	// MaxDeepLinkDecodeBytes bounds the links it reads. DEEP_LINK.md asks
	// parsers for "at least 8 KiB"; a certificate chain can need more.
	MaxDeepLinkDecodeBytes = 64 * 1024
	deepLinkVersion        = 2
	deepLinkMaxPayload     = 6139

	deepLinkTagVersion             = 0x00
	deepLinkTagHostname            = 0x01
	deepLinkTagAddress             = 0x02
	deepLinkTagCustomSNI           = 0x03
	deepLinkTagHasIPv6             = 0x04
	deepLinkTagUsername            = 0x05
	deepLinkTagPassword            = 0x06
	deepLinkTagSkipVerification    = 0x07
	deepLinkTagCertificate         = 0x08
	deepLinkTagUpstreamProtocol    = 0x09
	deepLinkTagAntiDPI             = 0x0a
	deepLinkTagClientRandomPrefix  = 0x0b
	deepLinkTagName                = 0x0c
	deepLinkTagDNSUpstreams        = 0x0d
	deepLinkTagSubscriptionURL     = 0x0e
	deepLinkTagClientRandomAuthKey = 0x0f
)

var (
	ErrInvalidDeepLink    = errors.New("invalid TrustTunnel deep link")
	ErrDeepLinkTooLong    = errors.New("TrustTunnel deep link is too long")
	ErrUnsupportedVersion = errors.New("unsupported TrustTunnel deep link version")
	ErrInvalidQUICVarint  = errors.New("invalid QUIC variable-length integer")
)

// DeepLink is the decoded content of a tt:// link (DEEP_LINK.md, version 2).
// Static connection fields are required unless SubscriptionURL is set.
//
// The decoder takes every tag of the format. The encoder additionally applies
// the Mistgate B6 policy: exactly one address (none when SubscriptionURL is
// set) and at most two IPv4 DNS upstreams.
type DeepLink struct {
	Hostname  string
	Addresses []string
	// HasIPv6 is true when the tag is absent (the format's default); the
	// encoder always writes it explicitly.
	HasIPv6  bool
	Username string
	Password string
	Name     string
	// DNSUpstreams are the raw upstream strings ("1.1.1.1", "tls://host",
	// "https://host/dns-query"). The decoder does not interpret them.
	DNSUpstreams        []string
	CustomSNI           string
	SkipVerification    bool
	Certificate         []byte // concatenated DER certificates, nil when absent
	HTTP3               bool   // upstream_protocol 2; false is HTTP/2 (the default)
	AntiDPI             bool
	ClientRandomPrefix  string // "hexprefix[/hexmask]"
	ClientRandomAuthKey string // hex
	SubscriptionURL     string // https:// only
}

// AppendQUICVarint appends value using the QUIC variable-length integer
// encoding. Values greater than 2^62-1 are rejected.
func AppendQUICVarint(destination []byte, value uint64) ([]byte, error) {
	switch {
	case value <= (1<<6)-1:
		return append(destination, byte(value)), nil
	case value <= (1<<14)-1:
		return append(destination,
			byte(value>>8)|0x40,
			byte(value)), nil
	case value <= (1<<30)-1:
		return append(destination,
			byte(value>>24)|0x80,
			byte(value>>16),
			byte(value>>8),
			byte(value)), nil
	case value <= (1<<62)-1:
		return append(destination,
			byte(value>>56)|0xc0,
			byte(value>>48),
			byte(value>>40),
			byte(value>>32),
			byte(value>>24),
			byte(value>>16),
			byte(value>>8),
			byte(value)), nil
	default:
		return destination, ErrInvalidQUICVarint
	}
}

// ReadQUICVarint reads one QUIC variable-length integer from data.
func ReadQUICVarint(data []byte) (value uint64, n int, err error) {
	if len(data) == 0 {
		return 0, 0, ErrInvalidQUICVarint
	}
	size := 1 << (data[0] >> 6)
	if len(data) < size {
		return 0, 0, ErrInvalidQUICVarint
	}
	value = uint64(data[0] & 0x3f)
	for _, b := range data[1:size] {
		value = value<<8 | uint64(b)
	}
	return value, size, nil
}

// EncodeDeepLink encodes a version-2 tt:// link. Tags are written in the order
// of the official encoder. has_ipv6 and upstream_protocol are always explicit
// (a Flutter client reads a missing has_ipv6 as false), except in a
// subscription-only link, which carries only version and subscription_url.
// Fields at their default (false, empty) are omitted.
func EncodeDeepLink(link DeepLink) (string, error) {
	subscription := link.SubscriptionURL != ""
	static := link.Hostname != "" || link.Username != "" || link.Password != "" || len(link.Addresses) != 0
	switch {
	case subscription:
		if !strings.HasPrefix(link.SubscriptionURL, "https://") || len(link.Addresses) > 1 {
			return "", ErrInvalidDeepLink
		}
	case link.Hostname == "" || link.Username == "" || link.Password == "" || len(link.Addresses) != 1:
		return "", ErrInvalidDeepLink
	}
	if !validDeepLinkString(link.Hostname) || !validDeepLinkString(link.Username) ||
		!validDeepLinkString(link.Password) || !validDeepLinkString(link.Name) ||
		!validDeepLinkString(link.CustomSNI) || !validDeepLinkString(link.SubscriptionURL) ||
		!validClientRandomPrefix(link.ClientRandomPrefix) || !validHex(link.ClientRandomAuthKey) {
		return "", ErrInvalidDeepLink
	}
	for _, address := range link.Addresses {
		if err := validateDeepLinkAddress(address); err != nil {
			return "", err
		}
	}
	if len(link.DNSUpstreams) > 2 {
		return "", ErrInvalidDeepLink
	}
	for _, upstream := range link.DNSUpstreams {
		if address, err := netip.ParseAddr(upstream); err != nil || !address.Is4() {
			return "", ErrInvalidDeepLink
		}
	}

	payload := make([]byte, 0, 512)
	var err error
	add := func(tag uint64, value []byte) {
		if err == nil {
			payload, err = appendDeepLinkTLV(payload, tag, value)
		}
	}
	addString := func(tag uint64, value string) {
		if value != "" {
			add(tag, []byte(value))
		}
	}
	addTrue := func(tag uint64, value bool) {
		if value {
			add(tag, []byte{1})
		}
	}

	version, _ := AppendQUICVarint(nil, deepLinkVersion)
	add(deepLinkTagVersion, version)
	addString(deepLinkTagHostname, link.Hostname)
	addString(deepLinkTagUsername, link.Username)
	addString(deepLinkTagPassword, link.Password)
	for _, address := range link.Addresses {
		add(deepLinkTagAddress, []byte(address))
	}
	addString(deepLinkTagClientRandomPrefix, link.ClientRandomPrefix)
	addString(deepLinkTagClientRandomAuthKey, link.ClientRandomAuthKey)
	addString(deepLinkTagCustomSNI, link.CustomSNI)
	if static {
		hasIPv6 := byte(0)
		if link.HasIPv6 {
			hasIPv6 = 1
		}
		add(deepLinkTagHasIPv6, []byte{hasIPv6})
	}
	addTrue(deepLinkTagSkipVerification, link.SkipVerification)
	addTrue(deepLinkTagAntiDPI, link.AntiDPI)
	if len(link.Certificate) != 0 {
		add(deepLinkTagCertificate, link.Certificate)
	}
	if static {
		protocol := byte(1)
		if link.HTTP3 {
			protocol = 2
		}
		add(deepLinkTagUpstreamProtocol, []byte{protocol})
	}
	addString(deepLinkTagName, link.Name)
	if len(link.DNSUpstreams) != 0 {
		dnsValue := make([]byte, 0, len(link.DNSUpstreams)*16)
		for _, upstream := range link.DNSUpstreams {
			dnsValue, _ = AppendQUICVarint(dnsValue, uint64(len(upstream)))
			dnsValue = append(dnsValue, upstream...)
		}
		add(deepLinkTagDNSUpstreams, dnsValue)
	}
	addString(deepLinkTagSubscriptionURL, link.SubscriptionURL)
	if err != nil {
		return "", err
	}

	encodedLength := base64.RawURLEncoding.EncodedLen(len(payload))
	if len(DeepLinkPrefix)+encodedLength > MaxDeepLinkBytes {
		return "", ErrDeepLinkTooLong
	}
	return DeepLinkPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeDeepLink decodes a tt:// link the way the official decoder does:
// "tt://?" or the older "tt://" prefix, unpadded canonical base64url, unknown
// tags skipped, a repeated tag overrides the earlier one (addresses
// accumulate), a version above 2 fails. Required fields may be missing only
// when a subscription_url is present. Deviations from the official decoder,
// all stricter: addresses must be valid host:port, and a tag number above 255
// is unknown instead of wrapping around.
func DecodeDeepLink(value string) (DeepLink, error) {
	if len(value) > MaxDeepLinkDecodeBytes {
		return DeepLink{}, ErrDeepLinkTooLong
	}
	encodedPayload, ok := strings.CutPrefix(value, "tt://")
	if !ok {
		return DeepLink{}, ErrInvalidDeepLink
	}
	encodedPayload = strings.TrimPrefix(encodedPayload, "?")
	if len(encodedPayload) == 0 {
		return DeepLink{}, ErrInvalidDeepLink
	}
	for i := 0; i < len(encodedPayload); i++ {
		c := encodedPayload[i]
		if !(c >= 'A' && c <= 'Z' ||
			c >= 'a' && c <= 'z' ||
			c >= '0' && c <= '9' ||
			c == '-' || c == '_') {
			return DeepLink{}, ErrInvalidDeepLink
		}
	}
	payload, err := base64.RawURLEncoding.Strict().DecodeString(encodedPayload)
	if err != nil {
		return DeepLink{}, ErrInvalidDeepLink
	}

	decoded := DeepLink{HasIPv6: true}
	hasSubscription := false
	for offset := 0; offset < len(payload); {
		tag, n, readErr := ReadQUICVarint(payload[offset:])
		if readErr != nil {
			return DeepLink{}, ErrInvalidDeepLink
		}
		offset += n
		valueLength, n, readErr := ReadQUICVarint(payload[offset:])
		if readErr != nil {
			return DeepLink{}, ErrInvalidDeepLink
		}
		offset += n
		if valueLength > uint64(len(payload)-offset) {
			return DeepLink{}, ErrInvalidDeepLink
		}
		field := payload[offset : offset+int(valueLength)]
		offset += int(valueLength)

		// Every string-valued tag must be UTF-8: the official decoder fails the
		// whole link otherwise.
		switch tag {
		case deepLinkTagHostname, deepLinkTagAddress, deepLinkTagCustomSNI, deepLinkTagUsername, deepLinkTagPassword,
			deepLinkTagClientRandomPrefix, deepLinkTagName, deepLinkTagSubscriptionURL, deepLinkTagClientRandomAuthKey:
			if !utf8.Valid(field) {
				return DeepLink{}, ErrInvalidDeepLink
			}
		}

		switch tag {
		case deepLinkTagVersion:
			// The official decoder reads the first varint and ignores trailing
			// bytes; it checks every occurrence, not just the last.
			parsed, _, parseErr := ReadQUICVarint(field)
			if parseErr != nil {
				return DeepLink{}, ErrInvalidDeepLink
			}
			if parsed > deepLinkVersion {
				return DeepLink{}, ErrUnsupportedVersion
			}
		case deepLinkTagHostname:
			decoded.Hostname = string(field)
		case deepLinkTagAddress:
			address := string(field)
			if err := validateDeepLinkAddress(address); err != nil {
				return DeepLink{}, err
			}
			decoded.Addresses = append(decoded.Addresses, address)
		case deepLinkTagCustomSNI:
			decoded.CustomSNI = string(field)
		case deepLinkTagHasIPv6:
			if decoded.HasIPv6, ok = deepLinkBool(field); !ok {
				return DeepLink{}, ErrInvalidDeepLink
			}
		case deepLinkTagUsername:
			decoded.Username = string(field)
		case deepLinkTagPassword:
			decoded.Password = string(field)
		case deepLinkTagSkipVerification:
			if decoded.SkipVerification, ok = deepLinkBool(field); !ok {
				return DeepLink{}, ErrInvalidDeepLink
			}
		case deepLinkTagCertificate:
			decoded.Certificate = nil
			if len(field) != 0 {
				decoded.Certificate = append([]byte(nil), field...)
			}
		case deepLinkTagUpstreamProtocol:
			// One raw byte, 1 = http2, 2 = http3.
			if len(field) != 1 || field[0] < 1 || field[0] > 2 {
				return DeepLink{}, ErrInvalidDeepLink
			}
			decoded.HTTP3 = field[0] == 2
		case deepLinkTagAntiDPI:
			if decoded.AntiDPI, ok = deepLinkBool(field); !ok {
				return DeepLink{}, ErrInvalidDeepLink
			}
		case deepLinkTagClientRandomPrefix:
			if !validClientRandomPrefix(string(field)) {
				return DeepLink{}, ErrInvalidDeepLink
			}
			decoded.ClientRandomPrefix = string(field)
		case deepLinkTagName:
			decoded.Name = string(field)
		case deepLinkTagDNSUpstreams:
			dns, parseErr := decodeDNSUpstreams(field)
			if parseErr != nil {
				return DeepLink{}, parseErr
			}
			decoded.DNSUpstreams = dns
		case deepLinkTagSubscriptionURL:
			decoded.SubscriptionURL = string(field)
			hasSubscription = true
		case deepLinkTagClientRandomAuthKey:
			if !validHex(string(field)) {
				return DeepLink{}, ErrInvalidDeepLink
			}
			decoded.ClientRandomAuthKey = string(field)
		default:
			// Unknown tags are forward-compatible extensions.
		}
	}

	if hasSubscription {
		// With a subscription URL the static fields are optional.
		if !strings.HasPrefix(decoded.SubscriptionURL, "https://") {
			return DeepLink{}, ErrInvalidDeepLink
		}
		return decoded, nil
	}
	if decoded.Hostname == "" || len(decoded.Addresses) == 0 ||
		decoded.Username == "" || decoded.Password == "" {
		return DeepLink{}, ErrInvalidDeepLink
	}
	return decoded, nil
}

func deepLinkBool(field []byte) (value, ok bool) {
	if len(field) != 1 || field[0] > 1 {
		return false, false
	}
	return field[0] == 1, true
}

func validDeepLinkString(value string) bool {
	return len(value) <= MaxDeepLinkBytes && utf8.ValidString(value)
}

func validHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

// validClientRandomPrefix checks "prefix[/mask]" the way the official decoder
// does: both parts hex, each possibly empty.
func validClientRandomPrefix(value string) bool {
	prefix, mask, _ := strings.Cut(value, "/")
	return validHex(prefix) && validHex(mask)
}

func validateDeepLinkAddress(value string) error {
	authority, err := ParseAuthority(value)
	if err != nil || authority.Kind != AuthorityHostPort {
		return ErrInvalidDeepLink
	}
	return nil
}

func appendDeepLinkTLV(payload []byte, tag uint64, value []byte) ([]byte, error) {
	tagBytes, err := AppendQUICVarint(nil, tag)
	if err != nil {
		return nil, err
	}
	lengthBytes, err := AppendQUICVarint(nil, uint64(len(value)))
	if err != nil {
		return nil, err
	}
	newLength := len(payload) + len(tagBytes) + len(lengthBytes) + len(value)
	if newLength > deepLinkMaxPayload {
		return nil, ErrDeepLinkTooLong
	}
	payload = append(payload, tagBytes...)
	payload = append(payload, lengthBytes...)
	payload = append(payload, value...)
	return payload, nil
}

// decodeDNSUpstreams reads a String[]: varint-length-prefixed UTF-8 strings,
// concatenated. Empty strings are allowed, as in the official decoder.
func decodeDNSUpstreams(value []byte) ([]string, error) {
	var upstreams []string
	for offset := 0; offset < len(value); {
		length, n, err := ReadQUICVarint(value[offset:])
		if err != nil {
			return nil, ErrInvalidDeepLink
		}
		offset += n
		if length > uint64(len(value)-offset) {
			return nil, ErrInvalidDeepLink
		}
		entry := value[offset : offset+int(length)]
		offset += int(length)
		if !utf8.Valid(entry) {
			return nil, ErrInvalidDeepLink
		}
		upstreams = append(upstreams, string(entry))
	}
	return upstreams, nil
}
