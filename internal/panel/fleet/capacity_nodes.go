//go:build !js

package fleet

import (
	"context"

	"github.com/mistgate/mistgate/internal/panel/capacity"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// CapacityNodes reads what the capacity read (internal/panel/capacity, OpsService.GetFleetLoad) needs for every node that is
// not retired: its hours of host load over the window, its CPU count and memory, the owner's bandwidth, whether its agent
// is connected and the root disk of its latest sample. Plain reads of rows the panel keeps; no node is contacted. The VPS
// edition only, like GetNodeHostHistory.
func (f *Fleet) CapacityNodes(ctx context.Context, hours int) ([]capacity.Node, error) {
	now := f.now().UTC()
	nodes, err := f.st.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	live, err := f.st.NodeLive(ctx, now, "", true)
	if err != nil {
		return nil, err
	}
	liveBy := liveRowsByID(live)
	out := make([]capacity.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.State == "retired" {
			continue
		}
		facts, err := f.st.NodeFacts(ctx, n.ID)
		if err != nil {
			return nil, err
		}
		hrs, err := f.st.NodeHostHours(ctx, n.ID, now, min(max(hours, 1), store.HostMaxHours))
		if err != nil {
			return nil, err
		}
		l := liveBy[n.ID]
		c := capacity.Node{ID: n.ID, Name: n.Name, CPUs: facts.CPUCount, RAMTotal: facts.RAMTotal,
			BandwidthMbps: uint32(min(max(n.BandwidthMbps, 0), 1_000_000)), Connected: l.Connected, Hours: hrs}
		if v := decodeLiveView(l); v.Metrics != nil {
			c.DiskUsed, c.DiskTotal = v.Metrics.DiskUsedBytes, v.Metrics.DiskTotalBytes
		}
		out = append(out, c)
	}
	return out, nil
}
