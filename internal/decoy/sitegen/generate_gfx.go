package sitegen

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Colours, fonts, favicons and the small generated pictures of a generated site. Everything is drawn from the site's rng, so one
// seed always gives the same pictures and two seeds never share a favicon.

// tone is one colour scheme (light or dark) of a site.
type tone struct {
	BG, Surface, FG, Muted, Line, Acc, AccInk, Acc2, Soft, Ok, Warn, Bad string
	// Roles of the redesigned kinds (see generate_style.go): a band between the page and its surfaces, a second surface, a
	// stronger line, and the soft backgrounds of the three states. A legacy palette derives them from the others.
	Band, Surface2, Line2, OkSoft, WarnSoft, BadSoft string
}

type palette struct {
	L, D tone
	Hue  float64
}

// style is the shape of a site's typography and boxes. Fields are strings or ints so a template can print them as they are.
type style struct {
	R, RS  int    // corner radius of boxes and of small controls, px
	W      int    // content width, px
	FS     int    // body font size, px
	LH     string // body line height
	Shadow string // box-shadow value of cards
	Stroke string // stroke width of the icons
	// The rest is the redesigned kinds' (legacy kinds get values that match what they have always assumed):
	RB, WT, BW, Sec, Gut int       // big radius, width of a text column, border width, space between sections, gutter: px
	Dens                 string    // the density the rhythm was drawn at, 1 is the middle
	T                    [6]string // type scale t0..t5: sizes as CSS values, t3 and up follow the window width
	TS                   string    // small text size
}

// fonts are the stacks of a site and the way its headings and labels are set.
type fonts struct {
	Body, Head, Mono, Label string
	FW                      int    // weight of headings
	LS, LH                  string // letter spacing and line height of headings
	TT, LSL, FSL            string // text-transform, letter spacing and size of labels
}

const monoStack = `ui-monospace,SFMono-Regular,Menlo,Consolas,"Liberation Mono",monospace`

var fontStacks = map[string]string{
	"mono":      monoStack,
	"rounded":   `ui-rounded,"SF Pro Rounded","Nunito","Segoe UI",system-ui,sans-serif`,
	"geometric": `"Avenir Next",Avenir,Montserrat,"Century Gothic","Segoe UI",system-ui,sans-serif`,
	"didone":    `Didot,"Bodoni 72","Bodoni MT","Playfair Display",Georgia,serif`,
	"bookish":   `Cambria,Georgia,"Times New Roman","DejaVu Serif",serif`,
	"verdana":   `Verdana,Geneva,"DejaVu Sans",sans-serif`,
	"sans":      `system-ui,-apple-system,"Segoe UI",Roboto,"Helvetica Neue",Arial,sans-serif`,
	"humanist":  `Seravek,"Gill Sans Nova",Ubuntu,Calibri,"DejaVu Sans",sans-serif`,
	"grotesque": `"Helvetica Neue",Helvetica,Arial,"Liberation Sans",sans-serif`,
	"charter":   `Charter,"Bitstream Charter","Sitka Text",Cambria,serif`,
	"old":       `"Iowan Old Style","Palatino Linotype",Palatino,"Book Antiqua",serif`,
	"georgia":   `Georgia,"Times New Roman",Times,serif`,
}

var fontPairs = [][2]string{
	{"sans", "sans"}, {"sans", "charter"}, {"charter", "sans"}, {"old", "humanist"}, {"humanist", "humanist"},
	{"grotesque", "grotesque"}, {"georgia", "grotesque"}, {"humanist", "old"}, {"charter", "charter"}, {"sans", "old"},
}

func newFonts(r *rng) fonts {
	p := fontPairs[r.n(len(fontPairs))]
	return fonts{Body: fontStacks[p[0]], Head: fontStacks[p[1]], Mono: monoStack}
}

func newStyle(r *rng) style {
	radii := [][2]int{{0, 0}, {3, 2}, {6, 4}, {10, 6}, {16, 8}, {22, 10}}
	rad := radii[r.n(len(radii))]
	shadow := "none"
	if r.chance(45) {
		shadow = "0 1px 2px rgba(0,0,0,.06),0 6px 20px rgba(0,0,0,.06)"
	}
	return style{
		R: rad[0], RS: rad[1],
		W:      []int{940, 1000, 1060, 1120}[r.n(4)],
		FS:     []int{16, 17, 18}[r.n(3)],
		LH:     []string{"1.5", "1.55", "1.6", "1.65"}[r.n(4)],
		Shadow: shadow,
		Stroke: []string{"1.5", "1.6", "1.7", "1.8", "1.9", "2", "2.1", "2.2"}[r.n(8)],
	}
}

// rgb is HSL (h in degrees, s and l in percent) as 0..1 channels.
func rgb(h, s, l float64) [3]float64 {
	h = math.Mod(math.Mod(h, 360)+360, 360)
	s, l = s/100, l/100
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return [3]float64{r + m, g + m, b + m}
}

func toHex(c [3]float64) string {
	f := func(v float64) int { return int(math.Round(math.Min(1, math.Max(0, v)) * 255)) }
	return fmt.Sprintf("#%02x%02x%02x", f(c[0]), f(c[1]), f(c[2]))
}

func hsl(h, s, l float64) string { return toHex(rgb(h, s, l)) }

func lum(c [3]float64) float64 {
	f := func(v float64) float64 {
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c[0]) + 0.7152*f(c[1]) + 0.0722*f(c[2])
}

func contrast(a, b [3]float64) float64 {
	la, lb := lum(a), lum(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// ink is the text colour (near-white or near-black) that reads best on c.
func ink(c [3]float64) string {
	white, black := [3]float64{1, 1, 1}, rgb(220, 25, 8)
	if contrast(c, white) >= contrast(c, black) {
		return "#ffffff"
	}
	return toHex(black)
}

func newPalette(r *rng) palette {
	h := float64(r.n(360))
	sat := float64(52 + r.n(34))
	// The accent carries white text in the light scheme: darken it until the pair reads (4.5:1).
	l := float64(38 + r.n(12))
	acc := rgb(h, sat, l)
	for contrast(acc, [3]float64{1, 1, 1}) < 4.5 && l > 20 {
		l -= 2
		acc = rgb(h, sat, l)
	}
	h2 := h + float64(140+r.n(80))
	if r.chance(40) {
		h2 = h + float64(20+r.n(30))
	}
	// Neutrals follow the accent hue, or a warm paper tone.
	hn := h
	if r.chance(35) {
		hn = float64(30 + r.n(30))
	}
	sn := float64(6 + r.n(18))
	bgL := float64(96 + r.n(4))
	surface := "#ffffff"
	if r.chance(35) {
		surface = hsl(hn, sn, bgL+0.8)
	}
	light := tone{
		BG: hsl(hn, sn, bgL), Surface: surface, FG: hsl(hn, sn+8, float64(11+r.n(7))), Muted: hsl(hn, sn, float64(38+r.n(7))),
		Line: hsl(hn, sn, float64(86+r.n(5))), Acc: toHex(acc), AccInk: ink(acc), Acc2: hsl(h2, float64(50+r.n(25)), float64(38+r.n(10))),
		Soft: hsl(h, float64(45+r.n(30)), float64(93+r.n(4))),
		Ok:   hsl(float64(140+r.n(20)), float64(50+r.n(14)), float64(28+r.n(6))), Warn: hsl(float64(30+r.n(12)), float64(86+r.n(10)), float64(33+r.n(6))), Bad: hsl(float64(358+r.n(10)), float64(64+r.n(12)), float64(41+r.n(6))),
	}
	dl := float64(58 + r.n(10))
	dacc := rgb(h, sat-8, dl)
	dark := tone{
		BG: hsl(hn, sn+4, float64(8+r.n(5))), Surface: hsl(hn, sn+4, float64(12+r.n(5))), FG: hsl(hn, sn, float64(91+r.n(5))),
		Muted: hsl(hn, sn, float64(62+r.n(6))), Line: hsl(hn, sn+4, float64(21+r.n(5))), Acc: toHex(dacc), AccInk: ink(dacc),
		Acc2: hsl(h2, float64(50+r.n(25)), float64(62+r.n(8))), Soft: hsl(h, float64(25+r.n(25)), float64(15+r.n(4))),
		Ok: hsl(float64(140+r.n(20)), float64(44+r.n(12)), float64(52+r.n(8))), Warn: hsl(float64(36+r.n(12)), float64(84+r.n(10)), float64(54+r.n(8))), Bad: hsl(float64(358+r.n(10)), float64(74+r.n(10)), float64(62+r.n(8))),
	}
	light.complete(false)
	dark.complete(true)
	return palette{L: light, D: dark, Hue: h}
}

// icons are 24x24 stroke icons; short on purpose so no two sites share a long literal.
var icons = map[string]string{
	"check":    `<path d="M5 12.5l4.5 4.5L19 7.5"/>`,
	"alert":    `<path d="M12 4l9 16H3z"/><path d="M12 10v4M12 17.5v.01"/>`,
	"clock":    `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`,
	"calendar": `<rect x="4" y="5" width="16" height="15" rx="2"/><path d="M4 10h16M9 3v4M15 3v4"/>`,
	"file":     `<path d="M7 3h7l4 4v14H7z"/><path d="M14 3v4h4"/>`,
	"upload":   `<path d="M12 16V5M7.5 9.5L12 5l4.5 4.5M5 19h14"/>`,
	"download": `<path d="M12 4v11M7.5 10.5L12 15l4.5-4.5M5 19h14"/>`,
	"lock":     `<rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 018 0v3"/>`,
	"user":     `<circle cx="12" cy="8" r="4"/><path d="M4 20c1-4 4-6 8-6s7 2 8 6"/>`,
	"users":    `<circle cx="9" cy="9" r="3"/><circle cx="16" cy="10" r="2.5"/><path d="M3 20c1-4 3-5 6-5s5 1 6 5"/>`,
	"arrow":    `<path d="M5 12h14M13 6l6 6-6 6"/>`,
	"star":     `<path d="M12 3l2.7 5.6 6.1.9-4.4 4.3 1 6.1L12 17l-5.4 2.9 1-6.1-4.4-4.3 6.1-.9z"/>`,
	"leaf":     `<path d="M5 19c0-9 5-14 14-14 0 9-5 14-14 14zM5 19l7-7"/>`,
	"book":     `<path d="M4 5h6a2 2 0 012 2v12a2 2 0 00-2-2H4zM20 5h-6a2 2 0 00-2 2v12a2 2 0 012-2h6z"/>`,
	"pin":      `<path d="M12 21s7-6 7-11a7 7 0 00-14 0c0 5 7 11 7 11z"/><circle cx="12" cy="10" r="2.5"/>`,
	"phone":    `<path d="M6 4h3l2 5-2 1.5a11 11 0 005 5l1.5-2 5 2v3a2 2 0 01-2 2A15 15 0 013 6a2 2 0 013-2z"/>`,
	"shield":   `<path d="M12 3l7 3v6c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V6z"/>`,
	"code":     `<path d="M8 8l-4 4 4 4M16 8l4 4-4 4M13.5 5l-3 14"/>`,
	"terminal": `<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M7 10l3 2-3 2M12 15h5"/>`,
	"music":    `<path d="M9 18V6l10-2v12"/><circle cx="6.5" cy="18" r="2.5"/><circle cx="16.5" cy="16" r="2.5"/>`,
	"heart":    `<path d="M12 20s-8-5-8-11a4.5 4.5 0 018-2.500A4.5 4.5 0 0120 9c0 6-8 11-8 11z"/>`,
	"mail":     `<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3.5 7l8.5 6 8.5-6"/>`,
	"eye":      `<path d="M3 12s3.5-6 9-6 9 6 9 6-3.5 6-9 6-9-6-9-6z"/><circle cx="12" cy="12" r="3"/>`,
	"bolt":     `<path d="M13 3L5 13.500h6L10 21l8-10.500h-6z"/>`,
	"globe":    `<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3c3 3 3 15 0 18M12 3c-3 3-3 15 0 18"/>`,
	"layers":   `<path d="M12 4l9 5-9 5-9-5zM3 14l9 5 9-5"/>`,
	"sun":      `<circle cx="12" cy="12" r="4"/><path d="M12 3v2M12 19v2M3 12h2M19 12h2"/>`,
	"tool":     `<path d="M14.5 6.500a4 4 0 005 5L20 14l-6 6-9-9 6-6z"/>`,
	"gift":     `<rect x="4" y="9" width="16" height="11"/><path d="M12 9v11M3 9h18M12 9V5"/>`,
	"menu":     `<path d="M4 7h16M4 12h16M4 17h16"/>`,
	"close":    `<path d="M6 6l12 12M18 6L6 18"/>`,
	"chev":     `<path d="M9 5l7 7-7 7"/>`,
	"chevd":    `<path d="M5 9l7 7 7-7"/>`,
	"back":     `<path d="M19 12H5M11 6l-6 6 6 6"/>`,
	"search":   `<circle cx="11" cy="11" r="6.5"/><path d="M16 16l4.5 4.5"/>`,
	"plus":     `<path d="M12 5v14M5 12h14"/>`,
	"minus":    `<path d="M5 12h14"/>`,
	"grid":     `<rect x="4" y="4" width="7" height="7" rx="1.5"/><rect x="13" y="4" width="7" height="7" rx="1.5"/><rect x="4" y="13" width="7" height="7" rx="1.5"/><path d="M13 16.5h7"/>`,
	"chart":    `<path d="M5 20V9M11 20V4M17 20v-7M3 20h18"/>`,
	"checkc":   `<circle cx="12" cy="12" r="9"/><path d="M8 12.5l3 3 5-6"/>`,
	"key":      `<circle cx="8" cy="15" r="4"/><path d="M11 12l8-8M16 7l3 3"/>`,
	"face":     `<circle cx="12" cy="12" r="9"/><path d="M9 10v.01M15 10v.01M8.5 14.5c2 2 5 2 7 0"/>`,
	"wheat":    `<path d="M12 21V8M12 11c-3 0-4.5-1.5-4.5-4 3 0 4.5 1.5 4.5 4zM12 11c3 0 4.5-1.5 4.5-4-3 0-4.5 1.5-4.5 4zM12 17c-3 0-4.5-1.5-4.5-4 3 0 4.5 1.5 4.5 4zM12 17c3 0 4.5-1.5 4.5-4-3 0-4.5 1.5-4.5 4z"/>`,
	"basket":   `<path d="M4 10h16l-1.5 9h-13zM8 10c0-5 8-5 8 0"/>`,
	"cup":      `<path d="M5 8h11v6a5 5 0 01-5 5h-1a5 5 0 01-5-5zM16 10h2a2 2 0 010 4h-2M8 3v2M12 3v2"/>`,
	"moon":     `<path d="M20 14.5A8 8 0 019.5 4 8 8 0 1020 14.5z"/>`,
	"nouser":   `<circle cx="12" cy="8" r="4"/><path d="M4 20c1-4 4-6 8-6s7 2 8 6M4 4l16 16"/>`,
	"tag":      `<path d="M4 4h8l8 8-8 8-8-8z"/><circle cx="8.5" cy="8.5" r="1"/>`,
	"link":     `<path d="M10 14a4 4 0 005.7 0l3-3a4 4 0 00-5.7-5.7l-1 1M14 10a4 4 0 00-5.7 0l-3 3a4 4 0 005.7 5.7l1-1"/>`,
	"arrowc":   `<circle cx="12" cy="12" r="9"/><path d="M7 12h10M13 8l4 4-4 4"/>`,
	"sparkle":  `<path d="M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8z"/>`,
	"external": `<path d="M14 4h6v6M20 4l-9 9M18 14v5H5V6h5"/>`,
	"cake":     `<path d="M4 20h16v-7H4zM6 13V9h12v4M12 9V5"/>`,
}

var spaces = []string{" ", " ", "  ", "\n"}

// svgOpen is the opening tag of an inline drawing with its attributes in an order and with a spacing of the rng's choosing, so
// that two drawings do not begin with the same run of markup.
func svgOpen(r *rng, attrs ...string) string {
	var b strings.Builder
	b.WriteString("<svg")
	for _, i := range r.perm(len(attrs)) {
		b.WriteString(spaces[r.n(len(spaces))] + attrs[i])
	}
	return b.String() + ">"
}

// iconSVG is an inline icon. Its attributes come in the order and spacing r chooses, so a run of markup that holds an icon is
// not the same on two sites.
func iconSVG(name string, r *rng) string {
	body, ok := icons[name]
	if !ok {
		body = icons["check"]
	}
	attrs := []string{`class="__ic__"`, `viewBox="0 0 24 24"`, `aria-hidden="true"`}
	if r.chance(50) {
		attrs = append(attrs, `focusable="false"`)
	}
	if r.chance(30) {
		attrs = append(attrs, `width="24" height="24"`)
	}
	var b strings.Builder
	b.WriteString("<svg")
	for _, i := range r.perm(len(attrs)) {
		b.WriteString(spaces[r.n(len(spaces))] + attrs[i])
	}
	return b.String() + ">" + body + "</svg>"
}

// faviconMark is the inside of the site's favicon (64x64) and, at 28px, the logo next to the name: a shape from a fixed set,
// with the initial of the name, in the site's colours.
func faviconMark(r *rng, p palette, initial string) string {
	a, b, ac := p.L.Acc, p.L.Acc2, p.L.AccInk
	rx := 8 + r.n(24)
	// the attributes of the letter come in an order and with a spacing of the rng's choosing, like those of svgOpen
	ta := []string{`x="32"`, fmt.Sprintf(`y="%d"`, 43+r.n(3)), `text-anchor="middle"`, `font-family="system-ui,sans-serif"`, fmt.Sprintf(`font-size="%d"`, 32+r.n(6)), `font-weight="700"`, fmt.Sprintf(`fill="%s"`, ac)}
	var lb strings.Builder
	lb.WriteString("<text")
	for _, i := range r.perm(len(ta)) {
		lb.WriteString(spaces[r.n(len(spaces))] + ta[i])
	}
	letter := lb.String() + ">" + initial + "</text>"
	switch r.n(7) {
	case 0:
		return fmt.Sprintf(`<rect width="64" height="64" rx="%d" fill="%s"/>%s`, rx, a, letter)
	case 1:
		return fmt.Sprintf(`<circle cx="32" cy="32" r="30" fill="%s"/><circle cx="32" cy="32" r="%d" fill="none" stroke="%s" stroke-width="%d" opacity=".55"/>%s`, a, 18+r.n(6), ac, 2+r.n(3), letter)
	case 2:
		return fmt.Sprintf(`<circle cx="%d" cy="%d" r="%d" fill="%s"/><circle cx="%d" cy="%d" r="%d" fill="%s" opacity=".85"/>`, 24+r.n(4), 26+r.n(6), 18+r.n(5), a, 40-r.n(4), 38+r.n(6), 16+r.n(6), b)
	case 3:
		w1, w2, w3 := 28+r.n(30), 20+r.n(30), 12+r.n(30)
		return fmt.Sprintf(`<rect x="%d" y="10" width="%d" height="12" rx="6" fill="%s"/><rect x="%d" y="26" width="%d" height="12" rx="6" fill="%s"/><rect x="%d" y="42" width="%d" height="12" rx="6" fill="%s"/>`, 4+r.n(4), w1, a, 4+r.n(10), w2, b, 4+r.n(14), w3, a)
	case 4:
		return fmt.Sprintf(`<path d="M32 3 61 32 32 61 3 32z" fill="%s"/>%s`, a, letter)
	case 5:
		return fmt.Sprintf(`<rect width="64" height="64" rx="%d" fill="%s"/><path d="M0 %d L%d %d L64 %d V64 H0z" fill="%s" opacity=".9"/><circle cx="%d" cy="%d" r="%d" fill="%s"/>`, rx, a, 46+r.n(8), 20+r.n(20), 30+r.n(14), 44+r.n(10), b, 40+r.n(14), 16+r.n(8), 6+r.n(4), ac)
	default:
		return fmt.Sprintf(`<path d="M12 8h%dv48H12z" fill="%s"/><path d="M%d 8h%d v48h-%d z" fill="%s"/>%s`, 22+r.n(6), a, 40+r.n(4), 12+r.n(4), 12+r.n(4), b, "")
	}
}

func faviconSVG(mark string) string {
	return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">` + mark + `</svg>`
}

// inlineMark is the mark as an inline logo; its size comes from the stylesheet.
func inlineMark(mark string) string {
	return `<svg class="__mark__" viewBox="0 0 64 64" aria-hidden="true">` + mark + `</svg>`
}

// Pictures: abstract "photos" and header art. kind is one of artKinds; w, h the viewBox.
var artKinds = []string{"landscape", "blobs", "tiles", "waves"}

// subjectKinds are the subjects of artSVG that a variant asks for with its "art" (data.json) or a template with a name; the
// old four are what a picture gets when neither does.
var subjectKinds = []string{"topo", "stars", "botanical", "still", "poster", "ui", "parcel", "checker", "staff"}

var allArtKinds = append(slices.Clone(artKinds), subjectKinds...)

func artSVG(r *rng, kind string, hue float64, w, h int, id string, standalone bool) string {
	var b strings.Builder
	vb, par := fmt.Sprintf(`viewBox="0 0 %d %d"`, w, h), `preserveAspectRatio="xMidYMid slice"`
	if standalone {
		b.WriteString(svgOpen(r, `xmlns="http://www.w3.org/2000/svg"`, vb, par))
	} else {
		b.WriteString(svgOpen(r, vb, par, `role="img"`, `aria-hidden="true"`, `class="__art__"`))
	}
	W, H := float64(w), float64(h)
	if drawSubject(&b, r, kind, hue, W, H, id) {
		b.WriteString("</svg>")
		return b.String()
	}
	fw := func(lo, hi float64) float64 { return lo + r.float()*(hi-lo) }
	switch kind {
	case "landscape":
		fmt.Fprintf(&b, `<defs><linearGradient id="%s" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="%s"/><stop offset="1" stop-color="%s"/></linearGradient></defs>`,
			id, hsl(hue+fw(-12, 12), 58, fw(66, 78)), hsl(hue+fw(15, 40), 70, fw(86, 93)))
		fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="url(#%s)"/>`, w, h, id)
		fmt.Fprintf(&b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="%s"/>`, fw(0.15, 0.85)*W, fw(0.18, 0.4)*H, fw(0.06, 0.12)*H*1.6, hsl(hue+fw(25, 70), fw(55, 78), fw(74, 84)))
		for i := 0; i < 3; i++ {
			y := H * (0.5 + 0.12*float64(i) + fw(-0.04, 0.04))
			cx := fw(0.2, 0.8) * W
			fmt.Fprintf(&b, `<path d="M0 %.0f Q%.0f %.0f %.0f %.0f T%d %.0f V%d H0z" fill="%s"/>`, y, cx/2, y-fw(0.08, 0.22)*H, cx, y-fw(0, 0.06)*H, w, y+fw(-0.02, 0.03)*H, h, hsl(hue+float64(i)*fw(8, 20), fw(28, 45), 58-float64(i)*13+fw(-4, 4)))
		}
	case "blobs":
		fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="%s"/>`, w, h, hsl(hue+fw(-10, 10), fw(35, 55), fw(88, 94)))
		for i, n := 0, 4+r.n(4); i < n; i++ {
			fmt.Fprintf(&b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="%s" opacity="%.2f"/>`, fw(0, 1)*W, fw(0, 1)*H, fw(0.1, 0.34)*H, hsl(hue+fw(-70, 70), fw(50, 78), fw(48, 72)), fw(0.5, 0.9))
		}
	case "tiles":
		cols, rows := 4, 3
		if w < h {
			cols, rows = 3, 4
		}
		cw, ch := W/float64(cols), H/float64(rows)
		pal := []string{hsl(hue, 60, 52), hsl(hue+fw(25, 60), 65, 62), hsl(hue+fw(160, 200), 60, 58), hsl(hue, 30, 92), hsl(hue, 45, 28)}
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				x0, y0 := float64(x)*cw, float64(y)*ch
				fmt.Fprintf(&b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" fill="%s"/>`, x0, y0, cw+1, ch+1, pal[r.n(len(pal))])
				switch r.n(3) {
				case 0:
					fmt.Fprintf(&b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="%s"/>`, x0+cw/2, y0+ch/2, math.Min(cw, ch)*fw(0.22, 0.42), pal[r.n(len(pal))])
				case 1:
					fmt.Fprintf(&b, `<path d="M%.0f %.0f h%.0f a%.0f %.0f 0 0 1 -%.0f %.0f z" fill="%s"/>`, x0+cw, y0, -cw, cw, cw, -cw, cw, pal[r.n(len(pal))])
				}
			}
		}
	default: // waves
		fmt.Fprintf(&b, `<defs><linearGradient id="%s" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="%s"/><stop offset="1" stop-color="%s"/></linearGradient></defs>`,
			id, hsl(hue, 62, fw(42, 54)), hsl(hue+fw(30, 70), 66, fw(50, 66)))
		fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="url(#%s)"/>`, w, h, id)
		for i := 0; i < 4; i++ {
			y := H * (0.35 + 0.16*float64(i))
			a := fw(0.05, 0.14) * H
			fmt.Fprintf(&b, `<path d="M0 %.0f C%.0f %.0f %.0f %.0f %d %.0f V%d H0z" fill="#fff" opacity="%.2f"/>`, y, W*0.3, y-a, W*0.65, y+a, w, y, h, 0.1+0.06*float64(i))
		}
	}
	b.WriteString("</svg>")
	return b.String()
}

// The look as maps, for the templates (they read maps only).
func (t tone) data() map[string]any {
	return map[string]any{"BG": t.BG, "Surface": t.Surface, "FG": t.FG, "Muted": t.Muted, "Line": t.Line, "Acc": t.Acc, "AccInk": t.AccInk,
		"Acc2": t.Acc2, "Soft": t.Soft, "Ok": t.Ok, "Warn": t.Warn, "Bad": t.Bad,
		"Band": t.Band, "Surface2": t.Surface2, "Line2": t.Line2, "OkSoft": t.OkSoft, "WarnSoft": t.WarnSoft, "BadSoft": t.BadSoft}
}

func (s style) data() map[string]any {
	return map[string]any{"R": s.R, "RS": s.RS, "W": s.W, "FS": s.FS, "LH": s.LH, "Shadow": s.Shadow, "Stroke": s.Stroke,
		"RB": s.RB, "WT": s.WT, "BW": s.BW, "Sec": s.Sec, "Gut": s.Gut, "Dens": s.Dens, "TS": s.TS,
		"T0": s.T[0], "T1": s.T[1], "T2": s.T[2], "T3": s.T[3], "T4": s.T[4], "T5": s.T[5]}
}

func (f fonts) data() map[string]any {
	return map[string]any{"Body": f.Body, "Head": f.Head, "Mono": f.Mono, "Label": f.Label, "FW": f.FW, "LS": f.LS, "LH": f.LH,
		"TT": f.TT, "LSL": f.LSL, "FSL": f.FSL}
}
