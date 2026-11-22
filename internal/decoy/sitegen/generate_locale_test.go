//go:build !js

package sitegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	"golang.org/x/net/html"
)

// The data of countries and languages: what a new language or country must bring, and what it may not break.

func readJSON(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := fs.ReadFile(templateFS, name)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

func langsOf(t *testing.T, dir string) []string {
	t.Helper()
	files, err := fs.ReadDir(templateFS, "templates/"+dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		if l, ok := textLang(f.Name()); ok {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

// Every language has the same keys: in the shared words, in the kind's ui and in every variant it writes (a language may write
// only some of the variants, never half of one).
func TestLanguagesHaveTheSameKeys(t *testing.T) {
	shapes := map[string]string{}
	for _, l := range langsOf(t, "shared") {
		s, err := shapeOf(readJSON(t, "templates/shared/text."+l+".json"))
		if err != nil {
			t.Fatalf("shared text.%s: %v", l, err)
		}
		shapes[l] = s
	}
	if len(shapes) < 2 {
		t.Fatal("fewer than two languages in shared")
	}
	first := ""
	for l, s := range shapes {
		if first == "" {
			first = l
		}
		if s != shapes[first] {
			t.Errorf("shared text.%s has other keys than text.%s:\n%s\n%s", l, first, s, shapes[first])
		}
	}
	kinds, err := loadKinds()
	if err != nil {
		t.Fatal(err)
	}
	for id, d := range kinds {
		uiShape, bundleShape := map[string]string{}, map[string]map[string]string{}
		for lang, lt := range d.texts {
			s, err := shapeOf(lt.UI)
			if err != nil {
				t.Fatalf("%s text.%s ui: %v", id, lang, err)
			}
			uiShape[lang] = s
			for bid, b := range lt.Bundles {
				bs, err := shapeOf(b)
				if err != nil {
					t.Fatalf("%s text.%s variant %s: %v", id, lang, bid, err)
				}
				if bundleShape[bid] == nil {
					bundleShape[bid] = map[string]string{}
				}
				bundleShape[bid][lang] = bs
			}
		}
		check := func(what string, by map[string]string) {
			var langs []string
			for l := range by {
				langs = append(langs, l)
			}
			sort.Strings(langs)
			for _, l := range langs[1:] {
				if by[l] != by[langs[0]] {
					t.Errorf("%s %s: text.%s has other keys than text.%s:\n  %s\n  %s", id, what, l, langs[0], by[l], by[langs[0]])
				}
			}
		}
		check("ui", uiShape)
		for bid, by := range bundleShape {
			check("variant "+bid, by)
		}
		// every variant is written in at least one language of every kind, and every language writes at least one
		for lang, lt := range d.texts {
			if len(lt.Bundles) == 0 {
				t.Errorf("%s text.%s has no variants", id, lang)
			}
		}
	}
}

// Every variant of every language joins its fixed data and its texts, and fills the pools its lists draw from.
func TestVariantsAssemble(t *testing.T) {
	kinds, err := loadKinds()
	if err != nil {
		t.Fatal(err)
	}
	loc, err := loadLocales()
	if err != nil {
		t.Fatal(err)
	}
	for id, d := range kinds {
		for lang, lt := range d.texts {
			for _, b := range d.bundles {
				text, ok := lt.Bundles[b.ID]
				if !ok {
					continue
				}
				merged := map[string]any{}
				for i, l := range []map[string]any{loc.shared[lang], d.fixedUI, lt.UI, b.Fixed, text} {
					if err := mergeText(merged, l); err != nil {
						t.Errorf("%s %s %s layer %d: %v", id, lang, b.ID, i, err)
					}
				}
				for _, ld := range d.meta.Lists {
					if ld.From == "" {
						continue
					}
					pool, _ := merged[ld.From].([]any)
					if len(pool) < ld.Min {
						t.Errorf("%s %s %s: list %s needs %d items from %q, the texts have %d", id, lang, b.ID, ld.Name, ld.Min, ld.From, len(pool))
					}
				}
				for _, k := range []string{"nav_label", "nf_title", "nf_text", "nf_back", "home_title", "desc", "footer", "tagline"} {
					if s, _ := merged[k].(string); s == "" {
						t.Errorf("%s %s %s: no text %q", id, lang, b.ID, k)
					}
				}
				if len(d.meta.PhotoQ)+len(b.PhotoQ) > 0 {
					for _, q := range append(append([]string(nil), d.meta.PhotoQ...), b.PhotoQ...) {
						if !regexp.MustCompile(`^[a-z ]+$`).MatchString(q) {
							t.Errorf("%s %s: photo search %q is not plain English words", id, b.ID, q)
						}
					}
				}
			}
		}
	}
}

// Templates carry no words: every word of a page is a text, so that a new language is new files and nothing else.
func TestTemplatesHaveNoWords(t *testing.T) {
	action := regexp.MustCompile(`(?s)\{\{.*?\}\}`)
	files, err := fs.Glob(templateFS, "templates/*/*.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		raw, _ := fs.ReadFile(templateFS, f)
		src := string(raw)
		for { // a {{define "contact_js"}} holds a script, which is checked as one in the .js.tmpl that includes it
			i := strings.Index(src, `{{define "contact_js"}}`)
			if i < 0 {
				break
			}
			j := strings.Index(src[i:], "{{end}}")
			lintScript(t, f, action.ReplaceAllString(src[i:i+j], "\x00"))
			src = src[:i] + src[i+j+len("{{end}}"):]
		}
		src = action.ReplaceAllString(src, "\x00")
		switch {
		case strings.HasSuffix(f, ".html.tmpl"):
			z := html.NewTokenizer(strings.NewReader(src))
			skip := ""
			for {
				tt := z.Next()
				if tt == html.ErrorToken {
					break
				}
				switch tt {
				case html.StartTagToken, html.SelfClosingTagToken:
					tn, has := z.TagName()
					name := string(tn)
					if name == "script" || name == "style" {
						skip = name
					}
					for has {
						var k, v []byte
						k, v, has = z.TagAttr()
						switch string(k) {
						case "alt", "title", "aria-label", "placeholder", "label":
							if strings.IndexFunc(string(v), unicode.IsLetter) >= 0 {
								t.Errorf("%s: <%s %s=%q> is a word in a template", f, name, k, v)
							}
						}
					}
				case html.EndTagToken:
					tn, _ := z.TagName()
					if string(tn) == skip {
						skip = ""
					}
				case html.TextToken:
					if skip == "" && strings.IndexFunc(string(z.Text()), unicode.IsLetter) >= 0 {
						t.Errorf("%s: the text %q is a word in a template", f, strings.TrimSpace(string(z.Text())))
					}
				}
			}
		case strings.HasSuffix(f, ".js.tmpl"):
			lintScript(t, f, src)
		}
	}
}

// lintScript finds the strings of a script that are words: with a space between letters, or with letters of a language.
func lintScript(t *testing.T, f, src string) {
	t.Helper()
	lit := regexp.MustCompile(`'([^'\
]*)'|"([^"\
]*)"`)
	for _, m := range lit.FindAllStringSubmatch(src, -1) {
		s := m[1] + m[2]
		letters := strings.IndexFunc(s, unicode.IsLetter) >= 0
		if letters && (strings.Contains(s, " ") && strings.TrimSpace(s) != "" || strings.IndexFunc(s, func(r rune) bool { return r > unicode.MaxASCII }) >= 0) {
			t.Errorf("%s: the string %q is a word in a script", f, s)
		}
	}
}

// What a country must bring, and that its numbers are well formed.
func TestCountryData(t *testing.T) {
	loc, err := loadLocales()
	if err != nil {
		t.Fatal(err)
	}
	if len(loc.countries) < 8 {
		t.Fatalf("%d countries", len(loc.countries))
	}
	for cc, c := range loc.countries {
		if n := len(c.Cities); n < 15 || n > 30 {
			t.Errorf("%s: %d cities, want 15 to 30", cc, n)
		}
		if len(c.Streets) < 12 || len(c.First) < 30 || len(c.Last) < 30 {
			t.Errorf("%s: %d streets, %d first and %d last names", cc, len(c.Streets), len(c.First), len(c.Last))
		}
		names := map[string]bool{}
		for _, ci := range c.Cities {
			if names[ci.Name] || ci.Name == "" || len(ci.Zip) == 0 {
				t.Errorf("%s: city %q is repeated or has no postcode beginning", cc, ci.Name)
			}
			names[ci.Name] = true
			for _, z := range ci.Zip {
				if z == "" || strings.Contains(z, " ") {
					t.Errorf("%s: %s postcode beginning %q", cc, ci.Name, z)
				}
			}
			if strings.Contains(c.Address, "{state}") && ci.State == "" {
				t.Errorf("%s: %s has no state", cc, ci.Name)
			}
			if p := phonePattern(c, ci); p != "" && !regexp.MustCompile(`^[0-9X() +-]+X$`).MatchString(p) {
				t.Errorf("%s: %s phone pattern %q", cc, ci.Name, p)
			}
		}
		for _, s := range []string{"{street}", "{no}", "{zip}", "{city}"} {
			if !strings.Contains(c.Address, s) {
				t.Errorf("%s: the address format has no %s", cc, s)
			}
		}
		if c.ZipTail == "" && cc != "xx" {
			// a postcode with nothing after the city's beginning is only for a city with a whole code; none of ours
			t.Errorf("%s: no zip_tail", cc)
		}
		for _, d := range c.Date {
			if !strings.Contains(d, "{y}") {
				t.Errorf("%s: date format %q has no year", cc, d)
			}
		}
		if len(loc.shared[c.Lang]) == 0 {
			t.Errorf("%s: no shared text for %s", cc, c.Lang)
		}
		first := map[string]bool{}
		for _, f := range c.First {
			first[strings.TrimSuffix(strings.TrimSuffix(f, ":f"), ":m")] = true
		}
		if len(first) < 30 {
			t.Errorf("%s: %d different first names", cc, len(first))
		}
	}
}

func phonePattern(c *countryDef, ci cityDef) string {
	if ci.Phone != nil {
		return *ci.Phone
	}
	return c.Phone
}

// phoneRE is the pattern of a number with X for a digit as an expression.
func phoneRE(p string) *regexp.Regexp {
	var b strings.Builder
	for _, r := range p {
		if r == 'X' {
			b.WriteString(`\d`)
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return regexp.MustCompile(b.String())
}

// addressRE finds the postal address of a country in a page's text; its group "city" is the city.
func addressRE(c *countryDef) *regexp.Regexp {
	alt := func(l []string) string {
		q := make([]string, len(l))
		for i, s := range l {
			q[i] = regexp.QuoteMeta(s)
		}
		return strings.Join(q, "|")
	}
	var cities []string
	for _, ci := range c.Cities {
		cities = append(cities, ci.Name)
	}
	re := regexp.QuoteMeta(c.Address)
	re = strings.NewReplacer(`\{street\}`, "(?:"+alt(c.Streets)+")", `\{no\}`, `\d+`, `\{zip\}`, `[0-9A-Z-]+(?: [0-9A-Z]+)?`,
		`\{city\}`, "(?P<city>"+alt(cities)+")", `\{state\}`, `[A-Z]{2}`).Replace(re)
	return regexp.MustCompile(re)
}

// The made-up ranges: the phone of a site is in the range of its city, a country or city without a range shows no phone at all,
// and it asks for a message in a form instead.
func TestPhonesAndContactForms(t *testing.T) {
	loc, err := loadLocales()
	if err != nil {
		t.Fatal(err)
	}
	// The usual run of digits of a phone number, in a page's text only (not in attributes, nor in code).
	phoneLike := regexp.MustCompile(`\(?\d[\d ()\-]{6,}\d`)
	texts := func(src []byte) string {
		var b strings.Builder
		z := html.NewTokenizer(bytes.NewReader(src))
		skip := ""
		for {
			switch z.Next() {
			case html.ErrorToken:
				return b.String()
			case html.StartTagToken:
				if tn, _ := z.TagName(); string(tn) == "script" || string(tn) == "style" || string(tn) == "pre" {
					skip = string(tn)
				}
			case html.EndTagToken:
				if tn, _ := z.TagName(); string(tn) == skip {
					skip = ""
				}
			case html.TextToken:
				if skip == "" {
					b.Write(z.Text())
					b.WriteByte('\n')
				}
			}
		}
	}
	for _, c := range cases(t) {
		if !contactKinds[c.kind] {
			continue
		}
		country := loc.countries[c.cc]
		addr := addressRE(country)
		t.Run(c.String(), func(t *testing.T) {
			t.Parallel() // the cases share nothing, and one after another the package does not fit its ten minutes on a slow runner
			withPhone, withForm := 0, 0
			for i := 0; i < seedsFor(c); i++ {
				files := mustGenerate(t, c, i)
				var city *cityDef
				page := ""
				hasForm := false
				for _, f := range files {
					if path.Ext(f.Path) != ".html" {
						continue
					}
					tx := texts(f.Content)
					page += tx
					if bytes.Contains(f.Content, []byte("<form")) {
						hasForm = true
					}
				}
				if m := addr.FindStringSubmatch(page); m != nil {
					for k := range country.Cities {
						if country.Cities[k].Name == m[addr.SubexpIndex("city")] {
							city = &country.Cities[k]
						}
					}
				}
				if city == nil {
					t.Fatalf("seed %d: no address of the country in the pages", i)
				}
				found := phoneLike.FindAllString(page, -1)
				want := phonePattern(country, *city)
				if want == "" {
					withForm++
					if len(found) > 0 {
						t.Errorf("seed %d: %s has no phone range but the pages show %q", i, city.Name, found)
					}
					if !hasForm {
						t.Errorf("seed %d: %s has no phone, and no contact form either", i, city.Name)
					}
					continue
				}
				withPhone++
				re := phoneRE(want)
				ok := false
				for _, f := range found {
					if re.MatchString(f) {
						ok = true
					}
				}
				if !ok {
					t.Errorf("seed %d: %s should show a number of the range %s, the pages show %q", i, city.Name, want, found)
				}
				for _, f := range found {
					if !re.MatchString(f) {
						t.Errorf("seed %d: %s shows %q, outside its range %s", i, city.Name, f, want)
					}
				}
				if hasForm {
					t.Errorf("seed %d: %s shows a phone and also a contact form", i, city.Name)
				}
			}
			t.Logf("%s: %d sites with a phone, %d with a form", c, withPhone, withForm)
		})
	}
}

// The sizes of the pieces: how many pages a site has, so that none of them is a dead end of three or four.
func TestDepth(t *testing.T) {
	min := map[string]int{"blog": 12, "portfolio": 12, "business": 5, "docs": 8, "project": 10, "club": 5, "status": 11, "files": 6, "login": 5, "card": 5}
	for _, c := range cases(t) {
		t.Run(c.String(), func(t *testing.T) {
			t.Parallel() // the cases share nothing, and one after another the package does not fit its ten minutes on a slow runner
			least := 1 << 20
			for i := 0; i < 12; i++ {
				n := 0
				for _, f := range mustGenerate(t, c, i) {
					if path.Ext(f.Path) == ".html" {
						n++ // the 404 page is one of them
					}
				}
				least = min2(least, n)
			}
			if least < min[c.kind] {
				t.Errorf("the smallest of 12 sites has %d pages, want %d", least, min[c.kind])
			}
		})
	}
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// No date is later than the day the templates were last written.
var datetimeAttr = regexp.MustCompile(`datetime="(\d{4}-\d\d-\d\d)"`)

func TestDatesAreNotInTheFuture(t *testing.T) {
	const latest = "2026-11-21"
	for _, c := range cases(t) {
		for i := 0; i < 8; i++ {
			for _, f := range mustGenerate(t, c, i) {
				for _, m := range datetimeAttr.FindAllSubmatch(f.Content, -1) {
					if string(m[1]) > latest {
						t.Errorf("%s seed %d %s: the date %s is in the future", c, i, f.Path, m[1])
					}
				}
			}
		}
	}
}

// The unit pieces of a locale.
func TestMoneyClockAndPatterns(t *testing.T) {
	loc, err := loadLocales()
	if err != nil {
		t.Fatal(err)
	}
	money := map[string][2]string{"us": {"4.8", "$5"}, "gb": {"95", "£76"}, "de": {"28", "25\u00a0€"}, "ch": {"28", "CHF\u00a039"}, "ru": {"45", "4\u00a0050\u00a0₽"}}
	for cc, w := range money {
		var v float64
		fmt.Sscan(w[0], &v)
		if got := loc.countries[cc].money(v); got != w[1] {
			t.Errorf("%s: money(%s) = %q, want %q", cc, w[0], got, w[1])
		}
	}
	clocks := []struct{ cc, in, want string }{{"us", "09:00", "9 am"}, {"us", "17:30", "5:30 pm"}, {"us", "12:00", "12 pm"}, {"us", "00:15", "12:15 am"}, {"de", "08:00", "8:00"}, {"ru", "19:30", "19:30"}}
	for _, c := range clocks {
		if got := loc.countries[c.cc].clock(c.in, "am", "pm"); got != c.want {
			t.Errorf("%s: clock(%s) = %q, want %q", c.cc, c.in, got, c.want)
		}
	}
	r := newRNG([]byte("pattern"))
	for i := 0; i < 50; i++ {
		got := fillPattern(r.sub(fmt.Sprint(i)), "(212) 555-01XX", "")
		if !regexp.MustCompile(`^\(212\) 555-01\d\d$`).MatchString(got) {
			t.Fatalf("pattern: %q", got)
		}
		z := fillPattern(r.sub(fmt.Sprint(i)), " XAA", "ABDEFGHJLNPQRSTUWXYZ")
		if !regexp.MustCompile(`^ \d[ABDEFGHJLNPQRSTUWXYZ]{2}$`).MatchString(z) {
			t.Fatalf("postcode tail: %q", z)
		}
	}
	// a Russian woman gets the woman's form of the surname
	c := loc.countries["ru"]
	for i := 0; i < 200; i++ {
		first, last := c.person(newRNG([]byte("p"), []byte(fmt.Sprint(i))))
		woman := false
		for _, f := range c.First {
			if f == first+":f" {
				woman = true
			}
		}
		if woman != strings.HasSuffix(last, "а") {
			t.Fatalf("%s %s: the surname does not fit the name", first, last)
		}
	}
}

// Swiss texts have no "ß", and the other German-speaking countries keep it where the texts have it.
func TestSwissSpelling(t *testing.T) {
	for _, kind := range kindIDs(t) {
		c := tcase{kind, "ch"}
		if !contains(Countries(), "ch", kind) {
			continue
		}
		for i := 0; i < 10; i++ {
			for _, f := range mustGenerate(t, c, i) {
				if path.Ext(f.Path) == ".html" && bytes.Contains(f.Content, []byte("ß")) {
					t.Fatalf("%s seed %d %s: a ß in a Swiss page", kind, i, f.Path)
				}
			}
		}
	}
}

func contains(cs []CountryInfo, cc, kind string) bool {
	for _, c := range cs {
		if c.Code == cc {
			for _, k := range c.Kinds {
				if k == kind {
					return true
				}
			}
		}
	}
	return false
}

// A kind that has no text in a country's language is refused, not made in another language.
func TestKindNeedsItsLanguage(t *testing.T) {
	if _, err := Generate("blog", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := GenerateWith("blog", []byte("x"), Options{Country: "zz"}); err == nil {
		t.Error("a site was made for a country that does not exist")
	}
	listed := map[string]bool{}
	for _, c := range Countries() {
		for _, k := range c.Kinds {
			listed[c.Code+"/"+k] = true
		}
	}
	for _, k := range Kinds() {
		for _, cc := range k.Countries {
			if !listed[cc+"/"+k.ID] {
				t.Errorf("Kinds lists %s for %s, Countries does not", k.ID, cc)
			}
		}
	}
}
