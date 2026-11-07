// Package logring keeps the panel's last log records in memory and lets the owner change the level while it runs. Handler wraps
// the process's slog handler: every record the current level lets through goes to the ring and on to the wrapped handler. The
// ring is for the panel's own diagnostics (the panel_logs RPC and MCP tool); it holds raw records, so a reader must scrub
// them (internal/panel/ops does) before they leave the process.
package logring

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/reqid"
)

// DefaultCapacity is how many records the ring keeps.
const DefaultCapacity = 5000

const (
	maxAttrs     = 16
	maxValueLen  = 400
	maxMessage   = 300
	maxComponent = 24
)

// Attr is one attribute of a record, its value rendered as text and cut.
type Attr struct{ Key, Value string }

// Record is one logged record as the ring keeps it.
type Record struct {
	Seq       int64 // 1, 2, 3 ...: grows with every record, never reused; the cursor of a read
	Time      time.Time
	Level     slog.Level
	Message   string
	Component string // attribute "component", else the word before the first ": " of the message ("health: prune" -> "health")
	Code      string // attribute "code"
	RequestID string // attribute "request_id", else the id of the request context
	Attrs     []Attr
}

// Ring is a fixed-size buffer of the latest records.
type Ring struct {
	mu   sync.Mutex
	buf  []Record
	next int64 // the Seq of the next record
	n    int   // records held
}

// NewRing returns a ring of capacity records (DefaultCapacity when capacity < 1).
func NewRing(capacity int) *Ring {
	if capacity < 1 {
		capacity = DefaultCapacity
	}
	return &Ring{buf: make([]Record, capacity), next: 1}
}

// Cap is the capacity.
func (r *Ring) Cap() int { return len(r.buf) }

// Len is the number of records held.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func (r *Ring) add(rec Record) {
	r.mu.Lock()
	rec.Seq = r.next
	r.buf[int((r.next-1)%int64(len(r.buf)))] = rec
	r.next++
	if r.n < len(r.buf) {
		r.n++
	}
	r.mu.Unlock()
}

// Snapshot returns the held records, newest first, whose Seq is below before (0 = from the newest) and for which keep (nil =
// all) is true, at most limit of them (limit < 1 = all). The records are copies.
func (r *Ring) Snapshot(before int64, limit int, keep func(*Record) bool) []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Record
	for seq := r.next - 1; seq >= r.next-int64(r.n) && seq >= 1; seq-- {
		if before > 0 && seq >= before {
			continue
		}
		rec := &r.buf[int((seq-1)%int64(len(r.buf)))]
		if keep != nil && !keep(rec) {
			continue
		}
		out = append(out, *rec)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Handler is a slog.Handler that records into a Ring and forwards to the handler it wraps.
type Handler struct {
	next   slog.Handler
	ring   *Ring
	level  *slog.LevelVar
	attrs  []slog.Attr
	groups []string
}

// NewHandler wraps next. level decides what is recorded and forwarded; next must not filter by a stricter level of its own
// (build it with the same LevelVar).
func NewHandler(next slog.Handler, ring *Ring, level *slog.LevelVar) *Handler {
	return &Handler{next: next, ring: ring, level: level}
}

// New builds the process logger: text or JSON ("json") records to w at a level the owner can change later, kept in a ring of
// capacity records. The level var starts at level.
func New(w io.Writer, format string, level slog.Level, capacity int) (*slog.Logger, *Ring, *slog.LevelVar) {
	lv := new(slog.LevelVar)
	lv.Set(level)
	opts := &slog.HandlerOptions{Level: lv}
	var inner slog.Handler = slog.NewTextHandler(w, opts)
	if strings.EqualFold(format, "json") {
		inner = slog.NewJSONHandler(w, opts)
	}
	ring := NewRing(capacity)
	return slog.New(NewHandler(inner, ring, lv)), ring, lv
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	rec := Record{Time: r.Time, Level: r.Level, Message: clip(r.Message, maxMessage)}
	for _, a := range h.attrs {
		rec.addAttr("", a)
	}
	prefix := strings.Join(h.groups, ".")
	r.Attrs(func(a slog.Attr) bool { rec.addAttr(prefix, a); return true })
	if rec.RequestID == "" {
		if id := reqid.From(ctx); id != "" {
			rec.RequestID = id
			r.AddAttrs(slog.String("request_id", id))
		}
	}
	if rec.Component == "" {
		rec.Component = componentOf(r.Message)
	}
	h.ring.add(rec)
	return h.next.Handle(ctx, r)
}

func (rec *Record) addAttr(prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		p := a.Key
		if prefix != "" {
			p = prefix + "." + a.Key
		}
		for _, g := range a.Value.Group() {
			rec.addAttr(p, g)
		}
		return
	}
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	val := clip(a.Value.String(), maxValueLen)
	switch key {
	case "component":
		rec.Component = clip(val, maxComponent)
		return
	case "code":
		rec.Code = clip(val, 64)
	case "request_id":
		rec.RequestID = clip(val, 32)
	}
	if len(rec.Attrs) < maxAttrs {
		rec.Attrs = append(rec.Attrs, Attr{Key: clip(key, 40), Value: val})
	}
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.next = h.next.WithAttrs(attrs)
	c.attrs = append(append([]slog.Attr(nil), h.attrs...), qualify(h.groups, attrs)...)
	return &c
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.next = h.next.WithGroup(name)
	c.groups = append(append([]string(nil), h.groups...), name)
	return &c
}

// qualify prefixes attribute keys with the open groups, so a logger with a group keeps them in the ring.
func qualify(groups []string, attrs []slog.Attr) []slog.Attr {
	if len(groups) == 0 {
		return attrs
	}
	p := strings.Join(groups, ".") + "."
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		a.Key = p + a.Key
		out[i] = a
	}
	return out
}

// componentOf reads "health: prune events" as "health": a short word before the first colon.
func componentOf(msg string) string {
	head, _, ok := strings.Cut(msg, ": ")
	if !ok || head == "" || len(head) > maxComponent || strings.ContainsAny(head, " \t") {
		return ""
	}
	return strings.ToLower(head)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 { // never cut inside a UTF-8 sequence
		n--
	}
	return s[:n] + "…"
}

// ParseLevel reads "debug", "info", "warn" (or "warning") and "error", any case.
func ParseLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return 0, false
}

// LevelName is the name ParseLevel reads back.
func LevelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	}
	return "debug"
}
