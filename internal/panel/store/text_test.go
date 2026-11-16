package store

import "testing"

func TestCleanDisplayText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"mi\u200ckhaham", "mi\u200ckhaham"}, // the non-joiner of a word in Persian script stays
		{"\U0001F468\u200d\U0001F469\u200d\U0001F467", "\U0001F468\u200d\U0001F469\u200d\U0001F467"}, // so does the joiner of an emoji family
		{"a\u202eb\u2066c\u2069d\u200ee\u200ff\u061cg", "abcdefg"},
		{"a\u200bb\u2060c\ufeffd\U000e0067e\x00f\x7fg", "abcdefg"},
		{"one\ntwo\u2028three", "onetwothree"},
		{"  padded  ", "padded"},
		{"a\xffb", "ab"},
	} {
		if got := CleanDisplayText(c.in, 40); got != c.want {
			t.Errorf("CleanDisplayText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := CleanDisplayText("abcde", 3); got != "abc" {
		t.Errorf("cut to 3 characters: %q", got)
	}
}
