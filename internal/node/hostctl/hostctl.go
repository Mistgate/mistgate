// Package hostctl is the agent's view of the machine it runs on: host facts for Hello, host metrics for
// StatsBatch, and the only host state the agent owns: its own nftables table for
// port-hop redirects and the SSH brute-force guard, the UDP delivery counter, the fail-open torrent queue, exact UDP inbound rules in an active UFW firewall,
// the fq + bbr, conntrack and UDP socket-buffer sysctl baseline and the journald size cap. The real implementation is
// Linux-only behind a build tag; other OSes get a no-op stub so the whole repo still builds and vets.
package hostctl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Host is implemented by the Linux host owner and by the stub.
type Host interface {
	// Facts are read once per connection (Hello).
	Facts(ctx context.Context) Facts
	// Metrics is sampled once per stats interval; rates are averaged since the previous call.
	Metrics() Metrics
	// ApplyBaseline installs the sysctl and journald baseline and the SSH brute-force guard. Idempotent; a
	// failure is returned but is never fatal for the agent (OpenVZ/LXC may not allow live sysctl writes).
	ApplyBaseline(ctx context.Context) error
	// SetPortHops makes the hop part of the agent's nft table match hops exactly. Atomic; the SSH guard
	// (installed by ApplyBaseline) stays. Every hop must pass ValidateHop.
	SetPortHops(ctx context.Context, hops []Hop) error
	// SyncInboundUDPPorts reconciles exact Mistgate UDP listener ports and hop ranges in a supported,
	// already-active host firewall. It never enables a firewall or edits provider-level rules.
	SyncInboundUDPPorts(ctx context.Context, ports []UDPInboundPort) error
	// SSHPorts are the sshd ports found by the last ApplyBaseline (22 when detection found nothing):
	// the SSH guard rate-limits them and no port-hop range may cover them.
	SSHPorts() []uint16
	// Cleanup removes everything the agent installed (nft tables with hops, SSH guard, UDP delivery checks and torrent queue, tagged UFW UDP rules
	// and the UFW rules the SSH install tagged with ProvisionUFWTag, sysctl and journald drop-ins,
	// and the resolver fix of the doctor: resolved drop-in, resolv.conf restored from its backup; the tunnel table
	// "mistgate_awg" and every link named mgawg* or mgwarp). The WARP routing rules and routes are the WARP
	// manager's (warp.Cleanup).
	// Live sysctl values are not reverted (the previous values are not recorded).
	Cleanup(ctx context.Context) error
}

// Fixer is the part of the host the doctor's safe fixes need beyond Host. The Linux host implements it, the
// stub returns ErrUnsupported; a caller type-asserts (`fx, ok := host.(Fixer)`). Cleanup undoes SetResolver.
type Fixer interface {
	// VacuumJournal runs journalctl --vacuum-size=200M --vacuum-time=7d.
	VacuumJournal(ctx context.Context) error
	// ResolverPlan says what SetResolver would do, changing nothing.
	ResolverPlan(ctx context.Context, servers []string) (ResolverPlan, error)
	// SetResolver points the host resolver at servers (IPs, optionally host:port where the mode allows it).
	// The previous configuration is kept so Cleanup can restore it.
	SetResolver(ctx context.Context, servers []string) error
}

// ResolverPlan is the dry run of SetResolver.
type ResolverPlan struct {
	Mode   string   // "resolved" (systemd-resolved drop-in) or "resolv_conf" (/etc/resolv.conf rewritten)
	Before []string // nameservers in effect now
	After  []string // nameservers that will be configured (validated, deduplicated)
}

// ErrUnsupported is returned by the non-Linux stub.
var ErrUnsupported = errors.New("hostctl: not supported on this OS")

type Facts struct {
	Hostname  string
	OS        string
	Kernel    string
	Arch      string
	CPUCount  int
	RAMTotal  uint64
	DiskTotal uint64
	Virt      string // systemd-detect-virt style: "kvm", "openvz", "lxc", "none", "unknown"
	HasIPv6   bool
	Boot      time.Time
}

type Metrics struct {
	CPUPct     float64
	SoftirqPct float64 // part of CPUPct spent in softirq
	Load1      float64
	RAMUsed    uint64
	RAMTotal   uint64
	DiskUsed   uint64
	DiskTotal  uint64
	NetRxBps   uint64
	NetTxBps   uint64
	UptimeS    uint64
}

// Hop is one port-hopping range: packets to Network/From-To are redirected to the inbound's real Port.
type Hop struct {
	InboundID string
	Network   string // "udp" | "tcp"
	From, To  uint16
	Port      uint16
}

// UDPInboundPort describes one exact UDP port or one exact Hysteria2 hop range.
// Set Port for a single listener; set From and To for a range.
type UDPInboundPort struct {
	Port     uint16
	From, To uint16
}

const (
	// NftTable is the agent's own table; the agent never touches any other.
	NftTable  = "mistgate_node"
	nftFamily = "inet"
)

// OwnNftTable says whether an nft table is one the agent itself creates (all of them are inet): the firewall and
// port hops, the UDP delivery check, AmneziaWG tunnels, WARP and the torrent guard. A new table goes here, or the doctor calls it foreign.
func OwnNftTable(family, name string) bool {
	if family != nftFamily {
		return false
	}
	switch name {
	case NftTable, NftTunnelTable, NftWarpTable, NftTorrentTable, NftUDPCheckTable:
		return true
	}
	return false
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

const (
	// MinHopPort and MaxHopSpan bound every port-hop range the node will redirect (F12): a hop range is a
	// DNAT of every incoming packet of that range, so an unbounded one (a typo, a hostile panel) would
	// swallow DNS, NTP, WireGuard or sshd on the same host.
	MinHopPort = 1024
	MaxHopSpan = 20000 // number of ports in the range
)

// ValidateHop is the one place that says whether a hop may be installed. reserved are ports a range must
// never cover (the sshd ports, other inbounds' ports): the caller knows them, this package cannot.
// The inbound's own Port may lie inside its range.
func ValidateHop(h Hop, reserved []uint16) error {
	switch {
	case !idRe.MatchString(h.InboundID):
		return fmt.Errorf("hop %q: bad inbound id", h.InboundID)
	case h.Network != "udp" && h.Network != "tcp":
		return fmt.Errorf("hop %s: network %q", h.InboundID, h.Network)
	case h.From == 0 || h.To < h.From || h.Port == 0:
		return fmt.Errorf("hop %s: bad range %d-%d -> %d", h.InboundID, h.From, h.To, h.Port)
	case h.From < MinHopPort:
		return fmt.Errorf("hop range %d-%d starts below %d (well-known ports, sshd 22, ACME 80 and 443 stay out of reach)", h.From, h.To, MinHopPort)
	case int(h.To)-int(h.From)+1 > MaxHopSpan:
		return fmt.Errorf("hop range %d-%d is %d ports wide, the limit is %d", h.From, h.To, int(h.To)-int(h.From)+1, MaxHopSpan)
	}
	for _, p := range reserved {
		if p >= h.From && p <= h.To && p != h.Port {
			return fmt.Errorf("hop range %d-%d covers port %d, which this node already uses", h.From, h.To, p)
		}
	}
	return nil
}

// RenderHops renders the agent's table with only the port-hop redirects (no SSH guard).
func RenderHops(hops []Hop) (string, error) { return RenderRuleset(hops, nil) }

// RenderRuleset returns the nft script that atomically replaces the agent's table with one redirect rule per
// hop and, when sshPorts is not empty, the SSH brute-force guard (ssh.go). `add` + `delete` first makes the
// script work whether or not the table exists, and nft applies a whole -f file as one transaction. No hops
// and no ssh ports renders the delete only (= cleanup). Every hop goes through ValidateHop, with the ssh
// ports reserved.
//
// `redirect` (DNAT to the local address of the incoming interface) works for IPv4 and IPv6 in the inet
// family (kernel >= 5.2) and needs no knowledge of the node's address.
func RenderRuleset(hops []Hop, sshPorts []uint16) (string, error) {
	hs := append([]Hop(nil), hops...)
	sort.Slice(hs, func(i, j int) bool { return hs[i].InboundID < hs[j].InboundID })
	for i, h := range hs {
		if err := ValidateHop(h, sshPorts); err != nil {
			return "", err
		}
		for _, o := range hs[:i] {
			if o.Network == h.Network && h.From <= o.To && o.From <= h.To {
				return "", fmt.Errorf("hop %s overlaps %s", h.InboundID, o.InboundID)
			}
		}
	}
	guard, err := renderSSHGuard(sshPorts)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "add table %s %s\ndelete table %s %s\n", nftFamily, NftTable, nftFamily, NftTable)
	if len(hs) == 0 && guard == "" {
		return b.String(), nil
	}
	fmt.Fprintf(&b, "table %s %s {\n", nftFamily, NftTable)
	b.WriteString(guard)
	if len(hs) > 0 {
		b.WriteString("\tchain hop {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
		for _, h := range hs {
			fmt.Fprintf(&b, "\t\t%s dport %d-%d redirect to :%d comment \"hop:%s\"\n", h.Network, h.From, h.To, h.Port, h.InboundID)
		}
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// Exported so the doctor's net_baseline check compares against the baseline ApplyBaseline writes.
const (
	SysctlFilePath   = "/etc/sysctl.d/90-mistgate.conf"
	JournaldFilePath = "/etc/systemd/journald.conf.d/90-mistgate.conf"
	SysctlFileBody   = sysctlFileBody
	JournaldFileBody = journaldFileBody
	// UDPBufferMinBytes is the minimum net.core.rmem_max and net.core.wmem_max value.
	UDPBufferMinBytes = 16 << 20
	// UDPDefaultBufferMinBytes is the minimum receive and send buffer inherited by new UDP sockets.
	UDPDefaultBufferMinBytes = 1 << 20
	// ConntrackMaxMin and ConntrackBucketsMin size the connection tracking table for relayed user traffic.
	ConntrackMaxMin     = 65536
	ConntrackBucketsMin = 16384
	// JournalCapMB is SystemMaxUse in journaldFileBody and the target of VacuumJournal.
	JournalCapMB = 200
)

const (
	// The conntrack keys exist only once nf_conntrack is loaded, which at boot is usually after systemd-sysctl ran: the
	// leading "-" tells systemd-sysctl to skip a missing key instead of failing; the agent applies them after its nft
	// rules load the module.
	sysctlFileBody = "# Managed by mistgate-node.\nnet.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\nnet.core.rmem_max = 16777216\nnet.core.wmem_max = 16777216\nnet.core.rmem_default = 1048576\nnet.core.wmem_default = 1048576\n-net.netfilter.nf_conntrack_max = 65536\n-net.netfilter.nf_conntrack_buckets = 16384\n"
	// SystemMaxUse/RuntimeMaxUse only; there is no retention-time override.
	journaldFileBody = "# Managed by mistgate-node.\n[Journal]\nSystemMaxUse=200M\nRuntimeMaxUse=200M\n"
)

// sysctlFileBodyWithValues keeps larger live values in the persistent baseline.
func sysctlFileBodyWithValues(rmemMax, wmemMax, rmemDefault, wmemDefault, conntrackMax, conntrackBuckets uint64) string {
	body := strings.Replace(sysctlFileBody, "net.core.rmem_max = 16777216", fmt.Sprintf("net.core.rmem_max = %d", rmemMax), 1)
	body = strings.Replace(body, "net.core.wmem_max = 16777216", fmt.Sprintf("net.core.wmem_max = %d", wmemMax), 1)
	body = strings.Replace(body, "net.core.rmem_default = 1048576", fmt.Sprintf("net.core.rmem_default = %d", rmemDefault), 1)
	body = strings.Replace(body, "net.core.wmem_default = 1048576", fmt.Sprintf("net.core.wmem_default = %d", wmemDefault), 1)
	body = strings.Replace(body, "net.netfilter.nf_conntrack_max = 65536", fmt.Sprintf("net.netfilter.nf_conntrack_max = %d", conntrackMax), 1)
	return strings.Replace(body, "net.netfilter.nf_conntrack_buckets = 16384", fmt.Sprintf("net.netfilter.nf_conntrack_buckets = %d", conntrackBuckets), 1)
}

// hasGlobalIPv6 reports whether any interface carries a global unicast IPv6 address (ULA excluded).
func hasGlobalIPv6() bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() == nil && ipn.IP.IsGlobalUnicast() && !ipn.IP.IsPrivate() {
			return true
		}
	}
	return false
}
