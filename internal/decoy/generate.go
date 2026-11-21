package decoy

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"
	"unicode"
)

// Generated decoy sites: a kind (a folder of templates under templates/) and a seed give a small, believable static site that
// is different for every seed. A kind is added by dropping a folder: Kinds finds it, Generate renders it, no code changes.
//
// Layout of a kind folder:
//
//	meta.json   names (en, ru), the pages and their possible paths, lists drawn from the vocabulary, number of layouts
//	data.json   the vocabulary: "ui" strings by language and "bundles" (a language plus the texts of one theme)
//	*.tmpl      one template per page, style.css.tmpl and, for a kind with scripts, script.js.tmpl
//
// Templates may use "__name__" for CSS classes and ids and "$$name" for JS identifiers: both are renamed per seed.
// templates/shared holds what every kind uses (head, header, footer, the base stylesheet, the 404 page); a kind may override
// any of those by defining the same template or file name.
//
// A bundle picks the language and the theme; its "vars" (a list, a number range, or one word from each of several lists)
// fill the {name} marks of every text, and {cmd} is the brand as a command line name. The texts that the shared parts print
// must exist in every bundle or its language's "ui": nav_label, tagline, footer, desc, home_title, nf_title, nf_text, nf_back,
// and the "nav" and "desc" keys the pages name. Templates see the site (.L texts, .V vars, .Layout, .Lists, .Nav, .Page) and
// the functions of funcMap. Texts are printed as they are, never escaped: write them so they are safe in a double-quoted
// attribute (no double quotes, no "<").

//go:embed templates
var templateFS embed.FS

// KindInfo describes a kind of generated site for the admin's list.
type KindInfo struct {
	ID     string `json:"id"`
	NameEN string `json:"name_en"`
	NameRU string `json:"name_ru"`
}

type pageDef struct {
	Name  string   `json:"name"`
	Tmpl  string   `json:"tmpl"`
	Paths []string `json:"paths"`
	Nav   string   `json:"nav"`   // key of the menu label; no menu entry when empty
	Title string   `json:"title"` // key of the page title; defaults to Nav
	Desc  string   `json:"desc"`  // key of the meta description; defaults to "desc"
	Each  string   `json:"each"`  // one page per item of this list; the path holds {slug}
}

type subDef struct {
	Field string `json:"field"`
	From  string `json:"from"`
	Min   int    `json:"min"`
	Max   int    `json:"max"`
}

type listDef struct {
	Name   string   `json:"name"`
	From   string   `json:"from"` // vocabulary key of the pool; empty means items made only of the generated fields
	Min    int      `json:"min"`
	Max    int      `json:"max"`
	Dates  *dateDef `json:"dates"`
	Semver bool     `json:"semver"`
	Art    bool     `json:"art"` // every item gets a generated picture file (field "img")
	Sub    []subDef `json:"sub"`
}

type dateDef struct {
	End    string `json:"end"` // newest date, YYYY-MM-DD
	GapMin int    `json:"gap_min"`
	GapMax int    `json:"gap_max"`
}

type metaDef struct {
	Title   map[string]string `json:"title"`
	Layouts int               `json:"layouts"`
	Pages   []pageDef         `json:"pages"`
	Blocks  []string          `json:"home_blocks"`
	Fixed   int               `json:"home_fixed"` // how many leading blocks keep their place
	Lists   []listDef         `json:"lists"`
}

type kindDef struct {
	id      string
	meta    metaDef
	ui      map[string]map[string]any
	bundles []map[string]any
	tpl     *template.Template
	hasJS   bool
}

var loadKinds = sync.OnceValues(func() (map[string]*kindDef, error) {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		return nil, err
	}
	out := map[string]*kindDef{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(templateFS, "templates/"+e.Name()+"/meta.json")
		if err != nil {
			continue // templates/shared has no meta.json: it is not a kind
		}
		d, err := parseKind(e.Name(), raw)
		if err != nil {
			return nil, fmt.Errorf("decoy kind %s: %w", e.Name(), err)
		}
		out[e.Name()] = d
	}
	return out, nil
})

func parseKind(id string, rawMeta []byte) (*kindDef, error) {
	d := &kindDef{id: id}
	if err := json.Unmarshal(rawMeta, &d.meta); err != nil {
		return nil, err
	}
	rawData, err := fs.ReadFile(templateFS, "templates/"+id+"/data.json")
	if err != nil {
		return nil, err
	}
	var data struct {
		UI      map[string]map[string]any `json:"ui"`
		Bundles []map[string]any          `json:"bundles"`
	}
	if err := json.Unmarshal(rawData, &data); err != nil {
		return nil, err
	}
	d.ui, d.bundles = data.UI, data.Bundles
	if len(d.bundles) == 0 || d.meta.Title["en"] == "" || d.meta.Title["ru"] == "" || len(d.meta.Pages) == 0 {
		return nil, errors.New("meta.json or data.json is incomplete")
	}
	if d.meta.Layouts < 1 {
		d.meta.Layouts = 1
	}
	if home := d.meta.Pages[0]; home.Name != "home" || len(home.Paths) != 1 || home.Paths[0] != "index.html" {
		return nil, errors.New(`the first page must be "home" at index.html`)
	}
	t := template.New(id).Funcs(funcMap(nil))
	if t, err = t.ParseFS(templateFS, "templates/shared/*.tmpl"); err != nil {
		return nil, err
	}
	if t, err = t.ParseFS(templateFS, "templates/"+id+"/*.tmpl"); err != nil {
		return nil, err
	}
	d.tpl = t
	d.hasJS = t.Lookup("script.js.tmpl") != nil
	return d, nil
}

// Kinds lists the kinds of generated site, sorted by id.
func Kinds() []KindInfo {
	kinds, err := loadKinds()
	if err != nil {
		return nil
	}
	out := make([]KindInfo, 0, len(kinds))
	for id, d := range kinds {
		out = append(out, KindInfo{ID: id, NameEN: d.meta.Title["en"], NameRU: d.meta.Title["ru"]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Generate makes the files of a site of the given kind. The same kind and seed always give the same files, byte for byte; the
// result is meant for FromFiles.
func Generate(kind string, seed []byte) ([]File, error) {
	kinds, err := loadKinds()
	if err != nil {
		return nil, err
	}
	d := kinds[kind]
	if d == nil {
		return nil, fmt.Errorf("decoy: unknown site kind %q", kind)
	}
	return d.render(seed)
}

type navItem struct {
	Href, Label string
	Cur         bool
}

type page struct {
	Name, Title, Full, Desc, Href, Path string
	Item                                map[string]any
}

// site is what every template sees: the chosen theme's texts, the look of this seed and the lists drawn for it.
type site struct {
	Kind, Lang, Brand, Initial string
	L                          map[string]any
	V                          map[string]string
	Layout, Head, Foot         int
	P                          palette
	S                          style
	Fnt                        fonts
	Year, Since                string
	CSS, JS, Icon, Mark        string
	mark                       string // the favicon's drawing, without the <svg> around it
	Lists                      map[string][]map[string]any
	Order                      []string
	DateFmt                    string
	titleStyle                 int
}

type pageData struct {
	*site
	Page page
	Nav  []navItem
}

type ctx struct {
	r     *rng
	s     *site
	t     *template.Template
	memo  map[string]any
	files map[string][]byte
	artID string
	artN  int
	iconN int
}

var (
	cssNames  = []string{"style.css", "styles.css", "main.css", "site.css", "assets/style.css", "assets/site.css", "css/main.css", "css/style.css", "static/site.css", "assets/css/main.css", "assets/site.{h}.css", "css/app.{h}.css", "static/main.{h}.css"}
	jsNames   = []string{"script.js", "app.js", "main.js", "assets/app.js", "assets/main.js", "js/main.js", "static/app.js", "assets/site.{h}.js", "js/app.{h}.js"}
	iconNames = []string{"favicon.svg", "icon.svg", "assets/icon.svg", "assets/favicon.svg", "img/mark.svg", "static/logo.svg", "images/favicon.svg", "assets/mark-{h}.svg"}
	imgDirs   = []string{"img", "images", "assets/img", "gallery", "media", "static/img", "assets/images", "pictures"}
)

func (d *kindDef) pickBundle(r *rng) map[string]any {
	total := 0
	for _, b := range d.bundles {
		total += weightOf(b)
	}
	roll := r.sub("bundle").n(total)
	for _, b := range d.bundles {
		if roll < weightOf(b) {
			return b
		}
		roll -= weightOf(b)
	}
	return d.bundles[0]
}

func weightOf(b map[string]any) int {
	if w, ok := b["weight"].(float64); ok && w >= 1 {
		return int(w)
	}
	return 1
}

func (d *kindDef) render(seed []byte) ([]File, error) {
	r := newRNG([]byte(d.id), seed)
	c := &ctx{r: r, memo: map[string]any{}, files: map[string][]byte{}, artID: "a" + hex6(r.sub("artid"))}
	s, err := d.newSite(c, r)
	if err != nil {
		return nil, err
	}
	c.s = s
	t, err := d.tpl.Clone()
	if err != nil {
		return nil, err
	}
	c.t = t.Funcs(funcMap(c))

	// Where every page lives, then what it is called.
	type placed struct {
		def  pageDef
		path string
		item map[string]any
	}
	var pages []placed
	for _, pd := range d.meta.Pages {
		pat := pd.Paths[r.sub("path:"+pd.Name).n(len(pd.Paths))]
		if pd.Each == "" {
			pages = append(pages, placed{def: pd, path: pat})
			continue
		}
		seen := map[string]bool{}
		for i, it := range s.Lists[pd.Each] {
			p := strings.NewReplacer("{slug}", fmt.Sprint(it["slug"]), "{n}", fmt.Sprint(i+1)).Replace(pat)
			if seen[p] {
				p = strings.Replace(p, ".html", fmt.Sprintf("-%d.html", i+1), 1)
			}
			seen[p] = true
			it["href"] = hrefOf(p)
			pages = append(pages, placed{def: pd, path: p, item: it})
		}
	}
	var nav []navItem
	for _, p := range pages {
		if p.def.Nav != "" && p.item == nil {
			nav = append(nav, navItem{Href: hrefOf(p.path), Label: str(s.L[p.def.Nav])})
		}
	}

	render := func(name string, pd pageData) (string, error) {
		var b bytes.Buffer
		if err := c.t.ExecuteTemplate(&b, name, pd); err != nil {
			return "", fmt.Errorf("decoy kind %s: %s: %w", d.id, name, err)
		}
		return b.String(), nil
	}
	mk := func(p placed) pageData {
		pg := page{Name: p.def.Name, Path: p.path, Href: hrefOf(p.path), Item: p.item}
		switch {
		case p.item != nil:
			pg.Title = str(p.item["title"])
		case p.def.Title != "":
			pg.Title = str(s.L[p.def.Title])
		default:
			pg.Title = str(s.L[p.def.Nav])
		}
		pg.Desc = str(s.L["desc"])
		if p.def.Desc != "" {
			pg.Desc = str(s.L[p.def.Desc])
		}
		if p.item != nil {
			if v := str(p.item["excerpt"]); v != "" {
				pg.Desc = v
			}
		}
		pg.Full = s.fullTitle(pg.Title)
		n := make([]navItem, len(nav))
		copy(n, nav)
		for i := range n {
			n[i].Cur = n[i].Href == pg.Href
		}
		return pageData{site: s, Page: pg, Nav: n}
	}

	for _, p := range pages {
		pd := mk(p)
		out, err := render(p.def.Tmpl, pd)
		if err != nil {
			return nil, err
		}
		if _, dup := c.files[p.path]; dup {
			return nil, fmt.Errorf("decoy kind %s: two pages at %s", d.id, p.path)
		}
		c.files[p.path] = []byte(out)
	}
	base := pageData{site: s, Page: page{Name: "assets"}}
	css, err := render("style.css.tmpl", base)
	if err != nil {
		return nil, err
	}
	c.files[strings.TrimPrefix(s.CSS, "/")] = []byte(css)
	if d.hasJS {
		js, err := render("script.js.tmpl", base)
		if err != nil {
			return nil, err
		}
		c.files[strings.TrimPrefix(s.JS, "/")] = []byte(js)
	}
	nf := mk(placed{def: pageDef{Name: "404", Title: "nf_title", Desc: "nf_text"}, path: "404.html"})
	out, err := render("404.html.tmpl", nf)
	if err != nil {
		return nil, err
	}
	c.files["404.html"] = []byte(out)
	c.files[strings.TrimPrefix(s.Icon, "/")] = []byte(faviconSVG(s.mark))
	c.files["robots.txt"] = robotsFor(r.sub("robots"), path.Dir(s.CSS))
	return c.finish(), nil
}

// finish renames the "__x__" and "$$x" names, breaks up long runs of prose and returns the files sorted by path.
func (c *ctx) finish() []File {
	paths := make([]string, 0, len(c.files))
	for p := range c.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	nm := newNamer(c.r.sub("names"))
	spr := c.r.sub("shake")
	out := make([]File, 0, len(paths))
	for _, p := range paths {
		body := c.files[p]
		switch ext := path.Ext(p); ext {
		case ".html", ".css", ".js":
			body = nm.rewrite(body)
			if ext == ".html" {
				body = []byte(shake(spr, string(body)))
			} else {
				body = []byte(loosen(spr, string(body)))
			}
		}
		out = append(out, File{Path: p, Content: body})
	}
	return out
}

func (d *kindDef) newSite(c *ctx, r *rng) (*site, error) {
	b := d.pickBundle(r)
	lang := "en"
	if l, ok := b["lang"].(string); ok && l != "" {
		lang = l
	}
	// Variables first: they fill the {name} marks of every text.
	vars := map[string]string{"year": "2026"}
	vs := map[string]any{} // the language's variables, then the bundle's own
	for _, src := range []any{d.ui[lang]["vars"], b["vars"]} {
		m, _ := src.(map[string]any)
		for k, v := range m {
			vs[k] = v
		}
	}
	names := make([]string, 0, len(vs))
	for k := range vs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v, err := drawVar(r.sub("var:"+k), vs[k])
		if err != nil {
			return nil, fmt.Errorf("decoy kind %s: var %s: %w", d.id, k, err)
		}
		vars[k] = v
	}
	for _, k := range names { // a variable may name another one, one level deep: "{city} Chess Club"
		vars[k] = fill(vars[k], vars).(string)
	}
	brand := vars["brand"]
	if brand == "" {
		return nil, fmt.Errorf("decoy kind %s: a bundle has no brand var", d.id)
	}
	if _, ok := vars["cmd"]; !ok { // the name as a command line tool would be typed
		vars["cmd"] = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, strings.ToLower(brand))
	}
	L := map[string]any{}
	for _, src := range []map[string]any{d.ui[lang], b} {
		for k, v := range src {
			if k != "lang" && k != "weight" && k != "vars" {
				L[k] = v
			}
		}
	}
	L = fill(L, vars).(map[string]any)

	look := r.sub("look")
	s := &site{
		Kind: d.id, Lang: lang, Brand: brand, Initial: initialOf(brand), L: L, V: vars,
		Layout: look.sub("layout").n(d.meta.Layouts), Head: look.sub("head").n(3), Foot: look.sub("foot").n(3),
		P: newPalette(look.sub("palette")), S: newStyle(look.sub("style")), Fnt: newFonts(look.sub("fonts")),
		Year: "2026", Since: fmt.Sprint(2015 + look.sub("since").n(10)), titleStyle: look.sub("title").n(4),
		Lists: map[string][]map[string]any{},
	}
	if fm := dateFormats[lang]; len(fm) > 0 {
		s.DateFmt = fm[look.sub("datefmt").n(len(fm))]
	} else {
		s.DateFmt = "{d} {m} {y}"
	}
	fr := r.sub("files")
	s.CSS = "/" + withHash(cssNames[fr.sub("css").n(len(cssNames))], fr.sub("csshash"))
	s.JS = "/" + withHash(jsNames[fr.sub("js").n(len(jsNames))], fr.sub("jshash"))
	s.Icon = "/" + withHash(iconNames[fr.sub("icon").n(len(iconNames))], fr.sub("iconhash"))
	s.mark = faviconMark(look.sub("mark"), s.P, s.Initial)
	s.Mark = inlineMark(s.mark)

	for _, ld := range d.meta.Lists {
		items, err := d.buildList(c, r.sub("list:"+ld.Name), ld, L, s)
		if err != nil {
			return nil, err
		}
		s.Lists[ld.Name] = items
	}
	s.Order = d.order(r.sub("order"))
	return s, nil
}

func (d *kindDef) order(r *rng) []string {
	out := append([]string(nil), d.meta.Blocks...)
	if f := d.meta.Fixed; f < len(out) {
		rest := out[f:]
		p := r.perm(len(rest))
		shuffled := make([]string, len(rest))
		for i, j := range p {
			shuffled[i] = rest[j]
		}
		copy(rest, shuffled)
	}
	return out
}

func withHash(name string, r *rng) string {
	return strings.ReplaceAll(name, "{h}", hex6(r))
}

func hex6(r *rng) string { return fmt.Sprintf("%06x", r.u64()&0xffffff) }

func initialOf(s string) string {
	for _, c := range s {
		if unicode.IsLetter(c) {
			return strings.ToUpper(string(c))
		}
	}
	return "S"
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func hrefOf(p string) string {
	switch {
	case p == "index.html":
		return "/"
	case strings.HasSuffix(p, "/index.html"):
		return "/" + strings.TrimSuffix(p, "index.html")
	}
	return "/" + p
}

func (s *site) fullTitle(t string) string {
	if t == "" || t == s.Brand {
		return s.Brand
	}
	switch s.titleStyle {
	case 0:
		return t + " – " + s.Brand
	case 1:
		return s.Brand + " | " + t
	case 2:
		return t + " · " + s.Brand
	}
	return s.Brand + ": " + t
}

// drawVar picks a variable's value: one of a list, a number in a range, or one word from each of several lists joined together.
func drawVar(r *rng, v any) (string, error) {
	switch x := v.(type) {
	case []any:
		if len(x) == 0 {
			return "", errors.New("empty list")
		}
		return str(x[r.n(len(x))]), nil
	case map[string]any:
		if parts, ok := x["join"].([]any); ok {
			sep := str(x["sep"])
			var out []string
			for i, p := range parts {
				l, _ := p.([]any)
				if len(l) == 0 {
					return "", errors.New("empty join part")
				}
				out = append(out, str(l[r.sub(fmt.Sprint(i)).n(len(l))]))
			}
			return strings.Join(out, sep), nil
		}
		lo, ok1 := x["min"].(float64)
		hi, ok2 := x["max"].(float64)
		if ok1 && ok2 {
			return fmt.Sprint(r.between(int(lo), int(hi))), nil
		}
	}
	return "", errors.New("unsupported variable")
}

var varMark = regexp.MustCompile(`\{([a-z][a-z0-9_]*)\}`)

// fill replaces {name} in every string of a vocabulary with the site's variable of that name.
func fill(v any, vars map[string]string) any {
	switch x := v.(type) {
	case string:
		return varMark.ReplaceAllStringFunc(x, func(m string) string {
			if val, ok := vars[m[1:len(m)-1]]; ok {
				return val
			}
			return m
		})
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fill(e, vars)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = fill(e, vars)
		}
		return out
	}
	return v
}

func (d *kindDef) buildList(c *ctx, r *rng, ld listDef, L map[string]any, s *site) ([]map[string]any, error) {
	var pool []any
	if ld.From != "" {
		pool, _ = L[ld.From].([]any)
		if len(pool) == 0 {
			return nil, fmt.Errorf("decoy kind %s: list %s: no pool %q in the bundle", d.id, ld.Name, ld.From)
		}
	}
	n := r.sub("n").between(ld.Min, ld.Max)
	if pool != nil && n > len(pool) {
		n = len(pool)
	}
	items := make([]map[string]any, n)
	perm := r.sub("perm").perm(max(len(pool), n))
	for i := range items {
		it := map[string]any{}
		if pool != nil {
			switch x := pool[perm[i]].(type) {
			case map[string]any:
				for k, v := range x {
					it[k] = v
				}
			case string:
				it["text"] = x
			}
		}
		items[i] = it
	}
	if ld.Dates != nil {
		end, err := time.Parse("2006-01-02", ld.Dates.End)
		if err != nil {
			return nil, fmt.Errorf("decoy kind %s: list %s: %w", d.id, ld.Name, err)
		}
		dr := r.sub("dates")
		t := end.AddDate(0, 0, -dr.n(ld.Dates.GapMax))
		for _, it := range items {
			it["date_iso"] = t.Format("2006-01-02")
			it["date"] = s.fdate(t.Format("2006-01-02"))
			t = t.AddDate(0, 0, -dr.between(ld.Dates.GapMin, ld.Dates.GapMax))
		}
	}
	if ld.Semver {
		vr := r.sub("semver")
		maj, mnr, pat := vr.n(2), vr.between(1, 6), 0
		vers := make([]string, n)
		for i := range vers {
			switch roll := vr.n(100); {
			case roll < 55:
				pat++
			case roll < 92:
				mnr, pat = mnr+1, 0
			default:
				maj, mnr, pat = maj+1, 0, 0
			}
			vers[i] = fmt.Sprintf("%d.%d.%d", maj, mnr, pat)
		}
		for i, it := range items { // oldest version first was drawn; the list is newest first
			it["version"] = vers[n-1-i]
		}
	}
	for _, sd := range ld.Sub {
		sp, _ := L[sd.From].([]any)
		if len(sp) == 0 {
			return nil, fmt.Errorf("decoy kind %s: list %s: no pool %q for %s", d.id, ld.Name, sd.From, sd.Field)
		}
		sr := r.sub("sub:" + sd.Field)
		order := sr.perm(len(sp))
		next := 0
		for _, it := range items {
			var got []any
			for k, cnt := 0, sr.between(sd.Min, sd.Max); k < cnt; k++ {
				got = append(got, sp[order[next%len(order)]])
				next++
			}
			it[sd.Field] = got
		}
	}
	slugs := map[string]bool{}
	ar := r.sub("art")
	for i, it := range items {
		sl := str(it["slug"])
		if sl == "" {
			sl = slugify(str(it["title"]))
		}
		if sl == "" {
			sl = fmt.Sprint(ld.Name, "-", i+1)
		}
		for base, k := sl, 2; slugs[sl]; k++ {
			sl = fmt.Sprintf("%s-%d", base, k)
		}
		slugs[sl] = true
		it["slug"] = sl
		it["n"] = i + 1
		if ld.Art {
			kind := artKinds[ar.n(len(artKinds))]
			dir := imgDirs[r.sub("imgdir").n(len(imgDirs))]
			p := fmt.Sprintf("%s/%s-%s.svg", dir, sl, hex6(ar))
			it["img"] = "/" + p
			it["art_kind"] = kind
			size := [][2]int{{480, 360}, {360, 480}, {480, 480}, {480, 320}, {400, 500}}[ar.n(5)]
			it["w"], it["h"] = size[0], size[1]
			c.files[p] = []byte(artSVG(ar, kind, s.P.Hue+float64(ar.n(120))-60, size[0], size[1], "g"+hex6(ar), true))
		}
	}
	return items, nil
}

var slugBad = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "ß", "ss", "å", "a", "é", "e", "è", "e").Replace(strings.ToLower(s))
	return strings.Trim(slugBad.ReplaceAllString(s, "-"), "-")
}

var monthNames = map[string][12]string{
	"en": {"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"},
	"de": {"Januar", "Februar", "März", "April", "Mai", "Juni", "Juli", "August", "September", "Oktober", "November", "Dezember"},
	"nl": {"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"},
	"sv": {"januari", "februari", "mars", "april", "maj", "juni", "juli", "augusti", "september", "oktober", "november", "december"},
}

var dateFormats = map[string][]string{
	"en": {"{d} {m} {y}", "{m} {d}, {y}"}, "de": {"{d}. {m} {y}"}, "nl": {"{d} {m} {y}"}, "sv": {"{d} {m} {y}"},
}

func (s *site) fdate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	months, ok := monthNames[s.Lang]
	if !ok {
		months = monthNames["en"]
	}
	return strings.NewReplacer("{d}", fmt.Sprint(t.Day()), "{m}", months[t.Month()-1], "{y}", fmt.Sprint(t.Year())).Replace(s.DateFmt)
}

func funcMap(c *ctx) template.FuncMap {
	key := func(k string) *rng { return c.r.sub("k:" + k) }
	return template.FuncMap{
		"one": func(k string, l []any) any {
			if v, ok := c.memo["one:"+k]; ok {
				return v
			}
			v := l[key(k).n(len(l))]
			c.memo["one:"+k] = v
			return v
		},
		"pick": func(k string, l []any, n int) []any {
			if v, ok := c.memo["pick:"+k]; ok {
				return v.([]any)
			}
			n = min(n, len(l))
			out := make([]any, n)
			for i, j := range key(k).perm(len(l))[:n] {
				out[i] = l[j]
			}
			c.memo["pick:"+k] = out
			return out
		},
		"num":    func(k string, lo, hi int) int { return key(k).between(lo, hi) },
		"chance": func(k string, pct int) bool { return key(k).chance(pct) },
		// pct is a percentage from lo/100 to hi/100 with two decimals: pct "k" 9950 9999 gives 99.50 to 99.99.
		"pct": func(k string, lo, hi int) string {
			v := key(k).between(lo, hi)
			return fmt.Sprintf("%d.%02d", v/100, v%100)
		},
		// rolls is n days: 1 a bad one (warn per mille), 2 a very bad one (bad per mille), 3 to 5 a good one in three shades.
		"rolls": func(k string, n, warn, bad int) []int {
			r := key(k)
			out := make([]int, n)
			for i := range out {
				switch v := r.n(1000); {
				case v < bad:
					out[i] = 2
				case v < bad+warn:
					out[i] = 1
				default:
					out[i] = 3 + r.n(3)
				}
			}
			return out
		},
		"seq": func(n int) []int {
			out := make([]int, n)
			for i := range out {
				out[i] = i
			}
			return out
		},
		"add":   func(a, b int) int { return a + b },
		"sub":   func(a, b int) int { return a - b },
		"mul":   func(a, b int) int { return a * b },
		"lower": strings.ToLower,
		"icon":  func(name string) string { c.iconN++; return iconSVG(name, c.r.sub(fmt.Sprint("icon", c.iconN))) },
		"fdate": func(iso string) string { return c.s.fdate(iso) },
		"art": func(k, kind string, w, h int) string {
			r := key("art:" + k)
			if kind == "" {
				kind = artKinds[r.n(len(artKinds))]
			}
			c.artN++
			return artSVG(r, kind, c.s.P.Hue+float64(r.n(100))-50, w, h, fmt.Sprintf("%s%d", c.artID, c.artN), false)
		},
		"blk": func(name string, data any) (string, error) {
			var b bytes.Buffer
			err := c.t.ExecuteTemplate(&b, "b_"+name, data)
			return b.String(), err
		},
	}
}
