// Package connectiondiag holds shared interpretation for the connection counter API and MCP tools.
package connectiondiag

import "strings"

var unsupported = map[string][]string{
	"awg": {"rejects", "drops", "hellos", "handshakes"},
	// VLESS sessions currently supply online_s; the event counters remain unsupported.
	"vless": {"connects", "rejects", "drops", "stalls", "unknown_keys", "hellos", "handshakes"},
}

// UnsupportedCounters returns counters this protocol does not report. A fresh slice keeps callers from mutating shared values.
func UnsupportedCounters(protocol string) []string {
	values, ok := unsupported[strings.ToLower(protocol)]
	if !ok {
		return []string{}
	}
	return append([]string(nil), values...)
}

// UnsupportedDescription is the common note used by MCP descriptions and tests.
func UnsupportedDescription() string {
	return "unsupported names counters this protocol does not report; their zero values mean nothing (AWG: rejects, drops, hellos, handshakes; VLESS: connects, rejects, drops, stalls, unknown_keys, hellos, handshakes; online_s is counted from VLESS sessions)."
}
