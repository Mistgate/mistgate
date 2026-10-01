//go:build linux

package hostctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
)

// tunnelState is what the host owner remembers about the tunnel table. The table is replaced as a whole, which
// resets its counters, so the last value of every counter is carried over: udp_rx_packets only ever grows.
type tunnelState struct {
	mu        sync.Mutex
	set       []Tunnel
	applied   bool // set was handed to nft (an empty set means the table is gone)
	forwardOK bool // and forwarding is on; false retries the sysctls with the next call
	carry     map[uint16]uint64
}

var _ TunnelHost = (*linuxHost)(nil)

func (h *linuxHost) SetTunnels(ctx context.Context, ts []Tunnel) error {
	st := &h.tun
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.applied && st.forwardOK && TunnelsEqual(ts, st.set) {
		return nil
	}
	script, err := RenderTunnels(ts) // reject before anything is sent to nft
	if err != nil {
		return err
	}
	old, keep := map[uint16]bool{}, map[uint16]bool{}
	for _, t := range st.set {
		old[t.UDPPort] = true
	}
	for _, t := range ts {
		keep[t.UDPPort] = true
	}
	if st.applied && len(st.set) > 0 {
		if raw, err := h.readCounters(ctx); err == nil {
			if st.carry == nil {
				st.carry = map[uint16]uint64{}
			}
			for p, v := range raw {
				if old[p] && keep[p] { // a port that goes away and comes back starts from zero
					st.carry[p] += v
				}
			}
		}
	}
	for p := range st.carry {
		if !keep[p] {
			delete(st.carry, p)
		}
	}
	st.applied, st.forwardOK = false, false
	if err := h.nft(ctx, script, len(ts) == 0); err != nil {
		st.set = nil
		return err
	}
	st.set, st.applied = append([]Tunnel(nil), ts...), true
	if len(ts) > 0 {
		if err := h.enableForwarding(NeedsV6(ts)); err != nil {
			return err
		}
	}
	st.forwardOK = true
	return nil
}

func (h *linuxHost) TunnelCounters(ctx context.Context) (map[uint16]uint64, error) {
	st := &h.tun
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.applied || len(st.set) == 0 {
		return map[uint16]uint64{}, nil
	}
	raw, err := h.readCounters(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[uint16]uint64, len(raw))
	for _, t := range st.set {
		out[t.UDPPort] = st.carry[t.UDPPort] + raw[t.UDPPort]
	}
	return out, nil
}

type nftCounters struct {
	Nftables []struct {
		Counter *struct {
			Name    string `json:"name"`
			Packets uint64 `json:"packets"`
		} `json:"counter"`
	} `json:"nftables"`
}

// readCounters reads the udp_<port> counters of the installed table.
func (h *linuxHost) readCounters(ctx context.Context) (map[uint16]uint64, error) {
	out, err := h.run(ctx, "", "nft", "-j", "list", "counters", "table", nftFamily, NftTunnelTable)
	if err != nil {
		return nil, fmt.Errorf("nft list counters: %w: %s", err, bytes.TrimSpace(out))
	}
	res := map[uint16]uint64{}
	if len(bytes.TrimSpace(out)) == 0 {
		return res, nil
	}
	var doc nftCounters
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("nft counters: %w", err)
	}
	for _, it := range doc.Nftables {
		if it.Counter == nil || !strings.HasPrefix(it.Counter.Name, "udp_") {
			continue
		}
		if p, err := strconv.ParseUint(strings.TrimPrefix(it.Counter.Name, "udp_"), 10, 16); err == nil {
			res[uint16(p)] = it.Counter.Packets
		}
	}
	return res, nil
}

// enableForwarding turns on IPv4 forwarding and, for a tunnel with an IPv6 subnet, IPv6 forwarding. Enabling IPv6
// forwarding makes the kernel stop accepting router advertisements on an interface whose accept_ra is 1, which cuts
// the host's own IPv6 connectivity on SLAAC networks; those interfaces are moved to accept_ra=2 first (accept RAs even
// when forwarding). Live values only: the agent re-applies them at every start, and Cleanup leaves them (like the
// baseline, the previous values are not recorded).
func (h *linuxHost) enableForwarding(v6 bool) error {
	var errs []error
	if err := h.setProcSys("net/ipv4/ip_forward", "1"); err != nil {
		errs = append(errs, err)
	}
	if v6 {
		fwd := filepath.Join(h.procSys, "net/ipv6/conf/all/forwarding")
		if cur, err := os.ReadFile(fwd); err == nil && strings.TrimSpace(string(cur)) != "1" {
			ras, _ := filepath.Glob(filepath.Join(h.procSys, "net/ipv6/conf/*/accept_ra"))
			for _, p := range ras {
				if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == "1" {
					if err := os.WriteFile(p, []byte("2"), 0o644); err != nil {
						errs = append(errs, fmt.Errorf("sysctl %s=2: %w", p, err))
					}
				}
			}
		}
		if err := h.setProcSys("net/ipv6/conf/all/forwarding", "1"); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (h *linuxHost) setProcSys(rel, val string) error {
	p := filepath.Join(h.procSys, rel)
	if cur, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(cur)) == val {
		return nil
	}
	if err := os.WriteFile(p, []byte(val), 0o644); err != nil {
		return fmt.Errorf("sysctl %s=%s: %w", strings.ReplaceAll(rel, "/", "."), val, err)
	}
	return nil
}

// ownLink says whether a network link is one the agent created: the tunnel interfaces of the AWG engine and the WARP device.
func ownLink(name string) bool {
	return (strings.HasPrefix(name, TunnelIfacePrefix) && len(name) > len(TunnelIfacePrefix)) || name == WarpIface
}

// deleteOwnLinks removes every link that is ours by name, and nothing else.
func deleteOwnLinks() error {
	links, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("list links: %w", err)
	}
	var errs []error
	for _, l := range links {
		if name := l.Attrs().Name; ownLink(name) {
			if err := netlink.LinkDel(l); err != nil {
				errs = append(errs, fmt.Errorf("delete link %s: %w", name, err))
			}
		}
	}
	return errors.Join(errs...)
}

// cleanupTunnels deletes the tunnel table and the links of the AWG engine and the WARP device. Quiet without nft.
func (h *linuxHost) cleanupTunnels(ctx context.Context) error {
	script, _ := RenderTunnels(nil)
	h.tun.mu.Lock()
	h.tun.set, h.tun.applied, h.tun.forwardOK, h.tun.carry = nil, false, false, nil
	err := h.nft(ctx, script, true)
	h.tun.mu.Unlock()
	errs := []error{err}
	if h.links != nil {
		errs = append(errs, h.links())
	}
	return errors.Join(errs...)
}

// CleanupTunnels is the stateless form for the unit's ExecStopPost (a crashed agent leaves its interfaces and its
// table behind): it deletes the tunnel table and every link named mgawg* or mgwarp. The WARP routing rules and routes
// are removed by internal/node/warp.CleanupHost, which the same subcommand calls.
func CleanupTunnels(ctx context.Context) error {
	script, _ := RenderTunnels(nil)
	h := &linuxHost{run: execRunner}
	return errors.Join(h.nft(ctx, script, true), deleteOwnLinks())
}
