package protocols

import (
	"net"
	"strings"
)

// IsHostname reports whether host is a DNS host name made of letters, digits, hyphens and dots: not an IP address, no port, no
// brackets, no empty or over-long label. (A name that needs a dot, as a TLS name does, is the caller's check.)
func IsHostname(host string) bool {
	if host == "" || len(host) > 253 || net.ParseIP(host) != nil || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}
