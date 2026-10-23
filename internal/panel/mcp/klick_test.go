package mcp

import (
	"strings"
	"testing"
)

// klick_reports: the counts per node with the node's name, the operators and regions as the app named them (cleaned), and
// one node when asked; a readonly token is enough.
func TestKlickReportsContent(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	v := decode[KlickReportsV](t, mustOK(t, s, "klick_reports", map[string]any{}))
	if !v.Enabled || v.Reports != 6 || v.LastUnix != 1700000600 || len(v.Nodes) != 2 {
		t.Fatalf("view: %+v", v)
	}
	n := v.Nodes[0]
	if n.NodeID != nodeA || n.NodeName != "de1" || n.Reports != 5 || n.Freeze != 3 || n.OK != 2 || len(n.Operators) != 2 || len(n.Regions) != 1 {
		t.Errorf("node: %+v", n)
	}
	if n.Operators[0].Name != "MTS PJSC" || n.Operators[0].Freeze != 3 || n.Regions[0].Name != "RU · Moscow" {
		t.Errorf("groups: %+v %+v", n.Operators, n.Regions)
	}
	if strings.Contains(n.Operators[1].Name, canaryTK) {
		t.Errorf("an operator name carried a token through: %q", n.Operators[1].Name)
	}
	if v.Nodes[1].NodeName != "" { // a node the panel no longer has keeps its id only
		t.Errorf("unknown node named %q", v.Nodes[1].NodeName)
	}

	one := decode[KlickReportsV](t, mustOK(t, s, "klick_reports", map[string]any{"node": "de1"}))
	if len(one.Nodes) != 1 || one.Nodes[0].NodeID != nodeA {
		t.Errorf("node filter: %+v", one.Nodes)
	}
}
