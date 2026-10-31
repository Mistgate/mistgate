package protocols

import (
	"encoding/json"

	"github.com/mistgate/mistgate/internal/plugin"
)

// ServerCDN says the client reaches this server through a CDN host, not the node's address.
func ServerCDN(spec plugin.InboundSpec) bool {
	var s struct {
		CDN *struct {
			Host string `json:"host"`
		} `json:"cdn"`
	}
	return len(spec.Settings) > 0 && json.Unmarshal(spec.Settings, &s) == nil && s.CDN != nil && s.CDN.Host != ""
}

// IPv6Unsupported says why a server has no IPv6 twin, "" when it has: a CDN host or a certificate for the node's IPv4 address
// cannot be used when people connect directly to the node's IPv6 address.
func IPv6Unsupported(spec plugin.InboundSpec) string {
	switch {
	case spec.TLS.Mode == plugin.TLSAcmeIP:
		return "acme_ip"
	case ServerCDN(spec):
		return "cdn"
	}
	return ""
}
