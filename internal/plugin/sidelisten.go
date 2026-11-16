//go:build !js

package plugin

import "encoding/json"

// SideListens returns listeners a protocol adds beside its primary listener. TrustTunnel's optional HTTP/3
// endpoint uses UDP on the same port as its required TCP HTTP/2 endpoint.
func SideListens(spec InboundSpec) []Listen {
	if spec.Protocol != "trusttunnel" || spec.Listen.Network != "tcp" || spec.Listen.Port == 0 || !TrustTunnelH3(spec) {
		return nil
	}
	return []Listen{{Network: "udp", Port: spec.Listen.Port}}
}

// TrustTunnelH3 reports whether a TrustTunnel inbound's settings ({"h3":bool}) turn HTTP/3 on.
func TrustTunnelH3(spec InboundSpec) bool {
	var settings struct {
		H3 bool `json:"h3"`
	}
	return json.Unmarshal(spec.Settings, &settings) == nil && settings.H3
}
