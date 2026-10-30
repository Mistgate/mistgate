package fleet

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The reasons a node cannot take an IPv6 entry (Ipv6Info.unavailable_reason, and the FAILED_PRECONDITION of CheckIpv6 and
// UpdateIpv6Entry). They are stable words; the admin UI words them.
const (
	ipv6MainIsIPv6 = "main_is_ipv6" // the node's address is already an IPv6 literal: there is nothing to add
	ipv6NoIPv6     = "no_ipv6"      // the host has no global IPv6 address
	ipv6BrokenWord = "ipv6_broken"  // it has one but outbound IPv6 does not work, so no connection will come in either
	ipv6AgentOldW  = "agent_old"    // IPv6 works but the agent names no address of the uplink
	ipv6NotChecked = "not_checked"  // the doctor has not reported yet
)

// ipv6Unavailable says why the node cannot take an IPv6 entry ("" when it can): its address is IPv6 already, or what the doctor's
// ipv6 check says of its uplink.
func ipv6Unavailable(n store.NodeRow, d store.IPv6Doctor) string {
	if _, ok := store.UsableIPv6(n.Address); ok {
		return ipv6MainIsIPv6
	}
	switch d.State {
	case store.IPv6None:
		return ipv6NoIPv6
	case store.IPv6Broken:
		return ipv6BrokenWord
	case store.IPv6OK:
		if d.FirstAddr() == "" {
			return ipv6AgentOldW
		}
		return ""
	}
	return ipv6NotChecked
}

// ownerRole: the signed-in owner, or a token of the admin profile (which acts as the owner). A helper and a readonly admin are
// refused whatever the policy table says. The one decision of "may this call change where people connect".
func ownerRole(ctx context.Context) error {
	if a, ok := auth.AdminFrom(ctx); !ok || a.Role != store.RoleOwner {
		return connect.NewError(connect.CodePermissionDenied, errors.New("only the owner can use an IPv6 entry"))
	}
	return nil
}

// ipv6Target reads the node and its doctor's view and settles which address a request is about: the given one (it has to be one
// of the node's uplink addresses) or the one the node uses for outbound IPv6.
func (f *Fleet) ipv6Target(ctx context.Context, nodeID, given string) (store.NodeRow, store.IPv6Doctor, string, error) {
	n, err := f.st.Node(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) {
		return n, store.IPv6Doctor{}, "", connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	}
	if err != nil {
		return n, store.IPv6Doctor{}, "", internalErr(f.log.Error, "read node for the IPv6 entry", err)
	}
	d, err := f.st.IPv6Doctor(ctx, n.ID)
	if err != nil {
		return n, d, "", internalErr(f.log.Error, "read the doctor's IPv6 for the IPv6 entry", err)
	}
	if n.State != "active" {
		return n, d, "", errNodeOffline
	}
	if why := ipv6Unavailable(n, d); why != "" {
		return n, d, "", connect.NewError(connect.CodeFailedPrecondition, errors.New(why))
	}
	addr := strings.TrimSpace(given)
	if addr == "" {
		addr = d.FirstAddr()
	}
	if !d.Has(addr) {
		return n, d, "", invalid("address: not an IPv6 address of this node")
	}
	canon, _ := store.UsableIPv6(addr)
	return n, d, canon, nil
}

func (s nodeService) CheckIpv6(ctx context.Context, req *connect.Request[adminv1.CheckIpv6Request]) (*connect.Response[adminv1.CheckIpv6Response], error) {
	f := s.f
	if err := ownerRole(ctx); err != nil {
		return nil, err
	}
	n, _, addr, err := f.ipv6Target(ctx, req.Msg.NodeId, req.Msg.Address)
	if err != nil {
		return nil, err
	}
	chk, err := f.CheckIPv6(ctx, n.ID, addr)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
	case err != nil:
		return nil, internalErr(f.log.Error, "check the node's IPv6 address", err)
	}
	names, err := f.nodeNames(ctx)
	if err != nil {
		return nil, internalErr(f.log.Error, "check the node's IPv6 address", err)
	}
	return connect.NewResponse(ipv6CheckMsg(names, chk)), nil
}

// ipv6SenderName is the sender of a check as the admin reads it: a node's name, "panel" or "globalping".
func ipv6SenderName(names map[string]string, by string) string {
	switch by {
	case "", store.IPv6ByPanel, store.IPv6ByGlobalping:
		return by
	}
	if name := names[by]; name != "" {
		return name
	}
	return "unknown"
}

func ipv6VerdictMsgs(v []store.IPv6Verdict) []*adminv1.Ipv6PortVerdict {
	out := make([]*adminv1.Ipv6PortVerdict, len(v))
	for i, x := range v {
		out[i] = &adminv1.Ipv6PortVerdict{Network: x.Net, Port: uint32(x.Port), Verdict: x.Verdict}
	}
	return out
}

func ipv6CheckMsg(names map[string]string, c store.IPv6Check) *adminv1.CheckIpv6Response {
	return &adminv1.CheckIpv6Response{Address: c.Addr, Passed: c.Pass, ErrorCode: c.Code, Verdicts: ipv6VerdictMsgs(c.Verdicts),
		Sender: ipv6SenderName(names, c.By), CheckedUnix: c.At}
}
