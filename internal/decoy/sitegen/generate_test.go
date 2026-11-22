//go:build !js

package sitegen

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"hash/maphash"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/decoy"
	"golang.org/x/net/html"
)

var (
	decoyDump  = flag.String("decoy-dump", "", "write sample sites of every kind into this folder, one subfolder per country and site, for looking at them")
	decoyDumpN = flag.Int("decoy-dump-n", 2, "how many seeds of every kind -decoy-dump writes")
	// decoyDumpNew makes the kinds that are still "legacy" in meta.json in the style of their palettes, to look at the palettes.
	decoyDumpNew = flag.Bool("decoy-dump-new", false, "with -decoy-dump: draw every kind in its new style (palettes, fonts, ranges of meta.json) even where legacy is on")
	// decoyDumpCC are the countries -decoy-dump writes sites for.
	decoyDumpCC = flag.String("decoy-dump-cc", DefaultCountry, "the countries (codes, separated by spaces) -decoy-dump writes sites for")
	// decoyDumpKinds narrows -decoy-dump to some kinds; empty means all.
	decoyDumpKinds = flag.String("decoy-dump-kinds", "", "with -decoy-dump: only these kinds (separated by spaces)")
)

// seedsFor is how many seeds a kind is made from for a country: the default country gets the full 200, the others 12 (their pages
// differ from it in text and data, not in structure, and there are sixteen of them: with 40 the package ran past the 10 minute limit in CI).
func seedsFor(c tcase) int {
	if c.cc == DefaultCountry {
		return 200
	}
	return 12
}

var titleTag = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func testSeed(i int) []byte { return []byte(fmt.Sprintf("decoy-test-seed-%d", i)) }

func mustGenerate(t *testing.T, c tcase, i int) []File {
	t.Helper()
	files, _, err := GenerateWith(c.kind, testSeed(i), Options{Country: c.cc})
	if err != nil {
		t.Fatalf("%s seed %d: %v", c, i, err)
	}
	return files
}

// tcase is a kind made for a country.
type tcase struct{ kind, cc string }

func (c tcase) String() string { return c.cc + "/" + c.kind }

// cases are every kind in every country that has its texts.
func cases(t *testing.T) []tcase {
	t.Helper()
	var out []tcase
	for _, c := range Countries() {
		for _, k := range c.Kinds {
			out = append(out, tcase{k, c.Code})
		}
	}
	if len(out) == 0 {
		t.Fatal("no kinds")
	}
	return out
}

func kindIDs(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, k := range Kinds() {
		ids = append(ids, k.ID)
	}
	if len(ids) == 0 {
		t.Fatal("no kinds")
	}
	return ids
}

func TestKinds(t *testing.T) {
	want := []string{"blog", "business", "card", "club", "docs", "files", "login", "portfolio", "project", "status"}
	var got []string
	for _, k := range Kinds() {
		got = append(got, k.ID)
		if k.NameEN == "" || k.NameRU == "" {
			t.Errorf("kind %s has no name in both languages", k.ID)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
	if _, err := Generate("nope", []byte("x")); err == nil {
		t.Error("an unknown kind was generated")
	}
}

// Kinds that must have scripts and a form: they are the only ones allowed the scripts_or_forms warning, together with the kinds
// that ask for a message in a form where the country has no phone range for made-up numbers (TestContactFormOrPhone).
var interactive = map[string]bool{"login": true, "files": true}

var contactKinds = map[string]bool{"business": true, "portfolio": true, "club": true}

var (
	leftoverMark = regexp.MustCompile(`__[a-z][a-z0-9-]*__|\$\$[a-z]|\{\{|<no value>|%!`)
	voidTags     = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true}
)

// checkPage parses one HTML page strictly (every element closed) and returns the local addresses it links to.
func checkPage(t *testing.T, name string, src []byte) (links []string) {
	t.Helper()
	z := html.NewTokenizer(bytes.NewReader(src))
	var stack []string
	ids := map[string]bool{}
	h1, titles := 0, 0
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.StartTagToken, html.SelfClosingTagToken:
			tn, hasAttr := z.TagName()
			tag := string(tn)
			for hasAttr {
				var k, v []byte
				k, v, hasAttr = z.TagAttr()
				switch string(k) {
				case "id":
					if ids[string(v)] {
						t.Errorf("%s: id %q twice", name, v)
					}
					ids[string(v)] = true
				case "href", "src":
					links = append(links, string(v))
				}
			}
			switch tag {
			case "h1":
				h1++
			case "title":
				titles++
			}
			if tt == html.StartTagToken && !voidTags[tag] {
				stack = append(stack, tag)
			}
		case html.EndTagToken:
			tn, _ := z.TagName()
			tag := string(tn)
			if len(stack) == 0 || stack[len(stack)-1] != tag {
				t.Errorf("%s: </%s> closes %v", name, tag, stack)
				return links
			}
			stack = stack[:len(stack)-1]
		}
	}
	if err := z.Err(); err != nil && err.Error() != "EOF" {
		t.Errorf("%s: %v", name, err)
	}
	if len(stack) != 0 {
		t.Errorf("%s: left open: %v", name, stack)
	}
	if h1 != 1 || titles != 1 {
		t.Errorf("%s: %d <h1> and %d <title>, want one of each", name, h1, titles)
	}
	return links
}

func TestGenerateValid(t *testing.T) {
	for _, c := range cases(t) {
		kind := c.kind
		t.Run(c.String(), func(t *testing.T) {
			largest := 0
			for i := 0; i < seedsFor(c); i++ {
				files := mustGenerate(t, c, i)
				b, err := decoy.FromFiles(files)
				if err != nil {
					t.Fatalf("seed %d: FromFiles: %v", i, err)
				}
				if b.Size() > largest {
					largest = b.Size()
				}
				if b.Size() > 600<<10 || len(b.Archive()) > (decoy.MaxBundleBytes+512<<10) {
					t.Fatalf("seed %d: %d bytes, more than a decoy site should be", i, b.Size())
				}
				rep := decoy.Analyze(b, true)
				for _, w := range rep.Warnings {
					if w.Code == decoy.WarnScripts && (interactive[kind] || contactKinds[kind]) {
						continue // the form and its script are what the kind is for
					}
					t.Errorf("seed %d: warning %s %v", i, w.Code, w.Paths)
				}
				if rep.Title == "" {
					t.Errorf("seed %d: no title", i)
				}
				has := map[string]bool{}
				for _, f := range files {
					has[f.Path] = true
				}
				for _, f := range files {
					if leftoverMark.Match(f.Content) {
						t.Errorf("seed %d: %s keeps a template mark: %q", i, f.Path, leftoverMark.Find(f.Content))
					}
					if path.Ext(f.Path) != ".html" {
						continue
					}
					for _, l := range checkPage(t, fmt.Sprintf("seed %d %s", i, f.Path), f.Content) {
						if strings.HasPrefix(l, "#") {
							continue
						}
						p := strings.TrimPrefix(strings.SplitN(strings.SplitN(l, "#", 2)[0], "?", 2)[0], "/")
						if !strings.HasPrefix(l, "/") || !(has[p] || has[path.Join(p, "index.html")] || p == "") {
							t.Errorf("seed %d: %s links to %q, which is not in the site", i, f.Path, l)
						}
					}
				}
			}
			t.Logf("%s: largest of %d sites %d bytes", kind, seedsFor(c), largest)
		})
	}
}

func hashes(files []File) map[[32]byte]string {
	m := map[[32]byte]string{}
	for _, f := range files {
		m[sha256.Sum256(f.Content)] = f.Path
	}
	return m
}

func titleOf(files []File) string {
	for _, f := range files {
		if f.Path == "index.html" {
			if m := titleTag.FindSubmatch(f.Content); m != nil {
				return string(m[1])
			}
		}
	}
	return ""
}

func TestGenerateDiffers(t *testing.T) {
	for _, c := range cases(t) {
		t.Run(c.String(), func(t *testing.T) {
			same := 0
			for i := 0; i < 60; i++ {
				a, b := mustGenerate(t, c, i), mustGenerate(t, c, i+1000)
				hb := hashes(b)
				for h, p := range hashes(a) {
					if q, same := hb[h]; same {
						t.Errorf("pair %d: %s of one site is %s of the other, byte for byte", i, p, q)
					}
				}
				if titleOf(a) == titleOf(b) {
					same++
				}
			}
			if same > 2 { // a name drawn from a few dozen words meets itself now and then; it must stay rare
				t.Errorf("%d of 60 pairs have the same title", same)
			}
		})
	}
}

// markupOnly is the body of an HTML page without its text: the tags with their attributes and the white space between them.
// Two sites may say the same thing; what must differ is how the page is built. The head (doctype, meta) is a fixed preamble
// and does not count.
func markupOnly(src []byte) []byte {
	var out bytes.Buffer
	z := html.NewTokenizer(bytes.NewReader(src))
	inBody := false
	for {
		tt := z.Next()
		if tt == html.ErrorToken {
			return out.Bytes()
		}
		raw := append([]byte(nil), z.Raw()...)
		if tt == html.StartTagToken {
			if name, _ := z.TagName(); string(name) == "body" {
				inBody = true
			}
		}
		if !inBody {
			continue
		}
		if tt == html.TextToken && len(bytes.TrimSpace(raw)) > 0 {
			continue
		}
		out.Write(raw)
	}
}

// sharedRun is the longest run of bytes (over limit) two sites have in common, or "". The text of the pages does not count.
func sharedRun(a, b []File, limit int) string {
	strip := func(files []File) []File {
		out := make([]File, len(files))
		for i, f := range files {
			out[i] = f
			if path.Ext(f.Path) == ".html" {
				out[i].Content = markupOnly(f.Content)
			}
		}
		return out
	}
	a, b = strip(a), strip(b)
	seed := maphash.MakeSeed()
	win := limit + 1
	seen := map[uint64][]byte{}
	for _, f := range b {
		for i := 0; i+win <= len(f.Content); i++ {
			seen[maphash.Bytes(seed, f.Content[i:i+win])] = f.Content[i : i+win]
		}
	}
	for _, f := range a {
		for i := 0; i+win <= len(f.Content); i++ {
			if w, ok := seen[maphash.Bytes(seed, f.Content[i:i+win])]; ok && bytes.Equal(w, f.Content[i:i+win]) {
				return f.Path + ": " + string(w[:min(len(w), 80)])
			}
		}
	}
	return ""
}

func TestGenerateNoSharedRuns(t *testing.T) {
	for _, c := range cases(t) {
		t.Run(c.String(), func(t *testing.T) {
			for i := 0; i < 12; i++ {
				if run := sharedRun(mustGenerate(t, c, i), mustGenerate(t, c, i+500), 200); run != "" {
					t.Errorf("pair %d: more than 200 bytes in common: %s", i, run)
				}
			}
		})
	}
}

func TestGenerateDeterministic(t *testing.T) {
	for _, c := range cases(t) {
		kind := c.kind
		for i := 0; i < 5; i++ {
			a, b := mustGenerate(t, c, i), mustGenerate(t, c, i)
			if len(a) != len(b) {
				t.Fatalf("%s seed %d: %d files, then %d", kind, i, len(a), len(b))
			}
			for j := range a {
				if a[j].Path != b[j].Path || !bytes.Equal(a[j].Content, b[j].Content) {
					t.Fatalf("%s seed %d: %s differs between two runs", kind, i, a[j].Path)
				}
			}
			ba, _ := decoy.FromFiles(a)
			bb, _ := decoy.FromFiles(b)
			if ba.Hash() != bb.Hash() {
				t.Fatalf("%s seed %d: bundle hash differs", kind, i)
			}
		}
	}
}

var (
	externalRef = regexp.MustCompile(`(?i)(https?:)?//[a-z0-9.-]+\.[a-z]{2,}|@import|url\(\s*["']?(https?:|//)|mailto:|tel:`)
	badWord     = regexp.MustCompile(`(?i)\b(vpn|proxy|proxies|mistgate|tunnel|hysteria|vless|wireguard)\b`)
	storageAPI  = regexp.MustCompile(`localStorage|sessionStorage|indexedDB|document\.cookie|XMLHttpRequest|fetch\s*\(|sendBeacon|WebSocket|EventSource|\.submit\s*\(`)
	formTag     = regexp.MustCompile(`(?is)<form\b[^>]*>`)
	buttonTag   = regexp.MustCompile(`(?is)<button\b[^>]*>`)
	submitInput = regexp.MustCompile(`(?i)type\s*=\s*["']?submit`)
	svgNS       = regexp.MustCompile(`http://www\.w3\.org/(2000/svg|1999/xlink)`)
)

func TestGenerateClean(t *testing.T) {
	for _, c := range cases(t) {
		kind := c.kind
		t.Run(c.String(), func(t *testing.T) {
			for i := 0; i < seedsFor(c); i++ {
				for _, f := range mustGenerate(t, c, i) {
					if f.Path == "robots.txt" {
						continue
					}
					text := svgNS.ReplaceAll(f.Content, nil)
					if m := externalRef.Find(text); m != nil {
						t.Errorf("seed %d %s: reaches outside the site: %q", i, f.Path, m)
					}
					if m := badWord.Find(text); m != nil {
						t.Errorf("seed %d %s: telltale word %q", i, f.Path, m)
					}
					if m := storageAPI.Find(text); m != nil {
						t.Errorf("seed %d %s: %q", i, f.Path, m)
					}
					for _, form := range formTag.FindAll(text, -1) {
						if regexp.MustCompile(`(?i)\b(action|method|enctype)\s*=`).Match(form) {
							t.Errorf("seed %d %s: a form that can send: %s", i, f.Path, form)
						}
					}
					for _, b := range buttonTag.FindAll(text, -1) {
						if !regexp.MustCompile(`(?i)type\s*=\s*["']?button`).Match(b) {
							t.Errorf("seed %d %s: a button that could submit: %s", i, f.Path, b)
						}
					}
					if submitInput.Match(text) {
						t.Errorf("seed %d %s: a submit input", i, f.Path)
					}
					if path.Ext(f.Path) == ".html" && !interactive[kind] && !contactKinds[kind] && (bytes.Contains(text, []byte("<script")) || bytes.Contains(text, []byte("<form"))) {
						t.Errorf("seed %d %s: a script or form in a kind without them", i, f.Path)
					}
				}
			}
		})
	}
}

// The same seed at different times of the run, and a seed's files, never depend on other seeds having been made first.
func TestGenerateIndependentOfOrder(t *testing.T) {
	c := cases(t)[0]
	first := mustGenerate(t, c, 7)
	for i := 0; i < 20; i++ {
		mustGenerate(t, c, i)
	}
	again := mustGenerate(t, c, 7)
	for i := range first {
		if !bytes.Equal(first[i].Content, again[i].Content) {
			t.Fatalf("%s differs after other sites were made", first[i].Path)
		}
	}
}

// With -decoy-dump=<dir> the test writes -decoy-dump-n sites of every kind into <dir>/<cc>/<kind>/ (or <kind>-<n> when there are
// several) for every country of -decoy-dump-cc, so that they can be opened and looked at.
func TestDumpSites(t *testing.T) {
	if *decoyDump == "" {
		t.Skip("no -decoy-dump")
	}
	if *decoyDumpNew {
		eachKind(t, func(id string, d *kindDef) {
			if sd := d.meta.Style; sd != nil {
				sd.Legacy = false
			}
		})
	}
	for _, cc := range strings.Fields(*decoyDumpCC) {
		for _, kind := range kindIDs(t) {
			if !contains(Countries(), cc, kind) || (*decoyDumpKinds != "" && !slices.Contains(strings.Fields(*decoyDumpKinds), kind)) {
				continue
			}
			for n := 1; n <= *decoyDumpN; n++ {
				files, _, err := GenerateWith(kind, []byte(fmt.Sprintf("dump-%s-%d", kind, n)), Options{Country: cc})
				if err != nil { // a kind that is being worked on may not build: the others are still written
					t.Logf("%s/%s %d skipped: %v", cc, kind, n, err)
					continue
				}
				name := kind
				if *decoyDumpN > 1 {
					name = fmt.Sprintf("%s-%d", kind, n)
				}
				dir := filepath.Join(*decoyDump, cc, name)
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				for _, f := range files {
					p := filepath.Join(dir, filepath.FromSlash(f.Path))
					if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, f.Content, 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
}

// run renders src as the template "t" with the given dot and functions.
func runTemplate(src string, dot any, fns map[string]tmplFunc, extra ...string) (string, error) {
	s := newTmplSet(fns)
	if err := s.add("t", src); err != nil {
		return "", err
	}
	for i := 0; i+1 < len(extra); i += 2 {
		if err := s.add(extra[i], extra[i+1]); err != nil {
			return "", err
		}
	}
	var b bytes.Buffer
	err := s.exec(&b, "t", dot, fns)
	return b.String(), err
}

func TestTemplateInterpreter(t *testing.T) {
	data := map[string]any{
		"A": "a&b", "N": 3, "Z": 0, "T": true, "Nil": nil, "S": "",
		"M":    map[string]any{"x": map[string]any{"y": "deep"}, "k": "v"},
		"List": []any{"p", "q", "r"},
		"Rows": []any{map[string]any{"n": "one", "ok": true}, map[string]any{"n": "two", "ok": false}},
		"Em":   []any{},
	}
	fns := map[string]tmplFunc{
		"up":  func(a []any) any { return strings.ToUpper(a[0].(string)) },
		"cat": func(a []any) any { return a[0].(string) + a[1].(string) },
	}
	cases := []struct{ name, src, want string }{
		{"text", "plain {not an action}", "plain {not an action}"},
		{"field", "{{.A}}|{{.N}}|{{.T}}", "a&b|3|true"},
		{"chain", "{{.M.x.y}} {{$.M.k}}", "deep v"},
		{"dot", "{{range .List}}{{.}}{{end}}", "pqr"},
		{"literals", `{{"s"}}{{12}}{{-3}}{{true}}`, "s12-3true"},
		{"function", `{{up .A}} {{cat "x" (up "y")}}`, "A&B xY"},
		{"paren chain", "{{(index .Rows 1).n}}", "two"},
		{"if", "{{if .N}}y{{end}}{{if .Z}}y{{else}}n{{end}}{{if .S}}y{{end}}{{if .Em}}y{{end}}{{if .List}}y{{end}}", "yny"},
		{"else if", "{{if eq .N 1}}one{{else if eq .N 3}}three{{else}}other{{end}}", "three"},
		{"eq many", `{{if eq .A "x" "a&b"}}in{{end}}{{if ne .N 4}}ne{{end}}`, "inne"},
		{"compare", "{{lt 1 2}}{{gt 1 2}}{{le 2 2}}{{ge 1 2}}{{lt \"a\" \"b\"}}", "truefalsetruefalsetrue"},
		{"not", "{{not .Z}}{{not .N}}", "truefalse"},
		{"range index", "{{range $i, $v := .List}}{{$i}}{{$v}},{{end}}", "0p,1q,2r,"},
		{"range value", "{{range $v := .List}}{{$v}}{{end}}", "pqr"},
		{"range else", "{{range .Em}}x{{else}}empty{{end}}", "empty"},
		{"range fields", "{{range .Rows}}{{.n}}{{if .ok}}!{{end}} {{end}}", "one! two "},
		{"with", "{{with .M.x}}{{.y}}{{end}}{{with .Z}}no{{else}}else{{end}}", "deepelse"},
		{"variables", "{{$x := 1}}{{if true}}{{$x = 2}}{{$y := 9}}{{end}}{{$x}}", "2"},
		{"range scope", "{{$n := 0}}{{range .List}}{{$n = .}}{{end}}{{$n}}", "r"},
		{"index", `{{index .List 1}}{{index .M "k"}}`, "qv"},
		{"len", "{{len .List}}{{len .A}}{{len .M}}", "332"},
		{"printf", `{{printf "%d-%s-%s" .N .A "z"}}`, "3-a&b-z"},
		{"js", `{{js "it's <b>\"x\" a=b\n…"}}`, `it\'s <b>\"x\" a=b\u000A…`},
		{"trim", "a  {{- 1 -}}  b", "a1b"},
		{"comment", "a{{/* gone */}}b", "ab"},
		{"template", `{{template "u" .M.k}}`, "<v>"},
		{"template no arg", `{{template "n"}}{{template "u" "x"}}`, "-<x>"},
		{"define in file", `{{define "d"}}[{{.}}]{{end}}{{template "d" .N}}`, "[3]"},
		{"dollar in template", `{{template "w" .M}}`, "v/v"},
	}
	extra := []string{"u", "<{{.}}>", "n", "-", "w", "{{.k}}/{{$.k}}"}
	for _, c := range cases {
		got, err := runTemplate(c.src, data, fns, extra...)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}

	// What it does not know is an error, never an empty string.
	bad := map[string]string{
		"missing field":     "{{.Nope}}",
		"deep missing":      "{{.M.x.nope}}",
		"field of text":     "{{.A.b}}",
		"nil printed":       "{{.Nil}}",
		"unknown function":  "{{nope 1}}",
		"pipe":              "{{.A | up}}",
		"unclosed if":       "{{if .N}}x",
		"stray end":         "x{{end}}",
		"stray else":        "{{else}}",
		"unclosed action":   "{{.A",
		"unknown template":  `{{template "zzz" .}}`,
		"undeclared var":    "{{$q}}",
		"assign undeclared": "{{$q = 1}}",
		"range of text":     "{{range .A}}x{{end}}",
		"bad compare":       `{{eq .N "x"}}`,
		"index range":       "{{index .List 7}}",
		"index key":         `{{index .M "zz"}}`,
		"nested define":     `{{if true}}{{define "x"}}{{end}}{{end}}`,
		"empty action":      "{{}}",
		"bad token":         "{{.N + 1}}",
		"two else":          "{{if true}}a{{else}}b{{else}}c{{end}}",
	}
	for name, src := range bad {
		if got, err := runTemplate(src, data, fns); err == nil {
			t.Errorf("%s: no error, got %q", name, got)
		}
	}
}

// The edge builds drop the methods nothing calls, unless the program reaches reflect's MethodByName. text/template does, and
// with it every edge build grows by 10 to 17 MB; the interpreter exists so that it stays out of this package.
func TestNoTemplateEngineImported(t *testing.T) {
	for _, goos := range []string{"", "js"} {
		cmd := exec.Command("go", "list", "-deps", ".")
		if goos != "" {
			cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
		}
		out, err := cmd.Output()
		if err != nil {
			t.Skipf("go list is not available: %v", err)
		}
		for _, pkg := range strings.Fields(string(out)) {
			if pkg == "text/template" || pkg == "html/template" {
				t.Errorf("GOOS=%q: the package depends on %s", goos, pkg)
			}
		}
	}
}

// The generator is for the panel, which runs on a server. Every package that imports it carries its templates and the package
// level tables of its code into the binary, and the edge Workers and the node agent do not need a byte of that: nothing but the
// panel's own build may depend on this package.
func TestGeneratorOnlyInThePanel(t *testing.T) {
	const self = "github.com/mistgate/mistgate/internal/decoy/sitegen"
	const photos = "github.com/mistgate/mistgate/internal/panel/decoyphotos" // the Pexels client and the picture work: the same rule
	const mod = "github.com/mistgate/mistgate/cmd/"
	edges := []string{"mistgate-edge", "mistgate-edge-admin", "mistgate-edge-jobs", "mistgate-edge-link", "mistgate-edge-provision", "mistgate-edge-public"}
	check := func(target string, env ...string) {
		cmd := exec.Command("go", "list", "-deps", mod+target)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.Output()
		if err != nil {
			t.Skipf("go list is not available: %v", err)
		}
		for _, pkg := range strings.Fields(string(out)) {
			if pkg == self || pkg == photos {
				t.Errorf("cmd/%s depends on %s", target, pkg)
			}
		}
	}
	for _, e := range edges {
		check(e, "GOOS=js", "GOARCH=wasm")
	}
	check("mistgate-node", "GOOS=linux", "GOARCH=amd64")
}

// The generator takes finished photos and never fetches any: it must not reach the client of Pexels, or any code that opens a
// connection of its own.
func TestGeneratorHasNoNetworkClient(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list is not available: %v", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasSuffix(pkg, "/internal/panel/decoyphotos") {
			t.Errorf("the generator depends on %s", pkg)
		}
	}
}
