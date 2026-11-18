//go:build !js

package mcp

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var updateVocabulary = flag.Bool("update-plan-vocabulary", false, "rewrite testdata/plan_vocabulary.json")

// planVocabulary is everything a plan says to the owner by key: the plan tools, the keys of their facts, the codes their facts carry
// and the warnings (danger codes) they raise. The owner's screens word each of them in five languages: web/src/lib/plan-vocabulary.test.ts
// reads this file and fails for any that has no words. A plan that says something new fails the check below until the file is
// refreshed (go test ./internal/panel/mcp -run PlanVocabulary -update-plan-vocabulary) and the web test is satisfied.
type planVocabulary struct {
	Tools    []string `json:"tools"`
	FactKeys []string `json:"fact_keys"`
	Codes    []string `json:"codes"`
	Dangers  []string `json:"dangers"`
	// Outcomes are the codes of what an applied plan reports (doneWith), Failures the codes of why one did not apply (failure, retryLater).
	Outcomes []string `json:"outcomes"`
	Failures []string `json:"failures"`
}

// not plans: the files whose Key: fields are of reads (timeline, access reports), not of an approval fact.
var vocabularySkip = map[string]bool{
	"tools_insight.go": true, "tools_access.go": true, "tools_access_block.go": true, "tools_read.go": true, "tools_data.go": true,
	"tools_capacity.go": true, "tools_probe.go": true, "views.go": true, "views_relay.go": true, "tools_owner_reads.go": true,
	"tools_access_report.go": true, "tools_access_reasons.go": true, "tools_observability.go": true,
}

// strLit is the value of a string literal expression, or "" and false.
func strLit(e ast.Expr) (string, bool) {
	if l, ok := e.(*ast.BasicLit); ok && l.Kind == token.STRING {
		if v, err := strconv.Unquote(l.Value); err == nil {
			return v, true
		}
	}
	return "", false
}

func currentPlanVocabulary(t *testing.T) planVocabulary {
	t.Helper()
	var v planVocabulary
	for _, d := range registry {
		if d.isPlan {
			v.Tools = append(v.Tools, strings.TrimSuffix(d.name, "_plan"))
		}
	}
	dangerPat := regexp.MustCompile(`(?m)^\s*(?:const\s+)?danger[A-Za-z]*\s*=\s*"([a-z_]+)"`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range dangerPat.FindAllStringSubmatch(string(b), -1) {
			v.Dangers = append(v.Dangers, m[1])
		}
		if vocabularySkip[f] {
			continue
		}
		tree, err := parser.ParseFile(fset, f, b, 0)
		if err != nil {
			t.Fatal(err)
		}
		addCode := func(c string) {
			// "path_speed_" + direction: a prefix of a code built at run time; the web test names those
			if c != "" && !strings.HasSuffix(c, "_") {
				v.Codes = append(v.Codes, c)
			}
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.KeyValueExpr:
				// Fact{Key: "node", Code: "restart"}
				if id, ok := x.Key.(*ast.Ident); ok {
					if val, ok := strLit(x.Value); ok {
						switch id.Name {
						case "Key":
							v.FactKeys = append(v.FactKeys, val)
						case "Code":
							addCode(val)
						}
					}
				}
			case *ast.CallExpr:
				// codedFact("key", value, "code", ...), secondsFact("key", n), approvalTextFacts("key", ...), changeFact("key", ...)
				id, ok := x.Fun.(*ast.Ident)
				if ok && len(x.Args) > 0 && (id.Name == "doneWith" || id.Name == "failure" || id.Name == "retryLater") {
					if c, ok := strLit(x.Args[0]); ok {
						if id.Name == "doneWith" {
							v.Outcomes = append(v.Outcomes, c)
						} else {
							v.Failures = append(v.Failures, c)
						}
					}
				}
				if !ok || !(strings.HasSuffix(id.Name, "Fact") || strings.HasSuffix(id.Name, "Facts")) || len(x.Args) == 0 {
					return true
				}
				if key, ok := strLit(x.Args[0]); ok {
					v.FactKeys = append(v.FactKeys, key)
				}
				switch {
				case id.Name == "codedFact" && len(x.Args) >= 3:
					if c, ok := strLit(x.Args[2]); ok {
						addCode(c)
					} else if bin, ok := x.Args[2].(*ast.BinaryExpr); ok {
						if c, ok := strLit(bin.X); ok {
							addCode(c)
						}
					}
				case id.Name == "changeFact":
					addCode("change")
				}
			}
			return true
		})
	}
	for _, s := range []*[]string{&v.Tools, &v.FactKeys, &v.Codes, &v.Dangers, &v.Outcomes, &v.Failures} {
		slices.Sort(*s)
		*s = slices.Compact(*s)
	}
	return v
}

func TestPlanVocabularyIsPinned(t *testing.T) {
	got := currentPlanVocabulary(t)
	if len(got.Tools) < 50 {
		t.Fatalf("only %d plan tools found: the scan is broken", len(got.Tools))
	}
	want, _ := json.MarshalIndent(got, "", "  ")
	want = append(want, '\n')
	path := filepath.Join("testdata", "plan_vocabulary.json")
	if *updateVocabulary {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	have, err := os.ReadFile(path)
	if err != nil || strings.ReplaceAll(string(have), "\r\n", "\n") != string(want) {
		t.Fatalf("%s is out of date: a plan says something new. Refresh it with `go test ./internal/panel/mcp -run PlanVocabulary -update-plan-vocabulary` and give the new words in web/src/i18n (see web/src/lib/plan-vocabulary.test.ts)", path)
	}
}
