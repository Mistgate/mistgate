package plugin

import (
	"net"
	"strconv"
	"strings"
)

// RealityTarget is the site a VLESS REALITY inbound shows to strangers when that is another site than the node's own: its
// target ("host:port") and the first server name the node asks it for (what the engine's own probe sends). ok is false for
// every other inbound: other protocols, TLS, the node's own site (target "self"), and settings that cannot be read.
func RealityTarget(s InboundSpec) (target, name string, ok bool) {
	v, readable := VLESSOf(s)
	if !readable || v.Security != "reality" || v.Target == "" || v.TargetSelf() || len(v.ServerNames) == 0 {
		return "", "", false
	}
	name = strings.TrimSuffix(strings.TrimSpace(v.ServerNames[0]), ".")
	return v.Target, name, name != ""
}

// RealityTargetShown is what a VLESS REALITY inbound shows strangers, for a person to read: "self" (the node's own site), the target's
// "host:port", or "" for an inbound that is not VLESS REALITY.
func RealityTargetShown(s InboundSpec) string {
	if target, _, ok := RealityTarget(s); ok {
		return target
	}
	if v, ok := VLESSOf(s); ok && v.Security == "reality" && v.TargetSelf() {
		return "self"
	}
	return ""
}

// NormalizeRealityTarget is a REALITY target typed as text in the form the panel keeps it: trimmed, the host in lower case without
// a closing dot, the port as a number ("Shop.Example.com.:0443" is "shop.example.com:443"). Text that is not "host:port" comes back
// trimmed, for the panel to refuse.
func NormalizeRealityTarget(text string) string {
	text = strings.TrimSpace(text)
	host, port, err := net.SplitHostPort(text)
	if err != nil {
		return text
	}
	if n, err := strconv.ParseUint(port, 10, 16); err == nil {
		port = strconv.FormatUint(n, 10)
	}
	return net.JoinHostPort(strings.ToLower(strings.TrimSuffix(host, ".")), port)
}
