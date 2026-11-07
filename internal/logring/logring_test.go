package logring

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/reqid"
)

func TestRingKeepsOnlyTheLastRecords(t *testing.T) {
	var out bytes.Buffer
	log, ring, _ := New(&out, "text", slog.LevelInfo, 3)
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		log.Info("msg " + n)
	}
	if ring.Cap() != 3 || ring.Len() != 3 {
		t.Fatalf("cap %d len %d", ring.Cap(), ring.Len())
	}
	got := ring.Snapshot(0, 0, nil)
	if len(got) != 3 || got[0].Message != "msg e" || got[2].Message != "msg c" || got[0].Seq != 5 || got[2].Seq != 3 {
		t.Fatalf("snapshot: %+v", got)
	}
	// the cursor: records below the oldest seen
	older := ring.Snapshot(got[1].Seq, 0, nil)
	if len(older) != 1 || older[0].Message != "msg c" {
		t.Fatalf("older: %+v", older)
	}
	if lim := ring.Snapshot(0, 2, nil); len(lim) != 2 {
		t.Fatalf("limit: %d", len(lim))
	}
	if !strings.Contains(out.String(), "msg a") {
		t.Fatal("the wrapped handler lost a record the ring dropped")
	}
}

func TestLevelChangeAppliesAtOnceAndToDerivedLoggers(t *testing.T) {
	var out bytes.Buffer
	log, ring, lv := New(&out, "text", slog.LevelWarn, 10)
	derived := log.With("component", "fleet")
	log.Info("hidden")
	derived.Debug("hidden too")
	derived.Warn("shown", "code", "x_failed", "k", "v")
	if ring.Len() != 1 {
		t.Fatalf("held %d at warn", ring.Len())
	}
	lv.Set(slog.LevelDebug)
	derived.WithGroup("g").Debug("now visible", "k", 1)
	got := ring.Snapshot(0, 0, nil)
	if len(got) != 2 || got[0].Message != "now visible" || got[0].Component != "fleet" || got[1].Code != "x_failed" {
		t.Fatalf("records: %+v", got)
	}
	if got[0].Attrs[len(got[0].Attrs)-1].Key != "g.k" {
		t.Fatalf("group prefix lost: %+v", got[0].Attrs)
	}
	if strings.Contains(out.String(), "hidden") {
		t.Fatalf("output has a filtered record: %s", out.String())
	}
}

func TestComponentRequestIDAndJSON(t *testing.T) {
	var out bytes.Buffer
	log, ring, _ := New(&out, "json", slog.LevelInfo, 10)
	ctx := reqid.With(context.Background(), "abc12345")
	log.InfoContext(ctx, "health: prune events", "count", 3)
	log.Info("a long sentence without a colon")
	got := ring.Snapshot(0, 0, nil)
	if got[1].Component != "health" || got[1].RequestID != "abc12345" {
		t.Fatalf("record: %+v", got[1])
	}
	if got[0].Component != "" || got[0].RequestID != "" {
		t.Fatalf("record: %+v", got[0])
	}
	if !strings.Contains(out.String(), `"request_id":"abc12345"`) || !strings.HasPrefix(out.String(), "{") {
		t.Fatalf("json output: %s", out.String())
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"DEBUG": slog.LevelDebug, "info": slog.LevelInfo, " Warn ": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError} {
		if got, ok := ParseLevel(in); !ok || got != want {
			t.Errorf("%q: %v %v", in, got, ok)
		}
	}
	if _, ok := ParseLevel("loud"); ok {
		t.Fatal("loud parsed")
	}
	if LevelName(slog.LevelWarn) != "warn" || LevelName(slog.LevelDebug-2) != "debug" {
		t.Fatal("level names")
	}
}
