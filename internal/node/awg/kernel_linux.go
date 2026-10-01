//go:build linux

package awg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/vishvananda/netlink"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awgnl"
)

// kernel drives the amneziawg kernel module: rtnetlink creates the link (kind "amneziawg"), our genetlink
// client (awgnl) sets the key, port, obfuscation and peers. It never shells out to `awg` and never links
// awgctrl-go (genl v2 only). Not exercised in WSL (its kernel has no module): the byte-level encoding is covered by
// awgnl's tests against genltest, the netlink calls are the ones that were checked on a real module.
type kernel struct {
	log  *slog.Logger
	cl   *awgnl.Client
	stop func() // ends the "auth" multicast watcher

	mu      sync.Mutex
	unknown map[string]uint32 // unknown-peer events per interface since the last take
}

func newKernel(log *slog.Logger) (*kernel, error) {
	cl, err := awgnl.Dial()
	if err != nil {
		return nil, err
	}
	k := &kernel{log: log, cl: cl, unknown: map[string]uint32{}}
	// Best effort (never ran against a real module): without the group, only the
	// counter stays 0.
	if stop, err := cl.WatchUnknownPeers(func(ifname string) {
		k.mu.Lock()
		k.unknown[ifname]++
		k.mu.Unlock()
	}); err != nil {
		log.Info("awg: the \"auth\" multicast group is not watched", "err", err)
	} else {
		k.stop = stop
	}
	return k, nil
}

func (k *kernel) Name() string { return "kernel" }
func (k *kernel) Is31() bool   { return k.cl.Info.Is31() }
func (k *kernel) Version() string {
	return fmt.Sprintf("amneziawg kernel module, genl v%d, maxattr %d", k.cl.Info.GenlVersion, k.cl.Info.MaxAttr)
}

func (k *kernel) Create(ctx context.Context, name string, d deviceConfig) error {
	_ = deleteLink(name) // never reuse a leftover of an earlier run: its key and parameters are unknown
	la := netlink.NewLinkAttrs()
	la.Name, la.MTU = name, int(d.Tunnel.MTU)
	if err := netlink.LinkAdd(&netlink.GenericLink{LinkAttrs: la, LinkType: "amneziawg"}); err != nil {
		return fmt.Errorf("create link %s: %w", name, err)
	}
	if err := k.cl.SetDevice(name, d.PrivateKey, d.Port, d.Obf, nil); err != nil {
		_ = deleteLink(name)
		return fmt.Errorf("configure %s (the module answers EINVAL without a reason): %w", name, err)
	}
	// The module binds its socket when the link goes up, so a busy port surfaces here.
	warn, err := configureLink(name, d.Tunnel)
	if err != nil {
		_ = deleteLink(name)
		return err
	}
	if warn != nil {
		k.log.Warn("awg interface: IPv6 address not set, the tunnel runs IPv4 only", "iface", name, "err", warn)
	}
	return nil
}

func (k *kernel) SetPeers(ctx context.Context, name string, replace bool, peers []awgcfg.Peer) error {
	return k.cl.SetPeers(name, replace, peers)
}

func (k *kernel) Stats(ctx context.Context, name string) ([]awgcfg.PeerStat, error) {
	return k.cl.Stats(name)
}

func (k *kernel) SetMTU(ctx context.Context, name string, mtu int) error {
	return setLinkMTU(name, mtu)
}
func (k *kernel) LinkUp(name string) bool                        { return linkIsUp(name) }
func (k *kernel) Destroy(ctx context.Context, name string) error { return deleteLink(name) }

func (k *kernel) TakeUnknownPeers(name string) uint32 {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := k.unknown[name]
	delete(k.unknown, name)
	return n
}

func (k *kernel) Close() error {
	if k.stop != nil {
		k.stop()
	}
	return k.cl.Close()
}
