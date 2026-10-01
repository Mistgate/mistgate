package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

const (
	ringSize   = 5000 // agent.proto: "the last 5000 records"
	followBuf  = 512
	defaultSrc = "agent"
)

type logRecord struct {
	at     time.Time
	level  pb.Severity
	source string
	msg    string
	attrs  map[string]string
}

// logRing keeps the agent's own slog records in memory for LogRequest.
type logRing struct {
	mu   sync.Mutex
	buf  [ringSize]logRecord
	n    int // records stored, capped at ringSize
	head int // next write index
	subs map[*follower]struct{}
}

// follower receives new records for a `follow` LogRequest. The ring never blocks on it: a slow consumer
// loses records and is told how many.
type follower struct {
	ch      chan logRecord
	dropped uint32 // guarded by logRing.mu
}

func newLogRing() *logRing { return &logRing{subs: map[*follower]struct{}{}} }

func (r *logRing) add(rec logRecord) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = rec
	r.head = (r.head + 1) % ringSize
	if r.n < ringSize {
		r.n++
	}
	for f := range r.subs {
		select {
		case f.ch <- rec:
		default:
			f.dropped++
		}
	}
}

// tail returns up to max of the most recent records accepted by keep, oldest first. With follow it also
// subscribes to new records in the same critical section, so none is lost or repeated between the two.
func (r *logRing) tail(max int, keep func(logRecord) bool, follow bool) ([]logRecord, *follower) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []logRecord
	for i := 0; i < r.n && len(out) < max; i++ {
		rec := r.buf[(r.head-1-i+2*ringSize)%ringSize]
		if keep(rec) {
			out = append(out, rec)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if !follow {
		return out, nil
	}
	f := &follower{ch: make(chan logRecord, followBuf)}
	r.subs[f] = struct{}{}
	return out, f
}

func (r *logRing) unfollow(f *follower) {
	r.mu.Lock()
	delete(r.subs, f)
	r.mu.Unlock()
}

func (r *logRing) takeDropped(f *follower) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := f.dropped
	f.dropped = 0
	return d
}

// ringHandler tees every record into the ring and forwards it to the real handler. Records of Info and
// above always reach the ring, even when the real handler is quieter, so the panel can read them.
type ringHandler struct {
	ring  *logRing
	inner slog.Handler
	attrs []slog.Attr // with the group prefix already applied
	group string
}

func (h *ringHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || h.inner.Enabled(ctx, l)
}

func (h *ringHandler) Handle(ctx context.Context, r slog.Record) error {
	rec := logRecord{at: r.Time, level: severity(r.Level), source: defaultSrc, msg: r.Message, attrs: map[string]string{}}
	var put func(a slog.Attr, prefix string)
	put = func(a slog.Attr, prefix string) {
		v := a.Value.Resolve() // secrets are LogValuers that render [REDACTED]
		if v.Kind() == slog.KindGroup {
			for _, ga := range v.Group() {
				put(ga, prefix+a.Key+".")
			}
			return
		}
		if prefix == "" && a.Key == "source" {
			rec.source = v.String()
			return
		}
		rec.attrs[prefix+a.Key] = v.String()
	}
	for _, a := range h.attrs {
		put(a, "")
	}
	r.Attrs(func(a slog.Attr) bool {
		put(a, h.group)
		return true
	})
	h.ring.add(rec)
	if h.inner.Enabled(ctx, r.Level) {
		return h.inner.Handle(ctx, r)
	}
	return nil
}

func (h *ringHandler) WithAttrs(as []slog.Attr) slog.Handler {
	c := *h
	c.inner = h.inner.WithAttrs(as)
	c.attrs = append([]slog.Attr(nil), h.attrs...)
	for _, a := range as {
		if h.group != "" {
			a.Key = h.group + a.Key
		}
		c.attrs = append(c.attrs, a)
	}
	return &c
}

func (h *ringHandler) WithGroup(name string) slog.Handler {
	c := *h
	c.inner = h.inner.WithGroup(name)
	if name != "" {
		c.group = h.group + name + "."
	}
	return &c
}

func severity(l slog.Level) pb.Severity {
	switch {
	case l >= slog.LevelError:
		return pb.Severity_SEVERITY_ERROR
	case l >= slog.LevelWarn:
		return pb.Severity_SEVERITY_WARNING
	default:
		return pb.Severity_SEVERITY_INFO
	}
}

func (r logRecord) line() *pb.LogLine {
	var attrs map[string]string
	if len(r.attrs) > 0 {
		attrs = r.attrs
	}
	return &pb.LogLine{TimeUnixMs: r.at.UnixMilli(), Level: r.level, Source: r.source, Message: r.msg, Attrs: attrs}
}

// logFilter builds the keep-function of a LogRequest.
func logFilter(req *pb.LogRequest) func(logRecord) bool {
	var srcs map[string]bool
	if len(req.Sources) > 0 {
		srcs = map[string]bool{}
		for _, s := range req.Sources {
			srcs[strings.TrimSpace(s)] = true
		}
	}
	return func(r logRecord) bool {
		return (srcs == nil || srcs[r.source]) && r.level >= req.MinLevel
	}
}
