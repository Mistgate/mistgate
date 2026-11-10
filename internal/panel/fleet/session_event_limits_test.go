package fleet

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func TestReliableEventBacklogKeepsUpdateCommitAndDeduplicatesBeforeCharging(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "reliable-event-backlog")
	ids := e.fixture(state.NodeID)
	state = stepHello(t, core, ctx, state, now, hello("instance-event-backlog", 0, "").GetHello()).State
	codes := []string{"agent_started", "state_applied", "engine_started", "engine_started", "update_committed"}
	for len(codes) < reliableEventBurst {
		codes = append(codes, "de1_outage_backlog")
	}
	for i, eventCode := range codes {
		event := &agentv1.Event{Code: eventCode, TimeUnix: now.Unix()}
		if eventCode == "engine_started" {
			if i == 2 {
				event.InboundId = ids.i1
			} else {
				event.InboundId = ids.i2
			}
		}
		if eventCode == "update_committed" {
			event.Params = map[string]string{"from_version": "0.1.0", "from_built": "1000", "to_version": "0.2.0", "to_built": "2000"}
		}
		frame := &agentv1.ConnectRequest{Seq: uint64(i + 1), Message: &agentv1.ConnectRequest_Event{Event: event}}
		tr, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now, Frame: frame})
		if err != nil || tr.Close != nil {
			t.Fatalf("reliable event %d (%s): transition=%+v err=%v", i+1, eventCode, tr, err)
		}
		state = tr.State
		if i == 4 {
			charged := state.ReliableEventTokens
			duplicate := &agentv1.ConnectRequest{Seq: uint64(i + 1), Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
				Code: "de1_duplicate_probe", TimeUnix: now.Unix(),
			}}}
			dedup, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now, Frame: duplicate})
			if err != nil || dedup.Close != nil || dedup.State.ReliableEventTokens != charged {
				t.Fatalf("duplicate reliable event spent a token: before=%d after=%d transition=%+v err=%v", charged, dedup.State.ReliableEventTokens, dedup, err)
			}
			if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'de1_duplicate_probe'`, state.NodeID); got != 0 {
				t.Fatalf("stored %d duplicate probe events", got)
			}
			state = dedup.State
		}
	}
	for _, code := range []string{"agent_started", "state_applied", "engine_started", "update_committed", "de1_outage_backlog"} {
		got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = ?`, state.NodeID, code)
		want := int64(1)
		if code == "engine_started" {
			want = 2
		}
		if code == "de1_outage_backlog" {
			want = int64(reliableEventBurst - 5)
		}
		if got != want {
			t.Errorf("stored %d %s events, want %d", got, code, want)
		}
	}
	node, err := e.st.Node(e.ctx, state.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if update, ok := node.LastUpdate(); !ok || update.ToBuilt != 2_000 || update.Outcome != "ok" {
		t.Fatalf("update_committed from the event backlog was not applied: %+v, present=%v", update, ok)
	}
	if state.ReliableEventTokens != 0 {
		t.Fatalf("reliable event tokens after a full burst = %d, want 0", state.ReliableEventTokens)
	}
	dropped, err := coreStep(ctx, core, state, SessionEvent{Kind: EventAgentFrame, At: now, Frame: &agentv1.ConnectRequest{
		Seq: reliableEventBurst + 1, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "de1_overflow_probe", TimeUnix: now.Unix()}},
	}})
	if err != nil || dropped.Close != nil || dropped.State.AckPending != reliableEventBurst+1 || dropped.State.DroppedEvents != 1 {
		t.Fatalf("event beyond the 500 event burst: transition=%+v err=%v", dropped, err)
	}
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'de1_overflow_probe'`, state.NodeID); got != 0 {
		t.Fatalf("stored %d over-limit reliable events", got)
	}
}

func TestTorrentUserLookupFailureDropsAndAcknowledges(t *testing.T) {
	e, core, ctx, state, now := coreFixture(t, "event-user-fail")
	e.fixture(state.NodeID)
	state = stepHello(t, core, ctx, state, now, hello("instance-user-fail", 0, "").GetHello()).State
	var logs bytes.Buffer
	core.f.log = slog.New(slog.NewTextHandler(&logs, nil))
	lookupCtx, cancel := context.WithCancel(ctx)
	cancel()
	frame := &agentv1.ConnectRequest{Seq: 1, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
		Code: "torrent_attempt", TimeUnix: now.Unix(), Params: map[string]string{"user_id": "usr_alice"},
	}}}
	tr, err := coreStep(lookupCtx, core, state, SessionEvent{Kind: EventAgentFrame, At: now, Frame: frame})
	if err != nil || tr.Close != nil || tr.State.AckPending != 1 || len(tr.Frames) != 1 || tr.Frames[0].GetAck().GetUpToSeq() != 1 {
		t.Fatalf("failed user lookup did not drop and acknowledge the event: transition=%+v err=%v", tr, err)
	}
	if got := e.count(`SELECT count(*) FROM event WHERE node_id = ? AND code = 'torrent_attempt'`, state.NodeID); got != 0 {
		t.Fatalf("stored %d events after the user lookup failed", got)
	}
	if got := strings.Count(logs.String(), "event_user_lookup_failed"); got != 1 {
		t.Fatalf("user lookup failure log count = %d, logs=%s", got, logs.String())
	}
}

func TestReliableEventBucketRefillsFivePerSecond(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	state := SessionState{ReliableEventTokens: 0, ReliableEventTokenAt: now}
	if !allowAgentEvent(&state, now.Add(time.Second), 1) || state.ReliableEventTokens != 4 {
		t.Fatalf("one-second refill left %d tokens, want 4", state.ReliableEventTokens)
	}
}
