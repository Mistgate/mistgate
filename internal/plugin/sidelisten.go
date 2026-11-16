package plugin

import "encoding/json"

// SideListens returns listeners a protocol adds beside its primary listener. TrustTunnel's optional HTTP/3
// endpoint uses UDP on the same port as its required TCP HTTP/2 endpoint.
func SideListens(spec InboundSpec) []Listen {
	if spec.Protocol != "trusttunnel" || spec.Listen.Network != "tcp" || spec.Listen.Port == 0 {
		return nil
	}
	var settings struct {
		H3 bool `json:"h3"`
	}
	if json.Unmarshal(spec.Settings, &settings) != nil || !settings.H3 {
		return nil
	}
	return []Listen{{Network: "udp", Port: spec.Listen.Port}}
}
