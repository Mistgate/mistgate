package trusttunnel

import (
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
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
		if host == "" || strings.ContainsAny(host, "[]/\\@?#") {
			return Authority{}, ErrInvalidAuthority
		}
		if address, err := netip.ParseAddr(host); err == nil && address.Is6() {
			// IPv6 literals in CONNECT authorities must be bracketed.
			return Authority{}, ErrInvalidAuthority
		}
		for _, r := range host {
			if unicode.IsSpace(r) || unicode.IsControl(r) {
				return Authority{}, ErrInvalidAuthority
			}
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

func isPseudoHost(host string) bool {
	switch host {
	case "_check", "_udp2", "_icmp":
		return true
	default:
		return false
	}
}
