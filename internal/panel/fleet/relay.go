package fleet

import (
	"context"
	"slices"
	"sort"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The relays of a node as its desired state carries them (agent.proto "RELAY FORWARDS"): the node is the entry, and
// forwards the port of each switched-on relay to the server it leads to.

// relayForwards lists the forwards of the entry node n, sorted by relay id. A relay is in it when it is switched on and
// store.RelayLink.Live holds (the exit's server is on, not failed, on an enrolled node); the target is the exit node's
// address and the server's own port (a hop range is not forwarded). A relay that stands in the reverse way, between two agents that
// list "relay-reverse/1" and with a bridge path on the entry, carries its mode and the exit it is bridged to (reverseCarried); every other
// one is the plain forward. A server whose spec does not build leaves the list: it
// is reported on its own, and the forward would point nowhere.
func (f *Fleet) relayForwards(ctx context.Context, n store.NodeRow, caps []string) ([]plugin.RelayForward, error) {
	links, err := f.st.RelayLinks(ctx, store.RelayFilter{ViaNodeID: n.ID, EnabledOnly: true})
	if err != nil || len(links) == 0 {
		return nil, err
	}
	var out []plugin.RelayForward
	for _, l := range links {
		if !l.Live() {
			continue
		}
		exit, err := f.st.Node(ctx, l.ExitNodeID)
		if err != nil {
			return nil, err
		}
		row, err := f.st.FleetInbound(ctx, l.InboundID)
		if err != nil {
			return nil, err
		}
		spec, err := f.buildSpec(exit, row)
		if err != nil {
			f.log.Warn("relay: the server's spec does not build, the forward is left out", "relay", l.ID, "inbound", l.InboundID, "err", err)
			continue
		}
		doctor, err := f.st.IPv6Doctor(ctx, exit.ID)
		if err != nil {
			f.log.Warn("relay: the exit's IPv6 facts do not read, the forward is left out", "relay", l.ID, "err", err)
			continue
		}
		host, family, ok := store.RelayHostFor(l.Relay, exit.Address, exit.IPv6Entry, doctor)
		if !ok {
			// The owner asked for the IPv6 leg and the exit names no usable address yet: the forward would point nowhere. The
			// leg check says the same to the owner; nothing is installed.
			f.log.Warn("relay: the exit has no address for the IPv6 leg, the forward is left out", "relay", l.ID, "inbound", l.InboundID)
			continue
		}
		toFamily := ""
		if family == store.RelayFamily6 {
			if !slices.Contains(caps, agentcap.RelayForwardV6) {
				f.log.Warn("relay: the entry's agent cannot carry an IPv6 leg", "relay", l.ID, "inbound", l.InboundID)
				continue
			}
			toFamily = store.RelayFamily6
		}
		to := row.RelayExit(host, spec.Listen.Port)
		fwd := plugin.RelayForward{ID: l.ID, Network: l.Network, Port: l.Port, ToHost: to.Host, ToPort: to.Port, ToFamily: toFamily}
		if bridgePathValid(n.BridgePath) && bridgePortValid(n.BridgePort) && reverseCarried(l, n.ID, caps) { // A bad endpoint cannot be carried as a reverse forward.
			fwd.Mode, fwd.ExitNodeID, fwd.ToFamily = store.RelayModeReverse, l.ExitNodeID, "" // The bridge dials the entry; the exit's family is not its to_host.
		}
		out = append(out, fwd)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// relaysApplied keeps what the entry reported for its forwards (RelayResult), and, when the node's inbounds were reported,
// tells the other nodes to look again: a relay leading to one of them is forwarded only while that server is usable, which
// the report may have changed.
func inboundRunStateChanged(previous []store.FleetInboundRow, applied []store.InboundApplied) bool {
	before := make(map[string]string, len(previous))
	for _, inbound := range previous {
		before[inbound.ID] = inbound.State
	}
	for _, inbound := range applied {
		if old, ok := before[inbound.ID]; ok && old != inbound.State {
			return true
		}
	}
	return false
}

func (c *SessionCore) relaysApplied(ctx context.Context, nodeID string, r *agentv1.ApplyResult, inboundsReported, inboundStateChanged bool, now time.Time) {
	if len(r.Relays) > 0 {
		rs := make([]store.RelayApplied, 0, len(r.Relays))
		for _, x := range r.Relays {
			rs = append(rs, store.RelayApplied{ID: x.RelayId, OK: x.Ok, Error: x.Error, ToIP: x.ToIp})
		}
		if err := c.f.st.RelaysApplied(ctx, nodeID, rs, now); err != nil {
			c.f.log.Warn("record relay results", "node", nodeID, "err", err)
		}
	}
	if !inboundsReported || !inboundStateChanged {
		return
	}
	if has, err := c.f.st.HasRelaysOnNode(ctx, nodeID); err != nil {
		c.f.log.Warn("read relays of node", "node", nodeID, "err", err)
	} else if has {
		c.f.StateChanged()
	}
}
