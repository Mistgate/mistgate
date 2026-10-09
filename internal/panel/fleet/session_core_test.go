package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func coreFixture(t *testing.T, name string) (*env, *SessionCore, context.Context, SessionState, time.Time) {
	t.Helper()
	e := newCoreEnv(t)
	nodeID, _, _ := e.createEnrollment(name, name+".example.com")
	_, ownerCtx := e.f.claimOwner(nodeID, e.ctx)
	e.t.Cleanup(func() {
		e.f.mu.Lock()
		owner := e.f.owners[nodeID]
		e.f.mu.Unlock()
		if owner.cancel != nil {
			owner.cancel(nil)
		}
	})
	core := NewSessionCore(e.f)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	state := SessionState{Version: sessionStateVersion, NodeID: nodeID, OwnerGeneration: 1,
		HelloDeadline: now.Add(helloTimeout)}
	return e, core, ownerCtx, state, now
}

func TestSessionStateJSONRoundTrip(t *testing.T) {
	assertSessionStateJSONTags(t, reflect.TypeOf(SessionState{}))

	original := SessionState{}
	fillSessionStateValue(t, reflect.ValueOf(&original).Elem())
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionState
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !sessionStateDeepEqual(reflect.ValueOf(original), reflect.ValueOf(decoded)) {
		t.Fatalf("SessionState JSON round trip differs:\noriginal: %+v\ndecoded:  %+v\nJSON: %s", original, decoded, encoded)
	}
}

func TestSessionStateDecodesLegacyCapabilities(t *testing.T) {
	var state SessionState
	if err := json.Unmarshal([]byte(`{"v":1,"n":"nod_legacy_state","c":["doctor/1"],"o":7}`), &state); err != nil {
		t.Fatal(err)
	}
	if state.NodeID != "nod_legacy_state" || state.OwnerGeneration != 7 {
		t.Fatalf("legacy state decoded as %+v", state)
	}
	encoded, err := json.Marshal(state)
	if err != nil || strings.Contains(string(encoded), `"c"`) {
		t.Fatalf("removed capabilities field was retained: %s, %v", encoded, err)
	}
}

func assertSessionStateJSONTags(t *testing.T, typ reflect.Type) {
	t.Helper()
	if typ == reflect.TypeOf(time.Time{}) {
		return
	}
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		assertSessionStateJSONTags(t, typ.Elem())
	case reflect.Map:
		assertSessionStateJSONTags(t, typ.Key())
		assertSessionStateJSONTags(t, typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if field.PkgPath != "" {
				continue
			}
			tag, ok := field.Tag.Lookup("json")
			if !ok || tag == "" || tag == "-" {
				t.Errorf("%s.%s has no JSON field tag", typ, field.Name)
			}
			assertSessionStateJSONTags(t, field.Type)
		}
	}
}

func fillSessionStateValue(t *testing.T, value reflect.Value) {
	t.Helper()
	if value.Type() == reflect.TypeOf(time.Time{}) {
		value.Set(reflect.ValueOf(time.Date(2026, 10, 6, 12, 34, 56, 123, time.FixedZone("roundtrip", 3600))))
		return
	}
	switch value.Kind() {
	case reflect.Pointer:
		value.Set(reflect.New(value.Type().Elem()))
		fillSessionStateValue(t, value.Elem())
	case reflect.Map:
		key := reflect.New(value.Type().Key()).Elem()
		fillSessionStateValue(t, key)
		item := reflect.New(value.Type().Elem()).Elem()
		fillSessionStateValue(t, item)
		value.Set(reflect.MakeMapWithSize(value.Type(), 1))
		value.SetMapIndex(key, item)
	case reflect.Slice:
		value.Set(reflect.MakeSlice(value.Type(), 1, 1))
		fillSessionStateValue(t, value.Index(0))
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			fillSessionStateValue(t, value.Index(i))
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			fillSessionStateValue(t, value.Field(i))
		}
	case reflect.String:
		value.SetString("roundtrip")
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		value.SetUint(1)
	case reflect.Float32, reflect.Float64:
		value.SetFloat(1)
	default:
		t.Fatalf("cannot fill non-zero test value for %s", value.Type())
	}
}

func sessionStateDeepEqual(a, b reflect.Value) bool {
	if a.Type() == reflect.TypeOf(time.Time{}) {
		return a.Interface().(time.Time).Equal(b.Interface().(time.Time))
	}
	if a.Kind() != b.Kind() || a.Type() != b.Type() {
		return false
	}
	switch a.Kind() {
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return sessionStateDeepEqual(a.Elem(), b.Elem())
	case reflect.Map:
		if a.Len() != b.Len() {
			return false
		}
		iter := a.MapRange()
		for iter.Next() {
			other := b.MapIndex(iter.Key())
			if !other.IsValid() || !sessionStateDeepEqual(iter.Value(), other) {
				return false
			}
		}
		return true
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			return false
		}
		for i := 0; i < a.Len(); i++ {
			if !sessionStateDeepEqual(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			if !sessionStateDeepEqual(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(a.Interface(), b.Interface())
	}
}

// newTestSession builds an adapter session the way runSession does: its own cancelable context under parent, done, the
// out queue, the alarm timer and a core (a fresh one when core is nil), with state as the starting point.
func newTestSession(e *env, parent context.Context, core *SessionCore, state SessionState) *session {
	e.t.Helper()
	ctx, cancel := context.WithCancelCause(parent)
	timer := time.NewTimer(time.Hour)
	e.t.Cleanup(func() {
		cancel(nil)
		timer.Stop()
	})
	if core == nil {
		core = NewSessionCore(e.f)
	}
	return &session{f: e.f, nodeID: state.NodeID, owner: state.OwnerGeneration,
		ctx: ctx, cancel: cancel, done: make(chan struct{}), out: make(chan *agentv1.ConnectResponse, outQueue),
		replies: map[string]chan *agentv1.ConnectRequest{}, logs: map[string]*logSub{},
		core: core, coreState: state, alarmTimer: timer}
}

type testTransition struct {
	Transition
	State SessionState
}

func coreStep(ctx context.Context, core *SessionCore, state SessionState, event SessionEvent) (testTransition, error) {
	tr, err := core.Step(ctx, &state, event)
	return testTransition{Transition: tr, State: state}, err
}

type replayWarpMod struct {
	e         *env
	health    []*agentv1.WarpHealth
	attention []string
}

func (w *replayWarpMod) Spec(context.Context, string) (*plugin.WarpSpec, error) { return nil, nil }

func (w *replayWarpMod) StoreHealth(ctx context.Context, nodeID string, health *agentv1.WarpHealth) error {
	b, err := protojson.Marshal(health)
	if err != nil {
		return err
	}
	if err := w.e.st.SetWarpHealth(ctx, nodeID, string(b), w.e.f.now()); err != nil {
		return err
	}
	w.health = append(w.health, proto.Clone(health).(*agentv1.WarpHealth))
	return nil
}

func (w *replayWarpMod) NeedsAttention(ctx context.Context, nodeID, reason string) error {
	if err := w.e.st.SetWarpAttention(ctx, nodeID, reason, w.e.f.now()); err != nil {
		return err
	}
	w.attention = append(w.attention, reason)
	return nil
}

func (*replayWarpMod) RefreshByNode(context.Context, string) error { return nil }

func (*replayWarpMod) AutoReregister(context.Context, string) (bool, error) { return false, nil }

func (*replayWarpMod) Summary(*store.WarpAccountRow, bool, time.Time) *adminv1.WarpSummary {
	return &adminv1.WarpSummary{}
}

type sessionReplay struct {
	e         *env
	ctx       context.Context
	core      *SessionCore
	state     SessionState
	now       time.Time
	rehydrate bool
	desired   *plugin.InboundSpec
	warp      *replayWarpMod
}

func newSessionReplay(t *testing.T, nodeID string, rehydrate bool) *sessionReplay {
	t.Helper()
	e := newCoreEnv(t)
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	desired := &plugin.InboundSpec{ID: "inb_awg", Protocol: "awg", ProfileID: "prf_awg", Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: 51820}, Egress: "direct", Settings: json.RawMessage(`{"mode":"default"}`)}
	r := &sessionReplay{e: e, now: now, rehydrate: rehydrate, desired: desired}
	e.f.now = func() time.Time { return r.now }
	e.f.cfg.Now = e.f.now
	e.f.certCheck = 30 * time.Second
	e.f.measureDelay = 5 * time.Second
	e.f.measureWait = 4 * time.Second
	e.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) {
		return []statehash.Inbound{{Spec: *r.desired}}, nil
	}
	if _, err := e.st.CreateEnrollment(e.ctx, &store.NodeRow{ID: nodeID, Name: "replay", Address: "example.com"}, "", []byte("test-hash"), "adm_test", now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	liveness := 3600
	if _, err := e.st.UpdateNode(e.ctx, nodeID, store.NodePatch{LivenessTimeoutS: &liveness}); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO profile (id, protocol, name, settings_json, version, created_at, updated_at) VALUES ('prf_awg', 'awg', 'awg', '{}', 1, ?, ?)`, now.Unix(), now.Unix())
	e.exec(`INSERT INTO inbound (id, profile_id, node_id, spec_version, created_at, updated_at) VALUES ('inb_awg', 'prf_awg', ?, 1, ?, ?)`, nodeID, now.Unix(), now.Unix())
	if err := e.st.CreateWarpAccount(e.ctx, store.WarpAccountRow{NodeID: nodeID, Source: store.WarpImported, SecretEnc: []byte{1},
		PeerPublicKey: "cGVlcg==", EndpointV4: "203.0.113.10", Ports: []uint16{2408}, AddressV4: "172.16.0.2/32", MTU: 1280,
		Enabled: true, Attention: "down_after_ladder", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	r.warp = &replayWarpMod{e: e}
	e.f.SetWarp(r.warp)
	owner, ctx := e.f.claimOwner(nodeID, e.ctx)
	r.ctx = ctx
	t.Cleanup(func() {
		e.f.mu.Lock()
		current := e.f.owners[nodeID]
		e.f.mu.Unlock()
		if current.cancel != nil {
			current.cancel(nil)
		}
	})
	r.state = SessionState{Version: sessionStateVersion, NodeID: nodeID, OwnerGeneration: owner, HelloDeadline: now.Add(helloTimeout)}
	r.core = NewSessionCore(e.f)
	return r
}

func (r *sessionReplay) step(t *testing.T, event SessionEvent) testTransition {
	t.Helper()
	r.now = event.At
	if r.rehydrate {
		b, err := json.Marshal(r.state)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &r.state); err != nil {
			t.Fatal(err)
		}
		r.core = NewSessionCore(r.e.f)
	}
	tr, err := coreStep(r.ctx, r.core, r.state, event)
	if err != nil {
		t.Fatalf("step %d: %v", event.Kind, err)
	}
	r.state = tr.State
	return tr
}

func (r *sessionReplay) prepare(t *testing.T, at time.Time) *preparedDesiredState {
	t.Helper()
	r.now = at
	prepared, err := r.e.f.prepareDesiredState(r.ctx, r.state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func replayPair(t *testing.T, label string, a, b *sessionReplay, event SessionEvent, prepare bool) (testTransition, testTransition) {
	t.Helper()
	eventA, eventB := event, event
	if prepare {
		eventA.Prepared = a.prepare(t, event.At)
		eventB.Prepared = b.prepare(t, event.At)
	}
	gotA, gotB := a.step(t, eventA), b.step(t, eventB)
	if len(gotA.Frames) != len(gotB.Frames) {
		t.Fatalf("%s frames differ in count: A=%d B=%d", label, len(gotA.Frames), len(gotB.Frames))
	}
	for i := range gotA.Frames {
		if !proto.Equal(gotA.Frames[i], gotB.Frames[i]) {
			t.Fatalf("%s frame %d differs: A=%v B=%v", label, i, gotA.Frames[i], gotB.Frames[i])
		}
	}
	if !reflect.DeepEqual(gotA.Effects, gotB.Effects) {
		t.Fatalf("%s effects differ: A=%+v B=%+v", label, gotA.Effects, gotB.Effects)
	}
	if !reflect.DeepEqual(gotA.Close, gotB.Close) {
		t.Fatalf("%s close differs: A=%+v B=%+v", label, gotA.Close, gotB.Close)
	}
	if (gotA.NextAlarm == nil) != (gotB.NextAlarm == nil) || gotA.NextAlarm != nil && !gotA.NextAlarm.Equal(*gotB.NextAlarm) {
		t.Fatalf("%s next alarm differs: A=%v B=%v", label, gotA.NextAlarm, gotB.NextAlarm)
	}
	stateA, _ := json.Marshal(a.state)
	stateB, _ := json.Marshal(b.state)
	if string(stateA) != string(stateB) {
		t.Fatalf("%s state differs after rehydration: A=%s B=%s", label, stateA, stateB)
	}
	if rowsA, rowsB := replayRows(t, a.e, a.state.NodeID), replayRows(t, b.e, b.state.NodeID); !reflect.DeepEqual(rowsA, rowsB) {
		t.Fatalf("%s stored rows differ: A=%v B=%v", label, rowsA, rowsB)
	}
	if len(a.warp.health) != len(b.warp.health) || !reflect.DeepEqual(a.warp.attention, b.warp.attention) {
		t.Fatalf("%s WARP writes differ: A=%d/%v B=%d/%v", label, len(a.warp.health), a.warp.attention, len(b.warp.health), b.warp.attention)
	}
	for i := range a.warp.health {
		if !proto.Equal(a.warp.health[i], b.warp.health[i]) {
			t.Fatalf("%s WARP report %d differs: A=%v B=%v", label, i, a.warp.health[i], b.warp.health[i])
		}
	}
	return gotA, gotB
}

func replayRows(t *testing.T, e *env, nodeID string) map[string]string {
	t.Helper()
	queries := map[string]struct {
		query string
		args  []any
	}{
		"node":      {`SELECT state, agent_version, api_version, agent_instance_id, last_seq, desired_revision, desired_hash, bandwidth_mbps, last_seen_at, last_connected_at FROM node WHERE id = ?`, []any{nodeID}},
		"facts":     {`SELECT hostname, os, kernel, arch, cpu_count, ram_total_bytes, disk_total_bytes, virt, has_ipv6, engines_json, updated_at FROM node_facts WHERE node_id = ?`, []any{nodeID}},
		"awg":       {`SELECT cert_pin_sha256, cert_not_after, awg_health_json, awg_health_at FROM inbound WHERE id = 'inb_awg'`, nil},
		"devices":   {`SELECT id, last_seen_at FROM device ORDER BY id`, nil},
		"node_sent": {`SELECT digest FROM node_sent WHERE node_id = ?`, []any{nodeID}},
		"node_live": {`SELECT session, drift, sample_at, rx_bps, tx_bps, cpu_pct, users, live_json FROM node_live WHERE node_id = ?`, []any{nodeID}},
		"warp":      {`SELECT attention, health_json, health_at, updated_at FROM warp_account WHERE node_id = ?`, []any{nodeID}},
		"events":    {`SELECT ts, severity, code, source, params_json, src_instance, src_seq FROM event WHERE node_id = ? ORDER BY id`, []any{nodeID}},
		"audit":     {`SELECT ts, actor, action, params, result FROM audit WHERE action = 'node.bandwidth_auto' ORDER BY id`, nil},
		"traffic":   {`SELECT protocol, hour_start, bytes_up, bytes_down, peak_users, peak_devices FROM node_traffic_hour WHERE node_id = ? ORDER BY protocol, hour_start`, []any{nodeID}},
	}
	out := make(map[string]string, len(queries))
	for name, item := range queries {
		rows, err := e.st.R.QueryContext(e.ctx, item.query, item.args...)
		if err != nil {
			t.Fatalf("read %s replay rows: %v", name, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		var result [][]string
		for rows.Next() {
			values, dest := make([]any, len(cols)), make([]any, len(cols))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			line := make([]string, len(cols))
			for i, value := range values {
				if b, ok := value.([]byte); ok {
					line[i] = string(b)
				} else if value != nil {
					line[i] = fmt.Sprint(value)
				}
			}
			result = append(result, line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
		b, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(b)
	}
	return out
}

func TestSessionCoreRehydratesStateBetweenEvents(t *testing.T) {
	a := newSessionReplay(t, "nod_replay", false)
	b := newSessionReplay(t, "nod_replay", true)
	base := a.now
	run := func(name string, event SessionEvent, prepare bool) (testTransition, testTransition) {
		t.Helper()
		return replayPair(t, name, a, b, event, prepare)
	}
	run("open", SessionEvent{Kind: EventOpen, At: base}, false)
	helloAt := base.Add(time.Second)
	h := hello("instance-replay", 0, "")
	h.GetHello().Capabilities = []string{capAWG, capBandwidth, capWarp, "doctor/1"}
	run("hello", SessionEvent{Kind: EventHello, At: helloAt, Frame: h}, false)
	run("initial desired changed", SessionEvent{Kind: EventDesiredChanged, At: helloAt}, false)
	run("initial desired prepared", SessionEvent{Kind: EventDesiredPrepared, At: helloAt}, true)

	autoAt := a.state.AutoBandwidthDeadline
	auto, _ := run("automatic bandwidth request", SessionEvent{Kind: EventAlarm, At: autoAt}, false)
	if len(auto.Frames) != 1 || auto.Frames[0].GetMeasureBandwidth().GetRequestId() != "auto-bandwidth" || len(auto.Effects) != 0 {
		t.Fatalf("automatic bandwidth transition = frames %+v effects %+v", auto.Frames, auto.Effects)
	}
	autoResultAt := autoAt.Add(time.Second)
	result := &agentv1.CommandResult{RequestId: "auto-bandwidth", Ok: true, Params: map[string]string{
		"down_mbps": "940", "up_mbps": "871", "server": "example.test", "seconds": "12", "runs": "3",
	}}
	run("automatic bandwidth result", SessionEvent{Kind: EventAgentFrame, At: autoResultAt,
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: result}}}, false)
	if node, err := a.e.st.Node(a.ctx, a.state.NodeID); err != nil || node.BandwidthMbps != 871 {
		t.Fatalf("rehydrated automatic capacity = %d, err %v; want 871", node.BandwidthMbps, err)
	}

	statsAt := autoResultAt.Add(time.Second)
	warpDown := &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_DOWN, LastError: "probe_other_failed", RxBytes: 10}
	warpUp := &agentv1.WarpHealth{State: agentv1.WarpState_WARP_STATE_UP, ProbeCloudflareOk: true, ProbeOtherOk: true, RxBytes: 20}
	stats := func(seq uint64, at time.Time, peers uint32, warp *agentv1.WarpHealth) (testTransition, testTransition) {
		t.Helper()
		frame := &agentv1.ConnectRequest{Seq: seq, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
			IntervalStartUnix: at.Add(-10 * time.Second).Unix(), IntervalEndUnix: at.Unix(),
			Health: []*agentv1.InboundHealth{{InboundId: "inb_awg", Awg: &agentv1.AwgHealth{Backend: "userspace", IfaceUp: true, Peers: 4, PeersHandshaken: 3, PeersOnline: peers}}},
			Warp:   warp,
		}}}
		return run(fmt.Sprintf("stats %d", seq), SessionEvent{Kind: EventAgentFrame, At: at, Frame: frame}, false)
	}
	first, _ := stats(1, statsAt, 1, warpDown)
	if len(first.Frames) != 1 || first.Frames[0].GetAck().GetUpToSeq() != 1 {
		t.Fatalf("first stats ack = %+v, want immediate ack 1", first.Frames)
	}
	for seq := uint64(2); seq <= 5; seq++ {
		got, _ := stats(seq, statsAt.Add(time.Duration(seq-1)*ackEvery/5), 1, warpDown)
		if len(got.Frames) != 0 {
			t.Fatalf("stats burst %d acked early: %+v", seq, got.Frames)
		}
	}
	burstAck, _ := run("stats burst alarm", SessionEvent{Kind: EventAlarm, At: statsAt.Add(ackEvery)}, false)
	if len(burstAck.Frames) != 1 || burstAck.Frames[0].GetAck().GetUpToSeq() != 5 || !burstAck.State.NextAckTick.IsZero() {
		t.Fatalf("stats burst ack = %+v, next tick %v; want ack 5 and no ack alarm", burstAck.Frames, burstAck.State.NextAckTick)
	}

	stats(6, base.Add(10*time.Second), 2, warpUp)  // AWG change waits; WARP state change writes urgently and clears attention.
	stats(7, base.Add(40*time.Second), 3, warpUp)  // AWG change passes the minimum gap.
	stats(8, base.Add(80*time.Second), 3, warpUp)  // Identical reports stay throttled.
	stats(9, base.Add(161*time.Second), 3, warpUp) // Both unchanged reports refresh after the gap.

	refreshEvent := func(seq uint64, at time.Time) SessionEvent {
		return SessionEvent{Kind: EventAgentFrame, At: at, Frame: &agentv1.ConnectRequest{Seq: seq,
			Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: eventWarpAttention, TimeUnix: at.Unix(),
				Params: map[string]string{"reason": warpReasonRefresh}}}}}
	}
	refresh1, _ := run("first WARP refresh request", refreshEvent(10, base.Add(162*time.Second)), false)
	if !hasEffect(refresh1, EffectWarpAttention) {
		t.Fatalf("first WARP refresh had no effect: %+v", refresh1.Effects)
	}
	refresh2, _ := run("throttled WARP refresh request", refreshEvent(11, base.Add(163*time.Second)), false)
	if hasEffect(refresh2, EffectWarpAttention) {
		t.Fatalf("second WARP refresh inside the gap emitted an effect: %+v", refresh2.Effects)
	}

	commandAt := base.Add(164 * time.Second)
	commandFrame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{RequestId: "admin-command"}}}
	run("admin command", SessionEvent{Kind: EventAdminCommand, At: commandAt, Request: &AdminRequest{
		RequestID: "admin-command", Deadline: commandAt.Add(10 * time.Second), Frame: commandFrame,
	}}, false)
	commandResult := &agentv1.CommandResult{RequestId: "admin-command", Ok: true}
	commandDone, _ := run("admin command result", SessionEvent{Kind: EventAgentFrame, At: commandAt.Add(time.Second),
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: commandResult}}}, false)
	if !hasEffect(commandDone, EffectReply) {
		t.Fatalf("admin command result was not routed: %+v", commandDone.Effects)
	}

	requestAt := base.Add(166 * time.Second)
	requestDeadline := base.Add(170 * time.Second)
	run("doctor request", SessionEvent{Kind: EventAdminCommand, At: requestAt, Request: &AdminRequest{
		RequestID: "doctor", Deadline: requestDeadline,
		Frame: &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{RunDoctor: &agentv1.RunDoctor{RequestId: "doctor"}}},
	}}, false)
	run("log request", SessionEvent{Kind: EventLogStart, At: requestAt, Request: &AdminRequest{
		RequestID: "log", Deadline: requestDeadline,
		Frame: &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_LogRequest{LogRequest: &agentv1.LogRequest{RequestId: "log"}}},
	}}, false)
	wrongKind, _ := run("doctor ignores command result", SessionEvent{Kind: EventAgentFrame, At: requestAt.Add(time.Second),
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{RequestId: "doctor", Ok: true}}}}, false)
	if len(wrongKind.State.Pending) != 2 {
		t.Fatalf("wrong result kind consumed a pending request: %+v", wrongKind.State.Pending)
	}
	expired, _ := run("doctor and log request expiry", SessionEvent{Kind: EventAlarm, At: requestDeadline}, false)
	if len(expired.State.Pending) != 0 {
		t.Fatalf("doctor or log request remained pending: %+v", expired.State.Pending)
	}

	a.desired.Listen.Port = 51821
	b.desired.Listen.Port = 51821
	changedAt := base.Add(171 * time.Second)
	changed, _ := run("desired changed", SessionEvent{Kind: EventDesiredChanged, At: changedAt}, false)
	if !hasEffect(changed, EffectPrepareDesired) {
		t.Fatalf("desired change did not request preparation: %+v", changed.Effects)
	}
	prepared, _ := run("desired prepared", SessionEvent{Kind: EventDesiredPrepared, At: changedAt}, true)
	if len(prepared.Frames) != 1 || prepared.Frames[0].GetDesiredState() == nil {
		t.Fatalf("desired preparation did not send a changed state: %+v", prepared.Frames)
	}

	certAt := a.state.NextCertCheck
	a.state.PeerCertSerial, b.state.PeerCertSerial = "missing-serial", "missing-serial"
	a.state.PeerCertNotAfter, b.state.PeerCertNotAfter = certAt.Add(time.Hour), certAt.Add(time.Hour)
	failedCert, _ := run("certificate recheck failure", SessionEvent{Kind: EventAgentFrame, At: certAt,
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: 1}}}}, false)
	if failedCert.Close == nil || failedCert.Close.Class != CloseUnauthenticated {
		t.Fatalf("certificate recheck close = %+v, want unauthenticated", failedCert.Close)
	}
}

func TestSessionCoreRehydratesLivenessTimeoutAndDisconnect(t *testing.T) {
	a := newSessionReplay(t, "nod_replaylive", false)
	b := newSessionReplay(t, "nod_replaylive", true)
	base := a.now
	run := func(name string, event SessionEvent, prepare bool) (testTransition, testTransition) {
		t.Helper()
		return replayPair(t, name, a, b, event, prepare)
	}
	run("open", SessionEvent{Kind: EventOpen, At: base}, false)
	helloAt := base.Add(time.Second)
	h := hello("instance-replay-live", 0, "")
	h.GetHello().Capabilities = []string{capAWG}
	run("hello", SessionEvent{Kind: EventHello, At: helloAt, Frame: h}, false)
	run("desired changed", SessionEvent{Kind: EventDesiredChanged, At: helloAt}, false)
	run("desired prepared", SessionEvent{Kind: EventDesiredPrepared, At: helloAt}, true)
	deadline := a.state.LivenessDeadline
	timedOut, _ := run("liveness timeout", SessionEvent{Kind: EventAlarm, At: deadline}, false)
	if timedOut.Close == nil || timedOut.Close.Class != CloseDeadline {
		t.Fatalf("liveness timeout close = %+v, want deadline", timedOut.Close)
	}
	disconnected, _ := run("disconnect", SessionEvent{Kind: EventDisconnected, At: deadline.Add(time.Second)}, false)
	if !disconnected.State.Disconnected || disconnected.NextAlarm != nil {
		t.Fatalf("disconnect state = %+v, next alarm %v", disconnected.State, disconnected.NextAlarm)
	}
}

func TestDisconnectStateChangesOnlyAfterStoreSucceeds(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-disconnect-write")
	connected := stepHello(t, core, ctx, state, now, hello("instance-disconnect-write", 0, "").GetHello())
	state = connected.State
	state.Pending = map[string]PendingRequest{"pending": {Kind: PendingAutoBandwidth, Deadline: now.Add(time.Minute)}}
	state.AutoBandwidthDeadline = now.Add(time.Minute)
	pending := map[string]PendingRequest{"pending": state.Pending["pending"]}
	autoDeadline := state.AutoBandwidthDeadline
	e.exec(`CREATE TRIGGER reject_core_disconnect BEFORE DELETE ON node_live BEGIN SELECT RAISE(ABORT, 'test refusal'); END`)

	_, err := core.Step(ctx, &state, SessionEvent{Kind: EventDisconnected, At: now})
	if err == nil {
		t.Fatal("EventDisconnected succeeded despite the store refusing the write")
	}
	if state.Disconnected || !reflect.DeepEqual(state.Pending, pending) || !state.AutoBandwidthDeadline.Equal(autoDeadline) {
		t.Fatalf("disconnect state changed after the store error: disconnected=%t pending=%v deadline=%v", state.Disconnected, state.Pending, state.AutoBandwidthDeadline)
	}
	rows, err := e.st.NodeLive(e.ctx, now, state.NodeID, false)
	if err != nil || len(rows) != 1 || !rows[0].Exists {
		t.Fatalf("failed disconnect changed node_live: %+v, %v", rows, err)
	}
}

func TestNodeLiveConnectedFollowsHelloAndClose(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-live-connect-close")
	e.f.now = func() time.Time { return now }
	e.f.cfg.Now = e.f.now
	if _, err := core.Step(ctx, &state, SessionEvent{Kind: EventHello, At: now,
		Frame: hello("instance-live-connect-close", 0, "")}); err != nil {
		t.Fatal(err)
	}
	rows, err := e.f.Live(ctx)
	if err != nil || len(rows) != 1 || !rows[0].Connected {
		t.Fatalf("node_live after Hello = %+v, %v; want connected", rows, err)
	}
	if _, err := core.Step(ctx, &state, SessionEvent{Kind: EventDisconnected, At: now.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	rows, err = e.f.Live(ctx)
	if err != nil || len(rows) != 1 || rows[0].Connected {
		t.Fatalf("node_live after close = %+v, %v; want offline", rows, err)
	}
}

func fireAlarmsThrough(t *testing.T, ctx context.Context, core *SessionCore, tr testTransition, target time.Time) testTransition {
	t.Helper()
	for i := 0; i < 10_000 && tr.Close == nil && tr.NextAlarm != nil && !tr.NextAlarm.After(target); i++ {
		at := *tr.NextAlarm
		var err error
		tr, err = coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAlarm, At: at})
		if err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

func stepHello(t *testing.T, core *SessionCore, ctx context.Context, state SessionState, now time.Time, h *agentv1.Hello) testTransition {
	t.Helper()
	frame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Hello{Hello: h}}
	started, err := coreStep(ctx, core, state, SessionEvent{Kind: EventHello, At: now, Frame: frame})
	if err != nil {
		t.Fatal(err)
	}
	if started.Close != nil {
		t.Fatalf("hello closed: %+v", started.Close)
	}
	requested, err := coreStep(ctx, core, started.State, SessionEvent{Kind: EventDesiredChanged, At: now})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := core.f.prepareDesiredState(ctx, requested.State.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredPrepared, At: now, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Close != nil {
		t.Fatalf("initial reconcile closed: %+v", tr.Close)
	}
	tr.Frames = append(started.Frames, tr.Frames...)
	tr.Effects = append(started.Effects, tr.Effects...)
	return tr
}

func reconcilePrepared(t *testing.T, e *env, core *SessionCore, ctx context.Context, tr testTransition, at time.Time) testTransition {
	t.Helper()
	requested := tr
	if !requested.State.Preparing {
		var err error
		requested, err = coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredChanged, At: at})
		if err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := e.f.prepareDesiredState(ctx, requested.State.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredPrepared, At: at, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	for _, effect := range tr.Effects {
		if effect.Kind != EffectPrepareDesired {
			updated.Effects = append([]SessionEffect{effect}, updated.Effects...)
		}
	}
	return updated
}

func TestSessionCoreHelloAndInitialState(t *testing.T) {
	t.Run("sends desired state when applied hash differs", func(t *testing.T) {
		_, core, ctx, state, now := coreFixture(t, "core-hello-send")
		tr := stepHello(t, core, ctx, state, now, hello("instance-1", 0, "").GetHello())
		if len(tr.Frames) != 2 || tr.Frames[0].GetHelloAck() == nil || tr.Frames[1].GetDesiredState() == nil {
			t.Fatalf("hello frames = %#v", tr.Frames)
		}
		if tr.State.SentRevision == 0 || tr.State.SentStateHash == "" {
			t.Fatalf("initial desired state not recorded: state=%+v", tr.State)
		}
		if !tr.State.NextAckTick.IsZero() || tr.NextAlarm == nil || !tr.NextAlarm.Equal(tr.State.LivenessDeadline) {
			t.Fatalf("hello alarm = %v, ack deadline = %v, liveness deadline = %v", tr.NextAlarm, tr.State.NextAckTick, tr.State.LivenessDeadline)
		}
	})

	t.Run("skips desired state when applied hash matches", func(t *testing.T) {
		e, core, ctx, state, now := coreFixture(t, "core-hello-skip")
		node, err := e.st.Node(ctx, state.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		want, err := e.f.buildState(ctx, node, nil)
		if err != nil {
			t.Fatal(err)
		}
		tr := stepHello(t, core, ctx, state, now, hello("instance-2", 7, want.hash).GetHello())
		if len(tr.Frames) != 1 || tr.Frames[0].GetHelloAck() == nil {
			t.Fatalf("matching applied state sent frames = %#v", tr.Frames)
		}
		if tr.State.SentRevision != 7 || tr.State.SentStateHash != want.hash {
			t.Fatalf("matching baseline not recorded: state=%+v", tr.State)
		}
	})
}

func TestSessionCoreReconnectUsesStoredSentDigest(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-reconnect-digest")
	e.fixture(state.NodeID)
	first := stepHello(t, core, ctx, state, now, hello("instance-before-reconnect", 0, "").GetHello())

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadline: now.Add(2 * time.Second).Add(helloTimeout)}
	reconnected, err := coreStep(newCtx, newCore, newState, SessionEvent{Kind: EventHello,
		At: now.Add(2 * time.Second), Frame: hello("instance-after-reconnect", first.State.SentRevision, first.State.SentStateHash)})
	if err != nil || reconnected.Close != nil {
		t.Fatalf("reconnect Hello = close %v, err %v", reconnected.Close, err)
	}
	requested, err := coreStep(newCtx, newCore, reconnected.State,
		SessionEvent{Kind: EventDesiredChanged, At: now.Add(3 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_alice'`)
	prepared, err := e.f.prepareDesiredState(newCtx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(newCtx, newCore, requested.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(3 * time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Frames) != 1 || updated.Frames[0].GetDesiredState() == nil {
		t.Fatalf("reconnect desired frames = %#v", updated.Frames)
	}
	delta := updated.Frames[0].GetDesiredState()
	if delta.BaseRevision != first.State.SentRevision || len(delta.Inbounds) == 0 {
		t.Fatalf("reconnect change = %+v, want delta based on revision %d", delta, first.State.SentRevision)
	}
}

func TestSessionCoreReconnectHelloMismatchSendsFullState(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-reconnect-mismatch")
	e.fixture(state.NodeID)
	first := stepHello(t, core, ctx, state, now, hello("instance-before-mismatch", 0, "").GetHello())
	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadline: now.Add(2 * time.Second).Add(helloTimeout)}
	reconnected, err := coreStep(newCtx, newCore, newState, SessionEvent{Kind: EventHello,
		At: now.Add(2 * time.Second), Frame: hello("instance-mismatch", first.State.SentRevision, "different-hash")})
	if err != nil || reconnected.Close != nil {
		t.Fatalf("mismatched Hello = close %v, err %v", reconnected.Close, err)
	}
	requested, err := coreStep(newCtx, newCore, reconnected.State,
		SessionEvent{Kind: EventDesiredChanged, At: now.Add(3 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(newCtx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(newCtx, newCore, requested.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(3 * time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Frames) != 1 || updated.Frames[0].GetDesiredState() == nil || updated.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("mismatched Hello state = %#v, want full resend after revision %d", updated.Frames, first.State.SentRevision)
	}
}

func TestSessionCoreOlderSentDigestSendsFullState(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-stale-digest")
	e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, now, hello("instance-stale-digest", 0, "").GetHello())
	_, rawDigest, err := e.st.NodeWithSentDigest(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var digest sentDigest
	if err := json.Unmarshal(rawDigest, &digest); err != nil {
		t.Fatal(err)
	}
	digest.Revision = connected.State.SentRevision - 1
	rawDigest, err = json.Marshal(digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.NodeDesired(ctx, state.NodeID, digest.Revision, digest.Hash, rawDigest); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_alice'`)
	requested, err := coreStep(ctx, core, connected.State,
		SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Frames) != 1 || updated.Frames[0].GetDesiredState() == nil || updated.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("older digest state = %#v, want full resend after revision %d", updated.Frames, connected.State.SentRevision)
	}
}

func TestSessionCoreSentDigestHashMismatchSendsFullState(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-hash-mismatch")
	e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, now, hello("instance-digest-hash", 0, "").GetHello())
	requested, err := coreStep(ctx, core, connected.State,
		SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.digest == nil || prepared.digest.Revision != requested.State.SentRevision {
		t.Fatalf("prepared digest = %+v, session sent revision = %d", prepared.digest, requested.State.SentRevision)
	}
	// A same-revision write from another session can land before an older session's digest.
	prepared.digest.Hash = "another-session-state"
	updated, err := coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Frames) != 1 || updated.Frames[0].GetDesiredState() == nil || updated.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("same-revision digest with another hash = %#v, want a full state", updated.Frames)
	}
}

func TestSessionCoreRetiredNodeDoesNotSendDesiredState(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-retired-desired")
	e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, now, hello("instance-retired-desired", 0, "").GetHello())
	if err := e.st.RetireNode(ctx, state.NodeID, now); err != nil {
		t.Fatal(err)
	}
	requested, err := coreStep(ctx, core, connected.State,
		SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range updated.Frames {
		if frame.GetDesiredState() != nil {
			t.Fatalf("retired node received a desired state: %+v", frame.GetDesiredState())
		}
	}
	if updated.State.SentRevision != connected.State.SentRevision {
		t.Fatalf("retired node sent revision %d, want %d", updated.State.SentRevision, connected.State.SentRevision)
	}
}

func TestSessionCoreKeepsMonotonicLivenessDeadline(t *testing.T) {
	_, core, ctx, state, _ := coreFixture(t, "core-monotonic-live")
	state.HelloDeadline = time.Time{}
	now := time.Now()
	if _, err := core.Step(ctx, &state, SessionEvent{Kind: EventOpen, At: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(ctx, &state, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-monotonic", 0, "")}); err != nil {
		t.Fatal(err)
	}
	if state.LivenessDeadline.IsZero() || state.LivenessDeadline == state.LivenessDeadline.Round(0) {
		t.Fatalf("liveness deadline lost its monotonic reading: %v", state.LivenessDeadline)
	}
	tr := nextSessionAlarm(state, now)
	if tr == nil || *tr == tr.Round(0) {
		t.Fatalf("next alarm lost its monotonic reading: %v", tr)
	}
	tick, err := core.Step(ctx, &state, SessionEvent{Kind: EventAlarm, At: now.Add(ackEvery)})
	if err != nil || tick.Close != nil || deadlineDue(state.LivenessDeadline, now.Add(ackEvery)) {
		t.Fatalf("early alarm closed the live session: close=%+v err=%v", tick.Close, err)
	}
}

func TestSessionCoreHelloDeadlineIsDecidedByAlarm(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "core-hello-deadline")
	opened, err := coreStep(ctx, core, state, SessionEvent{Kind: EventOpen, At: now})
	if err != nil {
		t.Fatal(err)
	}
	if opened.NextAlarm == nil || !opened.NextAlarm.Equal(opened.State.HelloDeadline) {
		t.Fatalf("open next alarm = %v, hello deadline = %v", opened.NextAlarm, opened.State.HelloDeadline)
	}
	tr, err := coreStep(ctx, core, opened.State, SessionEvent{Kind: EventAlarm, At: *opened.NextAlarm})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Close == nil || tr.Close.Class != CloseDeadline {
		t.Fatalf("hello alarm = %+v, want a deadline close", tr.Close)
	}
}

func TestSessionCoreAutoBandwidthAlarm(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "core-auto-bandwidth")
	h := hello("instance-auto-bandwidth", 0, "")
	h.GetHello().Capabilities = []string{capBandwidth}
	tr := stepHello(t, core, ctx, state, now, h.GetHello())
	want := now.Add(core.f.measureDelay)
	if !tr.State.AutoBandwidthDeadline.Equal(want) {
		t.Fatalf("auto-bandwidth deadline = %v, want %v", tr.State.AutoBandwidthDeadline, want)
	}
	deadline := want
	var due testTransition
	var fired bool
	for i := 0; i < 10_000 && tr.NextAlarm != nil && !tr.NextAlarm.After(deadline); i++ {
		at := *tr.NextAlarm
		next, stepErr := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAlarm, At: at})
		if stepErr != nil {
			t.Fatal(stepErr)
		}
		tr = next
		if len(next.Frames) == 1 && next.Frames[0].GetMeasureBandwidth() != nil {
			due = next
			fired = true
			break
		}
	}
	if !fired || !due.State.AutoBandwidthDeadline.IsZero() || due.Frames[0].GetMeasureBandwidth().RequestId != "auto-bandwidth" {
		t.Fatalf("due auto-bandwidth alarm = frames %+v state=%+v", due.Frames, due.State)
	}
	request, ok := due.State.Pending["auto-bandwidth"]
	if !ok || request.Kind != PendingAutoBandwidth || !request.Deadline.Equal(deadline.Add(core.f.measureWait)) {
		t.Fatalf("auto-bandwidth pending request = %+v, want deadline %v", request, deadline.Add(core.f.measureWait))
	}
	again, err := coreStep(ctx, core, due.State, SessionEvent{Kind: EventAlarm, At: request.Deadline})
	if err != nil || len(again.State.Pending) != 0 || len(again.Frames) != 0 {
		t.Fatalf("unanswered auto-bandwidth request did not expire: pending=%+v frames=%+v err=%v", again.State.Pending, again.Frames, err)
	}
}

func TestAutoBandwidthDeadlineStartsInHello(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-bw-start")
	h := hello("instance-auto-bandwidth-start", 0, "")
	h.GetHello().Capabilities = []string{capBandwidth}
	helloTransition, err := coreStep(ctx, core, state, SessionEvent{Kind: EventHello, At: now, Frame: h})
	if err != nil {
		t.Fatal(err)
	}
	want := now.Add(e.f.measureDelay)
	if !helloTransition.State.AutoBandwidthDeadline.Equal(want) {
		t.Fatalf("auto-bandwidth deadline = %v, want %v", helloTransition.State.AutoBandwidthDeadline, want)
	}
}

func TestSessionCoreAutoBandwidthResultStoresCapacityInline(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-auto-bw-result")
	e.f.measureDelay = 2 * time.Second
	e.f.measureWait = 3 * time.Second
	h := hello("instance-auto-bandwidth-result", 0, "")
	h.GetHello().Capabilities = []string{capBandwidth}
	started := stepHello(t, core, ctx, state, now, h.GetHello())
	due, err := coreStep(ctx, core, started.State, SessionEvent{Kind: EventAlarm, At: started.State.AutoBandwidthDeadline})
	if err != nil {
		t.Fatal(err)
	}
	if len(due.Frames) != 1 || due.Frames[0].GetMeasureBandwidth().GetRequestId() != "auto-bandwidth" || len(due.Effects) != 0 {
		t.Fatalf("automatic measurement request = frames %+v effects %+v", due.Frames, due.Effects)
	}
	result := &agentv1.CommandResult{RequestId: "auto-bandwidth", Ok: true, Params: map[string]string{
		"down_mbps": "940", "up_mbps": "871", "server": "example.test", "seconds": "12", "runs": "3",
	}}
	stored, err := coreStep(ctx, core, due.State, SessionEvent{Kind: EventAgentFrame, At: due.State.AutoBandwidthDeadline.Add(100 * time.Millisecond),
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: result}}})
	if err != nil || len(stored.Effects) != 0 {
		t.Fatalf("automatic measurement result = effects %+v, err %v", stored.Effects, err)
	}
	node, err := e.st.Node(ctx, state.NodeID)
	if err != nil || node.BandwidthMbps != 871 {
		t.Fatalf("stored capacity = %d, err %v; want slower direction 871", node.BandwidthMbps, err)
	}
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'bandwidth_measured'`, state.NodeID); got != 1 {
		t.Fatalf("bandwidth measurement events = %d, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM audit WHERE actor = 'system' AND action = 'node.bandwidth_auto'`); got != 1 {
		t.Fatalf("automatic bandwidth audit rows = %d, want 1", got)
	}
}

func TestSessionStepErrorCancelsSession(t *testing.T) {
	e, core, ctx, state, _ := coreFixture(t, "core-step-error")
	s := newTestSession(e, ctx, core, state)
	tr, err := s.stepCore(s.ctx, SessionEvent{Kind: EventKind(255), At: time.Now()})
	if err == nil || tr.NextAlarm != nil {
		t.Fatalf("failed Step transition = %+v, %v", tr, err)
	}
	if cause := context.Cause(s.ctx); cause == nil || code(cause) != connect.CodeInternal || errOrCause(s.ctx) != cause {
		t.Fatalf("Step error cause = %v, want an internal connect error", cause)
	}
}

// A full out queue ends the session with ResourceExhausted on every branch of the loop, not only the frame branch.
func TestRunSessionEndsWithResourceExhaustedWhenQueueIsFull(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("queue-full-core")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, ownerCtx := e.f.claimOwner(a.nodeID, ctx)
	stream := &refillingAgentStream{manualAgentSessionStream: manualAgentSessionStream{ctx: ownerCtx,
		in: make(chan *agentv1.ConnectRequest, 2), out: make(chan *agentv1.ConnectResponse, 16)}}
	done := make(chan error, 1)
	go func() {
		done <- (agentService{e.f}).runSession(ownerCtx, a.nodeID, peerCert{serial: a.leaf.SerialNumber.Text(16), notAfter: a.leaf.NotAfter}, owner, stream)
	}()
	stream.in <- hello("instance-queue-full", 0, "")
	waitSessionAck(t, stream.out, 0)
	s := e.f.session(a.nodeID)
	if s == nil {
		t.Fatal("session was not registered after HelloAck")
	}
	stream.full.Store(s)
	for s.enqueue(&agentv1.ConnectResponse{}) {
	}
	stream.in <- &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "core_test"}}}
	select {
	case err := <-done:
		if code(err) != connect.CodeResourceExhausted {
			t.Fatalf("session ended with %v, want ResourceExhausted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not end on a full queue")
	}
}

func TestSlowDesiredPreparationDoesNotBlockStatsOrLiveness(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-slow-desired")
	helloTransition := stepHello(t, core, ctx, state, now, hello("instance-slow-desired", 0, "").GetHello())
	s := newTestSession(e, ctx, core, helloTransition.State)

	originalDesired := e.f.cfg.Desired
	entered, release := make(chan struct{}), make(chan struct{})
	reconcileDone := make(chan struct{})
	type stepResult struct {
		transition Transition
		err        error
	}
	stepDone := make(chan stepResult, 1)
	stepStarted, stepFinished := false, false
	released := false
	reconcileFinished := false
	defer func() {
		if !reconcileFinished {
			if !released {
				close(release)
			}
			select {
			case <-reconcileDone:
			case <-time.After(5 * time.Second):
				t.Error("desired-state preparation did not stop")
			}
		}
		if stepStarted && !stepFinished {
			select {
			case <-stepDone:
			case <-time.After(5 * time.Second):
				t.Error("stats frame step did not stop")
			}
		}
	}()
	e.f.cfg.Desired = func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
		close(entered)
		<-release
		return originalDesired(ctx, nodeID)
	}
	go func() {
		e.f.reconcile(e.ctx, s)
		close(reconcileDone)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("desired-state builder did not block")
	}
	oldDeadline := s.coreState.LivenessDeadline
	frameAt := now.Add(time.Duration(s.coreState.LivenessNanos) - time.Second)
	stats := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: frameAt.Add(-10 * time.Second).Unix(), IntervalEndUnix: frameAt.Unix(),
	}}}
	stepStarted = true
	go func() {
		transition, err := s.stepCore(s.ctx, SessionEvent{Kind: EventAgentFrame, At: frameAt, Frame: stats})
		stepDone <- stepResult{transition: transition, err: err}
	}()
	var transition Transition
	var err error
	select {
	case result := <-stepDone:
		stepFinished = true
		transition, err = result.transition, result.err
	case <-time.After(5 * time.Second):
		t.Fatal("stats frame did not remain responsive while desired-state preparation was blocked")
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-s.out:
		if response.GetAck().GetUpToSeq() != 1 {
			t.Fatalf("stats ack = %+v", response.GetAck())
		}
	default:
		t.Fatal("stats frame was not acked while desired-state preparation was blocked")
	}
	if transition.Close != nil || !s.coreState.LivenessDeadline.After(oldDeadline) {
		t.Fatalf("stats did not reset liveness: close=%+v state=%+v", transition.Close, s.coreState)
	}
	liveness, err := s.stepCore(s.ctx, SessionEvent{Kind: EventAlarm, At: oldDeadline})
	if err != nil || liveness.Close != nil {
		t.Fatalf("old liveness deadline closed the session: close=%+v err=%v", liveness.Close, err)
	}

	close(release)
	released = true
	select {
	case <-reconcileDone:
		reconcileFinished = true
	case <-time.After(5 * time.Second):
		t.Fatal("desired-state preparation did not finish")
	}
	if s.ctx.Err() != nil || s.coreState.Preparing {
		t.Fatalf("reconcile left the session ended or preparing: cause=%v state=%+v", context.Cause(s.ctx), s.coreState)
	}
}

func TestSessionCoreLivenessTimeoutChangeTakesEffectOnNextFrame(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-liveness-change")
	connected := stepHello(t, core, ctx, state, now, hello("instance-liveness-change", 0, "").GetHello())
	oldDeadline := connected.State.LivenessDeadline
	e.exec(`UPDATE node SET liveness_timeout_s = 15 WHERE id = ?`, state.NodeID)
	updated := reconcilePrepared(t, e, core, ctx, connected, now.Add(5*time.Second))
	if updated.State.LivenessNanos != int64(15*time.Second) || !updated.State.LivenessDeadline.Equal(oldDeadline) {
		t.Fatalf("timeout change moved the current deadline: timeout=%v deadline=%v, want 15s and %v",
			time.Duration(updated.State.LivenessNanos), updated.State.LivenessDeadline, oldDeadline)
	}
	beforeNextFrame := fireAlarmsThrough(t, ctx, core, updated, now.Add(16*time.Second))
	if beforeNextFrame.Close != nil {
		t.Fatalf("new timeout closed the session before another frame: %+v", beforeNextFrame.Close)
	}
	frameAt := now.Add(20 * time.Second)
	frame, err := coreStep(ctx, core, beforeNextFrame.State, SessionEvent{Kind: EventAgentFrame, At: frameAt,
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	wantDeadline := frameAt.Add(15 * time.Second)
	if !frame.State.LivenessDeadline.Equal(wantDeadline) {
		t.Fatalf("next frame deadline = %v, want %v", frame.State.LivenessDeadline, wantDeadline)
	}
	expired := fireAlarmsThrough(t, ctx, core, frame, wantDeadline)
	if expired.Close == nil || expired.Close.Class != CloseDeadline {
		t.Fatalf("new liveness deadline alarm = %+v, want deadline close", expired.Close)
	}
}

func TestSessionCoreAckCoalescingAndImmediateFlush(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "core-ack")
	tr := stepHello(t, core, ctx, state, now, hello("instance-ack", 0, "").GetHello())
	state = tr.State
	batch := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
	}}}
	tr, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now, Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 1 || tr.Frames[0].GetAck().GetUpToSeq() != 1 || tr.State.AckPending != 1 || tr.State.AckSent != 1 {
		t.Fatalf("first ack should flush immediately: state=%+v frames=%#v", tr.State, tr.Frames)
	}
	if !tr.State.NextAckTick.IsZero() {
		t.Fatalf("first ack left an alarm: %v", tr.State.NextAckTick)
	}
	state = tr.State
	for seq := uint64(2); seq <= 5; seq++ {
		batch.Seq = seq
		at := now.Add(time.Duration(seq-1) * ackEvery / 5)
		tr, err = coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: at, Frame: batch})
		if err != nil {
			t.Fatal(err)
		}
		if len(tr.Frames) != 0 {
			t.Fatalf("batch %d flushed inside the ack interval: %#v", seq, tr.Frames)
		}
		state = tr.State
	}
	if !state.NextAckTick.Equal(now.Add(ackEvery)) {
		t.Fatalf("coalesced ack deadline = %v, want %v", state.NextAckTick, now.Add(ackEvery))
	}
	tr, err = coreStep(ctx, core, state, SessionEvent{Kind: EventAlarm, At: state.NextAckTick})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 1 || tr.Frames[0].GetAck().GetUpToSeq() != 5 || !tr.State.NextAckTick.IsZero() || tr.NextAlarm == nil || !tr.NextAlarm.Equal(tr.State.LivenessDeadline) {
		t.Fatalf("coalesced alarm ack = %#v, tick=%v", tr.Frames, tr.State.NextAckTick)
	}
	state = tr.State
	batch.Seq = 6
	tr, err = coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now.Add(2 * ackEvery), Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 1 || tr.Frames[0].GetAck().GetUpToSeq() != 6 {
		t.Fatalf("next ack did not flush immediately: %#v", tr.Frames)
	}
}

func TestSessionCoreIdleAlarmTracksLivenessOnly(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "core-idle-alarm")
	tr := stepHello(t, core, ctx, state, now, hello("instance-idle-alarm", 0, "").GetHello())
	stats := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
	}}}
	acked, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: stats})
	if err != nil || len(acked.Frames) != 1 || acked.Frames[0].GetAck().GetUpToSeq() != 1 {
		t.Fatalf("stats ack = frames %+v, err %v", acked.Frames, err)
	}
	if !acked.State.NextAckTick.IsZero() || acked.State.NextCertCheck.IsZero() || acked.NextAlarm == nil || !acked.NextAlarm.Equal(acked.State.LivenessDeadline) {
		t.Fatalf("idle deadlines: next=%v liveness=%v ack=%v cert=%v", acked.NextAlarm, acked.State.LivenessDeadline, acked.State.NextAckTick, acked.State.NextCertCheck)
	}
}

func TestSessionCoreDuplicateStatsAndEvents(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-dedupe")
	tr := stepHello(t, core, ctx, state, now, hello("instance-dedupe", 0, "").GetHello())
	state = tr.State
	stats := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Host: &agentv1.HostMetrics{CpuPct: 12.5},
	}}}
	first, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now, Frame: stats})
	if err != nil {
		t.Fatal(err)
	}
	second, err := coreStep(ctx, core, first.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: stats})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.NodeLive(e.ctx, now.Add(time.Second), state.NodeID, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("NodeLive after duplicate stats = %+v, %v", rows, err)
	}
	var live liveJSONView
	if err := json.Unmarshal([]byte(rows[0].LiveJSON), &live); err != nil || live.Metrics == nil || live.Metrics.CPUPct != 12.5 {
		t.Fatalf("stats did not update the live projection: %+v, %v", live, err)
	}
	if len(first.Frames) != 1 || len(second.Frames) != 0 {
		t.Fatalf("stats acknowledgements: first=%#v second=%#v", first.Frames, second.Frames)
	}

	ev := &agentv1.ConnectRequest{Seq: 2, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "core_test"}}}
	first, err = coreStep(ctx, core, second.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(2 * time.Second), Frame: ev})
	if err != nil {
		t.Fatal(err)
	}
	second, err = coreStep(ctx, core, first.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(3 * time.Second), Frame: ev})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Frames) != 1 || first.Frames[0].GetAck().GetUpToSeq() != 2 || len(second.Frames) != 0 {
		t.Fatalf("event acknowledgements: first=%#v second=%#v", first.Frames, second.Frames)
	}
}

func TestNodeLiveProjectionBoundsAndSanitizesStats(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-live-bounds")
	ids := e.fixture(state.NodeID)
	tr := stepHello(t, core, ctx, state, now, hello("instance-live-bounds", 0, "").GetHello())
	e.f.cfg.MaxNodeBytesPerSec = 1 << 54
	stats := &agentv1.StatsBatch{IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Traffic:  []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 1 << 51, BytesDown: 1 << 51}},
		Sessions: make([]*agentv1.Session, 2001), Health: make([]*agentv1.InboundHealth, 65),
		Host: &agentv1.HostMetrics{CpuPct: 150, SoftirqPct: float32(math.NaN()), Load1: float32(math.Inf(1)),
			RamTotalBytes: 1 << 63, DiskTotalBytes: 1<<63 + 1, NetRxBps: 1 << 63, NetTxBps: ^uint64(0)},
	}
	for i := range stats.Sessions {
		stats.Sessions[i] = &agentv1.Session{CredId: "crd_alice_hy", InboundId: ids.i1, ConnectedAtUnix: now.Add(-time.Minute).Unix()}
	}
	for i := range stats.Health {
		stats.Health[i] = &agentv1.InboundHealth{InboundId: strings.Repeat("inbound-id-", 8),
			State: agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING, Detail: strings.Repeat("detail", 60)}
	}
	frame := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: stats}}
	tr, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: frame})
	if err != nil {
		t.Fatalf("hostile stats batch failed: %v", err)
	}
	rows, err := e.st.NodeLive(e.ctx, now, state.NodeID, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("NodeLive() = %+v, %v", rows, err)
	}
	row := rows[0]
	var got liveJSONView
	if err := json.Unmarshal([]byte(row.LiveJSON), &got); err != nil {
		t.Fatalf("decode live_json: %v", err)
	}
	if len(got.Online) != 2000 || len(got.Health) != 64 {
		t.Fatalf("live bounds: projection sessions=%d health=%d", len(got.Online), len(got.Health))
	}
	if len(got.Health[0].InboundID) > 64 || len(got.Health[0].Detail) > 256 {
		t.Fatalf("health fields exceeded bounds: %+v", got.Health[0])
	}
	if got.Metrics == nil || got.Metrics.CPUPct != 100 || got.Metrics.SoftirqPct != 0 || got.Metrics.Load1 != 0 ||
		row.CPUPct != 100 || row.RxBps != 1<<50 || row.TxBps != 1<<50 || got.Metrics.RxBPS != 1<<50 || got.Metrics.TxBPS != 1<<50 {
		t.Fatalf("sanitized metrics = %+v, hot columns cpu/rx/tx=%d/%d/%d", got.Metrics, row.CPUPct, row.RxBps, row.TxBps)
	}
	if got.Metrics.RAMTotalBytes != 1<<63 || got.Metrics.DiskTotalBytes != 1<<63+1 {
		t.Fatalf("large metrics did not survive JSON encoding: %+v", got.Metrics)
	}
	if got.UserDownBPS["usr_alice"] != 1<<50 || got.UploadBPS != 1<<50 {
		t.Fatalf("traffic rates were not clamped: down=%v upload=%d", got.UserDownBPS, got.UploadBPS)
	}
	var users map[string]int64
	if err := json.Unmarshal([]byte(row.UsersJSON), &users); err != nil || len(users) != 1 || users["usr_alice"] != now.Add(-time.Minute).Unix() {
		t.Fatalf("users column = %v, %v", users, err)
	}
	if strings.Contains(row.LiveJSON, "remote_ip") || strings.Contains(row.LiveJSON, "client_ip") {
		t.Fatalf("live_json contains an IP field: %s", row.LiveJSON)
	}
}

func TestBuildFleetLiveIsPureDeterministicAndClipsOnlyAgentFields(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	userID, deviceID, protocol := strings.Repeat("u", 70), strings.Repeat("d", 70), strings.Repeat("p", 70)
	inboundID, detail := strings.Repeat("i", 80), strings.Repeat("x", 300)
	state := SessionState{Version: sessionStateVersion, NodeID: "nod_projection_pure", OwnerGeneration: 9,
		LastEndUnix: now.Add(-time.Minute).Unix(), Pending: map[string]PendingRequest{"pending": {Kind: PendingDoctor}}}
	stateBefore := state
	stateBefore.Pending = map[string]PendingRequest{"pending": state.Pending["pending"]}
	stats := &agentv1.StatsBatch{IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Sessions: []*agentv1.Session{{CredId: "credential", InboundId: inboundID, ConnectedAtUnix: now.Add(-time.Minute).Unix()}},
		Health:   []*agentv1.InboundHealth{{InboundId: inboundID, Detail: detail, State: agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING}}}
	traffic := []store.FleetTraffic{{CredID: "credential", InboundID: inboundID, Down: 20, Up: 10}}
	refs := map[string]store.FleetCredRef{"credential": {UserID: userID, DeviceID: deviceID, Protocol: protocol}}

	first, _, firstTooLarge := buildFleetLive(state, stats, traffic, now, refs)
	second, _, secondTooLarge := buildFleetLive(state, stats, traffic, now, refs)
	if firstTooLarge || secondTooLarge || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated buildFleetLive results differ: first=%+v second=%+v", first, second)
	}
	if !reflect.DeepEqual(state, stateBefore) {
		t.Fatalf("buildFleetLive mutated its state: %+v", state)
	}
	var view liveJSONView
	if err := json.Unmarshal([]byte(first.LiveJSON), &view); err != nil {
		t.Fatalf("decode live_json: %v", err)
	}
	if len(view.Online) != 1 || view.Online[0].UserID != userID || view.Online[0].DeviceID != deviceID || view.Online[0].Protocol != protocol ||
		len(view.Online[0].InboundID) != 64 || len(view.Health[0].InboundID) != 64 || len(view.Health[0].Detail) != 256 {
		t.Fatalf("projected database/agent fields have wrong clipping: online=%+v health=%+v", view.Online, view.Health)
	}
	if _, ok := view.UserDownBPS[userID]; !ok {
		t.Fatalf("database user id was clipped in per-user rates: %v", view.UserDownBPS)
	}
}

func TestStatsWriteCompleteLiveProjection(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-live-complete")
	ids := e.fixture(state.NodeID)
	tr := stepHello(t, core, ctx, state, now, hello("instance-live-complete", 0, "").GetHello())
	stats := &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Traffic: []*agentv1.TrafficDelta{
			{CredId: "crd_alice_hy", InboundId: ids.i1, BytesDown: 100, BytesUp: 50},
			{CredId: "crd_alice_wg", InboundId: ids.i2, BytesDown: 20, BytesUp: 10},
		},
		Sessions: []*agentv1.Session{
			{CredId: "crd_alice_hy", InboundId: ids.i1, ConnectedAtUnix: now.Add(-time.Minute).Unix()},
			{CredId: "crd_alice_wg", InboundId: ids.i2, ConnectedAtUnix: now.Add(-30 * time.Second).Unix()},
		},
		Health: []*agentv1.InboundHealth{{InboundId: ids.i1, State: agentv1.InboundRunState_INBOUND_RUN_STATE_RUNNING, Detail: "healthy"}},
		Host:   &agentv1.HostMetrics{CpuPct: 42.5, NetRxBps: 101, NetTxBps: 202},
	}
	if _, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: &agentv1.ConnectRequest{
		Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: stats}}}); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.NodeLive(e.ctx, now, state.NodeID, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("NodeLive() = %+v, %v", rows, err)
	}
	row := rows[0]
	if row.SampleAt != now.Unix() || row.RxBps != 101 || row.TxBps != 202 || row.CPUPct != 43 {
		t.Fatalf("live projection columns: sample=%d rx=%d tx=%d cpu=%d", row.SampleAt, row.RxBps, row.TxBps, row.CPUPct)
	}
	var users map[string]int64
	if err := json.Unmarshal([]byte(row.UsersJSON), &users); err != nil || len(users) != 1 || users["usr_alice"] != now.Add(-30*time.Second).Unix() {
		t.Fatalf("users column = %v, %v", users, err)
	}
	var view liveJSONView
	if err := json.Unmarshal([]byte(row.LiveJSON), &view); err != nil {
		t.Fatalf("decode live_json: %v", err)
	}
	if len(view.Online) != 2 || view.UserDownBPS["usr_alice"] != 96 || view.UploadBPS != 48 ||
		len(view.Health) != 1 || view.Health[0].InboundID != ids.i1 || view.Health[0].RunState != "active" || view.Health[0].Detail != "healthy" {
		t.Fatalf("live_json projection: %+v", view)
	}
}

func TestBuildFleetLiveStopsBeforeSessionAtMaxStatsDeltas(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	sessions := make([]*agentv1.Session, maxStatsDeltas+1)
	for i := range maxStatsDeltas {
		sessions[i] = &agentv1.Session{CredId: "unknown", ConnectedAtUnix: now.Unix()}
	}
	stats := &agentv1.StatsBatch{IntervalStartUnix: now.Add(-time.Second).Unix(), IntervalEndUnix: now.Unix(), Sessions: sessions}
	projection, _, tooLarge := buildFleetLive(SessionState{}, stats, nil, now, nil)
	if tooLarge || !projection.Apply {
		t.Fatalf("projection = %+v, tooLarge=%t", projection, tooLarge)
	}
}

func TestBuildFleetLiveSkipsOversizedProjection(t *testing.T) {
	const sessionsN = 40_000
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	stats := &agentv1.StatsBatch{IntervalStartUnix: now.Add(-time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Sessions: make([]*agentv1.Session, sessionsN)}
	refs := make(map[string]store.FleetCredRef, sessionsN)
	for i := range sessionsN {
		credID := fmt.Sprintf("cred_%05d", i)
		stats.Sessions[i] = &agentv1.Session{CredId: credID, InboundId: "inb_live_limit", ConnectedAtUnix: now.Unix()}
		refs[credID] = store.FleetCredRef{UserID: fmt.Sprintf("usr_%s_%05d", strings.Repeat("u", 40), i), DeviceID: "dev", Protocol: "awg"}
	}
	projection, _, tooLarge := buildFleetLive(SessionState{}, stats, nil, now, refs)
	if !tooLarge || projection.Apply || projection.UsersJSON != "" || projection.LiveJSON != "" {
		t.Fatalf("oversized projection was applied: users=%d live=%d apply=%t", len(projection.UsersJSON), len(projection.LiveJSON), projection.Apply)
	}
}

func TestOlderStatsBatchLeavesProjectionUnchanged(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-live-older")
	ids := e.fixture(state.NodeID)
	tr := stepHello(t, core, ctx, state, now, hello("instance-live-older", 0, "").GetHello())
	newer := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Traffic: []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesDown: 20}},
		Host:    &agentv1.HostMetrics{CpuPct: 33, NetRxBps: 44},
	}}}
	first, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: newer})
	if err != nil {
		t.Fatal(err)
	}
	beforeRows, err := e.st.NodeLive(e.ctx, now, state.NodeID, true)
	if err != nil || len(beforeRows) != 1 {
		t.Fatalf("NodeLive before older batch = %+v, %v", beforeRows, err)
	}
	older := &agentv1.ConnectRequest{Seq: 2, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-20 * time.Second).Unix(), IntervalEndUnix: now.Add(-time.Second).Unix(),
		Host: &agentv1.HostMetrics{CpuPct: 99, NetRxBps: 999},
	}}}
	second, err := coreStep(ctx, core, first.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: older})
	if err != nil {
		t.Fatal(err)
	}
	afterRows, err := e.st.NodeLive(e.ctx, now, state.NodeID, true)
	if err != nil || !reflect.DeepEqual(beforeRows, afterRows) {
		t.Fatalf("older batch changed node_live: before=%+v after=%+v err=%v", beforeRows, afterRows, err)
	}
	if second.State.LastEndUnix != first.State.LastEndUnix {
		t.Fatalf("older batch changed core state: end=%d/%d", first.State.LastEndUnix, second.State.LastEndUnix)
	}
}

// The host metrics come from the batch itself (every agent batch carries them): a batch without them shows none, and an
// unknown credential never appears.
func TestStatsWithoutHostAndUnknownCredential(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-live-unknown")
	ids := e.fixture(state.NodeID)
	tr := stepHello(t, core, ctx, state, now, hello("instance-live-unknown", 0, "").GetHello())
	first, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: &agentv1.ConnectRequest{
		Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{IntervalStartUnix: now.Add(-10 * time.Second).Unix(),
			IntervalEndUnix: now.Unix(), Host: &agentv1.HostMetrics{CpuPct: 37, NetRxBps: 123}}}}})
	if err != nil {
		t.Fatal(err)
	}
	unknown := &agentv1.ConnectRequest{Seq: 2, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Unix(), IntervalEndUnix: now.Add(time.Second).Unix(),
		Traffic:  []*agentv1.TrafficDelta{{CredId: "crd_unknown", InboundId: ids.i1, BytesDown: 10}},
		Sessions: []*agentv1.Session{{CredId: "crd_unknown", InboundId: ids.i1, ConnectedAtUnix: now.Unix()}},
	}}}
	_, err = coreStep(ctx, core, first.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: unknown})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.NodeLive(e.ctx, now.Add(time.Second), state.NodeID, true)
	if err != nil || len(rows) != 1 {
		t.Fatalf("NodeLive after unknown credential = %+v, %v", rows, err)
	}
	var view liveJSONView
	if err := json.Unmarshal([]byte(rows[0].LiveJSON), &view); err != nil {
		t.Fatal(err)
	}
	if view.Metrics != nil || rows[0].CPUPct != 0 || len(view.Online) != 0 || len(view.UserDownBPS) != 0 || view.UploadBPS != 0 {
		t.Fatalf("a batch without host metrics and with an unknown credential: %+v, cpu column %d", view, rows[0].CPUPct)
	}
}

// A batch may name up to maxStatsDeltas users: the projection keeps the top consumers, not a row past D1's 2 MB.
func TestTopRatesKeepsTheHighest(t *testing.T) {
	rates := map[string]uint64{"usr_a": 5, "usr_b": 9, "usr_c": 9, "usr_d": 1}
	if got := topRates(rates, 2); !reflect.DeepEqual(got, map[string]uint64{"usr_b": 9, "usr_c": 9}) {
		t.Fatalf("topRates = %v", got)
	}
	if got := topRates(rates, 4); len(got) != 4 {
		t.Fatalf("a small map must stay whole: %v", got)
	}
	many := map[string]uint64{}
	for i := range 3000 {
		many[fmt.Sprintf("usr_%04d", i)] = uint64(i)
	}
	if got := topRates(many, 2000); len(got) != 2000 || got["usr_2999"] != 2999 || got["usr_0999"] != 0 {
		t.Fatalf("topRates(3000 users) kept %d, highest %d", len(got), got["usr_2999"])
	}
}

func TestSessionCorePoisonBatchDroppedAfterReconnect(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-poison")
	ids := e.fixture(state.NodeID)
	tr := stepHello(t, core, ctx, state, now, hello("instance-poison", 0, "").GetHello())
	e.exec(`CREATE TRIGGER reject_core_stats BEFORE INSERT ON traffic_bucket WHEN NEW.user_id = 'usr_alice' BEGIN SELECT RAISE(ABORT, 'test refusal'); END`)
	batch := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Traffic: []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 5}},
	}}}
	first, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if first.Close == nil || first.Close.Class != CloseInternal || len(first.Frames) != 0 {
		t.Fatalf("first refusal = close %v, frames %#v", first.Close, first.Frames)
	}
	oldAdapter := newTestSession(e, ctx, nil, state)
	oldAdapter.persistPoison(first.State.Poison)
	disconnected, err := coreStep(ctx, core, first.State, SessionEvent{Kind: EventDisconnected, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	oldAdapter.persistPoison(disconnected.State.Poison)

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadline: now.Add(2 * time.Second).Add(helloTimeout), Poison: e.f.poisonFor(state.NodeID)}
	newHello, err := coreStep(newCtx, newCore, newState, SessionEvent{Kind: EventHello, At: now.Add(2 * time.Second), Frame: hello("instance-poison", 0, "")})
	if err != nil || newHello.Close != nil {
		t.Fatalf("reconnect Hello = close %v, err %v", newHello.Close, err)
	}
	second, err := coreStep(newCtx, newCore, newHello.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(3 * time.Second), Frame: batch})
	if err != nil {
		t.Fatal(err)
	}
	if second.Close != nil || len(second.Frames) != 1 || second.Frames[0].GetAck().GetUpToSeq() != 1 {
		t.Fatalf("second refusal = close %v, frames %#v", second.Close, second.Frames)
	}
	newAdapter := newTestSession(e, newCtx, nil, newState)
	newAdapter.persistPoison(second.State.Poison)
	if e.f.poisonFor(state.NodeID) != nil {
		t.Fatal("the successful drop left poison state behind")
	}
	if n := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_dropped'`, state.NodeID); n != 1 {
		t.Fatalf("stats_dropped events = %d, want 1", n)
	}
}

func TestSessionCoreRetainsPoisonWhenDropCannotAdvanceSequence(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-poison-skip-error")
	ids := e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, now, hello("instance-poison-skip-error", 0, "").GetHello())
	e.exec(`CREATE TRIGGER reject_core_stats BEFORE INSERT ON traffic_bucket WHEN NEW.user_id = 'usr_alice' BEGIN SELECT RAISE(ABORT, 'test refusal'); END`)
	batch := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: now.Add(-10 * time.Second).Unix(), IntervalEndUnix: now.Unix(),
		Traffic: []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 5}},
	}}}
	first, err := coreStep(ctx, core, connected.State, SessionEvent{Kind: EventAgentFrame, At: now, Frame: batch})
	if err != nil || first.Close == nil || first.State.Poison == nil {
		t.Fatalf("first refusal = transition %+v, err %v", first, err)
	}
	adapter := newTestSession(e, ctx, nil, state)
	adapter.persistPoison(first.State.Poison)
	disconnected, err := coreStep(ctx, core, first.State, SessionEvent{Kind: EventDisconnected, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	adapter.persistPoison(disconnected.State.Poison)

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner,
		HelloDeadline: now.Add(2 * time.Second).Add(helloTimeout), Poison: e.f.poisonFor(state.NodeID)}
	newHello, err := coreStep(newCtx, newCore, newState, SessionEvent{Kind: EventHello,
		At: now.Add(2 * time.Second), Frame: hello("instance-poison-skip-error", 0, "")})
	if err != nil || newHello.Close != nil {
		t.Fatalf("reconnect Hello = close %v, err %v", newHello.Close, err)
	}
	e.exec(`CREATE TRIGGER reject_core_skip_seq BEFORE UPDATE OF last_seq ON node WHEN NEW.last_seq > OLD.last_seq BEGIN SELECT RAISE(ABORT, 'test skip refusal'); END`)
	second, err := coreStep(newCtx, newCore, newHello.State, SessionEvent{Kind: EventAgentFrame,
		At: now.Add(3 * time.Second), Frame: batch})
	if err != nil || second.Close == nil || len(second.Frames) != 0 {
		t.Fatalf("failed sequence skip = close %v, frames %#v, err %v", second.Close, second.Frames, err)
	}
	if second.State.Poison == nil || second.State.Poison.Instance != "instance-poison-skip-error" || second.State.Poison.Seq != 1 {
		t.Fatalf("failed sequence skip cleared poison state: %+v", second.State.Poison)
	}
	adapter = newTestSession(e, newCtx, nil, newState)
	adapter.persistPoison(second.State.Poison)
	if poison := e.f.poisonFor(state.NodeID); poison == nil || *poison != *second.State.Poison {
		t.Fatalf("adapter did not retain poison state after failed skip: %+v", poison)
	}
	if count := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_dropped'`, state.NodeID); count != 0 {
		t.Fatalf("failed sequence skip recorded %d stats_dropped events", count)
	}
}

func TestSessionCoreApplyResultsAndAlarms(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-apply")
	tr := stepHello(t, core, ctx, state, now, hello("instance-apply", 0, "").GetHello())
	state = tr.State
	revision := state.SentRevision
	base := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: revision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH,
	}}}
	tr, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now, Frame: base})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(tr, EffectPrepareDesired) {
		t.Fatalf("base mismatch effects = %#v", tr.Effects)
	}
	tr = reconcilePrepared(t, e, core, ctx, tr, now)
	if len(tr.Frames) != 1 || tr.Frames[0].GetDesiredState() == nil || tr.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("base mismatch resend = %#v", tr.Frames)
	}

	state = tr.State
	stale := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: state.SentRevision - 1, Status: agentv1.ApplyStatus_APPLY_STATUS_APPLIED, StateHash: "stale",
	}}}
	tr, err = coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now, Frame: stale})
	if err != nil {
		t.Fatal(err)
	}
	if tr.State.DriftResent || tr.State.Drift || len(tr.Frames) != 0 {
		t.Fatalf("stale result affected drift: state=%+v frames=%#v", tr.State, tr.Frames)
	}

	bad := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: state.SentRevision, Status: agentv1.ApplyStatus_APPLY_STATUS_APPLIED, StateHash: "wrong",
	}}}
	first, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now, Frame: bad})
	if err != nil {
		t.Fatal(err)
	}
	if !first.State.DriftResent || !hasEffect(first, EffectPrepareDesired) {
		t.Fatalf("first drift = state %+v effects %#v", first.State, first.Effects)
	}
	first = reconcilePrepared(t, e, core, ctx, first, now)
	if len(first.Frames) != 1 || first.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("first drift resend = %#v", first.Frames)
	}
	bad.GetApplyResult().Revision = first.State.SentRevision
	second, err := coreStep(ctx, core, first.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: bad})
	if err != nil {
		t.Fatal(err)
	}
	if !second.State.Drift || !second.State.DriftResent || len(second.Frames) != 0 {
		t.Fatalf("persistent drift = state %+v frames %#v", second.State, second.Frames)
	}

	liveAt := second.State.LivenessDeadline
	expired := fireAlarmsThrough(t, ctx, core, second, liveAt)
	if expired.Close == nil || expired.Close.Class != CloseDeadline {
		t.Fatalf("liveness alarm = %+v", expired.Close)
	}

	certAt := second.State.NextCertCheck
	cert, err := coreStep(ctx, core, second.State, SessionEvent{Kind: EventAgentFrame, At: certAt,
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: 1}}}})
	if err != nil || cert.Close != nil || !cert.State.NextCertCheck.Equal(certAt.Add(core.f.certCheck)) {
		t.Fatalf("certificate recheck on agent event = close %v, deadline %v, err %v", cert.Close, cert.State.NextCertCheck, err)
	}
}

func TestSessionCoreCommandLogsAndSupersede(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-requests")
	tr := stepHello(t, core, ctx, state, now, hello("instance-requests", 0, "").GetHello())
	state = tr.State
	command := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{RequestId: "req-command"}}}
	tr, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAdminCommand, At: now, Request: &AdminRequest{
		RequestID: "req-command", Deadline: now.Add(time.Minute), Frame: command,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 1 || len(tr.State.Pending) != 1 {
		t.Fatalf("command transition = state %+v frames %#v", tr.State, tr.Frames)
	}
	result := &agentv1.CommandResult{RequestId: "req-command", Ok: true}
	replyFrame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: result}}
	beforeClose, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: replyFrame})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(beforeClose, EffectReply) || beforeClose.Effects[0].RequestID != "req-command" || beforeClose.Effects[0].Reply != replyFrame {
		t.Fatalf("command result effects = %+v", beforeClose.Effects)
	}
	if len(beforeClose.State.Pending) != 0 {
		t.Fatalf("command result left a pending request: %+v", beforeClose.State.Pending)
	}
	doctorRequest := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{RunDoctor: &agentv1.RunDoctor{RequestId: "req-doctor"}}}
	doctorStart, err := coreStep(ctx, core, beforeClose.State, SessionEvent{Kind: EventAdminCommand, At: now, Request: &AdminRequest{
		RequestID: "req-doctor", Deadline: now.Add(time.Minute), Frame: doctorRequest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	doctorReply := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_DoctorReport{DoctorReport: &agentv1.DoctorReport{RequestId: "req-doctor"}}}
	doctorDone, err := coreStep(ctx, core, doctorStart.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: doctorReply})
	if err != nil || len(doctorDone.Effects) != 1 || doctorDone.Effects[0].Kind != EffectReply || doctorDone.Effects[0].Reply != doctorReply {
		t.Fatalf("doctor reply effect = %+v, err %v", doctorDone.Effects, err)
	}
	disconnected, err := coreStep(ctx, core, beforeClose.State, SessionEvent{Kind: EventDisconnected, At: now.Add(2 * time.Second)})
	if err != nil || len(disconnected.State.Pending) != 0 {
		t.Fatalf("disconnect after command result left pending requests: %+v, err %v", disconnected.State.Pending, err)
	}
	duplicate, err := coreStep(ctx, core, disconnected.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(3 * time.Second),
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: result}}})
	if err != nil || hasEffect(duplicate, EffectReply) {
		t.Fatalf("disconnected session emitted a second command result: %+v, err %v", duplicate.Effects, err)
	}
	logFrame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_LogRequest{LogRequest: &agentv1.LogRequest{RequestId: "req-log"}}}
	started, err := coreStep(ctx, core, beforeClose.State, SessionEvent{Kind: EventLogStart, At: now, Request: &AdminRequest{
		RequestID: "req-log", Deadline: now.Add(time.Minute), Frame: logFrame,
	}})
	if err != nil {
		t.Fatal(err)
	}
	chunk := &agentv1.LogChunk{RequestId: "req-log", Lines: []*agentv1.LogLine{{Message: "line"}}}
	routed, err := coreStep(ctx, core, started.State, SessionEvent{Kind: EventAgentFrame, At: now,
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_LogChunk{LogChunk: chunk}}})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(routed, EffectLogChunk) || routed.Effects[0].RequestID != "req-log" {
		t.Fatalf("log routing effects = %+v", routed.Effects)
	}

	adapter := newTestSession(e, ctx, nil, state)
	adapter.logs["req-log"] = &logSub{ch: make(chan *agentv1.LogChunk, 1)}
	adapter.deliverLog(chunk)
	adapter.deliverLog(&agentv1.LogChunk{RequestId: "req-log", Lines: []*agentv1.LogLine{{Message: "overflow"}}})
	if got := adapter.logs["req-log"].dropped.Load(); got != 1 {
		t.Fatalf("log overflow dropped lines = %d, want 1", got)
	}

	superseded, err := coreStep(ctx, core, routed.State, SessionEvent{Kind: EventOwnerSuperseded, At: now})
	if err != nil || superseded.Close == nil || superseded.Close.Class != CloseConflict {
		t.Fatalf("supersede = %+v, %v", superseded.Close, err)
	}
}

// A new ConnectResponse message must be either an allowlisted admin request or a frame only the panel itself sends.
func TestSessionCoreAllowlistCoversEveryFrame(t *testing.T) {
	notRequests := map[protoreflect.Name]bool{"hello_ack": true, "ack": true, "desired_state": true, "kick": true, "log_cancel": true, "ping": true}
	fields := (&agentv1.ConnectResponse{}).ProtoReflect().Descriptor().Oneofs().ByName("message").Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		frame := (&agentv1.ConnectResponse{}).ProtoReflect()
		frame.Set(fd, frame.NewField(fd))
		allowed := requestKind(frame.Interface().(*agentv1.ConnectResponse)) != 0
		if allowed == notRequests[fd.Name()] {
			t.Errorf("%s: allowlisted %v, listed as not a request %v; it must be exactly one", fd.Name(), allowed, notRequests[fd.Name()])
		}
	}
}

func TestSessionCoreRequestKindsComeFromAllowlistedFrames(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "core-request-allowlist")
	beforeHello, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAdminCommand, At: now, Request: &AdminRequest{
		RequestID: "req-before-hello", Deadline: now.Add(time.Minute),
		Frame: &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{RequestId: "req-before-hello"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeHello.Frames) != 0 || len(beforeHello.State.Pending) != 0 || len(beforeHello.Effects) != 1 || beforeHello.Effects[0].Kind != EffectReply || beforeHello.Effects[0].RequestID != "req-before-hello" || beforeHello.Effects[0].Reply != nil {
		t.Fatalf("request before Hello was not refused with a nil reply: %+v", beforeHello)
	}

	tr := stepHello(t, core, ctx, beforeHello.State, now, hello("instance-allowlist", 0, "").GetHello())
	state = tr.State
	cases := []struct {
		id   string
		kind EventKind
		want PendingRequestKind
		msg  *agentv1.ConnectResponse
	}{
		{"doctor", EventAdminCommand, PendingDoctor, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RunDoctor{RunDoctor: &agentv1.RunDoctor{RequestId: "doctor"}}}},
		{"log", EventLogStart, PendingLog, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_LogRequest{LogRequest: &agentv1.LogRequest{RequestId: "log"}}}},
		{"restart", EventAdminCommand, PendingCommand, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RestartInbound{RestartInbound: &agentv1.RestartInbound{RequestId: "restart"}}}},
		{"fix", EventAdminCommand, PendingCommand, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_ApplyFix{ApplyFix: &agentv1.ApplyFix{RequestId: "fix"}}}},
		{"update", EventAdminCommand, PendingCommand, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{UpdateAgent: &agentv1.UpdateAgent{RequestId: "update"}}}},
		{"rollback", EventAdminCommand, PendingCommand, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RollbackAgent{RollbackAgent: &agentv1.RollbackAgent{RequestId: "rollback"}}}},
		{"bandwidth", EventAdminCommand, PendingCommand, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_MeasureBandwidth{MeasureBandwidth: &agentv1.MeasureBandwidth{RequestId: "bandwidth"}}}},
		{"awg", EventAdminCommand, PendingCommand, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_PrepareAwgKernel{PrepareAwgKernel: &agentv1.PrepareAwgKernel{RequestId: "awg"}}}},
		{"udp-count", EventAdminCommand, PendingCommand, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpCount{UdpCount: &agentv1.UdpCount{RequestId: "udp-count"}}}},
		{"udp-send", EventAdminCommand, PendingCommand, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UdpSend{UdpSend: &agentv1.UdpSend{RequestId: "udp-send"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			tr, err := coreStep(ctx, core, state, SessionEvent{Kind: tc.kind, At: now, Request: &AdminRequest{
				RequestID: tc.id, Deadline: now.Add(time.Minute), Frame: tc.msg,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if len(tr.Frames) != 1 || tr.State.Pending[tc.id].Kind != tc.want {
				t.Fatalf("request kind = %v, frames %d; want %v and one frame", tr.State.Pending[tc.id].Kind, len(tr.Frames), tc.want)
			}
		})
	}

	unknown, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAdminCommand, At: now, Request: &AdminRequest{
		RequestID: "req-unknown", Deadline: now.Add(time.Minute),
		Frame: &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Ack{Ack: &agentv1.Ack{UpToSeq: 1}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown.Frames) != 0 || len(unknown.State.Pending) != 0 || len(unknown.Effects) != 1 || unknown.Effects[0].Kind != EffectReply || unknown.Effects[0].RequestID != "req-unknown" || unknown.Effects[0].Reply != nil {
		t.Fatalf("unknown request was not refused with a nil reply: %+v", unknown)
	}
}

func TestSessionCoreRetireRepliesBeforeClosingAtAlarm(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "core-retire")
	tr := stepHello(t, core, ctx, state, now, hello("instance-retire", 0, "").GetHello())
	requestID := "req-retire"
	retire, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAdminCommand, At: now, Request: &AdminRequest{
		RequestID: requestID, Deadline: now.Add(time.Minute),
		Frame: &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Retire{Retire: &agentv1.Retire{RequestId: requestID}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	wantAlarm := now.Add(5 * time.Second)
	if len(retire.Frames) != 1 || retire.Frames[0].GetRetire() == nil || retire.NextAlarm == nil || !retire.NextAlarm.Equal(wantAlarm) {
		t.Fatalf("retire transition = frames %v, next alarm %v; want Retire and %v", retire.Frames, retire.NextAlarm, wantAlarm)
	}
	if len(retire.Effects) != 1 || retire.Effects[0].Kind != EffectReply || retire.Effects[0].RequestID != requestID || retire.Effects[0].Reply != nil {
		t.Fatalf("retire did not reply immediately: %+v", retire.Effects)
	}
	closed, err := coreStep(ctx, core, retire.State, SessionEvent{Kind: EventAlarm, At: wantAlarm})
	if err != nil || closed.Close == nil || closed.Close.Reason != "node retired" {
		t.Fatalf("retire alarm close = %+v, err %v", closed.Close, err)
	}
}

func TestSessionCorePendingRequestExpiresAtNextAlarm(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "core-request-expiry")
	tr := stepHello(t, core, ctx, state, now, hello("instance-request-expiry", 0, "").GetHello())
	deadline := now.Add(500 * time.Millisecond)
	request := &AdminRequest{RequestID: "req-expiry", Deadline: deadline, Frame: &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_UpdateAgent{
		UpdateAgent: &agentv1.UpdateAgent{RequestId: "req-expiry"},
	}}}
	tr, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAdminCommand, At: now, Request: request})
	if err != nil {
		t.Fatal(err)
	}
	if tr.NextAlarm == nil || !tr.NextAlarm.Equal(deadline) {
		t.Fatalf("next alarm = %v, want pending request expiry %v", tr.NextAlarm, deadline)
	}
	expired, err := coreStep(ctx, core, tr.State, SessionEvent{Kind: EventAlarm, At: *tr.NextAlarm})
	if err != nil {
		t.Fatal(err)
	}
	if len(expired.State.Pending) != 0 {
		t.Fatalf("expired request is still pending: %+v", expired.State.Pending)
	}
	if len(expired.Effects) != 1 || expired.Effects[0].Kind != EffectReply || expired.Effects[0].RequestID != "req-expiry" || expired.Effects[0].Reply != nil {
		t.Fatalf("expired request did not produce a nil reply: %+v", expired.Effects)
	}
}

func TestSessionCoreCertificateRevocationClosesOnEvent(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("node-cert-alarm")
	owner, ctx := e.f.claimOwner(a.nodeID, e.ctx)
	t.Cleanup(func() {
		e.f.mu.Lock()
		current := e.f.owners[a.nodeID]
		e.f.mu.Unlock()
		if current.cancel != nil {
			current.cancel(nil)
		}
	})
	core := NewSessionCore(e.f)
	now := e.f.now().UTC()
	serial := a.leaf.SerialNumber.Text(16)
	state := SessionState{Version: sessionStateVersion, NodeID: a.nodeID, OwnerGeneration: owner,
		PeerCertSerial: serial, PeerCertNotAfter: a.leaf.NotAfter,
		HelloDeadline: now.Add(helloTimeout)}

	helloTr, err := coreStep(ctx, core, state, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-cert-alarm", 0, "")})
	if err != nil || helloTr.Close != nil {
		t.Fatalf("hello transition = close %v, err %v", helloTr.Close, err)
	}
	e.exec(`UPDATE node_cert SET revoked_at = ?, revoke_reason = 'test' WHERE node_id = ?`, now.Add(-time.Second).Unix(), a.nodeID)
	certDeadline := helloTr.State.NextCertCheck
	if helloTr.NextAlarm == nil || helloTr.NextAlarm.Equal(certDeadline) {
		t.Fatalf("certificate deadline scheduled an alarm: next=%v certificate=%v", helloTr.NextAlarm, certDeadline)
	}
	closed, err := coreStep(ctx, core, helloTr.State, SessionEvent{Kind: EventAgentFrame, At: certDeadline,
		Frame: &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Pong{Pong: &agentv1.Pong{Nonce: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	if closed.Close == nil || closed.Close.Class != CloseUnauthenticated {
		t.Fatalf("revoked certificate event = %+v, want unauthenticated close", closed.Close)
	}
}

func TestSessionCoreCoalescesDesiredPreparation(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-stale-prepare")
	e.fixture(state.NodeID)
	current := stepHello(t, core, ctx, state, now, hello("instance-stale-prepare", 0, "").GetHello())
	requested, err := coreStep(ctx, core, current.State, SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if !requested.State.Preparing || !hasEffect(requested, EffectPrepareDesired) {
		t.Fatalf("desired change did not start preparation: %+v", requested.State)
	}
	older, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_erin'`)
	dirty, err := coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredChanged, At: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if !dirty.State.Preparing || !dirty.State.PrepareDirty || hasEffect(dirty, EffectPrepareDesired) {
		t.Fatalf("change during preparation was not coalesced: state=%+v effects=%v", dirty.State, effectKinds(dirty))
	}
	first, err := coreStep(ctx, core, dirty.State, SessionEvent{Kind: EventDesiredPrepared, At: now.Add(2 * time.Second), Prepared: older})
	if err != nil {
		t.Fatal(err)
	}
	if !first.State.Preparing || first.State.PrepareDirty || !hasEffect(first, EffectPrepareDesired) {
		t.Fatalf("dirty preparation did not start once after apply: state=%+v effects=%v", first.State, effectKinds(first))
	}
	fresh, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := coreStep(ctx, core, first.State, SessionEvent{Kind: EventDesiredPrepared, At: now.Add(3 * time.Second), Prepared: fresh})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Frames) != 1 || updated.Frames[0].GetDesiredState() == nil {
		t.Fatalf("coalesced prepared state frames = %#v", updated.Frames)
	}
	newRevision := updated.State.SentRevision
	node, err := e.st.Node(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.DesiredRevision != newRevision || node.DesiredHash != updated.State.SentStateHash {
		t.Fatalf("stored desired state = %d/%s, newest prepared state = %d/%s", node.DesiredRevision, node.DesiredHash, newRevision, updated.State.SentStateHash)
	}
}

func TestDesiredPreparationFromEndedOwnerDoesNotReachNewSession(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "core-ended-prepare")
	e.fixture(state.NodeID)
	connected := stepHello(t, core, ctx, state, now, hello("instance-ended-prepare", 0, "").GetHello())
	old := newTestSession(e, ctx, core, connected.State)

	originalDesired := e.f.cfg.Desired
	entered, release := make(chan struct{}), make(chan struct{})
	var blockNext atomic.Bool
	blockNext.Store(true)
	e.f.cfg.Desired = func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
		if blockNext.CompareAndSwap(true, false) {
			close(entered)
			<-release
		}
		return originalDesired(ctx, nodeID)
	}
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		e.f.cfg.Desired = originalDesired
	})
	oldDone := make(chan error, 1)
	go func() {
		_, err := old.stepDesired(context.Background())
		oldDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("old session preparation did not reach the desired-state read")
	}

	newOwner, newCtx := e.f.claimOwner(state.NodeID, e.ctx)
	old.cancel(nil)
	close(old.done)
	newCore := NewSessionCore(e.f)
	newState := SessionState{Version: sessionStateVersion, NodeID: state.NodeID, OwnerGeneration: newOwner}
	newHelloFrame := hello("instance-ended-prepare-new", 0, "")
	newHello, err := coreStep(newCtx, newCore, newState, SessionEvent{Kind: EventHello, At: now.Add(2 * time.Second), Frame: newHelloFrame})
	if err != nil || newHello.Close != nil {
		t.Fatalf("new session Hello = close %v, err %v", newHello.Close, err)
	}
	newSession := newTestSession(e, newCtx, newCore, newHello.State)
	if _, err := newSession.stepDesired(newCtx); err != nil {
		t.Fatal(err)
	}
	newRevision := newSession.coreState.SentRevision
	close(release)
	select {
	case err := <-oldDone:
		if err != nil {
			t.Fatalf("ended session preparation returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ended session preparation did not finish")
	}
	node, err := e.st.Node(e.ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.DesiredRevision != newRevision || node.DesiredHash != newSession.coreState.SentStateHash || len(old.out) != 0 {
		t.Fatalf("old preparation reached the new session: stored=%d/%s new=%d/%s old frames=%d",
			node.DesiredRevision, node.DesiredHash, newRevision, newSession.coreState.SentStateHash, len(old.out))
	}
}

func TestSessionCoreStepMutatesCallerState(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "core-pointer-step")
	tr, err := core.Step(ctx, &state, SessionEvent{Kind: EventOpen, At: now})
	if err != nil {
		t.Fatal(err)
	}
	if state.HelloDeadline.IsZero() || tr.NextAlarm == nil || !tr.NextAlarm.Equal(state.HelloDeadline) {
		t.Fatalf("Step did not update caller state: state=%+v transition=%+v", state, tr)
	}
}

type manualAgentSessionStream struct {
	ctx context.Context
	in  chan *agentv1.ConnectRequest
	out chan *agentv1.ConnectResponse
}

func (stream *manualAgentSessionStream) Context() context.Context { return stream.ctx }
func (stream *manualAgentSessionStream) Receive() (*agentv1.ConnectRequest, error) {
	select {
	case request, ok := <-stream.in:
		if !ok {
			return nil, io.EOF
		}
		return request, nil
	case <-stream.ctx.Done():
		return nil, io.EOF
	}
}
func (stream *manualAgentSessionStream) Send(response *agentv1.ConnectResponse) error {
	select {
	case stream.out <- response:
		return nil
	case <-stream.ctx.Done():
		return stream.ctx.Err()
	}
}

// refillingAgentStream drops what the loop sends and, once full is set, tops the session's out queue up again on every
// Send, so the next step that queues a frame finds it full.
type refillingAgentStream struct {
	manualAgentSessionStream
	full atomic.Pointer[session]
}

func (stream *refillingAgentStream) Send(response *agentv1.ConnectResponse) error {
	s := stream.full.Load()
	if s == nil {
		return stream.manualAgentSessionStream.Send(response)
	}
	for s.enqueue(&agentv1.ConnectResponse{}) {
	}
	return nil
}

func TestRunSessionStepsDisconnectBeforeClosingDone(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("disconnect-core")
	e.f.mu.Lock()
	e.f.stuck[a.nodeID] = stuckSeq{instance: "instance-disconnect", seq: 7}
	e.f.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	owner, ownerCtx := e.f.claimOwner(a.nodeID, ctx)
	stream := &manualAgentSessionStream{ctx: ownerCtx, in: make(chan *agentv1.ConnectRequest, 2), out: make(chan *agentv1.ConnectResponse, 16)}
	done := make(chan error, 1)
	go func() {
		done <- (agentService{e.f}).runSession(ownerCtx, a.nodeID, peerCert{serial: a.leaf.SerialNumber.Text(16), notAfter: a.leaf.NotAfter}, owner, stream)
	}()
	stream.in <- hello("instance-disconnect", 0, "")
	waitSessionAck(t, stream.out, 0)
	s := e.f.session(a.nodeID)
	if s == nil {
		t.Fatal("session was not registered after HelloAck")
	}
	close(stream.in)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("session close returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("session did not stop after cancellation")
	}
	if !s.coreState.Disconnected || nextSessionAlarm(s.coreState, e.f.now()) != nil {
		t.Fatalf("runSession ended without clearing core alarms: state=%+v", s.coreState)
	}
	e.f.mu.Lock()
	poison := e.f.stuck[a.nodeID]
	e.f.mu.Unlock()
	if poison != (stuckSeq{instance: "instance-disconnect", seq: 7}) {
		t.Fatalf("disconnect rewrote poison state: %+v", poison)
	}
}

func waitSessionAck(t *testing.T, responses <-chan *agentv1.ConnectResponse, upTo uint64) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case response := <-responses:
			if ack := response.GetAck(); ack != nil && ack.UpToSeq >= upTo && upTo != 0 {
				return
			}
			if upTo == 0 && response.GetHelloAck() != nil {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for ack up to %d", upTo)
		}
	}
}

func TestSessionStateSerializationSizeBudget(t *testing.T) {
	_, core, ctx, state, now := coreFixture(t, "core-size")
	tr := stepHello(t, core, ctx, state, now, hello("instance-large-node", 0, "").GetHello())
	state = tr.State
	state.SentWithheld = []string{"inb_awg", "inb_tunnel"}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var decoded SessionState
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, decoded) {
		t.Fatalf("session state round trip differs: before=%+v after=%+v", state, decoded)
	}
	if len(b) >= 4<<10 {
		t.Fatalf("session state serialized to %d bytes, want under 4096", len(b))
	}
}

func effectsOf(tr any) []SessionEffect {
	switch value := tr.(type) {
	case Transition:
		return value.Effects
	case testTransition:
		return value.Effects
	default:
		return nil
	}
}

func hasEffect(tr any, kind EffectKind) bool {
	for _, effect := range effectsOf(tr) {
		if effect.Kind == kind {
			return true
		}
	}
	return false
}

func effectKinds(tr any) []EffectKind {
	effects := effectsOf(tr)
	out := make([]EffectKind, len(effects))
	for i, effect := range effects {
		out[i] = effect.Kind
	}
	return out
}

// helloSession is a session whose core has taken Open and Hello; it is not registered with the fleet.
func helloSession(t *testing.T, name string) (*env, *session) {
	t.Helper()
	e, core, ctx, state, now := coreFixture(t, name)
	if _, err := core.Step(ctx, &state, SessionEvent{Kind: EventOpen, At: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := core.Step(ctx, &state, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-1", 0, "")}); err != nil {
		t.Fatal(err)
	}
	return e, newTestSession(e, ctx, core, state)
}

// registeredSession is a registered session that has sent its first desired state; end unregisters it.
func registeredSession(t *testing.T, e *env, name string) (*session, func()) {
	t.Helper()
	nodeID, _, _ := e.createEnrollment(name, name+".example.com")
	owner, ctx := e.f.claimOwner(nodeID, e.ctx)
	core := NewSessionCore(e.f)
	state := SessionState{Version: sessionStateVersion, NodeID: nodeID, OwnerGeneration: owner}

	now := e.f.now()
	if _, err := core.Step(ctx, &state, SessionEvent{Kind: EventOpen, At: now}); err != nil {
		t.Fatal(err)
	}
	helloTr, err := core.Step(ctx, &state, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-"+name, 0, "")})
	if err != nil || helloTr.Close != nil {
		t.Fatalf("session Hello = close %v, err %v", helloTr.Close, err)
	}
	s := newTestSession(e, ctx, core, state)
	if !e.f.register(s) {
		t.Fatal("session owner was not registered")
	}
	if _, err := s.stepDesired(s.ctx); err != nil {
		t.Fatal(err)
	}
	receiveDesired(t, s, 0)

	var endOnce sync.Once
	end := func() {
		endOnce.Do(func() {
			s.cancel(nil)
			close(s.done)
			e.f.unregister(s)
		})
	}
	t.Cleanup(end)
	return s, end
}

// receiveDesired returns the DesiredState frames queued for the agent: it waits for at least atLeast of them, then takes
// whatever else is already queued.
func receiveDesired(t *testing.T, s *session, atLeast int) []*agentv1.DesiredState {
	t.Helper()
	timeout := time.After(5 * time.Second)
	var frames []*agentv1.DesiredState
	for {
		var frame *agentv1.ConnectResponse
		if len(frames) < atLeast {
			select {
			case frame = <-s.out:
			case <-timeout:
				t.Fatalf("received %d DesiredState frames, want %d", len(frames), atLeast)
			}
		} else {
			select {
			case frame = <-s.out:
			default:
				return frames
			}
		}
		if desired := frame.GetDesiredState(); desired != nil {
			frames = append(frames, desired)
		}
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestInitialReconcileSurvivesConcurrentRecompute(t *testing.T) {
	e, s := helloSession(t, "initial-reconcile")
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	e.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) {
		if reads.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil, nil
	}
	initial := make(chan error, 1)
	go func() {
		_, err := s.stepDesired(s.ctx)
		initial <- err
	}()
	<-started
	if _, err := s.stepDesired(s.ctx); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-initial; err != nil {
		t.Fatal(err)
	}
	if got := receiveDesired(t, s, 0); len(got) == 0 {
		t.Fatal("initial desired state was lost when recompute finished first")
	}
}

func TestDesiredPreparationSerializesReadsAndSendsLatestState(t *testing.T) {
	e, s := helloSession(t, "prepare-order")
	if _, err := s.stepDesired(s.ctx); err != nil {
		t.Fatal(err)
	}
	receiveDesired(t, s, 0)

	var version atomic.Int32
	version.Store(1)
	var reads, active, maxActive atomic.Int32
	started, followupStarted, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	e.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) {
		v := version.Load()
		call := reads.Add(1)
		if call == 2 {
			close(followupStarted)
		}
		inFlight := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); inFlight > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, inFlight) {
				break
			}
		}
		if call == 1 {
			close(started)
			<-release
		}
		return []statehash.Inbound{{Spec: plugin.InboundSpec{ID: "inb_prepare", Protocol: "fakehy", ProfileID: "prf_prepare", Version: uint64(v), Enabled: true}}}, nil
	}
	first := make(chan error, 1)
	go func() {
		_, err := s.stepDesired(s.ctx)
		first <- err
	}()
	<-started
	version.Store(2)
	if _, err := s.stepDesired(s.ctx); err != nil {
		t.Fatal(err)
	}
	readsBeforeRelease := reads.Load()
	close(release)
	waitSignal(t, followupStarted, "the dirty preparation hand-off")
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if maxActive.Load() != 1 || readsBeforeRelease != 1 {
		t.Fatalf("preparations overlapped: max active=%d reads before release=%d", maxActive.Load(), readsBeforeRelease)
	}
	latest, err := e.f.prepareDesiredState(s.ctx, s.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	frames := receiveDesired(t, s, 2)
	s.coreMu.Lock()
	sentHash := s.coreState.SentStateHash
	s.coreMu.Unlock()
	if sentHash != latest.desired.hash {
		t.Fatalf("last sent hash = %q, latest database hash = %v", sentHash, latest.desired.hash)
	}
	if frames[len(frames)-1].StateHash != latest.desired.hash {
		t.Fatalf("last DesiredState = %v, latest database hash = %v", frames, latest.desired.hash)
	}
}

func TestStepAfterEndDoesNotRearmOrRewritePoison(t *testing.T) {
	e, s := helloSession(t, "step-after-end")
	now := e.f.now()
	poison := &PoisonBatch{Instance: "instance-1", Seq: 7}
	s.coreState.Poison = poison
	e.f.mu.Lock()
	e.f.stuck[s.nodeID] = stuckSeq{instance: poison.Instance, seq: poison.Seq}
	e.f.mu.Unlock()
	ended, err := s.stepCore(s.ctx, SessionEvent{Kind: EventDisconnected, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if ended.NextAlarm != nil {
		t.Fatalf("disconnected session has next alarm %v", ended.NextAlarm)
	}
	again, err := s.stepCore(s.ctx, SessionEvent{Kind: EventAlarm, At: now.Add(2 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if again.NextAlarm != nil || !s.coreState.Disconnected || nextSessionAlarm(s.coreState, now.Add(2*time.Second)) != nil {
		t.Fatalf("event after end changed session scheduling: disconnected=%t next=%v", s.coreState.Disconnected, again.NextAlarm)
	}
	e.f.mu.Lock()
	got := e.f.stuck[s.nodeID]
	e.f.mu.Unlock()
	if got != (stuckSeq{instance: poison.Instance, seq: poison.Seq}) {
		t.Fatalf("poison entry changed after end: %+v", got)
	}
}

func TestFailedPreparationRetriesOnAlarmAndKeepsFullResend(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "prepare-retry")
	connected := stepHello(t, core, ctx, state, now, hello("instance-retry", 0, "").GetHello())
	mismatchFrame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: connected.State.SentRevision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH,
	}}}
	mismatch, err := coreStep(ctx, core, connected.State, SessionEvent{Kind: EventAgentFrame, At: now.Add(time.Second), Frame: mismatchFrame})
	if err != nil || !hasEffect(mismatch, EffectPrepareDesired) || !mismatch.State.FullResendPending {
		t.Fatalf("base mismatch transition = %+v, err %v", mismatch, err)
	}
	failed, err := coreStep(ctx, core, mismatch.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(2 * time.Second), Err: errors.New("desired source unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State.Preparing || !failed.State.PrepareRetryAt.Equal(now.Add(2*time.Second+prepareRetryDelay)) || !failed.State.FullResendPending {
		t.Fatalf("failed preparation state = %+v", failed.State)
	}
	retryAt := failed.State.PrepareRetryAt
	retry, err := coreStep(ctx, core, failed.State, SessionEvent{Kind: EventAlarm, At: retryAt})
	if err != nil || !retry.State.Preparing || !hasEffect(retry, EffectPrepareDesired) {
		t.Fatalf("retry alarm transition = %+v, err %v", retry, err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	resent, err := coreStep(ctx, core, retry.State, SessionEvent{Kind: EventDesiredPrepared, At: retryAt, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(resent.Frames) != 1 || resent.Frames[0].GetDesiredState() == nil || resent.Frames[0].GetDesiredState().BaseRevision != 0 || resent.State.FullResendPending {
		t.Fatalf("successful retry did not send the pending full state: state=%+v frames=%#v", resent.State, resent.Frames)
	}
}

func TestRepeatedBaseMismatchCoalescesDesiredPreparation(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "rev-base-flight")
	connected := stepHello(t, core, ctx, state, now, hello("instance-base-mismatch", 0, "").GetHello())
	frame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: connected.State.SentRevision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH,
	}}}
	inFlight, err := coreStep(ctx, core, connected.State, SessionEvent{Kind: EventAgentFrame,
		At: now.Add(time.Second), Frame: frame})
	if err != nil || !hasEffect(inFlight, EffectPrepareDesired) || !inFlight.State.Preparing {
		t.Fatalf("initial base mismatch did not start preparation: state=%+v effects=%v err=%v", inFlight.State, effectKinds(inFlight), err)
	}
	for i := 2; i < 10; i++ {
		again, stepErr := coreStep(ctx, core, inFlight.State, SessionEvent{Kind: EventAgentFrame,
			At: now.Add(time.Duration(i) * time.Second), Frame: frame})
		if stepErr != nil {
			t.Fatal(stepErr)
		}
		if hasEffect(again, EffectPrepareDesired) || !again.State.PrepareDirty || !again.State.Preparing {
			t.Fatalf("base mismatch %d started extra work: state=%+v effects=%v", i, again.State, effectKinds(again))
		}
		inFlight = again
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := coreStep(ctx, core, inFlight.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(10 * time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if !first.State.Preparing || first.State.PrepareDirty || !hasEffect(first, EffectPrepareDesired) ||
		len(first.Frames) != 1 || first.Frames[0].GetDesiredState().BaseRevision != 0 {
		t.Fatalf("first completion did not send full state and request one more prepare: state=%+v effects=%v frames=%v",
			first.State, effectKinds(first), first.Frames)
	}
	secondPrepared, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := coreStep(ctx, core, first.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(11 * time.Second), Prepared: secondPrepared})
	if err != nil {
		t.Fatal(err)
	}
	if second.State.Preparing || hasEffect(second, EffectPrepareDesired) {
		t.Fatalf("coalesced second preparation left extra work: state=%+v effects=%v", second.State, effectKinds(second))
	}
}

func TestRecomputeWaitsForOnePreparationRoundPerNode(t *testing.T) {
	e := newCoreEnv(t)
	slow, endSlow := registeredSession(t, e, "de")
	fast, endFast := registeredSession(t, e, "node1")
	originalDesired := e.f.cfg.Desired
	firstRead, secondRead, fastRead := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseFirst, releaseSecond := make(chan struct{}), make(chan struct{})
	recomputeDone := make(chan struct{})
	var closeFirst, closeSecond sync.Once
	var reads atomic.Int32
	e.f.cfg.Desired = func(ctx context.Context, nodeID string) ([]statehash.Inbound, error) {
		if nodeID == slow.nodeID {
			switch reads.Add(1) {
			case 1:
				close(firstRead)
				<-releaseFirst
				if err := answerBaseMismatch(slow); err != nil {
					return nil, err
				}
			case 2:
				close(secondRead)
				if err := answerBaseMismatch(slow); err != nil {
					return nil, err
				}
				<-releaseSecond
			}
		} else if nodeID == fast.nodeID {
			close(fastRead)
		}
		return originalDesired(ctx, nodeID)
	}
	t.Cleanup(func() {
		closeFirst.Do(func() { close(releaseFirst) })
		closeSecond.Do(func() { close(releaseSecond) })
		endSlow()
		endFast()
		select {
		case <-recomputeDone:
		case <-time.After(5 * time.Second):
			t.Error("recompute did not stop after test sessions ended")
		}
	})

	go func() {
		e.f.recomputeAll(e.ctx)
		close(recomputeDone)
	}()
	waitSignal(t, firstRead, "the slow node's inline read")
	waitSignal(t, fastRead, "the other node's read")
	closeFirst.Do(func() { close(releaseFirst) })
	waitSignal(t, secondRead, "the slow node's background follow-up")
	select {
	case <-recomputeDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("recompute waited for the slow node's background follow-up; reads=%d", reads.Load())
	}
}

func answerBaseMismatch(s *session) error {
	s.coreMu.Lock()
	revision := s.coreState.SentRevision
	s.coreMu.Unlock()
	if revision == 0 {
		return nil
	}
	frame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_ApplyResult{ApplyResult: &agentv1.ApplyResult{
		Revision: revision, Status: agentv1.ApplyStatus_APPLY_STATUS_BASE_MISMATCH,
	}}}
	_, err := s.stepCore(s.ctx, SessionEvent{Kind: EventAgentFrame, At: s.f.now(), Frame: frame})
	return err
}

func TestHelloShortcutSendsChangedSettingsDelta(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "hello-settings-shortcut")
	node, err := e.st.Node(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	preparedBefore, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := coreStep(ctx, core, state, SessionEvent{Kind: EventHello, At: now,
		Frame: hello("instance-settings", 7, preparedBefore.desired.hash)})
	if err != nil || opened.Close != nil {
		t.Fatalf("Hello = close %v, err %v", opened.Close, err)
	}
	country := "DE"
	updatedNode, err := e.st.UpdateNode(ctx, node.ID, store.NodePatch{CountryCode: &country})
	if err != nil {
		t.Fatal(err)
	}
	requested, err := coreStep(ctx, core, opened.State, SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent.Frames) != 1 || sent.Frames[0].GetDesiredState() == nil {
		t.Fatalf("changed settings did not produce DesiredState: %+v", sent.Frames)
	}
	delta := sent.Frames[0].GetDesiredState()
	if delta.BaseRevision != 7 || delta.Settings == nil || delta.Settings.CountryCode != updatedNode.CountryCode {
		t.Fatalf("settings delta = %+v, want base revision 7 and country %q", delta, updatedNode.CountryCode)
	}
}

// The node row may already hold a higher desired revision than the one the agent applied (sent, then changed back):
// the shortcut keeps that revision and still records the state the agent holds as the desired one.
func TestHelloShortcutRecordsDesiredHashBehindAHigherRevision(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "hello-higher-rev")
	if err := e.st.NodeDesired(ctx, state.NodeID, 9, "sent-then-changed-back", []byte(`{"r":9,"h":"sent-then-changed-back"}`)); err != nil {
		t.Fatal(err)
	}
	want, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := coreStep(ctx, core, state, SessionEvent{Kind: EventHello, At: now, Frame: hello("instance-higher", 7, want.desired.hash)})
	if err != nil || opened.Close != nil {
		t.Fatalf("Hello = close %v, err %v", opened.Close, err)
	}
	requested, err := coreStep(ctx, core, opened.State, SessionEvent{Kind: EventDesiredChanged, At: now})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	done, err := coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredPrepared, At: now, Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(done.Frames) != 0 || done.State.SentRevision != 7 {
		t.Fatalf("shortcut sent %d frames, sent revision %d; want none and 7", len(done.Frames), done.State.SentRevision)
	}
	node, raw, err := e.st.NodeWithSentDigest(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	var digest sentDigest
	if err := json.Unmarshal(raw, &digest); err != nil {
		t.Fatal(err)
	}
	if node.DesiredRevision != 9 || node.DesiredHash != want.desired.hash || digest.Revision != 7 {
		t.Fatalf("node desired %d/%q, digest revision %d; want 9/%q and 7", node.DesiredRevision, node.DesiredHash, digest.Revision, want.desired.hash)
	}
}

func TestHelloShortcutResendsFullStateAfterSentDigestLoss(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "hello-sent-digest-loss")
	state.SentRevision = 9
	preparedBefore, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := coreStep(ctx, core, state, SessionEvent{Kind: EventHello, At: now,
		Frame: hello("instance-sent-digest-loss", 7, preparedBefore.desired.hash)})
	if err != nil || opened.Close != nil {
		t.Fatalf("Hello = close %v, err %v", opened.Close, err)
	}
	requested, err := coreStep(ctx, core, opened.State, SessionEvent{Kind: EventDesiredChanged, At: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := e.f.prepareDesiredState(ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	sent, err := coreStep(ctx, core, requested.State, SessionEvent{Kind: EventDesiredPrepared,
		At: now.Add(time.Second), Prepared: prepared})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent.Frames) != 1 || sent.Frames[0].GetDesiredState() == nil {
		t.Fatalf("sent digest loss skipped DesiredState: %+v", sent.Frames)
	}
	full := sent.Frames[0].GetDesiredState()
	if full.BaseRevision != 0 || full.Revision <= state.SentRevision || full.Settings == nil {
		t.Fatalf("sent digest loss state = %+v, want a full state after revision %d", full, state.SentRevision)
	}
}

func TestSessionStepCloseCancelsWithReceiveCause(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "session-close-cause")
	s := newTestSession(e, ctx, core, state)
	tr, err := s.stepCore(s.ctx, SessionEvent{Kind: EventOwnerSuperseded, At: now})
	if err != nil || tr.Close == nil {
		t.Fatalf("session close transition = %+v, err %v", tr.Close, err)
	}
	cause := context.Cause(s.ctx)
	closeErr := sessionCloseError(tr.Close)
	if cause == nil || code(cause) != code(closeErr) || cause.Error() != closeErr.Error() {
		t.Fatalf("session cause = %v, want %v", cause, closeErr)
	}
	if received := errOrCause(s.ctx); received == nil || code(received) != code(cause) || received.Error() != cause.Error() {
		t.Fatalf("receive loop error = %v, want close cause %v", received, cause)
	}
}
