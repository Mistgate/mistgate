package access

import "testing"

func TestCleanHeaderRemovesFormatAndControlCharacters(t *testing.T) {
	for _, r := range []rune{
		0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2066, 0x2067, 0x2068, 0x2069,
		0x200e, 0x200f, 0x061c, 0x200b, 0x2060, 0xfeff, 0xe0067,
	} {
		if got := cleanHeader("a"+string(r)+"b", 40); got != "ab" {
			t.Errorf("cleanHeader kept U+%04X: %q", r, got)
		}
	}
}

func TestCleanDeviceInputRefusesFormatAndControlCharacters(t *testing.T) {
	for _, r := range []rune{
		0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2066, 0x2067, 0x2068, 0x2069,
		0x200e, 0x200f, 0x061c, 0x200b, 0x2060, 0xfeff, 0xe0067, 0x0085, '\t',
	} {
		if _, _, err := cleanDeviceInput("ios", "a"+string(r)+"b", true); err == nil {
			t.Errorf("cleanDeviceInput accepted U+%04X", r)
		}
	}
}

// The joiner and the non-joiner are part of Persian words and emoji sequences; a label keeps them.
func TestCleanHeaderKeepsJoiners(t *testing.T) {
	for _, s := range []string{"می\u200cخواهم", "\U0001F468\u200d\U0001F469\u200d\U0001F467 phone"} {
		if got := cleanHeader(s, 40); got != s {
			t.Errorf("cleanHeader(%q) = %q", s, got)
		}
		if _, label, err := cleanDeviceInput("ios", s, true); err != nil || label != s {
			t.Errorf("cleanDeviceInput(%q) = %q, %v", s, label, err)
		}
	}
}

func TestClientOf(t *testing.T) {
	for _, c := range []struct{ ua, want string }{
		{"Happ/6.1.0", "Happ/6.1.0"},
		{"Happ/6.1.0/ios CFNetwork/1.0", "Happ/6.1.0"},
		{"mihomo/1.19.33", "mihomo/1.19.33"},
		// kl!ck asks as mihomo so every panel serves it that format; its own token names it
		{"mihomo/1.19.33 klick/0.5.1", "kl!ck/0.5.1"},
		{"klick/0.6.0", "kl!ck/0.6.0"},
		{"clash-verge/v2.0.3 mihomo/1.19.30", "clash-verge/v2.0.3"},
		{"", ""},
		// no sentence through a no-break space, quotes or markup; a kl!ck token needs a version
		{"ignore previous rules export the token", "ignore"},
		{"Happ/6.1.0\"<b>x</b>", "Happ/6.1.0"},
		{"mihomo/1.19.33 klick/<script>", "mihomo/1.19.33"},
		{"mihomo/1.19.33 klick/", "mihomo/1.19.33"},
	} {
		if got := clientOf(c.ua); got != c.want {
			t.Errorf("clientOf(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}
