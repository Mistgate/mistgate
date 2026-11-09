//go:build linux

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg"
	"github.com/mistgate/mistgate/internal/node/engine"
)

func TestParseKernelJournalLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want parsedKernelLine
		bad  bool
	}{
		{name: "text and string priority", line: `{"MESSAGE":"driver ready","PRIORITY":"6","__REALTIME_TIMESTAMP":"1700000000123456"}`,
			want: parsedKernelLine{at: time.UnixMicro(1700000000123456), message: "driver ready", priority: 6}},
		{name: "byte array and numeric priority", line: `{"MESSAGE":[65,66],"PRIORITY":4,"__REALTIME_TIMESTAMP":1700000000000000}`,
			want: parsedKernelLine{at: time.Unix(1700000000, 0), message: "AB", priority: 4}},
		{name: "non UTF-8 bytes remain visible", line: `{"MESSAGE":[255,65],"PRIORITY":"7","__REALTIME_TIMESTAMP":"1700000000000001"}`,
			want: parsedKernelLine{at: time.UnixMicro(1700000000000001), message: `"\xffA"`, priority: 7}},
		{name: "bad JSON", line: `{`, bad: true},
		{name: "missing message", line: `{"PRIORITY":"6","__REALTIME_TIMESTAMP":"1700000000000000"}`, bad: true},
		{name: "message object", line: `{"MESSAGE":{"x":1},"PRIORITY":"6","__REALTIME_TIMESTAMP":"1700000000000000"}`, bad: true},
		{name: "invalid byte", line: `{"MESSAGE":[256],"PRIORITY":"6","__REALTIME_TIMESTAMP":"1700000000000000"}`, bad: true},
		{name: "missing timestamp", line: `{"MESSAGE":"x","PRIORITY":"6"}`, bad: true},
		{name: "invalid priority", line: `{"MESSAGE":"x","PRIORITY":"8","__REALTIME_TIMESTAMP":"1700000000000000"}`, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseKernelJournalLine([]byte(tc.line))
			if tc.bad {
				if err == nil {
					t.Fatalf("parseKernelJournalLine(%s) succeeded: %+v", tc.line, got)
				}
				return
			}
			if err != nil || !got.at.Equal(tc.want.at) || got.message != tc.want.message || got.priority != tc.want.priority {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestKernelPriorityAndFirewallFilter(t *testing.T) {
	for _, tc := range []struct {
		priority int
		level    slog.Level
	}{
		{0, slog.LevelError}, {3, slog.LevelError}, {4, slog.LevelWarn}, {5, slog.LevelInfo}, {6, slog.LevelInfo}, {7, slog.LevelDebug},
	} {
		if got := kernelLevel(tc.priority); got != tc.level {
			t.Errorf("priority %d = %v, want %v", tc.priority, got, tc.level)
		}
	}
	for _, tc := range []struct {
		message string
		want    bool
	}{
		{"[UFW BLOCK] IN=eth0 SRC=203.0.113.7 DST=198.51.100.2 PROTO=UDP", true},
		{" SRC=203.0.113.7 DST=198.51.100.2 PROTO=TCP trailing", true},
		{"SRC=203.0.113.7 DST=198.51.100.2 PROTO=UDP", false},
		{" SRC=203.0.113.7 DST=198.51.100.2", false},
		{"ordinary kernel line", false},
	} {
		if got := firewallPacketLog(tc.message); got != tc.want {
			t.Errorf("firewallPacketLog(%q) = %t, want %t", tc.message, got, tc.want)
		}
	}
}

func TestClassifyEveryAWGKernelPhrase(t *testing.T) {
	for _, tc := range []struct {
		message string
		event   string
		peer    string
	}{
		{"Receiving handshake initiation from peer 17 (203.0.113.7:2408)", "initiation_in", "17"},
		{"Sending handshake response to peer 17 (203.0.113.7:2408)", "response_out", "17"},
		{"Sending handshake initiation to peer 17", "initiation_out", "17"},
		{"Receiving handshake response from peer 17", "response_in", "17"},
		{"Invalid handshake initiation from 203.0.113.7:2408", "invalid_initiation", ""},
		{"Invalid handshake response from 203.0.113.7:2408", "invalid_response", ""},
		{"Receiving cookie response from 203.0.113.7:2408", "cookie_in", ""},
		{"Sending cookie response", "cookie_out", ""},
		{"Receiving keepalive packet from peer 17", "keepalive_in", "17"},
		{"Sending keepalive packet to peer 17", "keepalive_out", "17"},
		{"Handshake for peer 17 (203.0.113.7:2408) did not complete after 5 seconds, retrying", "handshake_retry", "17"},
		{"Packet has unallowed src IP", "unallowed_src", ""},
		{"Keypair 3 created for peer 17", "keypair_created", "17"},
		{"Keypair 3 destroyed for peer 17", "keypair_destroyed", "17"},
		{"future module diagnostic", "other", ""},
	} {
		event, peer := classifyAWGKernelLine(tc.message)
		if event != tc.event || peer != tc.peer {
			t.Errorf("classifyAWGKernelLine(%q) = %s, %s; want %s, %s", tc.message, event, peer, tc.event, tc.peer)
		}
	}
	for _, event := range awgKernelEvents {
		if event == "unknown_peer" || event == "summary" {
			continue
		}
		found := false
		for _, tc := range []string{"initiation_in", "response_out", "initiation_out", "response_in", "invalid_initiation", "invalid_response", "cookie_in", "cookie_out", "keepalive_in", "keepalive_out", "handshake_retry", "unallowed_src", "keypair_created", "keypair_destroyed", "other"} {
			if event == tc {
				found = true
			}
		}
		if !found {
			t.Errorf("uncovered AWG event %q", event)
		}
	}
}

func TestKernelInterfaceAttributesAndUnansweredInitiations(t *testing.T) {
	ifname, inbound, body, ok := kernelInterfaceMessage("mgawg123: Receiving handshake initiation from peer 17", map[string]string{"mgawg123": "inb_awg"})
	if !ok || ifname != "mgawg123" || inbound != "inb_awg" || body != "Receiving handshake initiation from peer 17" {
		t.Fatalf("interface match = %q %q %q %t", ifname, inbound, body, ok)
	}
	base := time.Unix(1_700_000_000, 0)
	for _, tc := range []struct {
		name   string
		events []kernelHandshakeEvent
		want   uint64
	}{
		{"answered", []kernelHandshakeEvent{{base, "17", "initiation_in"}, {base.Add(5 * time.Second), "17", "response_out"}}, 0},
		{"unanswered", []kernelHandshakeEvent{{base, "17", "initiation_in"}}, 1},
		{"answered late", []kernelHandshakeEvent{{base, "17", "initiation_in"}, {base.Add(5*time.Second + time.Nanosecond), "17", "response_out"}}, 1},
		{"separate peers", []kernelHandshakeEvent{{base, "17", "initiation_in"}, {base.Add(time.Second), "18", "response_out"}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unansweredKernelInitiations(tc.events); got != tc.want {
				t.Errorf("unanswered = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestKernelDebugResetAtAgentStart(t *testing.T) {
	control := writeKernelTestFile(t, "debug", "module amneziawg +p\n")
	cost := writeKernelTestFile(t, "cost", "0\n")
	env := newKernelDebugEnvironment(kernelDebugOptions{dynamicDebugPaths: []string{control}, messageCostPath: cost})
	if errs := env.resetAtStart(); len(errs) != 0 {
		t.Fatalf("resetAtStart errors: %v", errs)
	}
	if got := strings.TrimSpace(readKernelTestFile(t, control)); got != "module amneziawg -p" {
		t.Errorf("dynamic debug at start = %q", got)
	}
	if got := strings.TrimSpace(readKernelTestFile(t, cost)); got != "5" {
		t.Errorf("message_cost at start = %q, want 5", got)
	}
}

func TestKernelDebugRestoresOnEveryCaptureEndPath(t *testing.T) {
	for _, path := range []string{"finish", "expiry", "stop", "shutdown"} {
		t.Run(path, func(t *testing.T) {
			control := writeKernelTestFile(t, "debug", "module amneziawg -p\n")
			cost := writeKernelTestFile(t, "cost", "9\n")
			processes := make(chan *testKernelJournalProcess, 1)
			runner := func(ctx context.Context, _ string, _ ...string) (kernelJournalProcess, error) {
				p := newTestKernelJournalProcess(ctx, nil)
				processes <- p
				return p, nil
			}
			env := newKernelDebugEnvironment(kernelDebugOptions{dynamicDebugPaths: []string{control}, messageCostPath: cost, startJournal: runner})
			a, d, _ := kernelDebugHarness(env, []awg.KernelDebugInterface{{Name: "mgawg123", InboundID: "inb_awg"}})
			duration := time.Minute
			if path == "expiry" {
				duration = 500 * time.Millisecond
			}
			id := "req_" + path
			if _, ok := d.start(id, slog.LevelDebug, map[string]bool{"awg": true}, duration, a.debugExpired, a.prepareKernelCapture); !ok {
				t.Fatal("capture did not start")
			}
			process := <-processes
			if got := strings.TrimSpace(readKernelTestFile(t, control)); got != "module amneziawg +p" {
				t.Fatalf("dynamic debug during capture = %q", got)
			}
			if got := strings.TrimSpace(readKernelTestFile(t, cost)); got != "0" {
				t.Fatalf("message_cost during capture = %q", got)
			}
			switch path {
			case "finish":
				d.finish(id, "finished")
			case "stop":
				d.finish(id, "stopped")
			case "shutdown":
				d.shutdown()
			case "expiry":
				waitKernelTest(t, func() bool { return d.active.Load() == nil })
			}
			select {
			case <-process.waited:
			case <-time.After(time.Second):
				t.Fatal("journalctl process did not stop with the capture")
			}
			waitKernelTest(t, func() bool {
				return strings.TrimSpace(readKernelTestFile(t, control)) == "module amneziawg -p" && strings.TrimSpace(readKernelTestFile(t, cost)) == "9"
			})
			if got := strings.TrimSpace(readKernelTestFile(t, control)); got != "module amneziawg -p" {
				t.Errorf("dynamic debug after %s = %q", path, got)
			}
			if got := strings.TrimSpace(readKernelTestFile(t, cost)); got != "9" {
				t.Errorf("message_cost after %s = %q, want 9", path, got)
			}
		})
	}
}

func TestKernelCaptureReadsAndSummarisesLogs(t *testing.T) {
	control := writeKernelTestFile(t, "debug", "module amneziawg -p\n")
	cost := writeKernelTestFile(t, "cost", "5\n")
	base := time.Now().Add(-time.Minute)
	lines := []string{
		journalTestLine(base, "host: driver ready", 6),
		journalTestLine(base.Add(time.Second), "kernel: net_ratelimit: 4 callbacks suppressed", 4),
		journalTestLine(base.Add(2*time.Second), "kernel: net_ratelimit: 3 callbacks suppressed", 4),
		journalTestLine(base.Add(3*time.Second), "[UFW BLOCK] IN=eth0 SRC=203.0.113.7 DST=198.51.100.2 PROTO=UDP", 6),
		journalTestLine(base.Add(4*time.Second), "mgawg123: Receiving handshake initiation from peer 17 (203.0.113.7:2408)", 7),
		journalTestLine(base.Add(5*time.Second), "mgawg123: Sending handshake response to peer 17 (203.0.113.7:2408)", 7),
		journalTestLine(base.Add(6*time.Second), "mgawg123: Receiving handshake initiation from peer 18 (203.0.113.8:2408)", 7),
		journalTestLine(base.Add(7*time.Second), "mgawg123: future module diagnostic", 6),
	}
	processes := make(chan *testKernelJournalProcess, 1)
	var gotCommand string
	var gotArgs []string
	runner := func(ctx context.Context, command string, args ...string) (kernelJournalProcess, error) {
		gotCommand, gotArgs = command, append([]string(nil), args...)
		p := newTestKernelJournalProcess(ctx, lines)
		processes <- p
		return p, nil
	}
	env := newKernelDebugEnvironment(kernelDebugOptions{journalctlPath: "fake-journalctl", dynamicDebugPaths: []string{control}, messageCostPath: cost, startJournal: runner})
	a, d, awgEngine := kernelDebugHarness(env, []awg.KernelDebugInterface{{Name: "mgawg123", InboundID: "inb_awg"}})
	if _, ok := d.start("req_summary", slog.LevelDebug, nil, time.Minute, a.debugExpired, a.prepareKernelCapture); !ok {
		t.Fatal("capture did not start")
	}
	process := <-processes
	if gotCommand != "fake-journalctl" || len(gotArgs) != 6 || !reflect.DeepEqual(gotArgs[:5], []string{"--dmesg", "--follow", "--output=json", "--no-pager", "--quiet"}) {
		t.Fatalf("journalctl command = %q %v", gotCommand, gotArgs)
	}
	wantSince := "--since=@" + strconv.FormatInt(d.cur.started.Add(-30*time.Minute).Unix(), 10)
	if gotArgs[5] != wantSince {
		t.Errorf("since argument = %q, want %q", gotArgs[5], wantSince)
	}
	waitKernelTest(t, func() bool { return kernelTestRecs(d) >= 6 })
	awgEngine.emitUnknown("mgawg123")
	d.finish("req_summary", "stopped")
	<-process.waited
	if got := strings.TrimSpace(readKernelTestFile(t, control)); got != "module amneziawg -p" {
		t.Errorf("dynamic debug after capture = %q", got)
	}
	if got := strings.TrimSpace(readKernelTestFile(t, cost)); got != "5" {
		t.Errorf("message_cost after capture = %q", got)
	}
	c, reason := d.take("req_summary")
	if c == nil || reason != "stopped" {
		t.Fatalf("finished capture = %v, %q", c, reason)
	}
	recs, _ := d.records(c)
	var awgInitiation, unknown, awgSummary, kernelSummary bool
	for _, rec := range recs {
		switch {
		case rec.source == "awg" && rec.attrs["event"] == "initiation_in" && rec.attrs["peer"] == "17" && rec.attrs["inbound"] == "inb_awg":
			awgInitiation = strings.Contains(rec.msg, "<ip>:port") && !strings.Contains(rec.msg, "203.0.113.7")
		case rec.source == "awg" && rec.attrs["event"] == "unknown_peer" && rec.attrs["inbound"] == "inb_awg":
			unknown = true
		case rec.source == "awg" && rec.attrs["event"] == "summary":
			awgSummary = strings.Contains(rec.msg, `"initiation_in":2`) && strings.Contains(rec.msg, `"response_out":1`) &&
				strings.Contains(rec.msg, "initiations_unanswered=1") && strings.Contains(rec.msg, "suppressed=7") && strings.Contains(rec.msg, "debug_on=true")
		case rec.source == "kernel" && strings.HasPrefix(rec.msg, "kernel log summary:"):
			kernelSummary = strings.Contains(rec.msg, "kept=3") && strings.Contains(rec.msg, "firewall_lines=1") && strings.Contains(rec.msg, "dropped_by_caps=0")
		}
		if strings.Contains(rec.msg, "UFW BLOCK") {
			t.Error("firewall packet log was kept")
		}
	}
	if !awgInitiation || !unknown || !awgSummary || !kernelSummary {
		t.Fatalf("records missing: initiation=%t unknown=%t awgSummary=%t kernelSummary=%t; records=%+v", awgInitiation, unknown, awgSummary, kernelSummary, recs)
	}
}

func TestKernelSummaryReportsRecordsDroppedByCaptureCaps(t *testing.T) {
	var d *debugState
	runner := func(ctx context.Context, _ string, _ ...string) (kernelJournalProcess, error) {
		// The runner is called under debugState.mu while the capture is prepared.
		d.cur.reservedRecords = debugMaxRecords
		return newTestKernelJournalProcess(ctx, []string{journalTestLine(time.Now(), "host: capped line", 6)}), nil
	}
	env := newKernelDebugEnvironment(kernelDebugOptions{startJournal: runner})
	d = newDebugState(nil, time.Now)
	a := &Agent{debug: d, kernelDebug: env, engines: map[string]engine.Engine{}}
	if _, ok := d.start("req_cap_summary", slog.LevelDebug, map[string]bool{"kernel": true}, time.Minute, a.debugExpired, a.prepareKernelCapture); !ok {
		t.Fatal("capture did not start")
	}
	waitKernelTest(t, func() bool { return kernelTestDropped(d) == 1 })
	d.finish("req_cap_summary", "stopped")
	recs, _ := d.records(d.done.c)
	for _, rec := range recs {
		if rec.source == "kernel" && strings.HasPrefix(rec.msg, "kernel log summary:") {
			if !strings.Contains(rec.msg, "kept=0") || !strings.Contains(rec.msg, "dropped_by_caps=1") {
				t.Fatalf("kernel summary = %q", rec.msg)
			}
			return
		}
	}
	t.Fatalf("kernel summary missing from records: %+v", recs)
}

func TestAWGKernelCaptureReadsWithoutKernelSourceAndReportsUnavailable(t *testing.T) {
	t.Run("AWG only still starts the kernel reader", func(t *testing.T) {
		control := writeKernelTestFile(t, "debug", "module amneziawg -p\n")
		cost := writeKernelTestFile(t, "cost", "5\n")
		lines := []string{journalTestLine(time.Now(), "kernel: not requested", 6), journalTestLine(time.Now(), "mgawg123: Sending cookie response", 7)}
		processes := make(chan *testKernelJournalProcess, 1)
		runner := func(ctx context.Context, _ string, _ ...string) (kernelJournalProcess, error) {
			p := newTestKernelJournalProcess(ctx, lines)
			processes <- p
			return p, nil
		}
		env := newKernelDebugEnvironment(kernelDebugOptions{dynamicDebugPaths: []string{control}, messageCostPath: cost, startJournal: runner})
		a, d, _ := kernelDebugHarness(env, []awg.KernelDebugInterface{{Name: "mgawg123", InboundID: "inb_awg"}})
		if _, ok := d.start("req_awg_only", slog.LevelDebug, map[string]bool{"awg": true}, time.Minute, a.debugExpired, a.prepareKernelCapture); !ok {
			t.Fatal("capture did not start")
		}
		process := <-processes
		waitKernelTest(t, func() bool { return kernelTestRecs(d) >= 1 })
		d.finish("req_awg_only", "stopped")
		<-process.waited
		recs, _ := d.records(d.done.c)
		for _, rec := range recs {
			if rec.source == "kernel" {
				t.Fatalf("kernel-only record was kept during AWG-only capture: %+v", rec)
			}
		}
		if len(recs) != 2 || recs[0].source != "awg" || recs[1].attrs["event"] != "summary" {
			t.Fatalf("AWG-only records = %+v", recs)
		}
	})

	t.Run("journalctl unavailable", func(t *testing.T) {
		env := newKernelDebugEnvironment(kernelDebugOptions{startJournal: func(context.Context, string, ...string) (kernelJournalProcess, error) {
			return nil, errors.New("journalctl not found")
		}})
		d := newDebugState(nil, time.Now)
		a := &Agent{debug: d, kernelDebug: env, engines: map[string]engine.Engine{}}
		if _, ok := d.start("req_no_journal", slog.LevelDebug, map[string]bool{"kernel": true}, time.Minute, a.debugExpired, a.prepareKernelCapture); !ok {
			t.Fatal("capture did not start")
		}
		d.finish("req_no_journal", "stopped")
		recs, _ := d.records(d.done.c)
		if got := countDebugCode(recs, "kernel_log_unavailable"); got != 1 {
			t.Fatalf("kernel_log_unavailable warnings = %d; records=%+v", got, recs)
		}
	})
	t.Run("malformed journal output", func(t *testing.T) {
		processes := make(chan *testKernelJournalProcess, 1)
		env := newKernelDebugEnvironment(kernelDebugOptions{startJournal: func(ctx context.Context, _ string, _ ...string) (kernelJournalProcess, error) {
			p := newTestKernelJournalProcess(ctx, []string{"not JSON", journalTestLine(time.Now(), "host: after malformed line", 6)})
			processes <- p
			return p, nil
		}})
		a, d, _ := kernelDebugHarness(env, nil)
		if _, ok := d.start("req_bad_journal", slog.LevelDebug, map[string]bool{"kernel": true}, time.Minute, a.debugExpired, a.prepareKernelCapture); !ok {
			t.Fatal("capture did not start")
		}
		process := <-processes
		d.mu.Lock()
		capture := d.cur
		d.mu.Unlock()
		waitKernelTest(t, func() bool {
			recs, _ := d.records(capture)
			return len(recs) == 1
		})
		d.finish("req_bad_journal", "stopped")
		<-process.waited
		recs, _ := d.records(d.done.c)
		if got := countDebugCode(recs, "kernel_log_unavailable"); got != 1 {
			t.Fatalf("kernel_log_unavailable warnings = %d; records=%+v", got, recs)
		}
	})

	t.Run("dynamic debug unavailable", func(t *testing.T) {
		cost := writeKernelTestFile(t, "cost", "5\n")
		lines := []string{journalTestLine(time.Now(), "mgawg123: Sending cookie response", 7)}
		processes := make(chan *testKernelJournalProcess, 1)
		runner := func(ctx context.Context, _ string, _ ...string) (kernelJournalProcess, error) {
			p := newTestKernelJournalProcess(ctx, lines)
			processes <- p
			return p, nil
		}
		env := newKernelDebugEnvironment(kernelDebugOptions{dynamicDebugPaths: []string{filepath.Join(t.TempDir(), "missing-control")}, messageCostPath: cost, startJournal: runner})
		a, d, _ := kernelDebugHarness(env, []awg.KernelDebugInterface{{Name: "mgawg123", InboundID: "inb_awg"}})
		if _, ok := d.start("req_no_dynamic", slog.LevelDebug, map[string]bool{"awg": true}, time.Minute, a.debugExpired, a.prepareKernelCapture); !ok {
			t.Fatal("capture did not start")
		}
		process := <-processes
		waitKernelTest(t, func() bool { return kernelTestRecs(d) == 1 })
		d.finish("req_no_dynamic", "stopped")
		<-process.waited
		recs, _ := d.records(d.done.c)
		if got := countDebugCode(recs, "awg_kernel_debug_unavailable"); got != 1 {
			t.Fatalf("awg_kernel_debug_unavailable warnings = %d; records=%+v", got, recs)
		}
		var summaryOn bool
		for _, rec := range recs {
			if rec.attrs["event"] == "summary" {
				summaryOn = strings.Contains(rec.msg, "debug_on=false")
			}
		}
		if !summaryOn {
			t.Fatalf("AWG summary did not report the unavailable dynamic debug: %+v", recs)
		}
		if got := strings.TrimSpace(readKernelTestFile(t, cost)); got != "5" {
			t.Errorf("message_cost after unavailable debug = %q", got)
		}
	})
}

func kernelDebugHarness(env *kernelDebugEnvironment, interfaces []awg.KernelDebugInterface) (*Agent, *debugState, *testKernelDebugEngine) {
	d := newDebugState(nil, time.Now)
	e := &testKernelDebugEngine{interfaces: append([]awg.KernelDebugInterface(nil), interfaces...)}
	a := &Agent{debug: d, kernelDebug: env, engines: map[string]engine.Engine{awg.Protocol: e}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return a, d, e
}

type testKernelDebugEngine struct {
	engine.Engine
	mu         sync.Mutex
	interfaces []awg.KernelDebugInterface
	watchers   map[uint64]func(string)
	next       uint64
}

func (e *testKernelDebugEngine) KernelDebugInfo() (bool, []awg.KernelDebugInterface) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return true, append([]awg.KernelDebugInterface(nil), e.interfaces...)
}

func (e *testKernelDebugEngine) WatchKernelUnknownPeers(fn func(string)) (bool, func()) {
	e.mu.Lock()
	if e.watchers == nil {
		e.watchers = map[uint64]func(string){}
	}
	e.next++
	id := e.next
	e.watchers[id] = fn
	e.mu.Unlock()
	return true, func() {
		e.mu.Lock()
		delete(e.watchers, id)
		e.mu.Unlock()
	}
}

func (e *testKernelDebugEngine) emitUnknown(ifname string) {
	e.mu.Lock()
	watchers := make([]func(string), 0, len(e.watchers))
	for _, fn := range e.watchers {
		watchers = append(watchers, fn)
	}
	e.mu.Unlock()
	for _, fn := range watchers {
		fn(ifname)
	}
}

type testKernelJournalProcess struct {
	ctx     context.Context
	reader  *io.PipeReader
	writer  *io.PipeWriter
	written chan struct{}
	waited  chan struct{}
	waitErr error
	wait    sync.Once
}

func newTestKernelJournalProcess(ctx context.Context, lines []string) *testKernelJournalProcess {
	reader, writer := io.Pipe()
	p := &testKernelJournalProcess{ctx: ctx, reader: reader, writer: writer, written: make(chan struct{}), waited: make(chan struct{})}
	go func() {
		for _, line := range lines {
			if _, err := io.WriteString(writer, line+"\n"); err != nil {
				break
			}
		}
		close(p.written)
		<-ctx.Done()
		_ = writer.Close()
	}()
	return p
}

func (p *testKernelJournalProcess) Read(b []byte) (int, error) { return p.reader.Read(b) }
func (p *testKernelJournalProcess) Close() error               { return p.reader.Close() }
func (p *testKernelJournalProcess) Wait() error {
	<-p.ctx.Done()
	p.wait.Do(func() { close(p.waited) })
	return p.waitErr
}

func journalTestLine(at time.Time, message string, priority int) string {
	line, err := json.Marshal(map[string]string{"MESSAGE": message, "PRIORITY": strconv.Itoa(priority), "__REALTIME_TIMESTAMP": strconv.FormatInt(at.UnixMicro(), 10)})
	if err != nil {
		panic(err)
	}
	return string(line)
}

func writeKernelTestFile(t *testing.T, name, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readKernelTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// kernelTestRecs and kernelTestDropped read the running capture under its lock: the journal reader appends to it meanwhile.
func kernelTestRecs(d *debugState) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cur.recs)
}

func kernelTestDropped(d *debugState) uint32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.cur.dropped
}

func waitKernelTest(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for kernel capture condition")
}

func countDebugCode(records []logRecord, code string) int {
	n := 0
	for _, record := range records {
		if record.attrs["code"] == code {
			n++
		}
	}
	return n
}

func TestKernelJournalCommandArguments(t *testing.T) {
	var gotName string
	var gotArgs []string
	runner := func(_ context.Context, name string, args ...string) (kernelJournalProcess, error) {
		gotName, gotArgs = name, args
		return nil, errors.New("expected runner stop")
	}
	env := newKernelDebugEnvironment(kernelDebugOptions{journalctlPath: "journalctl-test", startJournal: runner})
	_, _ = env.startJournal(context.Background(), time.Unix(1_700_000_000, 0))
	want := []string{"--dmesg", "--follow", "--output=json", "--no-pager", "--quiet", "--since=@1700000000"}
	if gotName != "journalctl-test" || !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("command %q %v; want journalctl-test %v", gotName, gotArgs, want)
	}
}

var _ kernelJournalProcess = (*testKernelJournalProcess)(nil)
var _ engine.Engine = (*testKernelDebugEngine)(nil)
