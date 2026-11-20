package fleet

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
	"google.golang.org/protobuf/proto"
)

// What the subscription host (internal/panel/subhost, design/adr/0018-panel-behind-nat.md) needs of the fleet besides the
// node settings (SetSubHostRoles, SetPanelLink).

// subHostHooks is the fleet's part of the subscription host.
type subHostHooks struct {
	linkHosts      sync.Map    // node id -> the host name its link session dialled
	linkEnrollment atomic.Bool // new nodes enrol over the link (the panel's address is the host's subdomain)
	isHost         atomic.Pointer[func(string) bool]
	status         sync.Map // node id -> *agentv1.SubHostStatus, its last report
}

// InboundSpecs are the specs of the node's enabled inbounds as the agent gets them.
func (f *Fleet) InboundSpecs(ctx context.Context, nodeID string) ([]plugin.InboundSpec, error) {
	n, err := f.st.Node(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	specs, _, err := f.tcpSpecsOf(ctx, n)
	if err != nil {
		return nil, err
	}
	out := make([]plugin.InboundSpec, len(specs))
	for i, s := range specs {
		out[i] = s.Spec
	}
	return out, nil
}

// SessionHost is the host name a node's link session dialled (lower case, no port), "" when it has none or runs over mTLS.
func (f *Fleet) SessionHost(nodeID string) string {
	if v, ok := f.sub.linkHosts.Load(nodeID); ok {
		return v.(*linkHost).host
	}
	return ""
}

// linkHost is one link session's host: a session's done removes its own entry, never the one of a session that replaced it
// (two sessions through the same host have equal names, so the entries are compared by identity).
type linkHost struct{ host string }

// noteLinkHost records the host a link session dialled until the session ends (done).
func (f *Fleet) noteLinkHost(nodeID, audience string) (done func()) {
	host := audience
	if h, _, err := net.SplitHostPort(audience); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	mine := &linkHost{host: host}
	f.sub.linkHosts.Store(nodeID, mine)
	return func() { f.sub.linkHosts.CompareAndDelete(nodeID, mine) }
}

// SetLinkEnrollment makes new nodes enrol over the link (true while a subscription host is the panel's address: the panel's
// mTLS endpoint is not reachable then).
func (f *Fleet) SetLinkEnrollment(on bool) { f.sub.linkEnrollment.Store(on) }

// LinkEnrollment says new nodes enrol over the link (SetLinkEnrollment).
func (f *Fleet) LinkEnrollment() bool { return f.sub.linkEnrollment.Load() }

// SetSubHostNodes tells the fleet which nodes are subscription hosts: they cannot be retired while they are.
func (f *Fleet) SetSubHostNodes(isHost func(nodeID string) bool) { f.sub.isHost.Store(&isHost) }

func (f *Fleet) subHostNode(nodeID string) bool {
	if p := f.sub.isHost.Load(); p != nil && *p != nil {
		return (*p)(nodeID)
	}
	return false
}

// errSubHostInUse refuses to retire the subscription host (the admin words it: err.sub_host_in_use).
var errSubHostInUse = connect.NewError(connect.CodeFailedPrecondition, errors.New("sub_host_in_use"))

// recordSubHost keeps what a host reports of itself (StatsBatch.sub_host); nil forgets it.
func (f *Fleet) recordSubHost(nodeID string, st *agentv1.SubHostStatus) {
	if st == nil {
		f.sub.status.Delete(nodeID)
		return
	}
	f.sub.status.Store(nodeID, proto.Clone(st).(*agentv1.SubHostStatus))
}

// SubHostStatus is what the host node last reported of its role, nil when it reported none.
func (f *Fleet) SubHostStatus(nodeID string) *agentv1.SubHostStatus {
	if v, ok := f.sub.status.Load(nodeID); ok {
		return v.(*agentv1.SubHostStatus)
	}
	return nil
}

// SubHostTarget is how the panel calls a host's site on 443 and verifies it: its address, the SNI, and a leaf pin when the
// site has no public certificate (public then false). With an inbound's site on 443 that is the bridge's choice; with the
// agent's own site it is siteName, verified by what the node reported, or before that by bootstrapPin (the SSH install's).
// ok is false when there is nothing to verify the host by yet: no dial, never an unverified one.
func (f *Fleet) SubHostTarget(ctx context.Context, nodeID, siteName, bootstrapPin string) (host, sni, pin string, public, ok bool) {
	n, err := f.st.Node(ctx, nodeID)
	if err != nil || n.Address == "" {
		return "", "", "", false, false
	}
	if siteName == "" {
		t, found := f.bridgeTLSName(ctx, n)
		if !found {
			return "", "", "", false, false
		}
		if t.PublicCert {
			return n.Address, t.SNI, "", true, true
		}
		pin, ok := protocols.NormalizePin(t.Pin)
		return n.Address, t.SNI, pin, false, ok
	}
	st := f.SubHostStatus(nodeID)
	switch {
	case st != nil && st.SiteCertPublic:
		return n.Address, siteName, "", true, true
	case st != nil && st.SitePinSha256 != "":
		pin, ok := protocols.NormalizePin(st.SitePinSha256)
		return n.Address, siteName, pin, false, ok
	}
	pin, ok = protocols.NormalizePin(bootstrapPin)
	return n.Address, siteName, pin, false, ok
}
