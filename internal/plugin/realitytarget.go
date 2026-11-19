package plugin

import (
	"encoding/json"
	"strings"
)

// RealityTarget is the site a VLESS REALITY inbound shows to strangers when that is another site than the node's own: its
// target ("host:port") and the first server name the node asks it for (what the engine's own probe sends). ok is false for
// every other inbound: other protocols, TLS, the node's own site (target "self"), and settings that cannot be read.
func RealityTarget(s InboundSpec) (target, name string, ok bool) {
	if s.Protocol != "vless" {
		return "", "", false
	}
	var j struct {
		Security string `json:"security"`
		Reality  struct {
			Target      string   `json:"target"`
			ServerNames []string `json:"server_names"`
		} `json:"reality"`
	}
	if len(s.Settings) == 0 || json.Unmarshal(s.Settings, &j) != nil || j.Security != "reality" {
		return "", "", false
	}
	target = strings.TrimSpace(j.Reality.Target)
	if target == "" || target == "self" || len(j.Reality.ServerNames) == 0 {
		return "", "", false
	}
	name = strings.TrimSuffix(strings.TrimSpace(j.Reality.ServerNames[0]), ".")
	return target, name, name != ""
}
