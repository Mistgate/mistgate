package fleet

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// sharedPortsRefresh is the longest the panel goes without writing what an agent says of its shared ports while only the counters
// change: they change with every connection, and nobody reads them closer than this.
const sharedPortsRefresh = 5 * time.Minute

// sharedSeen is what the panel last wrote of a node's shared ports.
type sharedSeen struct {
	shape string // the ports, their routes, the default and the bind error: what a change of is written at once
	at    time.Time
}

// sharedPortsOf is an agent's snapshot as the admin API says it (the field numbers are the same, the names too).
func sharedPortsOf(rows []*agentv1.SharedPort) []*adminv1.SharedPort {
	out := make([]*adminv1.SharedPort, 0, len(rows))
	for _, r := range rows {
		if r == nil || r.Port == 0 || r.Port > 65535 {
			continue
		}
		p := &adminv1.SharedPort{
			Port: r.Port, DefaultInboundId: clipText(r.DefaultInboundId, 64), Accepted: r.Accepted,
			DefaultNoSni: r.DefaultNoSni, DefaultUnknownName: r.DefaultUnknownName, DefaultNotTls: r.DefaultNotTls,
			DefaultBadHello: r.DefaultBadHello, DefaultHelloTooLarge: r.DefaultHelloTooLarge, DefaultRouteDown: r.DefaultRouteDown,
			Timeouts: r.Timeouts, Evicted: r.Evicted, Refused: r.Refused, NoRoute: r.NoRoute, BindError: clipText(r.BindError, 200),
		}
		for _, route := range r.Routes {
			if route != nil && route.InboundId != "" {
				p.Routes = append(p.Routes, &adminv1.SharedRoute{InboundId: clipText(route.InboundId, 64), Delivered: route.Delivered})
			}
		}
		for _, u := range r.UnknownNames[:min(len(r.UnknownNames), 5)] {
			// the name is what a stranger sent: only a host name is kept (the agent filters too; a panel does not trust it)
			if u != nil && len(u.Name) <= 253 && strings.Trim(u.Name, "abcdefghijklmnopqrstuvwxyz0123456789-._") == "" {
				p.UnknownNames = append(p.UnknownNames, &adminv1.SharedUnknownName{Name: u.Name, Count: u.Count})
			}
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *adminv1.SharedPort) int { return cmp.Compare(a.Port, b.Port) })
	if len(out) > 16 {
		out = out[:16]
	}
	return out
}

// sharedPortsShape is what of a snapshot is news at once: the ports, their routes, the default route and a bind error.
func sharedPortsShape(ports []*adminv1.SharedPort) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		ids := make([]string, 0, len(p.Routes))
		for _, r := range p.Routes {
			ids = append(ids, r.InboundId)
		}
		sort.Strings(ids)
		parts = append(parts, fmt.Sprintf("%d|%s|%s|%s", p.Port, p.DefaultInboundId, p.BindError, strings.Join(ids, ",")))
	}
	return strings.Join(parts, ";")
}

// recordSharedPorts consumes the agent's complete snapshot of the TCP ports it shares (StatsBatch.shared_ports; the newest
// wins). A node that has never shared a port costs nothing; a change of what is shared is written at once, a change of the counters
// alone every five minutes. A snapshot with no port after one with some removes the stored one.
func (f *Fleet) recordSharedPorts(ctx context.Context, nodeID string, rows []*agentv1.SharedPort, now time.Time) {
	ports := sharedPortsOf(rows)
	shape := sharedPortsShape(ports)
	f.sharedSeenMu.Lock()
	prev, seen := f.sharedSeen[nodeID]
	switch {
	case !seen && len(ports) == 0:
		f.sharedSeenMu.Unlock()
		return
	case seen && len(ports) > 0 && prev.shape == shape && !now.Before(prev.at) && now.Sub(prev.at) < sharedPortsRefresh:
		f.sharedSeenMu.Unlock()
		return
	case len(ports) == 0:
		delete(f.sharedSeen, nodeID)
	default:
		f.sharedSeen[nodeID] = sharedSeen{shape: shape, at: now}
	}
	f.sharedSeenMu.Unlock()

	body := ""
	if len(ports) > 0 {
		b, err := protojson.Marshal(&adminv1.GetNodeResponse{SharedPorts: ports, SharedPortsUnix: now.Unix()})
		if err != nil {
			f.log.Warn("encode shared ports", "code", "shared_ports_encode_failed", "node", nodeID, "err", err)
			return
		}
		body = string(b)
	}
	if err := f.st.SetSharedPorts(ctx, nodeID, body, now); err != nil {
		f.log.Warn("store shared ports", "code", "shared_ports_store_failed", "node", nodeID, "err", err)
	}
}

// storedSharedPorts is the node's last snapshot and when it was written; nil when it shares none.
func (f *Fleet) storedSharedPorts(ctx context.Context, nodeID string) ([]*adminv1.SharedPort, int64, error) {
	body, at, ok, err := f.st.SharedPorts(ctx, nodeID)
	if err != nil || !ok {
		return nil, 0, err
	}
	var snap adminv1.GetNodeResponse
	if err := protojson.Unmarshal([]byte(body), &snap); err != nil {
		f.log.Warn("read shared ports", "code", "shared_ports_unreadable", "node", nodeID, "err", err)
		return nil, 0, nil
	}
	return snap.SharedPorts, at.Unix(), nil
}
