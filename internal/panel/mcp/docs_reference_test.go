//go:build !js

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every tool of the registry is in the reference of every language it is written in: a read or a probe by its name, a change by its
// name without _plan or _apply (the reference lists the pair as one row). A tool added to the registry fails here until the three
// documents say what it does.
func TestEveryToolIsInTheReference(t *testing.T) {
	for _, lang := range []string{"en", "ru", "zh"} {
		path := filepath.Join("..", "..", "..", "docs", lang, "reference", "mcp.md")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		doc := string(b)
		for _, in := range Tools() {
			name := in.Name
			switch in.Kind {
			case KindPlan:
				name = ChangeOf(in.Name)
			case KindApply:
				continue // the pair is checked by its plan
			}
			if !strings.Contains(doc, "`"+name+"`") && !strings.Contains(doc, "`"+in.Name+"`") {
				t.Errorf("%s does not mention the tool %s", path, name)
			}
		}
	}
}
