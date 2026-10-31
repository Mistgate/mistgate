package fleet

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// ipv6CheckFresh is how long a passing check of an address lets the entry be turned on without a new one.
const ipv6CheckFresh = 10 * time.Minute

// ipv6Fresh says whether the state holds a check of addr that passed and is not older than ipv6CheckFresh.
func (f *Fleet) ipv6Fresh(st store.IPv6State, addr string) (store.IPv6Check, bool) {
	c, ok := st.CheckOf(addr)
	return c, ok && c.Pass && c.Code == "" && f.now().Sub(time.Unix(c.At, 0)) <= ipv6CheckFresh
}

// ipv6Doctors reads the doctor's ipv6 check of every node; a read that fails leaves them "not checked".
func (f *Fleet) ipv6Doctors(ctx context.Context) map[string]store.IPv6Doctor {
	m, err := f.st.IPv6Doctors(ctx)
	if err != nil {
		f.log.Warn("read the doctor's IPv6 of the nodes", "err", err)
	}
	return m
}

// ipv6Doctor is ipv6Doctors for one node.
func (f *Fleet) ipv6Doctor(ctx context.Context, nodeID string) store.IPv6Doctor {
	d, err := f.st.IPv6Doctor(ctx, nodeID)
	if err != nil {
		f.log.Warn("read the doctor's IPv6 of a node", "node", nodeID, "err", err)
	}
	return d
}

// ipv6Msg is Node.ipv6: the entry, whether and why the node can take one, the addresses it could use, and the latest check.
func ipv6Msg(n store.NodeRow, d store.IPv6Doctor) *adminv1.NodeIpv6 {
	out := &adminv1.NodeIpv6{Entry: n.IPv6Entry, UnavailableReason: ipv6Unavailable(n, d), Src: d.Src}
	out.EntrySupported = out.UnavailableReason == ""
	for _, p := range d.Addrs {
		out.Addresses = append(out.Addresses, &adminv1.Ipv6Address{Address: p.Addr().String(), PrefixLen: uint32(p.Bits())})
	}
	st := n.IPv6()
	c, ok := st.CheckOf(n.IPv6Entry)
	if !ok || n.IPv6Entry == "" {
		c, ok = st.Latest()
	}
	if ok {
		out.LastCheck = ipv6CheckMsg(c)
	}
	return out
}

func (s nodeService) UpdateIpv6Entry(ctx context.Context, req *connect.Request[adminv1.UpdateIpv6EntryRequest]) (*connect.Response[adminv1.UpdateIpv6EntryResponse], error) {
	f, m := s.f, req.Msg
	if err := ownerRole(ctx); err != nil {
		return nil, err
	}
	switch m.Action {
	case adminv1.Ipv6EntryAction_IPV6_ENTRY_ACTION_ENABLE:
		return f.enableIPv6Entry(ctx, m)
	case adminv1.Ipv6EntryAction_IPV6_ENTRY_ACTION_DISABLE:
		return f.disableIPv6Entry(ctx, m)
	}
	return nil, invalid("action: enable or disable")
}

var errIPv6EntryOn = connect.NewError(connect.CodeFailedPrecondition, errors.New("ipv6_entry_on"))

// enableIPv6Entry turns the entry on for an address that has passed an outside check less than ten minutes ago, running the check
// itself when there is none. The switch is a compare-and-swap on the node's IPv6 document: of two requests at once one changes
// the node and the other finds it done, and a check that went stale while it waited is not used.
func (f *Fleet) enableIPv6Entry(ctx context.Context, m *adminv1.UpdateIpv6EntryRequest) (*connect.Response[adminv1.UpdateIpv6EntryResponse], error) {
	n, _, addr, err := f.ipv6Target(ctx, m.NodeId, m.Address)
	if err != nil {
		return nil, err
	}
	switch {
	case n.IPv6Entry == addr:
		return f.ipv6EntryReply(ctx, n.ID, "", nil)
	case n.IPv6Entry != "":
		return nil, errIPv6EntryOn
	}
	chk, fresh := f.ipv6Fresh(n.IPv6(), addr)
	if !fresh {
		if chk, err = f.CheckIPv6(ctx, n.ID, addr); err != nil {
			return nil, internalErr(f.log.Error, "check the node's IPv6 address", err)
		}
		if err := f.st.RecordIPv6Check(ctx, n.ID, chk); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, internalErr(f.log.Error, "keep the check of the node's IPv6 address", err)
		}
		if !chk.Pass {
			code := chk.Code
			if code == "" {
				code = "check_failed"
			}
			return f.ipv6EntryReply(ctx, n.ID, code, &chk)
		}
	}
	errStale := errors.New("stale check")
	enabledAt := f.now().Unix()
	_, changed, err := f.st.UpdateIPv6(ctx, n.ID, func(cur store.IPv6Row) (store.IPv6Row, error) {
		switch {
		case cur.Entry == addr:
			return cur, nil
		case cur.Entry != "":
			return cur, errIPv6EntryOn
		}
		if _, ok := f.ipv6Fresh(cur.State, addr); !ok {
			return cur, errStale
		}
		cur.Entry = addr
		cur.State.EnabledAt = enabledAt
		return cur, nil
	})
	switch {
	case errors.Is(err, errStale):
		return nil, connect.NewError(connect.CodeAborted, errors.New("ipv6_check_stale"))
	case errors.Is(err, errIPv6EntryOn):
		return nil, errIPv6EntryOn
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	case err != nil:
		return nil, internalErr(f.log.Error, "turn on the node's IPv6 entry", err)
	}
	if changed {
		f.audit(ctx, "node.ipv6_entry", map[string]string{"node_id": n.ID, "node": n.Name, "action": "on", "address": addr, "by": chk.By})
	}
	return f.ipv6EntryReply(ctx, n.ID, "", &chk)
}

// disableIPv6Entry takes the "· IPv6" servers out of the subscriptions. It works whatever the doctor says: a node whose IPv6 broke
// is the very one that needs it off.
func (f *Fleet) disableIPv6Entry(ctx context.Context, m *adminv1.UpdateIpv6EntryRequest) (*connect.Response[adminv1.UpdateIpv6EntryResponse], error) {
	was := ""
	_, changed, err := f.st.UpdateIPv6(ctx, m.NodeId, func(cur store.IPv6Row) (store.IPv6Row, error) {
		was, cur.Entry = cur.Entry, ""
		cur.State.EnabledAt = 0
		return cur, nil
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	case err != nil:
		return nil, internalErr(f.log.Error, "turn off the node's IPv6 entry", err)
	}
	if changed {
		name := ""
		if row, err := f.st.Node(ctx, m.NodeId); err == nil {
			name = row.Name
		}
		f.audit(ctx, "node.ipv6_entry", map[string]string{"node_id": m.NodeId, "node": name, "action": "off", "address": was})
	}
	return f.ipv6EntryReply(ctx, m.NodeId, "", nil)
}

// ipv6EntryReply answers with the node as it is now.
func (f *Fleet) ipv6EntryReply(ctx context.Context, nodeID, code string, chk *store.IPv6Check) (*connect.Response[adminv1.UpdateIpv6EntryResponse], error) {
	n, err := f.st.Node(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return nil, internalErr(f.log.Error, "read the node", err)
	}
	live, err := f.liveRowsForNode(ctx, nodeID, true)
	if err != nil {
		return nil, internalErr(f.log.Error, "read node live projection", err)
	}
	now := f.now().UTC()
	protos, _ := f.st.FleetNodeProtocols(ctx)
	today, _ := f.todayBytesByNode(ctx, now)
	enabled, _ := f.st.FleetEnabledInbounds(ctx)
	panelBundle, _ := f.panelDecoyBundle()
	out := &adminv1.UpdateIpv6EntryResponse{ErrorCode: code,
		Node: f.nodeMsg(ctx, n, protos[n.ID], today[n.ID], inboundsOf(enabled, n.ID), now, live, panelBundle, f.healthSummaries(ctx)[n.ID], f.ipv6Doctor(ctx, n.ID))}
	if chk != nil {
		out.Check = ipv6CheckMsg(*chk)
	}
	return connect.NewResponse(out), nil
}
