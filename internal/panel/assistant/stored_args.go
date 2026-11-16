//go:build !js

package assistant

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// A site the model writes for node_decoy_plan is a few tens of kilobytes of file text. The plan card lists the files and
// their sizes, so neither the dialog's saved history nor the tool line in the chat keeps the contents: they hold the size and
// a short hash of each in their place. The turn itself still has the whole call, so the model can go on with it.

const bulkyTool = "node_decoy_plan"

// compactBulkyArgs returns the arguments of a tool call with the contents of its files cut out; other calls pass unchanged.
func compactBulkyArgs(tool string, input []byte) []byte {
	if tool != bulkyTool {
		return input
	}
	var args map[string]any
	if json.Unmarshal(input, &args) != nil {
		return input
	}
	if !compactDecoyArgs(args) {
		return input
	}
	out, err := json.Marshal(args)
	if err != nil {
		return input
	}
	return out
}

// compactDecoyArgs reports whether it changed anything.
func compactDecoyArgs(args map[string]any) (changed bool) {
	if s, ok := args["bundle_base64"].(string); ok {
		args["bundle_base64"], changed = omitted(s), true
	}
	files, _ := args["files"].([]any)
	for _, item := range files {
		if file, ok := item.(map[string]any); ok {
			for _, key := range []string{"content", "content_base64"} {
				if s, ok := file[key].(string); ok {
					file[key], changed = omitted(s), true
				}
			}
		}
	}
	return changed
}

func omitted(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("[omitted from history: %d bytes, sha256 %s]", len(s), hex.EncodeToString(sum[:6]))
}

// storedTranscript is a model message as the saved history keeps it: the calls of node_decoy_plan in it are compacted. The
// message is in the provider's own shape (Anthropic content blocks or OpenAI tool_calls), so every object is looked at.
func storedTranscript(raw json.RawMessage) json.RawMessage {
	if !bytes.Contains(raw, []byte(bulkyTool)) {
		return raw
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var message any
	if dec.Decode(&message) != nil {
		return raw
	}
	if !compactTranscript(message) {
		return raw
	}
	out, err := json.Marshal(message)
	if err != nil {
		return raw
	}
	return out
}

// compactTranscript reports whether it changed anything.
func compactTranscript(v any) (changed bool) {
	switch v := v.(type) {
	case []any:
		for _, item := range v {
			changed = compactTranscript(item) || changed
		}
	case map[string]any:
		if v["name"] == bulkyTool {
			if input, ok := v["input"].(map[string]any); ok { // Anthropic tool_use
				changed = compactDecoyArgs(input) || changed
			}
			if arguments, ok := v["arguments"].(string); ok { // OpenAI function call: the arguments are a JSON string
				compact := string(compactBulkyArgs(bulkyTool, []byte(arguments)))
				v["arguments"], changed = compact, changed || compact != arguments
			}
		}
		for _, item := range v {
			changed = compactTranscript(item) || changed
		}
	}
	return changed
}
