package protocols

// Refusals of BuildInbound that the admin UI words itself: access turns them into coded errors ("acme_needs_domain",
// "port_in_hop", "sni_invalid"; web/src/lib/errors.ts). Error() stays the English sentence, which is also what a node
// page shows as last_error when a stored inbound stops building.

// NeedsDomainError: the certificate is Let's Encrypt, which is issued for a DNS name only, and the inbound has none:
// the node address is an IP and no SNI is set (neither on the inbound nor in the profile).
type NeedsDomainError struct{ Address string }

func (*NeedsDomainError) Error() string {
	return "a host name is required for a Let's Encrypt certificate: the node address is an IP, set an SNI"
}

// PortInHopError: the port override lies inside the profile's own port-hopping range.
type PortInHopError struct{ From, To int }

func (*PortInHopError) Error() string { return "port override lies inside the hop range" }

// ServerNameError: the server name set for the inbound is not usable: an IP where Let's Encrypt needs a domain
// (DomainOnly), or neither a host name nor an IP.
type ServerNameError struct {
	Name       string
	DomainOnly bool
}

func (e *ServerNameError) Error() string {
	if e.DomainOnly {
		return "tls server name override: must be a host name (not an IP address) for a Let's Encrypt certificate"
	}
	return "tls server name override: must be a host name or an IP address"
}
