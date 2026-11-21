package decoy

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// A small template interpreter for the generator's templates. It replaces text/template, which looks methods up by name and so
// makes the linker keep every exported method of every type in the program (7.9 MB of every edge build). The syntax is the
// subset of text/template that the templates use, and it is the same on purpose:
//
//	text                        copied as it is
//	{{/* note */}}              a comment, dropped
//	{{- ... -}}                 a trim marker takes the white space on its side
//	{{expr}}                    a value, printed as fmt.Sprint prints it; nil is an error
//	{{if expr}} {{else if expr}} {{else}} {{end}}
//	{{range expr}} {{range $v := expr}} {{range $i, $v := expr}} {{else}} {{end}}   over a []any
//	{{with expr}} {{else}} {{end}}
//	{{$x := expr}} {{$x = expr}}   a variable; one set inside a block is gone after it, an assignment reaches outward
//	{{define "name"}} {{end}}   a named template, at the top level of a file only
//	{{template "name" expr}}    the named template with expr as dot (and as $)
//
// An expression is a term, or a function and its terms: terms are ".", ".a.b", "$", "$x.a.b", "\"text\"", 12, true, false, nil
// and "(expr)" optionally followed by ".a.b". Dot is a map[string]any and fields are its keys; a field that is not there is an
// error, not an empty string. The functions are eq ne lt gt le ge not index len printf js and those of funcMap. There are no
// pipes. Data are map[string]any, []any, string, int, float64 and bool.

type tnode any

type (
	textNode  string
	printNode struct {
		e    expr
		line int
	}
	declNode struct {
		name   string
		assign bool
		e      expr
		line   int
	}
	ifNode struct {
		cond      expr
		then, els []tnode
		line      int
	}
	rangeNode struct {
		idx, val  string
		e         expr
		body, els []tnode
		line      int
	}
	withNode struct {
		e         expr
		body, els []tnode
		line      int
	}
	callNode struct {
		name string
		arg  expr // nil: dot is nil
		line int
	}
)

type expr any

type (
	dotExpr struct{}
	varExpr struct {
		name   string
		fields []string
	}
	fieldExpr struct{ fields []string }
	litExpr   struct{ v any }
	callExpr  struct {
		fn   string
		args []expr
		line int
	}
	chainExpr struct {
		x      expr
		fields []string
	}
)

// tmplFunc is a function templates can call; it fails by panicking with a tmplError.
type tmplFunc func(args []any) any

type tmplError struct{ msg string }

func fail(format string, args ...any) { panic(tmplError{fmt.Sprintf(format, args...)}) }

func recoverTmpl(err *error) {
	if r := recover(); r != nil {
		te, ok := r.(tmplError)
		if !ok {
			panic(r)
		}
		*err = fmt.Errorf("%s", te.msg)
	}
}

var builtinFuncs = map[string]bool{"eq": true, "ne": true, "lt": true, "gt": true, "le": true, "ge": true, "not": true, "index": true, "len": true, "printf": true, "js": true}

// tmplSet is the parsed templates by name.
type tmplSet struct {
	t     map[string][]tnode
	known map[string]bool // the functions a template may call
}

func newTmplSet(funcs map[string]tmplFunc) *tmplSet {
	s := &tmplSet{t: map[string][]tnode{}, known: map[string]bool{}}
	for k := range builtinFuncs {
		s.known[k] = true
	}
	for k := range funcs {
		s.known[k] = true
	}
	return s
}

func (s *tmplSet) has(name string) bool { _, ok := s.t[name]; return ok }

// add parses text as the template called name, and every {{define}} in it as a template of its own. A name that is already
// there is replaced.
func (s *tmplSet) add(name, text string) (err error) {
	defer recoverTmpl(&err)
	p := &parser{items: lexItems(name, text), set: s, file: name}
	nodes, term := p.list(true)
	if term != nil {
		p.failf(p.items[p.i-1], "unexpected {{%s}}", term[0].s)
	}
	s.t[name] = nodes
	return nil
}

type item struct {
	text   string // text, or the inside of an action
	action bool
	line   int
}

func lexItems(file, src string) []item {
	var out []item
	line := 1
	emit := func(text string) {
		if text != "" {
			out = append(out, item{text: text, line: line})
			line += strings.Count(text, "\n")
		}
	}
	for len(src) > 0 {
		i := strings.Index(src, "{{")
		if i < 0 {
			emit(src)
			break
		}
		text := src[:i]
		j, inStr := i+2, byte(0)
		for ; j < len(src); j++ {
			c := src[j]
			if inStr != 0 {
				if c == '\\' && inStr == '"' {
					j++
				} else if c == inStr {
					inStr = 0
				}
				continue
			}
			if c == '"' || c == '`' {
				inStr = c
			} else if c == '}' && j+1 < len(src) && src[j+1] == '}' {
				break
			}
		}
		if j >= len(src) {
			fail("%s:%d: unclosed action", file, line+strings.Count(text, "\n"))
		}
		act := src[i+2 : j]
		src = src[j+2:]
		if strings.HasPrefix(act, "- ") || act == "-" {
			text = strings.TrimRight(text, " \t\r\n")
			act = act[1:]
		}
		trimAfter := strings.HasSuffix(act, " -") || act == "-"
		if trimAfter {
			act = act[:len(act)-1]
		}
		emit(text)
		if a := strings.TrimSpace(act); strings.HasPrefix(a, "/*") && strings.HasSuffix(a, "*/") {
			// a comment
		} else {
			out = append(out, item{text: act, action: true, line: line})
		}
		line += strings.Count(act, "\n")
		if trimAfter {
			rest := strings.TrimLeft(src, " \t\r\n")
			line += strings.Count(src[:len(src)-len(rest)], "\n")
			src = rest
		}
	}
	return out
}

type tokKind int

const (
	tIdent tokKind = iota
	tDot
	tField
	tVar
	tString
	tInt
	tLParen
	tRParen
	tComma
	tDecl
	tAssign
)

type tok struct {
	k  tokKind
	s  string // identifier, or the whole of a field or variable chain
	v  any    // value of a string or integer
	sp bool   // white space before it
}

func isIdentStart(c byte) bool { return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }
func isIdentChar(c byte) bool  { return isIdentStart(c) || c >= '0' && c <= '9' }

func lexAction(s string) []tok {
	var out []tok
	sp := false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
			sp = true
			continue
		case c == '(':
			out = append(out, tok{k: tLParen, sp: sp})
			i++
		case c == ')':
			out = append(out, tok{k: tRParen, sp: sp})
			i++
		case c == ',':
			out = append(out, tok{k: tComma, sp: sp})
			i++
		case c == ':' && i+1 < len(s) && s[i+1] == '=':
			out = append(out, tok{k: tDecl, sp: sp})
			i += 2
		case c == '=':
			out = append(out, tok{k: tAssign, sp: sp})
			i++
		case c == '"' || c == '`':
			j := i + 1
			for ; j < len(s) && s[j] != c; j++ {
				if c == '"' && s[j] == '\\' {
					j++
				}
			}
			if j >= len(s) {
				fail("unterminated string in {{%s}}", s)
			}
			v, err := strconv.Unquote(s[i : j+1])
			if err != nil {
				fail("bad string %s in {{%s}}", s[i:j+1], s)
			}
			out = append(out, tok{k: tString, v: v, sp: sp})
			i = j + 1
		case c >= '0' && c <= '9' || c == '-' && i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9':
			j := i + 1
			for ; j < len(s) && s[j] >= '0' && s[j] <= '9'; j++ {
			}
			n, err := strconv.Atoi(s[i:j])
			if err != nil {
				fail("bad number %s in {{%s}}", s[i:j], s)
			}
			out = append(out, tok{k: tInt, v: n, sp: sp})
			i = j
		case c == '.' || c == '$':
			j := i + 1
			if c == '$' || j < len(s) && isIdentStart(s[j]) {
				for ; j < len(s) && isIdentChar(s[j]); j++ {
				}
			}
			first := j
			for j+1 < len(s) && s[j] == '.' && isIdentStart(s[j+1]) {
				j++
				for ; j < len(s) && isIdentChar(s[j]); j++ {
				}
			}
			switch {
			case c == '$':
				out = append(out, tok{k: tVar, s: s[i:j], sp: sp})
			case first == i+1: // a lone dot
				out = append(out, tok{k: tDot, sp: sp})
			default:
				out = append(out, tok{k: tField, s: s[i:j], sp: sp})
			}
			i = j
		case isIdentStart(c):
			j := i + 1
			for ; j < len(s) && isIdentChar(s[j]); j++ {
			}
			out = append(out, tok{k: tIdent, s: s[i:j], sp: sp})
			i = j
		default:
			fail("{{%s}}: %q is not supported (no pipes, no comparisons but the functions)", s, c)
		}
		sp = false
	}
	return out
}

type parser struct {
	items []item
	i     int
	set   *tmplSet
	file  string
}

func (p *parser) failf(it item, format string, args ...any) {
	fail("%s:%d: %s", p.file, it.line, fmt.Sprintf(format, args...))
}

// list parses nodes up to an {{end}} or {{else}} and returns the tokens of that action; at the top level it runs to the end of
// the file, and {{define}} is allowed.
func (p *parser) list(top bool) (nodes []tnode, term []tok) {
	for p.i < len(p.items) {
		it := p.items[p.i]
		p.i++
		if !it.action {
			nodes = append(nodes, textNode(it.text))
			continue
		}
		toks := lexActionAt(p, it)
		if len(toks) == 0 {
			p.failf(it, "empty action")
		}
		if toks[0].k == tIdent {
			switch toks[0].s {
			case "end", "else":
				if top {
					p.failf(it, "unexpected {{%s}}", toks[0].s)
				}
				return nodes, toks
			case "define":
				if !top || len(toks) != 2 || toks[1].k != tString {
					p.failf(it, `{{define "name"}} belongs at the top level of a file`)
				}
				body, t := p.list(false)
				if t == nil || t[0].s != "end" {
					p.failf(it, "define is not closed by {{end}}")
				}
				p.set.t[toks[1].v.(string)] = body
				continue
			case "if":
				nodes = append(nodes, p.parseIf(it, toks[1:]))
				continue
			case "range":
				nodes = append(nodes, p.parseRange(it, toks[1:]))
				continue
			case "with":
				body, els := p.block(it)
				nodes = append(nodes, withNode{e: p.expression(it, toks[1:]), body: body, els: els, line: it.line})
				continue
			case "template":
				if len(toks) < 2 || toks[1].k != tString {
					p.failf(it, `{{template "name" expr}}`)
				}
				n := callNode{name: toks[1].v.(string), line: it.line}
				if len(toks) > 2 {
					n.arg = p.expression(it, toks[2:])
				}
				nodes = append(nodes, n)
				continue
			}
		}
		if toks[0].k == tVar && len(toks) > 2 && (toks[1].k == tDecl || toks[1].k == tAssign) {
			nodes = append(nodes, declNode{name: toks[0].s, assign: toks[1].k == tAssign, e: p.expression(it, toks[2:]), line: it.line})
			continue
		}
		nodes = append(nodes, printNode{e: p.expression(it, toks), line: it.line})
	}
	if !top {
		p.failf(p.items[len(p.items)-1], "missing {{end}}")
	}
	return nodes, nil
}

func lexActionAt(p *parser, it item) []tok {
	var toks []tok
	func() {
		defer func() {
			if r := recover(); r != nil {
				if te, ok := r.(tmplError); ok {
					p.failf(it, "%s", te.msg)
				}
				panic(r)
			}
		}()
		toks = lexAction(it.text)
	}()
	return toks
}

// block parses the body of an if, range or with and its optional else branch up to the shared {{end}}.
func (p *parser) block(it item) (body, els []tnode) {
	body, term := p.list(false)
	if term[0].s == "else" {
		if len(term) > 1 {
			p.failf(p.items[p.i-1], "{{else}} takes nothing here")
		}
		var t2 []tok
		els, t2 = p.list(false)
		if t2[0].s != "end" {
			p.failf(p.items[p.i-1], "a second {{else}}")
		}
	}
	return body, els
}

func (p *parser) parseIf(it item, cond []tok) tnode {
	n := ifNode{cond: p.expression(it, cond), line: it.line}
	body, term := p.list(false)
	n.then = body
	if term[0].s == "else" {
		if len(term) > 1 {
			if term[1].k != tIdent || term[1].s != "if" {
				p.failf(p.items[p.i-1], "{{else}} takes only another if")
			}
			n.els = []tnode{p.parseIf(p.items[p.i-1], term[2:])} // the chain shares one {{end}}
			return n
		}
		var t2 []tok
		n.els, t2 = p.list(false)
		if t2[0].s != "end" {
			p.failf(p.items[p.i-1], "a second {{else}}")
		}
	}
	return n
}

func (p *parser) parseRange(it item, toks []tok) tnode {
	n := rangeNode{line: it.line}
	for k := 0; k < len(toks); k++ {
		if toks[k].k == tDecl {
			vars := toks[:k]
			switch {
			case len(vars) == 1 && vars[0].k == tVar:
				n.val = vars[0].s
			case len(vars) == 3 && vars[0].k == tVar && vars[1].k == tComma && vars[2].k == tVar:
				n.idx, n.val = vars[0].s, vars[2].s
			default:
				p.failf(it, "range takes $v := or $i, $v :=")
			}
			toks = toks[k+1:]
			break
		}
	}
	n.e = p.expression(it, toks)
	n.body, n.els = p.block(it)
	return n
}

// expression parses the tokens of one command.
func (p *parser) expression(it item, toks []tok) expr {
	if len(toks) == 0 {
		p.failf(it, "an expression is missing")
	}
	ep := &exprParser{p: p, it: it, toks: toks}
	e := ep.command()
	if ep.pos != len(toks) {
		p.failf(it, "unexpected %s", ep.describe(toks[ep.pos]))
	}
	return e
}

type exprParser struct {
	p    *parser
	it   item
	toks []tok
	pos  int
}

func (ep *exprParser) describe(t tok) string {
	switch t.k {
	case tRParen:
		return `")"`
	case tComma:
		return `","`
	case tDecl:
		return `":="`
	case tAssign:
		return `"="`
	}
	return fmt.Sprintf("%q", t.s)
}

func (ep *exprParser) done() bool { return ep.pos >= len(ep.toks) || ep.toks[ep.pos].k == tRParen }

func (ep *exprParser) command() expr {
	first := ep.term()
	call, ok := first.(callExpr)
	if !ok {
		return first
	}
	for !ep.done() {
		call.args = append(call.args, ep.term())
	}
	return call
}

func (ep *exprParser) term() expr {
	if ep.pos >= len(ep.toks) {
		ep.p.failf(ep.it, "an expression is missing")
	}
	t := ep.toks[ep.pos]
	ep.pos++
	switch t.k {
	case tString, tInt:
		return litExpr{t.v}
	case tDot:
		return dotExpr{}
	case tField:
		return fieldExpr{strings.Split(t.s[1:], ".")}
	case tVar:
		parts := strings.Split(t.s, ".")
		return varExpr{name: parts[0], fields: parts[1:]}
	case tIdent:
		switch t.s {
		case "true":
			return litExpr{true}
		case "false":
			return litExpr{false}
		case "nil":
			return litExpr{nil}
		}
		if !ep.p.set.known[t.s] {
			ep.p.failf(ep.it, "function %q is not defined", t.s)
		}
		return callExpr{fn: t.s, line: ep.it.line}
	case tLParen:
		inner := ep.command()
		if ep.pos >= len(ep.toks) || ep.toks[ep.pos].k != tRParen {
			ep.p.failf(ep.it, "a ) is missing")
		}
		ep.pos++
		if ep.pos < len(ep.toks) && ep.toks[ep.pos].k == tField && !ep.toks[ep.pos].sp {
			f := ep.toks[ep.pos]
			ep.pos++
			return chainExpr{x: inner, fields: strings.Split(f.s[1:], ".")}
		}
		return inner
	}
	ep.p.failf(ep.it, "unexpected %s", ep.describe(t))
	return nil
}

// exec runs the named template with data as dot, appending to w.
func (s *tmplSet) exec(w *bytes.Buffer, name string, data any, fns map[string]tmplFunc) (err error) {
	defer recoverTmpl(&err)
	st := &state{set: s, fns: fns, w: w}
	st.call(name, data, 0)
	return nil
}

type varSlot struct {
	name string
	v    any
}

type state struct {
	set  *tmplSet
	fns  map[string]tmplFunc
	w    *bytes.Buffer
	vars []varSlot
	name string
}

func (st *state) call(name string, dot any, line int) {
	nodes, ok := st.set.t[name]
	if !ok {
		fail("template %q is not defined (line %d of %s)", name, line, st.name)
	}
	sub := &state{set: st.set, fns: st.fns, w: st.w, vars: []varSlot{{"$", dot}}, name: name}
	sub.walk(nodes, dot)
}

func (st *state) failf(line int, format string, args ...any) {
	fail("%s:%d: %s", st.name, line, fmt.Sprintf(format, args...))
}

func (st *state) walk(nodes []tnode, dot any) {
	for _, n := range nodes {
		switch n := n.(type) {
		case textNode:
			st.w.WriteString(string(n))
		case printNode:
			st.w.WriteString(st.print(st.eval(n.e, dot, n.line), n.line))
		case declNode:
			v := st.eval(n.e, dot, n.line)
			if !n.assign {
				st.vars = append(st.vars, varSlot{n.name, v})
				break
			}
			found := false
			for i := len(st.vars) - 1; i >= 0; i-- {
				if st.vars[i].name == n.name {
					st.vars[i].v, found = v, true
					break
				}
			}
			if !found {
				st.failf(n.line, "variable %s is not declared", n.name)
			}
		case ifNode:
			mark := len(st.vars)
			if truth(st.eval(n.cond, dot, n.line)) {
				st.walk(n.then, dot)
			} else {
				st.walk(n.els, dot)
			}
			st.vars = st.vars[:mark]
		case withNode:
			mark := len(st.vars)
			if v := st.eval(n.e, dot, n.line); truth(v) {
				st.walk(n.body, v)
			} else {
				st.walk(n.els, dot)
			}
			st.vars = st.vars[:mark]
		case rangeNode:
			list, ok := st.eval(n.e, dot, n.line).([]any)
			if !ok {
				st.failf(n.line, "range wants a list")
			}
			mark := len(st.vars)
			if len(list) == 0 {
				st.walk(n.els, dot)
			}
			for i, e := range list {
				st.vars = st.vars[:mark]
				if n.idx != "" {
					st.vars = append(st.vars, varSlot{n.idx, i})
				}
				if n.val != "" {
					st.vars = append(st.vars, varSlot{n.val, e})
				}
				st.walk(n.body, e)
			}
			st.vars = st.vars[:mark]
		case callNode:
			var arg any
			if n.arg != nil {
				arg = st.eval(n.arg, dot, n.line)
			}
			st.call(n.name, arg, n.line)
		}
	}
}

func (st *state) print(v any, line int) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		st.failf(line, "a nil value cannot be printed")
	}
	return fmt.Sprint(v)
}

func truth(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

func (st *state) field(v any, name string, line int) any {
	m, ok := v.(map[string]any)
	if !ok {
		st.failf(line, "cannot read .%s of %T", name, v)
	}
	r, ok := m[name]
	if !ok {
		st.failf(line, "there is no field .%s", name)
	}
	return r
}

func (st *state) eval(e expr, dot any, line int) any {
	switch e := e.(type) {
	case dotExpr:
		return dot
	case litExpr:
		return e.v
	case fieldExpr:
		v := dot
		for _, f := range e.fields {
			v = st.field(v, f, line)
		}
		return v
	case varExpr:
		var v any
		found := false
		for i := len(st.vars) - 1; i >= 0; i-- {
			if st.vars[i].name == e.name {
				v, found = st.vars[i].v, true
				break
			}
		}
		if !found {
			st.failf(line, "variable %s is not declared", e.name)
		}
		for _, f := range e.fields {
			v = st.field(v, f, line)
		}
		return v
	case chainExpr:
		v := st.eval(e.x, dot, line)
		for _, f := range e.fields {
			v = st.field(v, f, line)
		}
		return v
	case callExpr:
		args := make([]any, len(e.args))
		for i, a := range e.args {
			args[i] = st.eval(a, dot, line)
		}
		if builtinFuncs[e.fn] {
			return st.builtin(e.fn, args, e.line)
		}
		f := st.fns[e.fn]
		if f == nil {
			st.failf(e.line, "function %q is not defined", e.fn)
		}
		return f(args)
	}
	st.failf(line, "cannot evaluate %T", e)
	return nil
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

func (st *state) compare(a, b any, line int) int {
	if x, ok := a.(string); ok {
		if y, ok := b.(string); ok {
			return strings.Compare(x, y)
		}
	}
	if x, ok := number(a); ok {
		if y, ok := number(b); ok {
			switch {
			case x < y:
				return -1
			case x > y:
				return 1
			}
			return 0
		}
	}
	if x, ok := a.(bool); ok {
		if y, ok := b.(bool); ok {
			if x == y {
				return 0
			}
			return 1
		}
	}
	st.failf(line, "cannot compare %T with %T", a, b)
	return 0
}

func (st *state) builtin(name string, a []any, line int) any {
	want := func(n int) {
		if len(a) != n {
			st.failf(line, "%s takes %d arguments, got %d", name, n, len(a))
		}
	}
	switch name {
	case "eq", "ne":
		if len(a) < 2 || name == "ne" && len(a) != 2 {
			st.failf(line, "%s takes two arguments", name)
		}
		for _, b := range a[1:] {
			if st.compare(a[0], b, line) == 0 {
				return name == "eq"
			}
		}
		return name == "ne"
	case "lt":
		want(2)
		return st.compare(a[0], a[1], line) < 0
	case "gt":
		want(2)
		return st.compare(a[0], a[1], line) > 0
	case "le":
		want(2)
		return st.compare(a[0], a[1], line) <= 0
	case "ge":
		want(2)
		return st.compare(a[0], a[1], line) >= 0
	case "not":
		want(1)
		return !truth(a[0])
	case "len":
		want(1)
		switch x := a[0].(type) {
		case string:
			return len(x)
		case []any:
			return len(x)
		case map[string]any:
			return len(x)
		}
		st.failf(line, "len of %T", a[0])
	case "index":
		if len(a) < 2 {
			st.failf(line, "index takes a value and an index")
		}
		v := a[0]
		for _, k := range a[1:] {
			switch x := v.(type) {
			case []any:
				i, ok := k.(int)
				if !ok || i < 0 || i >= len(x) {
					st.failf(line, "index %v is out of range of a list of %d", k, len(x))
				}
				v = x[i]
			case map[string]any:
				key, ok := k.(string)
				if !ok {
					st.failf(line, "a map is indexed by a string, not %T", k)
				}
				r, ok := x[key]
				if !ok {
					st.failf(line, "there is no key %q", key)
				}
				v = r
			default:
				st.failf(line, "cannot index %T", v)
			}
		}
		return v
	case "printf":
		if len(a) == 0 {
			st.failf(line, "printf takes a format string")
		}
		f, ok := a[0].(string)
		if !ok {
			st.failf(line, "printf takes a format string")
		}
		return fmt.Sprintf(f, a[1:]...)
	case "js":
		want(1)
		if s, ok := a[0].(string); ok {
			return jsEscape(s)
		}
		return jsEscape(fmt.Sprint(a[0]))
	}
	return nil
}

// jsEscape is the escaping of text/template's js function: what goes inside a JavaScript string literal. Quotes, backslash,
// angle brackets, "&" and "=" and control characters become escapes; printable non-ASCII text stays as it is.
func jsEscape(s string) string {
	special := func(r rune) bool {
		switch r {
		case '\\', '\'', '"', '<', '>', '&', '=':
			return true
		}
		return r < ' ' || utf8.RuneSelf <= r
	}
	if strings.IndexFunc(s, special) < 0 {
		return s
	}
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); {
		c := s[i]
		if !special(rune(c)) {
			b.WriteByte(c)
			i++
			continue
		}
		if c < utf8.RuneSelf {
			switch c {
			case '\\':
				b.WriteString(`\\`)
			case '\'':
				b.WriteString(`\'`)
			case '"':
				b.WriteString(`\"`)
			case '<':
				b.WriteString(`<`)
			case '>':
				b.WriteString(`>`)
			case '&':
				b.WriteString(`&`)
			case '=':
				b.WriteString(`=`)
			default:
				b.WriteString(`\u00` + string(hexDigits[c>>4]) + string(hexDigits[c&0x0f]))
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if unicode.IsPrint(r) {
			b.WriteString(s[i : i+size])
		} else {
			fmt.Fprintf(&b, `\u%04X`, r)
		}
		i += size
	}
	return b.String()
}
