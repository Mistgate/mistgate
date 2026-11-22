package sitegen

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The look of a kind. A kind that has a "style" in its meta.json does not get its colours, fonts and shapes from independent dice:
// it declares a small set of choices that belong together, and a seed picks one of each.
//
//	scheme    "light", "dark" or "both" (the stylesheet answers prefers-color-scheme only for "both")
//	palettes  5 to 8 colour sets in the spirit of the kind's design; {"name":..., "l": roles, "d": roles}. A set holds "l" for
//	          the schemes light and both and "d" for dark and both. A role is a #rrggbb colour; bg, fg and ac are required and
//	          the others are derived from them when left out (see buildTone), so a palette can be three colours long
//	fonts     2 or 3 combinations of the system stacks of fontStacks (never a font file), each one at home in the kind's
//	          character; a site in Cyrillic only gets those whose stacks all have Cyrillic letters
//	radius, radius_s, radius_b, density, width, measure, base, scale, lh, stroke, border, shadows
//	          narrow ranges ([lo, hi]) or lists; one number t in [0,1] moves the radii and the density together
//	legacy    true: the kind has not been redesigned yet and still gets the old random look; the rest is ignored (but checked)
//
// Everything is drawn through labelled sub-streams of the site's rng, so adding a choice never moves another one.

type paletteDef struct {
	Name string            `json:"name"`
	L    map[string]string `json:"l"`
	D    map[string]string `json:"d"`
}

type fontDef struct {
	Head  string `json:"head"`
	Body  string `json:"body"`
	Label string `json:"label"` // labels and small caps lines; the body's stack when left out
	Mono  string `json:"mono"`
	FW    int    `json:"fw"`  // weight of headings
	LS    string `json:"ls"`  // letter spacing of headings
	LH    string `json:"lh"`  // line height of headings
	TT    string `json:"tt"`  // text-transform of labels
	LSL   string `json:"lsl"` // letter spacing of labels
	FSL   string `json:"fsl"` // size of labels
}

type styleDef struct {
	Legacy   bool         `json:"legacy"`
	Scheme   string       `json:"scheme"`
	Palettes []paletteDef `json:"palettes"`
	Fonts    []fontDef    `json:"fonts"`
	Radius   []int        `json:"radius"`
	RadiusS  []int        `json:"radius_s"`
	RadiusB  []int        `json:"radius_b"`
	Density  []float64    `json:"density"`
	Width    []int        `json:"width"`
	Measure  []int        `json:"measure"`
	Base     []int        `json:"base"`
	Scale    []float64    `json:"scale"`
	LH       []float64    `json:"lh"`
	Stroke   []float64    `json:"stroke"`
	Border   []int        `json:"border"`
	Shadows  []string     `json:"shadows"`

	pal []palette // built from Palettes by parseKind
}

var roleNames = []string{"bg", "band", "sf", "sf2", "fg", "mu", "ln", "ln2", "ac", "ai", "a2", "so", "ok", "wa", "bd", "oks", "was", "bds"}

var hexColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func parseHex(s string) ([3]float64, bool) {
	var c [3]float64
	if !hexColor.MatchString(s) {
		return c, false
	}
	for i := range c {
		v, _ := strconv.ParseUint(s[1+2*i:3+2*i], 16, 8)
		c[i] = float64(v) / 255
	}
	return c, true
}

// mix is a at t=0 and b at t=1.
func mix(a, b [3]float64, t float64) [3]float64 {
	var c [3]float64
	for i := range c {
		c[i] = a[i]*(1-t) + b[i]*t
	}
	return c
}

// toHSL is the hue in degrees and the saturation and lightness in percent.
func toHSL(c [3]float64) (h, s, l float64) {
	mx, mn := math.Max(c[0], math.Max(c[1], c[2])), math.Min(c[0], math.Min(c[1], c[2]))
	l = (mx + mn) / 2
	d := mx - mn
	if d == 0 {
		return 0, 0, l * 100
	}
	s = d / (1 - math.Abs(2*l-1))
	switch mx {
	case c[0]:
		h = math.Mod((c[1]-c[2])/d, 6)
	case c[1]:
		h = (c[2]-c[0])/d + 2
	default:
		h = (c[0]-c[1])/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s * 100, l * 100
}

// inkOn is the text colour that reads on c: white or a near black, a pure one where the near black would not reach 4.5:1.
func inkOn(c [3]float64) [3]float64 {
	white, soft, black := [3]float64{1, 1, 1}, [3]float64{.043, .043, .063}, [3]float64{}
	best := soft
	if contrast(c, white) >= contrast(c, soft) {
		best = white
	}
	if contrast(c, best) < 4.5 {
		best = white
		if contrast(c, black) > contrast(c, white) {
			best = black
		}
	}
	return best
}

func minContrast(c [3]float64, on ...[3]float64) float64 {
	m := math.Inf(1)
	for _, o := range on {
		m = math.Min(m, contrast(c, o))
	}
	return m
}

// buildTone completes the roles of one colour scheme: what a palette does not say is derived from what it does, in the way the
// scheme asks (a surface is a little lighter than the page in a dark scheme, white-ish in a light one, and so on).
func buildTone(roles map[string]string, dark bool) (tone, error) {
	c := map[string][3]float64{}
	for k, v := range roles {
		if !slices.Contains(roleNames, k) {
			return tone{}, fmt.Errorf("unknown role %q", k)
		}
		x, ok := parseHex(v)
		if !ok {
			return tone{}, fmt.Errorf("role %s: %q is not a #rrggbb colour", k, v)
		}
		c[k] = x
	}
	for _, k := range []string{"bg", "fg", "ac"} {
		if _, ok := c[k]; !ok {
			return tone{}, fmt.Errorf("role %s is missing", k)
		}
	}
	by := func(light, dk float64) float64 {
		if dark {
			return dk
		}
		return light
	}
	def := func(k string, v [3]float64) [3]float64 {
		if x, ok := c[k]; ok {
			return x
		}
		c[k] = v
		return v
	}
	white := [3]float64{1, 1, 1}
	bg, fg, ac := c["bg"], c["fg"], c["ac"]
	toward := white
	if dark {
		toward = fg
	}
	sf := def("sf", mix(bg, toward, by(.6, .05)))
	band := def("band", mix(bg, fg, by(.04, .035)))
	sf2 := def("sf2", mix(bg, fg, by(.075, .09)))
	mu, ok := c["mu"]
	if !ok {
		t := .42
		mu = mix(fg, bg, t)
		for t > .02 && minContrast(mu, bg, sf, band, sf2) < 4.6 {
			t -= .03
			mu = mix(fg, bg, t)
		}
	}
	def("ln", mix(bg, fg, by(.12, .14)))
	def("ln2", mix(bg, fg, by(.24, .28)))
	def("ai", inkOn(ac))
	h, s, l := toHSL(ac)
	def("a2", rgb(h+150, s*.9, l))
	def("so", mix(bg, ac, by(.12, .16)))
	def("ok", rgb(152, by(58, 52), by(33, 56)))
	def("wa", rgb(36, by(88, 86), by(34, 58)))
	def("bd", rgb(4, by(68, 74), by(43, 64)))
	for _, p := range [][2]string{{"oks", "ok"}, {"was", "wa"}, {"bds", "bd"}} {
		def(p[0], mix(bg, c[p[1]], by(.14, .2)))
	}
	hex := func(k string) string { return toHex(c[k]) }
	return tone{
		BG: hex("bg"), Band: hex("band"), Surface: hex("sf"), Surface2: hex("sf2"), FG: hex("fg"), Muted: toHex(mu), Line: hex("ln"), Line2: hex("ln2"),
		Acc: hex("ac"), AccInk: hex("ai"), Acc2: hex("a2"), Soft: hex("so"), Ok: hex("ok"), Warn: hex("wa"), Bad: hex("bd"),
		OkSoft: hex("oks"), WarnSoft: hex("was"), BadSoft: hex("bds"),
	}, nil
}

// build checks the style of a kind and turns its palettes into the palettes of a site.
func (sd *styleDef) build(layouts int) error {
	if !slices.Contains([]string{"light", "dark", "both"}, sd.Scheme) {
		return fmt.Errorf("scheme %q is none of light, dark, both", sd.Scheme)
	}
	if len(sd.Palettes) == 0 || len(sd.Fonts) == 0 {
		return fmt.Errorf("a style needs palettes and fonts")
	}
	for i, pd := range sd.Palettes {
		p := palette{}
		var err error
		if sd.Scheme != "dark" {
			if p.L, err = buildTone(pd.L, false); err != nil {
				return fmt.Errorf("palette %d (%s), l: %w", i, pd.Name, err)
			}
		}
		if sd.Scheme != "light" {
			if p.D, err = buildTone(pd.D, true); err != nil {
				return fmt.Errorf("palette %d (%s), d: %w", i, pd.Name, err)
			}
		}
		if sd.Scheme == "light" {
			p.D = p.L
		} else if sd.Scheme == "dark" {
			p.L = p.D
		}
		prim, _ := parseHex(p.prim(sd.Scheme).Acc)
		p.Hue, _, _ = toHSL(prim)
		sd.pal = append(sd.pal, p)
	}
	for i := range sd.Fonts {
		if err := sd.Fonts[i].check(); err != nil {
			return fmt.Errorf("font set %d: %w", i, err)
		}
	}
	for name, v := range map[string][]int{"radius": sd.Radius, "radius_s": sd.RadiusS, "radius_b": sd.RadiusB, "width": sd.Width, "measure": sd.Measure, "base": sd.Base, "border": sd.Border} {
		if len(v) > 2 || len(v) == 2 && v[0] > v[1] {
			return fmt.Errorf("%s is [lo, hi] or one number", name)
		}
	}
	return nil
}

// prim is the tone a page is drawn in first: the light one unless the scheme is dark.
func (p palette) prim(scheme string) tone {
	if scheme == "dark" {
		return p.D
	}
	return p.L
}

var (
	spacingRe = regexp.MustCompile(`^-?(\d+|\d*\.\d+)(em|px)$`)
	numberRe  = regexp.MustCompile(`^(\d+|\d*\.\d+)$`)
	sizeRe    = regexp.MustCompile(`^(\d+|\d*\.\d+)(rem|em|px)$`)
)

func (f *fontDef) check() error {
	for _, k := range []string{f.Head, f.Body, f.Label, f.Mono} {
		if k != "" {
			if _, ok := fontStacks[k]; !ok {
				return fmt.Errorf("no font stack %q", k)
			}
		}
	}
	if f.Head == "" || f.Body == "" {
		return fmt.Errorf("head and body are required")
	}
	if f.FW != 0 && (f.FW < 100 || f.FW > 900) {
		return fmt.Errorf("fw %d", f.FW)
	}
	for v, re := range map[string]*regexp.Regexp{f.LS: spacingRe, f.LSL: spacingRe, f.LH: numberRe, f.FSL: sizeRe} {
		if v != "" && !re.MatchString(v) {
			return fmt.Errorf("%q is not a CSS length", v)
		}
	}
	if f.TT != "" && f.TT != "none" && f.TT != "uppercase" {
		return fmt.Errorf("tt %q", f.TT)
	}
	return nil
}

// latinOnly are the stacks whose first fonts have no Cyrillic letters: a Russian page set in them would mix two faces letter by
// letter, so a site in Cyrillic does not get a font set that uses them.
var latinOnly = map[string]bool{"humanist": true, "grotesque": true, "charter": true, "old": true, "rounded": true, "geometric": true, "didone": true}

func (f fontDef) cyrillic() bool {
	for _, k := range []string{f.Head, f.Body, f.Label} {
		if latinOnly[k] {
			return false
		}
	}
	return true
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// fonts draws a font set; a site in Cyrillic draws among the sets that have Cyrillic letters.
func (sd *styleDef) fonts(r *rng, cyr bool) fonts {
	var set []fontDef
	for _, f := range sd.Fonts {
		if !cyr || f.cyrillic() {
			set = append(set, f)
		}
	}
	if len(set) == 0 {
		set = []fontDef{{Head: "sans", Body: "sans"}}
	}
	f := set[r.n(len(set))]
	return fonts{
		Body: fontStacks[f.Body], Head: fontStacks[f.Head], Mono: fontStacks[orStr(f.Mono, "mono")], Label: fontStacks[orStr(f.Label, f.Body)],
		FW: orInt(f.FW, 700), LS: orStr(f.LS, "-.01em"), LH: orStr(f.LH, "1.2"), TT: orStr(f.TT, "none"), LSL: orStr(f.LSL, ".06em"), FSL: orStr(f.FSL, ".78rem"),
	}
}

func orInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// lerpInt is the value at t in [0,1] of a range, or def when the style leaves the range out.
func lerpInt(v []int, t float64, def int) int {
	switch len(v) {
	case 0:
		return def
	case 1:
		return v[0]
	}
	return v[0] + int(math.Round(float64(v[1]-v[0])*t))
}

func lerpFloat(v []float64, t, def float64) float64 {
	switch len(v) {
	case 0:
		return def
	case 1:
		return v[0]
	}
	return v[0] + (v[1]-v[0])*t
}

// shape draws the measures of a site. One number moves the radii and the density together, so that a site is round or square all
// the way through, and the others are drawn on their own.
func (sd *styleDef) shape(r *rng) style {
	t, d := r.sub("round").float(), r.sub("dense").float()
	rad := lerpInt(sd.Radius, t, 8)
	s := style{
		R: rad, RS: lerpInt(sd.RadiusS, t, rad/2), RB: lerpInt(sd.RadiusB, t, rad*2),
		W: lerpInt(sd.Width, r.sub("w").float(), 1200), WT: lerpInt(sd.Measure, r.sub("wt").float(), 720),
		BW:     lerpInt(sd.Border, r.sub("bw").float(), 1),
		FS:     lerpInt(sd.Base, r.sub("fs").float(), 17),
		LH:     strconv.FormatFloat(math.Round(lerpFloat(sd.LH, r.sub("lh").float(), 1.6)*100)/100, 'f', -1, 64),
		Shadow: "none", Stroke: strconv.FormatFloat(math.Round(lerpFloat(sd.Stroke, r.sub("stroke").float(), 1.8)*10)/10, 'f', -1, 64),
	}
	if len(sd.Shadows) > 0 {
		s.Shadow = sd.Shadows[r.sub("sh").n(len(sd.Shadows))]
	}
	dens := lerpFloat(sd.Density, d, 1)
	s.Dens = strconv.FormatFloat(math.Round(dens*100)/100, 'f', -1, 64)
	s.Sec = int(math.Round(72 * dens))
	s.Gut = int(math.Round(20 * dens))
	s.T, s.TS = typeScale(float64(s.FS), math.Round(lerpFloat(sd.Scale, r.sub("scale").float(), 1.25)*1000)/1000)
	return s
}

// typeScale is the type scale of a site: step n is base x scale^n. The first three steps are plain sizes; the large ones (the
// display sizes) follow the width of the window up to their size at a 1440 px window, and never fall below half of themselves
// nor below a size that fits a long word on a phone.
func typeScale(base, scale float64) (t [6]string, small string) {
	px := func(v float64) string { return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64) + "px" }
	for n := range t {
		v := base * math.Pow(scale, float64(n))
		if n < 3 {
			t[n] = px(v)
			continue
		}
		lo := math.Max(v*.5, base*1.35)
		t[n] = fmt.Sprintf("clamp(%s,%.2fvw,%s)", px(lo), v/14.4, px(v))
	}
	return t, px(base / math.Pow(scale, 1.5))
}

// legacyStyle is what a kind that is not redesigned yet gets as the new measures: the old ones, with a scale and a rhythm that
// match what its stylesheet has always assumed.
func legacyStyle(s style) style {
	s.RB, s.WT, s.BW, s.Sec, s.Gut, s.Dens = s.R, 720, 1, 56, 20, "1"
	s.T, s.TS = typeScale(float64(s.FS), 1.25)
	return s
}

func legacyFonts(f fonts) fonts {
	f.Label, f.FW, f.LS, f.LH, f.TT, f.LSL, f.FSL = f.Body, 700, "-.01em", "1.2", "none", ".06em", ".78rem"
	return f
}

// colorScheme is the value of the color-scheme meta tag and property.
func colorScheme(scheme string) string {
	if scheme == "both" {
		return "light dark"
	}
	return scheme
}

// hasCyrillic is whether a language is written in Cyrillic.
func hasCyrillic(lang string) bool {
	return strings.HasPrefix(lang, "ru") || strings.HasPrefix(lang, "kk")
}

// complete derives the roles a legacy palette does not draw from the ones it does.
func (t *tone) complete(dark bool) {
	bg, _ := parseHex(t.BG)
	fg, _ := parseHex(t.FG)
	by := func(light, dk float64) float64 {
		if dark {
			return dk
		}
		return light
	}
	t.Band, t.Surface2, t.Line2 = toHex(mix(bg, fg, by(.04, .035))), toHex(mix(bg, fg, by(.075, .09))), toHex(mix(bg, fg, by(.24, .28)))
	soft := func(c string) string {
		x, _ := parseHex(c)
		return toHex(mix(bg, x, by(.14, .2)))
	}
	t.OkSoft, t.WarnSoft, t.BadSoft = soft(t.Ok), soft(t.Warn), soft(t.Bad)
}
