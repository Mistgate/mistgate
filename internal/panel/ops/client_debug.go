//go:build !js

package ops

import (
	"context"
	"errors"
	"strconv"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/clientdebug"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The owner's side of the logs people send for support: asking a device for them, withdrawing the request, and the list. The
// device's side (sync, the delivery, the refusal) is internal/panel/subs with internal/panel/clientdebug. Reading what came is
// a later step; so is the MCP's list.

func (s *Service) clientDebugInfo(row store.ClientDebugRow) *opsv1.ClientDebugInfo {
	out := &opsv1.ClientDebugInfo{
		Id: row.ID, UserId: row.UserID, UserName: s.scrub.Value(row.UserName, 64), DeviceId: row.DeviceID,
		DevicePlatform: s.scrub.Value(row.DevicePlatform, 32), DeviceModel: s.scrub.Value(row.DeviceModel, 64),
		State: row.State, Level: row.Level, Note: row.Note, Unasked: row.Unasked, Actor: s.scrub.Value(row.ActorName, 64),
		RequestedUnix: row.RequestedAt.Unix(), ExpiresUnix: row.ExpiresAt.Unix(),
		Records: uint32(max(row.Records, 0)), Bytes: uint32(max(row.Bytes, 0)), DroppedAttrs: uint32(max(row.DroppedKV, 0)),
		FirstUnixMs: row.FirstMs, LastUnixMs: row.LastMs, App: s.scrub.Value(row.App, 48),
	}
	if !row.ClosedAt.IsZero() {
		out.ClosedUnix = row.ClosedAt.Unix()
	}
	if !row.ReadAt.IsZero() {
		out.ReadUnix = row.ReadAt.Unix()
	}
	return out
}

func (r rpc) RequestClientDebug(ctx context.Context, req *connect.Request[opsv1.RequestClientDebugRequest]) (*connect.Response[opsv1.RequestClientDebugResponse], error) {
	s, m := r.s, req.Msg
	level := m.Level
	if level == "" {
		level = "info"
	}
	if level != "info" && level != "debug" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("level must be info or debug"))
	}
	if m.DeviceId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name a device"))
	}
	actor := "anonymous"
	if a, ok := auth.AdminFrom(ctx); ok {
		actor = a.ID
	}
	row, err := s.cfg.Store.CreateClientDebug(ctx, store.ClientDebugRow{ID: store.NewClientDebugID(), DeviceID: m.DeviceId, Actor: actor,
		Note: clientdebug.Note(m.Note), Level: level}, s.cfg.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such device with a key"))
	case errors.Is(err, store.ErrClientDebugOpen):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this device already has an open request"))
	case err != nil:
		return nil, internalErr(s.cfg.Log, "request client logs", err)
	}
	s.audit(ctx, "client_debug_requested", map[string]string{"user": row.UserID, "device": row.DeviceID, "id": row.ID, "level": row.Level,
		"expires": strconv.FormatInt(row.ExpiresAt.Unix(), 10)})
	return connect.NewResponse(&opsv1.RequestClientDebugResponse{Request: s.clientDebugInfo(row)}), nil
}

func (r rpc) CancelClientDebug(ctx context.Context, req *connect.Request[opsv1.CancelClientDebugRequest]) (*connect.Response[opsv1.CancelClientDebugResponse], error) {
	s := r.s
	row, err := s.cfg.Store.CancelClientDebug(ctx, req.Msg.Id, s.cfg.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such request"))
	case errors.Is(err, store.ErrClientDebugClosed):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the request is not open"))
	case err != nil:
		return nil, internalErr(s.cfg.Log, "cancel client logs request", err)
	}
	s.audit(ctx, "client_debug_cancelled", map[string]string{"user": row.UserID, "device": row.DeviceID, "id": row.ID})
	return connect.NewResponse(&opsv1.CancelClientDebugResponse{Request: s.clientDebugInfo(row)}), nil
}

func (r rpc) ListClientDebug(ctx context.Context, req *connect.Request[opsv1.ListClientDebugRequest]) (*connect.Response[opsv1.ListClientDebugResponse], error) {
	s := r.s
	rows, err := s.cfg.Store.ClientDebugList(ctx, req.Msg.UserId, s.cfg.Now())
	if err != nil {
		return nil, internalErr(s.cfg.Log, "list client logs", err)
	}
	resp := &opsv1.ListClientDebugResponse{}
	for _, row := range rows {
		resp.Items = append(resp.Items, s.clientDebugInfo(row))
	}
	return connect.NewResponse(resp), nil
}
