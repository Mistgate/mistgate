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
	attrs := map[string]string{}
	for _, key := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"} {
		attrs[key] = strings.Repeat("界", 161)
	}
	attrs[strings.Repeat("k", 33)] = "clipped key"
	entries := make([]*agentv1.JournalEntry, maxJournalEntries+1)
	for i := range entries {
		entries[i] = &agentv1.JournalEntry{
			Level: agentv1.Severity_SEVERITY_WARNING, Source: strings.Repeat("s", 40),
			Message: strings.Repeat("я", 130), InboundId: strings.Repeat("i", 70), Attrs: attrs,
			Count: 0, FirstUnix: 20, LastUnix: 10,
		}
	}
	entries[1].Level = agentv1.Severity_SEVERITY_INFO
	g := f.guardStats(&agentv1.StatsBatch{Journal: entries})
	if len(g.journal) != maxJournalEntries-1 {
		t.Fatalf("kept %d journal rows, want %d warning/error rows", len(g.journal), maxJournalEntries-1)
	}
	got := g.journal[0]
	if len(got.Message) > 256 || len(got.Source) > 32 || len(got.InboundID) > 64 {
		t.Fatalf("message/source/inbound lengths = %d/%d/%d", len(got.Message), len(got.Source), len(got.InboundID))
	}
	if !utf8.ValidString(got.Message) || len(got.Message) != 256 {
		t.Errorf("message clipping split a UTF-8 rune or missed the byte cap: %d bytes", len(got.Message))
	}
	if got.Count != 1 || got.FirstAt != 10 || got.LastAt != 20 {
		t.Errorf("count/times = %d/%d/%d, want 1/10/20", got.Count, got.FirstAt, got.LastAt)
	}
	if len(got.Attrs) != 8 || got.Attrs["a"] == "" || got.Attrs["h"] == "" || got.Attrs["i"] != "" {
		t.Errorf("deterministic attribute cap = %#v", got.Attrs)
	}
	for key, value := range got.Attrs {
		if len([]rune(key)) > 32 || len([]rune(value)) > 160 {
			t.Errorf("attribute %q has %d key runes and %d value runes", key, len([]rune(key)), len([]rune(value)))
		}
	}
	if !strings.Contains(logs.String(), "node journal over the row limit") {
		t.Errorf("over-limit journal did not produce a warning: %s", logs.String())
	}
	key := strings.Repeat("k", 33)
	clipped := f.guardStats(&agentv1.StatsBatch{Journal: []*agentv1.JournalEntry{{
		Level: agentv1.Severity_SEVERITY_ERROR, Attrs: map[string]string{key: "value"},
	}}})
	if _, ok := clipped.journal[0].Attrs[strings.Repeat("k", 32)]; !ok {
		t.Errorf("long attribute key was not clipped to 32 runes: %#v", clipped.journal[0].Attrs)
	}
}
