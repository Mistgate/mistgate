package agent

import (
	"context"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/node/hostctl"
)

// The node's own IPv6 addresses (agent.proto "IPV6 ADDRESSES"): NodeSettings.ipv6_addresses, held on the uplink by the host
// owner (hostctl.OwnedIPv6Host). The rules and the ownership file are hostctl's; this file is the wiring.

// capIPv6Addr is listed in Hello.capabilities when the host can hold addresses: the panel sends NodeSettings.ipv6_addresses
// only to an agent that has it.
const capIPv6Addr = "ipv6-addr/1"

// readdedEvery is how seldom the journal hears that an address was put back (a manager that removes it on every
// reconfigure would otherwise fill it).
const readdedEvery = time.Hour

// ipv6AddrState is what the worker remembers of the last sync.
type ipv6AddrState struct {
	key       string // "addr=state,..." of the last sync: a change of it is a reason for a doctor report
	readdedAt time.Time
	// noSettings is the empty settings of a new agent: until the saved state, a HelloAck or a desired state has replaced
	// it, nothing is known about the addresses, and nothing is removed because of that.
	noSettings *pb.NodeSettings
}

// ownedIPv6Host is the host's part of this feature, nil when the host cannot do it.
func (a *Agent) ownedIPv6Host() hostctl.OwnedIPv6Host {
	h, _ := a.host.(hostctl.OwnedIPv6Host)
	return h
}

// wantedIPv6 parses NodeSettings.ipv6_addresses; an entry that is not an IPv6 literal is dropped (the panel sends none).
func wantedIPv6(st *pb.NodeSettings) []netip.Addr {
	var out []netip.Addr
	for _, s := range st.GetIpv6Addresses() {
		if a, err := netip.ParseAddr(strings.TrimSpace(s)); err == nil && a.Is6() && !a.Is4In6() && a.Zone() == "" {
			out = append(out, a)
		}
	}
	return out
}

// syncOwnedIPv6 makes the host hold the addresses of the settings in force, on the worker goroutine: after a desired state,
// at the start from the saved one, and in every sweep (a network manager that took an address away gets it back within 30 s).
// With no list and nothing owned it only reads a file.
func (a *Agent) syncOwnedIPv6(ctx context.Context) {
	h := a.ownedIPv6Host()
	if h == nil || a.settings.Load() == a.v6.noSettings {
		return
	}
	res, err := h.SyncOwnedIPv6(ctx, wantedIPv6(a.settings.Load()))
	if err != nil {
		a.log.Warn("ipv6 addresses not fully synced", "err", err)
	}
	var parts []string
	states := make([]doctor.OwnedAddr, 0, len(res.States))
	for _, s := range res.States {
		parts = append(parts, s.Addr.String()+"="+s.State)
		states = append(states, doctor.OwnedAddr{Addr: s.Addr.String(), State: s.State})
		if s.Reason != "" && s.State != hostctl.IPv6AddrOK {
			a.log.Debug("ipv6 address", "addr", s.Addr, "state", s.State, "why", s.Reason)
		}
	}
	sort.Strings(parts)
	a.ownedV6.Store(&states)
	key := strings.Join(parts, ",")
	if len(res.Readded) > 0 && a.now().Sub(a.v6.readdedAt) >= readdedEvery {
		a.v6.readdedAt = a.now()
		a.event(pb.Severity_SEVERITY_WARNING, "ipv6_readded", "", map[string]string{"count": strconv.Itoa(len(res.Readded))})
	}
	if key != a.v6.key {
		a.v6.key = key
		a.log.Info("ipv6 addresses", "state", key)
		a.clientIPv6SettingsChanged(a.cur.Load()) // schedules just the ipv6 check; a reconnect catches up when there is no stream
	}
}

// doctorOwnedIPv6 is the doctor's view of the last sync.
func (a *Agent) doctorOwnedIPv6() []doctor.OwnedAddr {
	if p := a.ownedV6.Load(); p != nil {
		return append([]doctor.OwnedAddr(nil), *p...)
	}
	return nil
}

// ipv6AddrSettingsChanged says whether a settings message changes the list of own addresses.
func ipv6AddrSettingsChanged(old, next *pb.NodeSettings) bool {
	a, b := old.GetIpv6Addresses(), next.GetIpv6Addresses()
	if len(a) != len(b) {
		return true
	}
	for i := range a {
		if a[i] != b[i] {
			return true
		}
	}
	return false
}
