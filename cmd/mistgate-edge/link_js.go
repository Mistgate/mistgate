//go:build js && wasm

package main

import (
	"context"
	"errors"
	"math"
	"syscall/js"
	"time"

	"github.com/mistgate/mistgate/internal/panel/fleet"
)

var (
	errLinkRequest = errors.New("mgPanel.link requires an operation name and an arguments object")
	errLinkOp      = errors.New("mgPanel.link: unknown operation")
	errLinkArgs    = errors.New("mgPanel.link: invalid arguments")
	errLinkBudget  = errors.New("link step budget spent")
)

type linkStepNode struct{}

// mgPanel.link(op, args) runs one step of the agent link for the NodeLink Durable Object (edge/worker/src/nodelink.ts),
// which only holds the socket. Bytes cross as Uint8Array.
//
//	"challenge" {audience}                                        -> {nonce, frame}
//	"accept"    {nodeId, audience, nonce, auth, until}             -> {ok: true, frame, certSerial, certNotAfterUnix} | {ok: false}
//	"step"      {nodeId, state, until, event}                      -> LinkOut plus waitUntil
func linkFunc() js.Func {
	return js.FuncOf(func(_ js.Value, args []js.Value) any {
		return promise(func() (js.Value, error) { return link(args) })
	})
}

func link(args []js.Value) (js.Value, error) {
	if len(args) != 2 || args[0].Type() != js.TypeString || args[1].Type() != js.TypeObject || args[1].IsNull() {
		return js.Undefined(), errLinkRequest
	}
	stateMu.RLock()
	current := state
	stateMu.RUnlock()
	if current == nil {
		return js.Undefined(), errNotInitialized
	}
	defer current.afterResponse.release()
	in := args[1]
	switch op := args[0].String(); op {
	case "challenge":
		audience := in.Get("audience")
		if audience.Type() != js.TypeString {
			return js.Undefined(), errLinkArgs
		}
		nonce, frame, err := fleet.LinkChallenge(audience.String())
		if err != nil {
			return js.Undefined(), err
		}
		out := js.Global().Get("Object").New()
		out.Set("nonce", bytesToJS(nonce))
		out.Set("frame", bytesToJS(frame))
		return out, nil
	case "accept":
		nodeID, audience := in.Get("nodeId"), in.Get("audience")
		nonce, nonceErr := bytesFromJS(in.Get("nonce"))
		auth, authErr := bytesFromJS(in.Get("auth"))
		untilMS, untilErr := positiveMillis(in.Get("until"))
		if nodeID.Type() != js.TypeString || audience.Type() != js.TypeString || nonceErr != nil || authErr != nil ||
			nonce == nil || auth == nil || untilErr != nil {
			return js.Undefined(), errLinkArgs
		}
		ctx, cancel, err := linkContext(nodeID.String(), time.UnixMilli(untilMS))
		if err != nil {
			return js.Undefined(), err
		}
		defer cancel()
		serial, notAfter, frame, ok := current.fleet.LinkAccept(ctx, nodeID.String(), audience.String(), nonce, auth)
		out := js.Global().Get("Object").New()
		out.Set("ok", ok)
		if ok {
			out.Set("frame", bytesToJS(frame))
			out.Set("certSerial", serial)
			out.Set("certNotAfterUnix", notAfter.Unix())
		}
		return out, nil
	case "step":
		step, until, err := stepFromJS(in)
		if err != nil {
			return js.Undefined(), err
		}
		ctx, cancel, err := linkContext(step.NodeID, until)
		if err != nil {
			return js.Undefined(), err
		}
		defer cancel()
		result, err := current.fleet.Link(ctx, step)
		if err != nil {
			return js.Undefined(), err
		}
		return linkOutToJS(result, current.afterResponse), nil
	default:
		return js.Undefined(), errLinkOp
	}
}

func stepFromJS(value js.Value) (fleet.LinkIn, time.Time, error) {
	var out fleet.LinkIn
	if value.Type() != js.TypeObject || value.IsNull() {
		return out, time.Time{}, errLinkArgs
	}
	nodeID, state, event := value.Get("nodeId"), value.Get("state"), value.Get("event")
	if nodeID.Type() != js.TypeString || nodeID.String() == "" ||
		(state.Type() != js.TypeString && !state.IsNull()) || event.Type() != js.TypeObject || event.IsNull() {
		return out, time.Time{}, errLinkArgs
	}
	out.NodeID = nodeID.String()
	if state.Type() == js.TypeString {
		out.State = state.String()
	}
	untilMS, err := positiveMillis(value.Get("until"))
	if err != nil {
		return fleet.LinkIn{}, time.Time{}, errLinkArgs
	}
	atMS, err := positiveMillis(event.Get("at"))
	if err != nil {
		return fleet.LinkIn{}, time.Time{}, errLinkArgs
	}
	out.At = time.UnixMilli(atMS)
	kind := event.Get("kind")
	if kind.Type() != js.TypeString {
		return fleet.LinkIn{}, time.Time{}, errLinkArgs
	}
	switch out.Kind = fleet.LinkKind(kind.String()); out.Kind {
	case fleet.LinkOpen:
		generation, ok := safeGeneration(event.Get("generation"))
		serial, notAfter := event.Get("certSerial"), event.Get("certNotAfterUnix")
		notAfterUnix, notAfterErr := integerInt64(notAfter)
		if !ok || serial.Type() != js.TypeString || notAfterErr != nil {
			return fleet.LinkIn{}, time.Time{}, errLinkArgs
		}
		out.Generation = generation
		out.CertSerial = serial.String()
		out.CertNotAfter = time.Unix(notAfterUnix, 0)
	case fleet.LinkFrame, fleet.LinkRequest:
		frame, frameErr := requiredBytes(event.Get("frame"))
		if frameErr != nil {
			return fleet.LinkIn{}, time.Time{}, errLinkArgs
		}
		out.Frame = frame
		if out.Kind == fleet.LinkRequest {
			requestID := event.Get("requestId")
			deadlineMS, deadlineErr := positiveMillis(event.Get("deadlineAt"))
			if requestID.Type() != js.TypeString || requestID.String() == "" || deadlineErr != nil {
				return fleet.LinkIn{}, time.Time{}, errLinkArgs
			}
			out.RequestID = requestID.String()
			out.DeadlineAt = time.UnixMilli(deadlineMS)
		}
	case fleet.LinkAlarm, fleet.LinkDesired, fleet.LinkClosed:
		// closed.owned is deliberately ignored: a close can follow a reset or deploy too.
	default:
		return fleet.LinkIn{}, time.Time{}, errLinkArgs
	}
	return out, time.UnixMilli(untilMS), nil
}

func linkOutToJS(value fleet.LinkOut, afterResponse *edgeTaskRunner) js.Value {
	out := js.Global().Get("Object").New()
	out.Set("state", value.State)
	frames := js.Global().Get("Array").New(len(value.Frames))
	for i, frame := range value.Frames {
		frames.SetIndex(i, bytesToJS(frame))
	}
	out.Set("frames", frames)
	if value.Close != nil {
		close := js.Global().Get("Object").New()
		close.Set("code", value.Close.Code)
		close.Set("reason", value.Close.Reason)
		out.Set("close", close)
	}
	if !value.AlarmAt.IsZero() {
		out.Set("alarmAt", value.AlarmAt.UnixMilli())
	}
	if len(value.Replies) != 0 {
		replies := js.Global().Get("Array").New(len(value.Replies))
		for i, reply := range value.Replies {
			item := js.Global().Get("Object").New()
			item.Set("requestId", reply.RequestID)
			if reply.Frame == nil {
				item.Set("frame", js.Null())
			} else {
				item.Set("frame", bytesToJS(reply.Frame))
			}
			replies.SetIndex(i, item)
		}
		out.Set("replies", replies)
	}
	if value.Forget {
		out.Set("forget", true)
	}
	out.Set("waitUntil", afterResponse.WaitUntil())
	return out
}

func linkContext(nodeID string, until time.Time) (context.Context, context.CancelFunc, error) {
	now := time.Now()
	deadline := now.Add(15 * time.Second)
	blockDeadline := until.Add(-2 * time.Second)
	if blockDeadline.Before(deadline) {
		deadline = blockDeadline
	}
	if !deadline.After(now) {
		return nil, nil, errLinkBudget
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	return context.WithValue(ctx, linkStepNode{}, nodeID), cancel, nil
}

func requiredBytes(value js.Value) ([]byte, error) {
	if value.Type() != js.TypeObject || value.IsNull() || !value.InstanceOf(js.Global().Get("Uint8Array")) {
		return nil, errLinkArgs
	}
	return bytesFromJS(value)
}

func positiveMillis(value js.Value) (int64, error) {
	n, err := integerInt64(value)
	if err != nil || n <= 0 {
		return 0, errLinkArgs
	}
	return n, nil
}

func integerInt64(value js.Value) (int64, error) {
	if value.Type() != js.TypeNumber {
		return 0, errLinkArgs
	}
	n := value.Float()
	const int64Limit = float64(1 << 63)
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < -int64Limit || n >= int64Limit {
		return 0, errLinkArgs
	}
	return int64(n), nil
}

func safeGeneration(value js.Value) (uint64, bool) {
	if value.Type() != js.TypeNumber {
		return 0, false
	}
	n := value.Float()
	const maxSafeInteger = float64(1<<53 - 1)
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 1 || n > maxSafeInteger {
		return 0, false
	}
	return uint64(n), true
}
