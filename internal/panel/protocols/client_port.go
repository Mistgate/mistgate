package protocols

import "encoding/json"

// ClientPort is the public port at the node address, independently of its listener.
// Callers build/validate the profile first. CDN endpoints and relay entry overrides
// are selected separately by the subscription renderer.
func ClientPort(settings json.RawMessage, listenPort uint16) uint16 {
	var endpoint struct {
		Port uint16 `json:"client_port"`
	}
	if json.Unmarshal(settings, &endpoint) == nil && endpoint.Port != 0 {
		return endpoint.Port
	}
	return listenPort
}
