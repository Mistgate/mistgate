package spec

import (
	"encoding/json"
	"strings"
)

// Definition is what the owner writes for a task.
type Definition struct {
	Name           string
	Kind           string // KindCheck or KindAssistant
	Enabled        bool
	Schedule       Schedule
	Steps          []Step // check
	Prompt         string // assistant
	NotifyTelegram bool
}

// Tools is what a check's steps are checked against: whether a tool may be a step, and its input schema. A nil Allowed means
// the list could not be read (tools_unavailable).
type Tools struct {
	Allowed  func(string) bool
	SchemaOf func(string) json.RawMessage
}

// Check checks a definition and returns it normalised (trimmed name and prompt, compact arguments): the name, the kind, the
// schedule and its minimum for the kind, a check's steps against the tools, an assistant task's prompt, and no secret in a
// name, an argument, a condition or a prompt (secret reports one: the MCP scrubber would change the text).
func Check(d Definition, tools Tools, secret func(string) bool) (Definition, error) {
	d.Name = strings.TrimSpace(d.Name)
	if err := Name(d.Name); err != nil {
		return d, err
	}
	if secret(d.Name) {
		return d, refuse("secret_in_task")
	}
	if d.Kind != KindCheck && d.Kind != KindAssistant {
		return d, refuse("invalid_kind")
	}
	if err := d.Schedule.Validate(d.Kind); err != nil {
		return d, err
	}
	switch d.Kind {
	case KindCheck:
		if strings.TrimSpace(d.Prompt) != "" {
			return d, refuse("invalid_step")
		}
		d.Prompt = ""
		for _, st := range d.Steps {
			if secret(string(st.Args)) {
				return d, refuse("secret_in_task")
			}
			for _, c := range st.Conditions {
				if secret(c.Value) || secret(c.Path) {
					return d, refuse("secret_in_task")
				}
			}
		}
		if len(d.Steps) == 0 || len(d.Steps) > MaxSteps {
			return d, refuse("too_many_steps", "max", "8")
		}
		if tools.Allowed == nil || tools.SchemaOf == nil {
			return d, refuse("tools_unavailable")
		}
		steps, err := Steps(d.Steps, tools.Allowed, tools.SchemaOf)
		if err != nil {
			return d, err
		}
		d.Steps = steps
	case KindAssistant:
		if len(d.Steps) > 0 {
			return d, refuse("invalid_step")
		}
		d.Prompt = strings.TrimSpace(d.Prompt)
		if err := Prompt(d.Prompt); err != nil {
			return d, err
		}
		if secret(d.Prompt) {
			return d, refuse("secret_in_task")
		}
	}
	return d, nil
}
