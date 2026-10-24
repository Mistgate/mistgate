// Package logscrub cuts IP addresses out of log text that leaves a node (the node journal, agent.proto JournalEntry). The agent
// cleans before sending and the panel cleans again on intake. Names, versions, times and hashes stay, except in xray's text
// (Hosts), where a name may be a destination.
package logscrub

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// IP replaces an address; IPPort an address with a port (the port too: a client's source port is noise and
	// makes every line unique).
	IP     = "<ip>"
	IPPort = "<ip>:port"
	Host   = "<host>"
)

// JournalAttrs are the only attribute keys a journal entry carries, on the node and again in the panel (agent.proto
// JournalEntry).
var JournalAttrs = [...]string{
	"inbound", "protocol", "profile", "port", "state", "reason", "code", "backend", "mode", "version",
	"kernel", "iface", "check", "fix", "count", "err", "error",
}

// candidate finds what may be an address: a bracketed IPv6 (with zone and port), an IPv4 (with port), or an unbracketed IPv6
// (two colons or more, a zone, a dotted IPv4 tail). Every match is checked with netip before it is replaced.
var candidate = regexp.MustCompile(`\[[0-9A-Fa-f:.]+(?:%[0-9A-Za-z_.-]+)?\](?::[0-9]{1,5})?` +
	`|[0-9]{1,3}(?:\.[0-9]{1,3}){3}(?::[0-9]{1,8})?` +
	`|(?:[0-9A-Fa-f]{0,4}:){2,7}(?:[0-9]{1,3}(?:\.[0-9]{1,3}){3}|[0-9A-Fa-f]{1,4})?(?:%[0-9A-Za-z_.-]+)?`)

// Addrs replaces every IP address in s by IP, or by IPPort when it carries a port. Unspecified and loopback addresses
// (0.0.0.0, ::, 127.0.0.1, ::1) are the node's own and stay. Addrs(Addrs(s)) == Addrs(s).
func Addrs(s string) string {
	if !strings.ContainsAny(s, ".:") {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range candidate.FindAllStringIndex(s, -1) {
		i, j := m[0], m[1]
		if dotted(s[i:j]) && (i > 0 && digitDot(s[i-1]) || j < len(s) && digitDot(s[j])) {
			continue // four numbers inside a longer run of digits and dots: a version, not an address
		}
		b.WriteString(s[last:i])
		b.WriteString(replace(s[i:j]))
		last = j
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// dotted: the IPv4 form of the candidate pattern (letters may touch it: "remote203.0.113.7x" is still an address).
func dotted(m string) bool {
	return m[0] != '[' && strings.Count(m, ":") <= 1 && strings.Contains(m, ".")
}

func digitDot(c byte) bool { return c == '.' || '0' <= c && c <= '9' }

// host is a DNS name with a letter (or punycode) top-level label.
var host = regexp.MustCompile(`(?i)\b(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+(?:xn--[a-z0-9-]{1,59}|[a-z]{2,63})\b`)

// Hosts replaces every DNS name by Host. For text that may name a destination (xray's), where a lost file or domain name
// costs less than a site someone visited.
func Hosts(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	return host.ReplaceAllString(s, Host)
}

func replace(m string) string {
	// An unbracketed IPv6 match may take the colon or dot that follows the address ("fe80::1:" at the end of a clause).
	body := m
	for body != "" {
		if r, ok := parse(body); ok {
			return r + m[len(body):]
		}
		if last := body[len(body)-1]; last != ':' && last != '.' {
			break
		}
		body = body[:len(body)-1]
	}
	return m
}

func parse(s string) (string, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		if own(ap.Addr()) {
			return s, true
		}
		return IPPort, true
	}
	a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	if err != nil {
		// an IPv4 with a port netip refuses (":99999") is still an address
		if k := strings.LastIndexByte(s, ':'); k > 0 && !strings.Contains(s[:k], ":") {
			if a, err := netip.ParseAddr(s[:k]); err == nil && a.Is4() {
				if own(a) {
					return s, true
				}
				return IPPort, true
			}
		}
		return "", false
	}
	if own(a) {
		return s, true
	}
	return IP, true
}

func own(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsUnspecified() || a.IsLoopback()
}

// Clip cuts s to at most max bytes on a rune boundary.
func Clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// ClipRunes cuts s to at most max runes.
func ClipRunes(s string, max int) string {
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}
