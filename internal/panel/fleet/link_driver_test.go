package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/statehash"
	"google.golang.org/protobuf/proto"
)

// linkRig drives Fleet.Link the way the NodeLink object does: it keeps the last state string and the clock.
type linkRig struct {
	t      *testing.T
	e      *env
	nodeID string
	now    time.Time
	state  string
}

func newLinkRig(t *testing.T, name string) *linkRig {
	t.Helper()
	e := newCoreEnv(t)
	nodeID, _, _ := e.createEnrollment(name, "de1.example.com")
	r := &linkRig{t: t, e: e, nodeID: nodeID, now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	e.f.now = func() time.Time { return r.now }
	e.f.cfg.Now = e.f.now
	return r
}

// call runs one event and keeps its state for the next one.
func (r *linkRig) call(in LinkIn) LinkOut {
	r.t.Helper()
	out, err := r.try(in)
	if err != nil {
		r.t.Fatalf("Link(%s): %v", in.Kind, err)
	}
	return out
}

func (r *linkRig) try(in LinkIn) (LinkOut, error) {
	r.t.Helper()
	in.NodeID = r.nodeID
	if in.State == "" {
		in.State = r.state
	}
	if in.At.IsZero() {
		in.At = r.now
	}
	r.now = in.At
	out, err := r.e.f.Link(r.e.ctx, in)
	if err == nil {
		r.state = out.State
	}
	return out, err
}

func (r *linkRig) open(generation uint64) LinkOut {
	r.t.Helper()
	return r.call(LinkIn{Kind: LinkOpen, Generation: generation})
}

func (r *linkRig) frame(msg *agentv1.ConnectRequest) LinkOut {
	r.t.Helper()
	raw, err := proto.Marshal(msg)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.call(LinkIn{Kind: LinkFrame, Frame: raw})
}

func (r *linkRig) request(id string, deadline time.Time, frame *agentv1.ConnectResponse) LinkOut {
	r.t.Helper()
	raw, err := proto.Marshal(frame)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.call(LinkIn{Kind: LinkRequest, RequestID: id, DeadlineAt: deadline, Frame: raw})
}

// helloed opens a session and says Hello.
func (r *linkRig) helloed(generation uint64, instance string) LinkOut {
	r.t.Helper()
	r.open(generation)
	return r.frame(hello(instance, 0, ""))
}

func (r *linkRig) decoded() SessionState {
	r.t.Helper()
	var s SessionState
	if err := json.Unmarshal([]byte(r.state), &s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

func linkFrames(t *testing.T, out LinkOut) []*agentv1.ConnectResponse {
	t.Helper()
	frames := make([]*agentv1.ConnectResponse, 0, len(out.Frames))
	for _, raw := range out.Frames {
		var m agentv1.ConnectResponse
		if err := proto.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, &m)
	}
	return frames
}

func restartInbound(id string) *agentv1.ConnectResponse {
	return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_RestartInbound{RestartInbound: &agentv1.RestartInbound{RequestId: id}}}
}

func TestLinkOpenKeepsOnlyPoison(t *testing.T) {
	r := newLinkRig(t, "alice-open")
	var old SessionState
	fillSessionStateValue(t, reflect.ValueOf(&old).Elem())
	old.NodeID = r.nodeID
	old.Disconnected = true
	old.Poison = &PoisonBatch{Instance: "inst-a", Seq: 41}
	raw, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	r.state = string(raw)
	notAfter := r.now.Add(90 * 24 * time.Hour)
	out := r.call(LinkIn{Kind: LinkOpen, Generation: 7, CertSerial: "0a1b", CertNotAfter: notAfter})

	want := SessionState{Version: sessionStateVersion, NodeID: r.nodeID, OwnerGeneration: 7, PeerCertSerial: "0a1b",
		PeerCertNotAfter: notAfter, HelloDeadline: r.now.Add(helloTimeout), Poison: &PoisonBatch{Instance: "inst-a", Seq: 41}}
	if got := r.decoded(); !sessionStateDeepEqual(reflect.ValueOf(got), reflect.ValueOf(want)) {
		t.Fatalf("open state = %+v\nwant %+v", got, want)
	}
	if !out.AlarmAt.Equal(want.HelloDeadline) || len(out.Frames) != 0 || out.Close != nil || len(out.Replies) != 0 || out.Forget {
		t.Fatalf("open output = %+v; want only the Hello deadline as alarm", out)
	}
	// A first session of the node starts empty.
	r2 := newLinkRig(t, "alice-first")
	first := r2.open(1)
	if got := r2.decoded(); got.Poison != nil || got.OwnerGeneration != 1 || !first.AlarmAt.Equal(r2.now.Add(helloTimeout)) {
		t.Fatalf("first open = %+v, alarm %v", got, first.AlarmAt)
	}
}

func TestLinkHelloAndDesiredInOneCall(t *testing.T) {
	r := newLinkRig(t, "alice-hello")
	out := r.helloed(3, "instance-hello")
	frames := linkFrames(t, out)
	if len(frames) != 2 || frames[0].GetHelloAck() == nil || frames[1].GetDesiredState() == nil {
		t.Fatalf("hello frames = %v; want HelloAck then DesiredState", frames)
	}
	if out.Close != nil || len(out.Replies) != 0 {
		t.Fatalf("hello output = %+v", out)
	}
	state := r.decoded()
	if state.InstanceID != "instance-hello" || state.Preparing || state.PrepareDirty || state.SentRevision == 0 || state.SentStateHash == "" {
		t.Fatalf("state after Hello = %+v", state)
	}
	if !out.AlarmAt.Equal(state.LivenessDeadline) {
		t.Fatalf("alarm = %v, want the liveness deadline %v", out.AlarmAt, state.LivenessDeadline)
	}
	rows, err := r.e.f.Live(r.e.ctx)
	if err != nil || len(rows) != 1 || !rows[0].Connected || rows[0].Session != 3 {
		t.Fatalf("node_live after Hello = %+v, %v; want connected with session 3", rows, err)
	}
}

func TestLinkDesiredBeforeHelloDoesNothing(t *testing.T) {
	r := newLinkRig(t, "alice-poke-early")
	opened := r.open(1)
	state := r.state
	out := r.call(LinkIn{Kind: LinkDesired})
	if len(out.Frames) != 0 || out.Close != nil || out.State != state {
		t.Fatalf("desired before Hello = %+v; want no frames and the same state", out)
	}
	if !out.AlarmAt.Equal(opened.AlarmAt) {
		t.Fatalf("desired before Hello dropped the Hello deadline: alarm %v, want %v", out.AlarmAt, opened.AlarmAt)
	}
	// After Hello a poke with nothing changed sends nothing; a change gives a delta on the revision last sent.
	r.e.fixture(r.nodeID)
	r.frame(hello("instance-poke", 0, ""))
	sentRevision := r.decoded().SentRevision
	if sentRevision == 0 {
		t.Fatal("Hello sent no desired state")
	}
	quiet := r.call(LinkIn{Kind: LinkDesired})
	if len(quiet.Frames) != 0 || quiet.Close != nil || r.decoded().Preparing {
		t.Fatalf("desired with nothing changed = %+v", quiet)
	}
	r.e.exec(`UPDATE user SET status = 'disabled' WHERE id = 'usr_erin'`)
	poke := r.call(LinkIn{Kind: LinkDesired})
	frames := linkFrames(t, poke)
	if len(frames) != 1 || frames[0].GetDesiredState() == nil || frames[0].GetDesiredState().BaseRevision != sentRevision ||
		frames[0].GetDesiredState().Revision <= sentRevision || poke.Close != nil || r.decoded().Preparing {
		t.Fatalf("desired after a change = %+v (frames %v); want a delta on base revision %d", poke, frames, sentRevision)
	}
}

func TestLinkPreparationErrorIsRetriedByAlarm(t *testing.T) {
	r := newLinkRig(t, "alice-prepare-error")
	working := r.e.f.cfg.Desired
	r.e.f.cfg.Desired = func(context.Context, string) ([]statehash.Inbound, error) {
		return nil, errors.New("desired source down")
	}
	out := r.helloed(1, "instance-prepare-error")
	state := r.decoded()
	wantRetry := r.now.Add(prepareRetryDelay)
	if state.Preparing || !state.PrepareRetryAt.Equal(wantRetry) || !out.AlarmAt.Equal(wantRetry) {
		t.Fatalf("failed preparation: state %+v, alarm %v; want a retry at %v and Preparing cleared", state, out.AlarmAt, wantRetry)
	}
	if frames := linkFrames(t, out); len(frames) != 1 || frames[0].GetHelloAck() == nil || out.Close != nil {
		t.Fatalf("frames = %v, close %v; want only HelloAck", frames, out.Close)
	}
	// The source recovers; the alarm at the retry time sends the state the Hello could not.
	r.e.f.cfg.Desired = working
	retry := r.call(LinkIn{Kind: LinkAlarm, At: wantRetry})
	frames := linkFrames(t, retry)
	if len(frames) != 1 || frames[0].GetDesiredState() == nil || frames[0].GetDesiredState().BaseRevision != 0 || retry.Close != nil {
		t.Fatalf("retry alarm = %+v (frames %v); want one full DesiredState", retry, frames)
	}
	if state := r.decoded(); state.Preparing || !state.PrepareRetryAt.IsZero() || state.SentRevision == 0 {
		t.Fatalf("state after the retry = %+v", state)
	}
}

func TestLinkRefusedRequestsAreRepliedInTheSameCall(t *testing.T) {
	r := newLinkRig(t, "alice-refuse")
	r.open(1)
	deadline := r.now.Add(time.Minute)
	// Before Hello.
	out := r.request("req-early", deadline, restartInbound("req-early"))
	if len(out.Frames) != 0 || len(out.Replies) != 1 || out.Replies[0].RequestID != "req-early" || out.Replies[0].Frame != nil {
		t.Fatalf("request before Hello = %+v; want a nil reply and no frame", out)
	}
	if out.AlarmAt.IsZero() {
		t.Fatal("a refused request dropped the Hello deadline")
	}
	r.frame(hello("instance-refuse", 0, ""))
	// A frame that is not an admin request.
	out = r.request("req-unknown", deadline, &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Ack{Ack: &agentv1.Ack{UpToSeq: 1}}})
	if len(out.Frames) != 0 || len(out.Replies) != 1 || out.Replies[0].RequestID != "req-unknown" || out.Replies[0].Frame != nil {
		t.Fatalf("unknown request = %+v; want a nil reply and no frame", out)
	}
	// Bytes that are not a ConnectResponse.
	out = r.call(LinkIn{Kind: LinkRequest, RequestID: "req-junk", DeadlineAt: deadline, Frame: []byte{0xff, 0xff, 0xff}})
	if len(out.Frames) != 0 || len(out.Replies) != 1 || out.Replies[0].RequestID != "req-junk" || out.Replies[0].Frame != nil || out.Close != nil {
		t.Fatalf("undecodable request = %+v; want a nil reply", out)
	}
	if out.AlarmAt.IsZero() {
		t.Fatal("an undecodable request dropped the liveness alarm")
	}
}

func TestLinkRetireRepliesAtOnceThenClosesAndForgets(t *testing.T) {
	r := newLinkRig(t, "alice-retire")
	r.helloed(1, "instance-retire")
	retire := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_Retire{Retire: &agentv1.Retire{RequestId: "req-retire"}}}
	out := r.request("req-retire", r.now.Add(time.Minute), retire)
	frames := linkFrames(t, out)
	if len(frames) != 1 || frames[0].GetRetire() == nil || len(out.Replies) != 1 || out.Replies[0].RequestID != "req-retire" || out.Replies[0].Frame != nil {
		t.Fatalf("retire = frames %v, replies %+v; want the frame and a nil reply in the same call", frames, out.Replies)
	}
	wantAlarm := r.now.Add(5 * time.Second)
	if !out.AlarmAt.Equal(wantAlarm) || out.Close != nil {
		t.Fatalf("retire alarm = %v, close %v; want %v", out.AlarmAt, out.Close, wantAlarm)
	}
	closing := r.call(LinkIn{Kind: LinkAlarm, At: wantAlarm})
	if closing.Close == nil || closing.Close.Code != 1008 || closing.Close.Reason != "node retired" || closing.Forget {
		t.Fatalf("retire alarm = %+v; want close 1008 \"node retired\"", closing)
	}
	closed := r.call(LinkIn{Kind: LinkClosed, At: wantAlarm})
	if !closed.Forget || !closed.AlarmAt.IsZero() || !r.decoded().Disconnected {
		t.Fatalf("closed step after retire = %+v; want Forget, no alarm, Disconnected", closed)
	}
}

func TestLinkClosedWithoutRetireDoesNotForget(t *testing.T) {
	r := newLinkRig(t, "alice-closed")
	r.helloed(1, "instance-closed")
	out := r.call(LinkIn{Kind: LinkClosed, At: r.now.Add(time.Second)})
	if out.Forget || out.Close != nil || !r.decoded().Disconnected {
		t.Fatalf("closed = %+v", out)
	}
	rows, err := r.e.f.Live(r.e.ctx)
	if err != nil || len(rows) != 1 || rows[0].Connected {
		t.Fatalf("node_live after the closed step = %+v, %v; want offline", rows, err)
	}
	// The object runs the closed step once per socket; a second one, and one for a node that never opened, do nothing.
	again := r.call(LinkIn{Kind: LinkClosed, At: r.now.Add(2 * time.Second)})
	if again.State != out.State || again.Forget {
		t.Fatalf("second closed step = %+v", again)
	}
	empty, err := r.e.f.Link(r.e.ctx, LinkIn{NodeID: r.nodeID, Kind: LinkClosed, At: r.now})
	if err != nil || empty.State != "" || empty.Forget {
		t.Fatalf("closed step on an empty state = %+v, %v", empty, err)
	}
}

func TestLinkClosedOfAnOldGenerationKeepsTheNewerRow(t *testing.T) {
	r := newLinkRig(t, "alice-old-gen")
	r.helloed(1, "instance-old")
	oldState := r.state
	r.now = r.now.Add(time.Second)
	r.open(2)
	r.frame(hello("instance-new", 0, ""))
	if rows, err := r.e.f.Live(r.e.ctx); err != nil || len(rows) != 1 || rows[0].Session != 2 || !rows[0].Connected {
		t.Fatalf("node_live before the old close = %+v, %v", rows, err)
	}
	newState := r.state
	closed, err := r.try(LinkIn{Kind: LinkClosed, State: oldState, At: r.now.Add(time.Second)})
	if err != nil || closed.Forget {
		t.Fatalf("closed step of generation 1 = %+v, %v", closed, err)
	}
	rows, err := r.e.f.Live(r.e.ctx)
	if err != nil || len(rows) != 1 || rows[0].Session != 2 || !rows[0].Connected {
		t.Fatalf("node_live after the old close = %+v, %v; want the generation 2 row kept", rows, err)
	}
	if r.state == newState {
		t.Fatal("the test did not run the old state through the closed step")
	}
}

func TestLinkCommandResultRepliesWithTheAgentsBytes(t *testing.T) {
	r := newLinkRig(t, "alice-command")
	r.helloed(1, "instance-command")
	deadline := r.now.Add(time.Minute)
	sent := r.request("req-1", deadline, restartInbound("req-1"))
	if frames := linkFrames(t, sent); len(frames) != 1 || frames[0].GetRestartInbound() == nil || len(sent.Replies) != 0 {
		t.Fatalf("request = frames %v, replies %+v", frames, sent.Replies)
	}
	wantAlarm := deadline
	if live := r.decoded().LivenessDeadline; live.Before(wantAlarm) {
		wantAlarm = live
	}
	if _, ok := r.decoded().Pending["req-1"]; !ok || !sent.AlarmAt.Equal(wantAlarm) {
		t.Fatalf("pending %v, alarm %v; want req-1 pending and alarm %v", r.decoded().Pending, sent.AlarmAt, wantAlarm)
	}
	result := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{
		RequestId: "req-1", Ok: true, Affected: 2, Detail: "restarted"}}}
	out := r.frame(result)
	if len(out.Replies) != 1 || out.Replies[0].RequestID != "req-1" || out.Replies[0].Frame == nil {
		t.Fatalf("command result = %+v; want one reply with a frame", out.Replies)
	}
	var got agentv1.ConnectRequest
	if err := proto.Unmarshal(out.Replies[0].Frame, &got); err != nil || !proto.Equal(&got, result) {
		t.Fatalf("reply frame = %v, %v; want the agent's frame %v", &got, err, result)
	}
	if len(r.decoded().Pending) != 0 {
		t.Fatalf("request still pending after its result: %+v", r.decoded().Pending)
	}
}

func TestLinkExpiredRequestGetsANilReply(t *testing.T) {
	r := newLinkRig(t, "alice-expire")
	r.helloed(1, "instance-expire")
	deadline := r.now.Add(500 * time.Millisecond)
	sent := r.request("req-slow", deadline, restartInbound("req-slow"))
	if !sent.AlarmAt.Equal(deadline) {
		t.Fatalf("alarm = %v, want the request deadline %v", sent.AlarmAt, deadline)
	}
	out := r.call(LinkIn{Kind: LinkAlarm, At: deadline})
	if len(out.Replies) != 1 || out.Replies[0].RequestID != "req-slow" || out.Replies[0].Frame != nil || out.Close != nil {
		t.Fatalf("expiry = %+v; want one nil reply", out)
	}
	if len(r.decoded().Pending) != 0 || !out.AlarmAt.Equal(r.decoded().LivenessDeadline) {
		t.Fatalf("after expiry: pending %v, alarm %v", r.decoded().Pending, out.AlarmAt)
	}
}

func TestLinkCloseCodesAndReasonClip(t *testing.T) {
	for _, tc := range []struct {
		class CloseClass
		code  uint16
	}{
		{CloseConflict, 4000}, {CloseInternal, 1011}, {CloseInvalidArgument, 1008}, {CloseFailedPrecondition, 1008},
		{CloseUnauthenticated, 1008}, {CloseDeadline, 1008}, {CloseCanceled, 1008},
	} {
		c := &linkCall{}
		c.closeWith(tc.class, "reason")
		if c.out.Close == nil || c.out.Close.Code != tc.code || c.out.Close.Reason != "reason" {
			t.Fatalf("class %d closed as %+v, want code %d", tc.class, c.out.Close, tc.code)
		}
	}
	c := &linkCall{}
	c.closeWith(CloseInternal, strings.Repeat("é", 150)) // 300 bytes
	reason := c.out.Close.Reason
	if len(reason) > 123 || !utf8.ValidString(reason) || reason != strings.Repeat("é", 61) {
		t.Fatalf("clipped reason is %d bytes (valid %v), want 122 bytes of whole runes", len(reason), utf8.ValidString(reason))
	}
}

func TestLinkBadFramesCloseWith1008(t *testing.T) {
	r := newLinkRig(t, "alice-bad-frame")
	r.open(1)
	junk := r.call(LinkIn{Kind: LinkFrame, Frame: []byte{0xff, 0xff, 0xff}})
	if junk.Close == nil || junk.Close.Code != 1008 || len(junk.Frames) != 0 {
		t.Fatalf("undecodable frame = %+v; want close 1008", junk)
	}
	// A valid frame that is not Hello, as the first message.
	stats := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{}}}
	first := r.frame(stats)
	if first.Close == nil || first.Close.Code != 1008 || len(first.Frames) != 0 {
		t.Fatalf("first frame without Hello = %+v; want close 1008", first)
	}
	// Hello again on a running session.
	r.frame(hello("instance-twice", 0, ""))
	twice := r.frame(hello("instance-twice", 0, ""))
	if twice.Close == nil || twice.Close.Code != 1008 || twice.Close.Reason != "unexpected Hello" {
		t.Fatalf("second Hello = %+v; want close 1008", twice)
	}
}

func TestLinkErrors(t *testing.T) {
	r := newLinkRig(t, "alice-errors")
	ctx := r.e.ctx
	f := r.e.f
	in := func(kind LinkKind, state string) LinkIn {
		return LinkIn{NodeID: r.nodeID, Kind: kind, State: state, At: r.now, Generation: 1}
	}
	if _, err := f.Link(ctx, in(LinkAlarm, "{not json")); err == nil {
		t.Fatal("an undecodable state was accepted")
	}
	if _, err := f.Link(ctx, in(LinkClosed, "{not json")); err == nil {
		t.Fatal("an undecodable state was accepted by the closed step")
	}
	zero := in(LinkOpen, "")
	zero.Generation = 0
	if _, err := f.Link(ctx, zero); err == nil {
		t.Fatal("generation 0 was accepted on open")
	}
	for _, kind := range []LinkKind{LinkFrame, LinkAlarm, LinkDesired, LinkRequest} {
		if _, err := f.Link(ctx, in(kind, "")); err == nil {
			t.Fatalf("%s on an empty state was accepted", kind)
		}
	}
	r.helloed(1, "instance-errors")
	live := r.state
	if _, err := f.Link(ctx, in("bogus", live)); err == nil {
		t.Fatal("an unknown event kind was accepted")
	}
	other := in(LinkAlarm, live)
	other.NodeID = "nod_someone_else"
	if _, err := f.Link(ctx, other); err == nil {
		t.Fatal("a state of another node was accepted")
	}
	closed := r.call(LinkIn{Kind: LinkClosed, At: r.now.Add(time.Second)})
	for _, kind := range []LinkKind{LinkFrame, LinkAlarm, LinkDesired, LinkRequest} {
		if _, err := f.Link(ctx, in(kind, closed.State)); err == nil {
			t.Fatalf("%s on a disconnected state was accepted", kind)
		}
	}
	if _, err := f.Link(ctx, LinkIn{Kind: LinkAlarm, State: live, At: r.now}); err == nil {
		t.Fatal("an event without a node id was accepted")
	}
	// The cap on core steps.
	c := &linkCall{f: f, ctx: ctx, core: NewSessionCore(f), at: r.now, steps: linkMaxSteps}
	if err := c.step(SessionEvent{Kind: EventAlarm}); err == nil {
		t.Fatal("a fifth core step was accepted")
	}
}

func TestLinkStateRoundTripContinuesTheSession(t *testing.T) {
	r := newLinkRig(t, "alice-roundtrip")
	r.helloed(1, "instance-roundtrip")
	// The state string is all the next call has: stats are acknowledged, and a request is answered by a later call.
	stats := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: r.now.Add(-10 * time.Second).Unix(), IntervalEndUnix: r.now.Unix(),
		Host: &agentv1.HostMetrics{CpuPct: 12, NetRxBps: 1000, NetTxBps: 2000},
	}}}
	out := r.frame(stats)
	frames := linkFrames(t, out)
	if len(frames) != 1 || frames[0].GetAck().GetUpToSeq() != 1 || out.Close != nil {
		t.Fatalf("stats = frames %v, close %v; want Ack 1", frames, out.Close)
	}
	state := r.decoded()
	if state.AckSent != 1 || state.LastSeenAt.IsZero() {
		t.Fatalf("state after stats = %+v", state)
	}
	rows, err := r.e.f.Live(r.e.ctx)
	if err != nil || len(rows) != 1 || rows[0].SampleAt == 0 || rows[0].RxBps != 1000 {
		t.Fatalf("node_live after stats = %+v, %v", rows, err)
	}
	// A reply to a request made after the round trip.
	r.request("req-after", r.now.Add(time.Minute), restartInbound("req-after"))
	done := r.frame(&agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_CommandResult{CommandResult: &agentv1.CommandResult{RequestId: "req-after", Ok: true}}})
	if len(done.Replies) != 1 || done.Replies[0].Frame == nil {
		t.Fatalf("reply after the round trip = %+v", done.Replies)
	}
}

func TestLinkHandlerAnswersWithTheMarkerOnTheEdge(t *testing.T) {
	r := newLinkRig(t, "alice-marker")
	serve := func(h http.Handler, upgrade string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/link/nod_x", nil)
		if upgrade != "" {
			req.Header.Set("Upgrade", upgrade)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	r.e.f.cfg.Remote = &testRemote{}
	h := r.e.f.LinkHandler()
	if w := serve(h, "websocket"); w.Code != http.StatusNoContent || w.Header().Get(LinkMarkerHeader) != "nod_x" {
		t.Fatalf("edge upgrade: status %d, header %q", w.Code, w.Header().Get(LinkMarkerHeader))
	}
	if w := serve(h, ""); w.Code != http.StatusNotFound {
		t.Fatalf("edge request without an upgrade: status %d", w.Code)
	}
	// Without a Remote the handler still serves the socket itself (a plain GET is not an upgrade).
	r.e.f.cfg.Remote = nil
	if w := serve(r.e.f.LinkHandler(), "websocket"); w.Header().Get(LinkMarkerHeader) != "" {
		t.Fatalf("VPS handler answered with the marker: status %d", w.Code)
	}
}

func TestLinkOpenRepairsAStateItCannotUse(t *testing.T) {
	foreign, err := json.Marshal(SessionState{Version: sessionStateVersion, NodeID: "nod_someone_else", OwnerGeneration: 4,
		InstanceID: "instance-foreign", Poison: &PoisonBatch{Instance: "instance-foreign", Seq: 9}})
	if err != nil {
		t.Fatal(err)
	}
	for name, stored := range map[string]string{"undecodable": "{not json", "another node": string(foreign)} {
		r := newLinkRig(t, "alice-repair")
		out, err := r.try(LinkIn{Kind: LinkOpen, Generation: 5, State: stored})
		if err != nil {
			t.Fatalf("%s: open failed: %v", name, err)
		}
		want := SessionState{Version: sessionStateVersion, NodeID: r.nodeID, OwnerGeneration: 5, HelloDeadline: r.now.Add(helloTimeout)}
		if got := r.decoded(); !sessionStateDeepEqual(reflect.ValueOf(got), reflect.ValueOf(want)) || out.Close != nil || !out.AlarmAt.Equal(want.HelloDeadline) {
			t.Fatalf("%s: open = state %+v, close %v, alarm %v; want a clean generation 5 session", name, got, out.Close, out.AlarmAt)
		}
	}
}

func TestLinkCallPastItsContextWritesNothing(t *testing.T) {
	r := newLinkRig(t, "alice-late")
	ctx, cancel := context.WithCancel(r.e.ctx)
	cancel()
	if out, err := r.e.f.Link(ctx, LinkIn{NodeID: r.nodeID, Kind: LinkOpen, Generation: 1, At: r.now}); err == nil {
		t.Fatalf("a call past its context returned a state: %+v", out)
	}
}

func TestLinkRequestPastItsDeadlineIsNotSent(t *testing.T) {
	r := newLinkRig(t, "alice-past-deadline")
	r.helloed(1, "instance-past-deadline")
	before := r.state
	for name, deadline := range map[string]time.Time{"at": r.now, "after": r.now.Add(-time.Second)} {
		out := r.request("req-"+name, deadline, restartInbound("req-"+name))
		if len(out.Frames) != 0 || len(out.Replies) != 1 || out.Replies[0].Frame != nil || out.Close != nil || out.State != before {
			t.Fatalf("request %s its deadline = %+v; want a nil reply, no frame and an unchanged state", name, out)
		}
		if out.AlarmAt.IsZero() {
			t.Fatalf("request %s its deadline dropped the liveness alarm", name)
		}
	}
}

func TestLinkClosingCallReturnsTheChangedState(t *testing.T) {
	r := newLinkRig(t, "alice-poison")
	ids := r.e.fixture(r.nodeID)
	r.helloed(1, "instance-poison")
	r.e.exec(`CREATE TRIGGER reject_link_stats BEFORE INSERT ON traffic_bucket WHEN NEW.user_id = 'usr_alice' BEGIN SELECT RAISE(ABORT, 'test refusal'); END`)
	batch := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Stats{Stats: &agentv1.StatsBatch{
		IntervalStartUnix: r.now.Add(-10 * time.Second).Unix(), IntervalEndUnix: r.now.Unix(),
		Traffic: []*agentv1.TrafficDelta{{CredId: "crd_alice_hy", InboundId: ids.i1, BytesUp: 5}},
	}}}
	first := r.frame(batch)
	if first.Close == nil || first.Close.Code != 1011 || len(first.Frames) != 0 {
		t.Fatalf("first refusal = close %+v, frames %d; want 1011 and no frame", first.Close, len(first.Frames))
	}
	if p := r.decoded().Poison; p == nil || p.Instance != "instance-poison" || p.Seq != 1 {
		t.Fatalf("the closing call did not return Poison: %+v", p)
	}
	r.now = r.now.Add(time.Second)
	r.call(LinkIn{Kind: LinkClosed})
	// The agent reconnects as generation 2 and resends the batch it never got an Ack for.
	r.now = r.now.Add(time.Second)
	r.open(2)
	if p := r.decoded().Poison; p == nil || p.Seq != 1 {
		t.Fatalf("open lost Poison: %+v", p)
	}
	r.frame(hello("instance-poison", 0, ""))
	second := r.frame(batch)
	frames := linkFrames(t, second)
	if second.Close != nil || len(frames) != 1 || frames[0].GetAck().GetUpToSeq() != 1 {
		t.Fatalf("second refusal = close %+v, frames %v; want the batch dropped and Ack 1", second.Close, frames)
	}
	if r.decoded().Poison != nil {
		t.Fatalf("the successful drop left Poison behind: %+v", r.decoded().Poison)
	}
	if n := r.e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'stats_dropped'`, r.nodeID); n != 1 {
		t.Fatalf("stats_dropped events = %d, want 1", n)
	}
}
