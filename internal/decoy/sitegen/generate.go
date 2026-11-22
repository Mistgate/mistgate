package sitegen

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/mistgate/mistgate/internal/decoy"
)

// Generated decoy sites: a kind (a folder of templates under templates/) and a seed give a small, believable static site that
// is different for every seed. A kind is added by dropping a folder: Kinds finds it, Generate renders it, no code changes.
//
// A site is made for a country (cc) and written in the language of that country (lang). The two are different files, so that a
// new language or a new country is new data and no code:
//
//	templates/shared/country.<cc>.json   the country: its language, date and clock, money, address and postcode format, phone
//	                                     ranges, cities, streets, names (see countryDef)
//	templates/shared/text.<lang>.json    the words every kind prints: menu, months, weekdays, 404, the contact form, the legal pages
//	templates/<kind>/meta.json           names (en, ru), the pages and their possible paths, lists drawn from the texts, layouts
//	templates/<kind>/data.json           what does not depend on the language: "ui" (kind-wide), "bundles" (the variants: id, weight,
//	                                     English search words of the photos, "fixed" values such as icons, prices and code samples)
//	templates/<kind>/text.<lang>.json    the texts of the kind in one language: "ui" (kind-wide) and "bundles" by variant id
//	templates/<kind>/*.tmpl              one template per page, style.css.tmpl and, for a kind with scripts, script.js.tmpl
//
// A kind is available for a country when it has a text file for the country's language; a language may write only some of the
// kind's variants. Every variant that a language writes has the same keys as in every other language (a test keeps that). A text
// file is merged over the fixed values: a list of objects in both is joined item by item.
//
// Templates may use "__name__" for CSS classes and ids and "$$name" for JS identifiers: both are renamed per seed.
// templates/shared holds what every kind uses (head, header, footer, the base stylesheet, the 404 page, the contact form); a kind
// may override any of those by defining the same template or file name. No word is written in a template: every word is a text.
//
// A variant's "vars" (a list, a number range, or one word from each of several lists) fill the {name} marks of every text. The
// country adds {city}, {city_in}, {state}, {street}, {no}, {zip}, {address}, {phone} (empty where the country has no range of
// numbers made up for stories), {first}, {last} and {name}; {cmd} is the brand as a command line name. The texts that the shared
// parts print must exist in the shared text or in the kind's: nav_label, tagline, footer, desc, home_title, nf_title, nf_text,
// nf_back, and the "nav" and "desc" keys the pages name. Templates see the site (.L texts, .V vars, .Layout, .Lists, .Nav, .Page)
// and the functions of funcMap. Texts are printed as they are, never escaped: write them so they are safe in a double-quoted
// attribute (no double quotes, no "<").
//
// Photos (generate_photo.go) are optional. meta.json's "photo_slots" names the places that may hold one: a slot is a name with a
// number ("t3"), its group is the name without the number, and the group maps to indexes into the search words. The words are
// "photo_q" in a bundle (or in meta.json for every bundle that has none): at most three, plain things and places, never people.
// A template writes {{photo "slot" "" W H}} where it used to write {{art ...}}: it prints the photo when the caller has one and
// the same drawing as before when it has not. {{hasphoto "slot" W H}} guards markup that only makes sense around a photo and
// {{nphotos}} counts them, for the stylesheet. A list with "photo_max" is cut to that many items when the site has photos.
//
// The template language is the small subset of Go's text/template that generate_tmpl.go interprets (text/template itself is
// not used: it would keep every method of the program in the edge builds): fields, variables, if / else if / else, range,
// with, define and template, parentheses and the functions, trim markers and comments, but no pipes. A field or function that
// is not there is an error, and a test renders every kind from 200 seeds to find it.

// File is a file of a generated site, the type FromFiles takes.
type File = decoy.File

//go:embed templates
var templateFS embed.FS

// ErrUnknownKind is Generate's refusal of a kind that has no folder of templates.
var ErrUnknownKind = errors.New("decoy: unknown site kind")

// KindInfo describes a kind of generated site for the admin's list.
type KindInfo struct {
	ID     string `json:"id"`
	NameEN string `json:"name_en"`
	NameRU string `json:"name_ru"`
	// Countries are the codes of the countries the kind can be made for (it has texts in their language), in the order of Countries().
	Countries []string `json:"countries"`
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
	From   string   `json:"from"` // text key of the pool; empty means items made only of the generated fields
	Min    int      `json:"min"`
	Max    int      `json:"max"`
	Dates  *dateDef `json:"dates"`
	Semver bool     `json:"semver"`
	Art    bool     `json:"art"` // every item gets a generated picture file (field "img"), or a photo when the site has them
	// PhotoMax caps the list at this many items when the site has photos, so that each one can have its own.
	PhotoMax int      `json:"photo_max"`
	Sub      []subDef `json:"sub"`
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
	// PhotoSlots names the places that may hold a photo: a slot's name without its trailing number, then the indexes into the
	// search words (PhotoQ) its numbered slots take in turn. A site has photos only where this and the words both exist.
	PhotoSlots map[string][]int `json:"photo_slots"`
	PhotoQ     []string         `json:"photo_q"` // the search words of every variant that has none of its own
	// Style is the kind's look: its palettes, font sets and the ranges of its shapes (generate_style.go). A kind without one gets
	// the old random look.
	Style *styleDef `json:"style"`
}

// bundleDef is a variant of a kind, the part of it that does not depend on the language.
type bundleDef struct {
	ID     string   `json:"id"`
	Weight int      `json:"weight"`
	PhotoQ []string `json:"photo_q"` // English: the photo search is English whatever the site's language
	// Art is the subject of the pictures of this variant (one of the kinds artSVG draws), for {{art "k" "" W H}} and for the
	// pictures of lists; without one the pictures are drawn in the old four subjects at random.
	Art string `json:"art"`
	// Arts are the subjects the pictures of a list take in turn, from a start of the site's choosing, so that the works of one
	// bundle are not seven of one picture; Art is still the subject of {{art}} in the templates.
	Arts  []string       `json:"arts"`
	Fixed map[string]any `json:"fixed"` // merged under the texts of the variant
}

// langText is the texts of a kind in one language.
type langText struct {
	UI      map[string]any            `json:"ui"`
	Bundles map[string]map[string]any `json:"bundles"`
}

type kindDef struct {
	id      string
	meta    metaDef
	fixedUI map[string]any
	bundles []bundleDef
	texts   map[string]*langText // by language
	tpl     *tmplSet
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
	d := &kindDef{id: id, texts: map[string]*langText{}}
	if err := json.Unmarshal(rawMeta, &d.meta); err != nil {
		return nil, err
	}
	rawData, err := fs.ReadFile(templateFS, "templates/"+id+"/data.json")
	if err != nil {
		return nil, err
	}
	var data struct {
		UI      map[string]any `json:"ui"`
		Bundles []bundleDef    `json:"bundles"`
	}
	if err := json.Unmarshal(rawData, &data); err != nil {
		return nil, fmt.Errorf("data.json: %w", err)
	}
	d.fixedUI, d.bundles = data.UI, data.Bundles
	if len(d.bundles) == 0 || d.meta.Title["en"] == "" || d.meta.Title["ru"] == "" || len(d.meta.Pages) == 0 {
		return nil, errors.New("meta.json or data.json is incomplete")
	}
	ids := map[string]bool{}
	for i := range d.bundles {
		b := &d.bundles[i]
		if b.ID == "" || ids[b.ID] {
			return nil, fmt.Errorf("data.json: variant %d has no id or a repeated one", i)
		}
		ids[b.ID] = true
		if b.Weight < 1 {
			b.Weight = 1
		}
	}
	if d.meta.Layouts < 1 {
		d.meta.Layouts = 1
	}
	if sd := d.meta.Style; sd != nil {
		if err := sd.build(d.meta.Layouts); err != nil {
			return nil, fmt.Errorf("meta.json style: %w", err)
		}
	}
	for _, b := range d.bundles {
		if b.Art != "" && !slices.Contains(allArtKinds, b.Art) {
			return nil, fmt.Errorf("data.json: variant %s has the picture subject %q, which is not drawn", b.ID, b.Art)
		}
		for _, k := range b.Arts {
			if !slices.Contains(allArtKinds, k) {
				return nil, fmt.Errorf("data.json: variant %s has the picture subject %q, which is not drawn", b.ID, k)
			}
		}
	}
	if home := d.meta.Pages[0]; home.Name != "home" || len(home.Paths) != 1 || home.Paths[0] != "index.html" {
		return nil, errors.New(`the first page must be "home" at index.html`)
	}
	files, err := fs.ReadDir(templateFS, "templates/"+id)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		lang, ok := textLang(f.Name())
		if !ok {
			continue
		}
		raw, err := fs.ReadFile(templateFS, "templates/"+id+"/"+f.Name())
		if err != nil {
			return nil, err
		}
		lt := &langText{}
		if err := json.Unmarshal(raw, lt); err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name(), err)
		}
		for bid := range lt.Bundles {
			if !ids[bid] {
				return nil, fmt.Errorf("%s: variant %q is not in data.json", f.Name(), bid)
			}
		}
		d.texts[lang] = lt
	}
	t := newTmplSet(funcMap(nil))
	for _, dir := range []string{"shared", id} { // a kind may replace a template of the shared part
		files, err := fs.ReadDir(templateFS, "templates/"+dir)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			if !strings.HasSuffix(f.Name(), ".tmpl") {
				continue
			}
			text, err := fs.ReadFile(templateFS, "templates/"+dir+"/"+f.Name())
			if err != nil {
				return nil, err
			}
			if err := t.add(f.Name(), string(text)); err != nil {
				return nil, err
			}
		}
	}
	d.tpl = t
	d.hasJS = t.has("script.js.tmpl")
	return d, nil
}

// textLang is the language of a file called text.<lang>.json.
func textLang(name string) (string, bool) {
	lang, ok := strings.CutPrefix(name, "text.")
	if !ok {
		return "", false
	}
	lang, ok = strings.CutSuffix(lang, ".json")
	return lang, ok && lang != ""
}

// Kinds lists the kinds of generated site, sorted by id.
func Kinds() []KindInfo {
	kinds, err := loadKinds()
	if err != nil {
		return nil
	}
	byKind := map[string][]string{}
	for _, c := range Countries() {
		for _, k := range c.Kinds {
			byKind[k] = append(byKind[k], c.Code)
		}
	}
	out := make([]KindInfo, 0, len(kinds))
	for id, d := range kinds {
		out = append(out, KindInfo{ID: id, NameEN: d.meta.Title["en"], NameRU: d.meta.Title["ru"], Countries: byKind[id]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Generate makes the files of a site of the given kind for DefaultCountry. The same kind and seed always give the same files,
// byte for byte; the result is meant for FromFiles.
func Generate(kind string, seed []byte) ([]File, error) {
	files, _, err := GenerateNamed(kind, seed)
	return files, err
}

// GenerateNamed is Generate that also returns the brand the pages print (the site's own name, for naming it in the library).
func GenerateNamed(kind string, seed []byte) ([]File, string, error) {
	return GenerateWith(kind, seed, Options{})
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
	Scheme                     string   // light, dark or both
	Theme                      string   // the colour of the browser's own bars
	Art                        string   // the subject of the variant's pictures, "" for the old random four
	Arts                       []string // the subjects the pictures of a list take in turn (see bundleDef.Arts)
	S                          style
	Fnt                        fonts
	Year, Since                string
	CSS, JS, Icon, Mark        string
	mark                       string // the favicon's drawing, without the <svg> around it
	Lists                      map[string][]map[string]any
	Order                      []string
	DateFmt                    string
	titleStyle                 int
	photoQ                     []string
	photoSlots                 map[string][]int
	country                    *countryDef
}

// data is what a template sees as dot: the site and the page, as maps, which is all the interpreter reads.
func (s *site) data() map[string]any {
	vars := make(map[string]any, len(s.V))
	for k, v := range s.V {
		vars[k] = v
	}
	lists := make(map[string]any, len(s.Lists))
	for k, items := range s.Lists {
		l := make([]any, len(items))
		for i, it := range items {
			l[i] = it
		}
		lists[k] = l
	}
	order := make([]any, len(s.Order))
	for i, o := range s.Order {
		order[i] = o
	}
	return map[string]any{
		"Kind": s.Kind, "Lang": s.Lang, "Brand": s.Brand, "Initial": s.Initial, "L": s.L, "V": vars, "Layout": s.Layout, "Head": s.Head,
		"Foot": s.Foot, "P": map[string]any{"L": s.P.L.data(), "D": s.P.D.data()}, "Pm": s.P.prim(s.Scheme).data(), "Scheme": s.Scheme,
		"ColorScheme": colorScheme(s.Scheme), "Theme": s.Theme, "Art": s.Art, "S": s.S.data(), "Fnt": s.Fnt.data(), "Year": s.Year,
		"Since": s.Since, "CSS": s.CSS, "JS": s.JS, "Icon": s.Icon, "Mark": s.Mark, "Lists": lists, "Order": order, "DateFmt": s.DateFmt,
	}
}

// pageData is the site with one page of it and the menu as it looks on that page.
func pageData(base map[string]any, pg page, nav []navItem) map[string]any {
	d := maps.Clone(base)
	var item any
	if pg.Item != nil {
		item = pg.Item
	}
	d["Page"] = map[string]any{"Name": pg.Name, "Title": pg.Title, "Full": pg.Full, "Desc": pg.Desc, "Href": pg.Href, "Path": pg.Path, "Item": item}
	n := make([]any, len(nav))
	for i, it := range nav {
		n[i] = map[string]any{"Href": it.Href, "Label": it.Label, "Cur": it.Cur}
	}
	d["Nav"] = n
	return d
}

type ctx struct {
	ph    *photos
	r     *rng
	s     *site
	t     *tmplSet
	fns   map[string]tmplFunc
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

// pickBundle draws a variant among those written in the language.
func (d *kindDef) pickBundle(r *rng, lt *langText) bundleDef {
	var have []bundleDef
	total := 0
	for _, b := range d.bundles {
		if _, ok := lt.Bundles[b.ID]; ok {
			have = append(have, b)
			total += b.Weight
		}
	}
	roll := r.sub("bundle").n(total)
	for _, b := range have {
		if roll < b.Weight {
			return b
		}
		roll -= b.Weight
	}
	return have[0]
}

func (d *kindDef) render(seed []byte, cc string, ph *photos) ([]File, string, error) {
	loc, err := loadLocales()
	if err != nil {
		return nil, "", err
	}
	country, err := loc.country(cc)
	if err != nil {
		return nil, "", err
	}
	if !d.available(country.Lang) {
		return nil, "", fmt.Errorf("%w: %s in %s", ErrNotInCountry, d.id, country.Code)
	}
	r := newRNG([]byte(d.id), seed)
	c := &ctx{r: r, ph: ph, memo: map[string]any{}, files: map[string][]byte{}, artID: "a" + hex6(r.sub("artid"))}
	s, err := d.newSite(c, r, country, loc.shared[country.Lang])
	if err != nil {
		return nil, "", err
	}
	c.s = s
	c.t, c.fns = d.tpl, funcMap(c)
	base := s.data()

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

	render := func(name string, data map[string]any) (string, error) {
		var b bytes.Buffer
		if err := c.t.exec(&b, name, data, c.fns); err != nil {
			return "", fmt.Errorf("decoy kind %s: %s: %w", d.id, name, err)
		}
		return b.String(), nil
	}
	mk := func(p placed) map[string]any {
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
		return pageData(base, pg, n)
	}

	for _, p := range pages {
		pd := mk(p)
		out, err := render(p.def.Tmpl, pd)
		if err != nil {
			return nil, "", err
		}
		if _, dup := c.files[p.path]; dup {
			return nil, "", fmt.Errorf("decoy kind %s: two pages at %s", d.id, p.path)
		}
		c.files[p.path] = []byte(out)
	}
	assets := pageData(base, page{Name: "assets"}, nil)
	css, err := render("style.css.tmpl", assets)
	if err != nil {
		return nil, "", err
	}
	c.files[strings.TrimPrefix(s.CSS, "/")] = []byte(css)
	if d.hasJS && c.referenced(s.JS) { // a kind whose script serves a block that this site has no use for gets no script file
		js, err := render("script.js.tmpl", assets)
		if err != nil {
			return nil, "", err
		}
		c.files[strings.TrimPrefix(s.JS, "/")] = []byte(js)
	}
	nf := mk(placed{def: pageDef{Name: "404", Title: "nf_title", Desc: "nf_text"}, path: "404.html"})
	out, err := render("404.html.tmpl", nf)
	if err != nil {
		return nil, "", err
	}
	c.files["404.html"] = []byte(out)
	c.files[strings.TrimPrefix(s.Icon, "/")] = []byte(faviconSVG(s.mark))
	c.files["robots.txt"] = robotsFor(r.sub("robots"), path.Dir(s.CSS))
	return c.finish(), s.Brand, nil
}

// referenced is whether a page of the site loads the file at this address.
func (c *ctx) referenced(addr string) bool {
	for p, body := range c.files {
		if strings.HasSuffix(p, ".html") && bytes.Contains(body, []byte(`"`+addr+`"`)) {
			return true
		}
	}
	return false
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

func (d *kindDef) newSite(c *ctx, r *rng, country *countryDef, shared map[string]any) (*site, error) {
	lang := country.Lang
	lt := d.texts[lang]
	b := d.pickBundle(r, lt)
	// The layers of the texts, the last one wins: the language's shared words, the kind's fixed and written words, the variant's.
	merged := map[string]any{}
	layers := []map[string]any{shared, d.fixedUI, lt.UI, b.Fixed, lt.Bundles[b.ID]}
	for i, l := range layers {
		if err := mergeText(merged, l); err != nil {
			return nil, fmt.Errorf("decoy kind %s: variant %s, layer %d: %w", d.id, b.ID, i, err)
		}
	}
	// Variables first: they fill the {name} marks of every text. The country's come first, a text may name its own.
	vars := map[string]string{"year": "2026", "cc": strings.ToUpper(country.Code)}
	for k, v := range country.geo(r.sub("geo")) {
		vars[k] = v
	}
	vs, _ := merged["vars"].(map[string]any)
	names := make([]string, 0, len(vs))
	for k := range vs {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v, err := drawVar(r.sub("var:"+k), vs[k], country)
		if err != nil {
			return nil, fmt.Errorf("decoy kind %s: var %s: %w", d.id, k, err)
		}
		vars[k] = v
	}
	for _, k := range names { // a variable may name another one, one level deep: "{city} Chess Club"
		vars[k] = fill(vars[k], vars).(string)
	}
	if country.rep != nil {
		for k, v := range vars {
			vars[k] = country.rep.Replace(v)
		}
	}
	brand := vars["brand"]
	if brand == "" {
		return nil, fmt.Errorf("decoy kind %s: a variant has no brand var", d.id)
	}
	if _, ok := vars["cmd"]; !ok { // the name as a command line tool would be typed
		vars["cmd"] = strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				return r
			}
			return -1
		}, strings.ToLower(brand))
	}
	delete(merged, "vars")
	L := fill(replaceText(country.rep, merged), vars).(map[string]any)
	L = fillTimes(L, country, str(L["am"]), str(L["pm"])).(map[string]any)
	if left := leftoverVar(L); left != "" {
		return nil, fmt.Errorf("decoy kind %s: variant %s: the text %q has a variable that is not defined", d.id, b.ID, left)
	}

	look := r.sub("look")
	s := &site{
		Kind: d.id, Lang: country.HTML, Brand: brand, Initial: initialOf(brand), L: L, V: vars, country: country,
		Layout: look.sub("layout").n(d.meta.Layouts), Head: look.sub("head").n(3), Foot: look.sub("foot").n(3),
		Scheme: "both", Art: b.Art, Arts: b.Arts,
		Year: "2026", Since: fmt.Sprint(2015 + look.sub("since").n(10)), titleStyle: look.sub("title").n(4),
		Lists: map[string][]map[string]any{}, photoSlots: d.meta.PhotoSlots,
	}
	if sd := d.meta.Style; sd != nil && !sd.Legacy {
		s.Scheme = sd.Scheme
		s.P = sd.pal[look.sub("pal").n(len(sd.pal))]
		s.S = sd.shape(look.sub("shape"))
		s.Fnt = sd.fonts(look.sub("fnt"), hasCyrillic(country.Lang))
		s.Theme = s.P.prim(s.Scheme).BG
	} else {
		s.P, s.S, s.Fnt = newPalette(look.sub("palette")), legacyStyle(newStyle(look.sub("style"))), legacyFonts(newFonts(look.sub("fonts")))
		s.Theme = s.P.L.Acc
	}
	s.photoQ = d.photoWords(b)
	c.s = s // the lists below may ask for photos
	s.DateFmt = country.Date[look.sub("datefmt").n(len(country.Date))]
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

var timeMark = regexp.MustCompile(`\{(\d{1,2}:\d{2})\}`)

// fillTimes writes the times of the texts the way the country does: "{19:30}" becomes "19:30" or "7:30 pm".
func fillTimes(v any, c *countryDef, am, pm string) any {
	switch x := v.(type) {
	case string:
		return timeMark.ReplaceAllStringFunc(x, func(m string) string { return c.clock(m[1:len(m)-1], am, pm) })
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = fillTimes(e, c, am, pm)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = fillTimes(e, c, am, pm)
		}
		return out
	}
	return v
}

// leftoverVar is a text with a {name} mark that no variable filled, "" when there is none.
func leftoverVar(v any) string {
	switch x := v.(type) {
	case string:
		if varMark.MatchString(x) {
			return x
		}
	case []any:
		for _, e := range x {
			if s := leftoverVar(e); s != "" {
				return s
			}
		}
	case map[string]any:
		for _, e := range x {
			if s := leftoverVar(e); s != "" {
				return s
			}
		}
	}
	return ""
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
func drawVar(r *rng, v any, c *countryDef) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case []any:
		if len(x) == 0 {
			return "", errors.New("empty list")
		}
		return str(x[r.n(len(x))]), nil
	case map[string]any:
		if prices, ok := x["money"].([]any); ok && len(prices) > 0 { // one of several prices, in the country's money
			p, ok := number(prices[r.n(len(prices))])
			if !ok {
				return "", errors.New("a price is not a number")
			}
			return c.money(p), nil
		}
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
	if pool != nil && len(pool) < ld.Min {
		return nil, fmt.Errorf("decoy kind %s: list %s: the pool %q has %d items, %d are the least", d.id, ld.Name, ld.From, len(pool), ld.Min)
	}
	if pool != nil && n > len(pool) {
		n = len(pool)
	}
	if ld.PhotoMax > 0 && c.ph != nil && len(s.photoQ) > 0 {
		n = min(n, ld.PhotoMax)
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
			it["month"] = t.Format("2006-01")
			it["month_label"] = s.fmonth(t)
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
			it["title"] = vers[n-1-i]
			it["slug"] = "v" + strings.ReplaceAll(vers[n-1-i], ".", "-")
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
	artStart := r.sub("artstart").n(1 << 16)
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
			if s.Art != "" {
				kind = s.Art
			}
			if len(s.Arts) > 0 {
				kind = s.Arts[(i+artStart)%len(s.Arts)]
			}
			dir := imgDirs[r.sub("imgdir").n(len(imgDirs))]
			p := fmt.Sprintf("%s/%s-%s.svg", dir, sl, hex6(ar))
			it["img"] = "/" + p
			it["art_kind"] = kind
			size := [][2]int{{480, 360}, {360, 480}, {480, 480}, {480, 320}, {400, 500}}[ar.n(5)]
			it["w"], it["h"] = size[0], size[1]
			svg := []byte(c.tone(artSVG(ar, kind, s.P.Hue+float64(ar.n(120))-60, size[0], size[1], "g"+hex6(ar), true), kind))
			if pp, ok := c.photoFile(fmt.Sprintf("w%d", i+1), 4, 3, dir, true); ok { // a photo takes the picture's place
				it["img"], it["w"], it["h"] = pp, 640, 480
			} else {
				c.files[p] = svg
			}
		}
	}
	return items, nil
}

// artKind is the subject of a picture: the one the template names, else the variant's own, else one of the old four at random.
func (c *ctx) artKind(r *rng, kind string) string {
	switch {
	case kind != "":
		return kind
	case c.s.Art != "":
		return c.s.Art
	}
	return artKinds[r.n(len(artKinds))]
}

var slugBad = regexp.MustCompile(`[^a-z0-9]+`)

// translit is how slugify writes the letters that a web address does not take.
var translit = strings.NewReplacer("ä", "a", "ö", "o", "ü", "u", "ß", "ss", "å", "a", "é", "e", "è", "e", "ê", "e", "à", "a", "ç", "c", "ñ", "n",
	"ą", "a", "ć", "c", "ę", "e", "ł", "l", "ń", "n", "ó", "o", "ś", "s", "ź", "z", "ż", "z",
	"â", "a", "î", "i", "ï", "i", "ô", "o", "û", "u", "ù", "u", "ÿ", "y", "œ", "oe", "æ", "ae", "ë", "e", "á", "a", "í", "i", "ú", "u",
	"ı", "i", "̇", "", "ğ", "g", "ş", "s", "õ", "o",
	"ā", "a", "č", "c", "ē", "e", "ģ", "g", "ī", "i", "ķ", "k", "ļ", "l", "ņ", "n", "š", "s", "ū", "u", "ž", "z", "ė", "e", "į", "i", "ų", "u",
	"а", "a", "б", "b", "в", "v", "г", "g", "д", "d", "е", "e", "ё", "e", "ж", "zh", "з", "z", "и", "i", "й", "y", "к", "k", "л", "l",
	"м", "m", "н", "n", "о", "o", "п", "p", "р", "r", "с", "s", "т", "t", "у", "u", "ф", "f", "х", "kh", "ц", "ts", "ч", "ch", "ш", "sh",
	"щ", "shch", "ъ", "", "ы", "y", "ь", "", "э", "e", "ю", "yu", "я", "ya")

func slugify(s string) string {
	s = translit.Replace(strings.ToLower(s))
	return strings.Trim(slugBad.ReplaceAllString(s, "-"), "-")
}

// textList is a list of words of the site's language (months, weekdays) or nil.
func (s *site) textList(key string) []any {
	l, _ := s.L[key].([]any)
	return l
}

func nameAt(l []any, i int) string {
	if i < 0 || i >= len(l) {
		return ""
	}
	return str(l[i])
}

// fdate writes a date the way the site's country does, with the months and days of its language.
func (s *site) fdate(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return iso
	}
	months := s.textList("months")
	of := s.textList("months_gen") // the month of a date, where the language bends it
	if of == nil {
		of = months
	}
	wd := nameAt(s.textList("weekdays"), (int(t.Weekday())+6)%7)
	return strings.NewReplacer("{dd}", fmt.Sprintf("%02d", t.Day()), "{mm}", fmt.Sprintf("%02d", int(t.Month())), "{d}", fmt.Sprint(t.Day()),
		"{mn}", nameAt(months, int(t.Month())-1), "{m}", nameAt(of, int(t.Month())-1), "{y}", fmt.Sprint(t.Year()), "{wd}", wd).Replace(s.DateFmt)
}

// fmonth is a month and a year, "October 2026".
func (s *site) fmonth(t time.Time) string {
	return nameAt(s.textList("months"), int(t.Month())-1) + " " + fmt.Sprint(t.Year())
}

// The functions of the templates, other than the builtins of the interpreter. They are keyed draws: the same key in two places
// gives the same answer, whichever page is made first. funcMap(nil) is only the list of names.
func funcMap(c *ctx) map[string]tmplFunc {
	key := func(k string) *rng { return c.r.sub("k:" + k) }
	fm := map[string]tmplFunc{
		"one": func(a []any) any {
			k, l := argStr(a, 0), argList(a, 1)
			if v, ok := c.memo["one:"+k]; ok {
				return v
			}
			v := l[key(k).n(len(l))]
			c.memo["one:"+k] = v
			return v
		},
		"pick": func(a []any) any {
			k, l, n := argStr(a, 0), argList(a, 1), argInt(a, 2)
			if v, ok := c.memo["pick:"+k]; ok {
				return v
			}
			n = min(n, len(l))
			out := make([]any, n)
			for i, j := range key(k).perm(len(l))[:n] {
				out[i] = l[j]
			}
			c.memo["pick:"+k] = out
			return out
		},
		"num":    func(a []any) any { return key(argStr(a, 0)).between(argInt(a, 1), argInt(a, 2)) },
		"chance": func(a []any) any { return key(argStr(a, 0)).chance(argInt(a, 1)) },
		// hexs is n hex digits, such as a checksum.
		"hexs": func(a []any) any {
			r, n := key(argStr(a, 0)), argInt(a, 1)
			var b strings.Builder
			for b.Len() < n {
				fmt.Fprintf(&b, "%016x", r.u64())
			}
			return b.String()[:n]
		},
		// pct is a percentage from lo/100 to hi/100 with two decimals: pct "k" 9950 9999 gives 99.50 to 99.99.
		"pct": func(a []any) any {
			v := key(argStr(a, 0)).between(argInt(a, 1), argInt(a, 2))
			sep := "," // ponytail: every language here but English writes a decimal comma; a per-country field when one differs
			if strings.HasPrefix(c.s.country.Lang, "en") {
				sep = "."
			}
			return fmt.Sprintf("%d%s%02d", v/100, sep, v%100)
		},
		// rolls is n days: 1 a bad one (warn per mille), 2 a very bad one (bad per mille), 3 to 5 a good one in three shades.
		"rolls": func(a []any) any {
			r, n, warn, bad := key(argStr(a, 0)), argInt(a, 1), argInt(a, 2), argInt(a, 3)
			out := make([]any, n)
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
		"seq": func(a []any) any {
			out := make([]any, argInt(a, 0))
			for i := range out {
				out[i] = i
			}
			return out
		},
		"add":   func(a []any) any { return argInt(a, 0) + argInt(a, 1) },
		"sub":   func(a []any) any { return argInt(a, 0) - argInt(a, 1) },
		"mul":   func(a []any) any { return argInt(a, 0) * argInt(a, 1) },
		"lower": func(a []any) any { return strings.ToLower(argStr(a, 0)) },
		"icon": func(a []any) any {
			c.iconN++
			return iconSVG(argStr(a, 0), c.r.sub(fmt.Sprint("icon", c.iconN)))
		},
		"fdate": func(a []any) any { return c.s.fdate(argStr(a, 0)) },
		// money is a price of the kind's data in the money of the site's country.
		"money": func(a []any) any {
			if len(a) != 1 {
				fail("money takes a price")
			}
			v, ok := number(a[0])
			if !ok {
				fail("money takes a number, not %T", a[0])
			}
			return c.s.country.money(v)
		},
		// price is money for a price list: sums under 30 keep their pence or cents ("£4.80"), bigger ones are rounded as money does.
		"price": func(a []any) any {
			if len(a) != 1 {
				fail("price takes a price")
			}
			v, ok := number(a[0])
			if !ok {
				fail("price takes a number, not %T", a[0])
			}
			return c.s.country.price(v)
		},
		// hrs is a line of opening hours: the days from..to (1 is Monday), then the hours as "08:00" "18:00", or "" for a closed day.
		"hrs": func(a []any) any {
			days, when := c.hoursParts("hrs", a)
			f := str(c.s.L["hrs_fmt"])
			if f == "" {
				f = "%days%: %time%"
			}
			return strings.NewReplacer("%days%", days, "%time%", when).Replace(f)
		},
		// hrsp is the same line in two parts, {days, time}, for a layout with the days on the left and the hours on the right.
		"hrsp": func(a []any) any {
			days, when := c.hoursParts("hrsp", a)
			return map[string]any{"days": days, "time": when}
		},
		// lastword cuts a line into {head, last, end}: everything before the last word (with its space), the last word, and the
		// mark it ends with (. ! ? … or none) which stays with the word. A line of one word has an empty head.
		"lastword": func(a []any) any {
			head, last, end := lastWord(argStr(a, 0))
			return map[string]any{"head": head, "last": last, "end": end}
		},
		"art": func(a []any) any {
			k, kind := argStr(a, 0), argStr(a, 1)
			r := key("art:" + k)
			kind = c.artKind(r, kind)
			c.artN++
			return c.tone(artSVG(r, kind, c.s.P.Hue+float64(r.n(100))-50, argInt(a, 2), argInt(a, 3), fmt.Sprintf("%s%d", c.artID, c.artN), false), kind)
		},
		// photo is the photo of a slot as an <img>, or the picture "art" would draw with the same arguments when the slot has none.
		"photo": func(a []any) any {
			k, kind, w, h := argStr(a, 0), argStr(a, 1), argInt(a, 2), argInt(a, 3)
			if p, ok := c.photoFile(k, w, h, "", true); ok {
				return photoTag(k, p, w, h)
			}
			r := key("art:" + k)
			kind = c.artKind(r, kind)
			c.artN++
			return c.tone(artSVG(r, kind, c.s.P.Hue+float64(r.n(100))-50, w, h, fmt.Sprintf("%s%d", c.artID, c.artN), false), kind)
		},
		// hasphoto is whether the slot has a photo, for markup that only makes sense around one.
		"hasphoto": func(a []any) any {
			_, ok := c.photoFile(argStr(a, 0), argInt(a, 1), argInt(a, 2), "", false)
			return ok
		},
		// nphotos is how many photos the pages have asked for and got so far; the stylesheet is made last.
		"nphotos": func(a []any) any {
			n := 0
			if c.ph != nil {
				for _, p := range c.ph.path {
					if p != "" {
						n++
					}
				}
			}
			return n
		},
		"blk": func(a []any) any {
			if len(a) != 2 {
				fail("blk takes a name and the data")
			}
			var b bytes.Buffer
			if err := c.t.exec(&b, "b_"+argStr(a, 0), a[1], c.fns); err != nil {
				fail("%s", err)
			}
			return b.String()
		},
	}
	maps.Copy(fm, drawFuncs(c))
	return fm
}
