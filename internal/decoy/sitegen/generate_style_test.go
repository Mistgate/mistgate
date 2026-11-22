//go:build !js

package sitegen

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"testing"
)

func hexOf(t *testing.T, s string) [3]float64 {
	t.Helper()
	c, ok := parseHex(s)
	if !ok {
		t.Fatalf("%q is not a colour", s)
	}
	return c
}

func eachKind(t *testing.T, f func(id string, d *kindDef)) {
	t.Helper()
	kinds, err := loadKinds()
	if err != nil {
		t.Fatal(err)
	}
	for id, d := range kinds {
		f(id, d)
	}
}

// Every palette of every kind reads: the text on the page and on its surfaces, the muted text, the text on the accent; and a dark
// scheme is a clean dark, not a warm mud (the page not lighter than a surface, a line that can be seen, no brown or olive tint).
func TestPalettesReadable(t *testing.T) {
	eachKind(t, func(id string, d *kindDef) {
		sd := d.meta.Style
		if sd == nil {
			t.Errorf("%s: no style in meta.json", id)
			return
		}
		for i, p := range sd.pal {
			check := func(scheme string, tn tone, dark bool) {
				name := fmt.Sprintf("%s palette %d (%s) %s", id, i, sd.Palettes[i].Name, scheme)
				bg, sf, band, fg := hexOf(t, tn.BG), hexOf(t, tn.Surface), hexOf(t, tn.Band), hexOf(t, tn.FG)
				mu, ac, ai := hexOf(t, tn.Muted), hexOf(t, tn.Acc), hexOf(t, tn.AccInk)
				if c := contrast(fg, bg); c < 7 {
					t.Errorf("%s: text on the page %.2f:1, want 7", name, c)
				}
				if c := contrast(fg, sf); c < 7 {
					t.Errorf("%s: text on a surface %.2f:1, want 7", name, c)
				}
				for what, on := range map[string][3]float64{"page": bg, "surface": sf, "band": band} {
					if c := contrast(mu, on); c < 4.5 {
						t.Errorf("%s: muted text on the %s %.2f:1, want 4.5", name, what, c)
					}
				}
				if c := contrast(ai, ac); c < 4.5 {
					t.Errorf("%s: text on the accent %.2f:1, want 4.5", name, c)
				}
				if c := contrast(hexOf(t, tn.Line), bg); c < 1.08 {
					t.Errorf("%s: the line cannot be seen on the page (%.2f:1)", name, c)
				}
				for what, col := range map[string]string{"ok": tn.Ok, "warn": tn.Warn, "bad": tn.Bad} {
					if c := contrast(hexOf(t, col), bg); c < 3 {
						t.Errorf("%s: the %s colour on the page %.2f:1, want 3", name, what, c)
					}
				}
				if !dark {
					return
				}
				h, s, l := toHSL(bg)
				if l > 14 {
					t.Errorf("%s: a dark page with lightness %.0f%%", name, l)
				}
				if s > 12 && h >= 15 && h <= 100 {
					t.Errorf("%s: a dark page tinted brown or olive (hue %.0f, saturation %.0f%%)", name, h, s)
				}
				if lum(sf) < lum(bg) {
					t.Errorf("%s: a surface darker than the page", name)
				}
				for _, nt := range []string{tn.Surface, tn.Band, tn.Surface2, tn.Line} {
					if _, s2, _ := toHSL(hexOf(t, nt)); s2 > math.Max(s, 12)+30 {
						t.Errorf("%s: the neutral %s is much more saturated than the page", name, nt)
					}
				}
			}
			if sd.Scheme != "dark" {
				check("light", p.L, false)
			}
			if sd.Scheme != "light" {
				check("dark", p.D, true)
			}
		}
	})
}

// A kind's style is a small set of choices that belong together: how many, and that a site in Cyrillic has a font set of its own.
func TestStyleConfigs(t *testing.T) {
	eachKind(t, func(id string, d *kindDef) {
		sd := d.meta.Style
		if sd == nil {
			return
		}
		if n := len(sd.Palettes); n < 5 || n > 8 {
			t.Errorf("%s: %d palettes, want 5 to 8", id, n)
		}
		if n := len(sd.Fonts); n < 2 || n > 3 {
			t.Errorf("%s: %d font sets, want 2 or 3", id, n)
		}
		if d.meta.Layouts < 2 || d.meta.Layouts > 3 {
			t.Errorf("%s: %d layouts, want 2 or 3", id, d.meta.Layouts)
		}
		cyr := 0
		for _, f := range sd.Fonts {
			if f.cyrillic() {
				cyr++
			}
		}
		if cyr == 0 {
			t.Errorf("%s: no font set for a site in Cyrillic", id)
		}
		names := map[string]bool{}
		for _, p := range sd.Palettes {
			if names[p.Name] || p.Name == "" {
				t.Errorf("%s: palette name %q is empty or repeated", id, p.Name)
			}
			names[p.Name] = true
		}
	})
}

var cssVar = func(css, name string) string {
	m := regexp.MustCompile(`--` + name + `:([^;}]*)`).FindStringSubmatch(css)
	if m == nil {
		return ""
	}
	return m[1]
}

// The redesigned look reaches the stylesheet: tokens, the type scale, and a colour scheme that is the kind's own. Every kind is
// made here in its new style (the kinds that are not redesigned yet are "legacy" in meta.json).
func TestStylesEmitTokens(t *testing.T) {
	eachKind(t, func(id string, d *kindDef) {
		sd := d.meta.Style
		if sd == nil {
			return
		}
		was := sd.Legacy
		sd.Legacy = false
		defer func() { sd.Legacy = was }()
		for i := 0; i < 12; i++ {
			cc := DefaultCountry
			if i%4 == 3 {
				cc = "ru"
			}
			files, _, err := GenerateWith(id, testSeed(i), Options{Country: cc})
			if err != nil {
				t.Fatalf("%s seed %d: %v", id, i, err)
			}
			var css string
			for _, f := range files {
				if strings.HasSuffix(f.Path, ".css") {
					css = string(f.Content)
				}
				if strings.HasSuffix(f.Path, ".html") {
					checkPage(t, fmt.Sprintf("%s seed %d %s", id, i, f.Path), f.Content)
				}
			}
			for _, v := range []string{"t0", "t1", "t2", "t3", "t4", "t5", "r", "rb", "wt", "sec", "fl", "fwh", "lsh", "band", "sf2", "ln2", "oks"} {
				if strings.TrimSpace(cssVar(css, v)) == "" {
					t.Errorf("%s seed %d: no --%s in the stylesheet", id, i, v)
				}
			}
			dark := strings.Contains(css, "prefers-color-scheme") // loosen() may add spaces; the word is enough
			switch sd.Scheme {
			case "light":
				if dark || !regexp.MustCompile(`color-scheme:\s*light\s*;`).MatchString(css) {
					t.Errorf("%s seed %d: a light kind with a dark media query or without color-scheme: light", id, i)
				}
			case "dark":
				if dark || !regexp.MustCompile(`color-scheme:\s*dark\s*;`).MatchString(css) {
					t.Errorf("%s seed %d: a dark kind with a media query or without color-scheme: dark", id, i)
				}
			default:
				if !dark || !regexp.MustCompile(`color-scheme:\s*light dark\s*;`).MatchString(css) {
					t.Errorf("%s seed %d: a kind with both schemes lacks the media query or color-scheme", id, i)
				}
			}
			for _, f := range files {
				if strings.HasSuffix(f.Path, ".html") && strings.Contains(string(f.Content), `name="color-scheme" content="`) {
					want := colorScheme(sd.Scheme)
					if !strings.Contains(string(f.Content), `name="color-scheme" content="`+want+`"`) {
						t.Errorf("%s seed %d %s: color-scheme meta is not %q", id, i, f.Path, want)
					}
				}
			}
		}
	})
}

func TestTypeScale(t *testing.T) {
	ts, small := typeScale(17, 1.333)
	if ts[0] != "17px" || !strings.HasPrefix(ts[3], "clamp(") || !strings.HasSuffix(ts[5], "px)") || small == "" {
		t.Fatalf("scale %v %s", ts, small)
	}
	if !strings.Contains(ts[2], "px") || strings.Contains(ts[2], "clamp") {
		t.Fatalf("small steps are plain sizes: %v", ts)
	}
}
