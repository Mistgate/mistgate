package awg

import (
	"context"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/plugin"
)

// deviceConfig is what a backend builds an interface from.
type deviceConfig struct {
	Port       uint16
	PrivateKey [32]byte
	Version    string
	Obf        awgcfg.Obfuscation
	Tunnel     plugin.Tunnel
}

// awgBackend is one way to run an AmneziaWG interface: the kernel module (own genetlink client) or amneziawg-go
// inside the agent process. One interface = one profile = one UDP port; peers are changed
// on the live interface and never by "remove + add" of an unchanged active peer (the client would freeze for 15 s).
// The engine serialises calls; a backend may assume that.
type awgBackend interface {
	Name() string    // "kernel" | "userspace"
	Version() string // EngineInfo text: "amneziawg-go v3.1.20260828" | "amneziawg kernel module, genl v3, maxattr 34"
	Is31() bool      // knows RandomTrailers, DisableCookies and the other 3.1 keys
	// Create builds the interface (key, port, obfuscation, address, MTU) and brings it up. Anything half-built is
	// removed on failure; an existing interface of the same name is replaced, never reused.
	Create(ctx context.Context, name string, d deviceConfig) error
	// SetPeers applies peer operations in order. replace drops all other peers first. The engine sends batches
	// of at most peerBatch with the removals first.
	SetPeers(ctx context.Context, name string, replace bool, peers []awgcfg.Peer) error
	// Stats dumps the peers with counters, last handshake, endpoint and allowed ips.
	Stats(ctx context.Context, name string) ([]awgcfg.PeerStat, error)
	SetMTU(ctx context.Context, name string, mtu int) error
	// LinkUp reports whether the interface exists and is up.
	LinkUp(name string) bool
	Destroy(ctx context.Context, name string) error
	Close() error
}

// unknownPeerSource is implemented by the kernel backend: handshake initiations from unknown keys since the last
// call (multicast group "auth"). Optional; userspace has no equivalent.
type unknownPeerSource interface {
	TakeUnknownPeers(name string) uint32
}

// peerBatch is the largest number of peers in one SetPeers call (one netlink attribute holds 64 KiB).
const peerBatch = 250

// BackendStatus says which backend the engine runs, or why there is none.
type BackendStatus struct {
	Mode      string // what was asked: auto | kernel | userspace
	Name      string // "kernel" | "userspace"; "" when unavailable
	Version   string
	Available bool
	Reason    string // when unavailable: a short English fact, e.g. "no /dev/net/tun"
}
