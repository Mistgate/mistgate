// Package connectiondiag holds shared interpretation for the connection counter API and MCP tools.
package connectiondiag

import (
	"encoding/json"
	"slices"
	"strings"
)

const Links2Capability = "links/2"
const Links3Capability = "links/3"
const VLESSUnknownKeysCapability = "vless-unknown-keys/1"

var (
	link2Fields = []string{"client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille", "rtt_samples", "pkts_sent"}
	// Link3Fields are the counters of agents that list links/3 (agent.proto LinkCounters from hs_unfinished on).
	Link3Fields = []string{"hs_unfinished", "tls_alerts_sent", "tls_alerts_recv", "tls_cert_refused", "reality_fallback", "auth_failed",
		"tcp_stalls", "tcp_stalls_16k", "initiations", "responses", "responses_unmatched"}
	// KeyLink3Fields are the links/3 counters a key's row can carry (a device row of GetUserConnection).
	KeyLink3Fields = []string{"tcp_stalls", "tcp_stalls_16k", "initiations", "responses", "responses_unmatched"}
	unsupported    = map[string][]string{
		"awg": append([]string{"rejects", "drops", "hellos", "handshakes"}, link2Fields...),
		// VLESS sessions supply online_s; vless-unknown-keys/1 agents additionally report unknown_keys per inbound, and links/3
		// agents hellos, hellos_foreign and connects (see vlessSupported).
		"vless": {"connects", "rejects", "drops", "stalls", "unknown_keys", "hellos", "handshakes", "keys_stalled", "keys_tried",
			"client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille", "rtt_samples", "pkts_sent"},
	}
	hysteria2WithoutLinks2 = []string{"client_closes", "freezes", "quiet", "accepted", "hellos_foreign", "rtt_ms", "loss_permille", "rtt_samples", "pkts_sent"}
)

// Variant is what decides, beyond the protocol and the agent, which counters an inbound can have: the AmneziaWG backend the
// node last reported ("" = no report yet), and the VLESS security ("reality" or "tls"), transport ("tcp", "xhttp", "ws") and
// whether it is reached through a CDN. "" means not known, and a counter that depends on it is then not known either.
type Variant struct {
	AwgBackend          string
	Security, Transport string
	CDN                 bool
}

// VLESSVariant reads the variant from a VLESS profile's settings_json (no secret is in it); anything unreadable is "".
func VLESSVariant(settingsJSON string) Variant {
	var s struct {
		Security  string `json:"security"`
		Transport string `json:"transport"`
		CDN       struct {
			Host string `json:"host"`
		} `json:"cdn"`
	}
	if json.Unmarshal([]byte(settingsJSON), &s) != nil {
		return Variant{}
	}
	return Variant{Security: s.Security, Transport: s.Transport, CDN: s.CDN.Host != ""}
}

// UnsupportedCounters returns protocol-level counters this protocol does not report. A fresh slice keeps callers from mutating
// shared values. Hysteria2 and VLESS are treated as older agents unless their capability is supplied to UnsupportedCountersForNode.
func UnsupportedCounters(protocol string) []string { return UnsupportedCountersForNode(protocol, nil) }

// UnsupportedCountersForNode is UnsupportedCountersFor without a variant: what depends on the backend or the VLESS transport is
// not known.
func UnsupportedCountersForNode(protocol string, agentCaps []string) []string {
	return UnsupportedCountersFor(protocol, agentCaps, Variant{})
}

// UnsupportedCountersFor also accounts for node-agent capabilities and the inbound's variant. links/2 is the Hysteria2-only close
// and path-quality contract, vless-unknown-keys/1 enables the VLESS unknown-user-ID counter, and links/3 the handshake-failure
// reasons each engine can see (agent.proto LinkCounters).
func UnsupportedCountersFor(protocol string, agentCaps []string, v Variant) []string {
	protocol = strings.ToLower(protocol)
	links3 := slices.Contains(agentCaps, Links3Capability)
	var out []string
	switch protocol {
	case "hysteria2":
		if !slices.Contains(agentCaps, Links2Capability) {
			out = append(out, hysteria2WithoutLinks2...)
		}
	default:
		values, ok := unsupported[protocol]
		if !ok {
			return []string{}
		}
		out = append(out, values...)
		if protocol == "vless" && slices.Contains(agentCaps, VLESSUnknownKeysCapability) {
			out = slices.DeleteFunc(out, func(f string) bool { return f == "unknown_keys" })
		}
	}
	for _, f := range Link3Fields {
		if !links3 || !link3Supported(protocol, f, v) {
			out = append(out, f)
		}
	}
	if protocol == "vless" && links3 {
		out = slices.DeleteFunc(out, func(f string) bool {
			return f == "hellos" || f == "hellos_foreign" || f == "connects" && v.Security == "reality" && v.Transport == "tcp"
		})
	}
	if out == nil {
		return []string{}
	}
	return out
}

// UnsupportedKeyCounters is the part of UnsupportedCountersFor that a key's row (GetUserConnection) can carry.
func UnsupportedKeyCounters(protocol string, agentCaps []string, v Variant) []string {
	all := UnsupportedCountersFor(protocol, agentCaps, v)
	out := []string{}
	for _, f := range KeyLink3Fields {
		if slices.Contains(all, f) || !keyLink3(protocol, f, v) {
			out = append(out, f)
		}
	}
	return out
}

// link3Supported says which engine counts which links/3 counter on the inbound (its keys' rows included).
func link3Supported(protocol, field string, v Variant) bool {
	switch protocol {
	case "hysteria2":
		return field == "hs_unfinished" || field == "tls_alerts_sent" || field == "tls_alerts_recv" || field == "tls_cert_refused"
	case "awg":
		return v.AwgBackend == "userspace" && (field == "initiations" || field == "responses" || field == "responses_unmatched")
	case "vless":
		switch field {
		case "hs_unfinished":
			return v.Security == "reality" || v.Security == "tls"
		case "reality_fallback", "auth_failed":
			return v.Security == "reality"
		case "tls_alerts_sent", "tls_alerts_recv", "tls_cert_refused":
			return v.Security == "tls"
		case "tcp_stalls", "tcp_stalls_16k":
			return v.Security == "reality" && v.Transport == "tcp" || v.Security == "tls" && !v.CDN
		}
	}
	return false
}

// keyLink3 says whether the counter is put on the key (VLESS behind its TLS front counts the stall on the inbound row).
func keyLink3(protocol, field string, v Variant) bool {
	switch field {
	case "tcp_stalls", "tcp_stalls_16k":
		return protocol == "vless" && v.Security == "reality" && v.Transport == "tcp"
	}
	return protocol == "awg"
}

// SupportsHysteria2Links2 reports whether this protocol/node pair can produce the close and path-quality fields.
func SupportsHysteria2Links2(protocol string, supportsLinks2 bool) bool {
	return strings.EqualFold(protocol, "hysteria2") && supportsLinks2
}

// UnsupportedDescription is the common note used by MCP descriptions and tests.
func UnsupportedDescription() string {
	return "unsupported names counters the current protocol, its transport, the AWG backend or the agent does not report; their zero values mean nothing (AWG: rejects, drops, hellos, handshakes and all links/2 fields; VLESS: unknown_keys is supported with vless-unknown-keys/1, hellos, hellos_foreign and over REALITY TCP connects with links/3, and every other connection counter except online_s is unsupported; Hysteria2 without links/2: client_closes, freezes, quiet, accepted, hellos_foreign, rtt_ms, loss_permille, rtt_samples and pkts_sent; links/3 reasons only where the engine sees them: Hysteria2 hs_unfinished and the tls_* alerts, VLESS REALITY reality_fallback, auth_failed and hs_unfinished, VLESS tls hs_unfinished and the tls_* alerts, VLESS tcp_stalls over REALITY TCP or a direct TLS front, AWG initiations, responses and responses_unmatched on the userspace backend only (the kernel backend cannot tell them apart))."
}
