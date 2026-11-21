//go:build !js

package health

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var updateErrorCodes = flag.Bool("update-error-codes", false, "rewrite testdata/error_codes.json")

// errorCodes finds every code a check can end with in the source of this package: the Code of a ProbeError, an ErrorCode given to a
// result, the codes classify and relayDownloadProbe return, and the skip codes (skip* constants of targets.go). Each is worded in
// the admin as "health.check.err.<code>": web/src/lib/health.test.ts reads testdata/error_codes.json and fails for any with no words
// in a language, so the update screens (which show the code of the profile that did not pass) never show a raw one.
func errorCodes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	lit := func(e ast.Expr) {
		if l, ok := e.(*ast.BasicLit); ok && l.Kind == token.STRING {
			if v, err := strconv.Unquote(l.Value); err == nil && v != "" {
				codes = append(codes, v)
			}
		}
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.KeyValueExpr:
				if id, ok := x.Key.(*ast.Ident); ok && (id.Name == "Code" || id.Name == "ErrorCode") {
					lit(x.Value)
				}
			case *ast.AssignStmt:
				for i, l := range x.Lhs {
					if sel, ok := l.(*ast.SelectorExpr); ok && sel.Sel.Name == "ErrorCode" && i < len(x.Rhs) {
						lit(x.Rhs[i])
					}
				}
			case *ast.ValueSpec:
				for i, id := range x.Names {
					if strings.HasPrefix(id.Name, "skip") && i < len(x.Values) {
						lit(x.Values[i])
					}
				}
			case *ast.FuncDecl:
				if x.Name.Name == "classify" || x.Name.Name == "relayDownloadProbe" {
					ast.Inspect(x.Body, func(m ast.Node) bool {
						if r, ok := m.(*ast.ReturnStmt); ok && len(r.Results) > 0 {
							lit(r.Results[0])
						}
						return true
					})
				}
			}
			return true
		})
	}
	slices.Sort(codes)
	return slices.Compact(codes)
}

func TestErrorCodesArePinned(t *testing.T) {
	got := errorCodes(t)
	for _, c := range []string{"timeout", "auth", "tls", "refused", "http_status", "exit_unreachable", "freeze", "client_unsupported", "node_offline"} {
		if !slices.Contains(got, c) {
			t.Fatalf("the scan did not find %q: it is broken (found %v)", c, got)
		}
	}
	want, _ := json.MarshalIndent(got, "", "  ")
	want = append(want, '\n')
	path := filepath.Join("testdata", "error_codes.json")
	if *updateErrorCodes {
		if err := os.WriteFile(path, want, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	have, err := os.ReadFile(path)
	if err != nil || strings.ReplaceAll(string(have), "\r\n", "\n") != string(want) {
		t.Fatalf("%s is out of date: a check can end with a new code. Refresh it with `go test ./internal/panel/health -run ErrorCodes -update-error-codes` and word the code as health.check.err.<code> in web/src/i18n/health.ts", path)
	}
}
