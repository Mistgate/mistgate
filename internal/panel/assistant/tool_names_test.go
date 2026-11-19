//go:build !js

package assistant

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/mcp"
)

// The assistant says tool names in four places of its own: the prompt, the note of a scheduled run, the hints a plan's result gives
// and the lists of tools whose arguments it keeps out of the saved history. None of them may name a tool the registry does not have,
// or a rename of a tool would leave the model told to call one that is gone.

func registryNames(t *testing.T) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	for _, in := range mcp.Tools() {
		names[in.Name] = true
	}
	return names
}

func localToolNames() map[string]bool {
	names := map[string]bool{}
	for _, tool := range slices.Concat(assistantToolSchemas(), []mcp.AssistantTool{followUpVerdictSchema(), verdictSchema()}) {
		names[tool.Name] = true
	}
	return names
}

// words of the prompt that are written like a tool name and are not one: the names of arguments and of fields the model reads.
var notToolWords = map[string]bool{
	"changes_only": true, "plan_tool": true, "plan_id": true, "in_seconds": true, "mark_ready": true,
	"change_files": true, "remove_files": true, "site_id": true, "not_blocked_but_broken": true,
	"ip_blocked": true, "port_blocked": true, "tcp_throttled": true, "udp_blocked": true, "whitelist_mode": true,
}

var snakeWord = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)+\b`)

func TestPromptsNameToolsThatExist(t *testing.T) {
	known, local := registryNames(t), localToolNames()
	for name := range known {
		if mcp.KindOf(name) == mcp.KindPlan {
			known[mcp.ChangeOf(name)] = true // the prompt also speaks of a change by its name: path_speed, node_debug_capture
		}
	}
	for where, text := range map[string]string{"assistantFixedPrompt": assistantFixedPrompt, "scheduledNote": scheduledNote, "toolLimitNote": toolLimitNote} {
		for _, word := range snakeWord.FindAllString(text, -1) {
			if !known[word] && !local[word] && !notToolWords[word] {
				t.Errorf("%s says %q: no tool of the registry or of the assistant has that name", where, word)
			}
		}
	}
	for _, word := range []string{"fleet_digest", "path_speed_plan", "follow_up", "decoy_site_draft_save"} {
		if !strings.Contains(assistantFixedPrompt, word) {
			t.Errorf("the prompt no longer mentions %s: the scan above looks at the wrong text", word)
		}
	}
}

// The tools whose arguments or results the saved history keeps short are tools of the registry (a plan by its _plan name).
func TestHistoryToolListsNameToolsThatExist(t *testing.T) {
	known := registryNames(t)
	for _, name := range slices.Concat(bulkyTools, textReadTools, []string{templateTool}) {
		if !known[name] {
			t.Errorf("%s is no tool of the registry", name)
		}
	}
	for _, name := range bulkyTools {
		if k := mcp.KindOf(name); k != mcp.KindPlan && k != mcp.KindRun {
			t.Errorf("%s carries the files of a site in its arguments but is a %s tool", name, k)
		}
	}
}

// A follow-up hint is given for a change of the registry, under the name its plan card carries, and what it tells the model to read
// is a tool that exists.
func TestFollowUpPlansAreChangesOfTheRegistry(t *testing.T) {
	known := registryNames(t)
	for change, when := range followUpAfter {
		if !known[mcp.PlanTool(change)] {
			t.Errorf("followUpAfter[%q] is no change of the registry", change)
		}
		if when.seconds <= 0 || when.what == "" {
			t.Errorf("followUpAfter[%q] = %+v", change, when)
		}
		for _, word := range snakeWord.FindAllString(when.what, -1) {
			if !known[word] {
				t.Errorf("followUpAfter[%q] sends the model to %q, which is no tool", change, word)
			}
		}
	}
}

// One limit of tool calls to a turn, said by the prompt and by the constant of the scheduled run alike, and the same number in the
// texts the owner reads: the documentation of the three languages, the line that says the chat stopped, and the proto.
func TestToolCallLimitIsWhatTheTextsSay(t *testing.T) {
	n := strconv.Itoa(maxTurnToolCalls)
	if scheduledToolCalls != maxTurnToolCalls {
		t.Errorf("a scheduled run may make %d tool calls, a chat turn %d", scheduledToolCalls, maxTurnToolCalls)
	}
	if !strings.Contains(assistantFixedPrompt, "at most "+n+" tool calls") {
		t.Errorf("the prompt does not say %s tool calls", n)
	}
	root := filepath.Join("..", "..", "..")
	for _, f := range []struct{ path, want string }{
		{"docs/en/guide/assistant.md", "at most " + n + " tool calls"},
		{"docs/ru/guide/assistant.md", "не больше " + n + " вызовов"},
		{"docs/zh/guide/assistant.md", n + " 次工具调用"},
		{"web/src/i18n/ai.ts", "at most " + n + " tools"},
		{"web/src/i18n/ai.ts", "не больше " + n + " инструментов"},
		{"web/src/i18n/ai.ts", "最多使用 " + n + " 个工具"},
		{"proto/mistgate/admin/v1/assistant.proto", "at most " + n + " tool calls"},
	} {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(f.path)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), f.want) {
			t.Errorf("%s does not say %q", f.path, f.want)
		}
	}
}
