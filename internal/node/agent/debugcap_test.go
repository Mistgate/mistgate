package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The agent side of "DEBUG CAPTURE" (agent.proto): the capability, the level that goes up and comes back, the bounded buffer, the
// scrub on the node, and what a restart does.

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// debugHarness is an agent whose process logger is tied to a LevelVar at warn, like `mistgate-node run --log-level warn`.
func debugHarness(t *testing.T) (*harness, *slog.LevelVar, *lockedBuf) {
	lv := new(slog.LevelVar)
	lv.Set(slog.LevelWarn)
	out := &lockedBuf{}
	h := newHarness(t, harnessOpts{cfg: func(c *Config) {
		c.Level = lv
		c.Log = slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: lv}))
	}})
	return h, lv, out
}

func startCapture(h *harness, id, level string, sources []string, secs uint32) *pb.CommandResult {
	h.t.Helper()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_DebugCapture{DebugCapture: &pb.DebugCapture{
		RequestId: id, Level: level, Sources: sources, DurationS: secs}}})
	r := h.panel.nextCmd()
	if r.RequestId != id {
		h.t.Fatalf("answer to %q, asked %q", r.RequestId, id)
	}
	return r
}

func nextReport(t *testing.T, h *harness) *pb.DebugCaptureReport {
	t.Helper()
	select {
	case r := <-h.panel.debugs:
		return r
	case <-time.After(6 * time.Second):
		t.Fatal("no DebugCaptureReport")
		return nil
	}
}

// drainReport reads chunks up to the eof one.
func drainReport(t *testing.T, h *harness) (lines []*pb.LogLine, last *pb.DebugCaptureReport) {
	t.Helper()
	for {
		r := nextReport(t, h)
		lines = append(lines, r.Lines...)
		if len(r.Lines) > debugChunkLines {
			t.Fatalf("a chunk of %d lines", len(r.Lines))
		}
		if r.Eof {
			return lines, r
		}
	}
}

func TestDebugIsListedAsACapability(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	if !contains((<-h.panel.hellos).Capabilities, "debug/1") {
		t.Error("the agent does not list debug/1")
	}
	// an agent that does not list it is never asked: the panel's gate is tested in internal/panel/fleet
	if capDebug != "debug/1" {
		t.Errorf("capability string %q", capDebug)
	}
}

func TestDebugCaptureRaisesTheLevelAndPutsItBackOnExpiry(t *testing.T) {
	h, lv, out := debugHarness(t)
	h.waitConnected()
	h.a.log.With("source", "hysteria2").Debug("before the capture")
	if r := startCapture(h, "req_d1", "debug", []string{"hysteria2"}, 1); !r.Ok {
		t.Fatalf("start = %+v", r)
	}
	if lv.Level() != slog.LevelDebug {
		t.Fatalf("level during the capture = %v", lv.Level())
	}
	h.a.log.With("source", "hysteria2").Debug("handshake from 203.0.113.9:4433")
	h.a.log.With("source", "agent").Debug("other source, not asked")
	h.a.log.With("source", "hysteria2").Info("info of the asked source")
	h.a.log.With("source", "agent").Warn("a warning elsewhere stays out of it")

	lines, last := drainReport(t, h)
	if last.EndReason != "expired" || last.RequestId != "req_d1" {
		t.Fatalf("last chunk = %+v", last)
	}
	var texts []string
	for _, l := range lines {
		texts = append(texts, l.Message)
	}
	got := strings.Join(texts, "|")
	if len(lines) != 2 || !strings.Contains(got, "handshake from <ip>:port") || !strings.Contains(got, "info of the asked source") {
		t.Fatalf("captured %q", got)
	}
	if lines[0].Level != pb.Severity_SEVERITY_DEBUG || lines[1].Level != pb.Severity_SEVERITY_INFO {
		t.Errorf("levels %v %v", lines[0].Level, lines[1].Level)
	}
	if lv.Level() != slog.LevelWarn {
		t.Errorf("level after the capture = %v, want warn", lv.Level())
	}
	// the captured records never reach the process log (only the configured level does), and never the ring
	if strings.Contains(out.String(), "handshake from") || strings.Contains(out.String(), "other source") || strings.Contains(out.String(), "info of the asked") {
		t.Errorf("debug record reached the process log:\n%s", out.String())
	}
	if recs, _ := h.a.ring.tail(ringSize, func(r logRecord) bool { return strings.Contains(r.msg, "handshake") }, false); len(recs) != 0 {
		t.Error("a debug record is in the Info ring")
	}
	if h.a.debug.active.Load() != nil {
		t.Error("capture still active")
	}
}

func TestDebugCaptureStopShipsAtOnceAndAnswersAfterTheLastChunk(t *testing.T) {
	h, lv, _ := debugHarness(t)
	h.waitConnected()
	if r := startCapture(h, "req_d2", "info", nil, 600); !r.Ok {
		t.Fatalf("start = %+v", r)
	}
	if lv.Level() != slog.LevelInfo {
		t.Fatalf("level = %v", lv.Level())
	}
	h.a.log.Info("one")
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_DebugCapture{DebugCapture: &pb.DebugCapture{RequestId: "req_stop", CaptureId: "req_d2", Stop: true}}})
	r := h.panel.nextCmd()
	if !r.Ok || r.Detail != "stopped" {
		t.Fatalf("stop answer = %+v", r)
	}
	select {
	case rep := <-h.panel.debugs:
		if !rep.Eof || rep.EndReason != "stopped" {
			t.Errorf("report = %+v", rep)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the answer came before the report")
	}
	if lv.Level() != slog.LevelWarn {
		t.Errorf("level after stop = %v", lv.Level())
	}
	// a stop for a capture nobody knows is a plain ok
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_DebugCapture{DebugCapture: &pb.DebugCapture{RequestId: "req_stop2", CaptureId: "req_none", Stop: true}}})
	if r := h.panel.nextCmd(); !r.Ok || r.Detail != "none" {
		t.Errorf("unknown stop = %+v", r)
	}
}

func TestDebugCaptureRefusals(t *testing.T) {
	h, lv, _ := debugHarness(t)
	h.waitConnected()
	for name, tc := range map[string]struct {
		level   string
		sources []string
		secs    uint32
	}{
		"level":    {"trace", nil, 10},
		"zero":     {"debug", nil, 0},
		"source":   {"debug", []string{"bad source!"}, 10},
		"toomany":  {"debug", []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}, 10},
		"longname": {"debug", []string{strings.Repeat("x", 33)}, 10},
	} {
		if r := startCapture(h, "req_"+name, tc.level, tc.sources, tc.secs); r.Ok || r.Error != "invalid" {
			t.Errorf("%s: %+v", name, r)
		}
	}
	if lv.Level() != slog.LevelWarn {
		t.Errorf("a refused request moved the level to %v", lv.Level())
	}
	if r := startCapture(h, "req_first", "debug", nil, 600); !r.Ok {
		t.Fatal(r)
	}
	if r := startCapture(h, "req_second", "debug", nil, 600); r.Ok || r.Error != "busy" {
		t.Errorf("second capture = %+v", r)
	}
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_DebugCapture{DebugCapture: &pb.DebugCapture{RequestId: "req_stop3", CaptureId: "req_first", Stop: true}}})
	h.panel.nextCmd()
	// more than 900 s is clipped, not refused
	if r := startCapture(h, "req_long", "debug", nil, 99999); !r.Ok {
		t.Errorf("long duration = %+v", r)
	}
	h.a.debug.mu.Lock()
	left := time.Until(h.a.debug.cur.end)
	h.a.debug.mu.Unlock()
	if left > 901*time.Second {
		t.Errorf("capture runs for %v", left)
	}
}

func TestDebugCaptureEndsWithTheProcess(t *testing.T) {
	h, lv, _ := debugHarness(t)
	h.waitConnected()
	if r := startCapture(h, "req_d3", "debug", nil, 600); !r.Ok {
		t.Fatal(r)
	}
	if lv.Level() != slog.LevelDebug {
		t.Fatalf("level = %v", lv.Level())
	}
	_ = h.stop()
	if lv.Level() != slog.LevelWarn {
		t.Errorf("level after the agent stopped = %v, want warn", lv.Level())
	}
	// a restart builds a new agent: nothing of the capture is on disk, so it starts with none, at the configured level
	lv2 := new(slog.LevelVar)
	lv2.Set(slog.LevelWarn)
	st := newDebugState(lv2, time.Now)
	if st.active.Load() != nil || st.cur != nil || st.done != nil || lv2.Level() != slog.LevelWarn {
		t.Error("a new process starts with a capture")
	}
}

func TestDebugCaptureEndedWhileOfflineIsShippedOnTheNextSession(t *testing.T) {
	d := newDebugState(nil, time.Now)
	expired := make(chan string, 1)
	if _, ok := d.start("req_off", slog.LevelDebug, nil, 20*time.Millisecond, func(id string) { expired <- id }); !ok {
		t.Fatal("not started")
	}
	d.add(slog.LevelDebug, logRecord{at: time.Now(), source: "agent", msg: "kept"})
	d.finish(<-expired, "expired")
	if c, _ := d.take("other"); c != nil {
		t.Fatal("took another capture")
	}
	c, reason := d.take("")
	if c == nil || reason != "expired" || len(c.recs) != 1 {
		t.Fatalf("take = %+v %q", c, reason)
	}
	if c, _ := d.take(""); c != nil {
		t.Error("shipped twice")
	}
	// past the grace it is dropped
	now := time.Now()
	d.now = func() time.Time { return now }
	d.start("req_old", slog.LevelDebug, nil, time.Hour, func(string) {})
	d.finish("req_old", "expired")
	d.now = func() time.Time { return now.Add(debugShipGrace + time.Second) }
	if c, _ := d.take(""); c != nil {
		t.Error("a capture older than the grace was shipped")
	}
}

func TestDebugBufferIsBoundedByRecordsAndBytes(t *testing.T) {
	d := newDebugState(nil, time.Now)
	d.start("req_cap", slog.LevelDebug, nil, time.Hour, func(string) {})
	for i := 0; i < debugMaxRecords+50; i++ {
		d.add(slog.LevelDebug, logRecord{at: time.Now(), source: "agent", msg: "x"})
	}
	if len(d.cur.recs) != debugMaxRecords || d.cur.dropped != 50 {
		t.Fatalf("records %d dropped %d", len(d.cur.recs), d.cur.dropped)
	}
	d.shutdown()
	d.start("req_cap2", slog.LevelDebug, nil, time.Hour, func(string) {})
	defer d.shutdown()
	big := strings.Repeat("mm ", debugMsgBytes/3)
	for i := 0; i < 5000; i++ {
		d.add(slog.LevelDebug, logRecord{at: time.Now(), source: "agent", msg: big + fmt.Sprint(i)})
	}
	if d.cur.bytes > debugMaxBytes || len(d.cur.recs) >= 5000 || d.cur.dropped == 0 {
		t.Fatalf("bytes %d records %d dropped %d", d.cur.bytes, len(d.cur.recs), d.cur.dropped)
	}
	if uint32(len(d.cur.recs))+d.cur.dropped != 5000 {
		t.Errorf("records %d + dropped %d != 5000", len(d.cur.recs), d.cur.dropped)
	}
}

func TestDebugRecordsAreScrubbedOnTheNode(t *testing.T) {
	d := newDebugState(nil, time.Now)
	d.start("req_s", slog.LevelDebug, nil, time.Hour, func(string) {})
	defer d.shutdown()
	const key = "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY0123456789"
	const hexKey = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	add := func(src, msg string, attrs map[string]string) {
		d.add(slog.LevelDebug, logRecord{at: time.Now(), source: src, msg: msg, attrs: attrs})
	}
	add("xray", "accepted tcp:canary-site.example.com:443 from 198.51.100.7:51000 sni canary-sni.example.org", map[string]string{
		"err": "dial tcp 203.0.113.5:443: i/o timeout to host.example.net", "user": "alice@example.com", "path": "/etc/secret",
	})
	add("agent", "client [2001:db8::7]:5555 key "+key+" hex "+hexKey, map[string]string{"state": "ok 192.0.2.10", "token": key, "code": "x"})
	var all []string
	for _, r := range d.cur.recs {
		all = append(all, r.msg)
		for k, v := range r.attrs {
			all = append(all, k+"="+v)
			if k == "user" || k == "path" || k == "token" {
				t.Errorf("attribute %q is not on the allow-list", k)
			}
		}
	}
	text := strings.Join(all, "\n")
	for _, canary := range []string{"canary-site.example.com", "canary-sni.example.org", "host.example.net", "198.51.100.7", "203.0.113.5",
		"2001:db8::7", "192.0.2.10", key, hexKey, "K7MDENG", "alice@example.com"} {
		if strings.Contains(text, canary) {
			t.Errorf("%q survived the scrub:\n%s", canary, text)
		}
	}
	for _, want := range []string{"<host>", "<ip>:port", "<key>", "state=ok <ip>", "code=x"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	if d.cur.recs[0].level != pb.Severity_SEVERITY_DEBUG {
		t.Error("debug level lost")
	}
}

func TestDebugCaptureCostsNothingWhenOff(t *testing.T) {
	var d *debugState
	if d.wants("agent", slog.LevelDebug) {
		t.Fatal("nil state wants records")
	}
	d.add(slog.LevelDebug, logRecord{}) // must not panic
	d.shutdown()
	h := &ringHandler{ring: newLogRing(), inner: slog.NewTextHandler(io.Discard, nil)}
	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug enabled with no capture")
	}
}
