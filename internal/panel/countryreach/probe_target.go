package countryreach

import (
	"net/netip"
	"strings"
)

// ProbeSNI says whether s may go into a line of the own-probe job: a host name of letters, digits, hyphens and dots, at most
// 253 characters, no label empty, longer than 63 or edged by a hyphen, not an address and without a trailing dot. The probe's
// shell matches the same shape with `case`, so a name that is not in it is skipped by the panel and never sent.
func ProbeSNI(s string) bool {
	if s == "" || len(s) > 253 || strings.HasSuffix(s, ".") {
		return false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// ProbePath says whether p may go into a line of the own-probe job: ^/[A-Za-z0-9._~/-]{0,128}$.
func ProbePath(p string) bool {
	if len(p) == 0 || len(p) > 129 || p[0] != '/' {
		return false
	}
	for i := 1; i < len(p); i++ {
		c := p[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '~' || c == '/' || c == '-') {
			return false
		}
	}
	return true
}

// ProbeIPv4 says whether s is a target address for an own probe: a public unicast IPv4 in its canonical dotted form. Besides the
// private, loopback, link-local and multicast ranges the netip predicates know, it refuses "this network" (0/8), carrier-grade NAT
// (100.64/10), the IETF protocol block (192.0.0/24), the benchmarking block (198.18/15) and the reserved class E (240/4). The
// ranges are in code, not in a package variable: the Cloudflare Workers link this package and never call it.
func ProbeIPv4(s string) bool {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4() || a.String() != s || !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() ||
		a.IsMulticast() || a.IsUnspecified() {
		return false
	}
	b := a.As4()
	switch {
	case b[0] == 0, b[0] == 100 && b[1]&0xc0 == 64, b[0] == 192 && b[1] == 0 && b[2] == 0, b[0] == 198 && b[1]&0xfe == 18, b[0] >= 240:
		return false
	}
	return true
}
