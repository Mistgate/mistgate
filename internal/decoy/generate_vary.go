package decoy

import (
	"fmt"
	"regexp"
	"strings"
)

// What makes two generated sites differ where nobody looks: robots.txt, the names of classes and ids, and the white space and
// invisible breaks that keep two sites from sharing a long literal run.

// robotsFor is a robots.txt of its own for each site: the crawlers the shared file turns away, in the site's own order, and a
// few lines a hand-written file might have.
func robotsFor(r *rng, assetDir string) []byte {
	var agents []string
	for _, l := range strings.Split(strings.SplitN(robotsTXT, "\n\n", 2)[0], "\n") {
		if strings.HasPrefix(l, "User-agent: ") {
			agents = append(agents, l)
		}
	}
	var b strings.Builder
	if r.chance(40) {
		b.WriteString("# crawlers we do not feed\n")
	}
	order := r.perm(len(agents))
	groups := 1 + r.n(3)
	for i, j := range order {
		b.WriteString(agents[j] + "\n")
		if (i+1)*groups/len(order) != (i*groups)/len(order) || i == len(order)-1 {
			b.WriteString("Disallow: /\n\n")
		}
	}
	b.WriteString("User-agent: *\n")
	if r.chance(50) && assetDir != "." && assetDir != "/" {
		b.WriteString("Disallow: " + assetDir + "/\n")
	}
	if r.chance(35) {
		fmt.Fprintf(&b, "Crawl-delay: %d\n", 2+r.n(9))
	}
	b.WriteString("Allow: /\n")
	return []byte(b.String())
}

// namer renames the "__name__" classes and ids and the "$$name" script identifiers of a site. The style is chosen per site,
// the names per seed, so a stylesheet cannot be matched by its selectors.
type namer struct {
	r      *rng
	style  int
	prefix string
	used   map[string]bool
	css    map[string]string
	js     map[string]string
}

func newNamer(r *rng) *namer {
	n := &namer{r: r, style: r.n(3), used: map[string]bool{}, css: map[string]string{}, js: map[string]string{}}
	n.prefix = n.word(2, 3)
	return n
}

func (n *namer) word(lo, hi int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, n.r.between(lo, hi))
	for i := range b {
		b[i] = letters[n.r.n(len(letters))]
	}
	return string(b)
}

func (n *namer) unique(make func() string) string {
	for {
		if s := make(); !n.used[s] {
			n.used[s] = true
			return s
		}
	}
}

func (n *namer) cssName(name string) string {
	if v, ok := n.css[name]; ok {
		return v
	}
	v := n.unique(func() string {
		switch n.style {
		case 0:
			return n.prefix + "-" + name
		case 1:
			return name + "-" + n.word(2, 3) + fmt.Sprint(n.r.n(10))
		}
		return n.word(3, 5) + fmt.Sprint(n.r.n(100))
	})
	n.css[name] = v
	return v
}

func (n *namer) jsName(name string) string {
	if v, ok := n.js[name]; ok {
		return v
	}
	v := n.unique(func() string { return "_" + n.word(1, 2) + fmt.Sprint(n.r.n(1000)) })
	n.js[name] = v
	return v
}

var (
	cssMark = regexp.MustCompile(`__([a-z][a-z0-9]*(?:-[a-z0-9]+)*)__`)
	jsMark  = regexp.MustCompile(`\$\$([a-z][A-Za-z0-9]*)`)
)

func (n *namer) rewrite(b []byte) []byte {
	b = cssMark.ReplaceAllFunc(b, func(m []byte) []byte { return []byte(n.cssName(string(m[2 : len(m)-2]))) })
	return jsMark.ReplaceAllFunc(b, func(m []byte) []byte { return []byte(n.jsName(string(m[2:]))) })
}

// blockEnd are the elements whose closing tag may be followed by a line break and some indentation of the seed's choosing.
var blockEnd = map[string]bool{"li": true, "p": true, "h1": true, "h2": true, "h3": true, "h4": true, "div": true, "section": true, "ul": true, "ol": true,
	"tr": true, "td": true, "th": true, "dt": true, "dd": true, "article": true, "header": true, "footer": true, "main": true, "nav": true, "figure": true,
	"figcaption": true, "blockquote": true, "table": true, "aside": true}

var indents = []string{"", " ", "  ", "   ", "    ", "     ", "      ", "        ", "\t", "\t\t", "\n", "\n  "}

// shake varies the layout of the markup of a page, so that two sites made from one template do not share a long run of it: a
// line break and some indentation after block elements and after meta and link tags. The text is never touched: two sites
// may say the same thing, but they do not look the same in the source.
func shake(r *rng, h string) string {
	var out strings.Builder
	raw := map[string]bool{"script": true, "style": true, "pre": true, "svg": true, "title": true, "textarea": true}
	for len(h) > 0 {
		i := strings.IndexByte(h, '<')
		if i < 0 {
			out.WriteString(h)
			break
		}
		out.WriteString(h[:i])
		j := strings.IndexByte(h[i:], '>')
		if j < 0 {
			out.WriteString(h[i:])
			break
		}
		tag := h[i : i+j+1]
		out.WriteString(tag)
		h = h[i+j+1:]
		var name string
		if f := strings.Fields(strings.TrimSuffix(tag[1:len(tag)-1], "/")); len(f) > 0 {
			name = strings.ToLower(strings.TrimLeft(f[0], "/"))
		}
		closing := strings.HasPrefix(tag, "</")
		if raw[name] && !closing && !strings.HasSuffix(tag, "/>") {
			if k := strings.Index(strings.ToLower(h), "</"+name); k >= 0 {
				out.WriteString(h[:k])
				h = h[k:]
			}
		}
		if (closing && blockEnd[name]) || (!closing && (name == "meta" || name == "link")) {
			h = strings.TrimPrefix(h, "\n")
			out.WriteString("\n" + indents[r.n(len(indents))])
		}
	}
	return out.String()
}

var (
	looseSpace = []string{"", "", " ", "\n", "\n  ", "\n\t"}
	commaSpace = []string{"", "", " ", " ", "\n", "  "}
)

// loosen varies the white space after ; { and } of a stylesheet or script, outside strings, for the same reason as shake.
func loosen(r *rng, s string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		out.WriteByte(c)
		switch {
		case quote != 0:
			if c == '\\' && i+1 < len(s) {
				i++
				out.WriteByte(s[i])
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case (c == ';' || c == '{' || c == '}') && i+1 < len(s) && s[i+1] != '\n' && s[i+1] != ' ':
			out.WriteString(looseSpace[r.n(len(looseSpace))])
		case c == ',' && i+1 < len(s) && s[i+1] != '\n' && s[i+1] != ' ':
			out.WriteString(commaSpace[r.n(len(commaSpace))]) // long font stacks and selector lists are where runs would hide
		}
	}
	return out.String()
}
