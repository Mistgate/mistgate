//go:build js && wasm

package main

import (
	"context"
	"errors"
	"strings"
	"syscall/js"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"google.golang.org/protobuf/proto"
)

func jsResolved(value js.Value) js.Value {
	return js.Global().Get("Promise").Call("resolve", value)
}

func jsOutcome(fields map[string]js.Value) js.Value {
	out := js.Global().Get("Object").New()
	for name, value := range fields {
		out.Set(name, value)
	}
	return out
}

func TestEdgeRemoteAskOutcomes(t *testing.T) {
	validReply, err := proto.Marshal(&agentv1.ConnectRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		promise   func() js.Value
		wantReply bool
		wantError error
	}{
		{
			name:      "reply bytes",
			promise:   func() js.Value { return jsResolved(jsOutcome(map[string]js.Value{"reply": bytesToJS(validReply)})) },
			wantReply: true,
		},
		{
			name:    "null reply",
			promise: func() js.Value { return jsResolved(jsOutcome(map[string]js.Value{"reply": js.Null()})) },
		},
		{
			name:      "timeout",
			promise:   func() js.Value { return jsResolved(jsOutcome(map[string]js.Value{"error": js.ValueOf("timeout")})) },
			wantError: context.DeadlineExceeded,
		},
		{
			name:      "lost",
			promise:   func() js.Value { return jsResolved(jsOutcome(map[string]js.Value{"error": js.ValueOf("lost")})) },
			wantError: errRemoteLost,
		},
		{
			name:      "unknown outcome",
			promise:   func() js.Value { return jsResolved(js.Global().Get("Object").New()) },
			wantError: errRemoteLost,
		},
		{
			name:      "invalid reply bytes",
			promise:   func() js.Value { return jsResolved(jsOutcome(map[string]js.Value{"reply": bytesToJS([]byte{0xff})})) },
			wantError: errRemoteLost,
		},
		{
			name: "rejected promise",
			promise: func() js.Value {
				return js.Global().Get("Promise").Call("reject", js.Global().Get("Error").New("network failure"))
			},
			wantError: errRemoteLost,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var gotNode, gotRequestID string
			var gotFrame []byte
			var gotDeadline int64
			ask := js.FuncOf(func(_ js.Value, args []js.Value) any {
				calls++
				gotNode, gotRequestID = args[0].String(), args[1].String()
				gotFrame, _ = bytesFromJS(args[2])
				gotDeadline = int64(args[3].Float())
				return tc.promise()
			})
			defer ask.Release()
			request, err := (&edgeRemote{ask: ask.Value}).Ask(context.Background(), "node-a", "req_test", &agentv1.ConnectResponse{}, time.UnixMilli(1_800_000_000_000))
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("Ask() error = %v, want %v", err, tc.wantError)
			}
			if tc.wantReply && (err != nil || request == nil) {
				t.Fatalf("Ask() reply = %v, %v; want a decoded message", request, err)
			}
			if tc.wantError == nil && !tc.wantReply && (err != nil || request != nil) {
				t.Fatalf("Ask() null reply = %v, %v; want nil, nil", request, err)
			}
			if calls != 1 || gotNode != "node-a" || gotRequestID != "req_test" || gotDeadline != 1_800_000_000_000 || gotFrame == nil {
				t.Fatalf("Ask() JS args: calls=%d node=%q request=%q deadline=%d frame=%v", calls, gotNode, gotRequestID, gotDeadline, gotFrame)
			}
		})
	}
}

func TestEdgeRemoteCallerContextAndOwnNodeTripwire(t *testing.T) {
	var askCalls, closeCalls int
	ask := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		askCalls++
		return jsResolved(jsOutcome(map[string]js.Value{"reply": js.Null()}))
	})
	defer ask.Release()
	closeNode := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		closeCalls++
		return jsResolved(js.Undefined())
	})
	defer closeNode.Release()
	remote := &edgeRemote{ask: ask.Value, close: closeNode.Value}
	ctx, cancelLink, err := linkContext("node-a", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	defer cancelLink()
	if _, err := remote.Ask(ctx, "node-a", "req_test", &agentv1.ConnectResponse{}, time.Now().Add(time.Second)); !errors.Is(err, errOwnNode) {
		t.Fatalf("own-node Ask() error = %v, want %v", err, errOwnNode)
	}
	if err := remote.Close(ctx, "node-a", "re-enrol"); !errors.Is(err, errOwnNode) {
		t.Fatalf("own-node Close() error = %v, want %v", err, errOwnNode)
	}
	if askCalls != 0 || closeCalls != 0 {
		t.Fatalf("own-node tripwire called JavaScript: Ask=%d Close=%d, want both 0", askCalls, closeCalls)
	}
	if request, err := remote.Ask(ctx, "node-b", "req_test", &agentv1.ConnectResponse{}, time.Now().Add(time.Second)); err != nil || request != nil {
		t.Fatalf("other-node Ask() = %v, %v; want nil, nil", request, err)
	}
	if err := remote.Close(ctx, "node-b", "re-enrol"); err != nil {
		t.Fatalf("other-node Close() error = %v", err)
	}
	if askCalls != 1 || closeCalls != 1 {
		t.Fatalf("other-node calls: Ask=%d Close=%d, want one each", askCalls, closeCalls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var cancelingCalls int
	cancelingAsk := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		cancelingCalls++
		cancel()
		return jsResolved(jsOutcome(map[string]js.Value{"reply": js.Null()}))
	})
	defer cancelingAsk.Release()
	if _, err := (&edgeRemote{ask: cancelingAsk.Value}).Ask(ctx, "node-b", "req_test", &agentv1.ConnectResponse{}, time.Now().Add(time.Second)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Ask() error = %v, want context.Canceled", err)
	}
	if cancelingCalls != 1 {
		t.Fatalf("canceled Ask() called JavaScript %d times, want 1", cancelingCalls)
	}
}

func TestEdgeRemoteCloseClipsReasonAndMapsFailure(t *testing.T) {
	var gotReason string
	closeNode := js.FuncOf(func(_ js.Value, args []js.Value) any {
		gotReason = args[1].String()
		return jsResolved(js.Undefined())
	})
	defer closeNode.Release()
	if err := (&edgeRemote{close: closeNode.Value}).Close(context.Background(), "node-a", strings.Repeat("x", 124)); err != nil {
		t.Fatal(err)
	}
	if len(gotReason) != 123 {
		t.Fatalf("Close() reason length = %d, want 123 bytes", len(gotReason))
	}

	rejectingClose := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		return js.Global().Get("Promise").Call("reject", js.Global().Get("Error").New("link reset"))
	})
	defer rejectingClose.Release()
	if err := (&edgeRemote{close: rejectingClose.Value}).Close(context.Background(), "node-a", "re-enrol"); !errors.Is(err, errRemoteLost) {
		t.Fatalf("rejected Close() error = %v, want %v", err, errRemoteLost)
	}
}
