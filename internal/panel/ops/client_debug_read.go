//go:build !js

package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/clientdebug"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Reading, downloading and deleting the logs people send for support (the owner's; see client_debug.go for asking).
//
// A bundle is read as it was stored. It was cleaned when it came (clientdebug.Cleaner), and the node capture's read-time scrubber
// must not run over it: that one is made for a node's journal and turns a file name such as state.json into <host> and a long path
// into <key>, which is what the owner needs to see in a person's log. Neither does debugRecord, which keeps only the node journal's
// attribute keys and would drop path, server, via, engine, phase, ms, bytes, attempt, net, status and http. What the read adds is the
// panel's secret names as they are now, and the attribute keys of the contract (clientdebug.KVKeys), nothing else.

// clientDebugRecord is one stored record of a bundle as it leaves: the stored text with the panel's current secret names cut out,
// and only the attribute keys the contract keeps. The search and the download use this same record.
func (s *Service) clientDebugRecord(l *store.DebugLineRow) *opsv1.DebugRecord {
	level := "info"
	if l.Level >= 0 && l.Level < len(debugLevelNames) {
		level = debugLevelNames[l.Level]
	}
	e := &opsv1.DebugRecord{Seq: l.Seq, TimeUnixMs: l.AtMs, Level: level, Source: s.scrub.Secrets(l.Source), Message: s.scrub.Secrets(l.Message)}
	for k, v := range l.Attrs {
		if !clientdebug.KVKeys[k] {
			continue
		}
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		e.Attrs[k] = s.scrub.Secrets(v)
	}
	return e
}

// receivedClientDebug is the row a read or a download is about: NotFound, or FailedPrecondition when the request holds no logs.
func (s *Service) receivedClientDebug(ctx context.Context, id string) (store.ClientDebugRow, error) {
	row, err := s.cfg.Store.ClientDebug(ctx, id, s.cfg.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return row, connect.NewError(connect.CodeNotFound, errors.New("no such request"))
	case err != nil:
		return row, internalErr(s.cfg.Log, "read client logs", err)
	case row.State != "received":
		return row, connect.NewError(connect.CodeFailedPrecondition, errors.New("this request holds no logs"))
	}
	return row, nil
}

func adminID(ctx context.Context) string {
	if a, ok := auth.AdminFrom(ctx); ok {
		return a.ID
	}
	return "anonymous"
}

func (r rpc) GetClientDebug(ctx context.Context, req *connect.Request[opsv1.GetClientDebugRequest]) (*connect.Response[opsv1.GetClientDebugResponse], error) {
	s, m := r.s, req.Msg
	min := 0
	if m.MinLevel != "" {
		l, ok := debugLevels[strings.ToLower(m.MinLevel)]
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("min_level must be debug, info, warn or error"))
		}
		min = l
	}
	if len([]rune(m.Query)) > logsMaxQuery {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query is at most 100 characters"))
	}
	if m.FromMs < 0 || m.ToMs < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("from_ms and to_ms are not negative"))
	}
	row, err := s.receivedClientDebug(ctx, m.Id)
	if err != nil {
		return nil, err
	}
	limit := int(m.Limit)
	if limit == 0 {
		limit = debugDefaultRead
	}
	filter := store.DebugLineFilter{MinLevel: min, Source: m.Source, AfterSeq: max(m.AfterSeq, 0), FromMs: m.FromMs, ToMs: m.ToMs, Limit: debugPage}
	records, next, sources, err := s.readLines(ctx, row.ID, filter, s.cfg.Store.ClientDebugLines, s.clientDebugRecord, strings.ToLower(m.Query), min2(limit, logsMaxLimit))
	if err != nil {
		return nil, internalErr(s.cfg.Log, "read client logs lines", err)
	}
	// The first read of a bundle is the one that is audit-logged (a page, a filter and a search after it are not).
	now := s.cfg.Now()
	switch first, err := s.cfg.Store.MarkClientDebugRead(ctx, row.ID, adminID(ctx), now); {
	case err != nil:
		return nil, internalErr(s.cfg.Log, "mark client logs read", err)
	case first:
		row.ReadAt = now
		s.audit(ctx, "client_debug_read", map[string]string{"user": row.UserID, "device": row.DeviceID, "id": row.ID})
	}
	resp := &opsv1.GetClientDebugResponse{Bundle: s.clientDebugInfo(row), Records: records, NextCursor: next}
	for _, src := range sources {
		resp.Sources = append(resp.Sources, s.scrub.Secrets(src))
	}
	return connect.NewResponse(resp), nil
}

// exportedLine is one record of the download: the delivery's own line shape (ts, lvl, src, msg, kv) and the record's number.
type exportedLine struct {
	Seq int64             `json:"seq"`
	Ts  int64             `json:"ts"`
	Lvl string            `json:"lvl"`
	Src string            `json:"src"`
	Msg string            `json:"msg"`
	KV  map[string]string `json:"kv,omitempty"`
}

func (r rpc) ExportClientDebug(ctx context.Context, req *connect.Request[opsv1.ExportClientDebugRequest], stream *connect.ServerStream[opsv1.ExportClientDebugResponse]) error {
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return err
	}
	row, err := s.receivedClientDebug(ctx, req.Msg.Id)
	if err != nil {
		return err
	}
	// A download is audit-logged before the first byte leaves: one that is cut off half way has still shown the owner the lines.
	s.audit(ctx, "client_debug_downloaded", map[string]string{"user": row.UserID, "device": row.DeviceID, "id": row.ID, "records": strconv.Itoa(row.Records)})
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	flush := func() error {
		if buf.Len() == 0 {
			return nil
		}
		err := stream.Send(&opsv1.ExportClientDebugResponse{Data: bytes.Clone(buf.Bytes())})
		buf.Reset()
		return err
	}
	filter := store.DebugLineFilter{Limit: debugPage}
	for page := 0; page < debugPagesMax; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lines, _, err := s.cfg.Store.ClientDebugLines(ctx, row.ID, filter)
		if err != nil {
			return internalErr(s.cfg.Log, "export client logs", err)
		}
		more := len(lines) > debugPage
		if more {
			lines = lines[:debugPage]
		}
		for i := range lines {
			e := s.clientDebugRecord(&lines[i])
			if err := enc.Encode(exportedLine{Seq: e.Seq, Ts: e.TimeUnixMs, Lvl: e.Level, Src: e.Source, Msg: e.Message, KV: e.Attrs}); err != nil {
				return internalErr(s.cfg.Log, "export client logs", err)
			}
			if buf.Len() >= exportChunk {
				if err := flush(); err != nil {
					return err
				}
			}
			filter.AfterSeq = lines[i].Seq
		}
		if !more {
			break
		}
	}
	return flush()
}

func (r rpc) DeleteClientDebug(ctx context.Context, req *connect.Request[opsv1.DeleteClientDebugRequest]) (*connect.Response[opsv1.DeleteClientDebugResponse], error) {
	s := r.s
	row, err := s.cfg.Store.DeleteClientDebug(ctx, req.Msg.Id, s.cfg.Now())
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such request"))
	case errors.Is(err, store.ErrClientDebugStillOpen):
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the request is still open: cancel it"))
	case err != nil:
		return nil, internalErr(s.cfg.Log, "delete client logs", err)
	}
	s.audit(ctx, "client_debug_deleted", map[string]string{"user": row.UserID, "device": row.DeviceID, "id": row.ID, "state": row.State, "records": strconv.Itoa(row.Records)})
	return connect.NewResponse(&opsv1.DeleteClientDebugResponse{}), nil
}
