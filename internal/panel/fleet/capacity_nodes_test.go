//go:build !js

package fleet

import (
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The capacity read gets every node that is not retired with its hours, facts, the owner's bandwidth, the connection and
// the disk of the latest sample; a node that never sent a batch is there with no hours.
func TestCapacityNodes(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	quiet := e.fixture(a.nodeID).nodeB // active, never connected
	if _, err := e.st.W.ExecContext(e.ctx, `UPDATE node SET bandwidth_mbps = 500 WHERE id = ?`, a.nodeID); err != nil {
		t.Fatal(err)
	}
	c, _, _ := connectFull(a, "capacity")
	now := time.Now().Unix()
	b := statsBatch(now-10, now, nil, nil)
	b.GetStats().Host = &agentv1.HostMetrics{CpuPct: 42, RamUsedBytes: 512, RamTotalBytes: 1024, DiskUsedBytes: 30 << 30, DiskTotalBytes: 40 << 30,
		StealPct: 4, ConntrackCount: 10, ConntrackMax: 100, Agent: &agentv1.ProcessUsage{CpuPct: 3, RssBytes: 64 << 20}}
	c.send(1, b)

	var got map[string]int
	waitFor(t, func() bool {
		nodes, err := e.f.CapacityNodes(e.ctx, 24)
		if err != nil {
			t.Fatal(err)
		}
		got = map[string]int{}
		for i, n := range nodes {
			got[n.ID] = i
		}
		i, ok := got[a.nodeID]
		return ok && len(nodes[i].Hours) == 1 && nodes[i].DiskTotal > 0
	})
	nodes, _ := e.f.CapacityNodes(e.ctx, 24)
	n := nodes[got[a.nodeID]]
	if n.Name != "nodea" || n.BandwidthMbps != 500 || !n.Connected || n.DiskUsed != 30<<30 || n.DiskTotal != 40<<30 {
		t.Fatalf("node = %+v", n)
	}
	if h := n.Hours[0]; h.CPUAvg != 42 || h.ExtSamples != 1 || h.StealAvg != 4 || h.ConntrackLimit != 100 || h.AgentRSSMax != 64<<20 {
		t.Fatalf("hour = %+v", h)
	}
	q, ok := got[quiet]
	if !ok || len(nodes[q].Hours) != 0 || nodes[q].Connected {
		t.Fatalf("the quiet node: %+v (present %v)", nodes, ok)
	}
}
