package spec

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Condition operators.
const (
	OpSeverityAtLeast = "severity_at_least"
	OpEq              = "eq"
	OpNe              = "ne"
	OpIn              = "in"
	OpNotIn           = "not_in"
	OpGt              = "gt"
	OpGte             = "gte"
	OpLt              = "lt"
	OpLte             = "lte"
	OpNonempty        = "nonempty"
	OpContains        = "contains"
)

// Ops lists the operators in the order the editor offers them.
var Ops = []string{OpSeverityAtLeast, OpGt, OpGte, OpLt, OpLte, OpEq, OpNe, OpIn, OpNotIn, OpNonempty, OpContains}

// Condition is a test on a step's result: it holds when any value at Path matches Op and Value.
//
// Path is dotted names into the result; "name[]" walks every element of the list name (a bare "[]" the list at hand); "**."
// before a name finds that name at any depth. "totals.nodes_problem_now", "nodes[].status", "**.severity". An empty path is the
// whole result.
type Condition struct {
	Path  string `json:"path,omitempty"`
	Op    string `json:"op"`
	Value string `json:"value,omitempty"`
}

var pathSegment = func() func(string) bool {
	ok := func(r rune) bool {
		return r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
	}
	return func(seg string) bool {
		if seg == "**" || seg == "[]" {
			return true
		}
		seg = strings.TrimSuffix(seg, "[]")
		if seg == "" || len(seg) > 64 {
			return false
		}
		for _, r := range seg {
			if !ok(r) {
				return false
			}
		}
		return true
	}
}()

// Validate checks the condition's form.
func (c Condition) Validate() error {
	if utf8.RuneCountInString(c.Path) > MaxPathRunes || utf8.RuneCountInString(c.Value) > MaxValueRunes {
		return errors.New("condition too long")
	}
	if c.Path != "" {
		segs := strings.Split(c.Path, ".")
		for i, seg := range segs {
			if !pathSegment(seg) {
				return errors.New("bad path")
			}
			if seg == "**" && (i == len(segs)-1 || segs[i+1] == "**") {
				return errors.New("** must be followed by a name")
			}
		}
	}
	switch c.Op {
	case OpSeverityAtLeast:
		if severityRank(c.Value) == 0 {
			return errors.New("unknown severity")
		}
	case OpGt, OpGte, OpLt, OpLte:
		if _, err := strconv.ParseFloat(strings.TrimSpace(c.Value), 64); err != nil {
			return errors.New("value must be a number")
		}
	case OpEq, OpNe, OpContains:
		if c.Op == OpContains && strings.TrimSpace(c.Value) == "" {
			return errors.New("contains needs a value")
		}
	case OpIn, OpNotIn:
		if len(listOf(c.Value)) == 0 {
			return errors.New("in needs a list")
		}
	case OpNonempty:
	default:
		return errors.New("unknown operator")
	}
	return nil
}

// severityRank orders the severity words of the tools' results: info, warning (warn), error (critical, fail).
func severityRank(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info", "notice":
		return 1
	case "warning", "warn":
		return 2
	case "error", "critical", "fail", "failed":
		return 3
	}
	return 0
}

func listOf(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

type found struct {
	path  string
	value any
}

const conditionVisitLimit = 10000

// values collects what the path names in doc, with the concrete path of each ("nodes[2].status").
func values(doc any, path string) []found {
	out, _ := valuesWithBudget(doc, path, conditionVisitLimit)
	return out
}

func valuesWithBudget(doc any, path string, limit int) ([]found, int) {
	if limit <= 0 {
		return nil, 0
	}
	if path == "" {
		return []found{{path: "", value: doc}}, 1
	}
	var out []found
	visited := 0
	exhausted := limit <= 0
	var walk func(v any, at string, segs []string)
	walk = func(v any, at string, segs []string) {
		if exhausted || len(out) >= 1000 {
			return
		}
		if visited >= limit {
			exhausted = true
			return
		}
		visited++
		if len(segs) == 0 {
			out = append(out, found{path: at, value: v})
			return
		}
		seg := segs[0]
		switch {
		case seg == "**":
			// the next name at any depth: here, or under any child
			walk(v, at, segs[1:])
			switch x := v.(type) {
			case map[string]any:
				for _, k := range sortedKeys(x) {
					if exhausted || len(out) >= 1000 {
						break
					}
					walk(x[k], join(at, k), segs)
				}
			case []any:
				for i, e := range x {
					if exhausted || len(out) >= 1000 {
						break
					}
					walk(e, at+"["+strconv.Itoa(i)+"]", segs)
				}
			}
		case seg == "[]":
			if list, ok := v.([]any); ok {
				for i, e := range list {
					if exhausted || len(out) >= 1000 {
						break
					}
					walk(e, at+"["+strconv.Itoa(i)+"]", segs[1:])
				}
			}
		default:
			name, each := strings.CutSuffix(seg, "[]")
			obj, ok := v.(map[string]any)
			if !ok {
				return
			}
			child, ok := obj[name]
			if !ok {
				return
			}
			if each {
				list, ok := child.([]any)
				if !ok {
					return
				}
				for i, e := range list {
					if exhausted || len(out) >= 1000 {
						break
					}
					walk(e, join(at, name)+"["+strconv.Itoa(i)+"]", segs[1:])
				}
				return
			}
			walk(child, join(at, name), segs[1:])
		}
	}
	walk(doc, "", strings.Split(path, "."))
	return out, visited
}

func join(at, name string) string {
	if at == "" {
		return name
	}
	return at + "." + name
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// text is a value as a condition compares it: a string as it is, a number in its shortest form, true or false.
func text(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil && !math.IsNaN(f)
	}
	return 0, false
}

func nonempty(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(x) != ""
	case bool:
		return x
	case float64:
		return x != 0
	case json.Number:
		f, _ := x.Float64()
		return f != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return false
}

// holds reports whether one value matches the condition.
func (c Condition) holds(v any) bool {
	switch c.Op {
	case OpSeverityAtLeast:
		s, ok := v.(string)
		return ok && severityRank(s) > 0 && severityRank(s) >= severityRank(c.Value)
	case OpGt, OpGte, OpLt, OpLte:
		n, ok := number(v)
		want, err := strconv.ParseFloat(strings.TrimSpace(c.Value), 64)
		if !ok || err != nil {
			return false
		}
		switch c.Op {
		case OpGt:
			return n > want
		case OpGte:
			return n >= want
		case OpLt:
			return n < want
		}
		return n <= want
	case OpEq, OpNe:
		s, ok := text(v)
		if !ok {
			return false
		}
		eq := strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(c.Value))
		return eq == (c.Op == OpEq)
	case OpIn, OpNotIn:
		s, ok := text(v)
		if !ok {
			return false
		}
		in := false
		for _, w := range listOf(c.Value) {
			in = in || strings.EqualFold(strings.TrimSpace(s), w)
		}
		return in == (c.Op == OpIn)
	case OpNonempty:
		return nonempty(v)
	case OpContains:
		s, ok := text(v)
		return ok && strings.Contains(strings.ToLower(s), strings.ToLower(strings.TrimSpace(c.Value)))
	}
	return false
}

// Match evaluates the condition on a tool's result (JSON) and returns what matched, as "<path> = <value>" lines (at most limit).
// Invalid JSON is an error so a caller cannot mistake a failed tool result for a passing check.
func (c Condition) Match(result []byte, limit int) ([]string, error) {
	var doc any
	if err := json.Unmarshal(result, &doc); err != nil {
		return nil, errors.New("tool result is not valid JSON")
	}
	var out []string
	for _, f := range values(doc, c.Path) {
		if !c.holds(f.value) {
			continue
		}
		out = append(out, describe(f))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func describe(f found) string {
	path := f.path
	if path == "" {
		path = "result"
	}
	var v string
	switch x := f.value.(type) {
	case []any:
		v = strconv.Itoa(len(x)) + " items"
	case map[string]any:
		v = strconv.Itoa(len(x)) + " fields"
	default:
		v, _ = text(x)
	}
	if utf8.RuneCountInString(v) > 80 {
		v = string([]rune(v)[:80]) + "…"
	}
	return path + " = " + v
}
