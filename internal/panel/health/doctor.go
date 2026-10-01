package health

import (
	"context"
	"regexp"
	"slices"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The node doctor on the panel side: storage of the latest report of each node, the views built
// from it, and the relay of RunDoctor / ApplyFix through the fleet.

// capDoctor is the Hello capability of an agent that can run the doctor (agent.proto "DOCTOR").
const capDoctor = "doctor/1"

// fixIDs are the four fixes the agent has (agent.proto "FIX IDS"). A fix id anywhere else is ignored.
var fixIDs = []string{"journald_vacuum", "apply_baseline", "restart_inbound", "set_resolver"}

func knownFix(id string) string {
	if slices.Contains(fixIDs, id) {
		return id
	}
	return ""
}

const (
	maxDoctorResults = 64
	maxDoctorParams  = 16
)

var checkIDRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// detailCodeRe is the shape of DoctorResult.detail_code ("disk_space.usage"); anything else is dropped, the UI then
// shows the English detail line.
var detailCodeRe = regexp.MustCompile(`^[a-z0-9_]{1,40}\.[a-z0-9_]{1,40}$`)

// DoctorReport stores a report of a node (fleet.Health). A full report replaces the node's rows, a partial one
// merges by check id; a report that says it did not run ("busy", "not_root") or holds nothing changes nothing.
// What a node says is bounded and cleaned first: it is stored and shown in the UI.
func (s *Service) DoctorReport(ctx context.Context, nodeID string, r *agentv1.DoctorReport) {
	if r.Error != "" || len(r.Results) == 0 {
		return
	}
	now := s.now()
	seen := map[string]int{}
	var rows []store.DoctorRow
	for _, x := range r.Results {
		if len(rows) == maxDoctorResults {
			break
		}
		if !checkIDRe.MatchString(x.Id) || x.Status < agentv1.DoctorStatus_DOCTOR_STATUS_OK || x.Status > agentv1.DoctorStatus_DOCTOR_STATUS_SKIP {
			continue
		}
		row := store.DoctorRow{NodeID: nodeID, CheckID: x.Id, Status: int(x.Status), TitleKey: store.Clip(x.TitleKey, 128),
			Detail: store.Clip(x.Detail, 256), Measured: now}
		if row.TitleKey == "" {
			row.TitleKey = "doctor." + x.Id + ".title"
		}
		if detailCodeRe.MatchString(x.DetailCode) {
			row.DetailCode = x.DetailCode
		}
		if x.MeasuredUnix > 0 && x.MeasuredUnix <= now.Add(5*time.Minute).Unix() {
			row.Measured = time.Unix(x.MeasuredUnix, 0)
		}
		if len(x.Params) > 0 {
			row.Params = map[string]string{}
			for k, v := range x.Params {
				if len(row.Params) == maxDoctorParams {
					break
				}
				row.Params[store.Clip(k, 64)] = store.Clip(v, 256)
			}
		}
		if x.Status == agentv1.DoctorStatus_DOCTOR_STATUS_WARN || x.Status == agentv1.DoctorStatus_DOCTOR_STATUS_FAIL {
			row.FixID = knownFix(x.FixId)
		}
		reassess(&row)
		if i, dup := seen[x.Id]; dup {
			rows[i] = row
			continue
		}
		seen[x.Id] = len(rows)
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return
	}
	if err := s.st.PutDoctor(ctx, nodeID, rows, !r.Partial, now); err != nil {
		s.log.Warn("health: store doctor report", "node", nodeID, "err", err)
		return
	}
	if err := s.st.DropStaleAccepts(ctx, nodeID); err != nil { // a warning that changed or turned FAIL counts again
		s.log.Warn("health: drop stale acceptances", "node", nodeID, "err", err)
	}
	s.repMu.Lock()
	for _, ch := range s.waiters[nodeID] {
		close(ch)
	}
	delete(s.waiters, nodeID)
	s.repMu.Unlock()
	s.evaluateSoon()
}

// codeIPv6WarpOK is the panel's own fact for a host without IPv6 that runs WARP (doctor.detail.ipv6.none_warp_ipv4).
const codeIPv6WarpOK = "ipv6.none_warp_ipv4"

// reassess corrects what the agent cannot know. ipv6.none_warp warns that WARP has no IPv6 on this host; but the panel
// always gives WARP an IPv4 endpoint (warp.specOf never sends EndpointV6), so the tunnel never needs IPv6 and the
// warning is a fact, not a problem: it would keep a yellow badge on every IPv6-less node for nothing (ops-25).
func reassess(r *store.DoctorRow) {
	if r.CheckID == "ipv6" && r.DetailCode == "ipv6.none_warp" && r.Status == int(agentv1.DoctorStatus_DOCTOR_STATUS_WARN) {
		r.Status, r.DetailCode, r.FixID = int(agentv1.DoctorStatus_DOCTOR_STATUS_OK), codeIPv6WarpOK, ""
		r.Detail = "no global IPv6 address; WARP uses its IPv4 endpoint"
	}
}

// nextReport returns a channel that closes when the next doctor report of the node is stored.
func (s *Service) nextReport(nodeID string) <-chan struct{} {
	ch := make(chan struct{})
	s.repMu.Lock()
	s.waiters[nodeID] = append(s.waiters[nodeID], ch)
	s.repMu.Unlock()
	return ch
}

// nodeDoctor builds the doctor state of one node from its stored rows. The offered fix is cleared when the
// node cannot apply it now (offline, or an agent that predates the doctor). ts are the node's inbounds (the profile
// names of the params), acc its accepted warnings.
func (s *Service) nodeDoctor(ctx context.Context, n store.NodeRow, rows []store.DoctorRow, now time.Time, ts []*target, acc map[string]store.DoctorAccept) *adminv1.NodeDoctor {
	connected, caps, _ := s.fl.Live(n.ID)
	canFix := connected && slices.Contains(caps, capDoctor)
	nd := &adminv1.NodeDoctor{NodeId: n.ID, NodeName: n.Name, NodeStatus: s.fl.NodeStatus(ctx, n),
		AgentSupported: canFix || len(rows) > 0 || slices.Contains(n.AgentCaps, capDoctor), HasReport: len(rows) > 0,
		AgentVersion: n.AgentVersion, LastSeenUnix: fleetUnix(n.LastSeenAt)}
	if len(rows) == 0 {
		return nd
	}
	var received time.Time
	for _, r := range rows {
		received = latest(received, r.Received)
	}
	age := max(now.Sub(received), 0)
	nd.ReceivedUnix, nd.AgeS, nd.Stale = received.Unix(), uint32(age/time.Second), age > doctorStale
	sortDoctor(rows)
	for _, r := range rows {
		it := &adminv1.DoctorItem{Id: r.CheckID, Status: adminv1.DoctorStatus(r.Status), TitleKey: r.TitleKey, Detail: r.Detail,
			DetailCode: r.DetailCode, Params: withNames(r.CheckID, r.Params, ts), MeasuredUnix: fleetUnix(r.Measured)}
		if r.Status != int(agentv1.DoctorStatus_DOCTOR_STATUS_OK) {
			it.WhyKey = doctorWhyKey(r)
		}
		if canFix {
			it.FixId = r.FixID
		}
		if a, ok := accepted(r, acc); ok {
			it.AcceptedUnix, it.AcceptedBy, it.AcceptedByName = fleetUnix(a.At), a.By, a.ByName
		}
		nd.Items = append(nd.Items, it)
	}
	return nd
}

func fleetUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// doctorViews builds NodeDoctor for one node, or for every non-retired node (name order).
func (s *Service) doctorViews(ctx context.Context, nodeID string) ([]*adminv1.NodeDoctor, error) {
	nodes, err := s.st.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	rows, err := s.st.DoctorResults(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	sn, err := s.snapshot(ctx)
	if err != nil {
		return nil, err
	}
	accepts, err := s.st.DoctorAccepts(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	acc := acceptsByNode(accepts)
	byNode := map[string][]store.DoctorRow{}
	for _, r := range rows {
		byNode[r.NodeID] = append(byNode[r.NodeID], r)
	}
	now := s.now()
	var out []*adminv1.NodeDoctor
	for _, n := range nodes {
		if nodeID == "" || n.ID == nodeID {
			out = append(out, s.nodeDoctor(ctx, n, byNode[n.ID], now, sn.byNode[n.ID], acc[n.ID]))
		}
	}
	return out, nil
}
