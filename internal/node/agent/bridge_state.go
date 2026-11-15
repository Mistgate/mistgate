package agent

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

var (
	bridgeNodeIDPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	bridgeRelayIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	bridgePathPattern    = regexp.MustCompile(`^/[A-Za-z0-9_./-]{1,240}$`)
)

const maxBridgeDialEntries = 64

func cloneBridgeServe(in *plugin.BridgeServe) *plugin.BridgeServe {
	if in == nil {
		return nil
	}
	out := *in
	out.ExitNodeIDs = append([]string(nil), in.ExitNodeIDs...)
	return &out
}

func cloneBridgeDials(in []plugin.BridgeDial) []plugin.BridgeDial {
	if len(in) == 0 {
		return nil
	}
	out := make([]plugin.BridgeDial, len(in))
	for i, dial := range in {
		out[i] = dial
		out[i].Targets = append([]plugin.BridgeTarget(nil), dial.Targets...)
	}
	return out
}

func bridgeServeFromPB(in *pb.BridgeServe) (*plugin.BridgeServe, error) {
	if in == nil {
		return nil, nil
	}
	if in.Port > 65535 || len(in.ExitNodeIds) > 64 {
		return nil, fmt.Errorf("bridge_serve: bad_config")
	}
	if len(in.ExitNodeIds) == 0 && in.Path == "" && in.Port == 0 && !in.PanelVia {
		return nil, nil
	}
	if !bridgePathPattern.MatchString(in.Path) || strings.Contains(in.Path, "..") || strings.ContainsAny(in.Path, "?#%") || len(in.ExitNodeIds) == 0 {
		return nil, fmt.Errorf("bridge_serve: bad_config")
	}
	ids := append([]string(nil), in.ExitNodeIds...)
	sort.Strings(ids)
	for i, id := range ids {
		if !bridgeNodeIDPattern.MatchString(id) || (i > 0 && ids[i-1] == id) {
			return nil, fmt.Errorf("bridge_serve: bad_config")
		}
	}
	return &plugin.BridgeServe{Port: uint16(in.Port), Path: in.Path, ExitNodeIDs: ids, PanelVia: in.PanelVia}, nil
}

func bridgeServeToPB(in *plugin.BridgeServe) *pb.BridgeServe {
	if in == nil || in.IsZero() {
		return nil
	}
	ids := append([]string(nil), in.ExitNodeIDs...)
	sort.Strings(ids)
	return &pb.BridgeServe{Port: uint32(in.Port), Path: in.Path, ExitNodeIds: ids, PanelVia: in.PanelVia}
}

func bridgeDialsFromPB(in *pb.BridgeDials) ([]plugin.BridgeDial, []error) {
	if in == nil || len(in.Items) == 0 {
		return nil, nil
	}
	if len(in.Items) > maxBridgeDialEntries {
		return nil, []error{rejectf("bridge_dials: too many entries")}
	}
	out := make([]plugin.BridgeDial, 0, len(in.Items))
	seen := map[string]bool{}
	var rejected []error
	for _, wire := range in.Items {
		if wire == nil || !bridgeNodeIDPattern.MatchString(wire.EntryNodeId) {
			rejected = append(rejected, rejectf("bridge_dial: missing or invalid entry node id"))
			continue
		}
		if seen[wire.EntryNodeId] {
			rejected = append(rejected, rejectf("bridge_dial %s listed twice", wire.EntryNodeId))
			continue
		}
		if wire.Port > 65535 || len(wire.Targets) > 64 {
			rejected = append(rejected, rejectf("bridge_dial %s: bad_config", wire.EntryNodeId))
			continue
		}
		dial := plugin.BridgeDial{EntryNodeID: wire.EntryNodeId, Host: wire.Host, Port: uint16(wire.Port), SNI: wire.Sni,
			PinSHA256: wire.PinSha256, Path: wire.Path, Pool: wire.Pool, PanelVia: wire.PanelVia}
		seenTargets := map[string]bool{}
		badTarget := false
		for _, target := range wire.Targets {
			if target == nil || target.Port > 65535 || !bridgeRelayIDPattern.MatchString(target.RelayId) || target.Network != "tcp" || seenTargets[target.RelayId] {
				badTarget = true
				break
			}
			seenTargets[target.RelayId] = true
			dial.Targets = append(dial.Targets, plugin.BridgeTarget{RelayID: target.RelayId, Network: target.Network, Port: uint16(target.Port)})
		}
		if badTarget {
			rejected = append(rejected, rejectf("bridge_dial %s: target_not_local", wire.EntryNodeId))
			continue
		}
		seen[wire.EntryNodeId] = true
		out = append(out, dial)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EntryNodeID < out[j].EntryNodeID })
	return out, rejected
}

func bridgeDialsToPB(in []plugin.BridgeDial) *pb.BridgeDials {
	if len(in) == 0 {
		return nil
	}
	dials := cloneBridgeDials(in)
	sort.Slice(dials, func(i, j int) bool { return dials[i].EntryNodeID < dials[j].EntryNodeID })
	out := &pb.BridgeDials{Items: make([]*pb.BridgeDial, 0, len(dials))}
	for _, dial := range dials {
		wire := &pb.BridgeDial{EntryNodeId: dial.EntryNodeID, Host: dial.Host, Port: uint32(dial.Port), Sni: dial.SNI,
			PinSha256: dial.PinSHA256, Path: dial.Path, Pool: dial.Pool, PanelVia: dial.PanelVia}
		targets := append([]plugin.BridgeTarget(nil), dial.Targets...)
		sort.Slice(targets, func(i, j int) bool { return targets[i].RelayID < targets[j].RelayID })
		for _, target := range targets {
			wire.Targets = append(wire.Targets, &pb.BridgeTarget{RelayId: target.RelayID, Network: target.Network, Port: uint32(target.Port)})
		}
		out.Items = append(out.Items, wire)
	}
	return out
}
