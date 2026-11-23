package access

import (
	"slices"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The way of a relay in the admin API (relay.proto Relay.mode and the fields after it). This part only reports what the store
// holds and what the two agents can do; the choice of the way, the gate of switching on and the bridge report are the panel's
// controller, which does not exist yet.

// Why the reverse way cannot carry a relay (relay.proto Relay.reverse_blocked).
const (
	reverseBlockedEntryOld = "entry_agent_old"
	reverseBlockedExitOld  = "exit_agent_old"
	reverseBlockedUDP      = "udp"
)

// reverseBlocked says why the relay cannot go through the bridge, "" when it can: a UDP server first (no agent update helps it in
// this version), then the entry's agent, then the exit's. An agent that has not connected has no capabilities on record and counts
// as old.
func reverseBlocked(l store.RelayLink) string {
	return reverseBlockedOf(l.Network, l.ViaNodeCaps, l.ExitNodeCaps)
}

// reverseBlockedOf is the same for a server not yet relayed: its network and the capabilities of the two agents.
func reverseBlockedOf(network string, entryCaps, exitCaps []string) string {
	switch {
	case network != "tcp":
		return reverseBlockedUDP
	case !slices.Contains(entryCaps, agentcap.RelayReverse):
		return reverseBlockedEntryOld
	case !slices.Contains(exitCaps, agentcap.RelayReverse):
		return reverseBlockedExitOld
	}
	return ""
}

// fillRelayMode puts the way of the relay into its admin message.
func fillRelayMode(out *adminv1.Relay, l store.RelayLink) {
	out.Mode, out.ActiveMode, out.ModeReason = l.Mode, l.ActiveMode, l.ModeReason
	if !l.ModeChangedAt.IsZero() {
		out.ModeChangedUnix = l.ModeChangedAt.Unix()
	}
	out.DirectStreak = l.DirectStreak()
	out.ReverseBlocked = reverseBlocked(l)
	if b, ok := l.Bridge(); ok {
		out.Bridge = &adminv1.RelayBridge{Connected: b.Connected, Conns: b.Conns, RttMs: b.RTTMS, SinceUnix: b.SinceUnix, Error: b.Error}
		// How full the bridge is: the people the entry carries now, the connections the exit was asked to hold (2, or 4), what
		// that pool carries at most and whether the bridge is nearly full. Of the pair, not of this relay: every relay of the
		// entry and the exit shows the same (the entry's report is per exit).
		out.Bridge.Pool = max(b.Pool, store.BridgePoolMin)
		out.Bridge.Capacity = store.BridgeCapacity(out.Bridge.Pool)
		if b.Entry != nil {
			out.Bridge.Clients = b.Entry.Clients
		}
		out.Bridge.NearFull = store.BridgeNearFull(out.Bridge.Clients, out.Bridge.Pool)
	}
}
