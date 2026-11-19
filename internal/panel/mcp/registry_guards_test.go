//go:build !js

package mcp

import (
	"regexp"
	"strings"
	"testing"
)

// The kind of a tool is read off the registry, and the registry spells it by its name: a <change>_plan carries the plan flag, a
// <change>_apply is no read and no plan, and every plan has its apply and the other way round. Everything outside this package asks
// KindOf and never looks at a suffix, so a tool that breaks this rule would be put in the wrong list there.
func TestToolKindsFollowTheNames(t *testing.T) {
	infos := Tools()
	have := map[string]ToolInfo{}
	for _, in := range infos {
		have[in.Name] = in
	}
	if len(infos) != len(have) {
		t.Fatalf("Tools lists a name twice: %d entries, %d names", len(infos), len(have))
	}
	for _, in := range infos {
		isPlanName, isApplyName := strings.HasSuffix(in.Name, planSuffix), strings.HasSuffix(in.Name, applySuffix)
		switch in.Kind {
		case KindPlan:
			if !isPlanName {
				t.Errorf("%s is a plan but is not named %s", in.Name, PlanTool("<change>"))
			}
			if a, ok := have[ApplyTool(ChangeOf(in.Name))]; !ok || a.Kind != KindApply {
				t.Errorf("%s has no apply tool", in.Name)
			}
		case KindApply:
			if !isApplyName {
				t.Errorf("%s is an apply but is not named %s", in.Name, ApplyTool("<change>"))
			}
			if p, ok := have[PlanTool(strings.TrimSuffix(in.Name, applySuffix))]; !ok || p.Kind != KindPlan {
				t.Errorf("%s has no plan tool", in.Name)
			}
		case KindRead, KindRun:
			if isPlanName || isApplyName {
				t.Errorf("%s is named like a change tool but is a %s", in.Name, in.Kind)
			}
		default:
			t.Errorf("%s has no kind", in.Name)
		}
		if got := KindOf(in.Name); got != in.Kind {
			t.Errorf("KindOf(%s) = %q, the registry says %q", in.Name, got, in.Kind)
		}
	}
	// a name the registry does not know: a plan or an apply a model made up is still one
	for name, want := range map[string]ToolKind{"made_up_plan": KindPlan, "made_up_apply": KindApply, "made_up": "", "": ""} {
		if got := KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", name, got, want)
		}
	}
}

// toolNameInText is a name that ends in _plan or _apply, as the descriptions and the hints of the tools spell the tools they point at.
var toolNameInText = regexp.MustCompile(`\b[a-z][a-z0-9]*(?:_[a-z0-9]+)*_(?:plan|apply)\b`)

// A description or a hint that sends the agent to a change tool names one that exists. (A word of the generic text, "<tool>_plan", is not a name.)
func TestDescriptionsNameChangeToolsThatExist(t *testing.T) {
	known := map[string]bool{}
	for _, in := range Tools() {
		known[in.Name] = true
	}
	check := func(where, text string) {
		for _, name := range toolNameInText.FindAllString(text, -1) {
			if !known[name] {
				t.Errorf("%s names %s, which is no tool", where, name)
			}
		}
	}
	for _, d := range registry {
		check(d.name, strings.ReplaceAll(d.desc, "<tool>", ""))
	}
	for code, hint := range decoyHints {
		check("decoy hint "+code, hint)
	}
}
