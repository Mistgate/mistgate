//go:build !js

package decoy

import (
	"html"
	"path"
	"regexp"
	"strings"
)

// The warnings of a site (design: the library's "checks"). The required checks (index.html, size, file count, plain files and
// safe paths) are the bundle rules themselves: a bundle that exists has passed them.
const (
	WarnNo404         = "no_404"
	WarnExternalURLs  = "external_urls"
	WarnTelltale      = "telltale_words"
	WarnScripts       = "scripts_or_forms"
	WarnDuplicateName = "duplicate_title"
	WarnFewPages      = "few_pages"
	// WarnSharedDomains is decided when a site is put on nodes, not when it is saved.
	WarnSharedDomains = "shared_domains"
)

// MinAssistantPages is the fewest HTML pages a site made by the assistant should have.
const MinAssistantPages = 3

// Warning is one fired check; Paths are the files that fired it (at most maxWarnPaths).
type Warning struct {
	Code  string   `json:"code"`
	Paths []string `json:"paths,omitempty"`
}

// Report is what is known about a bundle without comparing it with other sites.
type Report struct {
	Title    string
	Pages    int
	Warnings []Warning
}

const maxWarnPaths = 5

var (
	externalURL  = regexp.MustCompile(`(?i)\bhttps?://[^\s"'<>)\\]+`)
	titleTag     = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	scriptOrForm = regexp.MustCompile(`(?i)<\s*(script|form)\b`)
	// Words that name what the site is hiding, and their translations. Latin words match whole words; the others anywhere.
	telltaleLatin = regexp.MustCompile(`(?i)\b(vpn|proxy|proxies|tunnel|mistgate|panel|v2ray|xray|wireguard|shadowsocks|hysteria|vless|vmess|trojan|amneziawg|amnezia|socks5|obfuscat\w*|censorship|circumvent\w*|vekil|tünel|panelı)\b`)
	telltaleOther = regexp.MustCompile(`(?i)(впн|прокси|туннел|тоннел|панел|обход блокиров|代理|翻墙|隧道|面板|科学上网|پراکسی|فیلترشکن|فیلتر شکن|تونل|پنل)`)
)

// svgNamespaces are addresses that are not requests: the namespaces of SVG and its links.
var svgNamespaces = map[string]bool{
	"http://www.w3.org/2000/svg":   true,
	"http://www.w3.org/1999/xlink": true,
	"http://www.w3.org/1999/xhtml": true,
}

func isHTMLPath(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm":
		return true
	}
	return false
}

// isTextPath says which files are read for words and addresses.
func isTextPath(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".html", ".htm", ".css", ".txt", ".xml", ".json", ".svg", ".js", ".webmanifest", ".md":
		return true
	}
	return false
}

// Analyze runs the checks that need only the bundle. assistant is true for a site the assistant made (it should have pages
// enough to look like a site). The warning duplicate_title needs the other sites and is added by the caller.
func Analyze(b *Bundle, assistant bool) Report {
	var r Report
	if home, ok := b.File("index.html"); ok {
		if m := titleTag.FindSubmatch(home); m != nil {
			r.Title = strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
			if len(r.Title) > 120 {
				r.Title = strings.ToValidUTF8(r.Title[:120], "")
			}
		}
	}
	var no404 = true
	hits := map[string][]string{}
	add := func(code, name string) {
		if len(hits[code]) < maxWarnPaths {
			hits[code] = append(hits[code], name)
		}
	}
	for _, f := range b.files {
		if f.Path == "404.html" {
			no404 = false
		}
		if isHTMLPath(f.Path) {
			r.Pages++
		}
		if ext := strings.ToLower(path.Ext(f.Path)); ext == ".js" || ext == ".mjs" {
			add(WarnScripts, f.Path)
		}
		if !isTextPath(f.Path) {
			continue
		}
		text := string(f.Content)
		if isHTMLPath(f.Path) || strings.EqualFold(path.Ext(f.Path), ".css") {
			for _, m := range externalURL.FindAllString(text, -1) {
				if !svgNamespaces[strings.ToLower(strings.TrimRight(m, "/\"'"))] {
					add(WarnExternalURLs, f.Path)
					break
				}
			}
		}
		if isHTMLPath(f.Path) && scriptOrForm.MatchString(text) {
			add(WarnScripts, f.Path)
		}
		if telltaleLatin.MatchString(text) || telltaleOther.MatchString(text) {
			add(WarnTelltale, f.Path)
		}
	}
	if no404 {
		r.Warnings = append(r.Warnings, Warning{Code: WarnNo404})
	}
	for _, code := range []string{WarnExternalURLs, WarnTelltale, WarnScripts} {
		if p := hits[code]; len(p) > 0 {
			r.Warnings = append(r.Warnings, Warning{Code: code, Paths: p})
		}
	}
	if assistant && r.Pages < MinAssistantPages {
		r.Warnings = append(r.Warnings, Warning{Code: WarnFewPages})
	}
	return r
}
