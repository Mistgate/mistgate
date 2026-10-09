package hysteria2

import "github.com/mistgate/mistgate/internal/node/torrentguard"

// torrentPort reports only the numeric destination port. The host stays on the node.
func torrentPort(addr string) string { return torrentguard.Port(addr) }
