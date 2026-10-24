package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
)

func TestJournalDeduplicatesByBatchKeyAndKeepsFirstAttrs(t *testing.T) {
	baseAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	base := logRecord{
		at: baseAt, level: pb.Severity_SEVERITY_WARNING, source: "hysteria2", msg: "auth failed",
		attrs: map[string]string{"inbound": "in-1", "protocol": "hysteria2", "code": "bad_auth", "state": "first"},
	}
	cases := []struct {
		name string
		edit func(*logRecord)
	}{
		{name: "same key, new attrs", edit: func(r *logRecord) { r.attrs["state"] = "later" }},
		{name: "level", edit: func(r *logRecord) { r.level = pb.Severity_SEVERITY_ERROR }},
		{name: "source", edit: func(r *logRecord) { r.source = "vless" }},
		{name: "message", edit: func(r *logRecord) { r.msg = "different" }},
		{name: "inbound", edit: func(r *logRecord) { r.attrs["inbound"] = "in-2" }},
		{name: "code", edit: func(r *logRecord) { r.attrs["code"] = "other" }},
	}

	j := newJournal()
	j.note(base)
	for i, tc := range cases {
		rec := logRecord{
			at: baseAt.Add(time.Duration(i+1) * time.Second), level: base.level, source: base.source, msg: base.msg,
			attrs: map[string]string{"inbound": "in-1", "protocol": "hysteria2", "code": "bad_auth", "state": "later"},
		}
		if tc.name != "same key, new attrs" {
			rec.attrs["state"] = "first"
		}
		tc.edit(&rec)
		t.Run(tc.name, func(t *testing.T) { j.note(rec) })
	}

	rows := j.take(0)
	if len(rows) != 6 {
		t.Fatalf("journal rows = %d, want 6: %+v", len(rows), rows)
	}
	var baseRow *pb.JournalEntry
	for _, row := range rows {
		if row.Source == "hysteria2" && row.Message == "auth failed" && row.InboundId == "in-1" && row.Attrs["code"] == "bad_auth" && row.Level == pb.Severity_SEVERITY_WARNING {
			baseRow = row
		}
	}
	if baseRow == nil {
		t.Fatalf("base row missing from %+v", rows)
	}
	if baseRow.Count != 2 || baseRow.FirstUnix != baseAt.Unix() || baseRow.LastUnix != baseAt.Add(time.Second).Unix() {
		t.Fatalf("deduplicated times/count = %+v", baseRow)
	}
	if baseRow.Attrs["state"] != "first" {
		t.Fatalf("attrs came from a later row: %+v", baseRow.Attrs)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i-1].FirstUnix > rows[i].FirstUnix {
			t.Fatalf("entries are not sorted by first timestamp: %+v", rows)
		}
	}
}

func TestJournalUsesTemplateScrubsAndLimitsAttributes(t *testing.T) {
	attrs := map[string]string{
		"msg_template": "template from 203.0.113.7:51234 and [2001:db8::7]:443",
		"inbound":      "203.0.113.55",
		"protocol":     "vless",
		"profile":      "profile [2001:db8::2]:8443",
		"port":         "443",
		"state":        "failed",
		"reason":       "peer 198.51.100.8:9999 via [2001:db8::8]:443",
		"code":         "bad_auth",
		"backend":      "userspace",
		"mode":         "must be beyond the first eight",
		"err":          "must also be omitted",
		"note":         "not allowlisted",
	}
	j := newJournal()
	j.note(logRecord{
		at: time.Unix(100, 0), level: pb.Severity_SEVERITY_ERROR, source: "awg",
		msg: "formatted message with 198.51.100.7:1234", attrs: attrs,
	})
	rows := j.take(0)
	if len(rows) != 1 {
		t.Fatalf("journal rows = %+v", rows)
	}
	row := rows[0]
	if want := "template from <ip>:port and <ip>:port"; row.Message != want {
		t.Errorf("message = %q, want %q", row.Message, want)
	}
	if row.InboundId != "<ip>" {
		t.Errorf("inbound_id = %q, want <ip>", row.InboundId)
	}
	wantAttrs := map[string]string{
		"inbound": "<ip>", "protocol": "vless", "profile": "profile <ip>:port", "port": "443",
		"state": "failed", "reason": "peer <ip>:port via <ip>:port", "code": "bad_auth", "backend": "userspace",
	}
	if !reflect.DeepEqual(row.Attrs, wantAttrs) {
		t.Errorf("attrs = %#v, want %#v", row.Attrs, wantAttrs)
	}
	if _, ok := row.Attrs["msg_template"]; ok {
		t.Error("msg_template must not be sent as a journal attribute")
	}

	// xray's text may name a destination: names go too, and only there
	j.note(logRecord{at: time.Unix(101, 0), level: pb.Severity_SEVERITY_WARNING, source: "xray",
		msg: "core: failed to dial destination.example.com", attrs: map[string]string{"err": "lookup visit.example.org"}})
	j.note(logRecord{at: time.Unix(101, 0), level: pb.Severity_SEVERITY_WARNING, source: "agent",
		msg: "certificate for vpn.example.com renews", attrs: map[string]string{}})
	for _, r := range j.take(0) {
		switch r.Source {
		case "xray":
			if r.Message != "core: failed to dial <host>" || r.Attrs["err"] != "lookup <host>" {
				t.Errorf("xray entry = %q %v", r.Message, r.Attrs)
			}
		case "agent":
			if r.Message != "certificate for vpn.example.com renews" {
				t.Errorf("agent entry lost its name: %q", r.Message)
			}
		}
	}
}

func TestJournalMessageClipIsByteBoundedAndUtf8Safe(t *testing.T) {
	j := newJournal()
	j.note(logRecord{
		at: time.Unix(1, 0), level: pb.Severity_SEVERITY_WARNING, source: "agent",
		msg: strings.Repeat("я", 200), attrs: map[string]string{"err": strings.Repeat("я", 161)},
	})
	row := j.take(0)[0]
	if len(row.Message) != 256 || !utf8.ValidString(row.Message) {
		t.Fatalf("message bytes/UTF-8 validity = %d/%v", len(row.Message), utf8.ValidString(row.Message))
	}
	if got := utf8.RuneCountInString(row.Attrs["err"]); got != 160 {
		t.Fatalf("attribute runes = %d, want 160", got)
	}
}

func TestJournalSortsByTimeThenTupleKey(t *testing.T) {
	at := time.Unix(20, 0)
	j := newJournal()
	for _, rec := range []logRecord{
		{at: at, level: pb.Severity_SEVERITY_WARNING, source: "z", msg: "same"},
		{at: at, level: pb.Severity_SEVERITY_WARNING, source: "a", msg: "same"},
		{at: at, level: pb.Severity_SEVERITY_WARNING, source: "a", msg: "a"},
		{at: at.Add(-time.Second), level: pb.Severity_SEVERITY_ERROR, source: "x", msg: "earlier"},
	} {
		j.note(rec)
	}
	rows := j.take(0)
	want := []journalKey{
		{level: pb.Severity_SEVERITY_ERROR, source: "x", message: "earlier"},
		{level: pb.Severity_SEVERITY_WARNING, source: "a", message: "a"},
		{level: pb.Severity_SEVERITY_WARNING, source: "a", message: "same"},
		{level: pb.Severity_SEVERITY_WARNING, source: "z", message: "same"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %+v", rows)
	}
	for i, row := range rows {
		got := journalKey{level: row.Level, source: row.Source, message: row.Message, inboundID: row.InboundId}
		if got != want[i] {
			t.Errorf("row %d key = %+v, want %+v", i, got, want[i])
		}
	}
}

func TestRingHandlerJournalsOnlyWarningsAndErrors(t *testing.T) {
	j, ring := newJournal(), newLogRing()
	h := &ringHandler{ring: ring, journal: j, inner: slog.NewTextHandler(io.Discard, nil)}
	for _, tc := range []struct {
		level slog.Level
		msg   string
	}{
		{level: slog.LevelInfo, msg: "info is live-only"},
		{level: slog.LevelWarn, msg: "warning"},
		{level: slog.LevelError, msg: "error"},
	} {
		r := slog.NewRecord(time.Unix(10, 0), tc.level, tc.msg, 0)
		if err := h.Handle(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	rows := j.take(0)
	if len(rows) != 2 || rows[0].Message != "warning" || rows[1].Message != "error" {
		t.Fatalf("WARN+ journal rows = %+v", rows)
	}
}

func TestJournalOverflowKeeps63KindsAndCountsDroppedRecords(t *testing.T) {
	baseAt := time.Unix(1000, 0)
	j := newJournal()
	for i := 0; i < 65; i++ {
		j.note(logRecord{
			at: baseAt.Add(time.Duration(i) * time.Second), level: pb.Severity_SEVERITY_WARNING,
			source: "agent", msg: fmt.Sprintf("kind-%02d", i),
		})
	}
	rows := j.take(0)
	if len(rows) != 64 {
		t.Fatalf("journal rows = %d, want 64", len(rows))
	}
	overflow := rows[len(rows)-1]
	if overflow.Level != pb.Severity_SEVERITY_WARNING || overflow.Source != "agent" || overflow.Message != "journal_overflow" || overflow.Count != 2 {
		t.Fatalf("overflow row = %+v", overflow)
	}
	if overflow.FirstUnix != baseAt.Add(63*time.Second).Unix() || overflow.LastUnix != baseAt.Add(64*time.Second).Unix() {
		t.Fatalf("overflow times = %+v", overflow)
	}
}

func TestJournalNoteAndTakeCanRunConcurrently(t *testing.T) {
	const workers, each = 4, 500
	j := newJournal()
	start := make(chan struct{})
	var wg sync.WaitGroup
	var drained atomic.Uint64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < each; i++ {
				j.note(logRecord{
					at: time.Unix(int64(i), 0), level: pb.Severity_SEVERITY_WARNING,
					source: "agent", msg: "same key",
				})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < workers*each; i++ {
			for _, row := range j.take(0) {
				drained.Add(uint64(row.Count))
			}
		}
	}()
	close(start)
	wg.Wait()
	for _, row := range j.take(0) {
		drained.Add(uint64(row.Count))
	}
	if got, want := drained.Load(), uint64(workers*each); got != want {
		t.Fatalf("drained count = %d, want %d", got, want)
	}
}

func TestBuildBatchTakesAndClearsJournalWithCorrectedTime(t *testing.T) {
	baseAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	j := newJournal()
	j.note(logRecord{
		at: baseAt, level: pb.Severity_SEVERITY_WARNING, source: "hysteria2", msg: "auth failed",
	})
	a := &Agent{journal: j, host: &fakeHost{}, engines: map[string]engine.Engine{}}
	a.offset.Store(90)
	first := a.buildBatch(context.Background(), baseAt, baseAt.Add(time.Second))
	if len(first.Journal) != 1 || first.Journal[0].FirstUnix != baseAt.Add(90*time.Second).Unix() || first.Journal[0].LastUnix != baseAt.Add(90*time.Second).Unix() {
		t.Fatalf("batch journal = %+v", first.Journal)
	}
	second := a.buildBatch(context.Background(), baseAt.Add(time.Second), baseAt.Add(2*time.Second))
	if len(second.Journal) != 0 {
		t.Fatalf("journal was not cleared after buildBatch: %+v", second.Journal)
	}
}

func TestCapabilitiesIncludeJournalV1(t *testing.T) {
	a := &Agent{engines: map[string]engine.Engine{}, host: &fakeHost{}}
	for _, cap := range a.capabilities() {
		if cap == "journal/1" {
			return
		}
	}
	t.Fatalf("capabilities = %v, missing journal/1", a.capabilities())
}
