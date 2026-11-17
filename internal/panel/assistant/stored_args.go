//go:build !js

package assistant

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
)

// A site the model writes for node_decoy_plan, decoy_site_draft_save or decoy_site_version_plan is a few tens of kilobytes of file
// text, and a Mihomo template for subscription_mihomo_template_set_plan is up to 64 KiB. The plan card lists the files and their
// sizes (the template's name, groups, size and hosts), so neither the dialog's saved history nor the tool line in the chat keeps the
// contents: they hold the size and a short hash of each in their place. The turn itself still has the whole call, so the model can
// go on with it. The text of a site's file that decoy_site_file returns is kept out of the saved history the same way.

const templateTool = "subscription_mihomo_template_set_plan"

// bulkyTools carry the files of a site in their arguments.
var bulkyTools = []string{"node_decoy_plan", "decoy_site_draft_save", "decoy_site_version_plan"}

func isBulky(tool string) bool { return slices.Contains(bulkyTools, tool) || tool == templateTool }

// compactToolArgs cuts the bulk out of the arguments of one call by its tool; it reports whether it changed anything.
func compactToolArgs(tool string, args map[string]any) bool {
	if slices.Contains(bulkyTools, tool) {
		return compactDecoyArgs(args)
	}
	if tool == templateTool {
		if s, ok := args["yaml"].(string); ok {
			args["yaml"] = omitted(s)
			return true
		}
	}
	return false
}

// compactBulkyArgs returns the arguments of a tool call with the contents of its files cut out; other calls pass unchanged.
func compactBulkyArgs(tool string, input []byte) []byte {
	if !isBulky(tool) {
		return input
	}
	var args map[string]any
	if json.Unmarshal(input, &args) != nil {
		return input
	}
	if !compactToolArgs(tool, args) {
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
	for _, list := range []string{"files", "change_files"} {
		files, _ := args[list].([]any)
		for _, item := range files {
			if file, ok := item.(map[string]any); ok {
				for _, key := range []string{"content", "content_base64"} {
					if s, ok := file[key].(string); ok {
						file[key], changed = omitted(s), true
					}
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

// storedTranscript is a model message as the saved history keeps it: the calls of the tools above in it are compacted. The
// message is in the provider's own shape (Anthropic content blocks or OpenAI tool_calls), so every object is looked at.
func storedTranscript(raw json.RawMessage) json.RawMessage {
	if !slices.ContainsFunc(append([]string{templateTool}, bulkyTools...), func(tool string) bool { return bytes.Contains(raw, []byte(tool)) }) {
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
		if tool, _ := v["name"].(string); isBulky(tool) {
			if input, ok := v["input"].(map[string]any); ok { // Anthropic tool_use
				changed = compactToolArgs(tool, input) || changed
			}
			if arguments, ok := v["arguments"].(string); ok { // OpenAI function call: the arguments are a JSON string
				compact := string(compactBulkyArgs(tool, []byte(arguments)))
				v["arguments"], changed = compact, changed || compact != arguments
			}
		}
		for _, item := range v {
			changed = compactTranscript(item) || changed
		}
	}
	return changed
}

// textReadTools are the reads of a text: a template, in windows of about 24 KiB, and one file of a decoy site. What the model reads is
// its own to use in the turn, but the saved history keeps the size and the hash, not the text.
var textReadTools = []string{"subscription_mihomo_template_get", "decoy_site_file"}

// toolResultMessages are the messages of one round of tool results: live is what the turn goes on with, stored what the saved history
// keeps (the text of a template read cut out). They are the same when no call of the round read a template.
func toolResultMessages(adapter adapter, calls []modelToolCall, outputs []toolOutput) (live, stored []jsonRaw) {
	live = adapter.ToolResults(calls, outputs)
	var compact []toolOutput
	for i, call := range calls {
		if !slices.Contains(textReadTools, call.name) || i >= len(outputs) || outputs[i].err {
			continue
		}
		if compact == nil {
			compact = slices.Clone(outputs)
		}
		compact[i].text = omitted(outputs[i].text)
	}
	if compact == nil {
		return live, live
	}
	return live, adapter.ToolResults(calls, compact)
}
