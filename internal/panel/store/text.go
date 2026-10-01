package store

import (
	"strings"
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
