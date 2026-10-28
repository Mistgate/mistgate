package fleet

import (
	"context"
	"sort"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The relays of a node as its desired state carries them (agent.proto "RELAY FORWARDS"): the node is the entry, and
// forwards the port of each switched-on relay to the server it leads to.

// relayForwards lists the forwards of the entry node n, sorted by relay id. A relay is in it when it is switched on and
// store.RelayLink.Live holds (the exit's server is on, not failed, on an enrolled node); the target is the exit node's
// address and the server's own port (a hop range is not forwarded). A server whose spec does not build leaves the list: it
// is reported on its own, and the forward would point nowhere.
func (f *Fleet) relayForwards(ctx context.Context, n store.NodeRow) ([]plugin.RelayForward, error) {
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
		out = append(out, plugin.RelayForward{ID: l.ID, Network: l.Network, Port: l.Port, ToHost: exit.Address, ToPort: spec.Listen.Port})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// relaysApplied keeps what the entry reported for its forwards (RelayResult), and, when the node's inbounds were reported,
// tells the other nodes to look again: a relay leading to one of them is forwarded only while that server is usable, which
// the report may have changed.
func (c *SessionCore) relaysApplied(ctx context.Context, nodeID string, r *agentv1.ApplyResult, inboundsReported bool, now time.Time) {
	if len(r.Relays) > 0 {
		rs := make([]store.RelayApplied, 0, len(r.Relays))
		for _, x := range r.Relays {
			rs = append(rs, store.RelayApplied{ID: x.RelayId, OK: x.Ok, Error: x.Error, ToIP: x.ToIp})
		}
		if err := c.f.st.RelaysApplied(ctx, nodeID, rs, now); err != nil {
			c.f.log.Warn("record relay results", "node", nodeID, "err", err)
		}
	}
	if !inboundsReported {
		return
	}
	if has, err := c.f.st.HasRelaysOnNode(ctx, nodeID); err != nil {
		c.f.log.Warn("read relays of node", "node", nodeID, "err", err)
	} else if has {
		c.f.StateChanged()
	}
}
