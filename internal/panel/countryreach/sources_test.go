package countryreach

import (
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A source that has a Source… name and no row in Sources would get the rank of a stranger, no facts and no place in the round of
// "Check now": the table is the one place a new source is added to, and this is what makes a forgotten row fail.
func TestEverySourceNameIsInTheTable(t *testing.T) {
	fset := token.NewFileSet()
	var named []string
	for _, file := range []string{"combine.go", "sources.go"} {
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				if len(vs.Values) != len(vs.Names) {
					continue
				}
				for i, name := range vs.Names {
					lit, isLit := vs.Values[i].(*ast.BasicLit)
					if strings.HasPrefix(name.Name, "Source") && isLit && lit.Kind == token.STRING {
						id, _ := strconv.Unquote(lit.Value)
						named = append(named, id)
					}
				}
			}
		}
	}
	var listed []string
	for _, s := range Sources {
		listed = append(listed, s.ID)
	}
	slices.Sort(named)
	slices.Sort(listed)
	if !slices.Equal(named, listed) {
		t.Fatalf("Source… names %v, Sources %v", named, listed)
	}
	if len(slices.Compact(slices.Clone(listed))) != len(listed) {
		t.Fatalf("a source is listed twice: %v", listed)
	}
}

// The answer of "Check now" lists its sources in one order, which proto/mistgate/admin/v1/health.proto documents and the page reads.
func TestNowSourcesKeepTheOrderOfTheAnswer(t *testing.T) {
	var got []string
	for _, s := range NowSources() {
		got = append(got, s.ID)
	}
	want := []string{"globalping", "tspu", "cheburcheck", "cheburcheck_own", "probes", "bsbord", "lists", "klick"}
	if !slices.Equal(got, want) {
		t.Fatalf("NowSources = %v, want %v", got, want)
	}
}
