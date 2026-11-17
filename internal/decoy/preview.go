//go:build !js

package decoy

import (
	"bytes"
	"encoding/base64"
	"errors"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// The preview of a site. Sites are written with paths from the root ("/style.css"), so a page cannot be shown under a prefix
// of the panel. PreviewDocument turns one page into a document that needs nothing: the stylesheets are inlined, the local
// images and fonts are data: URIs, scripts and everything that could load from outside are gone, links are inert. The caller
// puts it into <iframe sandbox="" srcdoc="...">.

var (
	ErrPageNotFound    = errors.New("decoy page not found")
	ErrNotText         = errors.New("decoy file is not text")
	ErrPreviewTooLarge = errors.New("decoy preview too large")
)

const (
	// MaxPreviewBytes caps the document: the bundle is at most 2 MiB and data: URIs are a third bigger, and a file used many
	// times is inlined every time.
	MaxPreviewBytes = 6 << 20
	// MaxTextBytes is how much of a text file GetFile returns.
	MaxTextBytes = 256 << 10
	maxCSSDepth  = 3
)

// BuiltinBundle is the built-in page as a bundle of one file, so the library reads it as it reads a site (preview, files). The text is
// BuiltinHTML, the one the node and the panel serve.
func BuiltinBundle() *Bundle {
	builtinOnce.Do(func() { builtinBundle, _ = FromFiles([]File{{Path: "index.html", Content: []byte(BuiltinHTML())}}) })
	return builtinBundle
}

var (
	builtinOnce   sync.Once
	builtinBundle *Bundle
)

// Pages lists the HTML files of the bundle, index.html first.
func (b *Bundle) Pages() []string {
	var out []string
	for _, f := range b.files {
		if isHTMLPath(f.Path) {
			out = append(out, f.Path)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i] == "index.html" && out[j] != "index.html" })
	return out
}

// TextFile returns the text of a file (the first MaxTextBytes of it) and whether it was cut.
func TextFile(b *Bundle, name string) (text string, size int, truncated bool, err error) {
	content, ok := b.File(name)
	if !ok {
		return "", 0, false, ErrPageNotFound
	}
	head := content
	if len(head) > MaxTextBytes {
		head, truncated = head[:MaxTextBytes], true
		for len(head) > 0 && !utf8.Valid(head) { // a rune cut in half
			head = head[:len(head)-1]
		}
	}
	if bytes.IndexByte(head, 0) >= 0 || !utf8.Valid(head) {
		return "", len(content), false, ErrNotText
	}
	return string(head), len(content), truncated, nil
}

// PreviewDocument is the page as one self-contained HTML document (see the top of this file).
func PreviewDocument(b *Bundle, page string) (string, error) {
	if page == "" {
		page = "index.html"
	}
	content, ok := b.File(page)
	if !ok || !isHTMLPath(page) {
		return "", ErrPageNotFound
	}
	p := &previewer{b: b}
	doc, err := html.Parse(bytes.NewReader(content))
	if err != nil {
		return "", err
	}
	if err := p.clean(doc, path.Dir(page)); err != nil {
		return "", err
	}
	if head := findElement(doc, atom.Head); head != nil {
		csp := &html.Node{Type: html.ElementNode, Data: "meta", DataAtom: atom.Meta, Attr: []html.Attribute{
			{Key: "http-equiv", Val: "Content-Security-Policy"},
			{Key: "content", Val: "default-src 'none'; img-src data:; font-src data:; style-src 'unsafe-inline'; form-action 'none'"},
		}}
		head.InsertBefore(csp, head.FirstChild)
	}
	var out bytes.Buffer
	if err := html.Render(&out, doc); err != nil {
		return "", err
	}
	if out.Len() > MaxPreviewBytes {
		return "", ErrPreviewTooLarge
	}
	return out.String(), nil
}

func findElement(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := findElement(c, a); f != nil {
			return f
		}
	}
	return nil
}

type previewer struct {
	b      *Bundle
	inline int // bytes of data: URIs and stylesheets put into the document so far
}

var (
	schemeRef  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.\-]*:`)
	cssURL     = regexp.MustCompile(`(?i)url\(\s*(?:"([^"]*)"|'([^']*)'|([^)\s"']*))\s*\)`)
	cssImport  = regexp.MustCompile(`(?i)@import\s+(?:url\(\s*)?["']?([^"')\s;]+)["']?\s*\)?[^;]*;`)
	styleClose = regexp.MustCompile(`(?i)</(style)`)
)

// local resolves a reference found in a file that lives in dir to a file of the bundle ("" when it is not one).
func (p *previewer) local(ref, dir string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || strings.HasPrefix(ref, "//") || schemeRef.MatchString(ref) {
		return ""
	}
	if i := strings.IndexAny(ref, "#?"); i >= 0 {
		ref = ref[:i]
	}
	if un, err := url.PathUnescape(ref); err == nil {
		ref = un
	}
	var name string
	if strings.HasPrefix(ref, "/") {
		name = path.Clean(ref)[1:]
	} else {
		name = path.Join(dir, ref)
	}
	if name == "" || name == "." {
		name = "index.html"
	}
	if strings.HasPrefix(name, "..") {
		return ""
	}
	if p.has(name) {
		return name
	}
	if idx := path.Join(name, "index.html"); p.has(idx) {
		return idx
	}
	return ""
}

var mimeByExt = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp",
	".avif": "image/avif", ".svg": "image/svg+xml", ".ico": "image/x-icon", ".bmp": "image/bmp",
	".woff": "font/woff", ".woff2": "font/woff2", ".ttf": "font/ttf", ".otf": "font/otf",
}

// dataURI is the file as a data: URI, "" when it is not a kind of file the preview inlines.
func (p *previewer) dataURI(name string) (string, error) {
	mime, ok := mimeByExt[strings.ToLower(path.Ext(name))]
	if !ok {
		return "", nil
	}
	content, _ := p.b.File(name)
	uri := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(content)
	if p.inline += len(uri); p.inline > MaxPreviewBytes {
		return "", ErrPreviewTooLarge
	}
	return uri, nil
}

// sourceURL is a value of src, href or a CSS url(): the data: URI of the local file, a data:image/font URI that is already
// inline, or "" for anything else.
func (p *previewer) sourceURL(ref, dir string) (string, error) {
	ref = strings.TrimSpace(ref)
	low := strings.ToLower(ref)
	if strings.HasPrefix(low, "data:image/") || strings.HasPrefix(low, "data:font/") || strings.HasPrefix(low, "data:application/font") {
		return ref, nil
	}
	if name := p.local(ref, dir); name != "" {
		return p.dataURI(name)
	}
	return "", nil
}

// css rewrites a stylesheet that lives in dir: local @import files are put in, local url() become data: URIs, everything else
// that would load from outside is cut.
func (p *previewer) css(text, dir string, depth int) (string, error) {
	var err error
	text = cssImport.ReplaceAllStringFunc(text, func(m string) string {
		ref := cssImport.FindStringSubmatch(m)[1]
		name := p.local(ref, dir)
		if err != nil || name == "" || depth >= maxCSSDepth || strings.ToLower(path.Ext(name)) != ".css" {
			return ""
		}
		sheet, _ := p.b.File(name)
		var inner string
		inner, err = p.css(string(sheet), path.Dir(name), depth+1)
		return inner
	})
	if err != nil {
		return "", err
	}
	text = cssURL.ReplaceAllStringFunc(text, func(m string) string {
		if err != nil {
			return ""
		}
		g := cssURL.FindStringSubmatch(m)
		uri, e := p.sourceURL(g[1]+g[2]+g[3], dir)
		if e != nil {
			err = e
			return ""
		}
		if uri == "" {
			return `url("data:,")`
		}
		return `url("` + uri + `")`
	})
	if err != nil {
		return "", err
	}
	text = styleClose.ReplaceAllString(text, `<\/$1`)
	if p.inline += len(text); p.inline > MaxPreviewBytes {
		return "", ErrPreviewTooLarge
	}
	return text, nil
}

var (
	// Elements the preview never keeps.
	droppedTags = map[string]bool{
		"script": true, "base": true, "iframe": true, "frame": true, "frameset": true, "object": true, "embed": true,
		"applet": true, "video": true, "audio": true, "track": true, "portal": true, "noscript": true, "template": true,
	}
	// Attributes whose value is an address; the ones not handled for their element are dropped.
	urlAttrs = map[string]bool{
		"src": true, "srcset": true, "href": true, "poster": true, "data": true, "action": true, "formaction": true,
		"background": true, "cite": true, "ping": true, "longdesc": true, "manifest": true, "codebase": true, "profile": true,
		"archive": true, "usemap": true, "dynsrc": true, "lowsrc": true,
	}
)

func (p *previewer) clean(parent *html.Node, dir string) error {
	for c := parent.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode {
			keep, err := p.element(c, dir)
			if err != nil {
				return err
			}
			if !keep {
				parent.RemoveChild(c)
				c = next
				continue
			}
			if err := p.clean(c, dir); err != nil {
				return err
			}
		}
		c = next
	}
	return nil
}

// element makes one element safe; keep is false when it goes.
func (p *previewer) element(n *html.Node, dir string) (keep bool, err error) {
	if droppedTags[n.Data] {
		return false, nil
	}
	switch n.Data {
	case "meta":
		for _, a := range n.Attr {
			if strings.EqualFold(a.Key, "http-equiv") {
				return false, nil
			}
		}
	case "source":
		if n.Parent == nil || n.Parent.Data != "picture" {
			return false, nil
		}
	case "link":
		return p.link(n, dir)
	case "style":
		var raw strings.Builder
		for t := n.FirstChild; t != nil; t = t.NextSibling {
			if t.Type == html.TextNode {
				raw.WriteString(t.Data)
			}
		}
		text, err := p.css(raw.String(), dir, 0)
		if err != nil {
			return false, err
		}
		n.FirstChild, n.LastChild = nil, nil
		n.AppendChild(&html.Node{Type: html.TextNode, Data: text})
	}
	attrs := n.Attr[:0]
	for _, a := range n.Attr {
		key := strings.ToLower(a.Key)
		switch {
		case strings.HasPrefix(key, "on"), key == "target":
			continue
		case key == "style":
			v, err := p.css(a.Val, dir, 0)
			if err != nil {
				return false, err
			}
			a.Val = v
		case urlAttrs[key]:
			v, ok, err := p.urlAttr(n, key, a.Val, dir)
			if err != nil {
				return false, err
			}
			if !ok {
				continue
			}
			a.Val = v
		}
		attrs = append(attrs, a)
	}
	n.Attr = attrs
	if n.Data == "a" {
		p.anchor(n, dir)
	}
	return true, nil
}

// urlAttr rewrites one address attribute; ok is false when the attribute goes. Anchors are finished by anchor.
func (p *previewer) urlAttr(n *html.Node, key, val, dir string) (string, bool, error) {
	switch {
	case n.Data == "a" && key == "href":
		return val, true, nil // anchor decides
	case (n.Data == "img" || n.Data == "source" || n.Data == "image") && key == "srcset":
		var parts []string
		for _, cand := range strings.Split(val, ",") {
			f := strings.Fields(cand)
			if len(f) == 0 {
				continue
			}
			uri, err := p.sourceURL(f[0], dir)
			if err != nil {
				return "", false, err
			}
			if uri != "" {
				parts = append(parts, strings.Join(append([]string{uri}, f[1:]...), " "))
			}
		}
		return strings.Join(parts, ", "), len(parts) > 0, nil
	case (n.Data == "img" || n.Data == "source" || n.Data == "image" || n.Data == "use") && (key == "src" || key == "href"):
		if key == "href" && strings.HasPrefix(val, "#") {
			return val, true, nil // a reference inside the document
		}
		uri, err := p.sourceURL(val, dir)
		return uri, uri != "", err
	}
	return "", false, nil
}

// anchor makes a link inert: no href, and the page it led to is in data-page.
func (p *previewer) anchor(n *html.Node, dir string) {
	for i, a := range n.Attr {
		if !strings.EqualFold(a.Key, "href") {
			continue
		}
		if strings.HasPrefix(a.Val, "#") {
			return
		}
		n.Attr = append(n.Attr[:i], n.Attr[i+1:]...)
		if name := p.local(a.Val, dir); name != "" && isHTMLPath(name) {
			n.Attr = append(n.Attr, html.Attribute{Key: "data-page", Val: name})
		}
		return
	}
}

// link keeps stylesheets (as <style>) and icons (with a data: href) that are files of the site; every other <link> goes.
func (p *previewer) link(n *html.Node, dir string) (bool, error) {
	var rel, href, media string
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "rel":
			rel = strings.ToLower(a.Val)
		case "href":
			href = a.Val
		case "media":
			media = a.Val
		}
	}
	name := p.local(href, dir)
	if name == "" {
		return false, nil
	}
	for _, token := range strings.Fields(rel) {
		switch {
		case token == "stylesheet":
			sheet, _ := p.b.File(name)
			text, err := p.css(string(sheet), path.Dir(name), 0)
			if err != nil {
				return false, err
			}
			n.Data, n.DataAtom, n.Attr = "style", atom.Style, nil
			if media != "" {
				n.Attr = []html.Attribute{{Key: "media", Val: media}}
			}
			n.AppendChild(&html.Node{Type: html.TextNode, Data: text})
			return true, nil
		case strings.Contains(token, "icon"):
			uri, err := p.dataURI(name)
			if err != nil || uri == "" {
				return false, err
			}
			n.Attr = []html.Attribute{{Key: "rel", Val: "icon"}, {Key: "href", Val: uri}}
			return true, nil
		}
	}
	return false, nil
}

func (p *previewer) has(name string) bool {
	_, ok := p.b.File(name)
	return ok
}
