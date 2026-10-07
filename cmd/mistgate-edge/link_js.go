//go:build js && wasm

package main

import (
	"context"
	"errors"
	"syscall/js"

	"github.com/mistgate/mistgate/internal/panel/fleet"
)

var (
	errLinkRequest = errors.New("mgPanel.link requires an operation name and an arguments object")
	errLinkOp      = errors.New("mgPanel.link: unknown operation")
	errLinkArgs    = errors.New("mgPanel.link: invalid arguments")
	errLinkStep    = errors.New("mgPanel.link: step not implemented")
)

// mgPanel.link(op, args) runs one step of the agent link for the NodeLink Durable Object (edge/worker/src/nodelink.ts),
// which only holds the socket. Bytes cross as Uint8Array.
//
//	"challenge" {nodeId, audience}              -> {nonce, frame}
//	"accept"    {nodeId, audience, nonce, auth} -> {ok: true, frame, certSerial, certNotAfterUnix} | {ok: false}
//	"step"      the session step: not implemented yet
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
	in := args[1]
	switch op := args[0].String(); op {
	case "challenge":
		audience := in.Get("audience")
		if in.Get("nodeId").Type() != js.TypeString || audience.Type() != js.TypeString {
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
		if nodeID.Type() != js.TypeString || audience.Type() != js.TypeString || nonceErr != nil || authErr != nil || nonce == nil || auth == nil {
			return js.Undefined(), errLinkArgs
		}
		serial, notAfter, frame, ok := current.fleet.LinkAccept(context.Background(), nodeID.String(), audience.String(), nonce, auth)
		out := js.Global().Get("Object").New()
		out.Set("ok", ok)
		if ok {
			out.Set("frame", bytesToJS(frame))
			out.Set("certSerial", serial)
			out.Set("certNotAfterUnix", notAfter.Unix())
		}
		return out, nil
	case "step":
		return js.Undefined(), errLinkStep
	default:
		return js.Undefined(), errLinkOp
	}
}
