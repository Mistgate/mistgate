package fleet

import (
	"context"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The L3 side of the fleet (agent.proto "AWG AND WARP"): the capability gates, the tunnel and
// the WarpSpec in the desired state (desired.go), and here the intake of AwgHealth / WarpHealth / WARP events, the seam to
// the WARP module and the node settings that the agent follows.

// Warp is implemented by the WARP module (internal/panel/warp). The fleet asks it for the node's WarpSpec and hands it what
// the node reports; it never touches an account itself. Every method must be cheap or return quickly: Spec runs for every
// reconcile of a node, StoreHealth for every stats batch.
type Warp interface {
	// Spec is the node's WarpSpec (nil = the node has no account; Enabled=false = paused). It is sent only to a stream
	// that listed "warp/1".
	Spec(ctx context.Context, nodeID string) (*plugin.WarpSpec, error)
	// StoreHealth keeps the last WarpHealth of a node (for GetWarp and the node badge).
	StoreHealth(ctx context.Context, nodeID string, h *agentv1.WarpHealth) error
	// NeedsAttention records why the owner has to decide ("" clears it).
	NeedsAttention(ctx context.Context, nodeID, reason string) error
	// RefreshByNode reads the account back from Cloudflare because the node's WARP could not heal itself (read only).
	RefreshByNode(ctx context.Context, nodeID string) error
	// AutoReregister replaces a revoked registered account when the owner switched that on; reports whether it did.
	AutoReregister(ctx context.Context, nodeID string) (bool, error)
	// Summary is the badge of a node card; a is nil when the node has no account.
	Summary(a *store.WarpAccountRow, online bool, now time.Time) *adminv1.WarpSummary
}

// SetWarp connects the WARP module. Call it before serving; nil disconnects (no WarpSpec is sent, no health is stored).
func (f *Fleet) SetWarp(w Warp) {
	f.mu.Lock()
	f.warp = w
	f.mu.Unlock()
}

func (f *Fleet) warpModule() Warp {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.warp
}

// WarpPauseApplied reports whether the node holds a paused WARP: the last state sent on its stream pauses it, and the node
// confirmed exactly that state. It is RestartWarp's proof that the tunnel went down; another change applied meanwhile (a
// state that still runs WARP) does not count.
func (f *Fleet) WarpPauseApplied(ctx context.Context, nodeID string) bool {
	n, rawDigest, err := f.st.NodeWithSentDigest(ctx, nodeID)
	if err != nil || len(rawDigest) == 0 {
		return false
	}
	digest, err := decodeSentDigest(rawDigest)
	if err != nil {
		return false
	}
	return digest.WarpOff && n.AppliedHash == digest.Hash
}

// Intervals of the persisted health: a changed report is written at most this often, an unchanged one this often (so that
// "reported at" stays fresh and a silent node shows as stale).
const (
	healthMinGap     = 30 * time.Second
	healthRefreshGap = 2 * time.Minute
	// warpRefreshGap bounds how often a node can make the panel call Cloudflare (a ladder asks once per 10 minutes at most).
	warpRefreshGap = 10 * time.Minute
)

// l3MemoState keeps only hashes and timestamps, so serialized session size grows with the number of AWG inbounds.
type l3MemoState struct {
	AWG           map[string]l3HealthMemo `json:"a,omitempty"`
	Warp          l3HealthMemo            `json:"w,omitzero"`
	WarpChangeKey string                  `json:"wk,omitempty"`
	WarpUp        bool                    `json:"u,omitempty"`
	WarpAsk       time.Time               `json:"q,omitzero"`
}

type l3HealthMemo struct {
	Hash string    `json:"h,omitempty"`
	At   time.Time `json:"t,omitzero"`
}

// due says whether a changed report or its refresh is due, and remembers it when it is.
func (m *l3HealthMemo) due(hash string, now time.Time, urgent bool) bool {
	have := !m.At.IsZero()
	if have && now.Sub(m.At) < healthMinGap && !urgent {
		return false
	}
	if have && hash == m.Hash && now.Sub(m.At) < healthRefreshGap {
		return false
	}
	m.Hash, m.At = hash, now
	return true
}

func l3ReportHash(v proto.Message) string {
	b, err := (proto.MarshalOptions{Deterministic: true}).Marshal(v)
	if err != nil {
		return ""
	}
	return shortHash(b)
}

// warpChangeKey covers exactly the fields that make a WARP health change urgent: the WARP tunnel went from one state to
// another (starting -> up, up -> down), or the latest check flipped between passing and failing (a probe went red or green,
// the reason changed). The first check that passes already shows a fresh handshake and green probes while the state still
// says "starting" (the node wants two), so a report written 30 s late would keep that picture for half a minute after the
// node was up; the same goes for a check that starts failing while the state stays "starting" (a slow WARP edge: the card
// must not keep showing green dots).
func warpChangeKey(v *agentv1.WarpHealth) string {
	if v == nil {
		v = &agentv1.WarpHealth{}
	}
	key := &agentv1.WarpHealth{
		State: v.State, LastError: v.LastError, ProbeCloudflareOk: v.ProbeCloudflareOk, ProbeOtherOk: v.ProbeOtherOk,
	}
	if failure := v.GetProbeCloudflare().GetFailureCode(); failure != "" {
		key.ProbeCloudflare = &agentv1.WarpProbeResult{FailureCode: failure}
	}
	if failure := v.GetProbeOther().GetFailureCode(); failure != "" {
		key.ProbeOther = &agentv1.WarpProbeResult{FailureCode: failure}
	}
	return l3ReportHash(key)
}

// warp event reasons the agent sends (internal/node/warp, internal/node/agent): codes of warp_needs_attention.
const (
	warpReasonRefresh  = "refresh_requested" // the ladder ran out of endpoints: read the account again
	warpReasonLadder   = "down_after_ladder"
	eventWarpAttention = "warp_needs_attention"
)

func (f *Fleet) dispatchWarpAttention(w Warp, nodeID, reason string) {
	if w == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		switch reason {
		case warpReasonRefresh:
			if err := w.RefreshByNode(ctx, nodeID); err != nil {
				f.log.Info("warp refresh asked by the node did not work", "node", nodeID, "err", err)
			}
		case warpReasonLadder:
			if _, err := w.AutoReregister(ctx, nodeID); err != nil {
				f.log.Info("automatic warp re-registration", "node", nodeID, "err", err)
			}
		}
	}()
}

// withheldReason is what the node page shows on an inbound the panel did not send to this agent.
const withheldReason = "agent_too_old: this node's agent does not list awg/1, update the agent (mistgate-node install, or the self-update)"

// withheldApplied are the inbound results the panel writes for inbounds it did not send to this agent.
func withheldApplied(ids []string) []store.InboundApplied {
	out := make([]store.InboundApplied, 0, len(ids))
	for _, id := range ids {
		out = append(out, store.InboundApplied{ID: id, State: "failed", Error: withheldReason})
	}
	return out
}

// awgStatusMsg is Inbound.awg from the stored report; nil before the first one.
func awgStatusMsg(row store.FleetInboundRow) *adminv1.AwgInboundStatus {
	if row.AwgHealthJSON == "" {
		return nil
	}
	var h agentv1.AwgHealth
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal([]byte(row.AwgHealthJSON), &h); err != nil {
		return nil
	}
	return &adminv1.AwgInboundStatus{
		Backend: clip(h.Backend, 32), BackendVersion: clip(h.BackendVersion, 96), IfaceUp: h.IfaceUp, Peers: h.Peers,
		PeersHandshaken: h.PeersHandshaken, PeersOnline: h.PeersOnline, NewestHandshakeUnix: h.NewestHandshakeUnix,
		UdpRxPackets: h.UdpRxPackets, UnknownPeerEvents: h.UnknownPeerEvents, ReportedUnix: fleetUnix(row.AwgHealthAt),
	}
}

// warpSummary is Node.warp: the module's badge, or "not configured"/"unknown" without a module.
func (f *Fleet) warpSummary(ctx context.Context, nodeID string, online bool, now time.Time) *adminv1.WarpSummary {
	a, err := f.st.WarpAccount(ctx, nodeID)
	var row *store.WarpAccountRow
	switch {
	case err == nil:
		row = &a
	case !errors.Is(err, store.ErrNotFound):
		f.log.Warn("warp account", "node", nodeID, "err", err)
		return nil
	}
	if w := f.warpModule(); w != nil {
		return w.Summary(row, online, now)
	}
	if row == nil {
		return &adminv1.WarpSummary{State: adminv1.WarpState_WARP_STATE_NOT_CONFIGURED}
	}
	return &adminv1.WarpSummary{State: adminv1.WarpState_WARP_STATE_UNKNOWN}
}

// awgBackends are the values of NodeSettings.awg_backend.
var awgBackends = []string{"auto", "kernel", "userspace"}

// checkAwgBackend validates the AWG backend of a node for UpdateNode: a node with awg inbounds needs an agent that lists
// "awg/1" (its last Hello or the live stream), else the setting would silently do nothing.
func (f *Fleet) checkAwgBackend(ctx context.Context, n store.NodeRow, value string) error {
	if !slices.Contains(awgBackends, value) {
		return invalid("awg_backend must be auto, kernel or userspace")
	}
	rows, err := f.st.FleetInbounds(ctx, n.ID, false)
	if err != nil {
		return internalErr(f.log.Error, "node inbounds", err)
	}
	has := false
	for _, r := range rows {
		has = has || r.Protocol == protocolAWG
	}
	if !has {
		return nil
	}
	if !slices.Contains(n.AgentCaps, capAWG) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("agent too old"))
	}
	return nil
}
