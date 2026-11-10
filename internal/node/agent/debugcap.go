package agent

import (
	"log/slog"
	"regexp"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/logscrub"
)

// On-demand debug capture (agent.proto "DEBUG CAPTURE", capability "debug/1"): the panel asks for a level and some sources for up
// to 15 minutes. The agent raises its log level, keeps the matching records already scrubbed in a bounded buffer, ships them when
// the time is up or on a stop, and puts the level back. Nothing is written to disk: a restart ends the capture.

// capDebug is listed in Hello.capabilities: the panel sends DebugCapture only to agents that have it.
const capDebug = "debug/1"
const capDebug2 = "debug/2"

const (
	debugMaxRecords  = 20000
	debugMaxBytes    = 4 << 20
	debugMaxSeconds  = 900
	debugMaxSources  = 8
	debugChunkLines  = 64
	debugShipGrace   = 15 * time.Minute // a capture that ended while the link was down is shipped within this
	debugMsgBytes    = 1024
	debugAttrRunes   = 160
	debugRecordExtra = 64 // what a record costs besides its text
)

// keyRun is a run that looks like a key or a token: long base64, base64url or hex text. Removed from every captured line.
var keyRun = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)

// debugCapture is one running or finished capture. recs is guarded by debugState.mu.
type debugCapture struct {
	id              string
	level           slog.Level
	sources         map[string]bool // nil = every source
	started         time.Time
	end             time.Time
	prev            slog.Level // the level to put back
	timer           *time.Timer
	onEnd           func(*debugCapture, string)
	finishing       bool
	reservedRecords int
	reservedBytes   int

	recs    []logRecord
	bytes   int
	dropped uint32
}

type debugFinished struct {
	c      *debugCapture
	reason string
	at     time.Time
}

// debugState is the agent's capture machinery. wants is lock free (an atomic pointer to the running capture), so a log call that
// no capture wants costs one load.
type debugState struct {
	lv   *slog.LevelVar
	base slog.Level // the level the process runs at; what a capture puts back
	now  func() time.Time

	active atomic.Pointer[debugCapture]

	mu   sync.Mutex
	cur  *debugCapture
	done *debugFinished // finished, not shipped yet
}

func newDebugState(lv *slog.LevelVar, now func() time.Time) *debugState {
	if lv == nil {
		lv = new(slog.LevelVar)
		lv.Set(slog.LevelInfo)
	}
	return &debugState{lv: lv, base: lv.Level(), now: now}
}

// wants reports whether a running capture keeps a record of this source and level. Safe on a nil receiver.
func (d *debugState) wants(source string, l slog.Level) bool {
	if d == nil {
		return false
	}
	c := d.active.Load()
	return c != nil && l >= c.level && (c.sources == nil || c.sources[source])
}

// add keeps a record in the running capture, scrubbed. A record below Info that no capture wants never gets here.
type debugAddResult uint8

const (
	debugAddSkipped debugAddResult = iota
	debugAddStored
	debugAddCapped
)

func (d *debugState) add(l slog.Level, rec logRecord) debugAddResult {
	if d == nil || !d.wants(rec.source, l) {
		return debugAddSkipped
	}
	c := d.active.Load()
	rec = scrubCaptured(rec, l)
	d.mu.Lock()
	defer d.mu.Unlock()
	if c == nil || d.cur != c { // The capture can finish after wants returns and before this load.
		return debugAddSkipped
	}
	if !appendDebugRecord(c, rec, true) {
		c.dropped = incrementJournalCount(c.dropped)
		return debugAddCapped
	}
	return debugAddStored
}

func appendDebugRecord(c *debugCapture, rec logRecord, reserve bool) bool {
	maxRecords, maxBytes := debugMaxRecords, debugMaxBytes
	if reserve {
		maxRecords -= c.reservedRecords
		maxBytes -= c.reservedBytes
	}
	if maxRecords < 0 {
		maxRecords = 0
	}
	if maxBytes < 0 {
		maxBytes = 0
	}
	size := debugRecordSize(rec)
	if len(c.recs) >= maxRecords || c.bytes+size > maxBytes {
		return false
	}
	c.recs = append(c.recs, rec)
	c.bytes += size
	return true
}

func debugRecordSize(rec logRecord) int {
	size := len(rec.msg) + len(rec.source) + debugRecordExtra
	for k, v := range rec.attrs {
		size += len(k) + len(v)
	}
	return size
}

func (d *debugState) addFinal(c *debugCapture, recs ...logRecord) {
	if d == nil || c == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, rec := range recs {
		level := slog.LevelInfo
		if rec.level == pb.Severity_SEVERITY_WARNING {
			level = slog.LevelWarn
		} else if rec.level == pb.Severity_SEVERITY_ERROR {
			level = slog.LevelError
		} else if rec.level == pb.Severity_SEVERITY_DEBUG {
			level = slog.LevelDebug
		}
		clean := scrubCaptured(rec, level)
		if !appendDebugRecord(c, clean, false) {
			c.dropped = incrementJournalCount(c.dropped)
		}
	}
}

// scrubCaptured is the journal's pass over a record, plus key-shaped runs: addresses become <ip>, xray's names are cut, only the
// allow-listed attributes stay, and every text is cut to a bound. The debug level is kept for the line.
func scrubCaptured(rec logRecord, l slog.Level) logRecord {
	clean := func(s string) string { return keyRun.ReplaceAllString(scrub(rec.source, s), "<key>") }
	out := logRecord{at: rec.at, level: rec.level, source: logscrub.Clip(rec.source, 32)}
	if l < slog.LevelInfo {
		out.level = pb.Severity_SEVERITY_DEBUG
	}
	out.msg = logscrub.Clip(clean(logscrub.Clip(rec.msg, debugMsgBytes)), debugMsgBytes)
	for _, key := range logscrub.JournalAttrs {
		value, ok := rec.attrs[key]
		if !ok {
			continue
		}
		if out.attrs == nil {
			out.attrs = make(map[string]string, 8)
		}
		out.attrs[key] = logscrub.ClipRunes(clean(logscrub.ClipRunes(value, 640)), debugAttrRunes)
	}
	return out
}

// debugRequest checks a start request. The error is the word of the CommandResult.
func debugRequest(r *pb.DebugCapture) (level slog.Level, sources map[string]bool, d time.Duration, bad string) {
	switch r.Level {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	default:
		return 0, nil, 0, "invalid"
	}
	if len(r.Sources) > debugMaxSources || r.DurationS == 0 {
		return 0, nil, 0, "invalid"
	}
	for _, s := range r.Sources {
		if !validSource(s) {
			return 0, nil, 0, "invalid"
		}
		if sources == nil {
			sources = map[string]bool{}
		}
		sources[s] = true
	}
	return level, sources, time.Duration(min(r.DurationS, debugMaxSeconds)) * time.Second, ""
}

func validSource(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// start begins a capture; busy while another runs. expire is called when the time is up.
func (d *debugState) start(id string, level slog.Level, sources map[string]bool, dur time.Duration, expire func(id string), prepare ...func(*debugCapture)) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur != nil {
		return time.Time{}, false
	}
	if d.done != nil {
		if d.now().Sub(d.done.at) <= debugShipGrace {
			return time.Time{}, false
		}
		d.done = nil
	}
	started := d.now()
	c := &debugCapture{id: id, level: level, sources: sources, started: started, end: started.Add(dur), prev: d.lv.Level()}
	d.cur = c
	if level < c.prev {
		d.lv.Set(level) // the raise: slog skips the work of a record nobody wants
	}
	d.active.Store(c)
	for _, fn := range prepare {
		fn(c)
	}
	c.timer = time.AfterFunc(dur, func() { expire(id) })
	return c.end, true
}

// finish ends the running capture with this id, puts the level back and keeps the records as finished. nil when there is none.
func (d *debugState) finish(id, reason string) *debugCapture {
	d.mu.Lock()
	c := d.cur
	if c == nil || c.id != id || c.finishing {
		d.mu.Unlock()
		return nil
	}
	c.finishing = true
	c.timer.Stop()
	d.active.Store(nil)
	d.lv.Set(c.prev)
	onEnd := c.onEnd
	d.mu.Unlock()
	if onEnd != nil {
		onEnd(c, reason)
	}
	d.mu.Lock()
	if d.cur == c {
		d.cur = nil
		d.done = &debugFinished{c: c, reason: reason, at: d.now()}
	}
	d.mu.Unlock()
	return c
}

// take returns the finished capture for this shipment attempt. With a nonempty id it must be that capture. Within the grace only.
func (d *debugState) take(id string) (*debugCapture, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.done
	if f == nil || (id != "" && f.c.id != id) {
		return nil, ""
	}
	if d.now().Sub(f.at) > debugShipGrace {
		d.done = nil
		return nil, ""
	}
	return f.c, f.reason
}

// acknowledge drops the finished capture after the panel confirms its final chunk.
func (d *debugState) acknowledge(id string, chunkSeq uint32) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.done
	if f == nil || f.c.id != id {
		return false
	}
	last := uint32(0)
	if len(f.c.recs) > 0 {
		last = uint32((len(f.c.recs) - 1) / debugChunkLines)
	}
	if chunkSeq != last {
		return false
	}
	d.done = nil
	return true
}

// shutdown drops a running capture without shipping it and puts the level back (Run ends: the process is going away).
func (d *debugState) shutdown() {
	if d == nil {
		return
	}
	d.mu.Lock()
	var c *debugCapture
	if running := d.cur; running != nil {
		if !running.finishing {
			running.timer.Stop()
			d.active.Store(nil)
			d.lv.Set(running.prev)
			d.cur = nil
			c = running
		}
	}
	d.mu.Unlock()
	if c != nil && c.onEnd != nil {
		c.onEnd(c, "shutdown")
	}
}

// snapshot copies a finished capture's records under the lock (nothing appends to a finished one, but the reader should not care).
func (d *debugState) records(c *debugCapture) ([]logRecord, uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return c.recs, c.dropped
}

// debugCapture answers a DebugCapture: a start or a stop.
func (a *Agent) debugCapture(s *session, r *pb.DebugCapture) {
	if r.Stop {
		a.debug.finish(r.CaptureId, "stopped") // moves a running capture to the finished slot
		c, reason := a.debug.take(r.CaptureId) // or it ended earlier and waits for a link
		if c == nil {
			s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true, Detail: "none"}))
			return
		}
		a.shipDebug(s, c, reason)
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true, Detail: "stopped"}))
		return
	}
	level, sources, dur, bad := debugRequest(r)
	if bad != "" {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: bad}))
		return
	}
	end, ok := a.debug.start(r.RequestId, level, sources, dur, a.debugExpired, a.prepareKernelCapture)
	if !ok {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}
	a.log.Warn("debug capture started", "code", "debug_capture_started", "mode", r.Level)
	s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true, Detail: "started", Params: map[string]string{
		"ends_unix": strconv.FormatInt(end.Unix(), 10)}}))
}

// debugExpired runs when a capture's time is up: ship it on the current stream, or keep it for the next one.
func (a *Agent) debugExpired(id string) {
	if a.debug.finish(id, "expired") == nil {
		return
	}
	a.log.Info("debug capture finished", "reason", "expired")
	if s := a.cur.Load(); s != nil {
		a.goBusy(func() { a.debugResume(s) })
	}
}

// debugResume retries a finished capture that has not been acknowledged.
func (a *Agent) debugResume(s *session) {
	if c, reason := a.debug.take(""); c != nil {
		a.shipDebug(s, c, reason)
	}
}

// shipDebug sends a capture as sequenced DebugCaptureReport chunks, the last with eof.
func (a *Agent) shipDebug(s *session, c *debugCapture, reason string) {
	recs, dropped := a.debug.records(c)
	var chunkSeq uint32
	for first := true; first || len(recs) > 0; first = false {
		n := min(len(recs), debugChunkLines)
		seq := chunkSeq
		rep := &pb.DebugCaptureReport{RequestId: c.id, Lines: make([]*pb.LogLine, n), ChunkSeq: &seq}
		for i, r := range recs[:n] {
			rep.Lines[i] = r.line()
		}
		recs = recs[n:]
		if len(recs) == 0 {
			rep.Eof, rep.Dropped, rep.EndReason = true, dropped, reason
		}
		s.send(&pb.ConnectRequest{Message: &pb.ConnectRequest_DebugReport{DebugReport: rep}})
		chunkSeq++
	}
}
