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
	"strconv"
	"strings"
	"testing"
)

var updateCaps = flag.Bool("update-caps", false, "rewrite testdata/caps.golden")

// capShape is what every capability string looks like: a name and a version. A string of this shape that names no capability
// (an HTTP version, the label of the bridge's key export) is listed in notCapabilities.
var capShape = regexp.MustCompile(`^[a-z][a-z0-9-]*/[0-9]+$`)

var notCapabilities = map[string]bool{"mistgate-bridge/1": true}

// consts are the name=value lines of agentcap.go, in the order of the file.
func consts(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "agentcap.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Imports) != 0 {
		t.Fatal("agentcap imports nothing: the agent, the panel and every edge build link it")
	}
	var out []string
	for _, d := range file.Decls {
		gen, ok := d.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s is not a string literal: the package holds the wire values and nothing computed", name.Name)
				}
				out = append(out, name.Name+"="+lit.Value)
			}
		}
	}
	return out
}

// A value is on the wire: an agent in the field lists the string it was built with. testdata/caps.golden is name=value for every
// constant; a changed or removed value fails here, and so does a new constant nobody wrote down.
func TestCapabilitiesAreTheWireValues(t *testing.T) {
	got := strings.Join(consts(t), "\n") + "\n"
	golden := filepath.Join("testdata", "caps.golden")
	if *updateCaps {
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
		t.Fatalf("agentcap.go differs from testdata/caps.golden (rerun with -update-caps for a new constant; a value never changes):\n%s\nwant:\n%s", got, want)
	}
}

// A capability string is written once, here: a literal of its shape anywhere else is a second copy the golden cannot hold.
func TestNoCapabilityLiteralOutsideThePackage(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			if filepath.ToSlash(rel) == "internal/agentcap/agentcap.go" {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				if s, err := strconv.Unquote(lit.Value); err == nil && capShape.MatchString(s) && !strings.HasPrefix(s, "http/") && !notCapabilities[s] {
					t.Errorf("%s: %q is a capability string written outside internal/agentcap", filepath.ToSlash(rel), s)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
