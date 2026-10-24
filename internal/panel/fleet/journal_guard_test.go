package fleet

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func TestGuardJournalBoundsAndFilters(t *testing.T) {
	var logs bytes.Buffer
	f := &Fleet{log: slog.New(slog.NewTextHandler(&logs, nil))}
	// a node's own keys never pass: only the agent's allow-list, at most 8 of it, each value cut
	attrs := map[string]string{"remote": "203.0.113.7", strings.Repeat("k", 33): "x"}
	for _, key := range []string{"inbound", "protocol", "profile", "port", "state", "reason", "code", "backend", "mode"} {
		attrs[key] = strings.Repeat("界", 700)
	}
	entries := make([]*agentv1.JournalEntry, maxJournalEntries+1)
	for i := range entries {
		entries[i] = &agentv1.JournalEntry{
			Level: agentv1.Severity_SEVERITY_WARNING, Source: strings.Repeat("s", 40),
			Message: strings.Repeat("я", 600), InboundId: strings.Repeat("i", 70), Attrs: attrs,
			Count: 0, FirstUnix: 1_000_020, LastUnix: 1_000_010,
		}
	}
	entries[1].Level = agentv1.Severity_SEVERITY_INFO
	g := f.guardStats(&agentv1.StatsBatch{IntervalStartUnix: 1_000_000, IntervalEndUnix: 1_000_030, Journal: entries})
	if len(g.journal) != maxJournalEntries-1 {
		t.Fatalf("kept %d journal rows, want %d warning/error rows", len(g.journal), maxJournalEntries-1)
	}
	got := g.journal[0]
	if len(got.Message) != 1024 || !utf8.ValidString(got.Message) || len(got.Source) > 32 || len(got.InboundID) > 64 {
		t.Fatalf("message/source/inbound lengths = %d/%d/%d (valid UTF-8 %v)", len(got.Message), len(got.Source), len(got.InboundID), utf8.ValidString(got.Message))
	}
	if got.Count != 1 || got.FirstAt != 1_000_010 || got.LastAt != 1_000_020 {
		t.Errorf("count/times = %d/%d/%d, want 1/1000010/1000020", got.Count, got.FirstAt, got.LastAt)
	}
	if len(got.Attrs) != 8 || got.Attrs["remote"] != "" || got.Attrs["mode"] != "" || got.Attrs["inbound"] == "" || got.Attrs["backend"] == "" {
		t.Errorf("attributes = %v keys, want the first 8 of the allow-list and no node key", len(got.Attrs))
	}
	for key, value := range got.Attrs {
		if len([]rune(value)) > 640 {
			t.Errorf("attribute %q has %d runes", key, len([]rune(value)))
		}
	}
	if !strings.Contains(logs.String(), "node journal over the row limit") {
		t.Errorf("over-limit journal did not produce a warning: %s", logs.String())
	}

	// times far from the batch's interval are pulled back to it, so no node sorts itself first
	far := f.guardStats(&agentv1.StatsBatch{IntervalStartUnix: 1_000_000, IntervalEndUnix: 1_000_030, Journal: []*agentv1.JournalEntry{{
		Level: agentv1.Severity_SEVERITY_ERROR, FirstUnix: 1, LastUnix: 1 << 62,
	}}})
	if j := far.journal[0]; j.FirstAt != 1_000_000-journalSkew || j.LastAt != 1_000_030+journalSkew {
		t.Errorf("clamped times = %d..%d", j.FirstAt, j.LastAt)
	}
}
