package fleet

import (
	"context"
	"encoding/json"
	"slices"
	"sort"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The bridge of the reverse relays in the desired state (agent.proto "REVERSE RELAY"): the entry is sent where the exits call
// (BridgeServe) and the way of each reverse forward (RelayForward.mode), the exit the entries it calls (BridgeDials). All of it goes
// only to an agent that lists "relay-reverse/1", and only for a relay that stands in the reverse way (Relay.active_mode) between two
// agents that both list it; every other relay is the plain forward it always was. Nothing here chooses the way: the controller does.

// reverseCarried says the relay is passed on through the bridge as far as the stream of node nodeID (one end of it, its agent
// listing caps) is concerned: the relay stands in the reverse way, its server speaks TCP, and the agents at both ends list the
// capability. The other end's capabilities are those the panel has on record.
func reverseCarried(l store.RelayLink, nodeID string, caps []string) bool {
	if !l.Reverse() || l.Network != "tcp" || !slices.Contains(caps, plugin.CapRelayReverse) {
		return false
	}
	other := l.ExitNodeCaps
	if l.ExitNodeID == nodeID {
		other = l.ViaNodeCaps
	}
	return slices.Contains(other, plugin.CapRelayReverse)
}

// bridgeDesired builds the bridge side of the node's desired state for an agent with these capabilities: nil serve and no dials
// for an agent without "relay-reverse/1" and for a node none of whose relays is carried through the bridge. The entry side needs
// the entry's bridge path (chosen with its first reverse relay); a relay of an entry without one is not served, and an exit does
// not dial an entry without one.
func (f *Fleet) bridgeDesired(ctx context.Context, n store.NodeRow, caps []string) (*plugin.BridgeServe, []plugin.BridgeDial, error) {
	if !slices.Contains(caps, plugin.CapRelayReverse) {
		return nil, nil, nil
	}
	links, err := f.st.RelayLinks(ctx, store.RelayFilter{NodeID: n.ID, EnabledOnly: true})
	if err != nil || len(links) == 0 {
		return nil, nil, err
	}
	var serve plugin.BridgeServe
	dials := map[string]*plugin.BridgeDial{}
	for _, l := range links {
		if !l.Live() || !reverseCarried(l, n.ID, caps) {
			continue
		}
		if l.ViaNodeID == n.ID && n.BridgePath != "" {
			if !slices.Contains(serve.ExitNodeIDs, l.ExitNodeID) {
				serve.ExitNodeIDs = append(serve.ExitNodeIDs, l.ExitNodeID)
			}
			serve.Port, serve.Path = n.BridgePort, n.BridgePath
		}
		if l.ExitNodeID != n.ID {
			continue
		}
		entry, err := f.st.Node(ctx, l.ViaNodeID)
		if err != nil {
			return nil, nil, err
		}
		if entry.BridgePath == "" {
			continue
		}
		row, err := f.st.FleetInbound(ctx, l.InboundID)
		if err != nil {
			return nil, nil, err
		}
		spec, err := f.buildSpec(n, row)
		if err != nil {
			f.log.Warn("relay: the server's spec does not build, the bridge target is left out", "code", "bridge_target_spec_failed", "relay", l.ID, "inbound", l.InboundID, "err", err)
			continue
		}
		d := dials[entry.ID]
		if d == nil {
			// The entry's site is 443 unless the panel gave the bridge a port of its own. Name and certificate pin of the site are the
			// panel's to supply with the endpoint (the controller step); until then a dial has neither and the agent refuses it.
			port := entry.BridgePort
			if port == 0 {
				port = 443
			}
			d = &plugin.BridgeDial{EntryNodeID: entry.ID, Host: entry.Address, Port: port, Path: entry.BridgePath}
			dials[entry.ID] = d
		}
		d.Targets = append(d.Targets, plugin.BridgeTarget{RelayID: l.ID, Network: l.Network, Port: spec.Listen.Port})
	}
	var outServe *plugin.BridgeServe
	if len(serve.ExitNodeIDs) > 0 {
		sort.Strings(serve.ExitNodeIDs)
		outServe = &serve
	}
	var outDials []plugin.BridgeDial
	for _, d := range dials {
		sort.Slice(d.Targets, func(i, j int) bool { return d.Targets[i].RelayID < d.Targets[j].RelayID })
		outDials = append(outDials, *d)
	}
	sort.Slice(outDials, func(i, j int) bool { return outDials[i].EntryNodeID < outDials[j].EntryNodeID })
	return outServe, outDials, nil
}

// serveHash and dialsHash tell two settings apart for the sent digest ("" = none: an absent one and an empty one are the same,
// as in the state hash).
func serveHash(s *plugin.BridgeServe) string {
	if s == nil || s.IsZero() {
		return ""
	}
	b, _ := json.Marshal(s)
	return shortHash(b)
}

func dialsHash(ds []plugin.BridgeDial) string {
	if len(ds) == 0 {
		return ""
	}
	b, _ := json.Marshal(ds)
	return shortHash(b)
}

// bridgeServeProto is the wire form of the serve setting: nil for none (a full state then carries no field at all, which is what
// an agent without the feature sees); a delta that stops the bridge sends the empty message (bridgeServeAlways).
func bridgeServeProto(s *plugin.BridgeServe) *agentv1.BridgeServe {
	if s == nil || s.IsZero() {
		return nil
	}
	return bridgeServeAlways(s)
}

func bridgeServeAlways(s *plugin.BridgeServe) *agentv1.BridgeServe {
	if s == nil {
		return &agentv1.BridgeServe{}
	}
	return &agentv1.BridgeServe{Port: uint32(s.Port), Path: s.Path, ExitNodeIds: slices.Clone(s.ExitNodeIDs), PanelVia: s.PanelVia}
}

// bridgeDialsProto is the same for the list of entries: nil for none; the empty wrapper clears the list in a delta.
func bridgeDialsProto(ds []plugin.BridgeDial) *agentv1.BridgeDials {
	if len(ds) == 0 {
		return nil
	}
	return bridgeDialsAlways(ds)
}

func bridgeDialsAlways(ds []plugin.BridgeDial) *agentv1.BridgeDials {
	out := &agentv1.BridgeDials{Items: make([]*agentv1.BridgeDial, 0, len(ds))}
	for _, d := range ds {
		item := &agentv1.BridgeDial{EntryNodeId: d.EntryNodeID, Host: d.Host, Port: uint32(d.Port), Sni: d.SNI, PinSha256: d.PinSHA256, Path: d.Path,
			Pool: d.Pool, PanelVia: d.PanelVia}
		for _, t := range d.Targets {
			item.Targets = append(item.Targets, &agentv1.BridgeTarget{RelayId: t.RelayID, Network: t.Network, Port: uint32(t.Port)})
		}
		out.Items = append(out.Items, item)
	}
	return out
}
