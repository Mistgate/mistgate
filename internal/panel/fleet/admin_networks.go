//go:build !js

package fleet

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"time"
	"unicode"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/asndb"
	"github.com/mistgate/mistgate/internal/logscrub"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const maxNetworkRows = 5000

const networkASNCacheTTL = 5 * time.Minute

type networkTableAccess interface {
	NetworksTable() *asndb.DB
	NetworkTableFile() (name, sha256 string)
}

type networkNodeIPv4 interface {
	NodeIPv4(context.Context, store.NodeRow) (netip.Addr, bool)
}

// GetNetworks returns per-inbound ASN counters for the requested whole-hour window and the equal window before it.
func (s nodeService) GetNetworks(ctx context.Context, req *connect.Request[adminv1.GetNetworksRequest]) (*connect.Response[adminv1.GetNetworksResponse], error) {
	f := s.f
	now := f.now().UTC()
	hours := req.Msg.GetHours()
	if hours == 0 {
		hours = 24
	}
	if hours > 168 {
		return nil, invalid("hours must be between 1 and 168")
	}
	if len(req.Msg.GetNodeId()) > 64 {
		return nil, invalid("node_id is too long")
	}
	windowEnd := now.Truncate(time.Hour).Add(time.Hour)
	since := windowEnd.Add(-time.Duration(hours) * time.Hour)
	previousSince := since.Add(-time.Duration(hours) * time.Hour)

	nodes, err := f.st.FleetNetworkNodes(ctx)
	if err != nil {
		return nil, internalErr(f.log.Error, "fleet network nodes", err)
	}
	if req.Msg.GetNodeId() != "" {
		found := false
		for _, n := range nodes {
			if n.ID == req.Msg.GetNodeId() {
				found = true
				break
			}
		}
		if !found {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("node not found"))
		}
	}
	selected := make([]store.FleetNetworkNode, 0, len(nodes))
	for _, n := range nodes {
		if req.Msg.GetNodeId() == "" || req.Msg.GetNodeId() == n.ID {
			selected = append(selected, n)
		}
	}

	var table networkTableReader = f.networkTableOverride
	var bundleSHA string
	if updater, ok := f.updates().(networkTableAccess); ok {
		if table == nil {
			if loaded := updater.NetworksTable(); loaded != nil {
				table = loaded
			}
		}
		_, bundleSHA = updater.NetworkTableFile()
	}
	response := &adminv1.GetNetworksResponse{SinceUnix: since.Unix(), NowUnix: now.Unix(), Hours: hours,
		Mode: f.NetworkMode(), Rows: []*adminv1.NetworkHourRow{}, NodesWithoutNets: []*adminv1.NetworkNodeRef{}, NodesWithoutTable: []*adminv1.NetworkNodeRef{}}
	if table != nil {
		response.TableBuiltUnix = table.Built().Unix()
	}

	type inboundInfo struct {
		profileName string
		protocol    string
		cdn         bool
	}
	inbounds := make(map[string]map[string]inboundInfo, len(selected))
	fleetASNs := make(map[uint32]bool, len(nodes))
	for _, n := range nodes {
		if table != nil {
			if asn := f.networkNodeASN(ctx, n, bundleSHA, table); asn != 0 {
				fleetASNs[asn] = true
			}
		}
	}
	for _, n := range selected {
		if !store.CapabilityPresent(n.AgentCaps, capNetworks) {
			response.NodesWithoutNets = append(response.NodesWithoutNets, &adminv1.NetworkNodeRef{NodeId: n.ID, NodeName: cleanNetworkText(n.Name, 80)})
		} else if bundleSHA == "" || n.NetworksDBSHA256 != bundleSHA {
			response.NodesWithoutTable = append(response.NodesWithoutTable, &adminv1.NetworkNodeRef{NodeId: n.ID, NodeName: cleanNetworkText(n.Name, 80)})
		}
		inbounds[n.ID] = make(map[string]inboundInfo, len(n.Inbounds))
		for _, inbound := range n.Inbounds {
			inbounds[n.ID][inbound.ID] = inboundInfo{profileName: inbound.ProfileName, protocol: inbound.Protocol, cdn: inbound.CDN}
		}
	}

	sums, err := f.st.FleetNetworkHourSums(ctx, since, previousSince, windowEnd, req.Msg.GetNodeId(), maxNetworkRows+1)
	if err != nil {
		return nil, internalErr(f.log.Error, "fleet network counters", err)
	}
	response.Truncated = len(sums) > maxNetworkRows
	if response.Truncated {
		sums = sums[:maxNetworkRows]
	}
	for _, sum := range sums {
		var inbound inboundInfo
		if byInbound := inbounds[sum.NodeID]; byInbound != nil {
			inbound = byInbound[sum.InboundID]
		}
		country, name := "", ""
		if table != nil {
			country, name, _ = table.Name(sum.ASN)
		}
		operator := f.networkOperatorName(country, sum.ASN)
		response.Rows = append(response.Rows, &adminv1.NetworkHourRow{
			NodeId: sum.NodeID, NodeName: nodeName(selected, sum.NodeID), InboundId: sum.InboundID,
			ProfileName: cleanNetworkText(inbound.profileName, 120), Protocol: cleanNetworkText(inbound.protocol, 32), Asn: sum.ASN,
			Country: cleanNetworkText(country, 2), Name: cleanNetworkText(name, 160), Operator: cleanNetworkText(operator, 80),
			Attempts: sum.Attempts, Unfinished: sum.Unfinished, Connects: sum.Connects, Broken: sum.Broken, PeakPeople: sum.PeakPeople,
			BaselineAttempts: sum.BaselineAttempts, BaselineUnfinished: sum.BaselineUnfinished, BaselineConnects: sum.BaselineConnects,
			BaselineBroken: sum.BaselineBroken, BaselinePeakPeople: sum.BaselinePeakPeople,
			Fleet: fleetASNs[sum.ASN], Cdn: inbound.cdn,
		})
	}
	return connect.NewResponse(response), nil
}

func nodeName(nodes []store.FleetNetworkNode, id string) string {
	for _, n := range nodes {
		if n.ID == id {
			return cleanNetworkText(n.Name, 80)
		}
	}
	return ""
}

func (f *Fleet) networkNodeAddr(ctx context.Context, node store.FleetNetworkNode) (netip.Addr, bool) {
	if address, err := netip.ParseAddr(strings.Trim(node.Address, "[]")); err == nil {
		return address.Unmap(), true
	}
	if resolver, ok := f.hooks().(networkNodeIPv4); ok {
		return resolver.NodeIPv4(ctx, store.NodeRow{ID: node.ID, Address: node.Address})
	}
	return netip.Addr{}, false
}

func (f *Fleet) networkNodeASN(ctx context.Context, node store.FleetNetworkNode, bundleSHA string, table networkTableReader) uint32 {
	now := f.now().UTC()
	builtUnix := table.Built().Unix()
	f.networkASNMu.Lock()
	if cached, ok := f.networkASNs[node.ID]; ok && cached.address == node.Address && cached.bundleSHA == bundleSHA &&
		cached.builtUnix == builtUnix && now.Before(cached.expires) {
		f.networkASNMu.Unlock()
		return cached.asn
	}
	f.networkASNMu.Unlock()

	var asn uint32
	if address, ok := f.networkNodeAddr(ctx, node); ok {
		asn = table.Lookup(address)
	}

	f.networkASNMu.Lock()
	if f.networkASNs == nil {
		f.networkASNs = make(map[string]networkASNCacheEntry)
	}
	if len(f.networkASNs) > 1024 {
		for id, cached := range f.networkASNs {
			if !now.Before(cached.expires) {
				delete(f.networkASNs, id)
			}
		}
	}
	f.networkASNs[node.ID] = networkASNCacheEntry{address: node.Address, bundleSHA: bundleSHA, builtUnix: builtUnix, asn: asn, expires: now.Add(networkASNCacheTTL)}
	f.networkASNMu.Unlock()
	return asn
}

func (f *Fleet) networkOperatorName(country string, asn uint32) string {
	if f.networkOperator != nil {
		return f.networkOperator(country, asn)
	}
	return health.NetworkOperator(country, asn)
}

func cleanNetworkText(value string, limit int) string {
	value = logscrub.Addrs(strings.ToValidUTF8(value, ""))
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return logscrub.Clip(strings.Join(strings.Fields(value), " "), limit)
}
