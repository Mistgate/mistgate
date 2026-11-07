//go:build !js

package ops

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/logring"
)

const (
	logsDefaultLimit = 100
	logsMaxLimit     = 500
	logsMaxQuery     = 100
	attrValueLen     = 200
)

func (r rpc) GetLogSettings(context.Context, *connect.Request[opsv1.GetLogSettingsRequest]) (*connect.Response[opsv1.GetLogSettingsResponse], error) {
	return connect.NewResponse(r.s.logSettings()), nil
}

func (s *Service) logSettings() *opsv1.GetLogSettingsResponse {
	out := &opsv1.GetLogSettingsResponse{Level: "info", DefaultLevel: logring.LevelName(s.cfg.DefaultLevel), Format: s.cfg.Format}
	if out.Format == "" {
		out.Format = "text"
	}
	if s.cfg.Level != nil {
		out.Level = logring.LevelName(s.cfg.Level.Level())
	}
	if s.cfg.Ring != nil {
		out.Buffered, out.Capacity = uint32(s.cfg.Ring.Len()), uint32(s.cfg.Ring.Cap())
	}
	return out
}

func (r rpc) SetLogLevel(ctx context.Context, req *connect.Request[opsv1.SetLogLevelRequest]) (*connect.Response[opsv1.SetLogLevelResponse], error) {
	s := r.s
	level, ok := logring.ParseLevel(req.Msg.Level)
	if !ok {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("level must be debug, info, warn or error"))
	}
	if s.cfg.Level == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this panel has no adjustable log level"))
	}
	old := logring.LevelName(s.cfg.Level.Level())
	s.cfg.Level.Set(level)
	now := logring.LevelName(level)
	if old != now {
		s.cfg.Log.Warn("ops: log level changed until restart", "code", "log_level_changed", "from", old, "to", now)
		s.audit(ctx, "log_level_set", map[string]string{"from": old, "to": now})
	}
	return connect.NewResponse(&opsv1.SetLogLevelResponse{Level: now}), nil
}

func (r rpc) GetPanelLogs(_ context.Context, req *connect.Request[opsv1.GetPanelLogsRequest]) (*connect.Response[opsv1.GetPanelLogsResponse], error) {
	s := r.s
	m := req.Msg
	min := slog.LevelInfo
	if m.MinLevel != "" {
		l, ok := logring.ParseLevel(m.MinLevel)
		if !ok {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("min_level must be debug, info, warn or error"))
		}
		min = l
	}
	if len([]rune(m.Query)) > logsMaxQuery {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("query is at most 100 characters"))
	}
	limit := int(m.Limit)
	if limit == 0 {
		limit = logsDefaultLimit
	}
	limit = min2(limit, logsMaxLimit)
	set := s.logSettings()
	resp := &opsv1.GetPanelLogsResponse{Level: set.Level, Buffered: set.Buffered, Capacity: set.Capacity}
	if s.cfg.Ring == nil {
		return connect.NewResponse(resp), nil
	}
	component, code, rid := strings.ToLower(m.Component), strings.ToLower(m.Code), m.RequestId
	query := strings.ToLower(m.Query)
	recs := s.cfg.Ring.Snapshot(m.BeforeSeq, 0, func(rec *logring.Record) bool {
		switch {
		case rec.Level < min,
			m.SinceUnix > 0 && rec.Time.Unix() < m.SinceUnix,
			m.UntilUnix > 0 && rec.Time.Unix() > m.UntilUnix,
			component != "" && strings.ToLower(rec.Component) != component,
			code != "" && strings.ToLower(rec.Code) != code,
			rid != "" && rec.RequestID != rid:
			return false
		}
		return true
	})
	for i := range recs {
		e := s.logEntry(&recs[i])
		// The query is matched against the cleaned text: a substring search must not tell whether a secret or an address is in a record.
		if query != "" && !entryContains(e, query) {
			continue
		}
		if len(resp.Entries) == limit {
			resp.NextCursor = resp.Entries[len(resp.Entries)-1].Seq
			break
		}
		resp.Entries = append(resp.Entries, e)
	}
	return connect.NewResponse(resp), nil
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// logEntry is one record as it may leave the panel: cleaned text, allow-listed attributes with cleaned values.
func (s *Service) logEntry(rec *logring.Record) *opsv1.PanelLogEntry {
	e := &opsv1.PanelLogEntry{
		Seq: rec.Seq, TimeUnixMs: rec.Time.UnixMilli(), Level: logring.LevelName(rec.Level),
		Component: s.scrub.Value(rec.Component, 40), Message: s.scrub.Text(rec.Message),
		Code: s.scrub.Value(rec.Code, 64), RequestId: s.scrub.Value(rec.RequestID, 32),
	}
	for _, a := range rec.Attrs {
		if !allowedAttrs[a.Key] {
			continue
		}
		if e.Attrs == nil {
			e.Attrs = map[string]string{}
		}
		e.Attrs[a.Key] = s.scrub.Value(a.Value, attrValueLen)
	}
	return e
}

func entryContains(e *opsv1.PanelLogEntry, lowerQuery string) bool {
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

// logRows are the ring's records in [since, until] as cleaned entries, oldest first, for the export.
func (s *Service) logRows(since, until time.Time) []*opsv1.PanelLogEntry {
	if s.cfg.Ring == nil {
		return nil
	}
	recs := s.cfg.Ring.Snapshot(0, 0, func(rec *logring.Record) bool { return !rec.Time.Before(since) && !rec.Time.After(until) })
	out := make([]*opsv1.PanelLogEntry, len(recs))
	for i := range recs {
		out[len(recs)-1-i] = s.logEntry(&recs[i])
	}
	return out
}
