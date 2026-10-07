package fleet

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/encoding/protojson"
)

const sessionStateVersion = 1

const prepareRetryDelay = 3 * time.Second

// maxCertLife bounds the expiry an agent may report: the self-signed certificates the panel makes live 10 years.
const maxCertLife = 11 * 365 * 24 * time.Hour

// SessionState is the compact connection state. Large snapshots and memo tables live in SessionSidecar.
type SessionState struct {
	Version         uint8     `json:"v"`
	NodeID          string    `json:"n,omitempty"`
	OwnerGeneration uint64    `json:"o,omitempty"`
	InstanceID      string    `json:"i,omitempty"`
	Capabilities    []string  `json:"c,omitempty"`
	LivenessNanos   int64     `json:"l,omitempty"`
	HelloDeadline   time.Time `json:"h,omitzero"`
	// LivenessDeadline keeps Go's monotonic reading on the VPS: a wall-clock step cannot close a live session.
	LivenessDeadline      time.Time    `json:"d,omitzero"`
	AutoBandwidthDeadline time.Time    `json:"b,omitzero"`
	AutoBandwidthPending  bool         `json:"ap,omitempty"`
	NextAckTick           time.Time    `json:"a,omitzero"`
	NextCertCheck         time.Time    `json:"x,omitzero"`
	AckPending            uint64       `json:"p,omitempty"`
	AckSent               uint64       `json:"s,omitempty"`
	LastAck               time.Time    `json:"k,omitzero"`
	LastReject            time.Time    `json:"r,omitzero"`
	LastEndUnix           int64        `json:"e,omitempty"`
	LastSeenAt            time.Time    `json:"v_at,omitzero"`
	SentRevision          uint64       `json:"q,omitempty"`
	SentStateHash         string       `json:"sh,omitempty"`
	SentSettingsHash      string       `json:"ss,omitempty"`
	DriftResent           bool         `json:"dr,omitempty"`
	Drift                 bool         `json:"dt,omitempty"`
	FullResendPending     bool         `json:"fr,omitempty"`
	HelloAppliedRevision  uint64       `json:"har,omitempty"`
	HelloAppliedStateHash string       `json:"hah,omitempty"`
	Preparing             bool         `json:"prep,omitempty"`
	PrepareDirty          bool         `json:"pd,omitempty"`
	PrepareRetryAt        time.Time    `json:"pra,omitzero"`
	Poison                *PoisonBatch `json:"p_seq,omitempty"`
	PeerCertSerial        string       `json:"cs,omitempty"`
	PeerCertNotAfter      time.Time    `json:"ce,omitzero"`
	Disconnected          bool         `json:"z,omitempty"`
	SidecarVersion        uint32       `json:"sv,omitempty"`
}

// PoisonBatch identifies the last stats batch refused by the store.
type PoisonBatch struct {
	Instance string `json:"i"`
	Seq      uint64 `json:"s"`
}

// LiveSnapshot is the current admin view of one agent, refreshed only by accepted StatsBatch messages.
type LiveSnapshot struct {
	Metrics   *agentv1.HostMetrics
	MetricsAt time.Time
	Health    []*agentv1.InboundHealth
	Online    []onlineSess
	UserDown  map[string]uint64
	UserUp    map[string]uint64
}

// PendingRequest is durable request metadata. VPS channels remain in the adapter; edge delivery is a later round.
type PendingRequest struct {
	Kind     PendingRequestKind
	Deadline time.Time
}

// PendingRequestKind identifies the adapter-owned request awaiting an agent result.
type PendingRequestKind uint8

const (
	PendingCommand PendingRequestKind = iota + 1
	PendingDoctor
	PendingLog
)

// SessionSidecar holds desired/live state that is too large for SessionState.
type SessionSidecar struct {
	Version     uint32
	SentDesired *nodeState
	Live        LiveSnapshot
	CertSeen    map[string]string
	L3          l3Intake
	Pending     map[string]PendingRequest
}

// EventKind identifies one input that the session core can process.
type EventKind uint8

const (
	EventOpen EventKind = iota + 1
	EventHello
	EventAgentFrame
	EventDesiredChanged
	EventDesiredPrepared
	EventAdminCommand
	EventLogStart
	EventLogCancel
	EventAlarm
	EventDisconnected
	EventOwnerSuperseded
)

// AdminRequest carries a request frame and its correlation metadata into the core.
type AdminRequest struct {
	RequestID string
	Deadline  time.Time
	Frame     *agentv1.ConnectResponse
	Kind      PendingRequestKind
}

// SessionEvent is a timestamped input to SessionCore.Step.
type SessionEvent struct {
	Kind     EventKind
	At       time.Time
	Frame    *agentv1.ConnectRequest
	Request  *AdminRequest
	Prepared *preparedDesiredState
	Err      error
}

// EffectKind identifies an adapter action requested by the session core.
type EffectKind uint8

const (
	EffectAutoBandwidth EffectKind = iota + 1
	EffectUsage
	EffectCommandResult
	EffectDoctorReport
	EffectLogChunk
	EffectWarpAttention
	EffectPrepareDesired
	EffectConnectEvents
)

// SessionEffect asks the adapter to perform work requested by a core transition.
type SessionEffect struct {
	Kind          EffectKind
	RequestID     string
	PreviousNode  *store.NodeRow
	BootAt        time.Time
	At            time.Time
	CommandResult *agentv1.CommandResult
	DoctorReport  *agentv1.DoctorReport
	LogChunk      *agentv1.LogChunk
	Users         []string
	WarpReason    string
}

// CloseClass identifies why a session should terminate.
type CloseClass uint8

const (
	CloseInvalidArgument CloseClass = iota + 1
	CloseFailedPrecondition
	CloseUnauthenticated
	CloseConflict
	CloseDeadline
	CloseInternal
	CloseCanceled
)

// SessionClose describes a protocol-level reason to end the connection.
type SessionClose struct {
	Class  CloseClass
	Reason string
}

// Transition contains the outputs produced by one session event.
type Transition struct {
	Frames    []*agentv1.ConnectResponse
	Effects   []SessionEffect
	NextAlarm *time.Time
	Close     *SessionClose
}

type preparedDesiredState struct {
	node    store.NodeRow
	desired *nodeState
}

type coreTransition struct {
	Transition
	state   *SessionState
	sidecar *SessionSidecar
}

// SessionCore processes one event at a time without owning goroutines, timers, channels, or transports.
type SessionCore struct{ f *Fleet }

// NewSessionCore creates a session transition engine backed by fleet services.
func NewSessionCore(f *Fleet) *SessionCore { return &SessionCore{f: f} }

// Step processes one event and writes the resulting state and sidecar back to the supplied session.
// On error the caller must end the session; state may already reflect work completed before the error.
func (c *SessionCore) Step(ctx context.Context, state *SessionState, sidecar *SessionSidecar, event SessionEvent) (Transition, error) {
	if c == nil || c.f == nil {
		return Transition{}, errors.New("session core has no fleet")
	}
	if state == nil || sidecar == nil {
		return Transition{}, errors.New("session state and sidecar are required")
	}
	if event.At.IsZero() {
		return Transition{}, errors.New("session event time is required")
	}
	if state.Version == 0 {
		state.Version = sessionStateVersion
	}
	if sidecar.Version == 0 {
		sidecar.Version = 1
	}
	if sidecar.Live.UserDown == nil {
		sidecar.Live.UserDown = map[string]uint64{}
	}
	if sidecar.Live.UserUp == nil {
		sidecar.Live.UserUp = map[string]uint64{}
	}
	if sidecar.CertSeen == nil {
		sidecar.CertSeen = map[string]string{}
	}
	if sidecar.Pending == nil {
		sidecar.Pending = map[string]PendingRequest{}
	}
	tr := coreTransition{state: state, sidecar: sidecar}
	if tr.state.Disconnected {
		return tr.Transition, nil
	}
	switch event.Kind {
	case EventOpen:
		if tr.state.HelloDeadline.IsZero() {
			tr.state.HelloDeadline = event.At.Add(helloTimeout)
		}
	case EventHello:
		if err := c.hello(ctx, &tr, event); err != nil {
			return tr.Transition, err
		}
	case EventAgentFrame:
		c.agentFrame(ctx, &tr, event)
	case EventDesiredChanged:
		c.requestDesiredPreparation(&tr)
	case EventDesiredPrepared:
		if err := c.desiredPrepared(ctx, &tr, event); err != nil {
			return tr.Transition, err
		}
	case EventAdminCommand:
		c.request(&tr, event.Request, event.At, PendingCommand)
	case EventLogStart:
		c.request(&tr, event.Request, event.At, PendingLog)
	case EventLogCancel:
		id := ""
		if event.Request != nil {
			id = event.Request.RequestID
		}
		delete(tr.sidecar.Pending, id)
		tr.Frames = append(tr.Frames, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_LogCancel{LogCancel: &agentv1.LogCancel{RequestId: id}}})
	case EventAlarm:
		if err := c.alarm(ctx, &tr, event); err != nil {
			return tr.Transition, err
		}
	case EventDisconnected:
		tr.state.Disconnected = true
		tr.sidecar.Pending = map[string]PendingRequest{}
		tr.state.AutoBandwidthDeadline = time.Time{}
		tr.state.AutoBandwidthPending = false
	case EventOwnerSuperseded:
		tr.Close = &SessionClose{Class: CloseConflict, Reason: "superseded by a newer stream"}
	default:
		return tr.Transition, fmt.Errorf("unknown session event kind %d", event.Kind)
	}
	tr.NextAlarm = nextSessionAlarm(*tr.state, *tr.sidecar, event.At)
	return tr.Transition, nil
}

func (c *SessionCore) hello(ctx context.Context, tr *coreTransition, event SessionEvent) error {
	h := event.Frame.GetHello()
	if h == nil {
		tr.Close = &SessionClose{Class: CloseInvalidArgument, Reason: "the first message must be Hello"}
		return nil
	}
	if h.ApiVersion != APIVersion {
		tr.Close = &SessionClose{Class: CloseFailedPrecondition, Reason: fmt.Sprintf("unsupported api_version %d, this panel serves %d..%d", h.ApiVersion, APIVersion, APIVersion)}
		return nil
	}
	if h.InstanceId == "" || len(h.InstanceId) > 64 {
		tr.Close = &SessionClose{Class: CloseInvalidArgument, Reason: "instance_id is required (random per agent process, at most 64 bytes)"}
		return nil
	}
	if h.NodeId != "" && h.NodeId != tr.state.NodeID {
		c.f.log.Warn("hello names another node than the certificate", "cert_node", tr.state.NodeID)
	}
	info := helloInfo(h)
	prev, acked, err := c.f.st.NodeHello(ctx, tr.state.NodeID, info, event.At)
	if errors.Is(err, store.ErrNodeRetired) {
		tr.Close = &SessionClose{Class: CloseFailedPrecondition, Reason: "node retired"}
		return nil
	}
	if err != nil {
		c.f.log.Error("hello", "node", tr.state.NodeID, "err", err)
		tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
		return nil
	}
	node, err := c.f.st.Node(ctx, tr.state.NodeID)
	if err != nil {
		tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
		return nil
	}
	tr.state.Version = sessionStateVersion
	tr.state.InstanceID = h.InstanceId
	tr.state.Capabilities = capabilities(h)
	tr.state.HelloDeadline = time.Time{}
	tr.state.LivenessNanos = int64(time.Duration(node.LivenessTimeoutS) * c.f.unit)
	tr.state.LastSeenAt = event.At
	tr.state.LivenessDeadline = event.At.Add(time.Duration(tr.state.LivenessNanos))
	tr.state.NextAckTick = event.At.Add(ackEvery)
	tr.state.NextCertCheck = event.At.Add(c.f.certCheck)
	tr.state.SidecarVersion = tr.sidecar.Version
	tr.state.HelloAppliedRevision = h.AppliedRevision
	tr.state.HelloAppliedStateHash = h.AppliedStateHash
	tr.sidecar.Live.UserDown = map[string]uint64{}
	tr.sidecar.Live.UserUp = map[string]uint64{}
	tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectConnectEvents, PreviousNode: &prev, BootAt: info.BootAt, At: event.At})
	autoMeasure := prev.State == "pending" && node.BandwidthMbps == 0
	tr.state.AutoBandwidthPending = autoMeasure && slices.Contains(tr.state.Capabilities, capBandwidth)
	settings := nodeSettings(node, tr.state.Capabilities)
	tr.state.SentSettingsHash = settingsSig(settings)
	if tr.state.AutoBandwidthPending {
		tr.state.AutoBandwidthDeadline = event.At.Add(c.f.measureDelay)
	}
	tr.Frames = append(tr.Frames, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_HelloAck{HelloAck: &agentv1.HelloAck{
		AckedSeq: acked, ServerTimeUnix: event.At.Unix(), Settings: settings, LinkSupported: c.f.cfg.LinkServed,
	}}})
	return nil
}

func (c *SessionCore) request(tr *coreTransition, request *AdminRequest, at time.Time, fallback PendingRequestKind) {
	if request == nil || request.RequestID == "" || request.Frame == nil {
		return
	}
	kind := request.Kind
	if kind == 0 {
		kind = fallback
	}
	deadline := request.Deadline
	if deadline.IsZero() {
		deadline = at
	}
	tr.sidecar.Pending[request.RequestID] = PendingRequest{Kind: kind, Deadline: deadline}
	tr.Frames = append(tr.Frames, request.Frame)
}

func (c *SessionCore) agentFrame(ctx context.Context, tr *coreTransition, event SessionEvent) {
	m := event.Frame
	if m == nil {
		return
	}
	tr.state.LastSeenAt = event.At
	if tr.state.LivenessNanos > 0 {
		tr.state.LivenessDeadline = event.At.Add(time.Duration(tr.state.LivenessNanos))
	}
	switch {
	case m.GetHello() != nil:
		tr.Close = &SessionClose{Class: CloseInvalidArgument, Reason: "unexpected Hello"}
	case m.GetStats() != nil:
		c.stats(ctx, tr, m.Seq, m.GetStats(), event.At)
	case m.GetEvent() != nil:
		c.agentEvent(ctx, tr, m.Seq, m.GetEvent(), event.At)
	case m.GetApplyResult() != nil:
		if err := c.applyResult(ctx, tr, m.GetApplyResult(), event.At); err != nil {
			c.f.log.Warn("apply result", "node", tr.state.NodeID, "err", err)
		}
	case m.GetCommandResult() != nil:
		r := m.GetCommandResult()
		pending, ok := tr.sidecar.Pending[r.RequestId]
		if !ok || pending.Kind != PendingCommand {
			return
		}
		delete(tr.sidecar.Pending, r.RequestId)
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectCommandResult, RequestID: r.RequestId, CommandResult: r})
	case m.GetLogChunk() != nil:
		chunk := m.GetLogChunk()
		pending, ok := tr.sidecar.Pending[chunk.RequestId]
		if !ok || pending.Kind != PendingLog {
			return
		}
		if chunk.Eof {
			delete(tr.sidecar.Pending, chunk.RequestId)
		}
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectLogChunk, RequestID: chunk.RequestId, LogChunk: chunk})
	case m.GetDoctorReport() != nil:
		r := m.GetDoctorReport()
		c.f.recordDoctorReport(ctx, tr.state.NodeID, r)
		if r.RequestId != "" {
			pending, ok := tr.sidecar.Pending[r.RequestId]
			if !ok || pending.Kind != PendingDoctor {
				return
			}
			delete(tr.sidecar.Pending, r.RequestId)
		}
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectDoctorReport, RequestID: r.RequestId, DoctorReport: r})
	}
}

func (c *SessionCore) stats(ctx context.Context, tr *coreTransition, seq uint64, st *agentv1.StatsBatch, now time.Time) {
	hour, stale := bucketHour(time.Unix(st.IntervalEndUnix, 0), now)
	g := c.f.guardStats(st)
	if g.rejected > 0 {
		c.rejectStats(ctx, tr.state, g, now)
	}
	in := store.FleetStatsIn{NodeID: tr.state.NodeID, Instance: tr.state.InstanceID, Seq: seq, Now: now, HourStart: hour, Traffic: g.traffic}
	for i, se := range st.Sessions {
		if i == maxStatsDeltas {
			break
		}
		in.Sessions = append(in.Sessions, store.FleetSessionRef{CredID: se.CredId, InboundID: se.InboundId})
	}
	out, err := c.f.st.IngestStats(ctx, in)
	if err != nil {
		// Do not ack and do not go on: a later ack would cover this seq and lose the batch. The agent
		// reconnects and resends from HelloAck.acked_seq. But a batch the database refuses twice will be
		// refused every time, and resending it would wedge this node's stats for good: drop it then.
		c.f.log.Error("ingest stats", "node", tr.state.NodeID, "err", err)
		if tr.state.Poison == nil || tr.state.Poison.Instance != tr.state.InstanceID || tr.state.Poison.Seq != seq {
			tr.state.Poison = &PoisonBatch{Instance: tr.state.InstanceID, Seq: seq}
			tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
			return
		}
		if seq != 0 {
			if err := c.f.st.SkipSeq(ctx, tr.state.NodeID, tr.state.InstanceID, seq, now); err != nil {
				tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
				return
			}
			markCoreAck(tr, seq, now)
		}
		tr.state.Poison = nil
		c.f.log.Error("stats batch refused by the database twice, dropped", "node", tr.state.NodeID, "seq", seq)
		c.f.event(ctx, 3, "stats_dropped", tr.state.NodeID, map[string]string{"reason": "database_refused", "seq": fmt.Sprint(seq)})
		return
	}
	tr.state.Poison = nil
	if !out.Duplicate {
		applyCoreSnapshot(tr.state, &tr.sidecar.Live, st, g.traffic, now, out.Refs)
		c.l3Stats(ctx, tr.state.NodeID, &tr.sidecar.L3, st, now)
		c.certStats(ctx, tr.state.NodeID, tr.sidecar.CertSeen, st, now)
		c.f.touchAwgDevices(ctx, st, out.Refs, now)
		if out.Skipped > 0 {
			c.f.log.Warn("stats for unknown credentials or foreign inbounds dropped", "node", tr.state.NodeID, "count", out.Skipped)
		}
		if stale {
			c.f.event(ctx, 2, "stats_stale", tr.state.NodeID, map[string]string{"age_hours": fmt.Sprint(int(now.Sub(time.Unix(st.IntervalEndUnix, 0)).Hours()))})
		}
		if c.f.cfg.OnUsage != nil && len(out.Users) > 0 {
			tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectUsage, Users: slices.Clone(out.Users)})
		}
	}
	if seq != 0 {
		markCoreAck(tr, seq, now)
	}
}

// applyCoreSnapshot replaces the live view with the newest absolute snapshot.
func applyCoreSnapshot(state *SessionState, live *LiveSnapshot, st *agentv1.StatsBatch, traffic []store.FleetTraffic, now time.Time, refs map[string]store.FleetCredRef) bool {
	end := st.IntervalEndUnix
	// An end time from the future would make every later (honest) batch look older and freeze the live
	// view: clamp it like the bucket hour.
	if end > now.Add(maxFuture).Unix() {
		end = now.Unix()
	}
	if end < state.LastEndUnix {
		return false // an older batch resent after a reconnect
	}
	state.LastEndUnix = end
	if st.Host != nil {
		live.Metrics = st.Host
		live.MetricsAt = now
	}
	live.Health = st.Health
	live.Online = make([]onlineSess, 0, len(st.Sessions))
	for i, se := range st.Sessions {
		ref, ok := refs[se.CredId]
		if !ok || i == maxStatsDeltas {
			continue
		}
		since := time.Unix(se.ConnectedAtUnix, 0).UTC()
		if se.ConnectedAtUnix <= 0 || since.After(now) {
			since = now
		}
		live.Online = append(live.Online, onlineSess{userID: ref.UserID, deviceID: ref.DeviceID, protocol: ref.Protocol,
			inboundID: se.InboundId, remoteIP: clip(se.RemoteIp, 64), since: since})
	}
	secs := uint64(min(max(1, end-st.IntervalStartUnix), int64(maxBatchSpan/time.Second)))
	live.UserDown, live.UserUp = map[string]uint64{}, map[string]uint64{}
	for _, d := range traffic {
		if ref, ok := refs[d.CredID]; ok {
			live.UserDown[ref.UserID] += d.Down * 8 / secs
			live.UserUp[ref.UserID] += d.Up * 8 / secs
		}
	}
	return true
}

func (c *SessionCore) rejectStats(ctx context.Context, state *SessionState, g guardedStats, now time.Time) {
	c.f.log.Warn("stats batch failed the sanity check, traffic dropped", "node", state.NodeID, "deltas", g.rejected, "reason", g.reason)
	if !state.LastReject.IsZero() && now.Sub(state.LastReject) < rejectEventEvery {
		return
	}
	state.LastReject = now
	c.f.event(ctx, 3, "stats_rejected", state.NodeID, map[string]string{"reason": g.reason, "deltas": fmt.Sprint(g.rejected)})
}

func markCoreAck(tr *coreTransition, seq uint64, now time.Time) {
	tr.state.AckPending = max(tr.state.AckPending, seq)
	last := tr.state.LastAck
	if now.Sub(last) >= ackEvery {
		flushCoreAck(tr, now)
	}
}

func flushCoreAck(tr *coreTransition, now time.Time) {
	if tr.state.AckPending > tr.state.AckSent {
		tr.Frames = append(tr.Frames, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Ack{Ack: &agentv1.Ack{UpToSeq: tr.state.AckPending}}})
		tr.state.AckSent = tr.state.AckPending
		tr.state.LastAck = now
	}
}

func (c *SessionCore) agentEvent(ctx context.Context, tr *coreTransition, seq uint64, ev *agentv1.Event, now time.Time) {
	t := time.Unix(ev.TimeUnix, 0).UTC()
	if ev.TimeUnix <= 0 || t.After(now.Add(maxFuture)) {
		t = now
	}
	sev := int(ev.Severity)
	if sev < 1 || sev > 3 {
		sev = 1
	}
	code := ev.Code
	if code == "" {
		code = "agent_event"
	}
	row := store.EventRow{Time: t, Severity: sev, Code: store.Clip(code, 64), InboundID: store.Clip(ev.InboundId, 64)}
	if row.Code == "torrent_attempt" {
		row.Params = map[string]string{}
		if userID := store.Clip(ev.Params["user_id"], 256); userID != "" {
			row.Params["user_id"] = userID
			if user, err := c.f.st.Access().User(ctx, userID); err == nil && user.Name != "" {
				row.Params["user_name"] = store.Clip(user.Name, 256)
			}
		}
		for _, k := range []string{"protocol", "torrent_protocol"} {
			if v := ev.Params[k]; v != "" {
				row.Params[k] = store.Clip(v, 256)
			}
		}
	} else if len(ev.Params) > 0 {
		row.Params = map[string]string{}
		for k, v := range ev.Params {
			if len(row.Params) == maxEventParam {
				break
			}
			row.Params[store.Clip(k, 64)] = store.Clip(v, 256)
		}
	}
	dup, err := c.f.st.IngestEvent(ctx, tr.state.NodeID, tr.state.InstanceID, seq, now, row)
	if err != nil {
		c.f.log.Error("ingest event", "node", tr.state.NodeID, "err", err)
		tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
		return
	}
	if !dup && row.Code == "update_committed" {
		c.f.recordCommit(ctx, tr.state.NodeID, row)
	}
	if !dup {
		c.f.onAwgPrepareEvent(ctx, tr.state.NodeID, row)
	}
	if seq != 0 {
		markCoreAck(tr, seq, now)
	}
	if ev.Code == eventWarpAttention {
		reason := store.Clip(ev.Params["reason"], 64)
		c.warpAttention(ctx, tr, reason, now)
	}
}

func (c *SessionCore) warpAttention(ctx context.Context, tr *coreTransition, reason string, now time.Time) {
	w := c.f.warpModule()
	if w == nil {
		return
	}
	if reason == warpReasonRefresh {
		if !tr.sidecar.L3.warpAsk.IsZero() && now.Sub(tr.sidecar.L3.warpAsk) < warpRefreshGap {
			return
		}
		tr.sidecar.L3.warpAsk = now
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectWarpAttention, WarpReason: reason})
		return
	}
	if err := w.NeedsAttention(ctx, tr.state.NodeID, reason); err != nil {
		c.f.log.Warn("record warp attention", "node", tr.state.NodeID, "err", err)
		return
	}
	if reason == warpReasonLadder {
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectWarpAttention, WarpReason: reason})
	}
}

// applyResult records an ApplyResult and reacts: BASE_MISMATCH -> a full resend; a hash that differs from what was sent
// -> a state_drift event and one full resend (a second drift right after stays as an error event).
func (c *SessionCore) applyResult(ctx context.Context, tr *coreTransition, r *agentv1.ApplyResult, now time.Time) error {
	switch r.Status {
	case agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH:
		c.f.log.Info("agent reports base mismatch, resending full state", "node", tr.state.NodeID, "revision", r.Revision)
		tr.state.FullResendPending = true
		c.requestDesiredPreparation(tr)
		return nil
	case agentv1.ApplyStatus_APPLY_STATUS_REJECTED:
		c.f.event(ctx, 3, "apply_rejected", tr.state.NodeID, map[string]string{"revision": fmt.Sprint(r.Revision), "error": store.Clip(r.Error, 256)})
		return nil
	case agentv1.ApplyStatus_APPLY_STATUS_APPLIED, agentv1.ApplyStatus_APPLY_STATUS_PARTIAL:
	default:
		return nil
	}
	var in []store.InboundApplied
	for _, ir := range r.Inbounds {
		ia := store.InboundApplied{ID: ir.InboundId, State: runState(ir.State), Error: clip(ir.Error, 512), SpecHash: clip(ir.SpecHash, 128)}
		if ir.CertPinSha256 != "" {
			if pin, ok := protocols.NormalizePin(ir.CertPinSha256); ok {
				ia.CertPin = pin
			} else {
				c.f.log.Warn("node reported a malformed certificate pin, ignored", "node", tr.state.NodeID, "inbound", ir.InboundId)
			}
		}
		if ir.CertNotAfterUnix > 0 {
			ia.CertNotAfter = time.Unix(ir.CertNotAfterUnix, 0).UTC()
		}
		in = append(in, ia)
	}
	if tr.sidecar.SentDesired != nil {
		in = append(in, withheldApplied(tr.sidecar.SentDesired.withheld)...)
	}
	if err := c.f.st.NodeApplied(ctx, tr.state.NodeID, r.Revision, r.StateHash, in, now); err != nil {
		c.f.log.Warn("record apply result", "node", tr.state.NodeID, "err", err)
	}
	tr.sidecar.CertSeen = map[string]string{}
	if r.Status != agentv1.ApplyStatus_APPLY_STATUS_APPLIED {
		return nil
	}
	if tr.sidecar.SentDesired == nil || r.Revision != tr.state.SentRevision {
		return nil
	}
	if r.StateHash == tr.sidecar.SentDesired.hash {
		tr.state.DriftResent = false
		tr.state.Drift = false
		return nil
	}
	if !tr.state.DriftResent {
		tr.state.DriftResent = true
		tr.state.FullResendPending = true
		c.f.event(ctx, 2, "state_drift", tr.state.NodeID, map[string]string{"revision": fmt.Sprint(r.Revision)})
		c.requestDesiredPreparation(tr)
		return nil
	}
	tr.state.Drift = true
	c.f.event(ctx, 3, "state_drift", tr.state.NodeID, map[string]string{"revision": fmt.Sprint(r.Revision), "persists": "true"})
	return nil
}

func (c *SessionCore) requestDesiredPreparation(tr *coreTransition) {
	if tr.state.Preparing {
		tr.state.PrepareDirty = true
		return
	}
	tr.state.Preparing = true
	tr.state.PrepareDirty = false
	tr.state.PrepareRetryAt = time.Time{}
	tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectPrepareDesired})
}

func (c *SessionCore) desiredPrepared(ctx context.Context, tr *coreTransition, event SessionEvent) error {
	if !tr.state.Preparing {
		return nil
	}
	tr.state.Preparing = false
	if event.Err != nil {
		tr.state.PrepareRetryAt = event.At.Add(prepareRetryDelay)
		return nil
	}
	tr.state.PrepareRetryAt = time.Time{}
	if event.Prepared == nil {
		return errors.New("prepared desired state is required")
	}
	if event.Prepared.node.State != "retired" && event.Prepared.desired != nil && len(event.Prepared.desired.withheld) > 0 {
		if err := c.f.st.FailWithheldInbounds(ctx, event.Prepared.node.ID, event.Prepared.desired.withheld, withheldReason, event.At); err != nil {
			c.f.log.Warn("mark withheld inbounds", "node", event.Prepared.node.ID, "err", err)
		}
	}
	if err := c.applyPreparedDesired(ctx, tr, event.Prepared, event.At); err != nil {
		return err
	}
	if tr.state.PrepareDirty {
		tr.state.PrepareDirty = false
		c.requestDesiredPreparation(tr)
	}
	return nil
}

func (c *SessionCore) applyPreparedDesired(ctx context.Context, tr *coreTransition, prepared *preparedDesiredState, now time.Time) error {
	state, sidecar := tr.state, tr.sidecar
	node, want := prepared.node, prepared.desired
	if node.State == "retired" {
		return nil
	}
	if want == nil {
		return errors.New("prepared desired state has no node state")
	}
	state.LivenessNanos = int64(time.Duration(node.LivenessTimeoutS) * c.f.unit)
	settings := nodeSettings(node, state.Capabilities)
	sig := settingsSig(settings)
	if sidecar.SentDesired == nil && !state.FullResendPending && state.SentRevision == 0 {
		if state.HelloAppliedStateHash != "" && state.HelloAppliedStateHash == want.hash && state.HelloAppliedRevision > 0 {
			sidecar.SentDesired = want
			state.SentRevision = state.HelloAppliedRevision
			state.SentStateHash = want.hash
			rev := max(node.DesiredRevision, state.HelloAppliedRevision)
			if sig == state.SentSettingsHash {
				return c.f.st.NodeDesired(ctx, node.ID, rev, want.hash)
			}
		}
	}
	full := state.FullResendPending || sidecar.SentDesired == nil || state.SentRevision == 0
	if !full && sidecar.SentDesired.warp != nil && want.warp == nil {
		full = true
	}
	if !full && want.hash == sidecar.SentDesired.hash && sig == state.SentSettingsHash {
		return nil
	}
	rev := max(node.DesiredRevision, state.SentRevision) + 1
	ds := &agentv1.DesiredState{Revision: rev, StateHash: want.hash, Settings: settings}
	if full {
		ds.Inbounds = fullInbounds(want)
		ds.Warp = warpProto(want.warp)
	} else {
		ds.BaseRevision = state.SentRevision
		ds.Inbounds, ds.RemovedInboundIds = diffState(sidecar.SentDesired, want)
		if !sameWarp(sidecar.SentDesired.warp, want.warp) {
			ds.Warp = warpProto(want.warp)
		}
	}
	if err := c.f.st.NodeDesired(ctx, node.ID, rev, want.hash); err != nil {
		return err
	}
	tr.Frames = append(tr.Frames, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_DesiredState{DesiredState: ds}})
	sidecar.SentDesired = want
	state.SentRevision = rev
	state.SentStateHash = want.hash
	state.SentSettingsHash = sig
	if full {
		state.FullResendPending = false
	}
	return nil
}

func (c *SessionCore) alarm(ctx context.Context, tr *coreTransition, event SessionEvent) error {
	now := event.At
	if deadlineDue(tr.state.HelloDeadline, now) {
		tr.Close = &SessionClose{Class: CloseDeadline, Reason: "no Hello"}
		return nil
	}
	if tr.state.AutoBandwidthPending && deadlineDue(tr.state.AutoBandwidthDeadline, now) {
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectAutoBandwidth})
		tr.state.AutoBandwidthDeadline = time.Time{}
		tr.state.AutoBandwidthPending = false
	}
	if deadlineDue(tr.state.NextAckTick, now) {
		flushCoreAck(tr, now)
		tr.state.NextAckTick = advancePeriodic(tr.state.NextAckTick, ackEvery, now)
	}
	if deadlineDue(tr.state.NextCertCheck, now) {
		cert := peerCert{serial: tr.state.PeerCertSerial, notAfter: tr.state.PeerCertNotAfter}
		if err := c.f.recheckCertAt(ctx, cert, now); err != nil {
			tr.Close = &SessionClose{Class: CloseUnauthenticated, Reason: err.Error()}
			return nil
		}
		tr.state.NextCertCheck = advancePeriodic(tr.state.NextCertCheck, c.f.certCheck, now)
	}
	for id, req := range tr.sidecar.Pending {
		if deadlineDue(req.Deadline, now) {
			delete(tr.sidecar.Pending, id)
		}
	}
	if deadlineDue(tr.state.LivenessDeadline, now) {
		tr.Close = &SessionClose{Class: CloseDeadline, Reason: fmt.Sprintf("no message from the agent for %d s", int(time.Duration(tr.state.LivenessNanos).Seconds()))}
	}
	if tr.Close == nil && !tr.state.Preparing && deadlineDue(tr.state.PrepareRetryAt, now) {
		c.requestDesiredPreparation(tr)
	}
	return nil
}

func deadlineDue(deadline, now time.Time) bool {
	return !deadline.IsZero() && !now.Before(deadline)
}

func advancePeriodic(deadline time.Time, period time.Duration, now time.Time) time.Time {
	if deadline.IsZero() || period <= 0 {
		return time.Time{}
	}
	if now.Before(deadline) {
		return deadline
	}
	missed := now.Sub(deadline)/period + 1
	return deadline.Add(missed * period)
}

func nextSessionAlarm(state SessionState, sidecar SessionSidecar, now time.Time) *time.Time {
	if state.Disconnected {
		return nil
	}
	var delay time.Duration
	var found bool
	add := func(deadline time.Time) {
		wait := deadline.Sub(now)
		if wait < 0 {
			wait = 0
		}
		if !found || wait < delay {
			delay, found = wait, true
		}
	}
	for _, deadline := range []time.Time{state.HelloDeadline, state.AutoBandwidthDeadline,
		state.NextAckTick, state.NextCertCheck, state.PrepareRetryAt} {
		if !deadline.IsZero() {
			add(deadline)
		}
	}
	if !state.LivenessDeadline.IsZero() {
		add(state.LivenessDeadline)
	}
	for _, req := range sidecar.Pending {
		if !req.Deadline.IsZero() {
			add(req.Deadline)
		}
	}
	if !found {
		return nil
	}
	t := now.Add(delay)
	return &t
}

// l3Stats stores the AWG and WARP health from a stats batch; applyCoreSnapshot replaces the stream's live view. A health
// write failure is logged but never fails the batch: health is a snapshot, and the next batch replaces it.
func (c *SessionCore) l3Stats(ctx context.Context, nodeID string, intake *l3Intake, st *agentv1.StatsBatch, now time.Time) {
	for _, h := range st.Health {
		if h.Awg == nil {
			continue
		}
		if intake.awg == nil {
			intake.awg = map[string]*healthMemo[*agentv1.AwgHealth]{}
		}
		m := intake.awg[h.InboundId]
		if m == nil {
			m = &healthMemo[*agentv1.AwgHealth]{}
			intake.awg[h.InboundId] = m
		}
		if !m.due(h.Awg, now, nil) {
			continue
		}
		b, err := protojson.Marshal(h.Awg)
		if err != nil {
			continue
		}
		if err := c.f.st.SetInboundAwgHealth(ctx, nodeID, h.InboundId, string(b), now); err != nil {
			c.f.log.Warn("store awg health", "node", nodeID, "inbound", h.InboundId, "err", err)
		}
	}
	w := c.f.warpModule()
	if st.Warp == nil || w == nil {
		return
	}
	if intake.warp.due(st.Warp, now, warpStateChanged) {
		if err := w.StoreHealth(ctx, nodeID, st.Warp); err != nil {
			c.f.log.Warn("store warp health", "node", nodeID, "err", err)
		}
	}
	up := st.Warp.State == agentv1.WarpState_WARP_STATE_UP
	if up && !intake.warpUp {
		// The tunnel is working again: whatever the node asked the owner to look at is over (the owner sees the badge
		// again, not a stale "needs attention").
		if a, err := c.f.st.WarpAccount(ctx, nodeID); err == nil && a.Attention != "" {
			if err := w.NeedsAttention(ctx, nodeID, ""); err != nil {
				c.f.log.Warn("clear warp attention", "node", nodeID, "err", err)
			}
		}
	}
	intake.warpUp = up
}

// certStats keeps the certificate each inbound serves, as the stats stream reports it: an ACME certificate is issued after
// the ApplyResult was sent and renewed later, so the apply-time value alone stays empty. Only a well-formed pin is taken
// (it ends up in subscriptions of self-signed inbounds); a database failure is logged, the next batch tries again.
// An expiry beyond maxCertLife is not believed (the health check would never warn): the stored value stays.
func (c *SessionCore) certStats(ctx context.Context, nodeID string, seen map[string]string, st *agentv1.StatsBatch, now time.Time) {
	for _, h := range st.Health {
		if h.CertPinSha256 == "" || h.CertNotAfterUnix <= 0 || h.CertNotAfterUnix > now.Add(maxCertLife).Unix() {
			continue
		}
		pin, ok := protocols.NormalizePin(h.CertPinSha256)
		if !ok {
			continue
		}
		key := pin + "/" + fmt.Sprint(h.CertNotAfterUnix)
		if seen[h.InboundId] == key {
			continue
		}
		if err := c.f.st.SetInboundCert(ctx, nodeID, h.InboundId, pin, time.Unix(h.CertNotAfterUnix, 0).UTC(), now); err != nil {
			c.f.log.Warn("store inbound certificate", "node", nodeID, "inbound", h.InboundId, "err", err)
			continue
		}
		seen[h.InboundId] = key
	}
}
