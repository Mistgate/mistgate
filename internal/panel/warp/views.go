package warp

import (
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// healthFresh is how old the node's last WarpHealth may be before the badge says "unknown" instead of repeating it
// (a node reports with every stats batch).
const healthFresh = 5 * time.Minute

var sourceProto = map[string]adminv1.WarpSource{
	store.WarpRegistered: adminv1.WarpSource_WARP_SOURCE_REGISTERED,
	store.WarpImported:   adminv1.WarpSource_WARP_SOURCE_IMPORTED,
}

var agentStateProto = map[agentv1.WarpState]adminv1.WarpState{
	agentv1.WarpState_WARP_STATE_UNAVAILABLE: adminv1.WarpState_WARP_STATE_UNAVAILABLE,
	agentv1.WarpState_WARP_STATE_STARTING:    adminv1.WarpState_WARP_STATE_STARTING,
	agentv1.WarpState_WARP_STATE_UP:          adminv1.WarpState_WARP_STATE_UP,
	agentv1.WarpState_WARP_STATE_DOWN:        adminv1.WarpState_WARP_STATE_DOWN,
	agentv1.WarpState_WARP_STATE_DISABLED:    adminv1.WarpState_WARP_STATE_DISABLED,
}

// health decodes the stored WarpHealth; nil when the node never reported.
func health(a store.WarpAccountRow) *agentv1.WarpHealth {
	if a.HealthJSON == "" {
		return nil
	}
	var h agentv1.WarpHealth
	if (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(a.HealthJSON), &h) != nil {
		return nil
	}
	return &h
}

// ViewState is the state the node badge shows (for the fleet module's Node.warp): a is nil when the node has no
// account, online is whether the node has an agent stream now.
func ViewState(a *store.WarpAccountRow, online bool, now time.Time) adminv1.WarpState {
	switch {
	case a == nil:
		return adminv1.WarpState_WARP_STATE_NOT_CONFIGURED
	case !a.Enabled:
		return adminv1.WarpState_WARP_STATE_DISABLED
	}
	h := health(*a)
	if h == nil || !online || now.Sub(a.HealthAt) > healthFresh {
		return adminv1.WarpState_WARP_STATE_UNKNOWN
	}
	if st, ok := agentStateProto[h.State]; ok {
		return st
	}
	return adminv1.WarpState_WARP_STATE_UNKNOWN
}

// Summary is the WARP badge of a node card (Node.warp); nil a gives the "not configured" badge.
func Summary(a *store.WarpAccountRow, online bool, now time.Time) *adminv1.WarpSummary {
	m := &adminv1.WarpSummary{State: ViewState(a, online, now)}
	if a == nil {
		return m
	}
	m.Source, m.AccountType = sourceProto[a.Source], a.AccountType
	if h := health(*a); h != nil && m.State != adminv1.WarpState_WARP_STATE_UNKNOWN {
		m.Colo = h.Colo
	}
	return m
}

func healthMsg(a store.WarpAccountRow) *adminv1.WarpHealthView {
	h := health(a)
	if h == nil {
		return nil
	}
	st := agentStateProto[h.State]
	return &adminv1.WarpHealthView{State: st, Backend: h.Backend, Endpoint: h.Endpoint, LastHandshakeUnix: h.LastHandshakeUnix,
		WarpFlag: h.WarpFlag, Colo: h.Colo, ProbeCloudflareOk: h.ProbeCloudflareOk, ProbeOtherOk: h.ProbeOtherOk,
		ConsecutiveFailures: h.ConsecutiveFailures, RxBytes: h.RxBytes, TxBytes: h.TxBytes, LastError: h.LastError,
		ReportedUnix: a.HealthAt.Unix(), ProbeCloudflare: probeMsg(h.ProbeCloudflare), ProbeOther: probeMsg(h.ProbeOther),
		CheckedUnix: h.CheckedUnix}
}

// probeMsg passes a probe result through; nil stays nil (the round did not run it, or the agent predates the field).
func probeMsg(p *agentv1.WarpProbeResult) *adminv1.WarpProbeResult {
	if p == nil {
		return nil
	}
	return &adminv1.WarpProbeResult{Ok: p.Ok, LatencyMs: p.LatencyMs, AtUnix: p.AtUnix, FailureCode: p.FailureCode}
}

// accountMsg is the account without its secrets.
func (s *Service) accountMsg(a store.WarpAccountRow) *adminv1.WarpAccount {
	m := &adminv1.WarpAccount{NodeId: a.NodeID, Source: sourceProto[a.Source], AccountType: a.AccountType, Enabled: a.Enabled,
		PeerPublicKey: a.PeerPublicKey, EndpointV4: a.EndpointV4, EndpointV6: a.EndpointV6, AddressV4: a.AddressV4,
		AddressV6: a.AddressV6, Mtu: uint32(a.MTU), UseReserved: a.UseReserved, TosUrl: a.TOSURL, TosAcceptedBy: a.TOSAcceptedBy,
		RegisteredWith: a.RegisteredWith, CreatedUnix: a.CreatedAt.Unix(), UpdatedUnix: a.UpdatedAt.Unix()}
	for _, p := range a.Ports {
		m.Ports = append(m.Ports, uint32(p))
	}
	if !a.TOSAcceptedAt.IsZero() {
		m.TosAcceptedUnix = a.TOSAcceptedAt.Unix()
	}
	if x, err := s.open(a); err == nil {
		m.HasToken = x.hasToken()
	} else {
		s.log.Error("warp: open secrets (wrong master key?)", "node", a.NodeID)
	}
	return m
}

func paramsMsg(p Params) *adminv1.WarpRegistrationParams {
	p = p.fill()
	return &adminv1.WarpRegistrationParams{ApiVersion: p.APIVersion, UserAgent: p.UserAgent, CfClientVersion: p.CFClientVersion,
		TlsSpecId: p.TLSSpecID, AutoReregister: p.AutoReregister}
}
