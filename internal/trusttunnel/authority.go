package trusttunnel

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
)

const MaxAuthorityBytes = 8 * 1024

// AuthorityKind describes a CONNECT authority.
type AuthorityKind uint8

const (
	AuthorityInvalid AuthorityKind = iota
	AuthorityHostPort
	AuthorityCheck
	AuthorityUDP2
	AuthorityICMP
)

// Authority is a parsed CONNECT authority. Pseudo-hosts have port zero.
type Authority struct {
	Kind AuthorityKind
	Host string
	Port uint16
}

var (
	ErrInvalidAuthority = errors.New("invalid CONNECT authority")
	ErrAuthorityTooLong = errors.New("CONNECT authority is too long")
)

// ParseAuthority classifies the three reserved pseudo-hosts and parses
// host:port or [IPv6]:port authorities. Reserved pseudo-hosts are accepted
// without a port; every ordinary host requires a decimal port from 1 to 65535.
func ParseAuthority(value string) (Authority, error) {
	if len(value) > MaxAuthorityBytes {
		return Authority{}, ErrAuthorityTooLong
	}

	switch value {
	case "_check":
		return Authority{Kind: AuthorityCheck, Host: value}, nil
	case "_udp2":
		return Authority{Kind: AuthorityUDP2, Host: value}, nil
	case "_icmp":
		return Authority{Kind: AuthorityICMP, Host: value}, nil
	}
	if value == "" {
		return Authority{}, ErrInvalidAuthority
	}

	var host, portText string
	if value[0] == '[' {
		closeBracket := strings.IndexByte(value, ']')
		if closeBracket <= 1 || closeBracket+1 >= len(value) || value[closeBracket+1] != ':' {
			return Authority{}, ErrInvalidAuthority
		}
		if strings.IndexByte(value[closeBracket+2:], ']') >= 0 {
			return Authority{}, ErrInvalidAuthority
		}
		host = value[1:closeBracket]
		portText = value[closeBracket+2:]
		address, err := netip.ParseAddr(host)
		if err != nil || !address.Is6() || address.Is4In6() || address.Zone() != "" {
			return Authority{}, ErrInvalidAuthority
		}
	} else {
		var ok bool
		host, portText, ok = strings.Cut(value, ":")
		if !ok || strings.Contains(portText, ":") {
			return Authority{}, ErrInvalidAuthority
		}
		if !validHostName(host) {
			return Authority{}, ErrInvalidAuthority
		}
		if address, err := netip.ParseAddr(host); err == nil && address.Is6() {
			// IPv6 literals in CONNECT authorities must be bracketed.
			return Authority{}, ErrInvalidAuthority
		}
	}

	if isPseudoHost(host) {
		// The reserved endpoint names have no port in the TrustTunnel wire
		// format. Treating a suffixed form as an ordinary TCP target is unsafe.
		return Authority{}, ErrInvalidAuthority
	}
	if portText == "" {
		return Authority{}, ErrInvalidAuthority
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return Authority{}, ErrInvalidAuthority
	}
	return Authority{Kind: AuthorityHostPort, Host: host, Port: uint16(port)}, nil
}

// maxHostNameBytes is the longest DNS name (253 octets) with room for a final dot.
const maxHostNameBytes = 255

// validHostName accepts what the official server's HTTP parser accepts in the
// host part of an authority: ASCII unreserved characters, sub-delimiters and
// '%'. Non-ASCII names (IDN) are expected in punycode; userinfo, brackets,
// slashes, whitespace and control bytes are refused.
func validHostName(host string) bool {
	if host == "" || len(host) > maxHostNameBytes {
		return false
	}
	for i := 0; i < len(host); i++ {
		c := host[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("-._~!$&'()*+,;=%", c) >= 0) {
			return false
		}
	}
	return true
}

func isPseudoHost(host string) bool {
	switch host {
	case "_check", "_udp2", "_icmp":
		return true
	default:
		return false
	}
}
