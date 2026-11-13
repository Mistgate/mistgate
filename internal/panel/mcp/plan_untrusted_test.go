//go:build !js

package mcp

import (
	"strings"
	"testing"
)

// A plan's Summary never embeds untrusted text (that goes in Facts), and a fact that carries a node name is marked untrusted.
func TestPlanTextKeepsNodeNameOutOfSummaryAndMarksFact(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileAdmin)
	s := e.session(secret)

	osPlan := planOf(t, s, "os_rollout_start", map[string]any{"node_ids": []any{nodeA}, "mode": "security", "reboot": "if_required"})
	if strings.Contains(osPlan.Summary, "de1") {
		t.Errorf("os_rollout_start summary embeds the node name: %q", osPlan.Summary)
	}

	dbg := planOf(t, s, "node_debug_capture", map[string]any{"node": nodeA})
	if strings.Contains(dbg.Summary, "de1") {
		t.Errorf("node_debug_capture summary embeds the node name: %q", dbg.Summary)
	}
	seen := false
	for _, f := range dbg.Facts {
		if f.Key == "node" {
			seen = true
			if !f.Untrusted {
				t.Errorf("node_debug_capture: the node name fact is not marked untrusted: %+v", f)
			}
		}
	}
	if !seen {
		t.Error("node_debug_capture: no node fact")
	}
}
