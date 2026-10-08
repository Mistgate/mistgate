package health

import (
	"context"
	"hash/fnv"
	"slices"
	"sort"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// PortCheckInterval is the minimum time between periodic UDP delivery checks of one node.
const PortCheckInterval = 6 * time.Hour

const (
	portCheckSlotWidth  = time.Minute
	portCheckSlots      = int64(PortCheckInterval / portCheckSlotWidth)
	portCheckCapability = "udpcheck/1"
	// one look a minute: every look reads the nodes and the stored checks, and a first check waits for its minute slot
	portCheckPollInterval = time.Minute
)

// PortCheckScheduleNode is the current eligibility and last stored check time used by DuePortCheckNodes.
type PortCheckScheduleNode struct {
	ID                string
	State             string
	Online            bool
	UDPCheck          bool
	HasEnabledInbound bool
	LastCheckedAt     time.Time
}

// DuePortCheckNodes returns eligible nodes whose last check is at least six hours old. Nodes without a stored check
// get a stable first-check slot in the six-hour window; later checks keep their cadence from the stored timestamp.
func DuePortCheckNodes(now time.Time, nodes []PortCheckScheduleNode) []string {
	if now.IsZero() {
		return nil
	}
	slot := (now.Unix() / int64(portCheckSlotWidth/time.Second)) % portCheckSlots
	var due []PortCheckScheduleNode
	for _, node := range nodes {
		if node.ID == "" || node.State != "active" || !node.Online || !node.UDPCheck || !node.HasEnabledInbound {
			continue
		}
		if !node.LastCheckedAt.IsZero() {
			age := now.Sub(node.LastCheckedAt)
			if age < PortCheckInterval || age < 0 {
				continue
			}
		} else if slot != portCheckSlot(node.ID) {
			continue
		}
		due = append(due, node)
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].LastCheckedAt.IsZero() != due[j].LastCheckedAt.IsZero() {
			return due[i].LastCheckedAt.IsZero()
		}
		if !due[i].LastCheckedAt.Equal(due[j].LastCheckedAt) {
			return due[i].LastCheckedAt.Before(due[j].LastCheckedAt)
		}
		return due[i].ID < due[j].ID
	})
	ids := make([]string, len(due))
	for i := range due {
		ids[i] = due[i].ID
	}
	return ids
}

func portCheckSlot(nodeID string) int64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(nodeID))
	return int64(h.Sum32()) % portCheckSlots
}

func (s *Service) runOnePortRecheck(ctx context.Context) bool {
	if s.cfg.CheckPorts == nil || ctx.Err() != nil {
		return false
	}
	select {
	case s.portCheckRun <- struct{}{}:
	default:
		return false
	}
	defer func() { <-s.portCheckRun }()

	now := s.now()
	sn, err := s.snapshot(ctx)
	if err != nil {
		s.log.Warn("health: select periodic UDP port check", "err", err)
		return false
	}
	nodes, err := s.portCheckScheduleNodes(ctx, now, sn)
	if err != nil {
		s.log.Warn("health: select periodic UDP port check", "err", err)
		return false
	}
	due := DuePortCheckNodes(now, nodes)
	if len(due) == 0 {
		return false
	}
	period := now.Unix() / int64(PortCheckInterval/time.Second)
	nodeID := due[0]
	s.portCheckAttempts[nodeID] = period
	checked, _, errorCode := s.cfg.CheckPorts(ctx, nodeID, nil)
	if errorCode == "" && portCheckFoundBadInbound(sn.byNode[nodeID], checked) {
		// A second sample confirms bursty loss; the latest stored verdict drives the alert.
		_, _, _ = s.cfg.CheckPorts(ctx, nodeID, nil)
	}
	s.evaluateSoon()
	return true
}

func (s *Service) portCheckScheduleNodes(ctx context.Context, now time.Time, sn *snapshot) ([]PortCheckScheduleNode, error) {
	checks, err := s.st.PortChecks(ctx)
	if err != nil {
		return nil, err
	}
	liveRows, err := s.fl.Live(ctx)
	if err != nil {
		return nil, err
	}
	live := indexLiveRows(liveRows)
	lastChecked := make(map[string]time.Time, len(checks))
	for _, check := range checks {
		if check.CheckedAt.After(lastChecked[check.NodeID]) {
			lastChecked[check.NodeID] = check.CheckedAt
		}
	}
	period := now.Unix() / int64(PortCheckInterval/time.Second)
	nodes := make([]PortCheckScheduleNode, 0, len(sn.nodes))
	for _, node := range sn.nodes {
		if s.portCheckAttempts[node.ID] == period {
			continue
		}
		current := live[node.ID]
		hasEnabledInbound := false
		for _, target := range sn.byNode[node.ID] {
			if target.in.Enabled {
				hasEnabledInbound = true
				break
			}
		}
		nodes = append(nodes, PortCheckScheduleNode{
			ID: node.ID, State: node.State, Online: current.Connected, UDPCheck: slices.Contains(current.AgentCaps, portCheckCapability),
			HasEnabledInbound: hasEnabledInbound, LastCheckedAt: lastChecked[node.ID],
		})
	}
	return nodes, nil
}

func portCheckFoundBadInbound(inbounds []*target, checks []store.PortCheck) bool {
	ports := make(map[uint16]bool, len(inbounds))
	for _, inbound := range inbounds {
		if inbound.in.Enabled && inbound.err == nil {
			port := uint16(inbound.spec.Listen.Port)
			if port != 0 {
				ports[port] = true
			}
		}
	}
	for _, check := range checks {
		if ports[check.Port] && (check.Verdict == "lossy" || check.Verdict == "broken") {
			return true
		}
	}
	return false
}
