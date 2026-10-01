package hostctl

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// SSH brute-force guard: a per-source-IP rate limit on NEW connections to the sshd
// port(s), in the agent's own nft table. Only `ct state new` is matched, so an open session, scp or a
// sleeping ControlMaster is never touched, and nothing is ever banned: a source over the limit just has its
// SYNs dropped until its bucket refills, and a full set makes the rule stop matching (fail open), so the
// guard cannot lock the owner out. Loopback is exempt.
//
// Fixed 6/minute with a burst of 10 and /64 buckets for IPv6, not configurable; the panel's own
// provisioning SSH stays well below it. Add settings when a real deployment needs different numbers.
const (
	sshRate  = "6/minute"
	sshBurst = 10
	// An entry lives 10 minutes after its last NEW connection; size bounds the memory (fail open when full).
	sshSetSize  = 65536
	maxSSHPorts = 16
)

var (
	sshdTPortRe    = regexp.MustCompile(`(?mi)^\s*port\s+(\d{1,5})\s*$`)
	sshdConfPortRe = regexp.MustCompile(`(?mi)^\s*port\s+(\d{1,5})\s*(?:#.*)?$`)
	socketListenRe = regexp.MustCompile(`(?m)^Listen=.*:(\d{1,5}) \(Stream\)\s*$`)
)

func portsFrom(re *regexp.Regexp, s string) []uint16 {
	var out []uint16
	for _, m := range re.FindAllStringSubmatch(s, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n <= 65535 {
			out = append(out, uint16(n))
		}
	}
	return out
}

// normalizePorts sorts, deduplicates and caps a port list.
func normalizePorts(in []uint16) []uint16 {
	seen := map[uint16]bool{}
	var out []uint16
	for _, p := range in {
		if p != 0 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	if len(out) > maxSSHPorts {
		out = out[:maxSSHPorts]
	}
	return out
}

func portList(ports []uint16) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = strconv.Itoa(int(p))
	}
	return strings.Join(s, ", ")
}

// renderSSHGuard returns the set and chain declarations for the guard (inside `table inet ...`).
func renderSSHGuard(ports []uint16) (string, error) {
	if len(ports) == 0 {
		return "", nil
	}
	if len(ports) > maxSSHPorts {
		return "", fmt.Errorf("ssh guard: %d ports", len(ports))
	}
	for _, p := range ports {
		if p == 0 {
			return "", fmt.Errorf("ssh guard: port 0")
		}
	}
	var b strings.Builder
	for _, v := range []struct{ name, typ string }{{"ssh_v4", "ipv4_addr"}, {"ssh_v6", "ipv6_addr"}} {
		fmt.Fprintf(&b, "\tset %s {\n\t\ttype %s; flags dynamic,timeout; timeout 10m; size %d\n\t}\n", v.name, v.typ, sshSetSize)
	}
	b.WriteString("\tchain ssh {\n\t\ttype filter hook input priority filter; policy accept;\n\t\tiifname \"lo\" accept\n")
	fmt.Fprintf(&b, "\t\ttcp dport { %s } ct state new add @ssh_v4 { ip saddr limit rate over %s burst %d packets } drop comment \"ssh:new-rate\"\n", portList(ports), sshRate, sshBurst)
	fmt.Fprintf(&b, "\t\ttcp dport { %s } ct state new add @ssh_v6 { ip6 saddr & ffff:ffff:ffff:ffff:: limit rate over %s burst %d packets } drop comment \"ssh:new-rate\"\n", portList(ports), sshRate, sshBurst)
	b.WriteString("\t}\n")
	return b.String(), nil
}
