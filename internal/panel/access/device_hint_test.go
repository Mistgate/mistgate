package access

import "testing"

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
	} {
		if got := clientOf(c.ua); got != c.want {
			t.Errorf("clientOf(%q) = %q, want %q", c.ua, got, c.want)
		}
	}
}
