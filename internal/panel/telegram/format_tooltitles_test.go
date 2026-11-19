//go:build !js

package telegram

import (
	"testing"

	"github.com/mistgate/mistgate/internal/panel/mcp"
)

// The message that a plan waits for the owner names the plan by a title in the owner's language. Every plan tool of the registry has
// one in each of the three languages (the table test above checks that the three are filled), and no title is left for a tool that
// is gone, so a plan never reaches the owner as a raw tool name.
func TestToolTitlesCoverEveryPlanTool(t *testing.T) {
	plans := map[string]bool{}
	for _, in := range mcp.Tools() {
		if in.Kind == mcp.KindPlan {
			plans[mcp.ChangeOf(in.Name)] = true
		}
	}
	for change := range plans {
		if _, ok := toolTitles[change]; !ok {
			t.Errorf("the plan tool %s has no title for the Telegram message", mcp.PlanTool(change))
		}
	}
	for change := range toolTitles {
		if !plans[change] {
			t.Errorf("toolTitles[%q] is a title of no plan tool of the registry", change)
		}
	}
}
