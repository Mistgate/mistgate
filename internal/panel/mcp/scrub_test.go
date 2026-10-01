package mcp

import (
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	key43 := "kJ8f2Lw0Zr5uPq1vXy3cNb7mHg4tDa6sEo9iUl2WxYk"
	for _, c := range []struct {
		name, in string
		gone     []string // must not survive
		keep     []string // must survive
	}{
		{"panel token", "x " + canaryTK + " y", []string{canaryTK, "QQQQQQ"}, []string{"x ", " y"}},
		{"confirm token", "cf_" + strings.Repeat("A", 43), []string{"AAAAAAAA"}, nil},
		{"vless link", "see " + canaryVless + " now", []string{"vless://", "CANARYPBK", "11111111"}, []string{"see ", " now"}},
		{"hysteria link", "hysteria2://pw@203.0.113.7:443/?sni=example.com#n", []string{"hysteria2://", "pw@"}, nil},
		{"private key line", "[Interface]\nPrivateKey = " + key43 + "=\nAddress = 10.0.0.2", []string{key43, "[Interface]"}, []string{"Address"}},
		{"preshared", "PresharedKey: " + key43 + "=", []string{key43}, nil},
		{"bare 32-byte key", "key " + key43 + "= end", []string{key43}, []string{"key ", " end"}},
		{"two keys", key43 + " " + key43, []string{key43}, nil},
		{"subscription url", "https://sub.example.com/ZZsecretprefix1234567890/CANARYtoken0123456789abcdefghij", []string{"ZZsecret", "CANARYtoken"}, []string{"sub.example.com"}},
		{"url with a query", "https://example.com/a?token=abc123&x=1", []string{"abc123", "x=1"}, []string{"example.com/a"}},
		{"url with a fragment", "http://example.com/p#secret", []string{"secret"}, []string{"example.com"}},
		{"plain url", "https://example.com/docs/page", nil, []string{"https://example.com/docs/page"}},
	} {
		got := scrub(c.in)
		for _, g := range c.gone {
			if strings.Contains(got, g) {
				t.Errorf("%s: %q survived in %q", c.name, g, got)
			}
		}
		for _, k := range c.keep {
			if !strings.Contains(got, k) {
				t.Errorf("%s: %q was lost from %q", c.name, k, got)
			}
		}
	}
	// ids, versions, hashes and ordinary words are not touched
	for _, s := range []string{"usr_alice", "nod_aaaaaaaaaaaaaaaaaaaaaaaaaa", "v1.2.3", "node_blip", "alert.node_down", strings.Repeat("a", 64), "10.8.0.2", "de1.example.com"} {
		if got := scrub(s); got != s {
			t.Errorf("%q became %q", s, got)
		}
	}
	// the JSON pass stops a link at the closing quote and leaves the structure intact
	j := `{"a":"` + canaryVless + `","b":"https://x.example.com/a?b=c","c":"keep"}`
	got := scrubJSON(j)
	if strings.Contains(got, "vless") || strings.Contains(got, "b=c") || !strings.Contains(got, `"c":"keep"`) || !strings.HasSuffix(got, `"}`) {
		t.Errorf("json pass: %s", got)
	}
}

func TestClean(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"  a \n\t b  ", 50, "a b"},
		{"zero​width‮over", 50, "zerowidthover"},
		{"line1\r\nline2", 50, "line1 line2"},
		{"abcdefghij", 5, "abcde…"},
		{"héllo wörld", 5, "héllo…"},
		{"", 5, ""},
		{"\x00\x01ok\x7f", 10, "ok"},
	} {
		if got := clean(c.in, c.max); got != c.want {
			t.Errorf("clean(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
	if got := clean("a "+canaryTK, 100); strings.Contains(got, "QQQ") {
		t.Errorf("clean keeps a token: %q", got)
	}
	m := cleanMap(map[string]string{"b": "2\n2", "a": strings.Repeat("x", 500)}, 10)
	if len(m) != 2 || m["b"] != "2 2" || len([]rune(m["a"])) != 11 {
		t.Errorf("cleanMap: %v", m)
	}
}
