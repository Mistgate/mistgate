package sitegen

import (
	"regexp"
	"strings"
)

// Highlighting of code samples for the templates: {{hl .L.quick "sh"}}. It escapes the text itself and wraps the parts that have a
// meaning in <span class="__hX__">; the text of the result with its tags taken off is the input, character for character. The
// classes are drawn in draw_css (base.css.tmpl): hc comment, hs string, hf flag, hk key, hn number, hp prompt, hm command.
//
// Languages: "sh" (a command line, with or without the "$ " prompt), "json", "yaml" (also toml and ini: keys, strings, numbers,
// comments), "out" (what a program prints: only the numbers), and "" or anything else (escaped, nothing more).

type seg struct{ cls, text string }

func highlight(code, lang string) string {
	var segs []seg
	switch lang {
	case "sh":
		segs = shSegs(code)
	case "json":
		segs = jsonSegs(code)
	case "yaml", "toml", "ini":
		segs = yamlSegs(code)
	case "out":
		segs = outSegs(code)
	default:
		segs = []seg{{"", code}}
	}
	var b strings.Builder
	for i := 0; i < len(segs); i++ {
		cls, text := segs[i].cls, segs[i].text
		for i+1 < len(segs) && segs[i+1].cls == cls { // neighbours of one class are one span
			i++
			text += segs[i].text
		}
		if text == "" {
			continue
		}
		if cls == "" {
			b.WriteString(escapeCode(text))
			continue
		}
		b.WriteString(`<span class="__dh` + cls + `__">` + escapeCode(text) + `</span>`)
	}
	return b.String()
}

var codeEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func escapeCode(s string) string { return codeEscaper.Replace(s) }

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

// numberWord is a word that is a number, a date, a time or a size: 500, 2h, 2025-03-01, 10:30, 4.5MB.
var numberWord = regexp.MustCompile(`^\d[\w.:/-]*$`)

func shSegs(code string) []seg {
	var out []seg
	add := func(cls, s string) {
		if s != "" {
			out = append(out, seg{cls, s})
		}
	}
	for li, line := range strings.Split(code, "\n") {
		if li > 0 {
			add("", "\n")
		}
		i := 0
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		add("", line[:i])
		if rest := line[i:]; strings.HasPrefix(rest, "$ ") || strings.HasPrefix(rest, "> ") {
			add("p", rest[:2])
			i += 2
		}
		expect := true // the next word is a command
		for i < len(line) {
			c := line[i]
			switch {
			case isSpace(c):
				j := i
				for j < len(line) && isSpace(line[j]) {
					j++
				}
				add("", line[i:j])
				i = j
			case c == '#':
				add("c", line[i:])
				i = len(line)
			case c == '\'' || c == '"':
				j := i + 1
				for j < len(line) && line[j] != c {
					if line[j] == '\\' && c == '"' && j+1 < len(line) {
						j++
					}
					j++
				}
				if j < len(line) {
					j++
				}
				add("s", line[i:j])
				i = j
				expect = false
			case c == '|' || c == '&' || c == ';' || c == '>' || c == '<':
				j := i
				for j < len(line) && strings.IndexByte("|&;<>", line[j]) >= 0 {
					j++
				}
				add("", line[i:j])
				i = j
				expect = c == '|' || c == '&' || c == ';'
			default:
				j := i
				for j < len(line) && !isSpace(line[j]) && line[j] != '\'' && line[j] != '"' && strings.IndexByte("|&;<>", line[j]) < 0 {
					j++
				}
				w := line[i:j]
				switch {
				case expect:
					add("m", w)
					expect = w == "sudo" || w == "env" || w == "time"
				case len(w) > 1 && w[0] == '-' && (w[1] == '-' || w[1] >= 'a' && w[1] <= 'z' || w[1] >= 'A' && w[1] <= 'Z'):
					if k := strings.IndexByte(w, '='); k > 0 {
						add("f", w[:k+1])
						if v := w[k+1:]; numberWord.MatchString(v) {
							add("n", v)
						} else {
							add("", v)
						}
					} else {
						add("f", w)
					}
				case numberWord.MatchString(w):
					add("n", w)
				default:
					add("", w)
				}
				i = j
			}
		}
	}
	return out
}

var (
	jsonNumber = regexp.MustCompile(`^-?\d+(\.\d+)?([eE][+-]?\d+)?`)
	jsonWord   = regexp.MustCompile(`^(true|false|null)\b`)
)

func jsonSegs(code string) []seg {
	var out []seg
	plain := 0
	flush := func(to int) {
		if to > plain {
			out = append(out, seg{"", code[plain:to]})
		}
	}
	for i := 0; i < len(code); {
		c := code[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(code) && code[j] != '"' && code[j] != '\n' {
				if code[j] == '\\' && j+1 < len(code) {
					j++
				}
				j++
			}
			if j < len(code) && code[j] == '"' {
				j++
			}
			k := j
			for k < len(code) && isSpace(code[k]) {
				k++
			}
			cls := "s"
			if k < len(code) && code[k] == ':' {
				cls = "k"
			}
			flush(i)
			out = append(out, seg{cls, code[i:j]})
			i, plain = j, j
		case c == '-' || c >= '0' && c <= '9':
			if m := jsonNumber.FindString(code[i:]); m != "" {
				flush(i)
				out = append(out, seg{"n", m})
				i += len(m)
				plain = i
				continue
			}
			i++
		case c == 't' || c == 'f' || c == 'n':
			if m := jsonWord.FindString(code[i:]); m != "" && (i == 0 || !isWordChar(code[i-1])) {
				flush(i)
				out = append(out, seg{"n", m})
				i += len(m)
				plain = i
				continue
			}
			i++
		default:
			i++
		}
	}
	flush(len(code))
	return out
}

func isWordChar(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

var yamlKey = regexp.MustCompile(`^(\s*(?:- )?)(\[[^\]]*\]|[A-Za-z0-9_."'\-/ ]+?)(\s*[:=])(\s|$)`)

func yamlSegs(code string) []seg {
	var out []seg
	add := func(cls, s string) {
		if s != "" {
			out = append(out, seg{cls, s})
		}
	}
	for li, line := range strings.Split(code, "\n") {
		if li > 0 {
			add("", "\n")
		}
		body, comment := line, ""
		if k := commentStart(line); k >= 0 {
			body, comment = line[:k], line[k:]
		}
		rest := body
		if strings.HasPrefix(strings.TrimSpace(body), "[") && strings.HasSuffix(strings.TrimSpace(body), "]") { // a section: [name]
			add("k", body)
			rest = ""
		} else if m := yamlKey.FindStringSubmatch(body); m != nil {
			add("", m[1])
			add("k", m[2])
			add("", m[3]+m[4])
			rest = body[len(m[0]):]
		}
		for _, w := range splitKeep(rest) {
			v := strings.TrimSpace(w)
			switch {
			case v == "":
				add("", w)
			case v[0] == '"' || v[0] == '\'':
				add("s", w)
			case numberWord.MatchString(v) || v == "true" || v == "false" || v == "null" || v == "yes" || v == "no":
				add("n", w)
			default:
				add("", w)
			}
		}
		add("c", comment)
	}
	return out
}

// commentStart is where a # comment begins in a line (at its start or after a space, outside quotes), or -1.
func commentStart(line string) int {
	var q byte
	for i := 0; i < len(line); i++ {
		switch c := line[i]; {
		case q != 0:
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
		case c == '#' && (i == 0 || isSpace(line[i-1])):
			return i
		}
	}
	return -1
}

// splitKeep splits s into words and the white space between them, keeping both.
func splitKeep(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		j := i
		sp := isSpace(s[i])
		for j < len(s) && isSpace(s[j]) == sp {
			j++
		}
		out = append(out, s[i:j])
		i = j
	}
	return out
}

var outNumber = regexp.MustCompile(`\d+(?:[ ,.]\d{3})*(?:[.,:]\d+)*`)

func outSegs(code string) []seg {
	var out []seg
	last := 0
	for _, m := range outNumber.FindAllStringIndex(code, -1) {
		if m[0] > 0 && isWordChar(code[m[0]-1]) { // a digit inside a name stays plain
			continue
		}
		out = append(out, seg{"", code[last:m[0]]}, seg{"n", code[m[0]:m[1]]})
		last = m[1]
	}
	return append(out, seg{"", code[last:]})
}
