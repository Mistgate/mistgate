package access

import (
	"strings"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

type pathKind uint8

const (
	pathDirect pathKind = iota // the node's own address
	pathIPv6                   // the node's IPv6 entry
	pathRelay                  // the exit's server through the entry node of a relay
)

// serverPath is one way a person reaches one server of a node.
type serverPath struct {
	Kind  pathKind
	Key   string // SubServer.PathKey; an AmneziaWG credential holds "" for the direct path
	Mark  string // the mark of the twin server: "" for the direct path
	Addr  string // pathIPv6: the address of the entry
	Relay store.RelayLink
}

// serverPaths lists the ways to the server f of node n, the direct one first, then the IPv6 entry, then each of relays (the relays
// of this server that the caller found usable for the person). A server that cannot go to another address (protocols.IPv6Unsupported:
// a CDN host, a certificate for the node's IP) has the direct path alone. What a protocol cannot carry over a path (TCP only, a
// per-device key) is the caller's to skip.
func serverPaths(n store.AccessNode, spec plugin.InboundSpec, relays []store.RelayLink) []serverPath {
	paths := []serverPath{{Kind: pathDirect, Key: store.PathDirect}}
	if protocols.IPv6Unsupported(spec) != "" {
		return paths
	}
	if addr := ipv6EntryAddr(n); addr != "" {
		paths = append(paths, serverPath{Kind: pathIPv6, Key: store.PathIPv6, Mark: MarkIPv6, Addr: addr})
	}
	for _, l := range relays {
		paths = append(paths, serverPath{Kind: pathRelay, Key: store.RelayPath(l.ViaNodeID), Mark: ViaMark(l.ViaNodeCountry), Relay: l})
	}
	return paths
}

// server is srv, the direct server, as it is on this path.
func (p serverPath) server(srv SubServer) SubServer {
	if p.Kind != pathDirect {
		srv.Mark, srv.PathKey = p.Mark, p.Key
	}
	return srv
}

// input is the render input of the direct server in for this path.
func (p serverPath) input(in protocols.RenderInput, f store.AccessInboundFull, spec plugin.InboundSpec) protocols.RenderInput {
	switch p.Kind {
	case pathIPv6:
		return ipv6Input(in, p.Addr)
	case pathRelay:
		return twinInput(in, f, spec, p.Relay)
	}
	return in
}

// conn is the way the page of the person lists the server on this path, out through exit.
func (p serverPath) conn(exit string) SubConn {
	c := SubConn{Way: "link", Exit: exit}
	switch p.Kind {
	case pathIPv6:
		c.IPv6 = true
	case pathRelay:
		c.Via, c.Relay = strings.ToUpper(strings.TrimSpace(p.Relay.ViaNodeCountry)), true
	}
	return c
}
