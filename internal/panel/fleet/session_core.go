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
	"google.golang.org/protobuf/proto"
)

const sessionStateVersion = 1

// maxCertLife bounds the expiry an agent may report: the self-signed certificates the panel makes live 10 years.
const maxCertLife = 11 * 365 * 24 * time.Hour

// SessionState is the compact connection state. Large snapshots and memo tables live in SessionSidecar.
type SessionState struct {
	Version                  uint8    `json:"v"`
	NodeID                   string   `json:"n,omitempty"`
	OwnerGeneration          uint64   `json:"o,omitempty"`
	InstanceID               string   `json:"i,omitempty"`
	Capabilities             []string `json:"c,omitempty"`
	LivenessNanos            int64    `json:"l,omitempty"`
	HelloDeadlineUnixNano    int64    `json:"h,omitempty"`
	LivenessDeadlineUnixNano int64    `json:"d,omitempty"`
	AutoBandwidthUnixNano    int64    `json:"b,omitempty"`
	AutoBandwidthPending     bool     `json:"ap,omitempty"`
	NextAckTickUnixNano      int64    `json:"a,omitempty"`
	NextCertCheckUnixNano    int64    `json:"x,omitempty"`
	AckPending               uint64   `json:"p,omitempty"`
	AckSent                  uint64   `json:"s,omitempty"`
	LastAckUnixNano          int64    `json:"k,omitempty"`
	LastRejectUnixNano       int64    `json:"r,omitempty"`
	LastEndUnix              int64    `json:"e,omitempty"`
	LastSeenUnixNano         int64    `json:"v_at,omitempty"`
	SentRevision             uint64   `json:"q,omitempty"`
	SentStateHash            string   `json:"sh,omitempty"`
	SentSettingsHash         string   `json:"ss,omitempty"`
	DriftResent              bool     `json:"dr,omitempty"`
	Drift                    bool     `json:"dt,omitempty"`
	SidecarVersion           uint32   `json:"sv,omitempty"`
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
	Kind             PendingRequestKind
	DeadlineUnixNano int64
}

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
	PoisonSeq   *stuckSeq
}

type EventKind uint8

const (
	EventOpen EventKind = iota + 1
	EventTimersStarted
	EventHello
	EventInitialReconcile
	EventAgentFrame
	EventDesiredChanged
	EventAdminCommand
	EventLogStart
	EventLogCancel
	EventAlarm
	EventDisconnected
	EventOwnerSuperseded
)

type AlarmKind uint8

const (
	AlarmAny AlarmKind = iota
	AlarmHello
	AlarmLiveness
	AlarmAutoBandwidth
	AlarmAck
	AlarmCertificate
)

type AdminRequest struct {
	RequestID string
	Deadline  time.Time
	Frame     *agentv1.ConnectResponse
	Kind      PendingRequestKind
}

type SessionEvent struct {
	Kind            EventKind
	At              time.Time
	Frame           *agentv1.ConnectRequest
	Request         *AdminRequest
	Alarm           AlarmKind
	Mode            reconcileMode
	Prepared        *preparedDesiredState
	AutoBandwidthAt time.Time
}

type EffectKind uint8

const (
	EffectSessionStarted EffectKind = iota + 1
	EffectAutoBandwidth
	EffectLiveUpdate
	EffectUsage
	EffectCommandResult
	EffectDoctorReport
	EffectLogChunk
	EffectAwgPrepare
	EffectCheckCertificate
	EffectWarpAttention
	EffectDesiredReconcile
)

type SessionStarted struct {
	Previous    store.NodeRow
	BootAt      time.Time
	Now         time.Time
	AutoMeasure bool
}

type SessionEffect struct {
	Kind          EffectKind
	RequestID     string
	CommandResult *agentv1.CommandResult
	DoctorReport  *agentv1.DoctorReport
	LogChunk      *agentv1.LogChunk
	Live          *LiveSnapshot
	Users         []string
	Started       *SessionStarted
	Event         *store.EventRow
	WarpReason    string
	ReconcileMode reconcileMode
	ErrorLog      string
}

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

type SessionClose struct {
	Class  CloseClass
	Reason string
}

type Transition struct {
	State     SessionState
	Sidecar   SessionSidecar
	Frames    []*agentv1.ConnectResponse
	Effects   []SessionEffect
	NextAlarm *time.Time
	Close     *SessionClose
}

type preparedDesiredState struct {
	node    store.NodeRow
	desired *nodeState
}

// SessionCore processes one event at a time. It owns no goroutines, timers, channels, or transports.
type SessionCore struct{ f *Fleet }

func NewSessionCore(f *Fleet) *SessionCore { return &SessionCore{f: f} }

// Step processes one event. On error, the caller must discard both the transition and the sidecar it passed in;
// Step may have mutated maps shared with that sidecar.
func (c *SessionCore) Step(ctx context.Context, state SessionState, sidecar SessionSidecar, event SessionEvent) (Transition, error) {
	if c == nil || c.f == nil {
		return Transition{}, errors.New("session core has no fleet")
	}
	if event.At.IsZero() {
		return Transition{}, errors.New("session event time is required")
	}
	event.At = event.At.UTC()
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
	tr := Transition{State: state, Sidecar: sidecar}
	switch event.Kind {
	case EventOpen:
		if tr.State.HelloDeadlineUnixNano == 0 {
			tr.State.HelloDeadlineUnixNano = event.At.Add(helloTimeout).UnixNano()
		}
	case EventTimersStarted:
		tr.State.LivenessDeadlineUnixNano = event.At.Add(time.Duration(tr.State.LivenessNanos)).UnixNano()
		tr.State.NextAckTickUnixNano = event.At.Add(ackEvery).UnixNano()
		tr.State.NextCertCheckUnixNano = event.At.Add(c.f.certCheck).UnixNano()
	case EventHello:
		if err := c.hello(ctx, &tr, event); err != nil {
			return tr, err
		}
	case EventInitialReconcile:
		if !event.AutoBandwidthAt.IsZero() && tr.State.AutoBandwidthPending {
			tr.State.AutoBandwidthUnixNano = event.AutoBandwidthAt.Add(c.f.measureDelay).UnixNano()
		}
		if err := c.reconcile(ctx, &tr, reconcileConnect, event.Frame.GetHello(), event.Prepared, event.At); err != nil {
			return tr, err
		}
	case EventAgentFrame:
		c.agentFrame(ctx, &tr, event)
	case EventDesiredChanged:
		mode := event.Mode
		if mode == 0 {
			mode = reconcileChange
		}
		if err := c.reconcile(ctx, &tr, mode, nil, event.Prepared, event.At); err != nil {
			return tr, err
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
		delete(tr.Sidecar.Pending, id)
		tr.Frames = append(tr.Frames, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_LogCancel{LogCancel: &agentv1.LogCancel{RequestId: id}}})
	case EventAlarm:
		if err := c.alarm(ctx, &tr, event); err != nil {
			return tr, err
		}
	case EventDisconnected:
		tr.Sidecar.Pending = map[string]PendingRequest{}
		tr.State.AutoBandwidthUnixNano = 0
	case EventOwnerSuperseded:
		tr.Close = &SessionClose{Class: CloseConflict, Reason: "superseded by a newer stream"}
	default:
		return tr, fmt.Errorf("unknown session event kind %d", event.Kind)
	}
	tr.NextAlarm = nextSessionAlarm(tr.State, tr.Sidecar)
	return tr, nil
}

func (c *SessionCore) hello(ctx context.Context, tr *Transition, event SessionEvent) error {
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
	if !c.f.ownsSession(tr.State.NodeID, tr.State.OwnerGeneration) {
		tr.Close = &SessionClose{Class: CloseConflict, Reason: "superseded by a newer stream"}
		return nil
	}
	if h.NodeId != "" && h.NodeId != tr.State.NodeID {
		c.f.log.Warn("hello names another node than the certificate", "cert_node", tr.State.NodeID)
	}
	info := helloInfo(h)
	prev, acked, err := c.f.st.NodeHello(ctx, tr.State.NodeID, info, event.At)
	if errors.Is(err, store.ErrNodeRetired) {
		tr.Close = &SessionClose{Class: CloseFailedPrecondition, Reason: "node retired"}
		return nil
	}
	if err != nil {
		c.f.log.Error("hello", "node", tr.State.NodeID, "err", err)
		tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
		return nil
	}
	node, err := c.f.st.Node(ctx, tr.State.NodeID)
	if err != nil {
		tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
		return nil
	}
	tr.State.Version = sessionStateVersion
	tr.State.InstanceID = h.InstanceId
	tr.State.Capabilities = capabilities(h)
	tr.State.HelloDeadlineUnixNano = 0
	tr.State.LivenessNanos = int64(time.Duration(node.LivenessTimeoutS) * c.f.unit)
	tr.State.LastSeenUnixNano = event.At.UnixNano()
	tr.State.LivenessDeadlineUnixNano = event.At.Add(time.Duration(tr.State.LivenessNanos)).UnixNano()
	tr.State.NextAckTickUnixNano = event.At.Add(ackEvery).UnixNano()
	tr.State.NextCertCheckUnixNano = event.At.Add(c.f.certCheck).UnixNano()
	tr.State.SidecarVersion = tr.Sidecar.Version
	tr.Sidecar.Live.UserDown = map[string]uint64{}
	tr.Sidecar.Live.UserUp = map[string]uint64{}
	tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectSessionStarted, Started: &SessionStarted{
		Previous: prev, BootAt: info.BootAt, Now: event.At, AutoMeasure: prev.State == "pending" && node.BandwidthMbps == 0,
	}})
	tr.State.AutoBandwidthPending = prev.State == "pending" && node.BandwidthMbps == 0 && slices.Contains(tr.State.Capabilities, capBandwidth)
	tr.Frames = append(tr.Frames, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_HelloAck{HelloAck: &agentv1.HelloAck{
		AckedSeq: acked, ServerTimeUnix: event.At.Unix(), Settings: nodeSettings(node, tr.State.Capabilities), LinkSupported: true,
	}}})
	return nil
}

func (c *SessionCore) request(tr *Transition, request *AdminRequest, at time.Time, fallback PendingRequestKind) {
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
	tr.Sidecar.Pending[request.RequestID] = PendingRequest{Kind: kind, DeadlineUnixNano: deadline.UnixNano()}
	tr.Frames = append(tr.Frames, request.Frame)
}

func (c *SessionCore) agentFrame(ctx context.Context, tr *Transition, event SessionEvent) {
	m := event.Frame
	if m == nil {
		return
	}
	tr.State.LastSeenUnixNano = event.At.UnixNano()
	if tr.State.LivenessNanos > 0 {
		tr.State.LivenessDeadlineUnixNano = event.At.Add(time.Duration(tr.State.LivenessNanos)).UnixNano()
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
			c.f.log.Warn("apply result", "node", tr.State.NodeID, "err", err)
		}
	case m.GetCommandResult() != nil:
		r := m.GetCommandResult()
		delete(tr.Sidecar.Pending, r.RequestId)
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectCommandResult, RequestID: r.RequestId, CommandResult: r})
	case m.GetLogChunk() != nil:
		chunk := m.GetLogChunk()
		if chunk.Eof {
			delete(tr.Sidecar.Pending, chunk.RequestId)
		}
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectLogChunk, RequestID: chunk.RequestId, LogChunk: chunk})
	case m.GetDoctorReport() != nil:
		r := m.GetDoctorReport()
		delete(tr.Sidecar.Pending, r.RequestId)
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectDoctorReport, RequestID: r.RequestId, DoctorReport: r})
	}
}

func (c *SessionCore) stats(ctx context.Context, tr *Transition, seq uint64, st *agentv1.StatsBatch, now time.Time) {
	hour, stale := bucketHour(time.Unix(st.IntervalEndUnix, 0), now)
	g := c.f.guardStats(st)
	if g.rejected > 0 {
		c.rejectStats(ctx, &tr.State, g, now)
	}
	in := store.FleetStatsIn{NodeID: tr.State.NodeID, Instance: tr.State.InstanceID, Seq: seq, Now: now, HourStart: hour, Traffic: g.traffic}
	for i, se := range st.Sessions {
		if i == maxStatsDeltas {
			break
		}
		in.Sessions = append(in.Sessions, store.FleetSessionRef{CredID: se.CredId, InboundID: se.InboundId})
	}
	out, err := c.f.st.IngestStats(ctx, in)
	if err != nil {
		c.f.log.Error("ingest stats", "node", tr.State.NodeID, "err", err)
		if !c.stuckStats(tr.State.NodeID, tr.State.InstanceID, seq) {
			tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
			return
		}
		c.f.log.Error("stats batch refused by the database twice, dropped", "node", tr.State.NodeID, "seq", seq)
		c.f.event(ctx, 3, "stats_dropped", tr.State.NodeID, map[string]string{"reason": "database_refused", "seq": fmt.Sprint(seq)})
		if seq != 0 {
			if err := c.f.st.SkipSeq(ctx, tr.State.NodeID, tr.State.InstanceID, seq, now); err != nil {
				tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
				return
			}
			markCoreAck(tr, seq, now)
		}
		return
	}
	c.unstickStats(tr.State.NodeID)
	if !out.Duplicate {
		updated := applyCoreSnapshot(&tr.State, &tr.Sidecar.Live, st, g.traffic, now, out.Refs)
		c.l3Stats(ctx, tr.State.NodeID, &tr.Sidecar.L3, st, now)
		c.certStats(ctx, tr.State.NodeID, &tr.Sidecar.CertSeen, st, now)
		c.f.touchAwgDevices(ctx, st, out.Refs, now)
		if out.Skipped > 0 {
			c.f.log.Warn("stats for unknown credentials or foreign inbounds dropped", "node", tr.State.NodeID, "count", out.Skipped)
		}
		if stale {
			c.f.event(ctx, 2, "stats_stale", tr.State.NodeID, map[string]string{"age_hours": fmt.Sprint(int(now.Sub(time.Unix(st.IntervalEndUnix, 0)).Hours()))})
		}
		if updated {
			live := cloneLiveSnapshot(tr.Sidecar.Live)
			tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectLiveUpdate, Live: &live})
		}
		if c.f.cfg.OnUsage != nil && len(out.Users) > 0 {
			tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectUsage, Users: slices.Clone(out.Users)})
		}
	}
	if seq != 0 {
		markCoreAck(tr, seq, now)
	}
}

func applyCoreSnapshot(state *SessionState, live *LiveSnapshot, st *agentv1.StatsBatch, traffic []store.FleetTraffic, now time.Time, refs map[string]store.FleetCredRef) bool {
	end := st.IntervalEndUnix
	if end > now.Add(maxFuture).Unix() {
		end = now.Unix()
	}
	if end < state.LastEndUnix {
		return false
	}
	state.LastEndUnix = end
	if st.Host != nil {
		live.Metrics = st.Host
		live.MetricsAt = now
	}
	live.Health = st.Health
	live.Online = live.Online[:0]
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

func cloneLiveSnapshot(live LiveSnapshot) LiveSnapshot {
	out := live
	out.Health = slices.Clone(live.Health)
	out.Online = slices.Clone(live.Online)
	out.UserDown = make(map[string]uint64, len(live.UserDown))
	for id, value := range live.UserDown {
		out.UserDown[id] = value
	}
	out.UserUp = make(map[string]uint64, len(live.UserUp))
	for id, value := range live.UserUp {
		out.UserUp[id] = value
	}
	return out
}

func (c *SessionCore) rejectStats(ctx context.Context, state *SessionState, g guardedStats, now time.Time) {
	c.f.log.Warn("stats batch failed the sanity check, traffic dropped", "node", state.NodeID, "deltas", g.rejected, "reason", g.reason)
	if state.LastRejectUnixNano != 0 && now.Sub(time.Unix(0, state.LastRejectUnixNano)) < rejectEventEvery {
		return
	}
	state.LastRejectUnixNano = now.UnixNano()
	c.f.event(ctx, 3, "stats_rejected", state.NodeID, map[string]string{"reason": g.reason, "deltas": fmt.Sprint(g.rejected)})
}

func (c *SessionCore) stuckStats(nodeID, instance string, seq uint64) bool {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	k := stuckSeq{instance: instance, seq: seq}
	if c.f.stuck[nodeID] == k {
		delete(c.f.stuck, nodeID)
		return true
	}
	c.f.stuck[nodeID] = k
	return false
}

func (c *SessionCore) unstickStats(nodeID string) {
	c.f.mu.Lock()
	delete(c.f.stuck, nodeID)
	c.f.mu.Unlock()
}

func markCoreAck(tr *Transition, seq uint64, now time.Time) {
	tr.State.AckPending = max(tr.State.AckPending, seq)
	last := time.Time{}
	if tr.State.LastAckUnixNano != 0 {
		last = time.Unix(0, tr.State.LastAckUnixNano)
	}
	if now.Sub(last) >= ackEvery {
		flushCoreAck(tr, now)
	}
}

func flushCoreAck(tr *Transition, now time.Time) {
	if tr.State.AckPending > tr.State.AckSent {
		tr.Frames = append(tr.Frames, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Ack{Ack: &agentv1.Ack{UpToSeq: tr.State.AckPending}}})
		tr.State.AckSent = tr.State.AckPending
		tr.State.LastAckUnixNano = now.UnixNano()
	}
}

func (c *SessionCore) agentEvent(ctx context.Context, tr *Transition, seq uint64, ev *agentv1.Event, now time.Time) {
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
	dup, err := c.f.st.IngestEvent(ctx, tr.State.NodeID, tr.State.InstanceID, seq, now, row)
	if err != nil {
		c.f.log.Error("ingest event", "node", tr.State.NodeID, "err", err)
		tr.Close = &SessionClose{Class: CloseInternal, Reason: "internal error"}
		return
	}
	if !dup && row.Code == "update_committed" {
		c.f.recordCommit(ctx, tr.State.NodeID, row)
	}
	if !dup {
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectAwgPrepare, Event: &row})
	}
	if seq != 0 {
		markCoreAck(tr, seq, now)
	}
	if ev.Code == eventWarpAttention {
		reason := store.Clip(ev.Params["reason"], 64)
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectWarpAttention, WarpReason: reason})
	}
}

func (c *SessionCore) applyResult(ctx context.Context, tr *Transition, r *agentv1.ApplyResult, now time.Time) error {
	switch r.Status {
	case agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH:
		c.f.log.Info("agent reports base mismatch, resending full state", "node", tr.State.NodeID, "revision", r.Revision)
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectDesiredReconcile, ReconcileMode: reconcileFull, ErrorLog: "full resend"})
		return nil
	case agentv1.ApplyStatus_APPLY_STATUS_REJECTED:
		c.f.event(ctx, 3, "apply_rejected", tr.State.NodeID, map[string]string{"revision": fmt.Sprint(r.Revision), "error": store.Clip(r.Error, 256)})
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
				c.f.log.Warn("node reported a malformed certificate pin, ignored", "node", tr.State.NodeID, "inbound", ir.InboundId)
			}
		}
		if ir.CertNotAfterUnix > 0 {
			ia.CertNotAfter = time.Unix(ir.CertNotAfterUnix, 0).UTC()
		}
		in = append(in, ia)
	}
	if tr.Sidecar.SentDesired != nil {
		in = append(in, withheldApplied(tr.Sidecar.SentDesired.withheld)...)
	}
	if err := c.f.st.NodeApplied(ctx, tr.State.NodeID, r.Revision, r.StateHash, in, now); err != nil {
		c.f.log.Warn("record apply result", "node", tr.State.NodeID, "err", err)
	}
	tr.Sidecar.CertSeen = map[string]string{}
	if r.Status != agentv1.ApplyStatus_APPLY_STATUS_APPLIED {
		return nil
	}
	if tr.Sidecar.SentDesired == nil || r.Revision != tr.State.SentRevision {
		return nil
	}
	if r.StateHash == tr.Sidecar.SentDesired.hash {
		tr.State.DriftResent = false
		tr.State.Drift = false
		return nil
	}
	if !tr.State.DriftResent {
		tr.State.DriftResent = true
		c.f.event(ctx, 2, "state_drift", tr.State.NodeID, map[string]string{"revision": fmt.Sprint(r.Revision)})
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectDesiredReconcile, ReconcileMode: reconcileFull, ErrorLog: "full resend after drift"})
		return nil
	}
	tr.State.Drift = true
	c.f.event(ctx, 3, "state_drift", tr.State.NodeID, map[string]string{"revision": fmt.Sprint(r.Revision), "persists": "true"})
	return nil
}

func (c *SessionCore) reconcile(ctx context.Context, tr *Transition, mode reconcileMode, hello *agentv1.Hello, prepared *preparedDesiredState, now time.Time) error {
	state, sidecar := &tr.State, &tr.Sidecar
	if prepared == nil {
		return errors.New("prepared desired state is required")
	}
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
	if mode == reconcileConnect {
		if hello != nil && hello.AppliedStateHash != "" && hello.AppliedStateHash == want.hash && hello.AppliedRevision > 0 {
			sidecar.SentDesired = want
			state.SentRevision = hello.AppliedRevision
			state.SentSettingsHash = sig
			state.SentStateHash = want.hash
			rev := max(node.DesiredRevision, hello.AppliedRevision)
			return c.f.st.NodeDesired(ctx, node.ID, rev, want.hash)
		}
		mode = reconcileFull
	}
	if sidecar.SentDesired == nil && mode == reconcileChange {
		return nil
	}
	if sidecar.SentDesired == nil || state.SentRevision == 0 {
		mode = reconcileFull
	}
	if mode == reconcileChange && sidecar.SentDesired.warp != nil && want.warp == nil {
		mode = reconcileFull
	}
	if mode == reconcileChange && want.hash == sidecar.SentDesired.hash && sig == state.SentSettingsHash {
		return nil
	}
	rev := max(node.DesiredRevision, state.SentRevision) + 1
	ds := &agentv1.DesiredState{Revision: rev, StateHash: want.hash, Settings: settings}
	if mode == reconcileFull {
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
	return nil
}

func (c *SessionCore) alarm(ctx context.Context, tr *Transition, event SessionEvent) error {
	now := event.At
	if event.Alarm == AlarmHello || event.Alarm == AlarmAny {
		if tr.State.HelloDeadlineUnixNano != 0 && now.UnixNano() >= tr.State.HelloDeadlineUnixNano {
			tr.Close = &SessionClose{Class: CloseDeadline, Reason: "no Hello"}
			return nil
		}
	}
	if event.Alarm == AlarmLiveness || event.Alarm == AlarmAny {
		if tr.State.LivenessDeadlineUnixNano != 0 && now.UnixNano() >= tr.State.LivenessDeadlineUnixNano {
			tr.Close = &SessionClose{Class: CloseDeadline, Reason: fmt.Sprintf("no message from the agent for %d s", int(time.Duration(tr.State.LivenessNanos).Seconds()))}
			return nil
		}
	}
	if (event.Alarm == AlarmAutoBandwidth || event.Alarm == AlarmAny) && deadlineDue(tr.State.AutoBandwidthUnixNano, now) {
		tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectAutoBandwidth})
		tr.State.AutoBandwidthUnixNano = 0
		tr.State.AutoBandwidthPending = false
	}
	if event.Alarm == AlarmAck || event.Alarm == AlarmAny {
		if event.Alarm == AlarmAck || deadlineDue(tr.State.NextAckTickUnixNano, now) {
			flushCoreAck(tr, now)
			tr.State.NextAckTickUnixNano = advancePeriodic(tr.State.NextAckTickUnixNano, ackEvery, now)
		}
	}
	if event.Alarm == AlarmCertificate || event.Alarm == AlarmAny {
		if event.Alarm == AlarmCertificate || deadlineDue(tr.State.NextCertCheckUnixNano, now) {
			tr.Effects = append(tr.Effects, SessionEffect{Kind: EffectCheckCertificate})
			tr.State.NextCertCheckUnixNano = advancePeriodic(tr.State.NextCertCheckUnixNano, c.f.certCheck, now)
		}
	}
	for id, req := range tr.Sidecar.Pending {
		if deadlineDue(req.DeadlineUnixNano, now) {
			delete(tr.Sidecar.Pending, id)
		}
	}
	return nil
}

func deadlineDue(deadline int64, now time.Time) bool {
	return deadline != 0 && now.UnixNano() >= deadline
}

func advancePeriodic(deadline int64, period time.Duration, now time.Time) int64 {
	if deadline == 0 || period <= 0 {
		return 0
	}
	periodNanos := int64(period)
	if now.UnixNano() < deadline {
		return deadline
	}
	missed := (now.UnixNano()-deadline)/periodNanos + 1
	return deadline + missed*periodNanos
}

func nextSessionAlarm(state SessionState, sidecar SessionSidecar) *time.Time {
	var next int64
	for _, deadline := range []int64{state.HelloDeadlineUnixNano, state.LivenessDeadlineUnixNano, state.AutoBandwidthUnixNano,
		state.NextAckTickUnixNano, state.NextCertCheckUnixNano} {
		if deadline != 0 && (next == 0 || deadline < next) {
			next = deadline
		}
	}
	for _, req := range sidecar.Pending {
		if req.DeadlineUnixNano != 0 && (next == 0 || req.DeadlineUnixNano < next) {
			next = req.DeadlineUnixNano
		}
	}
	if next == 0 {
		return nil
	}
	t := time.Unix(0, next).UTC()
	return &t
}

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
		b, err := protojsonMarshal(h.Awg)
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
		if a, err := c.f.st.WarpAccount(ctx, nodeID); err == nil && a.Attention != "" {
			if err := w.NeedsAttention(ctx, nodeID, ""); err != nil {
				c.f.log.Warn("clear warp attention", "node", nodeID, "err", err)
			}
		}
	}
	intake.warpUp = up
}

func protojsonMarshal(message proto.Message) ([]byte, error) {
	return protojson.Marshal(message)
}

func (c *SessionCore) certStats(ctx context.Context, nodeID string, seen *map[string]string, st *agentv1.StatsBatch, now time.Time) {
	for _, h := range st.Health {
		if h.CertPinSha256 == "" || h.CertNotAfterUnix <= 0 || h.CertNotAfterUnix > now.Add(maxCertLife).Unix() {
			continue
		}
		pin, ok := protocols.NormalizePin(h.CertPinSha256)
		if !ok {
			continue
		}
		key := pin + "/" + fmt.Sprint(h.CertNotAfterUnix)
		if (*seen)[h.InboundId] == key {
			continue
		}
		if err := c.f.st.SetInboundCert(ctx, nodeID, h.InboundId, pin, time.Unix(h.CertNotAfterUnix, 0).UTC(), now); err != nil {
			c.f.log.Warn("store inbound certificate", "node", nodeID, "inbound", h.InboundId, "err", err)
			continue
		}
		(*seen)[h.InboundId] = key
	}
}
