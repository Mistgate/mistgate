//go:build !js

package decoy

import (
	"errors"
	"strings"
	"testing"
)

func previewSite(t *testing.T) *Bundle {
	return mustBundle(t, map[string]string{
		"index.html": `<!doctype html><html><head><title>Bakery</title>
<link rel="stylesheet" href="/style.css">
<link rel="icon" href="/favicon.ico">
<link rel="preconnect" href="https://fonts.example.com">
<link rel="stylesheet" href="https://cdn.example.com/lib.css">
<base href="https://example.com/">
<script src="/app.js"></script><script>alert(1)</script>
<meta http-equiv="refresh" content="0;url=https://example.com/">
</head><body onload="steal()">
<img src="/logo.png" alt="logo" onerror="steal()"><img src="http://tracker.example.com/p.gif">
<img srcset="/logo.png 1x, https://cdn.example.com/b.png 2x">
<a href="/about.html" target="_blank">About</a> <a href="docs/page.html#top">Docs</a> <a href="https://evil.example.com/">Out</a> <a href="#top">Top</a>
<div style="background:url(/logo.png)">x</div>
<iframe src="https://example.com/frame"></iframe><video src="/v.mp4"></video><noscript><img src="https://evil.example.com/n.gif"></noscript>
<form action="https://evil.example.com/post"><input type="image" src="https://evil.example.com/i.png"></form>
<style>@import "/more.css"; h1{background:url("img/bg.png")}</style>
</body></html>`,
		"about.html":     `<title>About</title><p>about us`,
		"docs/page.html": `<link rel="stylesheet" href="local.css"><img src="../logo.png"><img src="pic.png"><a href="../index.html">home</a>`,
		"docs/local.css": `p{color:red;background:url(pic.png)}`,
		"docs/pic.png":   "PIC",
		"style.css":      `body{background:url(/logo.png)} .x{background:url(https://cdn.example.com/x.png)}`,
		"more.css":       `@import "/style.css"; .more{color:blue}`,
		"img/bg.png":     "BG",
		"logo.png":       "PNG",
		"favicon.ico":    "ICO",
		"app.js":         "alert(3)",
		"404.html":       "<p>gone",
	})
}

func TestPreviewDocumentIsSelfContained(t *testing.T) {
	doc, err := PreviewDocument(previewSite(t), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"<script", "alert(", "steal(", "https://", "http://", "<iframe", "<video", "<noscript", "<base", "http-equiv=\"refresh\"", "target=", "action=", "/logo.png", "href=\"/about.html\"", "<link rel=\"stylesheet\""} {
		if strings.Contains(doc, bad) {
			t.Errorf("the preview still has %q:\n%s", bad, doc)
		}
	}
	for _, want := range []string{
		"<style>", "Content-Security-Policy", "default-src &#39;none&#39;",
		"data:image/png;base64,UE5H", "data:image/x-icon;base64,SUNP", // logo.png and favicon.ico
		"data:image/png;base64,Qkc=", // img/bg.png, from a <style> block
		`data-page="about.html"`, `data-page="docs/page.html"`, `href="#top"`,
		".more{color:blue}", // the @import of a stylesheet is put in
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the preview lacks %q:\n%s", want, doc)
		}
	}
}

func TestPreviewResolvesRelativePaths(t *testing.T) {
	doc, err := PreviewDocument(previewSite(t), "docs/page.html")
	if err != nil {
		t.Fatal(err)
	}
	// docs/local.css (relative to the page) with its docs/pic.png, ../logo.png, pic.png and the link to ../index.html
	for _, want := range []string{"color:red", "data:image/png;base64,UElD", "data:image/png;base64,UE5H", `data-page="index.html"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/page.html: no %q in\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "<link") || strings.Contains(doc, "../") {
		t.Errorf("docs/page.html still points at files:\n%s", doc)
	}
}

func TestPreviewCannotBeBrokenOutOfItsStyle(t *testing.T) {
	b := mustBundle(t, map[string]string{
		"index.html": `<link rel="stylesheet" href="s.css"><style>p{content:"</STYLE><img src=x onerror=alert(1)>"}</style>`,
		"s.css":      `a{content:"</style ><script>alert(2)</script>"}`,
	})
	doc, err := PreviewDocument(b, "")
	if err != nil {
		t.Fatal(err)
	}
	if lower := strings.ToLower(doc); strings.Count(lower, "<style") != strings.Count(lower, "</style>") || strings.Contains(lower, "</style ><script") || strings.Contains(lower, "onerror") {
		t.Fatalf("a stylesheet closed its <style>:\n%s", doc)
	}
}

func TestPreviewRefusesWhatItCannotShow(t *testing.T) {
	b := previewSite(t)
	for _, page := range []string{"missing.html", "style.css", "../index.html"} {
		if _, err := PreviewDocument(b, page); !errors.Is(err, ErrPageNotFound) {
			t.Errorf("preview of %q = %v, want not found", page, err)
		}
	}
	// one big image used again and again is inlined every time: the document is capped
	big := strings.Repeat("A", 1<<20+512<<10)
	page := "<title>x</title>" + strings.Repeat(`<img src="/big.png">`, 6)
	if _, err := PreviewDocument(mustBundle(t, map[string]string{"index.html": page, "big.png": big}), ""); !errors.Is(err, ErrPreviewTooLarge) {
		t.Fatalf("preview of a page that inlines 6 x 1.5 MiB = %v, want too large", err)
	}
	if doc, err := PreviewDocument(mustBundle(t, map[string]string{"index.html": "<title>x</title><img src=\"/big.png\">", "big.png": big}), ""); err != nil || len(doc) < len(big) {
		t.Fatalf("one inlined image = %d bytes, %v", len(doc), err)
	}
}

func TestPagesAndTextFile(t *testing.T) {
	b := mustBundle(t, map[string]string{"zeta.html": "z", "index.html": "i", "a/b.htm": "b", "style.css": "p{}", "logo.png": "\x00\x01bin", "big.txt": strings.Repeat("é", MaxTextBytes)})
	if got := strings.Join(b.Pages(), ","); got != "index.html,a/b.htm,zeta.html" {
		t.Fatalf("pages = %s", got)
	}
	if text, size, cut, err := TextFile(b, "style.css"); err != nil || text != "p{}" || size != 3 || cut {
		t.Fatalf("css = %q %d %v %v", text, size, cut, err)
	}
	if _, _, _, err := TextFile(b, "logo.png"); !errors.Is(err, ErrNotText) {
		t.Fatalf("binary file = %v", err)
	}
	if _, _, _, err := TextFile(b, "nothing"); !errors.Is(err, ErrPageNotFound) {
		t.Fatalf("missing file = %v", err)
	}
	text, size, cut, err := TextFile(b, "big.txt")
	if err != nil || !cut || size != 2*MaxTextBytes || len(text) > MaxTextBytes || len(text) < MaxTextBytes-3 {
		t.Fatalf("long file: %d bytes of %d, cut %v, %v", len(text), size, cut, err)
	}
}

// The library shows the built-in page from the text the node serves, not from a copy.
func TestBuiltinBundleIsTheServedPage(t *testing.T) {
	b := BuiltinBundle()
	if b == nil || len(b.Pages()) != 1 {
		t.Fatalf("builtin bundle = %+v", b)
	}
	if got, _ := b.File("index.html"); string(got) != BuiltinHTML() {
		t.Fatal("the bundle's page differs from BuiltinHTML")
	}
	if served := do(Handler(), "GET", "/").Body.String(); served != BuiltinHTML() {
		t.Fatal("the handler serves another text than BuiltinHTML")
	}
	if doc, err := PreviewDocument(b, ""); err != nil || !strings.Contains(doc, "Coming soon") {
		t.Fatalf("preview = %q, %v", doc, err)
	}
}
