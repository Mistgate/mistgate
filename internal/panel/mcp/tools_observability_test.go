//go:build !js

package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPanelLogsResultShrinksAndKeepsCursor(t *testing.T) {
	v := &PanelLogsV{Level: "info", Buffered: 500, Capacity: 500, Entries: make([]panelLogEntry, 100)}
	originalEntries := len(v.Entries)
	for i := range v.Entries {
		v.Entries[i] = panelLogEntry{Seq: int64(1000 - i), Level: "info", Message: strings.Repeat("x", 300)}
	}

	result, _, err := result(v)
	if err != nil {
		t.Fatal(err)
	}
	text := result.Content[0].(*sdk.TextContent).Text
	if len(text) > maxResultBytes {
		t.Fatalf("result is %d bytes, over %d", len(text), maxResultBytes)
	}
	var got PanelLogsV
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Truncated || len(got.Entries) == 0 || len(got.Entries) >= originalEntries {
		t.Fatalf("truncated=%v entries=%d", got.Truncated, len(got.Entries))
	}
	if got.NextCursor != got.Entries[len(got.Entries)-1].Seq {
		t.Errorf("next_cursor=%d, last kept seq=%d", got.NextCursor, got.Entries[len(got.Entries)-1].Seq)
	}
}
