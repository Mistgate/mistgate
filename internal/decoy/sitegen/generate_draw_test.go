//go:build !js

package sitegen

import (
	"bytes"
	"encoding/xml"
	"flag"
	"fmt"
	"hash/maphash"
	"html"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var decoyDraw = flag.String("decoy-draw", "", "write a sheet with every drawing of generate_draw.go into this folder, for looking at it")

// ---- drawings

// testCtx is a context to call the template functions with, for a country and a seed.
func testCtx(t *testing.T, cc, seed string) *ctx {
	t.Helper()
	loc, err := loadLocales()
	if err != nil {
		t.Fatal(err)
	}
	country, err := loc.country(cc)
	if err != nil {
		t.Fatal(err)
	}
	r := newRNG([]byte("draw"), []byte(seed))
	s := &site{country: country, L: loc.shared[country.Lang], Art: ""}
	return &ctx{r: r, s: s, memo: map[string]any{}, files: map[string][]byte{}, artID: "t" + seed}
}

func call(t *testing.T, c *ctx, name string, args ...any) any {
	t.Helper()
	fn := funcMap(c)[name]
	if fn == nil {
		t.Fatalf("no function %s", name)
	}
	var out any
	func() {
		defer func() {
			if r := recover(); r != nil {
				te, ok := r.(tmplError)
				if !ok {
					panic(r)
				}
				t.Fatalf("%s: %s", name, te.msg)
			}
		}()
		out = fn(args)
	}()
	return out
}

// wellFormed parses an inline SVG strictly.
func wellFormed(t *testing.T, name, svg string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(svg))
	d.Strict = true
	depth, first := 0, ""
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("%s: not well formed: %v\n%.300s", name, err, svg)
		}
		switch x := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				first = x.Name.Local
			}
			if x.Name.Local == "title" {
				t.Errorf("%s: has a <title>", name)
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
	if first != "svg" || depth != 0 {
		t.Errorf("%s: root %q depth %d", name, first, depth)
	}
	if len(svg) > 8<<10 {
		t.Errorf("%s: %d bytes, want at most 8192", name, len(svg))
	}
}

// commonRun is a run of limit bytes that a and b share, or "".
func commonRun(a, b string, limit int) string {
	if len(a) < limit || len(b) < limit {
		return ""
	}
	seed := maphash.MakeSeed()
	seen := map[uint64]string{}
	for i := 0; i+limit <= len(b); i++ {
		seen[maphash.String(seed, b[i:i+limit])] = b[i : i+limit]
	}
	for i := 0; i+limit <= len(a); i++ {
		if w, ok := seen[maphash.String(seed, a[i:i+limit])]; ok && w == a[i:i+limit] {
			return w
		}
	}
	return ""
}

// drawings are the template functions that return a picture, with their arguments.
func drawings(t *testing.T, c *ctx) map[string]string {
	t.Helper()
	s := call(t, c, "series", "s", 60, 100, 180)
	items := []any{}
	for _, d := range []string{"2026-11-14", "2026-11-02", "2026-10-27", "2026-10-19", "2026-10-05", "2026-09-29", "2026-09-17", "2026-09-08", "2026-09-08", "2026-08-30"} {
		items = append(items, map[string]any{"date_iso": d, "impact": []string{"minor", "major", "maint"}[len(items)%3]})
	}
	out := map[string]string{
		"chart area":  call(t, c, "chart", s, "area", 900, 220).(string),
		"chart spark": call(t, c, "chart", s, "spark", 160, 40).(string),
		"chart bars":  call(t, c, "chart", s, "bars", 900, 220).(string),
		"cal":         call(t, c, "cal", items, "2026-11-14", 26).(string),
		"map":         call(t, c, "mapsvg", "m", 640, 420).(string),
		"ident":       call(t, c, "ident", "i").(string),
		"stamp":       call(t, c, "stamp", "st", "Baked every morning", 160).(string),
	}
	for _, k := range allArtKinds {
		out["art "+k] = call(t, c, "art", "a", k, 480, 360).(string)
		out["art "+k+" tall"] = call(t, c, "art", "b", k, 360, 480).(string)
	}
	return out
}

func TestDrawingsAreWellFormedSmallAndDifferent(t *testing.T) {
	for _, cc := range []string{"gb", "de", "ru"} {
		a, b := drawings(t, testCtx(t, cc, "one")), drawings(t, testCtx(t, cc, "two"))
		for name, svg := range a {
			wellFormed(t, cc+" "+name, svg)
			wellFormed(t, cc+" "+name+" (other seed)", b[name])
			if run := commonRun(svg, b[name], 150); run != "" {
				t.Errorf("%s %s: two seeds share %q", cc, name, run)
			}
			if svg == b[name] {
				t.Errorf("%s %s: two seeds draw the same picture", cc, name)
			}
		}
		again := drawings(t, testCtx(t, cc, "one"))
		for name, svg := range a {
			if again[name] != svg {
				t.Errorf("%s %s: the same seed draws two pictures", cc, name)
			}
		}
	}
}

func TestIconsAreWellFormed(t *testing.T) {
	for name, body := range icons {
		wellFormed(t, "icon "+name, `<svg viewBox="0 0 24 24">`+body+`</svg>`)
	}
	for _, n := range []string{"menu", "close", "chev", "chevd", "back", "search", "plus", "minus", "grid", "chart", "checkc", "key", "face", "wheat", "basket", "cup", "moon", "nouser", "tag", "link", "arrowc", "sparkle", "external", "cake"} {
		if _, ok := icons[n]; !ok {
			t.Errorf("no icon %q", n)
		}
	}
}

func TestAvailabilityAddsUp(t *testing.T) {
	warn, bad, days := 0, 0, 0
	for i := 0; i < 3000; i++ {
		c := testCtx(t, "gb", fmt.Sprint("av", i))
		m := call(t, c, "avail", "k", 90, 15, 4, 6).(map[string]any)
		ds := m["days"].([]any)
		if len(ds) != 90 {
			t.Fatalf("%d days", len(ds))
		}
		w, b := 0, 0
		for j, d := range ds {
			if j >= 84 && d.(int) != 0 {
				t.Fatalf("seed %d: day %d of a calm end is %v", i, j, d)
			}
			switch d.(int) {
			case 1:
				w++
			case 2:
				b++
			}
		}
		if w != m["warn"] || b != m["bad"] {
			t.Fatalf("counts %d %d, said %v %v", w, b, m["warn"], m["bad"])
		}
		warn, bad, days = warn+w, bad+b, days+90
		// what the figure says is between the best and the worst the days could have cost
		bp := m["bp"].(int)
		lo := 10000 - (w*40+b*240)*10000/(90*1440) - 1
		hi := 10000 - (w*6+b*40)*10000/(90*1440)
		if bp < lo || bp > hi {
			t.Fatalf("seed %d: %d warn and %d bad days but %d hundredths of a percent", i, w, b, bp)
		}
		if w+b == 0 && m["pct"] != "100" {
			t.Fatalf("a month without trouble is %v", m["pct"])
		}
		if want := formatBP(bp, "en-GB"); m["pct"] != want {
			t.Fatalf("pct %v, want %s", m["pct"], want)
		}
	}
	if float64(warn)/float64(days) > .015*1.15 || float64(bad)/float64(days) > .004*1.3 {
		t.Errorf("%d warn and %d bad of %d days: more than 15 and 4 per mille", warn, bad, days)
	}
	if got := formatBP(9998, "de"); got != "99,98" {
		t.Errorf("de: %s", got)
	}
}

func TestSeriesStayInRange(t *testing.T) {
	for i := 0; i < 200; i++ {
		m := call(t, testCtx(t, "gb", fmt.Sprint("se", i)), "series", "k", 60, 90, 160).(map[string]any)
		for _, v := range m["v"].([]any) {
			if v.(int) < 90 || v.(int) > 160 {
				t.Fatalf("%v out of range", v)
			}
		}
		if m["min"].(int) > m["avg"].(int) || m["avg"].(int) > m["max"].(int) {
			t.Fatalf("min avg max %v %v %v", m["min"], m["avg"], m["max"])
		}
	}
}

func TestMapHasStreetsAndLabels(t *testing.T) {
	for i := 0; i < 100; i++ {
		c := testCtx(t, "de", fmt.Sprint("map", i))
		svg := call(t, c, "mapsvg", "k", 640, 420).(string)
		n := 0
		for _, cls := range []string{"__dms__", "__dmm__"} {
			m := regexp.MustCompile(`class="` + cls + `" d="([^"]*)"`).FindStringSubmatch(svg)
			if m == nil {
				t.Fatalf("no %s", cls)
			}
			n += strings.Count(m[1], "M")
		}
		if n < 6 || n > 9 {
			t.Fatalf("seed %d: %d streets", i, n)
		}
		labels := regexp.MustCompile(`<text class="__dml__"[^>]*>([^<]*)</text>`).FindAllStringSubmatch(svg, -1)
		if len(labels) < 2 || len(labels) > 3 {
			t.Fatalf("seed %d: %d street names", i, len(labels))
		}
		for _, l := range labels {
			found := false
			for _, st := range c.s.country.Streets {
				found = found || xmlEscape(st) == l[1]
			}
			if !found {
				t.Errorf("%q is not a street of the country", l[1])
			}
		}
	}
}

func TestStampFitsOnce(t *testing.T) {
	for n := 1; n <= 90; n++ {
		text := strings.Repeat("W", n)
		svg := drawStamp(newRNG([]byte("st"), []byte(fmt.Sprint(n))), text, 160, "x")
		wellFormed(t, fmt.Sprint("stamp ", n), svg)
		if strings.Count(svg, "<textPath") != 1 || strings.Count(svg, ">"+text+" ·<") != 1 {
			t.Fatalf("%d letters: the text is not there once: %s", n, svg)
		}
		fs, _ := strconv.ParseFloat(regexp.MustCompile(`font-size="([\d.]+)"`).FindStringSubmatch(svg)[1], 64)
		tl, _ := strconv.ParseFloat(regexp.MustCompile(`textLength="([\d.]+)"`).FindStringSubmatch(svg)[1], 64)
		if natural := float64(n+2) * .7 * fs; natural > tl+1 {
			t.Errorf("%d letters: %.0f px of letters on %.0f px of rim", n, natural, tl)
		}
		if tl > 2*math.Pi*80 {
			t.Errorf("%d letters: the text is longer than the circle", n)
		}
	}
}

func TestIdenticonMirrors(t *testing.T) {
	for i := 0; i < 50; i++ {
		svg := drawIdent(newRNG([]byte("id"), []byte(fmt.Sprint(i))))
		wellFormed(t, "ident", svg)
		cells := map[[2]int]bool{}
		for _, m := range regexp.MustCompile(`M(\d) (\d)h1v1h-1z`).FindAllStringSubmatch(svg, -1) {
			x, _ := strconv.Atoi(m[1])
			y, _ := strconv.Atoi(m[2])
			cells[[2]int{x, y}] = true
		}
		if len(cells) < 8 {
			t.Fatalf("only %d cells", len(cells))
		}
		for c := range cells {
			if !cells[[2]int{4 - c[0], c[1]}] {
				t.Fatalf("cell %v has no mirror", c)
			}
		}
	}
}

func TestPersonAndTags(t *testing.T) {
	c := testCtx(t, "ru", "p")
	p := call(t, c, "person", "a").(map[string]any)
	if p["name"] != fmt.Sprint(p["first"], " ", p["last"]) || len([]rune(p["ini"].(string))) != 2 {
		t.Errorf("person %v", p)
	}
	if q := call(t, c, "person", "a").(map[string]any); q["name"] != p["name"] {
		t.Error("a key gives two people")
	}
	items := []any{
		map[string]any{"tag": "b", "tags": []any{"x", "y"}}, map[string]any{"tag": "a", "tags": []any{"x"}},
		map[string]any{"tag": "b"}, map[string]any{"tag": ""},
	}
	got := call(t, c, "tags", items, "tag").([]any)
	if len(got) != 2 || got[0].(map[string]any)["tag"] != "b" || got[0].(map[string]any)["n"] != 2 || got[1].(map[string]any)["tag"] != "a" {
		t.Errorf("tags %v", got)
	}
	got = call(t, c, "tags", items, "tags").([]any)
	if len(got) != 2 || got[0].(map[string]any)["tag"] != "x" || got[0].(map[string]any)["n"] != 2 {
		t.Errorf("tags of lists %v", got)
	}
}

func TestCalendarMonths(t *testing.T) {
	c := testCtx(t, "gb", "cm")
	items := []any{map[string]any{"date_iso": "2026-11-14", "impact": "major"}, map[string]any{"date_iso": "2026-11-02", "impact": "minor"}, map[string]any{"date_iso": "2026-10-05", "impact": "maint"}}
	ms := call(t, c, "calmonths", items, "2026-11-14", 3).([]any)
	if len(ms) != 3 {
		t.Fatalf("%d months", len(ms))
	}
	nov := ms[0].(map[string]any)
	days := nov["days"].([]any)
	if len(days) != 30 || nov["pad"] != 6 || nov["label"] != "November 2026" { // 1 November 2026 is a Sunday
		t.Errorf("november: %d days, pad %v, %v", len(days), nov["pad"], nov["label"])
	}
	k := func(d int) any { return days[d-1].(map[string]any)["k"] }
	if k(14) != 2 || k(2) != 1 || k(3) != 0 || k(15) != 4 {
		t.Errorf("kinds %v %v %v %v", k(14), k(2), k(3), k(15))
	}
	if oct := ms[1].(map[string]any)["days"].([]any); oct[4].(map[string]any)["k"] != 3 {
		t.Errorf("maintenance missing: %v", oct[4])
	}
	times := call(t, c, "times", "k", 4).([]any)
	for i := 1; i < len(times); i++ {
		if times[i].(string) <= times[i-1].(string) {
			t.Errorf("times %v are not in order", times)
		}
	}
	if sz := call(t, c, "fsize", "k", 2048, 9000).(string); !regexp.MustCompile(`^\d+\.\d MB$`).MatchString(sz) {
		t.Errorf("size %q", sz)
	}
}

// ---- code

func stripTags(s string) string {
	return html.UnescapeString(regexp.MustCompile(`</?span[^>]*>`).ReplaceAllString(s, ""))
}

func TestHighlightKeepsTheText(t *testing.T) {
	samples := map[string]string{
		"sh":   "$ tool summary --since 2h --format=json app.log # note\n  cat app.log | tool summary - && echo \"a <b> & 'c'\"\ntar xf tool-2.3.tar.gz\nsudo mv tool /usr/local/bin/\n> prompt 42 'open",
		"json": "{\n  \"name\": \"x\\\"y\",\n  \"n\": -12.5e3, \"ok\": true, \"none\": null,\n  \"list\": [1, 2, \"a<b\"]\n}",
		"yaml": "# top\nname: tool  # why\nitems:\n  - key: \"v\"\n  - 12\n[section]\nport = 8080\n",
		"out":  "app.log: 48 211 lines, 3 levels\n  info   46 902\ntop message (9 413 times) a1 b2",
		"":     "plain <b> & text",
	}
	alphabet := []rune("$ -#\"'\n\t<>&:=0123456789a{}[],|;äж\\")
	r := newRNG([]byte("hl"))
	for i := 0; i < 3000; i++ {
		var b strings.Builder
		for n := r.between(0, 60); n > 0; n-- {
			b.WriteRune(alphabet[r.n(len(alphabet))])
		}
		samples[fmt.Sprintf("fuzz%d", i)] = b.String()
	}
	for name, code := range samples {
		langs := []string{"sh", "json", "yaml", "toml", "out", ""}
		if v, ok := map[string]string{"sh": "sh", "json": "json", "yaml": "yaml", "out": "out", "": ""}[name]; ok {
			langs = []string{v}
		}
		for _, lang := range langs {
			out := highlight(code, lang)
			if got := stripTags(out); got != code {
				t.Fatalf("%s/%s: text changed\n got %q\nwant %q\nhtml %s", name, lang, got, code, out)
			}
			if strings.ContainsAny(strings.ReplaceAll(regexp.MustCompile(`</?span[^>]*>`).ReplaceAllString(out, ""), "&", ""), "<>") {
				t.Fatalf("%s/%s: unescaped angle bracket in %s", name, lang, out)
			}
		}
	}
}

func TestHighlightMarksTheParts(t *testing.T) {
	out := highlight("$ tally summary --since 2h \"a b\" # note", "sh")
	for _, want := range []string{`__dhp__">$ `, `__dhm__">tally`, `__dhf__">--since`, `__dhn__">2h`, `__dhs__">&quot;a b&quot;`, `__dhc__"># note`} {
		if !strings.Contains(out, want) {
			t.Errorf("sh: no %q in %s", want, out)
		}
	}
	j := highlight(`{"a": "b", "n": 3, "t": true}`, "json")
	for _, want := range []string{`__dhk__">&quot;a&quot;`, `__dhs__">&quot;b&quot;`, `__dhn__">3`, `__dhn__">true`} {
		if !strings.Contains(j, want) {
			t.Errorf("json: no %q in %s", want, j)
		}
	}
	if y := highlight("port: 80 # x", "yaml"); !strings.Contains(y, `__dhk__">port`) || !strings.Contains(y, `__dhn__">80`) || !strings.Contains(y, `__dhc__"># x`) {
		t.Errorf("yaml: %s", y)
	}
}

// ---- the functions through the interpreter, as a template calls them

func TestDrawFunctionsInTemplates(t *testing.T) {
	c := testCtx(t, "gb", "tpl")
	src := `{{$s := series "k" 30 100 200}}{{chart $s "area" 600 200}}{{$a := avail "a" 30 15 4 3}}{{range $a.days}}{{.}}{{end}} {{$a.pct}}` +
		`{{$p := person "x"}}{{$p.first}} {{$p.ini}}{{range tags (pick "t" (seq 3) 2) "x"}}{{end}}{{ident "i"}}{{stamp "s" "ab" 100}}{{hl "$ a -b" "sh"}}`
	out, err := runTemplate(src, map[string]any{}, funcMap(c))
	if err != nil {
		// tags of a list of numbers has nothing to count, which is not an error; any other failure is
		t.Fatal(err)
	}
	if !strings.Contains(out, "<svg") || !strings.Contains(out, "__dhp__") {
		t.Errorf("output %.200s", out)
	}
}

// With -decoy-draw=<dir> the test writes one page with every drawing, in a light and a dark colour scheme, so that they can be
// looked at in a browser: <dir>/draw.html.
func TestDumpDrawings(t *testing.T) {
	if *decoyDraw == "" {
		t.Skip("no -decoy-draw")
	}
	c := testCtx(t, "gb", "sheet")
	var b bytes.Buffer
	b.WriteString(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><style>
:root{color-scheme:light dark;--bg:#f6f5f1;--band:#ecebe5;--sf:#fff;--sf2:#e4e2da;--fg:#14130f;--mu:#5a574c;--ln:#d6d3c8;--ac:#c2410c;--ai:#fff;--a2:#2447a3;--so:#fbe0d1;--ok:#22875a;--oks:#d9efe3;--wa:#c98a0a;--bd:#c53030;--fb:system-ui,sans-serif;--fh:Georgia,serif;--fm:ui-monospace,monospace}
@media (prefers-color-scheme:dark){:root{--bg:#0e1114;--band:#13171b;--sf:#171c21;--sf2:#212830;--fg:#e8ecef;--mu:#9aa6b0;--ln:#2a323a;--ac:#fb923c;--ai:#14130f;--a2:#7aa2ff;--so:#3a2a1f;--ok:#4ade80;--oks:#17321f;--wa:#fbbf24;--bd:#f87171}}
body{margin:0;padding:24px;background:var(--bg);color:var(--fg);font:15px/1.5 var(--fb)}h2{font:700 13px var(--fb);text-transform:uppercase;letter-spacing:.08em;color:var(--mu);margin:28px 0 8px}
.g{display:grid;grid-template-columns:repeat(auto-fill,minmax(300px,1fr));gap:16px}.w{background:var(--sf);border:1px solid var(--ln);padding:12px;border-radius:10px}.w svg{display:block;width:100%;height:auto}.s{width:140px}.i{width:72px}
pre{background:var(--sf);border:1px solid var(--ln);padding:14px;border-radius:8px;overflow:auto}
`)
	b.WriteString(drawCSSForSheet(t))
	b.WriteString("</style><body>")
	for _, name := range []string{"chart area", "chart spark", "chart bars", "cal", "map"} {
		fmt.Fprintf(&b, "<h2>%s</h2><div class=w>%s</div>", name, drawings(t, c)[name])
	}
	b.WriteString("<h2>ident, stamp</h2><div class=g>")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, "<div class=w><div class=i>%s</div></div>", call(t, c, "ident", fmt.Sprint("i", i)))
	}
	fmt.Fprintf(&b, "<div class=w><div class=s>%s</div></div>", call(t, c, "stamp", "s1", "Baked every morning", 160))
	fmt.Fprintf(&b, "<div class=w><div class=s>%s</div></div>", call(t, c, "stamp", "s2", "Hand made in small batches since the very beginning of the century", 160))
	fmt.Fprintf(&b, "<div class=w><div class=s>%s</div></div></div>", call(t, c, "stamp", "s3", "Open", 160))
	b.WriteString("<h2>art</h2><div class=g>")
	for _, k := range allArtKinds {
		fmt.Fprintf(&b, "<div class=w>%s<br>%s</div>", call(t, c, "art", "p", k, 480, 360), k)
	}
	b.WriteString("</div><h2>code</h2>")
	fmt.Fprintf(&b, "<pre>%s</pre><pre>%s</pre><pre>%s</pre>", highlight("$ tally summary --since 2h \"a b\" app.log # note\ntar xf tally-2.3.tar.gz", "sh"),
		highlight("{\n  \"name\": \"x\",\n  \"n\": 3, \"ok\": true\n}", "json"), highlight("# top\nname: tally\nport: 8080", "yaml"))
	if err := os.MkdirAll(*decoyDraw, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*decoyDraw, "draw.html"), cssMark.ReplaceAll(b.Bytes(), []byte("$1")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// drawCSSForSheet is the draw_css template, run with names that a sheet can use.
func drawCSSForSheet(t *testing.T) string {
	t.Helper()
	d, err := loadKinds()
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := d["status"].tpl.exec(&b, "draw_css", map[string]any{}, nil); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// ---- helpers for the second wave of kinds

func TestHoursPartsAreHrsCut(t *testing.T) {
	for _, cc := range []string{"gb", "us", "de", "ru"} {
		c := testCtx(t, cc, "hours")
		for _, args := range [][]any{{1, 5, "08:00", "18:30"}, {6, 6, "09:00", "13:00"}, {7, 7, "", ""}} {
			p := call(t, c, "hrsp", args...).(map[string]any)
			days, when := p["days"].(string), p["time"].(string)
			if days == "" || when == "" {
				t.Fatalf("%s %v: days %q, time %q", cc, args, days, when)
			}
			line := call(t, c, "hrs", args...).(string)
			if !strings.Contains(line, days) || !strings.Contains(line, when) {
				t.Errorf("%s %v: hrs says %q, hrsp %q and %q", cc, args, line, days, when)
			}
		}
		if p := call(t, c, "hrsp", 1, 1, "", "").(map[string]any); p["time"] != str(c.s.L["closed"]) {
			t.Errorf("%s: a closed day says %q", cc, p["time"])
		}
	}
}

func TestLastWord(t *testing.T) {
	for _, w := range []struct{ in, head, last, end string }{
		{"Slow bread, made well", "Slow bread, made ", "well", ""},
		{"Slow bread, made well.", "Slow bread, made ", "well", "."},
		{"Hello world!", "Hello ", "world", "!"},
		{"Вкусно и просто.", "Вкусно и ", "просто", "."},
		{"Schnell und zuverlässig…", "Schnell und ", "zuverlässig", "…"},
		{"Say \"cheese\"!", "Say ", "\"cheese", "\"!"},
		{"One", "", "One", ""},
		{"Один.", "", "Один", "."},
		{"  padded words  ", "padded ", "words", ""},
		{"a-b c-d", "a-b ", "c-d", ""},
		{"", "", "", ""},
		{"...", "", "...", ""},
	} {
		head, last, end := lastWord(w.in)
		if head != w.head || last != w.last || end != w.end {
			t.Errorf("lastWord(%q) = %q %q %q, want %q %q %q", w.in, head, last, end, w.head, w.last, w.end)
		}
		if head+last+end != strings.TrimSpace(w.in) {
			t.Errorf("lastWord(%q) does not add up", w.in)
		}
	}
	c := testCtx(t, "gb", "lw")
	m := call(t, c, "lastword", "Fresh every morning.").(map[string]any)
	if m["head"] != "Fresh every " || m["last"] != "morning" || m["end"] != "." {
		t.Errorf("lastword from a template: %v", m)
	}
}

func TestPriceKeepsSmallChange(t *testing.T) {
	loc, err := loadLocales()
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		cc   string
		in   float64
		want string
	}{{"us", 4.8, "$4.80"}, {"us", 30, "$30"}, {"gb", 95, "£76"}, {"gb", 4.8, "£3.85"}, {"ru", 4.8, loc.countries["ru"].money(4.8)}} {
		if got := loc.countries[w.cc].price(w.in); got != w.want {
			t.Errorf("%s: price(%v) = %q, want %q", w.cc, w.in, got, w.want)
		}
	}
	if got := loc.countries["de"].price(6); !regexp.MustCompile(`^\d+,\d\d\x{a0}€$`).MatchString(got) {
		t.Errorf("de: price(6) = %q", got)
	}
}

// Pictures and marks that the second wave of kinds leans on must not repeat between sites: 40 pairs of seeds, not two.
func TestCheckerAndMarkDifferBetweenSites(t *testing.T) {
	for i := 0; i < 40; i++ {
		a, b := testCtx(t, "gb", fmt.Sprint("pa", i)), testCtx(t, "gb", fmt.Sprint("pb", i))
		for _, size := range [][2]int{{480, 360}, {360, 480}} {
			pa := call(t, a, "art", "k", "checker", size[0], size[1]).(string)
			pb := call(t, b, "art", "k", "checker", size[0], size[1]).(string)
			wellFormed(t, "checker", pa)
			if run := commonRun(pa, pb, 150); run != "" {
				t.Fatalf("pair %d: two checkers share %q", i, run)
			}
		}
		pal := palette{}
		pal.L.Acc, pal.L.Acc2, pal.L.AccInk = "#b5472a", "#3a6a4a", "#ffffff"
		ma, mb := inlineMark(faviconMark(newRNG([]byte("m"), []byte(fmt.Sprint("a", i))), pal, "K")), inlineMark(faviconMark(newRNG([]byte("m"), []byte(fmt.Sprint("b", i))), pal, "K"))
		wellFormed(t, "mark", ma)
		if run := commonRun(ma, mb, 150); run != "" {
			t.Fatalf("pair %d: two marks share %q", i, run)
		}
	}
}

func TestDarkArtIsDarkAndInThePalette(t *testing.T) {
	acc, page := "#ff8a65", "#1b1530" // an orange accent (hue about 14) on a violet night (about 255)
	hue := func(hex string) float64 {
		c, _ := parseHex(hex)
		h, _, _ := toHSL(c)
		return h
	}
	near := func(a, b float64) float64 { return math.Abs(math.Mod(a-b+540, 360) - 180) }
	for _, kind := range allArtKinds {
		r := newRNG([]byte("dark"), []byte(kind))
		svg := artSVG(r, kind, 120, 480, 360, "d1", false)
		dark := darkArt(svg, kind, acc, page)
		wellFormed(t, "dark "+kind, dark)
		if strings.Count(svg, "<") != strings.Count(dark, "<") {
			t.Errorf("%s: the markup changed", kind)
		}
		if bg := regexp.MustCompile(`<rect[^>]*fill="(#[0-9a-f]{6})"`).FindStringSubmatch(dark); bg != nil && kind != "poster" && kind != "tiles" {
			c, _ := parseHex(bg[1])
			if _, _, l := toHSL(c); l > 25 {
				t.Errorf("%s: the ground is still pale (%s, lightness %.0f)", kind, bg[1], l)
			}
		}
		// every saturated colour is in the hues of the page or of the accent
		for _, hex := range hexInSVG.FindAllString(dark, -1) {
			c, _ := parseHex(hex)
			if h, s, _ := toHSL(c); s > 30 && near(h, hue(acc)) > 60 && near(h, hue(page)) > 25 {
				t.Errorf("%s: colour %s is in neither the accent's nor the page's hue", kind, hex)
				break
			}
		}
		if turned := darkArt(svg, kind, "", ""); turned == svg && kind != "stars" {
			t.Errorf("%s: without a palette the picture should still be turned", kind)
		}
	}
	// ids that look like colours are not touched
	if got := darkArt(`<g id="a1b2c3d4"/><use href="#a1b2c3d4"/><path fill="#ffffff"/>`, "staff", acc, page); !strings.Contains(got, `href="#a1b2c3d4"`) || strings.Contains(got, "#ffffff") {
		t.Errorf("an id was recoloured or a colour was not: %s", got)
	}
}
