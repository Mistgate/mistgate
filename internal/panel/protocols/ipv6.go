package protocols

import "github.com/mistgate/mistgate/internal/plugin"

// IPv6Unsupported says why a server has no IPv6 twin, "" when it has: a CDN host or a certificate for the node's IPv4 address
// cannot be used when people connect directly to the node's IPv6 address. The same two reasons keep a relay entry from passing
// the server on: people reach a CDN server through the CDN, and a certificate on the exit's IP address would not fit the entry's.
func IPv6Unsupported(spec plugin.InboundSpec) string {
	switch {
	case spec.TLS.Mode == plugin.TLSAcmeIP:
		return "acme_ip"
	case plugin.ServerCDN(spec):
		return "cdn"
	}
	return ""
}
