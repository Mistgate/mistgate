package access

import (
	"context"
	"strconv"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

const portCheckTimeout = 8 * time.Second

type portCheckCache map[string]map[uint16]store.PortCheck

func (s *Service) readPortChecks(ctx context.Context, nodeIDs ...string) (portCheckCache, error) {
	if len(nodeIDs) == 0 {
		return portCheckCache{}, nil
	}
	if s.portChecksReadHookForTest != nil {
		s.portChecksReadHookForTest()
	}
	rows, err := s.st.PortChecks(ctx, nodeIDs...)
	if err != nil {
		return nil, err
	}
	out := make(portCheckCache)
	for _, row := range rows {
		if out[row.NodeID] == nil {
			out[row.NodeID] = make(map[uint16]store.PortCheck)
		}
		out[row.NodeID][row.Port] = row
	}
	return out, nil
}

func freePortCandidates(protocol string, n int, listen plugin.Listen, others []nodeListen,
	checks map[uint16]store.PortCheck, now time.Time) []uint16 {
	if n <= 0 {
		return nil
	}
	limit := len(twinPreferred) + 64
	candidates := freePorts(protocol, limit, func(port uint16) bool { return portAllowed(listen, port, others) })
	var fresh, unchecked []uint16
	for _, port := range candidates {
		if check, ok := checks[port]; ok && check.Bad(now) {
			continue
		}
		if check, ok := checks[port]; ok && check.Fresh(now) {
			fresh = append(fresh, port)
		} else {
			unchecked = append(unchecked, port)
		}
	}
	ordered := append(fresh, unchecked...)
	if len(ordered) > n {
		ordered = ordered[:n]
	}
	return ordered
}

func badPortVerdict(verdict string) bool { return verdict == "lossy" || verdict == "broken" }

func (s *Service) portSenderName(ctx context.Context, sender string) string {
	if sender == "" || sender == "panel" {
		return sender
	}
	if node, err := s.st.Access().Node(ctx, sender); err == nil {
		return node.Name
	}
	return sender
}

func (s *Service) portLossyWarning(ctx context.Context, node string, check store.PortCheck) *adminv1.StatusReason {
	at := check.BadAt
	if at.IsZero() {
		at = check.CheckedAt
	}
	if at.IsZero() {
		at = s.now()
	}
	return &adminv1.StatusReason{Code: "port_lossy", Params: map[string]string{
		"node": node, "port": strconv.Itoa(int(check.Port)), "sent": strconv.Itoa(int(check.Sent)),
		"got": strconv.Itoa(int(check.Got)), "at": strconv.FormatInt(at.Unix(), 10), "sender": s.portSenderName(ctx, check.Sender),
	}}
}

func portUncheckedWarning(node string, port uint16, reason string) *adminv1.StatusReason {
	if reason == "" {
		reason = "inconclusive"
	}
	return &adminv1.StatusReason{Code: "port_unchecked", Params: map[string]string{
		"node": node, "port": strconv.Itoa(int(port)), "reason": reason,
	}}
}

func portLossyRefusal(ctx context.Context, s *Service, node string, check store.PortCheck, free uint16) error {
	warning := s.portLossyWarning(ctx, node, check)
	p := warning.Params
	return coded(connect.CodeFailedPrecondition, "port_lossy", "port", p["port"], "node", p["node"],
		"sent", p["sent"], "got", p["got"], "at", p["at"], "sender", p["sender"], "free", strconv.Itoa(int(free)))
}

func cachedPortLossy(checks map[uint16]store.PortCheck, port uint16, now time.Time) (store.PortCheck, bool) {
	check, ok := checks[port]
	return check, ok && check.Bad(now)
}

func (s *Service) checkPort(ctx context.Context, node store.AccessNode, protocol string, listen plugin.Listen,
	others []nodeListen, checks map[uint16]store.PortCheck, allowLossy bool) (*adminv1.StatusReason, bool, error) {
	if s.cfg.CheckPorts == nil {
		return nil, false, nil
	}
	if cached, ok := checks[listen.Port]; ok && cached.Fresh(s.now()) {
		return nil, false, nil
	}

	requested := []uint16{listen.Port}
	if listen.Port != 443 {
		requested = append(requested, 443)
	}
	seen := make(map[uint16]bool, len(requested))
	for _, port := range requested {
		seen[port] = true
	}
	for _, candidate := range freePortCandidates(protocol, 3, listen, others, checks, s.now()) {
		if !seen[candidate] {
			requested = append(requested, candidate)
			seen[candidate] = true
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, portCheckTimeout)
	rows, sender, reason := s.cfg.CheckPorts(runCtx, node.ID, requested)
	cancel()
	if reason != "" {
		return portUncheckedWarning(node.Name, listen.Port, reason), false, nil
	}
	byPort := make(map[uint16]store.PortCheck, len(rows))
	for _, row := range rows {
		if row.Sender == "" {
			row.Sender = sender
		}
		byPort[row.Port] = row
	}
	checked, ok := byPort[listen.Port]
	if !ok || checked.Verdict == "" {
		return portUncheckedWarning(node.Name, listen.Port, "inconclusive"), false, nil
	}
	if badPortVerdict(checked.Verdict) {
		free := uint16(0)
		for _, candidate := range requested {
			if candidate == listen.Port || !portAllowed(listen, candidate, others) {
				continue
			}
			if row, ok := byPort[candidate]; ok && row.Verdict == "ok" {
				free = candidate
				break
			}
		}
		if free == 0 {
			free = freePortFor(protocol, listen, others, checks, s.now())
		}
		if !allowLossy {
			return nil, false, portLossyRefusal(ctx, s, node.Name, checked, free)
		}
		return s.portLossyWarning(ctx, node.Name, checked), true, nil
	}
	if checked.Verdict != "ok" {
		return portUncheckedWarning(node.Name, listen.Port, "inconclusive"), false, nil
	}
	return nil, false, nil
}

func portAllowed(listen plugin.Listen, port uint16, others []nodeListen) bool {
	if port == listen.Port || (listen.HopFrom != 0 && port >= listen.HopFrom && port <= listen.HopTo) {
		return false
	}
	moved := listen
	moved.Port = port
	return clashOf(moved, others) == nil
}

func (s *Service) auditLossyPort(ctx context.Context, node string, port uint16) {
	s.audit(ctx, actor(ctx), "port_lossy_override", map[string]any{
		"node": node, "port": port, "decision": "admin chose to save a lossy port",
	})
}

func (s *Service) portCheckMessage(ctx context.Context, row store.PortCheck, reason string) *adminv1.PortCheck {
	badAt := int64(0)
	if !row.BadAt.IsZero() {
		badAt = row.BadAt.Unix()
	}
	checkedAt := int64(0)
	if !row.CheckedAt.IsZero() {
		checkedAt = row.CheckedAt.Unix()
	}
	if row.Verdict != "" {
		reason = ""
	}
	return &adminv1.PortCheck{NodeId: row.NodeID, Port: uint32(row.Port), Verdict: row.Verdict, Sent: row.Sent, Got: row.Got,
		CheckedUnix: checkedAt, BadUnix: badAt, Sender: s.portSenderName(ctx, row.Sender), Reason: reason}
}
