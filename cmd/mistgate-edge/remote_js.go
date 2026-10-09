//go:build js && wasm

package main

import (
	"context"
	"errors"
	"log/slog"
	"syscall/js"
	"time"

	"github.com/mistgate/mistgate/edge/d1driver"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/proto"
)

var (
	errRemoteLost = errors.New("link lost")
	errOwnNode    = errors.New("edge Remote call targets its own node")
)

type edgeRemote struct {
	ask   js.Value
	close js.Value
}

var _ fleet.Remote = (*edgeRemote)(nil)

func (r *edgeRemote) Ask(ctx context.Context, nodeID, requestID string, frame *agentv1.ConnectResponse, deadline time.Time) (*agentv1.ConnectRequest, error) {
	if err := r.checkNode(ctx, nodeID); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.ask.Type() != js.TypeFunction || frame == nil {
		return nil, errRemoteLost
	}
	encoded, err := proto.Marshal(frame)
	if err != nil {
		return nil, errRemoteLost
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	promise, err := invokeRemote(r.ask, nodeID, requestID, bytesToJS(encoded), deadline.UnixMilli())
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errRemoteLost
	}
	result, err := d1driver.Await(ctx, promise)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errRemoteLost
	}
	if result.Type() != js.TypeObject || result.IsNull() {
		return nil, errRemoteLost
	}
	if outcome := result.Get("error"); outcome.Type() == js.TypeString {
		if outcome.String() == "timeout" {
			return nil, context.DeadlineExceeded
		}
		return nil, errRemoteLost
	}
	reply := result.Get("reply")
	if reply.IsNull() {
		return nil, nil
	}
	encoded, err = requiredBytes(reply)
	if err != nil {
		return nil, errRemoteLost
	}
	var request agentv1.ConnectRequest
	if err := proto.Unmarshal(encoded, &request); err != nil {
		return nil, errRemoteLost
	}
	return &request, nil
}

func (r *edgeRemote) Close(ctx context.Context, nodeID, reason string) error {
	if err := r.checkNode(ctx, nodeID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.close.Type() != js.TypeFunction {
		return errRemoteLost
	}
	promise, err := invokeRemote(r.close, nodeID, store.Clip(reason, 123))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errRemoteLost
	}
	if _, err := d1driver.Await(ctx, promise); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errRemoteLost
	}
	return nil
}

func (r *edgeRemote) checkNode(ctx context.Context, nodeID string) error {
	if current, ok := ctx.Value(linkStepNode{}).(string); ok && current != "" && current == nodeID {
		slog.Error("refusing edge Remote call to its own node", "node", nodeID)
		return errOwnNode
	}
	return nil
}

func invokeRemote(fn js.Value, args ...any) (value js.Value, err error) {
	defer func() {
		if recover() != nil {
			value = js.Undefined()
			err = errRemoteLost
		}
	}()
	return fn.Invoke(args...), nil
}
