package agentcap

import (
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var updateWireValues = flag.Bool("update-wire-values", false, "rewrite testdata/wire_values.golden")

// capShape is what every capability string looks like: a name and a version. A string of this shape that names no capability
// (an HTTP version, the label of the bridge's key export) is listed in notCapabilities.
var capShape = regexp.MustCompile(`^[a-z][a-z0-9-]*/[0-9]+$`)

var notCapabilities = map[string]bool{"mistgate-bridge/1": true}

// capabilityLiterals are the distinct strings of the shape of a capability in the Go files of the program (tests and generated
// code left out), by their place in the tree.
func capabilityLiterals(t *testing.T) map[string][]string {
	t.Helper()
	root := filepath.Join("..", "..")
	found := map[string][]string{}
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil && capShape.MatchString(s) && !strings.HasPrefix(s, "http/") && !notCapabilities[s] {
					rel, _ := filepath.Rel(root, path)
					found[s] = append(found[s], filepath.ToSlash(rel))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return found
}

// The set of capability strings in the program is a wire value: an agent in the field lists the string it was built with.
func TestCapabilityStringsAreTheWireValues(t *testing.T) {
	found := capabilityLiterals(t)
	values := make([]string, 0, len(found))
	for v := range found {
		values = append(values, v)
	}
	slices.Sort(values)
	got := strings.Join(values, "\n") + "\n"
	golden := filepath.Join("testdata", "wire_values.golden")
	if *updateWireValues {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("the capability strings of the program differ from testdata/wire_values.golden:\n%s\nwant:\n%s", got, want)
	}
}
