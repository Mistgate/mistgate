package store

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Clip returns s as valid UTF-8 of at most n bytes, cut on a rune boundary. Text that comes from a node or a
// browser header is clipped with it before it is stored: a byte-wise cut can end inside a multi-byte
// character, and an invalid string makes every proto3 response that carries it fail to marshal.
func Clip(s string, n int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// UnsafeDisplayRune reports a character that must not reach text shown to people: a control character, a
// bidirectional control or isolate, a zero-width space, a deprecated format character, a byte-order mark or a tag
// character. They reorder or hide the text around them. The joiner and the non-joiner (U+200D, U+200C) stay: Persian
// and Arabic words and emoji sequences need them.
func UnsafeDisplayRune(r rune) bool {
	switch {
	case unicode.IsControl(r), r == 0x2028, r == 0x2029:
		return true
	case r == 0x061c, r == 0x200b, r == 0x200e, r == 0x200f, r == 0xfeff:
		return true
	case r >= 0x202a && r <= 0x202e, r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x206f:
		return true
	}
	return r >= 0xe0000 && r <= 0xe007f
}

// CleanDisplayText makes text from a subscriber or a node fit to show: invalid UTF-8 and UnsafeDisplayRune
// characters go, the ends are trimmed, and it is cut to max characters.
func CleanDisplayText(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "")
	}
	s = strings.Map(func(r rune) rune {
		if UnsafeDisplayRune(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}
