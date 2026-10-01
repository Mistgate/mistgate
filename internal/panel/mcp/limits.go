package mcp

import (
	"slices"
	"strings"
	"unicode"
)

// The limits of the MCP server. Strings that came from data (names, notes, log lines) are untrusted and
// cut short; whole results are capped; lists are paged.
const (
	maxResultBytes   = 32 << 10 // a tool result, serialised
	maxBodyBytes     = 256 << 10
	maxConcurrent    = 4 // tool calls at once per token
	maxName          = 200
	maxNote          = 400
	maxParamValue    = 120
	maxParams        = 12
	maxAuditParams   = 500
	maxError         = 300
	maxReason        = 300
	maxArgsJSON      = 8 << 10
	maxIDs           = 50
	maxOnlineUsers   = 10
	defaultUsersPage = 25
	maxUsersPage     = 50
	defaultListLimit = 50
	maxListLimit     = 100

	// MaxUnattendedBulk is how many users one user_disable may name before it needs the owner.
	MaxUnattendedBulk = 3
)

// clean makes a string from data safe to show an agent: control characters, line breaks and runs of blanks become one
// space, invisible format characters (zero width, bidi overrides) go, and the rest is cut to max runes with an ellipsis.
func clean(s string, max int) string {
	s = scrub(s)
	var b strings.Builder
	n := 0
	space := false
	for _, r := range s {
		switch {
		case unicode.IsControl(r) || unicode.IsSpace(r):
			r = ' '
		case unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r):
			continue
		}
		if r == ' ' {
			if space || b.Len() == 0 {
				continue
			}
			space = true
		} else {
			space = false
		}
		if n >= max {
			return strings.TrimRight(b.String(), " ") + "…"
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimRight(b.String(), " ")
}

// cleanMap cleans a map of parameters from data: at most maxParams keys (the first in sorted order), each value cut to
// valueMax.
func cleanMap(m map[string]string, valueMax int) map[string]string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make(map[string]string, min(len(keys), maxParams))
	for _, k := range keys {
		if len(out) == maxParams {
			break
		}
		out[clean(k, 40)] = clean(m[k], valueMax)
	}
	return out
}

// enumName turns a generated enum's String() ("NODE_STATUS_ONLINE") into the lowercase word an agent reads ("online").
func enumName(prefix, s string) string {
	return strings.ToLower(strings.TrimPrefix(s, prefix))
}

// cut halves a list (keeps at least one element); false when there is nothing left to cut.
func cut[T any](s *[]T) bool {
	if n := len(*s); n > 1 {
		*s = (*s)[:n/2]
		return true
	}
	return false
}

// shrinker is implemented by the views that hold a list: shrink drops half of it and marks the view truncated.
type shrinker interface{ shrink() bool }
