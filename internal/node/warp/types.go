// Package warp is the node's WARP egress.
//
// One Cloudflare WARP account per node, run as a plain WireGuard tunnel (device "mgwarp"). Two kinds of traffic
// leave through it:
//
//   - Hysteria2 inbounds with egress "warp": their dialers are bound to the device (egress.WithDevice), an "oif"
//     rule sends that to the WARP routing table.
//   - AWG inbounds with egress "warp": "ip rule from <client subnet>" sends the forwarded traffic into the same
//     table, and the node masquerades it to the WARP address (WARP accepts only its own address as source).
//
// Fail closed. The WARP table always ends in "unreachable default", so a dead or missing tunnel makes the
// selected traffic fail with "no route to host" instead of leaving the node directly; the rules for the client
// subnets stay while the tunnel is paused or gone. Only Cleanup removes everything.
//
// The package has three layers: the Manager (state machine, health, the recovery ladder; portable, unit-tested
// with fakes), the dataplane (Linux: netlink, wgctrl or amneziawg-go on a TUN, nft; other OSes: a stub that
// reports UNAVAILABLE) and cfapi (the Cloudflare registration client, used by the panel, not by the agent).
package warp

import (
	"errors"
	"fmt"
	"time"
)

const (
	// DefaultIface is the tunnel device. It has no fwmark (an encapsulated packet must not enter the WARP table).
	DefaultIface = "mgwarp"
	// DefaultTable is the routing table of the WARP path; the two preferences are the "oif" rule and the
	// "from <client subnet>" rules. Preflight reports a clash with another tool on the host.
	DefaultTable      = 51820
	DefaultOifPref    = 90
	DefaultSubnetPref = 110
	// NftTable is the WARP nft table (masquerade, MSS clamp, optional "reserved" stamping). The doctor treats it as ours.
	NftTable = "mistgate_warp"

	unreachableMetric = 4096
	defaultMTU        = 1280
	keepaliveSeconds  = 25

	// Health thresholds: a handshake older than this, or none, is a failure.
	handshakeMaxAge = 180 * time.Second
	failuresToDown  = 3
	successesToUp   = 2
)

// Default probe targets: A is Cloudflare's own trace (tells warp=on|plus|off and the colo), B must not be Cloudflare
// (a handshake can work while everything else is black-holed, wgcf issue 619).
const (
	DefaultProbeA = "http://connectivity.cloudflareclient.com/cdn-cgi/trace"
	DefaultProbeB = "http://connectivitycheck.gstatic.com/generate_204"
)

// Settings are the host-level knobs. The zero value means the defaults. The table and the preferences can clash
// with other tools (Tailscale, wg-quick), which is why they are settings; Preflight detects a clash.
type Settings struct {
	Iface      string
	Table      int
	OifPref    int
	SubnetPref int
	NftTable   string
}

func (s Settings) withDefaults() Settings {
	if s.Iface == "" {
		s.Iface = DefaultIface
	}
	if s.Table == 0 {
		s.Table = DefaultTable
	}
	if s.OifPref == 0 {
		s.OifPref = DefaultOifPref
	}
	if s.SubnetPref == 0 {
		s.SubnetPref = DefaultSubnetPref
	}
	if s.NftTable == "" {
		s.NftTable = NftTable
	}
	return s
}

// Validate checks a Settings value after defaults.
func (s Settings) Validate() error {
	s = s.withDefaults()
	switch {
	case len(s.Iface) > 15 || !ifaceRe(s.Iface):
		return fmt.Errorf("warp: bad interface name %q", s.Iface)
	case s.Table < 1 || s.Table > 4294967295 || (s.Table >= 253 && s.Table <= 255):
		return fmt.Errorf("warp: routing table %d is not usable (0, 253-255 are reserved)", s.Table)
	case s.OifPref < 1 || s.OifPref > 32765 || s.SubnetPref < 1 || s.SubnetPref > 32765:
		return errors.New("warp: rule preference must be in 1..32765")
	case s.OifPref == s.SubnetPref:
		return errors.New("warp: the oif and subnet rule preferences must differ")
	case !nameRe(s.NftTable):
		return fmt.Errorf("warp: bad nft table name %q", s.NftTable)
	}
	return nil
}

func ifaceRe(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return s != ""
}

func nameRe(s string) bool { return ifaceRe(s) && len(s) <= 64 }

// State is the WARP path state (agent.v1.WarpState without the UNSPECIFIED value).
type State int

const (
	// StateNone: the node has no WarpSpec.
	StateNone State = iota
	// StateUnavailable: no usable backend on this host (no kernel WireGuard and no /dev/net/tun).
	StateUnavailable
	StateStarting
	StateUp
	StateDown
	// StateDisabled: the spec is present but enabled = false (paused).
	StateDisabled
)

func (s State) String() string {
	switch s {
	case StateNone:
		return "none"
	case StateUnavailable:
		return "unavailable"
	case StateStarting:
		return "starting"
	case StateUp:
		return "up"
	case StateDown:
		return "down"
	case StateDisabled:
		return "disabled"
	}
	return "unknown"
}

// Health mirrors agent.v1.WarpHealth; the agent converts it.
type Health struct {
	State State
	// "kernel" | "userspace" | "".
	Backend string
	// ip:port in use right now (after a rotation).
	Endpoint      string
	LastHandshake time.Time
	// Probe A: "on" | "plus" | "off" | "" (failed).
	WarpFlag          string
	Colo              string
	ProbeCloudflareOK bool
	ProbeOtherOK      bool
	// Consecutive failed checks.
	Failures uint32
	RxBytes  uint64
	TxBytes  uint64
	// Short English fact for the LATEST check ("" when it passed), e.g. "probe_other_failed", "handshake_never", then
	// "; ladder: <note>" with the last recovery action. The codes are listed in web/src/lib/warp-error.ts.
	LastError string
	// The latest round of each probe; nil when the round did not run it (link down, no stats).
	ProbeCloudflare, ProbeOther *ProbeResult
	// When the latest health check finished (zero before the first one).
	CheckedAt time.Time
}

// ProbeResult is one probe of a health check: ok, how long it took (a timeout shows the timeout) and when it ended.
type ProbeResult struct {
	OK      bool
	Latency time.Duration
	At      time.Time
}

// Event is what the manager tells the agent (which turns it into an agent.v1.Event). Codes: "warp_state"
// {state, from}, "warp_needs_attention" {reason}.
type Event struct {
	Code   string
	Warn   bool
	Params map[string]string
}

// Finding is one preflight result (the doctor shows it); a clash the owner has to resolve.
type Finding struct {
	// "table_in_use" | "rule_pref_in_use" | "forward_drop" | "interface_in_use"
	ID     string
	Detail string
}

// ErrUnavailable means no WireGuard backend can run on this host.
var ErrUnavailable = errors.New("warp: no usable WireGuard backend (kernel module or /dev/net/tun)")

// ErrNotActive is what the WARP egress returns while the node has no WARP, it is paused or unavailable:
// there is no silent direct exit.
var ErrNotActive = errors.New("warp: egress not active")
