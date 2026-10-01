package instance

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

// MaxLogoBytes is the largest SVG the admin may upload (before sanitising).
const MaxLogoBytes = 64 << 10

const (
	svgNS       = "http://www.w3.org/2000/svg"
	maxSVGDepth = 64
	maxSVGNodes = 4096
)

// Shapes, gradients and text: what a logo needs. Everything else (script, style,
// foreignObject, animation, image, use, filter, pattern, a, anything in another
// namespace ...) is dropped together with its children.
var svgElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "title": true, "desc": true,
	"path": true, "rect": true, "circle": true, "ellipse": true, "line": true, "polyline": true, "polygon": true,
	"linearGradient": true, "radialGradient": true, "stop": true, "clipPath": true, "mask": true,
	"text": true, "tspan": true,
}

// Plain presentation and geometry attributes. No event handlers, no href, no class
// (there is no stylesheet), no namespaced attribute.
var svgAttrs = map[string]bool{
	"id": true, "viewBox": true, "width": true, "height": true, "preserveAspectRatio": true,
	"x": true, "y": true, "cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"x1": true, "y1": true, "x2": true, "y2": true, "fx": true, "fy": true, "d": true, "points": true,
	"transform": true, "fill": true, "fill-opacity": true, "fill-rule": true, "opacity": true,
	"stroke": true, "stroke-width": true, "stroke-linecap": true, "stroke-linejoin": true, "stroke-miterlimit": true,
	"stroke-dasharray": true, "stroke-dashoffset": true, "stroke-opacity": true,
	"offset": true, "stop-color": true, "stop-opacity": true,
	"gradientUnits": true, "gradientTransform": true, "spreadMethod": true,
	"clip-path": true, "clip-rule": true, "clipPathUnits": true, "mask": true, "maskUnits": true, "maskContentUnits": true,
	"font-family": true, "font-size": true, "font-weight": true, "text-anchor": true, "dominant-baseline": true,
	"style": true,
}

func svgErr(why string) error { return invalid("logo: %s", why) }

// SanitizeSVG rewrites an SVG so it can be served and inlined safely: it keeps the
// allow-listed shapes, gradients and text with allow-listed attributes and drops the
// rest — scripts, foreign objects, event handlers, links, animation and every
// reference that is not "#id" inside the document. The result is rebuilt from tokens,
// so nothing the parser tolerated but a browser might interpret differently survives.
// Input over MaxLogoBytes, DOCTYPE/entity tricks, several roots and a root that is not
// <svg> are rejected.
func SanitizeSVG(in string) (string, error) {
	if len(in) > MaxLogoBytes {
		return "", svgErr("larger than 64 KB")
	}
	d := xml.NewDecoder(strings.NewReader(in))
	d.Strict = true // unknown entities are errors; none are defined
	var out bytes.Buffer
	var (
		stack   []string // kept elements that are open
		skip    int      // >0: inside a dropped subtree, this many elements deep
		roots   int
		nodes   int
		hasRoot bool
	)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", svgErr("not well-formed XML")
		}
		switch t := tok.(type) {
		case xml.Directive:
			return "", svgErr("DOCTYPE and entity declarations are not allowed")
		case xml.StartElement:
			if nodes++; nodes > maxSVGNodes {
				return "", svgErr("too many elements")
			}
			if len(stack) == 0 && skip == 0 {
				if roots++; roots > 1 || t.Name.Local != "svg" || (t.Name.Space != "" && t.Name.Space != svgNS) {
					return "", svgErr("the root element must be a single <svg>")
				}
			}
			if skip > 0 || !svgElements[t.Name.Local] || (t.Name.Space != "" && t.Name.Space != svgNS) {
				skip++
				continue
			}
			if len(stack) >= maxSVGDepth {
				return "", svgErr("nested too deeply")
			}
			hasRoot = true
			out.WriteString("<" + t.Name.Local)
			if len(stack) == 0 {
				out.WriteString(` xmlns="` + svgNS + `"`)
			}
			for _, a := range t.Attr {
				if a.Name.Space == "" && svgAttrs[a.Name.Local] && safeAttr(a.Name.Local, a.Value) {
					out.WriteString(" " + a.Name.Local + `="`)
					xml.EscapeText(&out, []byte(a.Value))
					out.WriteString(`"`)
				}
			}
			out.WriteString(">")
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if skip > 0 {
				skip--
				continue
			}
			out.WriteString("</" + stack[len(stack)-1] + ">")
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if skip == 0 && len(stack) > 0 && isTextElement(stack[len(stack)-1]) {
				xml.EscapeText(&out, t)
			}
		}
	}
	if !hasRoot {
		return "", svgErr("no <svg> element")
	}
	return out.String(), nil
}

func isTextElement(name string) bool {
	return name == "text" || name == "tspan" || name == "title" || name == "desc"
}

// safeAttr screens attribute values: a reference must be "url(#id)", and nothing that
// smells like script or an external load is kept. id gets a tight character set.
func safeAttr(name, val string) bool {
	if len(val) > 8192 {
		return false
	}
	if name == "id" {
		for _, r := range val {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
				return false
			}
		}
		return val != ""
	}
	v := strings.ToLower(val)
	for _, bad := range []string{"javascript:", "vbscript:", "data:", "http:", "https:", "//", "expression", "@", "\\", "/*", "<", "behavior", "binding"} {
		if strings.Contains(v, bad) {
			return false
		}
	}
	if name == "style" && strings.Contains(v, "position") { // no overlays when the SVG is inlined
		return false
	}
	for rest := v; ; {
		i := strings.Index(rest, "url(")
		if i < 0 {
			return true
		}
		rest = strings.TrimLeft(rest[i+len("url("):], " \t\r\n'\"")
		if !strings.HasPrefix(rest, "#") {
			return false
		}
	}
}
