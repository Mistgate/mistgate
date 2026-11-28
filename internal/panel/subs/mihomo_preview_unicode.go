package subs

import (
	"bytes"
	"strconv"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// The YAML v3 emitter escapes supplementary code points. Show printable ones and
// standard subdivision flags literally without changing scalar values.
func readableMihomoPreview(body []byte) string {
	if !bytes.Contains(body, []byte(`\U`)) {
		return string(body)
	}
	var doc yaml.Node
	if yaml.Unmarshal(body, &doc) != nil {
		return string(body)
	}
	var out []byte
	line, lineStart, offset, column, copied := 1, 0, 0, 1, 0
	var visit func(*yaml.Node)
	visit = func(n *yaml.Node) {
		if n.Kind != yaml.ScalarNode {
			for _, child := range n.Content {
				visit(child)
			}
			return
		}
		if n.Style&yaml.DoubleQuotedStyle == 0 || n.Line < line {
			return
		}
		for line < n.Line {
			nl := bytes.IndexByte(body[lineStart:], '\n')
			if nl < 0 {
				return
			}
			lineStart += nl + 1
			offset, column = lineStart, 1
			line++
		}
		for column < n.Column && offset < len(body) {
			_, width := utf8.DecodeRune(body[offset:])
			offset += width
			column++
		}
		i := offset
		// The emitter puts a scalar's anchor or explicit tag before its opening quote.
		for i < len(body) && (body[i] == '&' || body[i] == '!') {
			for i < len(body) && body[i] != ' ' && body[i] != '\t' && body[i] != '\n' {
				i++
			}
			for i < len(body) && (body[i] == ' ' || body[i] == '\t') {
				i++
			}
		}
		if i >= len(body) || body[i] != '"' || i < copied {
			return
		}
		for i++; i+1 < len(body) && body[i] != '"'; i++ {
			escaped := body[i] == '\\'
			if !escaped && !bytes.HasPrefix(body[i:], []byte("🏴")) {
				continue
			}
			r, width := mihomoPreviewRune(body[i:])
			var tags [5]byte
			var tagWidth int
			if r == '🏴' {
				tags, tagWidth = mihomoSubdivisionTags(body[i+width:])
			}
			if tagWidth > 0 || escaped && r > 0xffff && unicode.IsPrint(r) {
				if out == nil {
					out = make([]byte, 0, len(body))
				}
				out = append(out, body[copied:i]...)
				out = utf8.AppendRune(out, r)
				if tagWidth > 0 {
					for _, tag := range tags {
						out = utf8.AppendRune(out, 0xe0000+rune(tag))
					}
					out = utf8.AppendRune(out, '\U000e007f')
				}
				copied = i + width + tagWidth
				i = copied - 1
			} else if escaped {
				i++ // Skip the escaped byte, including a literal backslash or quote.
			}
		}
	}
	visit(&doc)
	if out == nil {
		return string(body)
	}
	return string(append(out, body[copied:]...))
}

func mihomoPreviewRune(body []byte) (rune, int) {
	if len(body) == 0 || body[0] != '\\' {
		return utf8.DecodeRune(body)
	}
	if len(body) >= 10 && body[1] == 'U' {
		value, err := strconv.ParseUint(string(body[2:10]), 16, 32)
		if err == nil && value <= utf8.MaxRune {
			return rune(value), 10
		}
	}
	return 0, 0
}

// After a black flag, the only RGI tag sequences are gbeng, gbsct and gbwls,
// each terminated by CANCEL TAG. Leave other invisible tag escapes alone.
func mihomoSubdivisionTags(body []byte) (tags [5]byte, width int) {
	for i := range tags {
		r, n := mihomoPreviewRune(body[width:])
		if r < '\U000e0061' || r > '\U000e007a' {
			return tags, 0
		}
		tags[i] = byte(r - 0xe0000)
		width += n
	}
	switch string(tags[:]) {
	case "gbeng", "gbsct", "gbwls":
		if r, n := mihomoPreviewRune(body[width:]); r == '\U000e007f' {
			return tags, width + n
		}
	}
	return tags, 0
}
