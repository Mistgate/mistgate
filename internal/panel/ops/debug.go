//go:build !js

package ops

import (
	"context"
	"errors"
	"strings"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/logscrub"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// On-demand debug capture of one node (agent.proto "DEBUG CAPTURE"). The fleet starts and stops it (it owns the node sessions
// and the audit entry); this file is the API: the four procedures, and the read, which passes every record through the panel's
// scrubbers once more.

// DebugBackend is what the service needs of the fleet.
type DebugBackend interface {
	StartDebugCapture(ctx context.Context, in fleet.DebugStart) (store.DebugCaptureRow, error)
	StopDebugCapture(ctx context.Context, captureID string) (store.DebugCaptureRow, error)
}

// debugAttrs are the attribute keys a capture record may carry: the node journal's.
var debugAttrs = func() map[string]bool {
	out := map[string]bool{}
	for _, k := range logscrub.JournalAttrs {
		out[k] = true
	}
	return out
}()

var debugLevels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}
var debugLevelNames = [...]string{"debug", "info", "warn", "error"}

func (r rpc) debug() (DebugBackend, error) {
	if r.s.cfg.Debug == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not available on this edition"))
	}
	return r.s.cfg.Debug, nil
}

func (s *Service) debugInfo(row store.DebugCaptureRow) *opsv1.DebugCaptureInfo {
	out := &opsv1.DebugCaptureInfo{
		CaptureId: row.ID, NodeId: row.NodeID, NodeName: s.scrub.Value(row.NodeName, 64), State: row.State, Level: row.Level,
		DurationS: uint32(max(row.DurationS, 0)), StartedUnix: row.StartedAt.Unix(), EndsUnix: row.EndsAt.Unix(),
		Actor: s.scrub.Value(row.Actor, 64), Records: uint32(max(row.Records, 0)), Dropped: uint32(max(row.Dropped, 0)), EndReason: row.EndReason,
	}
	for _, src := range row.Sources {
		out.Sources = append(out.Sources, s.scrub.Value(src, 32))
	}
	if row.State == "running" && !s.cfg.Now().Before(row.EndsAt) {
		out.EndReason = "" // the time is up; the node is shipping
	}
	return out
}

func (r rpc) StartDebugCapture(ctx context.Context, req *connect.Request[opsv1.StartDebugCaptureRequest]) (*connect.Response[opsv1.StartDebugCaptureResponse], error) {
	be, err := r.debug()
	if err != nil {
		return nil, err
	}
	m := req.Msg
	row, err := be.StartDebugCapture(ctx, fleet.DebugStart{NodeID: m.NodeId, Level: m.Level, Sources: m.Sources, DurationS: m.DurationS})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&opsv1.StartDebugCaptureResponse{Capture: r.s.debugInfo(row)}), nil
}

func (r rpc) StopDebugCapture(ctx context.Context, req *connect.Request[opsv1.StopDebugCaptureRequest]) (*connect.Response[opsv1.StopDebugCaptureResponse], error) {
	be, err := r.debug()
	if err != nil {
		return nil, err
	}
	row, err := be.StopDebugCapture(ctx, req.Msg.CaptureId)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&opsv1.StopDebugCaptureResponse{Capture: r.s.debugInfo(row)}), nil
}

func (r rpc) ListDebugCaptures(ctx context.Context, req *connect.Request[opsv1.ListDebugCapturesRequest]) (*connect.Response[opsv1.ListDebugCapturesResponse], error) {
	s := r.s
	rows, err := s.cfg.Store.DebugCaptures(ctx, req.Msg.NodeId, s.cfg.Now())
	if err != nil {
		return nil, internalErr(s.cfg.Log, "list debug captures", err)
	}
	resp := &opsv1.ListDebugCapturesResponse{}
	for _, row := range rows {
		resp.Captures = append(resp.Captures, s.debugInfo(row))
	}
	return connect.NewResponse(resp), nil
}

const (
	debugPage        = 500
	debugPagesMax    = 50
	debugDefaultRead = 100
)

func (r rpc) GetDebugCapture(ctx context.Context, req *connect.Request[opsv1.GetDebugCaptureRequest]) (*connect.Response[opsv1.GetDebugCaptureResponse], error) {
	s := r.s
	m := req.Msg
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
	if (m.CaptureId == "") == (m.NodeId == "") {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("name a capture or a node"))
	}
	var row store.DebugCaptureRow
	var err error
	if m.CaptureId != "" {
		row, err = s.cfg.Store.DebugCapture(ctx, m.CaptureId)
	} else {
		row, err = s.cfg.Store.LatestDebugCapture(ctx, m.NodeId, s.cfg.Now())
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such capture"))
	}
	if err != nil {
		return nil, internalErr(s.cfg.Log, "read debug capture", err)
	}
	if row.State == "running" && !row.Running(s.cfg.Now()) {
		row.State, row.EndReason = "expired", "no_report"
	}
	limit := int(m.Limit)
	if limit == 0 {
		limit = debugDefaultRead
	}
	limit = min2(limit, logsMaxLimit)
	query := strings.ToLower(m.Query)
	resp := &opsv1.GetDebugCaptureResponse{Capture: s.debugInfo(row)}
	after := max(m.AfterSeq, 0)
	filter := store.DebugLineFilter{MinLevel: min, Source: m.Source, Limit: debugPage}
	var sources []string
read:
	for page := 0; page < debugPagesMax; page++ {
		filter.AfterSeq = after
		lines, srcs, err := s.cfg.Store.DebugLines(ctx, row.ID, filter)
		if err != nil {
			return nil, internalErr(s.cfg.Log, "read debug capture lines", err)
		}
		sources = srcs
		more := len(lines) > debugPage
		if more {
			lines = lines[:debugPage]
		}
		for i := range lines {
			after = lines[i].Seq
			e := s.debugRecord(&lines[i])
			// the query is matched against the cleaned text, like panel_logs: a search must not tell whether a secret is in a record
			if query != "" && !recordContains(e, query) {
				continue
			}
			if len(resp.Records) == limit {
				resp.NextCursor = resp.Records[len(resp.Records)-1].Seq
				break read
			}
			resp.Records = append(resp.Records, e)
		}
		if !more {
			break
		}
	}
	for _, src := range sources {
		resp.Sources = append(resp.Sources, s.scrub.Value(src, 32))
	}
	return connect.NewResponse(resp), nil
}

// debugRecord is one stored record as it may leave the panel: cleaned again with the panel's secret names, only the journal's
// attribute keys, names cut from every value.
func (s *Service) debugRecord(l *store.DebugLineRow) *opsv1.DebugRecord {
	level := "info"
	if l.Level >= 0 && l.Level < len(debugLevelNames) {
		level = debugLevelNames[l.Level]
	}
	e := &opsv1.DebugRecord{Seq: l.Seq, TimeUnixMs: l.AtMs, Level: level, Source: s.scrub.Value(l.Source, 32), Message: s.scrub.Text(l.Message)}
	for k, v := range l.Attrs {
		if !debugAttrs[k] {
			continue
		}
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		e.Attrs[k] = s.scrub.Value(v, attrValueLen)
	}
	return e
}

func recordContains(e *opsv1.DebugRecord, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(e.Message), lowerQuery) {
		return true
	}
	for _, v := range e.Attrs {
		if strings.Contains(strings.ToLower(v), lowerQuery) {
			return true
		}
	}
	return false
}
