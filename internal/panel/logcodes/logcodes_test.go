// Package logcodes holds one test: the catalogue of the panel's warnings and errors. Every log.Warn and log.Error call in
// internal/panel must carry a "code" attribute, a short stable snake_case name of what went wrong ("prune_failed",
// "audit_unavailable"). The code is what panel_logs filters on and what an operator greps for; a free-text message alone cannot be.
//
// The calls that predate the rule are listed in allowlist.txt, one line per message with the number of calls. The list may only
// shrink: a new call without a code fails the test, and so does a line that no longer matches (the call got its code, so the
// line goes). To give a call its code and shrink the list:
//
//	MG_LOGCODES_SHRINK=1 go test ./internal/panel/logcodes
//
// internal/node is not covered: the node agent's warnings reach the panel through the journal (internal/logscrub.JournalAttrs
// lists "code" among its attribute keys) and are changed with the agent protocol, not here.
package logcodes

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const allowlistFile = "allowlist.txt"

var loggingMethods = map[string]bool{"Warn": true, "Error": true, "WarnContext": true, "ErrorContext": true}

// violations returns, per "file<TAB>message", how many Warn/Error calls lack a code attribute.
func violations(t *testing.T, root string) map[string]int {
	t.Helper()
	out := map[string]int{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !loggingMethods[sel.Sel.Name] || !isLogger(sel.X) || len(call.Args) == 0 {
				return true
			}
			if !hasCode(call) {
				out[rel+"\t"+message(fset, call)]++
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// isLogger: the receiver is named log, logger or l (s.log, f.log, r.s.log, w.l ...): the repository's naming of *slog.Logger.
func isLogger(x ast.Expr) bool {
	name := ""
	switch v := x.(type) {
	case *ast.Ident:
		name = v.Name
	case *ast.SelectorExpr:
		name = v.Sel.Name
	}
	switch strings.ToLower(name) {
	case "log", "logger", "l":
		return true
	}
	return false
}

func hasCode(call *ast.CallExpr) bool {
	for _, a := range call.Args {
		if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil && s == "code" {
				return true
			}
		}
	}
	return false
}

// message is the first argument as written: the literal's text, or the expression for a computed message.
func message(fset *token.FileSet, call *ast.CallExpr) string {
	arg := call.Args[0]
	if _, isCtx := call.Fun.(*ast.SelectorExpr); isCtx && strings.HasSuffix(call.Fun.(*ast.SelectorExpr).Sel.Name, "Context") && len(call.Args) > 1 {
		arg = call.Args[1]
	}
	if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
		if s, err := strconv.Unquote(lit.Value); err == nil {
			return s
		}
	}
	var b strings.Builder
	_ = printer.Fprint(&b, fset, arg)
	return "<" + b.String() + ">"
}

func readAllowlist(t *testing.T) map[string]int {
	t.Helper()
	b, err := os.ReadFile(allowlistFile)
	if err != nil {
		t.Fatalf("%s: %v", allowlistFile, err)
	}
	out := map[string]int{}
	for i, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimRight(line, "\r"); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) != 3 {
			t.Fatalf("%s:%d: want file<TAB>message<TAB>count", allowlistFile, i+1)
		}
		n, err := strconv.Atoi(parts[2])
		if err != nil || n < 1 {
			t.Fatalf("%s:%d: bad count", allowlistFile, i+1)
		}
		out[parts[0]+"\t"+parts[1]] = n
	}
	return out
}

func writeAllowlist(t *testing.T, m map[string]int) {
	t.Helper()
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Warn and Error calls in internal/panel that have no \"code\" attribute yet: file, message, number of calls.\n")
	b.WriteString("# This list only shrinks (see logcodes_test.go). Give the call a code, then run: MG_LOGCODES_SHRINK=1 go test ./internal/panel/logcodes\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s\t%d\n", k, m[k])
	}
	if err := os.WriteFile(allowlistFile, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEveryWarnAndErrorHasACode(t *testing.T) {
	found := violations(t, "..")
	if os.Getenv("MG_LOGCODES_BOOTSTRAP") == "1" { // one time, to create the list from today's calls
		writeAllowlist(t, found)
		return
	}
	allowed := readAllowlist(t)
	var added, stale []string
	for k, n := range found {
		if n > allowed[k] {
			added = append(added, strings.Replace(k, "\t", ": ", 1))
		}
	}
	for k, n := range allowed {
		if found[k] < n {
			stale = append(stale, strings.Replace(k, "\t", ": ", 1))
		}
	}
	if os.Getenv("MG_LOGCODES_SHRINK") == "1" {
		next := map[string]int{}
		for k, n := range allowed {
			if f := found[k]; f > 0 {
				next[k] = min(f, n)
			}
		}
		writeAllowlist(t, next)
		stale = nil
	}
	sort.Strings(added)
	sort.Strings(stale)
	for _, a := range added {
		t.Errorf("a Warn or Error call has no code attribute: %s (add \"code\", \"<snake_case_name>\" to it; do not extend the allowlist)", a)
	}
	for _, s := range stale {
		t.Errorf("allowlist line is out of date, its call has a code now or is gone: %s (run MG_LOGCODES_SHRINK=1 go test ./internal/panel/logcodes)", s)
	}
}

// The detector itself: a call with a code passes, one without is found, a Context variant is read, a computed message is named.
func TestDetector(t *testing.T) {
	dir := t.TempDir()
	src := `package x
import "log/slog"
func f(log *slog.Logger, s struct{ log *slog.Logger }, op string) {
	log.Warn("fine", "code", "fine_failed", "err", nil)
	log.Warn("no code", "err", nil)
	s.log.Error("access: "+op, "err", nil)
	s.log.ErrorContext(nil, "ctx message")
	s.log.Info("info needs none")
}
`
	if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got := violations(t, dir)
	want := map[string]int{"x.go\tno code": 1, "x.go\t<\"access: \" + op>": 1, "x.go\tctx message": 1}
	if len(got) != len(want) {
		t.Fatalf("found %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("found %v, want %v", got, want)
		}
	}
}
