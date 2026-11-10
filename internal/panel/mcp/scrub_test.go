//go:build !js

package mcp

import (
	"encoding/json"
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
		{"Telegram bot token", "bot 123456:" + strings.Repeat("Ab3_-", 6), []string{"123456:"}, []string{"bot ", "[token]"}},
		{"preshared", "PresharedKey: " + key43 + "=", []string{key43}, nil},
		{"bare 32-byte key", "key " + key43 + "= end", []string{key43}, []string{"key ", " end"}},
		{"hex key", "key " + strings.Repeat("0123456789abcdef", 2) + " end", []string{strings.Repeat("0123456789abcdef", 2)}, []string{"key ", " end"}},
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
	for _, s := range []string{"usr_alice", "nod_aaaaaaaaaaaaaaaaaaaaaaaaaa", "v1.2.3", "node_blip", "alert.node_down", strings.Repeat("a", 64), "10.8.0.2", "de1.example.com", "https://example.com/docs/page"} {
		if got := scrub(s); got != s {
			t.Errorf("%q became %q", s, got)
		}
	}
	if got := scrub("https://example.com/550e8400-e29b-41d4-a716-446655440000"); got != "https://example.com/[uuid]" {
		t.Errorf("UUID in URL was not redacted as a UUID: %q", got)
	}
	// the JSON pass stops a link at the closing quote and leaves the structure intact
	j := `{"a":"` + canaryVless + `","b":"https://x.example.com/a?b=c","c":"keep"}`
	got := scrubJSON(j)
	if strings.Contains(got, "vless") || strings.Contains(got, "b=c") || !strings.Contains(got, `"c":"keep"`) || !strings.HasSuffix(got, `"}`) {
		t.Errorf("json pass: %s", got)
	}
}

func TestScrubCredentialFormatsInTextAndSerializedResults(t *testing.T) {
	secrets := []string{
		"sk-ant-api03-" + strings.Repeat("aB3_", 10),
		"sk-" + strings.Repeat("Ab3x", 8),
		"sk-proj-" + strings.Repeat("Pr0j", 8),
		"ghp_" + strings.Repeat("aB3x", 9),
		"gho_" + strings.Repeat("aB3x", 9),
		"github_pat_" + strings.Repeat("aB3x_", 8),
		"xoxb-1234567890-" + strings.Repeat("aB3x", 6),
		"xoxa-1234567890-" + strings.Repeat("aB3x", 6),
		"xoxp-1234567890-" + strings.Repeat("aB3x", 6),
		"xoxr-1234567890-" + strings.Repeat("aB3x", 6),
		"AKIA1234567890ABCDEF",
		"AIza" + strings.Repeat("aB3x_", 7),
		"AGE-SECRET-KEY-1" + strings.Repeat("QWERTY1234567890", 4),
		"-----BEGIN OPENSSH PRIVATE KEY-----\n" + strings.Repeat("aB3x/", 20) + "\n-----END OPENSSH PRIVATE KEY-----",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0." + strings.Repeat("aB3x", 8),
		"Bearer " + strings.Repeat("aB3x", 10),
		"aB3x_7ZpQ9mN2vC5kL8sD1fG4hJ6uW0yR3tE",                             // generic long high-entropy credential
		"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz+/", // base64-shaped generic credential
	}
	for _, secret := range secrets {
		encoded, err := json.Marshal(secret)
		if err != nil {
			t.Fatal(err)
		}
		for name, got := range map[string]string{
			"text": scrub(secret),
			"json": scrubJSON(`{"credential":` + string(encoded) + `}`),
		} {
			if strings.Contains(got, secret) {
				t.Errorf("%s scrub kept credential %q in %q", name, secret, got)
			}
		}
	}
}

func TestScrubDoesNotOverRedactOrdinaryIdentifiers(t *testing.T) {
	for _, ordinary := range []string{
		"nod_aaaaaaaaaaaaaaaaaaaaaaaaaa", "nod_1234567890abcdef", "v1.2.3", "10.2.30",
		"de1.example.com", "vpn-node-27.example.com", "a-long-lowercase-hostname.example.com",
		strings.Repeat("a", 64),
	} {
		if got := scrub(ordinary); got != ordinary {
			t.Errorf("ordinary value %q became %q", ordinary, got)
		}
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

func TestDebugCaptureAttributeScrubKeepsAWGKernelFields(t *testing.T) {
	got := scrubDebugCaptureAttrs(map[string]string{"event": "initiation_in", "peer": "7", "inbound": "inb_awg", "code": "keep", "secret": "drop"})
	for key, want := range map[string]string{"event": "initiation_in", "peer": "7", "inbound": "inb_awg", "code": "keep"} {
		if got[key] != want {
			t.Errorf("attribute %s = %q, want %q", key, got[key], want)
		}
	}
	if _, ok := got["secret"]; ok {
		t.Errorf("unlisted attribute survived: %v", got)
	}
}
