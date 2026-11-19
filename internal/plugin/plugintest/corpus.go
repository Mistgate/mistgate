// Package plugintest holds the inputs the tests of the protocol-fact copies share (it is removed with the last copy).
package plugintest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Case is one inbound the copies of a protocol fact are asked about.
type Case struct {
	ID, Protocol string
	TLSMode      int // plugin.TLSMode
	SNI          string
	Settings     string
}

// Corpus is the common set of inputs: Hysteria2 settings at the edges of the port rule and of masquerade.type, VLESS settings
// of every shape (TLS, REALITY with the node's own site and with another one, a CDN host, missing or broken parts), each also
// with a TLS block.
func Corpus() []Case {
	var cs []Case
	for _, h := range []struct{ id, s string }{
		{"empty", ``}, {"null", `null`}, {"obj", `{}`}, {"bad", `not json`},
		{"port0", `{"masquerade":{"tcp_port":0}}`}, {"neg", `{"masquerade":{"tcp_port":-1}}`},
		{"p443", `{"masquerade":{"tcp_port":443}}`}, {"p8443", `{"masquerade":{"tcp_port":8443}}`},
		{"p65535", `{"masquerade":{"tcp_port":65535}}`}, {"p65536", `{"masquerade":{"tcp_port":65536}}`}, {"p70000", `{"masquerade":{"tcp_port":70000}}`},
		{"pnull", `{"masquerade":{"tcp_port":null}}`}, {"pstr", `{"masquerade":{"tcp_port":"443"}}`},
		{"none", `{"masquerade":{"type":"none"}}`}, {"none8443", `{"masquerade":{"type":"none","tcp_port":8443}}`},
		{"NONE", `{"masquerade":{"type":"NONE"}}`}, {"none0", `{"masquerade":{"type":"none","tcp_port":0}}`}, {"decoy", `{"masquerade":{"type":"decoy"}}`},
	} {
		cs = append(cs, Case{"hy2-" + h.id, "hysteria2", 1, "de1.example.com", h.s})
	}
	cs = append(cs, Case{"hy2-mode0", "hysteria2", 0, "", `{}`}, Case{"hy2-self", "hysteria2", 3, "192.0.2.1", `{}`},
		Case{"vless-as-hy2", "vless", 1, "de1.example.com", `{}`})
	for _, v := range []struct{ id, s string }{
		{"empty", ``}, {"bad", `not json`}, {"obj", `{}`}, {"null", `null`},
		{"tls", `{"security":"tls"}`},
		{"tls-cdn", `{"security":"tls","cdn":{"host":"cdn.example.com"}}`},
		{"tls-cdn-caps", `{"security":"tls","cdn":{"host":" CDN.Example.com. "}}`},
		{"tls-cdn-blank", `{"security":"tls","cdn":{"host":" "}}`},
		{"tls-cdn-empty", `{"security":"tls","cdn":{"host":""}}`},
		{"tls-cdn-null", `{"security":"tls","cdn":null}`},
		{"tls-names", `{"security":"tls","reality":{"server_names":["x.example.com"]}}`},
		{"self", `{"security":"reality","reality":{"target":"self","server_names":["Self.example.com"]}}`},
		{"self-sp", `{"security":"reality","reality":{"target":" self ","server_names":["self.example.com"]}}`},
		{"self-nonames", `{"security":"reality","reality":{"target":"self"}}`},
		{"other", `{"security":"reality","reality":{"target":"www.example.org:443","server_names":["Www.Example.org."]}}`},
		{"other-sp", `{"security":"reality","reality":{"target":" www.example.org:443 ","server_names":[" www.example.org "]}}`},
		{"other-v6", `{"security":"reality","reality":{"target":"[2001:db8::1]:443","server_names":["www.example.org"]}}`},
		{"other-ip", `{"security":"reality","reality":{"target":"203.0.113.9:443"}}`},
		{"other-nonames", `{"security":"reality","reality":{"target":"www.example.org:443"}}`},
		{"other-nohost", `{"security":"reality","reality":{"target":"www.example.org","server_names":["www.example.org"]}}`},
		{"other-multi", `{"security":"reality","reality":{"target":"www.example.org:443","server_names":["","b.example.com","www.example.org"]}}`},
		{"other-blankname", `{"security":"reality","reality":{"target":"www.example.org:443","server_names":["  "]}}`},
		{"reality-notarget", `{"security":"reality","reality":{"server_names":["a.example.com"]}}`},
		{"reality-noblock", `{"security":"reality"}`},
		{"reality-cdn", `{"security":"reality","reality":{"target":"self","server_names":["a.example.com"]},"cdn":{"host":"cdn.example.com"}}`},
		{"nosec-self", `{"reality":{"target":"self","server_names":["a.example.com"]}}`},
		{"nosec-other", `{"reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`},
		{"nosec-cdn", `{"cdn":{"host":"cdn.example.com"}}`},
		{"foo-self", `{"security":"foo","reality":{"target":"self","server_names":["a.example.com"]}}`},
		{"upper-sec", `{"security":"REALITY","reality":{"target":"www.example.org:443","server_names":["www.example.org"]}}`},
	} {
		cs = append(cs, Case{"vless-" + v.id, "vless", 0, "", v.s}, Case{"vless-" + v.id + "+sni", "vless", 1, "de1.example.com", v.s})
	}
	return cs
}

// Golden compares lines with the file name under the testdata of the test's package; FACTS_UPDATE=1 rewrites the file.
func Golden(t testing.TB, name string, lines []string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	got := strings.Join(lines, "\n") + "\n"
	if os.Getenv("FACTS_UPDATE") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(string(raw), "\n")
	for i := 0; i < len(gl) && i < len(wl); i++ {
		if gl[i] != wl[i] {
			t.Fatalf("%s line %d:\n got %s\nwant %s", name, i+1, gl[i], wl[i])
		}
	}
	if len(gl) != len(wl) {
		t.Fatalf("%s: %d lines, want %d", name, len(gl), len(wl))
	}
}
