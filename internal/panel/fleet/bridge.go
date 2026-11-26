package fleet

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

var (
	bridgeNodeIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	bridgeRelayIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	bridgePathPattern    = regexp.MustCompile(`^/[A-Za-z0-9_./-]{1,240}$`)
)

// What the agent takes (internal/node/bridge: maxBridgeListeners, the dial and target limits): a state over any of them is not
// trimmed by the agent but refused whole, and an entry that refuses its BridgeServe stops the bridge for every exit at once.
// bridge_test.go holds the agent's own validators against these.
const (
	bridgeMaxExits   = 64 // exits an entry serves
	bridgeMaxDials   = 64 // entries an exit dials
	bridgeMaxTargets = 64 // relays one dial passes
)

// The bridge of the reverse relays in the desired state (agent.proto "REVERSE RELAY"): the entry is sent where the exits call
// (BridgeServe) and the way of each reverse forward (RelayForward.mode), the exit the entries it calls (BridgeDials). All of it goes
// only to an agent that lists "relay-reverse/1", and only for a relay that stands in the reverse way (Relay.active_mode) between two
// agents that both list it; every other relay is the plain forward it always was. Nothing here chooses the way: the controller does.

// reverseCarried says the relay is passed on through the bridge as far as the stream of node nodeID (one end of it, its agent
// listing caps) is concerned: the relay stands in the reverse way, its server speaks TCP, and the agents at both ends list the
// capability. The other end's capabilities are those the panel has on record.
func reverseCarried(l store.RelayLink, nodeID string, caps []string) bool {
	if !l.Reverse() || l.Network != "tcp" || !slices.Contains(caps, agentcap.RelayReverse) {
		return false
	}
	other := l.ExitNodeCaps
	if l.ExitNodeID == nodeID {
		other = l.ViaNodeCaps
	}
	return slices.Contains(other, agentcap.RelayReverse)
}

func bridgePathValid(path string) bool {
	return bridgePathPattern.MatchString(path) && !strings.Contains(path, "..") && !strings.ContainsAny(path, "?#%")
}

func bridgePortValid(port uint16) bool { return port == 0 || port == 443 || port >= 1024 }

// bridgeDesired builds the bridge side of the node's desired state for an agent with these capabilities: nil serve and no dials
// for an agent without "relay-reverse/1" and for a node none of whose relays uses the bridge. A pair needs the capability at both
// ends.
//
// The ENTRY serves (BridgeServe) as soon as a TCP relay through it may use the bridge at all (store.RelayLink.BridgeServed: its
// mode is auto or reverse), switched on or not, so that its site is up and its pin reported before anything needs them: the check
// that decides whether a relay may be switched on probes that site, and an exit can verify a site only by a pin it was given. It
// needs the entry's bridge path (chosen with the first such relay); an entry without one serves nothing.
//
// The EXIT dials (BridgeDials) only for a relay that is switched on and whose bridge is wanted (store.RelayLink.BridgeWanted: it
// stands in the reverse way, or the controller may need it within seconds). A dial always carries a way to verify the entry:
// the TLS name of the entry's own site on 443 (a public certificate, or an inbound's pin), or, for a separate bridge site, the pin the
// entry's agent reported; an entry that has told no pin is not dialed yet.
func (f *Fleet) bridgeDesired(ctx context.Context, n store.NodeRow, caps []string) (*plugin.BridgeServe, []plugin.BridgeDial, error) {
	if !slices.Contains(caps, agentcap.RelayReverse) {
		return nil, nil, nil
	}
	links, err := f.st.RelayLinks(ctx, store.RelayFilter{NodeID: n.ID})
	if err != nil || len(links) == 0 {
		return nil, nil, err
	}
	sort.Slice(links, func(i, j int) bool { return links[i].ID < links[j].ID })
	now := f.now()
	var serve plugin.BridgeServe
	exits := map[string]bool{}
	dials := map[string]*plugin.BridgeDial{}
	entries := map[string]store.NodeRow{}
	for _, l := range links {
		if !l.Live() || l.Network != "tcp" || !slices.Contains(l.ViaNodeCaps, agentcap.RelayReverse) || !slices.Contains(l.ExitNodeCaps, agentcap.RelayReverse) {
			continue
		}
		if l.ViaNodeID == n.ID && l.BridgeServed() && bridgePathValid(n.BridgePath) && bridgePortValid(n.BridgePort) &&
			bridgeNodeIDPattern.MatchString(n.ID) && bridgeNodeIDPattern.MatchString(l.ExitNodeID) {
			exits[l.ExitNodeID] = true
			serve.Port, serve.Path, serve.PanelVia = n.BridgePort, n.BridgePath, true
		}
		if l.ExitNodeID != n.ID || !l.Enabled {
			continue
		}
		entry, ok := entries[l.ViaNodeID]
		if !ok {
			if entry, err = f.st.Node(ctx, l.ViaNodeID); err != nil {
				return nil, nil, err
			}
			entries[l.ViaNodeID] = entry
		}
		if !bridgePathValid(entry.BridgePath) || !bridgePortValid(entry.BridgePort) || !bridgeNodeIDPattern.MatchString(entry.ID) ||
			!bridgeRelayIDPattern.MatchString(l.ID) || entry.Address == "" {
			continue
		}
		if _, ok := dials[entry.ID]; !ok && len(dials) >= bridgeMaxDials {
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
		if spec.Listen.Network != "tcp" || spec.Listen.Port == 0 {
			continue
		}
		l = l.ForTarget(l.Target(row, n.Address, spec.Listen.Port))
		if !l.BridgeWanted(now, entry.BridgeEndpointPort()) {
			continue
		}
		d := dials[entry.ID]
		if d == nil {
			d = &plugin.BridgeDial{EntryNodeID: entry.ID, Host: entry.Address, Port: entry.BridgeEndpointPort(), Path: entry.BridgePath, PanelVia: true}
			if entry.BridgePort != 0 {
				d.PinSHA256 = l.BridgeSitePin(entry.BridgePort, now)
			} else if target, ok := f.bridgeTLSName(ctx, entry); ok {
				d.SNI, d.PinSHA256 = target.SNI, target.Pin
			}
			if d.SNI == "" && d.PinSHA256 == "" {
				continue // nothing to verify the entry by yet: no dial, never an unverified one
			}
			if d.PinSHA256 != "" {
				pin, ok := protocols.NormalizePin(d.PinSHA256)
				if !ok {
					continue
				}
				d.PinSHA256 = pin
			}
			dials[entry.ID] = d
		}
		if len(d.Targets) < bridgeMaxTargets {
			pool := uint32(store.BridgePoolMin)
			if b, ok := l.Bridge(); ok && b.Pool >= store.BridgePoolMin && b.Pool <= store.BridgePoolMax {
				pool = b.Pool
			}
			d.Pool = max(d.Pool, pool)
			d.Targets = append(d.Targets, plugin.BridgeTarget{RelayID: l.ID, Network: "tcp", Port: spec.Listen.Port})
		}
	}
	var outServe *plugin.BridgeServe
	if len(exits) > 0 {
		for id := range exits {
			serve.ExitNodeIDs = append(serve.ExitNodeIDs, id)
		}
		sort.Strings(serve.ExitNodeIDs)
		if len(serve.ExitNodeIDs) > bridgeMaxExits {
			serve.ExitNodeIDs = serve.ExitNodeIDs[:bridgeMaxExits] // the agent takes 64 exits; the rest wait for a relay to go
		}
		if bridgePathValid(serve.Path) {
			outServe = &serve
		}
	}
	var outDials []plugin.BridgeDial
	for _, d := range dials {
		if !bridgeNodeIDPattern.MatchString(d.EntryNodeID) || d.Host == "" || d.Port == 0 || len(d.Targets) == 0 || len(d.Targets) > bridgeMaxTargets ||
			(d.SNI == "" && d.PinSHA256 == "") || !bridgePathValid(d.Path) || d.Pool < 2 || d.Pool > 4 {
			continue
		}
		validTargets := d.Targets[:0]
		for _, target := range d.Targets {
			if bridgeRelayIDPattern.MatchString(target.RelayID) && target.Network == "tcp" && target.Port != 0 {
				validTargets = append(validTargets, target)
			}
		}
		d.Targets = validTargets
		if len(d.Targets) == 0 {
			continue
		}
		sort.Slice(d.Targets, func(i, j int) bool { return d.Targets[i].RelayID < d.Targets[j].RelayID })
		outDials = append(outDials, *d)
	}
	sort.Slice(outDials, func(i, j int) bool { return outDials[i].EntryNodeID < outDials[j].EntryNodeID })
	return outServe, outDials, nil
}

func (f *Fleet) bridgeTLSName(ctx context.Context, entry store.NodeRow) (tcpTarget, bool) {
	specs, pins, err := f.tcpSpecsOf(ctx, entry)
	if err != nil {
		return tcpTarget{}, false
	}
	for _, target := range tcpTargets(specs, pins) {
		if target.Port == 443 && (target.Role == tcpRoleSite || target.Role == tcpRoleMTProto || target.Role == tcpRoleVLESS && bridgeVLESSOwnSite(specs, target.Profile)) &&
			(target.Pin != "" || target.PublicCert) {
			return target, true
		}
	}
	return tcpTarget{}, false
}

func bridgeVLESSOwnSite(specs []tcpSpec, profile string) bool {
	for _, spec := range specs {
		if spec.Profile != profile || spec.Spec.Protocol != "vless" || spec.Spec.Listen.Network != "tcp" || spec.Spec.Listen.Port != 443 {
			continue
		}
		if _, own := vlessProbeName(spec.Spec); own {
			return true
		}
	}
	return false
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
