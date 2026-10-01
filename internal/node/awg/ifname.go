package awg

import "strconv"

// IfaceName is the name of the interface an inbound on a UDP port gets: "mgawg51842". The agent derives the same name
// for the firewall of the tunnel (hostctl.TunnelIface) and its Cleanup deletes links that carry the prefix; a test keeps
// them equal.
func IfaceName(port uint16) string { return ifacePrefix + strconv.Itoa(int(port)) }
