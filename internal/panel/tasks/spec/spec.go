// Package spec is what a scheduled task is (mistgate.tasks.v1): its schedule and when it runs next, the steps of a check task
// and the conditions on their results, and the limits every definition is checked against. It is plain Go with no panel
// dependency, shared by the TasksService (internal/panel/tasks) and the MCP task tools (internal/panel/mcp), so a plan an agent
// makes is checked by the same rules as a task the owner saves.
package spec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
)

// Kinds of task.
const (
	KindCheck     = "check"
	KindAssistant = "assistant"
)

// Kinds of schedule.
const (
	Every  = "every"
	Daily  = "daily"
	Weekly = "weekly"
)

// Limits of a definition.
const (
	MaxTasks          = 50
	MaxNameRunes      = 80
	MaxSteps          = 8
	MaxConditions     = 5
	MaxArgsBytes      = 4 << 10
	MaxPromptRunes    = 4000
	MaxPathRunes      = 200
	MaxValueRunes     = 200
	MinCheckEvery     = 5     // minutes
	MinAssistantEvery = 15    // minutes
	MaxEvery          = 10080 // a week, in minutes
	// MaxJitter is the longest delay a task's runs start after their time.
	MaxJitter = 3 * time.Minute
)

// Error is a refusal with a code ("schedule_too_often") and parameters, worded by the admin ("code: k=v&k=v").
type Error struct {
	Code   string
	Params [][2]string
}

func (e *Error) Error() string {
	if len(e.Params) == 0 {
		return e.Code
	}
	var b strings.Builder
	b.WriteString(e.Code + ": ")
	for i, p := range e.Params {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(p[0] + "=" + p[1])
	}
	return b.String()
}

func refuse(code string, kv ...string) error {
	e := &Error{Code: code}
	for i := 0; i+1 < len(kv); i += 2 {
		e.Params = append(e.Params, [2]string{kv[i], kv[i+1]})
	}
	return e
}

// Schedule is when a task runs.
type Schedule struct {
	Kind         string // Every, Daily or Weekly
	EveryMinutes int
	MinuteOfDay  int
	Weekday      int // 0 Sunday .. 6 Saturday
}

// MinEvery is the shortest interval a task of this kind may run at.
func MinEvery(kind string) int {
	if kind == KindAssistant {
		return MinAssistantEvery
	}
	return MinCheckEvery
}

// Validate checks the schedule for a task of the kind.
func (s Schedule) Validate(kind string) error {
	switch s.Kind {
	case Every:
		if s.EveryMinutes < MinEvery(kind) {
			return refuse("schedule_too_often", "min", strconv.Itoa(MinEvery(kind)))
		}
		if s.EveryMinutes > MaxEvery {
			return refuse("invalid_schedule")
		}
	case Daily, Weekly:
		if s.MinuteOfDay < 0 || s.MinuteOfDay >= 24*60 {
			return refuse("invalid_schedule")
		}
		if s.Kind == Weekly && (s.Weekday < 0 || s.Weekday > 6) {
			return refuse("invalid_schedule")
		}
	default:
		return refuse("invalid_schedule")
	}
	return nil
}

var weekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// String is the schedule in the words ParseSchedule reads: "every 15m", "every 2h", "daily 07:30", "weekly mon 07:30".
func (s Schedule) String() string {
	hm := fmt.Sprintf("%02d:%02d", s.MinuteOfDay/60, s.MinuteOfDay%60)
	switch s.Kind {
	case Every:
		switch {
		case s.EveryMinutes%(24*60) == 0:
			return "every " + strconv.Itoa(s.EveryMinutes/(24*60)) + "d"
		case s.EveryMinutes%60 == 0:
			return "every " + strconv.Itoa(s.EveryMinutes/60) + "h"
		}
		return "every " + strconv.Itoa(s.EveryMinutes) + "m"
	case Daily:
		return "daily " + hm
	case Weekly:
		if s.Weekday >= 0 && s.Weekday < len(weekdays) {
			return "weekly " + weekdays[s.Weekday] + " " + hm
		}
	}
	return ""
}

var everyRe = regexp.MustCompile(`^every\s+(\d{1,5})\s*(m|min|minutes?|h|hours?|d|days?)$`)

// ParseSchedule reads "every 15m" (m, h or d), "daily 07:30" and "weekly mon 07:30" (sun..sat), ignoring case. It checks the
// form, not the minimum interval of a kind (Validate does).
func ParseSchedule(text string) (Schedule, error) {
	t := strings.ToLower(strings.Join(strings.Fields(text), " "))
	if m := everyRe.FindStringSubmatch(t); m != nil {
		n, _ := strconv.Atoi(m[1])
		switch m[2][0] {
		case 'h':
			n *= 60
		case 'd':
			n *= 24 * 60
		}
		if n <= 0 || n > MaxEvery {
			return Schedule{}, refuse("invalid_schedule")
		}
		return Schedule{Kind: Every, EveryMinutes: n}, nil
	}
	parts := strings.Fields(t)
	switch {
	case len(parts) == 2 && parts[0] == Daily:
		m, ok := parseHM(parts[1])
		if !ok {
			return Schedule{}, refuse("invalid_schedule")
		}
		return Schedule{Kind: Daily, MinuteOfDay: m}, nil
	case len(parts) == 3 && parts[0] == Weekly:
		day := -1
		for i, w := range weekdays {
			if strings.HasPrefix(parts[1], w) {
				day = i
			}
		}
		m, ok := parseHM(parts[2])
		if day < 0 || !ok {
			return Schedule{}, refuse("invalid_schedule")
		}
		return Schedule{Kind: Weekly, Weekday: day, MinuteOfDay: m}, nil
	}
	return Schedule{}, refuse("invalid_schedule")
}

func parseHM(s string) (int, bool) {
	h, m, ok := strings.Cut(s, ":")
	if !ok || len(m) != 2 || len(h) == 0 || len(h) > 2 {
		return 0, false
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
		return 0, false
	}
	return hh*60 + mm, true
}

// Next is the first time the schedule fires strictly after `after`, plus the task's jitter, in a zone of offsetMin minutes
// east of UTC. EVERY counts from midnight of that zone, so "every 15m" is :00, :15, :30 and :45 and "every 2h" is the even
// hours. A time that already passed is never returned: a task the panel missed runs once, at the next time from now.
func (s Schedule) Next(after time.Time, offsetMin int, jitter time.Duration) time.Time {
	zone := time.FixedZone("panel", offsetMin*60)
	// the slot is chosen without the jitter, so a run that ended inside its jitter window does not fire again for the same slot
	base := after.Add(-jitter).In(zone)
	var slot time.Time
	switch s.Kind {
	case Every:
		period := time.Duration(max(s.EveryMinutes, 1)) * time.Minute
		midnight := time.Date(base.Year(), base.Month(), base.Day(), 0, 0, 0, 0, zone)
		// periods that do not divide a day run on from the epoch's midnight in the zone instead, so they never bunch at midnight
		origin := midnight
		if (24*time.Hour)%period != 0 {
			origin = time.Date(1970, 1, 1, 0, 0, 0, 0, zone)
		}
		n := base.Sub(origin)/period + 1
		slot = origin.Add(n * period)
	case Daily, Weekly:
		slot = time.Date(base.Year(), base.Month(), base.Day(), s.MinuteOfDay/60, s.MinuteOfDay%60, 0, 0, zone)
		for !slot.After(base) || s.Kind == Weekly && int(slot.Weekday()) != s.Weekday {
			slot = slot.AddDate(0, 0, 1)
		}
	default:
		return time.Time{}
	}
	return slot.Add(jitter).UTC()
}

// Jitter is the delay of a task's runs after their time: from its id, so it stays the same, and at most a tenth of an EVERY
// interval (a task every 5 minutes starts at most 30 s late).
func Jitter(id string, s Schedule) time.Duration {
	limit := MaxJitter
	if s.Kind == Every {
		limit = min(limit, time.Duration(s.EveryMinutes)*time.Minute/10)
	}
	if limit <= 0 {
		return 0
	}
	var h uint32 = 2166136261
	for i := 0; i < len(id); i++ {
		h = (h ^ uint32(id[i])) * 16777619
	}
	return time.Duration(h%uint32(limit/time.Second)) * time.Second
}

// Name checks a task's name.
func Name(name string) error {
	n := utf8.RuneCountInString(strings.TrimSpace(name))
	if n == 0 || n > MaxNameRunes || strings.ContainsAny(name, "\x00\r\n") {
		return refuse("invalid_name")
	}
	return nil
}

// Prompt checks an assistant task's prompt.
func Prompt(prompt string) error {
	n := utf8.RuneCountInString(strings.TrimSpace(prompt))
	if n == 0 {
		return refuse("empty_prompt")
	}
	if n > MaxPromptRunes || strings.ContainsRune(prompt, 0) {
		return refuse("prompt_too_long", "max", strconv.Itoa(MaxPromptRunes))
	}
	return nil
}

// Step is one step of a check task.
type Step struct {
	Tool           string          `json:"tool"`
	Args           json.RawMessage `json:"args,omitempty"`
	Conditions     []Condition     `json:"conditions,omitempty"`
	ErrorIsProblem bool            `json:"error_is_problem,omitempty"`
}

// Steps checks the steps of a check task: allowed reports whether a tool may run in one (the operator profile's read and run
// tools), schemaOf gives a tool's input schema (nil: the arguments are only checked to be an object). The steps come back
// with their arguments normalised (an empty one is {}).
func Steps(steps []Step, allowed func(string) bool, schemaOf func(string) json.RawMessage) ([]Step, error) {
	if len(steps) == 0 || len(steps) > MaxSteps {
		return nil, refuse("too_many_steps", "max", strconv.Itoa(MaxSteps))
	}
	out := make([]Step, len(steps))
	for i, st := range steps {
		st.Tool = strings.TrimSpace(st.Tool)
		if st.Tool == "" || len(st.Tool) > 64 {
			return nil, refuse("invalid_step", "step", strconv.Itoa(i+1))
		}
		if !allowed(st.Tool) {
			return nil, refuse("tool_not_allowed", "tool", st.Tool)
		}
		args := bytes.TrimSpace(st.Args)
		if len(args) == 0 || string(args) == "null" {
			args = []byte("{}")
		}
		if len(args) > MaxArgsBytes {
			return nil, refuse("invalid_args", "tool", st.Tool)
		}
		norm, err := ValidateArgs(schemaOf(st.Tool), args)
		if err != nil {
			return nil, refuse("invalid_args", "tool", st.Tool)
		}
		if len(st.Conditions) > MaxConditions {
			return nil, refuse("invalid_condition", "step", strconv.Itoa(i+1))
		}
		conds := make([]Condition, 0, len(st.Conditions))
		for _, c := range st.Conditions {
			c.Path, c.Op = strings.TrimSpace(c.Path), strings.TrimSpace(c.Op)
			if err := c.Validate(); err != nil {
				return nil, refuse("invalid_condition", "step", strconv.Itoa(i+1))
			}
			conds = append(conds, c)
		}
		out[i] = Step{Tool: st.Tool, Args: norm, Conditions: conds, ErrorIsProblem: st.ErrorIsProblem}
	}
	return out, nil
}

// ValidateArgs checks a step's arguments: one JSON object, valid against the tool's schema when one is given. It returns them
// compacted.
func ValidateArgs(schemaRaw, args json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, errors.New("arguments must be a JSON object")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("arguments must be one JSON object")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, args); err != nil {
		return nil, errors.New("arguments are not valid JSON")
	}
	if len(schemaRaw) > 0 {
		var schema jsonschema.Schema
		if err := json.Unmarshal(schemaRaw, &schema); err != nil {
			return nil, errors.New("tool schema is invalid")
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return nil, errors.New("tool schema is invalid")
		}
		// plain float64 numbers, as the validator expects
		var instance any
		if err := json.Unmarshal(compact.Bytes(), &instance); err != nil {
			return nil, errors.New("arguments are not valid JSON")
		}
		if err := resolved.Validate(instance); err != nil {
			return nil, errors.New("arguments do not match the tool's schema")
		}
	}
	return json.RawMessage(compact.Bytes()), nil
}
