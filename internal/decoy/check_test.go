//go:build !js

package decoy

import (
	"reflect"
	"strings"
	"testing"
)

func mustBundle(t *testing.T, files map[string]string) *Bundle {
	t.Helper()
	var list []File
	for p, c := range files {
		list = append(list, File{Path: p, Content: []byte(c)})
	}
	b, err := FromFiles(list)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func warningCodes(r Report) []string {
	var out []string
	for _, w := range r.Warnings {
		out = append(out, w.Code)
	}
	return out
}

func TestAnalyzeWarnings(t *testing.T) {
	page := func(body string) string {
		return `<!doctype html><title>Corner Bakery</title><body>` + body
	}
	cases := []struct {
		name      string
		files     map[string]string
		assistant bool
		want      []string
	}{
		{"clean site", map[string]string{"index.html": page("<p>fresh bread"), "404.html": "<p>gone"}, false, nil},
		{"no 404", map[string]string{"index.html": page("<p>fresh bread")}, false, []string{WarnNo404}},
		{"external address in a page", map[string]string{"index.html": page(`<a href="https://example.com/shop">shop</a>`), "404.html": "x"}, false, []string{WarnExternalURLs}},
		{"external address in a style", map[string]string{"index.html": page("<p>x"), "404.html": "x", "site.css": `@import url(http://example.com/f.css);`}, false, []string{WarnExternalURLs}},
		{"svg namespace is not an address", map[string]string{"index.html": page(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"></svg>`), "404.html": "x"}, false, nil},
		{"words that give it away", map[string]string{"index.html": page("<p>Our VPN is fast"), "404.html": "x"}, false, []string{WarnTelltale}},
		{"a word inside another word is not one", map[string]string{"index.html": page("<p>vpnless proxyless panelling"), "404.html": "x"}, false, nil},
		{"a translation gives it away", map[string]string{"index.html": page("<p>Наш прокси быстрый"), "404.html": "x"}, false, []string{WarnTelltale}},
		{"script", map[string]string{"index.html": page(`<script>1</script>`), "404.html": "x"}, false, []string{WarnScripts}},
		{"form", map[string]string{"index.html": page(`<form action="/x"></form>`), "404.html": "x"}, false, []string{WarnScripts}},
		{"script file", map[string]string{"index.html": page("<p>x"), "404.html": "x", "app.js": "1"}, false, []string{WarnScripts}},
		{"assistant site with two pages", map[string]string{"index.html": page("<p>x"), "404.html": "x"}, true, []string{WarnFewPages}},
		{"assistant site with three pages", map[string]string{"index.html": page("<p>x"), "404.html": "x", "about.html": "y"}, true, nil},
		{"a short site that was not made by the assistant", map[string]string{"index.html": page("<p>x"), "404.html": "x"}, false, nil},
		{"several at once", map[string]string{"index.html": page(`<script src="https://example.com/a.js"></script>proxy`)}, false, []string{WarnNo404, WarnExternalURLs, WarnTelltale, WarnScripts}},
	}
	for _, tc := range cases {
		got := warningCodes(Analyze(mustBundle(t, tc.files), tc.assistant))
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: warnings = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestAnalyzeTitleAndPaths(t *testing.T) {
	b := mustBundle(t, map[string]string{
		"index.html":    "<title>\n  Corner   &amp; Co\n</title>",
		"about.html":    `<a href="http://example.com/">x</a>`,
		"a/deeper.html": `<a href="https://example.org/">x</a>`,
		"404.html":      "x",
	})
	r := Analyze(b, false)
	if r.Title != "Corner & Co" || r.Pages != 4 {
		t.Fatalf("title %q pages %d", r.Title, r.Pages)
	}
	if len(r.Warnings) != 1 || r.Warnings[0].Code != WarnExternalURLs || strings.Join(r.Warnings[0].Paths, ",") != "a/deeper.html,about.html" {
		t.Fatalf("warnings = %+v", r.Warnings)
	}
	many := map[string]string{"index.html": "x", "404.html": "x"}
	for i := 0; i < 9; i++ {
		many["p"+string(rune('a'+i))+".html"] = `<script></script>`
	}
	if w := Analyze(mustBundle(t, many), false).Warnings; len(w) != 1 || len(w[0].Paths) != maxWarnPaths {
		t.Fatalf("paths are not capped: %+v", w)
	}
}
