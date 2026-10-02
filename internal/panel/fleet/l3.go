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
	s := f.session(nodeID)
	if s == nil {
		return false
	}
	s.desMu.Lock()
	defer s.desMu.Unlock()
	if s.sent == nil || s.sent.warp == nil || s.sent.warp.Enabled {
		return false
	}
	n, err := f.st.Node(ctx, nodeID)
	return err == nil && n.AppliedHash == s.sent.hash
}

// Intervals of the persisted health: a changed report is written at most this often, an unchanged one this often (so that
// "reported at" stays fresh and a silent node shows as stale).
const (
	healthMinGap     = 30 * time.Second
	healthRefreshGap = 2 * time.Minute
	// warpRefreshGap bounds how often a node can make the panel call Cloudflare (a ladder asks once per 10 minutes at most).
	warpRefreshGap = 10 * time.Minute
)

// l3Intake is the per-stream memory of what was last persisted, so a stats batch every 10 s does not become a database
// write every 10 s. It is only touched by the stream's own goroutine.
type l3Intake struct {
	awg     map[string]*healthMemo[*agentv1.AwgHealth]
	warp    healthMemo[*agentv1.WarpHealth]
	warpUp  bool
	warpAsk time.Time
}

type healthMemo[T proto.Message] struct {
	last T
	at   time.Time
	have bool
}

// due says whether v must be written now, and remembers it when it is. urgent (may be nil) names a change that must not
// wait for the gap: the rest of a report (byte counters, handshake age) changes with every batch and may be written late,
// a state may not, or the badge keeps saying what the node stopped saying.
func (m *healthMemo[T]) due(v T, now time.Time, urgent func(prev, next T) bool) bool {
	if m.have && now.Sub(m.at) < healthMinGap && (urgent == nil || !urgent(m.last, v)) {
		return false
	}
	if m.have && proto.Equal(m.last, v) && now.Sub(m.at) < healthRefreshGap {
		return false
	}
	m.last, m.at, m.have = proto.Clone(v).(T), now, true
	return true
}

// warpStateChanged: the WARP tunnel went from one state to another (starting -> up, up -> down), or the latest check
// flipped between passing and failing (a probe went red or green, the reason changed). The first check that passes already
// shows a fresh handshake and green probes while the state still says "starting" (the node wants two), so a report written
// 30 s late would keep that picture for half a minute after the node was up; the same goes for a check that starts failing
// while the state stays "starting" (a slow WARP edge: the card must not keep showing green dots).
func warpStateChanged(prev, next *agentv1.WarpHealth) bool {
	return prev.GetState() != next.GetState() ||
		prev.GetLastError() != next.GetLastError() ||
		prev.GetProbeCloudflareOk() != next.GetProbeCloudflareOk() ||
		prev.GetProbeOtherOk() != next.GetProbeOtherOk() ||
		prev.GetProbeCloudflare().GetFailureCode() != next.GetProbeCloudflare().GetFailureCode() ||
		prev.GetProbeOther().GetFailureCode() != next.GetProbeOther().GetFailureCode()
}

// touchAwgDevices writes the time of the newest handshake of every AWG peer that has a session into the device's
// last_seen_at: the admin and the user pages show it as the last handshake and draw the online dot from it. The
// session's connected_at is the peer's last handshake; the update only ever moves the time
// forward, so a batch resent after a reconnect changes nothing. A failure is logged and never fails the batch.
func (f *Fleet) touchAwgDevices(ctx context.Context, st *agentv1.StatsBatch, refs map[string]store.FleetCredRef, now time.Time) {
	for i, se := range st.Sessions {
		if i == maxStatsDeltas {
			break
		}
		ref, ok := refs[se.CredId]
		if !ok || ref.Protocol != "awg" || ref.DeviceID == "" || se.ConnectedAtUnix <= 0 {
			continue
		}
		t := time.Unix(se.ConnectedAtUnix, 0).UTC()
		if t.After(now) {
			t = now
		}
		if err := f.st.Access().TouchDevice(ctx, ref.DeviceID, t, t); err != nil {
			f.log.Warn("touch awg device", "device", ref.DeviceID, "err", err)
		}
	}
}

// l3Stats stores the AWG health of the inbounds and the WARP health of the node from a stats batch (the live view of the
// stream is replaced by applySnapshot). A failure to write is logged and never fails the batch: health is a snapshot, the
// next batch replaces it.
func (f *Fleet) l3Stats(ctx context.Context, s *session, st *agentv1.StatsBatch, now time.Time) {
	for _, h := range st.Health {
		if h.Awg == nil {
			continue
		}
		if s.l3.awg == nil {
			s.l3.awg = map[string]*healthMemo[*agentv1.AwgHealth]{}
		}
		m := s.l3.awg[h.InboundId]
		if m == nil {
			m = &healthMemo[*agentv1.AwgHealth]{}
			s.l3.awg[h.InboundId] = m
		}
		if !m.due(h.Awg, now, nil) {
			continue
		}
		b, err := protojson.Marshal(h.Awg)
		if err != nil {
			continue
		}
		if err := f.st.SetInboundAwgHealth(ctx, s.nodeID, h.InboundId, string(b), now); err != nil {
			f.log.Warn("store awg health", "node", s.nodeID, "inbound", h.InboundId, "err", err)
		}
	}
	w := f.warpModule()
	if st.Warp == nil || w == nil {
		return
	}
	if s.l3.warp.due(st.Warp, now, warpStateChanged) {
		if err := w.StoreHealth(ctx, s.nodeID, st.Warp); err != nil {
			f.log.Warn("store warp health", "node", s.nodeID, "err", err)
		}
	}
	up := st.Warp.State == agentv1.WarpState_WARP_STATE_UP
	if up && !s.l3.warpUp {
		// The tunnel is working again: whatever the node asked the owner to look at is over (the owner sees the badge
		// again, not a stale "needs attention").
		if a, err := f.st.WarpAccount(ctx, s.nodeID); err == nil && a.Attention != "" {
			if err := w.NeedsAttention(ctx, s.nodeID, ""); err != nil {
				f.log.Warn("clear warp attention", "node", s.nodeID, "err", err)
			}
		}
	}
	s.l3.warpUp = up
}

// warp event reasons the agent sends (internal/node/warp, internal/node/agent): codes of warp_needs_attention.
const (
	warpReasonRefresh  = "refresh_requested" // the ladder ran out of endpoints: read the account again
	warpReasonLadder   = "down_after_ladder"
	eventWarpAttention = "warp_needs_attention"
)

// onWarpEvent reacts to a warp_needs_attention event of the node, after it was stored like every event. "refresh_requested"
// is a request to the panel, not a problem for the owner: the account is read back from Cloudflare (read only, at most once
// per warpRefreshGap per node). Anything else is something the owner decides and is recorded on the account; a ladder that
// ran out may also trigger the automatic re-registration, if the owner switched it on (the module decides). Cloudflare is
// never called on the stream's goroutine.
func (f *Fleet) onWarpEvent(s *session, ev *agentv1.Event) {
	w := f.warpModule()
	if w == nil || ev.Code != eventWarpAttention {
		return
	}
	reason := store.Clip(ev.Params["reason"], 64)
	now := f.now()
	if reason == warpReasonRefresh {
		if now.Sub(s.l3.warpAsk) < warpRefreshGap {
			return
		}
		s.l3.warpAsk = now
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		switch reason {
		case warpReasonRefresh:
			if err := w.RefreshByNode(ctx, s.nodeID); err != nil {
				f.log.Info("warp refresh asked by the node did not work", "node", s.nodeID, "err", err)
			}
		default:
			if err := w.NeedsAttention(ctx, s.nodeID, reason); err != nil {
				f.log.Warn("record warp attention", "node", s.nodeID, "err", err)
				return
			}
			if reason == warpReasonLadder {
				if _, err := w.AutoReregister(ctx, s.nodeID); err != nil {
					f.log.Info("automatic warp re-registration", "node", s.nodeID, "err", err)
				}
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
	caps := n.AgentCaps
	if _, live, _ := f.Live(n.ID); live != nil {
		caps = live
	}
	if !slices.Contains(caps, capAWG) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("agent too old"))
	}
	return nil
}
