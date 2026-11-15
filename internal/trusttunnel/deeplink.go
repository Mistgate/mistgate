package trusttunnel

import (
	"encoding/base64"
	"errors"
	"net/netip"
	"unicode/utf8"
)

const (
	DeepLinkPrefix     = "tt://?"
	MaxDeepLinkBytes   = 8 * 1024
	deepLinkVersion    = 2
	deepLinkMaxPayload = 6139

	deepLinkTagVersion          = 0x00
	deepLinkTagHostname         = 0x01
	deepLinkTagAddress          = 0x02
	deepLinkTagHasIPv6          = 0x04
	deepLinkTagUsername         = 0x05
	deepLinkTagPassword         = 0x06
	deepLinkTagUpstreamProtocol = 0x09
	deepLinkTagName             = 0x0c
	deepLinkTagDNSUpstreams     = 0x0d
)

var (
	ErrInvalidDeepLink    = errors.New("invalid TrustTunnel deep link")
	ErrDeepLinkTooLong    = errors.New("TrustTunnel deep link exceeds 8 KiB")
	ErrUnsupportedVersion = errors.New("unsupported TrustTunnel deep link version")
	ErrInvalidQUICVarint  = errors.New("invalid QUIC variable-length integer")
)

// DeepLink contains the static TrustTunnel parameters supported by this
// package. The encoder requires exactly one address, as defined by the
// Mistgate B6 contract; the decoder preserves repeated address tags.
type DeepLink struct {
	Hostname     string
	Addresses    []string
	HasIPv6      bool
	Username     string
	Password     string
	Name         string
	DNSUpstreams []netip.Addr
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

// EncodeDeepLink encodes a version-2 tt:// link. It always writes has_ipv6,
// writes upstream_protocol as HTTP/2 (1), and omits unsupported optional tags.
func EncodeDeepLink(link DeepLink) (string, error) {
	if !validDeepLinkString(link.Hostname, false) ||
		!validDeepLinkString(link.Username, false) ||
		!validDeepLinkString(link.Password, false) ||
		!validDeepLinkString(link.Name, true) ||
		len(link.Addresses) != 1 {
		return "", ErrInvalidDeepLink
	}
	if err := validateDeepLinkAddress(link.Addresses[0]); err != nil {
		return "", err
	}
	if len(link.DNSUpstreams) > 2 {
		return "", ErrInvalidDeepLink
	}
	for _, upstream := range link.DNSUpstreams {
		if !upstream.Is4() {
			return "", ErrInvalidDeepLink
		}
	}

	payload := make([]byte, 0, 512)
	version, _ := AppendQUICVarint(nil, deepLinkVersion)
	var err error
	payload, err = appendDeepLinkTLV(payload, deepLinkTagVersion, version)
	if err != nil {
		return "", err
	}
	payload, err = appendDeepLinkStringTLV(payload, deepLinkTagHostname, link.Hostname)
	if err != nil {
		return "", err
	}
	payload, err = appendDeepLinkStringTLV(payload, deepLinkTagAddress, link.Addresses[0])
	if err != nil {
		return "", err
	}

	hasIPv6 := byte(0)
	if link.HasIPv6 {
		hasIPv6 = 1
	}
	payload, err = appendDeepLinkTLV(payload, deepLinkTagHasIPv6, []byte{hasIPv6})
	if err != nil {
		return "", err
	}
	payload, err = appendDeepLinkStringTLV(payload, deepLinkTagUsername, link.Username)
	if err != nil {
		return "", err
	}
	payload, err = appendDeepLinkStringTLV(payload, deepLinkTagPassword, link.Password)
	if err != nil {
		return "", err
	}
	upstreamProtocol, _ := AppendQUICVarint(nil, 1)
	payload, err = appendDeepLinkTLV(payload, deepLinkTagUpstreamProtocol, upstreamProtocol)
	if err != nil {
		return "", err
	}
	if link.Name != "" {
		payload, err = appendDeepLinkStringTLV(payload, deepLinkTagName, link.Name)
		if err != nil {
			return "", err
		}
	}
	if len(link.DNSUpstreams) != 0 {
		dnsValue := make([]byte, 0, len(link.DNSUpstreams)*16)
		for _, upstream := range link.DNSUpstreams {
			address := upstream.String()
			dnsValue, err = AppendQUICVarint(dnsValue, uint64(len(address)))
			if err != nil {
				return "", err
			}
			dnsValue = append(dnsValue, address...)
		}
		payload, err = appendDeepLinkTLV(payload, deepLinkTagDNSUpstreams, dnsValue)
		if err != nil {
			return "", err
		}
	}

	encodedLength := base64.RawURLEncoding.EncodedLen(len(payload))
	if len(DeepLinkPrefix)+encodedLength > MaxDeepLinkBytes {
		return "", ErrDeepLinkTooLong
	}
	return DeepLinkPrefix + base64.RawURLEncoding.EncodeToString(payload), nil
}

// DecodeDeepLink decodes a tt:// link. Unknown tags are skipped, version 0
// links are accepted as specified by TrustTunnel, and versions above 2 fail.
func DecodeDeepLink(value string) (DeepLink, error) {
	if len(value) > MaxDeepLinkBytes {
		return DeepLink{}, ErrDeepLinkTooLong
	}
	if len(value) < len(DeepLinkPrefix) || value[:len(DeepLinkPrefix)] != DeepLinkPrefix {
		return DeepLink{}, ErrInvalidDeepLink
	}
	encodedPayload := value[len(DeepLinkPrefix):]
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
	if len(payload) > deepLinkMaxPayload {
		return DeepLink{}, ErrDeepLinkTooLong
	}

	decoded := DeepLink{HasIPv6: true}
	version := uint64(0)
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

		switch tag {
		case deepLinkTagVersion:
			parsed, consumed, parseErr := ReadQUICVarint(field)
			if parseErr != nil || consumed != len(field) {
				return DeepLink{}, ErrInvalidDeepLink
			}
			version = parsed
		case deepLinkTagHostname:
			if !utf8.Valid(field) {
				return DeepLink{}, ErrInvalidDeepLink
			}
			decoded.Hostname = string(field)
		case deepLinkTagAddress:
			if !utf8.Valid(field) {
				return DeepLink{}, ErrInvalidDeepLink
			}
			address := string(field)
			if err := validateDeepLinkAddress(address); err != nil {
				return DeepLink{}, err
			}
			decoded.Addresses = append(decoded.Addresses, address)
		case deepLinkTagHasIPv6:
			if len(field) != 1 || field[0] > 1 {
				return DeepLink{}, ErrInvalidDeepLink
			}
			decoded.HasIPv6 = field[0] == 1
		case deepLinkTagUsername:
			if !utf8.Valid(field) {
				return DeepLink{}, ErrInvalidDeepLink
			}
			decoded.Username = string(field)
		case deepLinkTagPassword:
			if !utf8.Valid(field) {
				return DeepLink{}, ErrInvalidDeepLink
			}
			decoded.Password = string(field)
		case deepLinkTagUpstreamProtocol:
			protocol, consumed, parseErr := ReadQUICVarint(field)
			if parseErr != nil || consumed != len(field) || protocol != 1 {
				return DeepLink{}, ErrInvalidDeepLink
			}
		case deepLinkTagName:
			if !utf8.Valid(field) {
				return DeepLink{}, ErrInvalidDeepLink
			}
			decoded.Name = string(field)
		case deepLinkTagDNSUpstreams:
			dns, parseErr := decodeDNSUpstreams(field)
			if parseErr != nil {
				return DeepLink{}, parseErr
			}
			decoded.DNSUpstreams = dns
		default:
			// Unknown and unsupported tags are forward-compatible extensions.
		}
	}

	if version > deepLinkVersion {
		return DeepLink{}, ErrUnsupportedVersion
	}
	if decoded.Hostname == "" || len(decoded.Addresses) == 0 ||
		decoded.Username == "" || decoded.Password == "" {
		return DeepLink{}, ErrInvalidDeepLink
	}
	return decoded, nil
}

func validDeepLinkString(value string, emptyAllowed bool) bool {
	return (emptyAllowed || value != "") &&
		len(value) <= MaxDeepLinkBytes &&
		utf8.ValidString(value)
}

func validateDeepLinkAddress(value string) error {
	authority, err := ParseAuthority(value)
	if err != nil || authority.Kind != AuthorityHostPort {
		return ErrInvalidDeepLink
	}
	return nil
}

func appendDeepLinkStringTLV(payload []byte, tag uint64, value string) ([]byte, error) {
	if len(value) > deepLinkMaxPayload || !utf8.ValidString(value) {
		return nil, ErrDeepLinkTooLong
	}
	return appendDeepLinkTLV(payload, tag, []byte(value))
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

func decodeDNSUpstreams(value []byte) ([]netip.Addr, error) {
	var addresses []netip.Addr
	for offset := 0; offset < len(value); {
		length, n, err := ReadQUICVarint(value[offset:])
		if err != nil {
			return nil, ErrInvalidDeepLink
		}
		offset += n
		if length > uint64(len(value)-offset) || len(addresses) == 2 {
			return nil, ErrInvalidDeepLink
		}
		entry := value[offset : offset+int(length)]
		offset += int(length)
		if !utf8.Valid(entry) {
			return nil, ErrInvalidDeepLink
		}
		address, err := netip.ParseAddr(string(entry))
		if err != nil || !address.Is4() {
			return nil, ErrInvalidDeepLink
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}
