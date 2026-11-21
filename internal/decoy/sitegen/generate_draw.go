package sitegen

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Drawings and small data helpers for the templates. Every one is a keyed draw (the same key gives the same answer on every
// page), takes its numbers from the site's rng, writes no <title> and stays far below 8 KB, so that two sites never share a
// long run of them. Colours are classes, never values: the stylesheet of the kind (draw_css in base.css.tmpl has a default)
// decides what a line, an area, a street or a cell looks like, and so the picture follows the palette and the colour scheme.
//
//	series "k" n lo hi              {v, last, min, max, avg, n}: n numbers that wander between lo and hi
//	chart $series kind w h          kind spark, area or bars: an <svg class="__dchart__"> of the series (classes cl line, ca area,
//	                                cg grid, cb bars, cp last point), no axis labels: write them outside it
//	avail "k" n warn bad calm       {days, pct, bp, warn, bad}: n days of availability (0 up, 1 degraded, 2 down) with warn and bad
//	                                days in per mille, the last calm days always up, and the percentage that the days add up to
//	cal $items "2026-11-14" weeks   a heat map of weeks x 7 days: the cells of the dates ($items[].date_iso) are classes h1, h2
//	mapsvg "k" w h                  a quarter: 6 to 9 streets, blocks, a park, a river, a pin and 2 or 3 street names of the country
//	ident "k"                       a 5x5 mirrored pattern in one of the four fill classes i0 to i3
//	person "k"                      {first, last, name, ini}: a person of the site's country
//	tags $items "field"             [{tag, n}]: the values of a field (a word or a list of words) of the items, most used first
//	stamp "k" text size             a round stamp with the text once around its rim
//	hl code lang                    a code sample as escaped HTML with spans (generate_code.go)
//	calmonths $items "2026-11-14" 3 the n months that end with the date's, newest first, as {label, pad, days}: pad is the number of
//	                                blank days before the 1st (a week starts on Monday), days are {n, k} with k 0 a quiet day, 1 a
//	                                minor, 2 a major incident, 3 maintenance (by the item's impact: minor, major, maint) and 4 a day
//	                                after the end date
//	times "k" n                     n times of one day in order, "14:24" (24 hours; write the time zone as a text)
//	fsize "k" loKB hiKB             a file size between the two, written with the site's units and decimal mark: "4,2 MB"

func argStr(a []any, i int) string {
	if i >= len(a) {
		fail("a function is missing argument %d", i+1)
	}
	s, ok := a[i].(string)
	if !ok {
		fail("argument %d should be a string, not %T", i+1, a[i])
	}
	return s
}

func argInt(a []any, i int) int {
	if i >= len(a) {
		fail("a function is missing argument %d", i+1)
	}
	switch n := a[i].(type) {
	case int:
		return n
	case float64: // a number of the data files
		if n == math.Trunc(n) {
			return int(n)
		}
	}
	fail("argument %d should be a whole number, not %v", i+1, a[i])
	return 0
}

func argList(a []any, i int) []any {
	if i >= len(a) {
		fail("a function is missing argument %d", i+1)
	}
	l, ok := a[i].([]any)
	if !ok || len(l) == 0 {
		fail("argument %d should be a list that is not empty", i+1)
	}
	return l
}

func argMap(a []any, i int) map[string]any {
	if i >= len(a) {
		fail("a function is missing argument %d", i+1)
	}
	m, ok := a[i].(map[string]any)
	if !ok {
		fail("argument %d should be an object, not %T", i+1, a[i])
	}
	return m
}

// uid is an id that no other drawing of the site has.
func (c *ctx) uid() string {
	c.artN++
	return fmt.Sprintf("%s%d", c.artID, c.artN)
}

func xmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func drawFuncs(c *ctx) map[string]tmplFunc {
	key := func(k string) *rng { return c.r.sub("k:" + k) }
	return map[string]tmplFunc{
		"series": func(a []any) any {
			return makeSeries(key("series:"+argStr(a, 0)), argInt(a, 1), argInt(a, 2), argInt(a, 3))
		},
		"chart": func(a []any) any {
			s := argMap(a, 0)
			return drawChart(key(fmt.Sprintf("chart:%v:%v:%v:%v", s["v"], a[1], a[2], a[3])), s, argStr(a, 1), argInt(a, 2), argInt(a, 3))
		},
		"avail": func(a []any) any {
			m := makeAvail(key("avail:"+argStr(a, 0)), argInt(a, 1), argInt(a, 2), argInt(a, 3), argInt(a, 4))
			m["pct"] = formatBP(m["bp"].(int), c.s.country.Lang)
			return m
		},
		"cal": func(a []any) any {
			return drawCal(key(fmt.Sprintf("cal:%v:%v:%d", a[1], a[2], len(argList(a, 0)))), argList(a, 0), argStr(a, 1), argInt(a, 2), c.uid())
		},
		"mapsvg": func(a []any) any {
			return drawMap(key("map:"+argStr(a, 0)), c.s.country.Streets, argInt(a, 1), argInt(a, 2))
		},
		"ident":  func(a []any) any { return drawIdent(key("ident:" + argStr(a, 0))) },
		"person": func(a []any) any { return c.person(key("person:" + argStr(a, 0))) },
		"tags":   func(a []any) any { return countTags(argList(a, 0), argStr(a, 1)) },
		"stamp": func(a []any) any {
			return drawStamp(key("stamp:"+argStr(a, 0)), argStr(a, 1), argInt(a, 2), c.uid())
		},
		"hl":        func(a []any) any { return highlight(argStr(a, 0), argStr(a, 1)) },
		"calmonths": func(a []any) any { return c.calMonths(argList(a, 0), argStr(a, 1), argInt(a, 2)) },
		"times":     func(a []any) any { return makeTimes(key("times:"+argStr(a, 0)), argInt(a, 1)) },
		"fsize":     func(a []any) any { return c.fsize(key("fsize:"+argStr(a, 0)), argInt(a, 1), argInt(a, 2)) },
	}
}

func (c *ctx) person(r *rng) map[string]any {
	first, last := c.s.country.person(r)
	if rep := c.s.country.rep; rep != nil {
		first, last = rep.Replace(first), rep.Replace(last)
	}
	ini := func(s string) string {
		for _, ch := range s {
			if unicode.IsLetter(ch) {
				return string(unicode.ToUpper(ch))
			}
		}
		return ""
	}
	return map[string]any{"first": first, "last": last, "name": first + " " + last, "ini": ini(first) + ini(last)}
}

// ---- series and charts

func clampF(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

func makeSeries(r *rng, n, lo, hi int) map[string]any {
	if n < 2 || hi <= lo {
		fail("series: %d numbers between %d and %d", n, lo, hi)
	}
	span := float64(hi - lo)
	base := float64(lo) + span*(.36+.2*r.float())
	v := base
	vals := make([]any, n)
	mn, mx, sum := hi, lo, 0
	for i := range vals {
		v += (base-v)*.3 + (r.float()-.5)*span*.12
		if r.chance(5) { // now and then a bad moment, which the pull toward the middle takes back
			v += span * (.25 + .3*r.float())
		}
		v = clampF(v, float64(lo), float64(hi))
		x := int(math.Round(v))
		vals[i] = x
		mn, mx, sum = min(mn, x), max(mx, x), sum+x
	}
	return map[string]any{"v": vals, "last": vals[n-1], "min": mn, "max": mx, "avg": sum / n, "n": n}
}

func seriesInts(s map[string]any) []int {
	l, ok := s["v"].([]any)
	if !ok {
		fail("chart wants a series")
	}
	out := make([]int, len(l))
	for i, e := range l {
		out[i], _ = e.(int)
	}
	return out
}

func drawChart(r *rng, s map[string]any, kind string, w, h int) string {
	vals := seriesInts(s)
	if len(vals) < 2 || w < 40 || h < 20 {
		fail("chart: %d numbers in %dx%d", len(vals), w, h)
	}
	lo, hi := slices.Min(vals), slices.Max(vals)
	pad := float64(h) * .1
	if kind == "spark" {
		pad = 3
	}
	span := float64(hi - lo)
	if span == 0 {
		span = 1
	}
	y := func(v int) float64 { return pad + (float64(h)-2*pad)*(1-(float64(v)-float64(lo))/span) }
	var b strings.Builder
	b.WriteString(svgOpen(r, fmt.Sprintf(`viewBox="0 0 %d %d"`, w, h), `preserveAspectRatio="none"`, `class="__dchart__"`, `aria-hidden="true"`))
	n := float64(len(vals))
	if kind != "spark" {
		b.WriteString(`<path class="__dgrid__" d="`)
		for i := 1; i <= 4; i++ {
			fmt.Fprintf(&b, "M0 %d H%d", int(float64(h)*float64(i)/5)+r.between(-3, 3), w)
		}
		b.WriteString(`"/>`)
	}
	switch kind {
	case "bars":
		slot := float64(w) / n
		bw := slot * (.52 + .26*r.float())
		var all, last strings.Builder
		for i, v := range vals {
			x, top := float64(i)*slot+(slot-bw)/2, y(v)
			dst := &all
			if i == len(vals)-1 {
				dst = &last
			}
			fmt.Fprintf(dst, "M%.1f %.1fV%dh%.1fV%.1fz", x, float64(h), int(top), bw, top)
		}
		fmt.Fprintf(&b, `<path class="__dbars__" d="%s"/><path class="__dpt__" d="%s"/>`, all.String(), last.String())
	default: // area, spark
		var line strings.Builder
		for i, v := range vals {
			sep := " "
			if i == 0 {
				sep = "M"
			} else if i == 1 {
				sep = "L"
			}
			fmt.Fprintf(&line, "%s%d %d", sep, int(math.Round(float64(i)*float64(w)/(n-1))), int(math.Round(y(v))))
		}
		if kind == "area" {
			fmt.Fprintf(&b, `<path class="__darea__" d="%sV%dH0z"/>`, line.String(), h)
		}
		fmt.Fprintf(&b, `<path class="__dline__" d="%s"/>`, line.String())
		fmt.Fprintf(&b, `<circle class="__dpt__" cx="%d" cy="%d" r="%d"/>`, w-4, int(math.Round(y(vals[len(vals)-1]))), 3)
	}
	return b.String() + "</svg>"
}

// ---- availability

// makeAvail draws n days: each is up (0), degraded (1, warn per mille of days) or down (2, bad per mille), the last calm days
// are up, and the percentage is what the minutes lost add up to, so the bars and the figure tell the same story.
func makeAvail(r *rng, n, warn, bad, calm int) map[string]any {
	if n < 1 || warn < 0 || bad < 0 || calm < 0 {
		fail("avail: %d days, %d, %d, %d", n, warn, bad, calm)
	}
	days := make([]any, n)
	lost, nw, nb := 0, 0, 0
	for i := range days {
		d := 0
		switch roll := r.n(1000); {
		case roll < bad:
			d = 2
		case roll < bad+warn:
			d = 1
		}
		if i >= n-calm {
			d = 0
		}
		switch d {
		case 1:
			nw++
			lost += r.between(6, 40)
		case 2:
			nb++
			lost += r.between(40, 240)
		}
		days[i] = d
	}
	bp := 10000 - (lost*10000+n*1440-1)/(n*1440) // hundredths of a percent, rounded down so that the figure never says more than the days do
	if bp > 9999 && lost > 0 {
		bp = 9999
	}
	return map[string]any{"days": days, "bp": bp, "warn": nw, "bad": nb}
}

// formatBP writes hundredths of a percent as the language writes a decimal: 9998 is "99.98" or "99,98", 10000 is "100".
func formatBP(bp int, lang string) string {
	if bp >= 10000 {
		return "100"
	}
	sep := ","
	if strings.HasPrefix(lang, "en") {
		sep = "."
	}
	return fmt.Sprintf("%d%s%02d", bp/100, sep, bp%100)
}

// ---- calendar heat map

func drawCal(r *rng, items []any, endISO string, weeks int, id string) string {
	end, err := time.Parse("2006-01-02", endISO)
	if err != nil || weeks < 4 || weeks > 80 {
		fail("cal: the end %q, %d weeks", endISO, weeks)
	}
	cell, gap := 11+r.n(4), 2+r.n(2)
	step := cell + gap
	wd := (int(end.Weekday()) + 6) % 7          // Monday is 0
	first := end.AddDate(0, 0, -wd-(weeks-1)*7) // the Monday of the first column
	count := map[[2]int]int{}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		d, err := time.Parse("2006-01-02", str(m["date_iso"]))
		if err != nil || d.Before(first) || d.After(end) {
			continue
		}
		days := int(d.Sub(first).Hours()/24 + .5)
		count[[2]int{days / 7, days % 7}]++
	}
	W, H := weeks*step-gap, 7*step-gap
	rx := r.between(1, cell/4)
	var one, many strings.Builder
	for col := 0; col < weeks; col++ {
		for row := 0; row < 7; row++ {
			n := count[[2]int{col, row}]
			if n == 0 {
				continue
			}
			dst := &one
			if n > 1 {
				dst = &many
			}
			fmt.Fprintf(dst, "M%d %dh%dv%dh-%dz", col*step, row*step, cell, cell, cell)
		}
	}
	var b strings.Builder
	b.WriteString(svgOpen(r, fmt.Sprintf(`viewBox="0 0 %d %d"`, W, H), `class="__dcal__"`, `aria-hidden="true"`))
	fmt.Fprintf(&b, `<defs><pattern id="%s" width="%d" height="%d" patternUnits="userSpaceOnUse"><rect width="%d" height="%d" rx="%d" class="__dc0__"/></pattern></defs><rect width="%d" height="%d" fill="url(#%s)"/>`,
		id, step, step, cell, cell, rx, W, H, id)
	if one.Len() > 0 {
		fmt.Fprintf(&b, `<path class="__dc1__" d="%s"/>`, one.String())
	}
	if many.Len() > 0 {
		fmt.Fprintf(&b, `<path class="__dc2__" d="%s"/>`, many.String())
	}
	return b.String() + "</svg>"
}

// ---- a map of a quarter

func spread(r *rng, n int, size float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = size * (.1 + .8*(float64(i)+.5)/float64(n) + (r.float()-.5)*.6/float64(n))
	}
	return out
}

func drawMap(r *rng, streets []string, w, h int) string {
	W, H := float64(w), float64(h)
	nv, nh, nd := 3+r.n(3), 2+r.n(3), r.n(2)
	for nv+nh+nd < 6 {
		nh++
	}
	for nv+nh+nd > 9 {
		nv--
	}
	xs, ys := spread(r, nv, W), spread(r, nh, H)
	xb, yb := append(append([]float64{0}, xs...), W), append(append([]float64{0}, ys...), H)
	var b strings.Builder
	b.WriteString(svgOpen(r, fmt.Sprintf(`viewBox="0 0 %d %d"`, w, h), `preserveAspectRatio="xMidYMid slice"`, `class="__dmap__"`, `aria-hidden="true"`))
	fmt.Fprintf(&b, `<rect width="%d" height="%d" class="__dmg__"/>`, w, h)
	// blocks, one cell between four streets each; a few are left empty and one or two are a park
	pi, pj := r.n(len(xb)-1), r.n(len(yb)-1)
	var blocks, park, trees strings.Builder
	for i := 0; i+1 < len(xb); i++ {
		for j := 0; j+1 < len(yb); j++ {
			x0, y0, x1, y1 := xb[i]+9, yb[j]+9, xb[i+1]-9, yb[j+1]-9
			if x1-x0 < 14 || y1-y0 < 14 {
				continue
			}
			if i == pi && j == pj {
				fmt.Fprintf(&park, "M%.0f %.0fh%.0fv%.0fh-%.0fz", x0, y0, x1-x0, y1-y0, x1-x0)
				for k, n := 0, 5+r.n(5); k < n; k++ {
					cx, cy, rr := x0+6+r.float()*(x1-x0-12), y0+6+r.float()*(y1-y0-12), 3+r.float()*3.5
					fmt.Fprintf(&trees, "M%.0f %.0fa%.1f %.1f 0 1 0 %.1f 0a%.1f %.1f 0 1 0 -%.1f 0", cx-rr, cy, rr, rr, 2*rr, rr, rr, 2*rr)
				}
				continue
			}
			if r.chance(14) {
				continue
			}
			fmt.Fprintf(&blocks, "M%.0f %.0fh%.0fv%.0fh-%.0fz", x0, y0, x1-x0, y1-y0, x1-x0)
		}
	}
	fmt.Fprintf(&b, `<path class="__dmb__" d="%s"/><path class="__dmp__" d="%s"/><path class="__dmt__" d="%s"/>`, blocks.String(), park.String(), trees.String())
	// the river crosses the quarter and the streets run over it
	ry := H * (.25 + .5*r.float())
	fmt.Fprintf(&b, `<path class="__dmr__" d="M-10 %.0fC%.0f %.0f %.0f %.0f %.0f %.0f"/>`, ry, W*.3, ry+H*(r.float()-.5)*.5, W*.65, ry+H*(r.float()-.5)*.5, W+10, ry+H*(r.float()-.5)*.3)
	var minor, major strings.Builder
	mv, mh := r.n(nv), r.n(nh)
	for i, x := range xs {
		dst := &minor
		if i == mv {
			dst = &major
		}
		fmt.Fprintf(dst, "M%.0f 0V%d", x+(r.float()-.5)*4, h)
	}
	for j, y := range ys {
		dst := &minor
		if j == mh {
			dst = &major
		}
		fmt.Fprintf(dst, "M0 %.0fH%d", y+(r.float()-.5)*4, w)
	}
	if nd > 0 {
		fmt.Fprintf(&minor, "M%.0f 0L%.0f %d", W*(.1+.3*r.float()), W*(.6+.35*r.float()), h)
	}
	fmt.Fprintf(&b, `<path class="__dms__" d="%s"/><path class="__dmm__" d="%s"/>`, minor.String(), major.String())
	// two or three street names of the country, on streets of their own
	names := []string{}
	if len(streets) > 0 {
		for _, i := range r.perm(len(streets)) {
			if len(names) < 2+r.n(2) {
				names = append(names, streets[i])
			}
		}
	}
	lines := r.perm(nv + nh)
	for k, name := range names {
		if k >= len(lines) {
			break
		}
		l := lines[k]
		var size float64
		if l < nv {
			x, y := xs[l], H*(.3+.4*r.float())
			size = clampF(.75*H/(float64(utf8.RuneCountInString(name))*.62), 8, 12)
			fmt.Fprintf(&b, `<text class="__dml__" font-size="%.1f" x="%.0f" y="%.0f" text-anchor="middle" dy=".35em" transform="rotate(-90 %.0f %.0f)">%s</text>`, size, x, y, x, y, xmlEscape(name))
		} else {
			y, x := ys[l-nv], W*(.3+.4*r.float())
			size = clampF(.75*W/(float64(utf8.RuneCountInString(name))*.62), 8, 12)
			fmt.Fprintf(&b, `<text class="__dml__" font-size="%.1f" x="%.0f" y="%.0f" text-anchor="middle" dy=".35em">%s</text>`, size, x, y, xmlEscape(name))
		}
	}
	// the pin stands on a crossing near the middle
	px, py := xs[len(xs)/2], ys[len(ys)/2]
	fmt.Fprintf(&b, `<circle class="__dmh__" cx="%.0f" cy="%.0f" r="%d"/><path class="__dpin__" d="M%.0f %.0fL%.0f %.0fA9 9 0 1 1 %.0f %.0fz"/><circle class="__dpi__" cx="%.0f" cy="%.0f" r="3.5"/>`,
		px, py, 20+r.n(8), px, py, px-8, py-12, px+8, py-12, px, py-17)
	return b.String() + "</svg>"
}

// ---- identicon

func drawIdent(r *rng) string {
	var cells [5][5]bool
	n := 0
	for n < 8 {
		n = 0
		for y := 0; y < 5; y++ {
			for x := 0; x < 3; x++ {
				on := r.chance(52)
				cells[y][x], cells[y][4-x] = on, on
			}
			for x := 0; x < 5; x++ {
				if cells[y][x] {
					n++
				}
			}
		}
	}
	var d strings.Builder
	for y := 0; y < 5; y++ {
		for x := 0; x < 5; x++ {
			if cells[y][x] {
				fmt.Fprintf(&d, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	return svgOpen(r, `viewBox="0 0 5 5"`, `class="__did__"`, `shape-rendering="crispEdges"`, `aria-hidden="true"`) + fmt.Sprintf(`<path class="__di%d__" d="%s"/></svg>`, r.n(4), d.String())
}

// ---- tags

func countTags(items []any, field string) []any {
	count := map[string]int{}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		switch v := m[field].(type) {
		case string:
			if v != "" {
				count[v]++
			}
		case []any:
			for _, e := range v {
				if s := str(e); s != "" {
					count[s]++
				}
			}
		}
	}
	names := make([]string, 0, len(count))
	for k := range count {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		if count[names[i]] != count[names[j]] {
			return count[names[i]] > count[names[j]]
		}
		return names[i] < names[j]
	})
	out := make([]any, len(names))
	for i, k := range names {
		out[i] = map[string]any{"tag": k, "n": count[k]}
	}
	return out
}

// ---- stamp

// drawStamp lays the text once around the rim of a round stamp. The text ends with a separator so that its end does not touch its
// beginning, and its size is the one at which it just fits the circle at the width its letters have, so that a long text is small
// and a short one is spread by the letter spacing (textLength) instead of repeating or running over.
func drawStamp(r *rng, text string, size int, id string) string {
	if size < 40 {
		fail("stamp: size %d", size)
	}
	text = strings.TrimSpace(text)
	if !strings.HasSuffix(text, "·") {
		text += " ·"
	}
	R := float64(size) / 2
	tr := R * (.66 + .04*r.float())
	circ := 2 * math.Pi * tr
	n := float64(utf8.RuneCountInString(text))
	rim := circ * .97
	fs := math.Floor(math.Min(rim/(n*.7), R*.26)*10) / 10 // .7 em is wider than any capital of the system fonts but a few
	var b strings.Builder
	b.WriteString(svgOpen(r, fmt.Sprintf(`viewBox="0 0 %d %d"`, size, size), `class="__dstamp__"`, `aria-hidden="true"`))
	fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%.1f" class="__dsb__"/><circle cx="%.1f" cy="%.1f" r="%.1f" class="__dsd__"/>`,
		R, R, R-1+r.float()*.5, R, R, R*(.8+.04*r.float()))
	fmt.Fprintf(&b, `<defs><path id="%s" d="M%.1f %.1fa%.1f %.1f 0 1 1 %.1f 0a%.1f %.1f 0 1 1 -%.1f 0"/></defs>`, id, R-tr, R, tr, tr, 2*tr, tr, tr, 2*tr)
	fmt.Fprintf(&b, `<text class="__dst__" font-size="%.1f"><textPath href="#%s" textLength="%.1f" lengthAdjust="spacing">%s</textPath></text>`, fs, id, rim, xmlEscape(text))
	// the middle: a small figure of the stamp's own
	switch r.n(3) {
	case 0:
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="%.1f" class="__dsm__"/>`, R, R, R*(.16+.1*r.float()))
	case 1:
		k := R * (.2 + .1*r.float())
		fmt.Fprintf(&b, `<path class="__dsm__" d="M%.1f %.1fL%.1f %.1fL%.1f %.1fL%.1f %.1fz"/>`, R, R-k, R+k, R, R, R+k, R-k, R)
	default:
		k, j := R*(.28+.08*r.float()), R*(.05+.03*r.float())
		fmt.Fprintf(&b, `<path class="__dsm__" d="M%.1f %.1fh%.1fv%.1fh%.1fv%.1fh-%.1fv%.1fh-%.1fv-%.1fh%.1fz"/>`, R-j, R-k, 2*j, k-j, k-j, 2*j, k-j, k-j, 2*j, k-j, k-j)
	}
	return b.String() + "</svg>"
}

// ---- subjects of the pictures

// drawSubject draws one of the subjectKinds into b, and says whether the kind is one of them.
func drawSubject(b *strings.Builder, r *rng, kind string, hue, W, H float64, id string) bool {
	fw := func(lo, hi float64) float64 { return lo + r.float()*(hi-lo) }
	rect := func(x, y, w, h float64, fill string) {
		fmt.Fprintf(b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" fill="%s"/>`, x, y, w, h, fill)
	}
	bg := func(fill string) { rect(0, 0, W, H, fill) }
	switch kind {
	case "topo":
		bg(hsl(hue+fw(-8, 8), fw(25, 40), fw(90, 95)))
		for c, nc := 0, 1+r.n(2); c < nc; c++ {
			cx, cy := fw(.25, .75)*W, fw(.25, .75)*H
			rings := 7 + r.n(5)
			if c > 0 {
				rings = 4 + r.n(3)
			}
			step := math.Min(W, H) * fw(.04, .062)
			f1, f2 := float64(2+r.n(2)), float64(3+r.n(3))
			a1, a2, p1, p2 := fw(.06, .14), fw(.03, .08), fw(0, 6.28), fw(0, 6.28)
			for k := rings; k >= 1; k-- {
				rad := float64(k) * step
				fmt.Fprintf(b, `<path fill="%s" stroke="%s" stroke-width="%.1f" d="`, hsl(hue+float64(k)*2.5, 30, 93-float64(k)*1.2), hsl(hue, 34, fw(46, 56)), fw(.9, 1.5))
				for i := 0; i <= 28; i++ {
					th := 2 * math.Pi * float64(i) / 28
					rr := rad * (1 + a1*math.Sin(f1*th+p1) + a2*math.Sin(f2*th+p2))
					sep := " "
					if i == 0 {
						sep = "M"
					} else if i == 1 {
						sep = "L"
					}
					fmt.Fprintf(b, "%s%.0f %.0f", sep, cx+rr*math.Cos(th), cy+rr*math.Sin(th))
				}
				b.WriteString(`z"/>`)
			}
		}
	case "stars":
		bg(hsl(hue+fw(-15, 15), fw(40, 55), fw(10, 16)))
		fmt.Fprintf(b, `<g fill="%s">`, hsl(hue, 30, fw(88, 96)))
		for i, n := 0, 40+r.n(30); i < n; i++ {
			fmt.Fprintf(b, `<circle cx="%.0f" cy="%.0f" r="%.1f" opacity="%.2f"/>`, fw(0, 1)*W, fw(0, 1)*H, fw(.5, 2.2), fw(.45, 1))
		}
		b.WriteString(`</g>`)
		fmt.Fprintf(b, `<g fill="%s">`, hsl(hue+fw(30, 60), 85, fw(62, 72)))
		for i, n := 0, 2+r.n(3); i < n; i++ {
			x, y, k := fw(.08, .92)*W, fw(.08, .6)*H, fw(5, 11)
			fmt.Fprintf(b, `<path d="M%.0f %.0fQ%.0f %.0f %.0f %.0fQ%.0f %.0f %.0f %.0fQ%.0f %.0f %.0f %.0fQ%.0f %.0f %.0f %.0fz"/>`, x, y-2*k, x, y, x+2*k, y, x, y, x, y+2*k, x, y, x-2*k, y, x, y, x, y-2*k)
		}
		b.WriteString(`</g>`)
		mx, my, mr := fw(.55, .85)*W, fw(.2, .45)*H, math.Min(W, H)*fw(.12, .2)
		fmt.Fprintf(b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="%s"/><circle cx="%.0f" cy="%.0f" r="%.0f" fill="%s"/>`, mx, my, mr, hsl(hue, 30, fw(88, 94)), mx+mr*.38, my-mr*.2, mr*.94, hsl(hue+fw(-15, 15), 45, 12))
		fmt.Fprintf(b, `<ellipse cx="%.0f" cy="%.0f" rx="%.0f" ry="%.0f" transform="rotate(%.0f %.0f %.0f)" fill="none" stroke="%s" stroke-width="1.5"/>`, fw(.12, .3)*W, fw(.65, .85)*H, mr*1.1, mr*.3, fw(-30, 30), W*.2, H*.75, hsl(hue, 40, 70))
	case "botanical":
		bg(hsl(hue+fw(10, 40), 30, fw(90, 95)))
		green := 120 + (hue-120)*.25
		for s, ns := 0, 2+r.n(2); s < ns; s++ {
			x0, y0 := fw(.1, .9)*W, H*1.02
			x1, y1 := x0+fw(-.2, .2)*W, fw(.12, .45)*H
			cx, cy := x0+fw(-.25, .25)*W, (y0+y1)/2
			fmt.Fprintf(b, `<path d="M%.0f %.0fQ%.0f %.0f %.0f %.0f" fill="none" stroke="%s" stroke-width="%.1f"/>`, x0, y0, cx, cy, x1, y1, hsl(green, 30, fw(26, 34)), fw(1.6, 3))
			for k, nl := 0, 6+r.n(4); k < nl; k++ {
				t := .22 + .78*float64(k)/float64(nl)
				px := (1-t)*(1-t)*x0 + 2*t*(1-t)*cx + t*t*x1
				py := (1-t)*(1-t)*y0 + 2*t*(1-t)*cy + t*t*y1
				side := float64(1 - 2*(k%2))
				ang := side * fw(28, 70)
				ll := math.Min(W, H) * fw(.07, .12) * (1.15 - t*.5)
				fmt.Fprintf(b, `<ellipse cx="%.0f" cy="%.0f" rx="%.0f" ry="%.0f" transform="rotate(%.0f %.0f %.0f)" fill="%s"/>`, px+side*ll*.7, py-ll*.2, ll, ll*fw(.3, .45), ang-side*20, px, py, hsl(green+fw(-12, 14), fw(28, 45), fw(30, 50)))
			}
		}
	case "still":
		bg(hsl(hue, fw(25, 38), fw(86, 91)))
		ty := H * fw(.62, .7)
		rect(0, ty, W, H-ty, hsl(hue+fw(10, 25), fw(30, 40), fw(62, 72)))
		vx, vw, vh := fw(.22, .42)*W, W*fw(.14, .2), H*fw(.36, .5)
		fmt.Fprintf(b, `<ellipse cx="%.0f" cy="%.0f" rx="%.0f" ry="%.0f" fill="%s" opacity=".18"/>`, vx+vw*.7, ty+H*.025, vw*.9, H*.025, hsl(hue, 30, 10))
		vc := hsl(hue+fw(-20, 20), fw(45, 65), fw(38, 50))
		fmt.Fprintf(b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="4" fill="%s"/><ellipse cx="%.0f" cy="%.0f" rx="%.0f" ry="%.0f" fill="%s"/>`,
			vx+vw*.35, ty-vh, vw*.3, vh*.4, vc, vx+vw/2, ty-vh*.32, vw*.55, vh*.32, vc)
		for i, n := 0, 2+r.n(2); i < n; i++ {
			fr := math.Min(W, H) * fw(.06, .1)
			fx := vx + vw*1.7 + float64(i)*fr*1.5 + fw(0, fr*.3)
			fmt.Fprintf(b, `<ellipse cx="%.0f" cy="%.0f" rx="%.0f" ry="%.0f" fill="%s" opacity=".2"/><circle cx="%.0f" cy="%.0f" r="%.0f" fill="%s"/>`, fx+fr*.4, ty+fr*.1, fr, fr*.25, hsl(hue, 30, 10), fx, ty-fr*.85+float64(i%2)*fr*.2, fr, hsl(hue+fw(-50, 60), fw(55, 75), fw(48, 62)))
		}
	case "poster":
		bg(hsl(hue+fw(-10, 10), fw(60, 80), fw(48, 60)))
		cols := []string{hsl(hue+180, fw(55, 70), fw(50, 62)), hsl(hue, 30, fw(92, 96)), hsl(hue, 50, fw(10, 16)), hsl(hue+40, fw(70, 85), fw(55, 65))}
		for i, n := 0, 4+r.n(3); i < n; i++ {
			c := cols[r.n(len(cols))]
			switch r.n(4) {
			case 0:
				fmt.Fprintf(b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="%s"/>`, fw(0, 1)*W, fw(0, 1)*H, math.Min(W, H)*fw(.12, .36), c)
			case 1:
				rect(fw(0, .8)*W, 0, W*fw(.05, .14), H, c)
			case 2:
				x, y, rr := fw(0, 1)*W, fw(0, 1)*H, math.Min(W, H)*fw(.15, .4)
				fmt.Fprintf(b, `<path d="M%.0f %.0fa%.0f %.0f 0 0 1 %.0f 0z" fill="%s"/>`, x-rr, y, rr, rr, 2*rr, c)
			default:
				x, y, k := fw(0, .8)*W, fw(0, .8)*H, math.Min(W, H)*fw(.15, .35)
				fmt.Fprintf(b, `<path d="M%.0f %.0fL%.0f %.0fL%.0f %.0fz" fill="%s"/>`, x, y+k, x+k, y+k, x+k*fw(0, 1), y, c)
			}
		}
	case "ui":
		bg(hsl(hue, fw(30, 45), fw(90, 95)))
		wx, wy, ww, wh := W*.07, H*.1, W*.86, H*.8
		fmt.Fprintf(b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="%.0f" fill="%s" stroke="%s"/>`, wx, wy, ww, wh, fw(8, 16), hsl(hue, 20, 99), hsl(hue, 25, 80))
		for i := 0; i < 3; i++ {
			fmt.Fprintf(b, `<circle cx="%.0f" cy="%.0f" r="3.5" fill="%s"/>`, wx+16+float64(i)*14, wy+16, hsl(hue+float64(i)*20, 55, 70))
		}
		sw := ww * fw(.2, .27)
		rect(wx, wy+32, sw, wh-32, hsl(hue, 30, 94))
		for i, n := 0, 4+r.n(3); i < n; i++ {
			fmt.Fprintf(b, `<rect x="%.0f" y="%.0f" width="%.0f" height="8" rx="4" fill="%s"/>`, wx+14, wy+52+float64(i)*22, sw*fw(.4, .75), hsl(hue, 25, fw(72, 82)))
		}
		mx := wx + sw + 20
		mw := ww - sw - 40
		fmt.Fprintf(b, `<rect x="%.0f" y="%.0f" width="%.0f" height="14" rx="7" fill="%s"/>`, mx, wy+48, mw*fw(.3, .55), hsl(hue, 40, fw(30, 40)))
		cw := (mw - 24) / 3
		for i := 0; i < 3; i++ {
			x := mx + float64(i)*(cw+12)
			fmt.Fprintf(b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="10" fill="%s"/><rect x="%.0f" y="%.0f" width="%.0f" height="7" rx="3.5" fill="%s"/>`, x, wy+80, cw, wh*.2, hsl(hue+float64(i)*14, fw(45, 65), fw(88, 93)), x+10, wy+92, cw*fw(.3, .6), hsl(hue, 30, 55))
		}
		cy := wy + 80 + wh*.2 + 24
		fmt.Fprintf(b, `<path d="M%.0f %.0f`, mx, wy+wh-24)
		for i := 1; i <= 8; i++ {
			fmt.Fprintf(b, `L%.0f %.0f`, mx+mw*float64(i)/8, cy+fw(0, 1)*(wy+wh-24-cy))
		}
		fmt.Fprintf(b, `" fill="none" stroke="%s" stroke-width="2.5" stroke-linejoin="round"/>`, hsl(hue, 60, 48))
	case "parcel":
		bg(hsl(hue, fw(15, 25), fw(88, 93)))
		bw, bh := W*fw(.5, .62), H*fw(.5, .62)
		bx, by := (W-bw)/2, (H-bh)/2
		fmt.Fprintf(b, `<g transform="rotate(%.1f %.0f %.0f)"><rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="4" fill="%s"/>`, fw(-8, 8), W/2, H/2, bx+8, by+10, bw, bh, hsl(30, 25, 10))
		fmt.Fprintf(b, `<rect x="%.0f" y="%.0f" width="%.0f" height="%.0f" rx="4" fill="%s"/>`, bx, by, bw, bh, hsl(fw(30, 38), fw(38, 50), fw(55, 66)))
		tw := bw * fw(.1, .16)
		rect(bx+bw*fw(.4, .55), by, tw, bh, hsl(34, 35, fw(70, 78)))
		lw, lh := bw*fw(.34, .42), bh*fw(.3, .38)
		lx, ly := bx+bw*fw(.06, .14), by+bh*fw(.45, .6)
		rect(lx, ly, lw, lh, hsl(40, 20, 98))
		x := lx + 8
		for x < lx+lw-10 {
			w := fw(1, 3.5)
			rect(x, ly+lh*.45, w, lh*.4, hsl(30, 10, 15))
			x += w + fw(1.5, 3.5)
		}
		rect(lx+8, ly+8, lw*fw(.3, .6), 4, hsl(30, 10, 30))
		fmt.Fprintf(b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="none" stroke="%s" stroke-width="2.5"/></g>`, bx+bw*fw(.7, .85), by+bh*fw(.2, .3), bw*fw(.07, .1), hsl(hue, 60, 48))
	case "checker":
		cols := 6 + r.n(4)
		s := W / float64(cols)
		bg(hsl(hue, fw(20, 30), fw(92, 96)))
		fmt.Fprintf(b, `<path fill="%s" d="`, hsl(hue, fw(40, 55), fw(32, 46)))
		for y := 0; float64(y)*s < H; y++ {
			for x := 0; x < cols; x++ {
				if (x+y)%2 == 0 {
					fmt.Fprintf(b, "M%.0f %.0fh%.0fv%.0fh-%.0fz", float64(x)*s, float64(y)*s, s+1, s+1, s+1)
				}
			}
		}
		b.WriteString(`"/>`)
		for i, n := 0, 2+r.n(2); i < n; i++ {
			fmt.Fprintf(b, `<circle cx="%.0f" cy="%.0f" r="%.0f" fill="%s"/>`, (float64(r.n(cols))+.5)*s, (float64(r.n(int(H/s)))+.5)*s, s*fw(.3, .4), hsl(hue+fw(150, 210), fw(55, 75), fw(48, 62)))
		}
	case "staff":
		bg(hsl(hue, fw(20, 30), fw(92, 96)))
		ink := hsl(hue, 20, fw(14, 24))
		gap := H * fw(.026, .036)
		for sy, ns := H*fw(.16, .24), 2+r.n(2); ns > 0; ns-- {
			fmt.Fprintf(b, `<path fill="none" stroke="%s" stroke-width="1.2" d="`, ink)
			for i := 0; i < 5; i++ {
				fmt.Fprintf(b, "M%.0f %.0fH%.0f", W*.06, sy+float64(i)*gap, W*.94)
			}
			fmt.Fprintf(b, `M%.0f %.0fV%.0fM%.0f %.0fV%.0f"/>`, W*.06, sy, sy+4*gap, W*.94, sy, sy+4*gap)
			x := W * .16
			for x < W*.9 {
				ny := sy + float64(r.n(9)-2)*gap/2
				fmt.Fprintf(b, `<ellipse cx="%.0f" cy="%.0f" rx="%.1f" ry="%.1f" transform="rotate(-20 %.0f %.0f)" fill="%s"/><path d="M%.0f %.0fV%.0f" stroke="%s" stroke-width="1.4"/>`, x, ny, gap*.62, gap*.45, x, ny, ink, x+gap*.55, ny, ny-gap*3.2, ink)
				x += W * fw(.045, .09)
				if r.chance(14) {
					fmt.Fprintf(b, `<path d="M%.0f %.0fV%.0f" stroke="%s" stroke-width="1.2"/>`, x-W*.02, sy, sy+4*gap, ink)
				}
			}
			fmt.Fprintf(b, `<path d="M%.0f %.0fc%.0f %.0f %.0f %.0f %.0f %.0f" fill="none" stroke="%s" stroke-width="2"/>`, W*.1, sy+gap*5, -gap*3, -gap*3, gap*2, -gap*8, gap*2, -gap*3, ink)
			sy += gap * 9
		}
	default:
		return false
	}
	return true
}

// ---- the calendar of months

func (c *ctx) calMonths(items []any, endISO string, n int) []any {
	end, err := time.Parse("2006-01-02", endISO)
	if err != nil || n < 1 || n > 24 {
		fail("calmonths: the end %q, %d months", endISO, n)
	}
	kind := map[string]int{}
	for _, it := range items {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		k := map[string]int{"minor": 1, "major": 2, "maint": 3}[str(m["impact"])]
		if d := str(m["date_iso"]); k > kind[d] || kind[d] == 0 {
			kind[d] = max(k, 1)
		}
	}
	out := make([]any, 0, n)
	first := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		m0 := first.AddDate(0, -i, 0)
		days := make([]any, 0, 31)
		for d := m0; d.Month() == m0.Month(); d = d.AddDate(0, 0, 1) {
			k := kind[d.Format("2006-01-02")]
			if d.After(end) {
				k = 4
			}
			days = append(days, map[string]any{"n": d.Day(), "k": k})
		}
		out = append(out, map[string]any{"label": c.s.fmonth(m0), "pad": (int(m0.Weekday()) + 6) % 7, "days": days})
	}
	return out
}

func makeTimes(r *rng, n int) []any {
	if n < 1 || n > 12 {
		fail("times: %d times", n)
	}
	t := r.between(6*60, 20*60)
	out := make([]any, n)
	for i := range out {
		out[i] = fmt.Sprintf("%02d:%02d", t/60%24, t%60)
		t += r.between(8, 45)
	}
	return out
}

func (c *ctx) fsize(r *rng, lo, hi int) string {
	kb := r.between(lo, hi)
	units := c.s.textList("units")
	if len(units) < 3 {
		fail("fsize: the language has no units")
	}
	sep := ","
	if strings.HasPrefix(c.s.country.Lang, "en") {
		sep = "."
	}
	switch {
	case kb >= 1024:
		return fmt.Sprintf("%d%s%d %s", kb/1024, sep, kb%1024*10/1024, str(units[2]))
	}
	return fmt.Sprintf("%d %s", kb, str(units[1]))
}
