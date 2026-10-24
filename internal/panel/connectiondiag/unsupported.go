// Package connectiondiag holds shared interpretation for the connection counter API and MCP tools.
package connectiondiag

import (
	"slices"
	"strings"
)

const Links2Capability = "links/2"

var (
	link2Fields = []string{"client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille", "rtt_samples", "pkts_sent"}
	unsupported = map[string][]string{
		"awg": append([]string{"rejects", "drops", "hellos", "handshakes"}, link2Fields...),
		// VLESS sessions currently supply online_s; every other connection field is unsupported.
		"vless": {"connects", "rejects", "drops", "stalls", "unknown_keys", "hellos", "handshakes", "keys_stalled", "keys_tried",
			"client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille", "rtt_samples", "pkts_sent"},
	}
	hysteria2WithoutLinks2 = []string{"client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille"}
)

// UnsupportedCounters returns protocol-level counters this protocol does not report. A fresh slice keeps callers from mutating
// shared values. Hysteria2 is treated as an older agent unless its capability is supplied to UnsupportedCountersForNode.
func UnsupportedCounters(protocol string) []string { return UnsupportedCountersForNode(protocol, nil) }

// UnsupportedCountersForNode also accounts for node-agent capabilities. links/2 is the Hysteria2-only close and path-quality
// contract; the other protocols do not support these fields even if the agent lists the capability.
func UnsupportedCountersForNode(protocol string, agentCaps []string) []string {
	protocol = strings.ToLower(protocol)
	if protocol == "hysteria2" {
		if slices.Contains(agentCaps, Links2Capability) {
			return []string{}
		}
		return append([]string(nil), hysteria2WithoutLinks2...)
	}
	values, ok := unsupported[protocol]
	if !ok {
		return []string{}
	}
	return append([]string(nil), values...)
}

// SupportsHysteria2Links2 reports whether this protocol/node pair can produce the close and path-quality fields.
func SupportsHysteria2Links2(protocol string, supportsLinks2 bool) bool {
	return strings.EqualFold(protocol, "hysteria2") && supportsLinks2
}

// UnsupportedDescription is the common note used by MCP descriptions and tests.
func UnsupportedDescription() string {
	return "unsupported names counters the current protocol or agent does not report; their zero values mean nothing (AWG: rejects, drops, hellos, handshakes and all links/2 fields; VLESS: every connection counter except online_s; Hysteria2 without links/2: client_closes, freezes, quiet, accepted, hellos_foreign, rtt_ms and loss_permille)."
}
