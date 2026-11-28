package subs

import (
	"bytes"
	"strconv"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// The YAML v3 emitter escapes supplementary code points. Show printable ones
// literally in the preview without reformatting the document or changing scalar values.
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
			if body[i] != '\\' {
				continue
			}
			if i+10 <= len(body) && body[i+1] == 'U' {
				value, err := strconv.ParseUint(string(body[i+2:i+10]), 16, 32)
				if err == nil && value > 0xffff && value <= utf8.MaxRune && unicode.IsPrint(rune(value)) {
					if out == nil {
						out = make([]byte, 0, len(body))
					}
					out = append(out, body[copied:i]...)
					out = utf8.AppendRune(out, rune(value))
					copied = i + 10
					i += 9
					continue
				}
			}
			i++ // Skip the escaped byte, including a literal backslash or quote.
		}
	}
	visit(&doc)
	if out == nil {
		return string(body)
	}
	return string(append(out, body[copied:]...))
}
