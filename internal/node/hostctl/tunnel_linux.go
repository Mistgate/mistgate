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
	"time"

	"github.com/vishvananda/netlink"
)

// tunnelState is what the host owner remembers about the tunnel table. The table is replaced as a whole, which
// resets its counters, so the last value of every counter is carried over: udp_rx_packets only ever grows.
type tunnelState struct {
	mu        sync.Mutex
	set       []Tunnel
	applied   bool   // set was handed to nft (an empty set means the table is gone)
	forwardOK bool   // and forwarding is on; false retries the sysctls with the next call
	v6verdict string // "" the IPv6 rule is as asked; "drop" or "none" when the kernel refused reject (TunnelV6Fallback)
	v6RetryAt time.Time
	now       func() time.Time // injectable for the fallback retry cadence tests
	carry     map[uint16]uint64
}

const tunnelV6RetryInterval = 10 * time.Minute

func (st *tunnelState) currentTime() time.Time {
	if st.now != nil {
		return st.now()
	}
	return time.Now()
}

var _ TunnelHost = (*linuxHost)(nil)

func (h *linuxHost) SetTunnels(ctx context.Context, ts []Tunnel) error {
	st := &h.tun
	st.mu.Lock()
	defer st.mu.Unlock()
	now := st.currentTime()
	same := st.applied && TunnelsEqual(ts, st.set)
	if same {
		if st.v6verdict != "" && now.Before(st.v6RetryAt) {
			if st.forwardOK {
				return nil
			}
			if len(ts) > 0 {
				if err := h.enableForwarding(NeedsV6(ts)); err != nil {
					return err
				}
			}
			st.forwardOK = true
			return nil
		}
		if st.forwardOK && st.v6verdict == "" {
			return nil
		}
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
		if raw, err := h.readCounters(ctx, NftTunnelTable, "udp_"); err == nil {
			if st.carry == nil {
				st.carry = map[uint16]uint64{}
			}
			for p, v := range raw {
				if old[p] && keep[p] { // a port that goes away and comes back starts from zero
					st.carry[p] += v.Packets
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
	verdict := ""
	if err := h.nft(ctx, script, len(ts) == 0); err != nil {
		if !asksTunnelV6Reject(ts) {
			st.set, st.v6verdict, st.v6RetryAt = nil, "", time.Time{}
			return err
		}
		// Retry the exact ruleset once so a transient nft failure does not make us install a weaker IPv6 rule.
		if retryErr := h.nft(ctx, script, false); retryErr != nil {
			// If reject is unsupported, preserve tunnel service with drop, then without the IPv6 rule.
			if verdict = h.tunnelsWithoutReject(ctx, ts); verdict == "" {
				st.set, st.v6verdict, st.v6RetryAt = nil, "", time.Time{}
				return retryErr
			}
		}
	}
	st.v6verdict = verdict
	if verdict == "" {
		st.v6RetryAt = time.Time{}
	} else {
		st.v6RetryAt = st.currentTime().Add(tunnelV6RetryInterval)
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

// tunnelsWithoutReject installs ts with the IPv6 verdict "drop" (apps wait for a timeout instead of an instant ICMP error,
// but IPv6 stays off), and failing that with no IPv6 rule at all. It returns "drop", "none", or "" when ts has no such rule
// or nft refuses the table for another reason too.
func (h *linuxHost) tunnelsWithoutReject(ctx context.Context, ts []Tunnel) string {
	if !asksTunnelV6Reject(ts) {
		return ""
	}
	if script, err := renderTunnels(ts, v6Drop); err == nil && h.nft(ctx, script, false) == nil {
		return "drop"
	}
	off := append([]Tunnel(nil), ts...)
	for i := range off {
		off[i].RejectV6 = false
	}
	if script, err := RenderTunnels(off); err == nil && h.nft(ctx, script, false) == nil {
		return "none"
	}
	return ""
}

func asksTunnelV6Reject(ts []Tunnel) bool {
	for _, t := range ts {
		if t.RejectV6 && t.Subnet6.IsValid() && !t.ViaWarp {
			return true
		}
	}
	return false
}

// TunnelV6Fallback tells how the installed table differs from the one asked for because the kernel refused reject: "drop"
// (IPv6 from the tunnels is dropped instead of rejected), "none" (no IPv6 rule at all), "" when it is as asked.
func (h *linuxHost) TunnelV6Fallback() string {
	h.tun.mu.Lock()
	defer h.tun.mu.Unlock()
	return h.tun.v6verdict
}

func (h *linuxHost) TunnelCounters(ctx context.Context) (map[uint16]uint64, error) {
	st := &h.tun
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.applied || len(st.set) == 0 {
		return map[uint16]uint64{}, nil
	}
	raw, err := h.readCounters(ctx, NftTunnelTable, "udp_")
	if err != nil {
		return nil, err
	}
	out := make(map[uint16]uint64, len(raw))
	for _, t := range st.set {
		out[t.UDPPort] = st.carry[t.UDPPort] + raw[t.UDPPort].Packets
	}
	return out, nil
}

type nftCounters struct {
	Nftables []struct {
		Counter *struct {
			Name    string `json:"name"`
			Packets uint64 `json:"packets"`
			Bytes   uint64 `json:"bytes"`
		} `json:"counter"`
	} `json:"nftables"`
}

// readCounters reads the named counters with the given prefix from one table.
func (h *linuxHost) readCounters(ctx context.Context, table, prefix string) (map[uint16]Count, error) {
	out, err := h.run(ctx, "", "nft", "-j", "list", "counters", "table", nftFamily, table)
	if err != nil {
		return nil, fmt.Errorf("nft list counters %s: %w: %s", table, err, bytes.TrimSpace(out))
	}
	res := map[uint16]Count{}
	if len(bytes.TrimSpace(out)) == 0 {
		return res, nil
	}
	var doc nftCounters
	if err := json.Unmarshal(out, &doc); err != nil {
		return nil, fmt.Errorf("nft counters: %w", err)
	}
	for _, it := range doc.Nftables {
		if it.Counter == nil || !strings.HasPrefix(it.Counter.Name, prefix) {
			continue
		}
		if p, err := strconv.ParseUint(strings.TrimPrefix(it.Counter.Name, prefix), 10, 16); err == nil {
			res[uint16(p)] = Count{Packets: it.Counter.Packets, Bytes: it.Counter.Bytes}
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
	h.tun.v6verdict, h.tun.v6RetryAt = "", time.Time{}
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
