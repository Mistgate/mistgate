package instance

import (
	"context"
	"encoding/xml"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func sp(s string) *string { return &s }

func TestSanitizeKeepsALogo(t *testing.T) {
	in := `<?xml version="1.0" encoding="UTF-8"?>
<!-- made in a drawing tool -->
<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" xmlns:inkscape="http://www.inkscape.org/namespaces/inkscape"
     viewBox="0 0 64 64" width="64" height="64" inkscape:version="1.3" onload="alert(1)">
  <title>Logo &amp; mark</title>
  <defs>
    <linearGradient id="g1" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#fff"/><stop offset="1" stop-color="#693fc2" stop-opacity=".5"/></linearGradient>
    <clipPath id="c"><circle cx="32" cy="32" r="30"/></clipPath>
  </defs>
  <g clip-path="url(#c)" transform="rotate(10 32 32)">
    <path d="M0 0L64 64" fill="url(#g1)" stroke="none" style="fill-rule:evenodd"/>
    <rect x="1" y="2" width="3" height="4" rx="1" fill="#cba5fa"/>
  </g>
  <text x="2" y="60" font-family="sans-serif" font-size="8">mist<tspan fill="#b8acf2">gate</tspan></text>
</svg>`
	out, err := SanitizeSVG(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64">`, `<title>Logo &amp; mark</title>`,
		`<linearGradient id="g1" x1="0" y1="0" x2="1" y2="1">`, `<stop offset="1" stop-color="#693fc2" stop-opacity=".5">`,
		`<clipPath id="c"><circle cx="32" cy="32" r="30"></circle></clipPath>`, `clip-path="url(#c)"`, `fill="url(#g1)"`,
		`style="fill-rule:evenodd"`, `<text x="2" y="60" font-family="sans-serif" font-size="8">mist<tspan fill="#b8acf2">gate</tspan></text>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lost %q\n%s", want, out)
		}
	}
	for _, gone := range []string{"onload", "inkscape", "xlink", "<?xml", "<!--", "alert"} {
		if strings.Contains(out, gone) {
			t.Errorf("kept %q\n%s", gone, out)
		}
	}
	// The output is well-formed XML and sanitising is idempotent.
	d := xml.NewDecoder(strings.NewReader(out))
	for {
		if _, err := d.Token(); err != nil {
			if err.Error() != "EOF" {
				t.Errorf("output is not XML: %v", err)
			}
			break
		}
	}
	if again, err := SanitizeSVG(out); err != nil || again != out {
		t.Errorf("not idempotent: %v\n%s\n%s", err, out, again)
	}
}

func TestSanitizeStripsAttacks(t *testing.T) {
	wrap := func(inner string) string {
		return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10">` + inner + `</svg>`
	}
	for name, tc := range map[string]struct {
		in   string
		gone []string
	}{
		"script":                {wrap(`<script>alert(1)</script><path d="M0 0"/>`), []string{"script", "alert"}},
		"script in group":       {wrap(`<g><script type="text/javascript">steal()</script></g>`), []string{"script", "steal"}},
		"foreignObject":         {wrap(`<foreignObject width="10" height="10"><body xmlns="http://www.w3.org/1999/xhtml"><img src="x" onerror="alert(1)"/><iframe src="//evil"/></body></foreignObject>`), []string{"foreignObject", "iframe", "onerror", "evil"}},
		"event handlers":        {wrap(`<path d="M0 0" onclick="x()" onmouseover="y()" onfocus="z()" OnLoad="w()"/>`), []string{"onclick", "onmouseover", "onfocus", "onload", "()"}},
		"javascript href":       {wrap(`<a href="javascript:alert(1)"><path d="M0 0"/></a><a xlink:href="javascript:alert(1)">x</a>`), []string{"javascript", "href", "<a"}},
		"external image":        {wrap(`<image href="https://evil.example/x.png" width="1" height="1"/><image href="data:image/png;base64,AAAA"/>`), []string{"image", "evil", "data:"}},
		"use with external ref": {wrap(`<use href="https://evil.example/sprite.svg#x"/><use xlink:href="#a"/>`), []string{"use", "evil", "sprite"}},
		"external fill":         {wrap(`<path d="M0 0" fill="url(https://evil.example/p.svg#g)" stroke="url('http://evil/x')"/>`), []string{"evil", "url("}},
		"fill url spelled odd":  {wrap(`<path d="M0 0" fill="URL( 'https://evil/x' )" stroke="uRl(//evil/x)"/>`), []string{"evil", "rl("}},
		"style import":          {wrap(`<style>@import url(https://evil.example/a.css); path{fill:red}</style><path d="M0 0" style="background:url(//evil/x)"/>`), []string{"style", "import", "evil"}},
		"style expression":      {wrap(`<path d="M0 0" style="width:expression(alert(1))"/><path d="M1 1" style="-moz-binding:url(x)"/><path d="M2 2" style="position:fixed;inset:0"/>`), []string{"expression", "binding", "position"}},
		"animation":             {wrap(`<path d="M0 0"><animate attributeName="href" values="javascript:alert(1)"/></path><set attributeName="onload" to="alert(1)"/><animateTransform/>`), []string{"animate", "set", "alert"}},
		"switch and filter":     {wrap(`<switch><path d="M0 0"/></switch><filter id="f"><feImage href="http://evil/"/></filter><path d="M1 1" filter="url(#f)"/>`), []string{"switch", "filter", "feImage", "evil"}},
		"other namespace":       {wrap(`<x:thing xmlns:x="urn:evil"><x:script/></x:thing><html:b xmlns:html="http://www.w3.org/1999/xhtml">x</html:b>`), []string{"thing", "evil", "html"}},
		"namespaced attribute":  {wrap(`<path d="M0 0" xlink:href="javascript:alert(1)" xml:base="http://evil/" foo:bar="x" xmlns:foo="urn:foo"/>`), []string{"xlink", "base", "foo", "javascript"}},
		"id with markup":        {wrap(`<path id="a&quot; onload=&quot;alert(1)" d="M0 0"/>`), []string{"alert", "onload"}},
		"data in text":          {wrap(`<text>hi<script>x</script></text>`), []string{"script", "<script"}},
	} {
		out, err := SanitizeSVG(tc.in)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		low := strings.ToLower(out)
		for _, g := range tc.gone {
			if strings.Contains(low, strings.ToLower(g)) {
				t.Errorf("%s: %q survived\n%s", name, g, out)
			}
		}
	}
}

func TestSanitizeRejects(t *testing.T) {
	big := `<svg xmlns="http://www.w3.org/2000/svg"><path d="` + strings.Repeat("M0 0 ", MaxLogoBytes/5) + `"/></svg>`
	deep := strings.Repeat("<g>", maxSVGDepth+1) + strings.Repeat("</g>", maxSVGDepth+1)
	many := strings.Repeat("<path/>", maxSVGNodes+1)
	for name, in := range map[string]string{
		"empty":            "",
		"not xml":          "hello",
		"not svg":          `<html xmlns="http://www.w3.org/1999/xhtml"><body/></html>`,
		"svg in a wrapper": `<div><svg xmlns="http://www.w3.org/2000/svg"/></div>`,
		"foreign root":     `<svg xmlns="urn:evil"/>`,
		"two roots":        `<svg xmlns="http://www.w3.org/2000/svg"/><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"doctype":          `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd"><svg xmlns="http://www.w3.org/2000/svg"/>`,
		"entity bomb":      `<!DOCTYPE svg [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;">]><svg xmlns="http://www.w3.org/2000/svg"><text>&b;</text></svg>`,
		"undefined entity": `<svg xmlns="http://www.w3.org/2000/svg"><text>&nbsp;</text></svg>`,
		"unclosed":         `<svg xmlns="http://www.w3.org/2000/svg"><g>`,
		"mismatched":       `<svg xmlns="http://www.w3.org/2000/svg"><g></path></svg>`,
		"too big":          big,
		"too deep":         `<svg xmlns="http://www.w3.org/2000/svg">` + deep + `</svg>`,
		"too many":         `<svg xmlns="http://www.w3.org/2000/svg">` + many + `</svg>`,
		"other encoding":   `<?xml version="1.0" encoding="ISO-8859-1"?><svg xmlns="http://www.w3.org/2000/svg"/>`,
	} {
		out, err := SanitizeSVG(in)
		if !errors.Is(err, ErrInvalid) || out != "" {
			t.Errorf("%s: %q, %v", name, out, err)
		}
	}
}

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDefaultsAndUpdate(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	s, err := Load(ctx, st)
	if err != nil || s != Defaults() || s.BrandName() != "Mistgate" || s.LogoSVG != "" || s.Accent != "#b8acf2" || s.Language != "en" {
		t.Fatalf("defaults: %+v %v", s, err)
	}
	// A partial update changes only what it names.
	s, err = Update(ctx, st, Patch{BrandTail: sp("wake"), Accent: sp("#7DD3A0")})
	if err != nil || s.BrandHead != "Mist" || s.BrandTail != "wake" || s.Accent != "#7dd3a0" || s.Language != "en" {
		t.Fatalf("partial update: %+v %v", s, err)
	}
	logo := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1 1"><path d="M0 0" onclick="x()"/><script>1</script></svg>`
	s, err = Update(ctx, st, Patch{Language: sp("ru"), LogoSVG: &logo})
	if err != nil || s.Language != "ru" || strings.Contains(s.LogoSVG, "script") || strings.Contains(s.LogoSVG, "onclick") || !strings.Contains(s.LogoSVG, "<path") {
		t.Fatalf("logo update: %+v %v", s, err)
	}
	got, _ := Load(ctx, st)
	if got != s {
		t.Fatalf("not persisted: %+v vs %+v", got, s)
	}
	// An empty head is rejected; an empty tail is fine; "" removes the logo.
	if _, err := Update(ctx, st, Patch{BrandHead: sp("  ")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty head: %v", err)
	}
	s, err = Update(ctx, st, Patch{BrandHead: sp(" Solo "), BrandTail: sp(""), LogoSVG: sp("")})
	if err != nil || s.BrandName() != "Solo" || s.LogoSVG != "" {
		t.Fatalf("one-word brand: %+v %v", s, err)
	}
}

func TestUpdateRejectsBadValuesAtomically(t *testing.T) {
	st := openTemp(t)
	ctx := context.Background()
	for name, p := range map[string]Patch{
		"accent name":    {Accent: sp("lavender")},
		"accent short":   {Accent: sp("#fff")},
		"accent alpha":   {Accent: sp("#b8acf2ff")},
		"accent no hash": {Accent: sp("b8acf2")},
		"language":       {Language: sp("de")},
		"markup in head": {BrandHead: sp("<b>x</b>")},
		"quote in tail":  {BrandTail: sp(`a"b`)},
		"control":        {BrandHead: sp("a\nb")},
		"long head":      {BrandHead: sp(strings.Repeat("x", 25))},
		"long tail":      {BrandTail: sp(strings.Repeat("ы", 25))},
		"bad logo":       {LogoSVG: sp("<svg")},
		// Valid fields in the same patch must not be applied when another one is bad.
		"mixed": {Accent: sp("#112233"), Language: sp("xx")},
	} {
		if _, err := Update(ctx, st, p); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if s, _ := Load(ctx, st); s != Defaults() {
		t.Errorf("a rejected patch changed the settings: %+v", s)
	}
	// Unicode letters are fine.
	if _, err := Update(ctx, st, Patch{BrandHead: sp("Туман"), BrandTail: sp("врата")}); err != nil {
		t.Errorf("unicode brand: %v", err)
	}
}
