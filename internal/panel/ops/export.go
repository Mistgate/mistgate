//go:build !js

package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	exportMaxWindow = 31 * 24 * time.Hour
	exportChunk     = 64 << 10
	exportMaxRows   = 100000 // per source; a source that hits it ends with a "truncated" line
	exportPage      = 200
)

var exportSources = []string{"events", "audit", "journal", "logs"}

func (r rpc) ExportData(ctx context.Context, req *connect.Request[opsv1.ExportDataRequest], stream *connect.ServerStream[opsv1.ExportDataResponse]) error {
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return err
	}
	now := s.cfg.Now().UTC()
	until, since := now, now.Add(-24*time.Hour)
	if req.Msg.UntilUnix > 0 {
		until = time.Unix(req.Msg.UntilUnix, 0).UTC()
	}
	if req.Msg.SinceUnix > 0 {
		since = time.Unix(req.Msg.SinceUnix, 0).UTC()
	} else if req.Msg.UntilUnix > 0 {
		since = until.Add(-24 * time.Hour)
	}
	if !since.Before(until) || until.Sub(since) > exportMaxWindow {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the window must be positive and at most 31 days"))
	}
	sources := req.Msg.Sources
	if len(sources) == 0 {
		sources = exportSources
	}
	for _, src := range sources {
		if !slices.Contains(exportSources, src) {
			return connect.NewError(connect.CodeInvalidArgument, errors.New("sources are events, audit, journal and logs"))
		}
	}
	w := &exportWriter{ctx: ctx, stream: stream, scrub: s.scrub}
	w.line(map[string]any{"source": "export", "since": since.Unix(), "until": until.Unix(), "sources": sources})
	for _, src := range exportSources {
		if !slices.Contains(sources, src) || w.err != nil {
			continue
		}
		var err error
		switch src {
		case "events":
			err = s.exportEvents(ctx, w, since, until)
		case "audit":
			err = s.exportAudit(ctx, w, since, until)
		case "journal":
			err = s.exportJournal(ctx, w, since, until)
		case "logs":
			for _, e := range s.logRows(since, until) {
				w.line(map[string]any{"source": "logs", "seq": e.Seq, "ts_ms": e.TimeUnixMs, "level": e.Level, "component": e.Component,
					"message": e.Message, "code": e.Code, "request_id": e.RequestId, "attrs": e.Attrs})
			}
		}
		if err != nil && ctx.Err() == nil {
			return internalErr(s.cfg.Log, "export "+src, err)
		}
	}
	if w.err != nil {
		return w.err
	}
	if err := w.flush(); err != nil {
		return err
	}
	s.audit(ctx, "export_data", map[string]string{"since": since.Format(time.RFC3339), "until": until.Format(time.RFC3339), "rows": itoa(w.rows)})
	return nil
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// exportWriter turns rows into scrubbed NDJSON lines and sends them in chunks that end at a line boundary.
type exportWriter struct {
	ctx    context.Context
	stream *connect.ServerStream[opsv1.ExportDataResponse]
	scrub  *Scrubber
	buf    bytes.Buffer
	rows   int
	err    error
}

func (w *exportWriter) line(row map[string]any) {
	if w.err != nil {
		return
	}
	if err := w.ctx.Err(); err != nil {
		w.err = err
		return
	}
	b, err := json.Marshal(row)
	if err != nil {
		return
	}
	w.buf.WriteString(w.scrub.Line(string(b)))
	w.buf.WriteByte('\n')
	w.rows++
	if w.buf.Len() >= exportChunk {
		w.err = w.flush()
	}
}

func (w *exportWriter) flush() error {
	if w.buf.Len() == 0 {
		return nil
	}
	err := w.stream.Send(&opsv1.ExportDataResponse{Data: bytes.Clone(w.buf.Bytes())})
	w.buf.Reset()
	return err
}

func (s *Service) exportEvents(ctx context.Context, w *exportWriter, since, until time.Time) error {
	before, n := int64(0), 0
	for {
		rows, more, err := s.cfg.Store.Events(ctx, store.EventFilter{BeforeID: before, Limit: exportPage})
		if err != nil {
			return err
		}
		for _, e := range rows {
			before = e.ID
			if e.Time.Before(since) {
				return nil
			}
			if e.Time.After(until) {
				continue
			}
			if n++; n > exportMaxRows {
				w.line(map[string]any{"source": "events", "truncated": true})
				return nil
			}
			w.line(map[string]any{"source": "events", "id": e.ID, "ts": e.Time.Unix(), "severity": e.Severity, "code": e.Code, "origin": e.Source,
				"node": e.NodeName, "user": e.UserName, "inbound": e.InboundID, "profile": e.ProfileName, "protocol": e.Protocol, "params": e.Params})
		}
		if !more || len(rows) == 0 || w.err != nil {
			return nil
		}
	}
}

func (s *Service) exportAudit(ctx context.Context, w *exportWriter, since, until time.Time) error {
	before, n := int64(0), 0
	for {
		rows, err := s.cfg.Store.ListAuditQuery(ctx, store.AuditQuery{BeforeID: before, Limit: exportPage, Since: since})
		if err != nil {
			return err
		}
		for _, a := range rows {
			before = a.ID
			if a.Time.After(until) {
				continue
			}
			if n++; n > exportMaxRows {
				w.line(map[string]any{"source": "audit", "truncated": true})
				return nil
			}
			// the client address is left out, as in the MCP timeline
			w.line(map[string]any{"source": "audit", "id": a.ID, "ts": a.Time.Unix(), "actor": a.Actor, "actor_name": a.ActorName,
				"action": a.Action, "params": a.Params, "result": a.Result, "origin": a.Source})
		}
		if len(rows) < exportPage || w.err != nil {
			return nil
		}
	}
}

func (s *Service) exportJournal(ctx context.Context, w *exportWriter, since, until time.Time) error {
	rows, err := s.cfg.Store.NodeJournal(ctx, store.NodeJournalFilter{Since: since, Until: until, MinLevel: 2, Limit: exportMaxRows})
	if err != nil {
		return err
	}
	for _, j := range rows {
		if j.LastAt < since.Unix() || j.FirstAt > until.Unix() {
			continue
		}
		w.line(map[string]any{"source": "journal", "node": j.NodeName, "hour": j.HourStart, "level": j.Level, "origin": j.Source,
			"message": j.Message, "inbound": j.InboundID, "profile": j.ProfileName, "attrs": j.Attrs, "count": j.Count, "first": j.FirstAt, "last": j.LastAt})
	}
	return nil
}
