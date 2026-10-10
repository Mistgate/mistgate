package subs

import (
	"bytes"
	"compress/gzip"
	"context"
	"flag"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Errorf("%s differs:\n got:\n%s\nwant:\n%s", name, got, want)
	}
}

// fixture reads a proxy fragment the plugins' own golden files hold (one source of truth for the proxy text).
func fixture(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const (
	hy2Line = "../protocols/hysteria2/testdata/mihomo_salamander.yaml"
	awgLine = "../protocols/awg/testdata/awg31.mihomo.yaml"
)

func ruSplit() *dns.Preset {
	return &dns.Preset{
		Name: "ru", Transport: dns.KindPlain, SplitDirect: true,
		Servers: []dns.Server{{Variant: "cloudflare/standard"}, {Variant: "google/standard"}},
		Split:   []dns.SplitRule{{Suffixes: []string{".ru", ".su", ".xn--p1ai"}, Servers: []dns.Server{{Variant: "yandex/basic"}}}},
	}
}

type profileDoc struct {
	Proxies []map[string]any `yaml:"proxies"`
	Groups  []struct {
		Name    string   `yaml:"name"`
		Type    string   `yaml:"type"`
		Proxies []string `yaml:"proxies"`
	} `yaml:"proxy-groups"`
	DNS   map[string]any `yaml:"dns"`
	Rules []string       `yaml:"rules"`
}

func parseProfile(t *testing.T, b []byte) profileDoc {
	t.Helper()
	var d profileDoc
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		t.Fatalf("not a profile: %v\n%s", err, b)
	}
	return d
}

// The whole profile for one hysteria2 and one AWG 3.1 proxy under the Russia preset.
func TestMihomoProfileGolden(t *testing.T) {
	p := mihomoProfile{
		title:  "Example VPN",
		lines:  []string{fixture(t, hy2Line), fixture(t, awgLine)},
		names:  []string{"\U0001F1E9\U0001F1EA de1 · hy2", "\U0001F1E9\U0001F1EA de1 · awg"},
		preset: ruSplit(),
	}
	b, err := p.build()
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "mihomo_profile.yaml", string(b))
	d := parseProfile(t, b)
	if len(d.Proxies) != 2 || d.Proxies[0]["name"] != "\U0001F1E9\U0001F1EA de1 · hy2" || d.Proxies[1]["type"] != "wireguard" {
		t.Errorf("proxies = %v", d.Proxies)
	}
	if len(d.Groups) != 1 || d.Groups[0].Name != "Example VPN" || d.Groups[0].Type != "select" ||
		strings.Join(d.Groups[0].Proxies, "|") != "\U0001F1E9\U0001F1EA de1 · hy2|\U0001F1E9\U0001F1EA de1 · awg|DIRECT" {
		t.Errorf("groups = %+v", d.Groups)
	}
	if last := d.Rules[len(d.Rules)-1]; last != "MATCH,Example VPN" || d.Rules[0] != "DOMAIN-SUFFIX,ru,DIRECT" || len(d.Rules) != 4 {
		t.Errorf("rules = %v", d.Rules)
	}
	// The group named in the dns section is the group of the profile.
	ns := d.DNS["nameserver"].([]any)
	if ns[0] != "1.1.1.1#Example%20VPN" {
		t.Errorf("nameserver = %v", ns)
	}
}

// Names are hostile input: whatever a node, a profile or the brand is called, the profile keeps exactly the proxies it
// was given and one rule per direct suffix plus MATCH.
func TestMihomoProfileHostileNames(t *testing.T) {
	hostile := []string{
		"x\"\n  - name: evil", "- name: evil", "</script>", "null", "yes", "{a: b}", "a: b #c", "'q'", "tab\there", " ",
		"\U0001F1E9\U0001F1EA \U0001F600", "DIRECT", "reject", "Global", "", "same", "same",
	}
	var lines []string
	for range hostile {
		lines = append(lines, fixture(t, hy2Line))
	}
	for _, title := range []string{"Brand, Inc", "a#b&c=d", "50% off", "  ", "x\ny", "MATCH,DIRECT"} {
		b, err := (mihomoProfile{title: title, lines: lines, names: hostile, preset: ruSplit()}).build()
		if err != nil {
			t.Fatal(err)
		}
		d := parseProfile(t, b)
		if len(d.Proxies) != len(hostile) || len(d.Groups) != 1 || len(d.Rules) != 4 {
			t.Fatalf("title %q: %d proxies, %d groups, %d rules\n%s", title, len(d.Proxies), len(d.Groups), len(d.Rules), b)
		}
		seen := map[string]bool{"DIRECT": true, "REJECT": true, "GLOBAL": true}
		for i, px := range d.Proxies {
			n := px["name"].(string)
			if n == "" || seen[strings.ToUpper(n)] && !strings.Contains(n, "·") {
				t.Errorf("title %q: proxy %d has the name %q", title, i, n)
			}
			if seen[n] {
				t.Errorf("title %q: name %q twice", title, n)
			}
			seen[n] = true
			if px["type"] != "hysteria2" || len(px) != 10 {
				t.Errorf("proxy %d changed shape: %v", i, px)
			}
		}
		g := d.Groups[0].Name
		if strings.ContainsAny(g, ",#&=\"%\n") || g == "" || seen[g] {
			t.Errorf("title %q: group name %q", title, g)
		}
		if d.Rules[len(d.Rules)-1] != "MATCH,"+g || strings.Count(d.Rules[len(d.Rules)-1], ",") != 1 {
			t.Errorf("title %q: last rule %q", title, d.Rules[len(d.Rules)-1])
		}
		if len(d.Groups[0].Proxies) != len(hostile)+1 {
			t.Errorf("title %q: the group lists %d members", title, len(d.Groups[0].Proxies))
		}
	}
}

// A view with nothing in it (an expired user) is still a valid profile; a line that is not one proxy is skipped.
func TestMihomoProfileEmptyAndBroken(t *testing.T) {
	b, err := (mihomoProfile{title: "T"}).build()
	if err != nil {
		t.Fatal(err)
	}
	d := parseProfile(t, b)
	if len(d.Proxies) != 0 || len(d.Groups[0].Proxies) != 1 || d.Groups[0].Proxies[0] != "DIRECT" || d.DNS != nil || len(d.Rules) != 1 {
		t.Errorf("empty profile: %+v\n%s", d, b)
	}
	for _, bad := range []string{"", "not: a list", "- 1\n- 2", "- a: 1\n- b: 2", "- name: x\n  type: [", "- [x]", "- type: only"} {
		b, err := (mihomoProfile{title: "T", lines: []string{bad, fixture(t, awgLine)}}).build()
		if err != nil {
			t.Fatal(err)
		}
		if d := parseProfile(t, b); len(d.Proxies) != 1 || d.Proxies[0]["type"] != "wireguard" {
			t.Errorf("%q: %v", bad, d.Proxies)
		}
	}
	// The plugin's own name is kept when the assembler is given none.
	b, _ = (mihomoProfile{title: "T", lines: []string{fixture(t, awgLine)}}).build()
	if d := parseProfile(t, b); d.Proxies[0]["name"] != "de1 · AWG 3.1" {
		t.Errorf("name = %v", d.Proxies[0]["name"])
	}
	// A preset the core cannot use leaves the dns section out but keeps the rest.
	b, _ = (mihomoProfile{title: "T", lines: []string{fixture(t, awgLine)}, preset: &dns.Preset{Name: "none"}}).build()
	if d := parseProfile(t, b); d.DNS != nil || len(d.Proxies) != 1 || d.Rules[len(d.Rules)-1] != "MATCH,T" {
		t.Errorf("without dns: %+v", d)
	}
}

func TestAcceptsGzip(t *testing.T) {
	for in, want := range map[string]bool{
		"": false, "gzip": true, "GZIP": true, "deflate, gzip;q=0.5": true, "br, gzip, deflate": true, "gzip;q=0": false, "gzip; q=0": false,
		"identity": false, "x-gzip": false, "gzip;q=0.0": false, " gzip , br": true,
	} {
		r, _ := http.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Encoding", in)
		if acceptsGzip(r) != want {
			t.Errorf("%q: %v", in, !want)
		}
	}
	zr, err := gzip.NewReader(bytes.NewReader(gzipped([]byte("hello"))))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(zr); string(b) != "hello" {
		t.Errorf("gzip round trip: %q", b)
	}
}

func TestGzippedMatchesFreshWriter(t *testing.T) {
	input := []byte("profile-name: de1 · Hysteria2\n")
	var want bytes.Buffer
	zw, err := gzip.NewWriterLevel(&want, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := zw.Write(input); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if got := gzipped(input); !bytes.Equal(got, want.Bytes()) {
			t.Fatalf("gzip output changed on call %d", i+1)
		}
	}
}

// A burst of requests cannot get more than the budget through.
func TestWriteAdmitConcurrent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	limiter := securitylimit.NewMemory(func() time.Time { return now }, 0)
	h := Handler(&fakeSrc{valid: map[string]access.SubView{}}, decoyHandler, Config{Limiter: limiter, Now: func() time.Time { return now }}).(*handler)
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h.writeAdmit(context.Background(), tokA) == 0 {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 20 {
		t.Errorf("%d admitted, want 20", admitted.Load())
	}
}

// writeAdmit: the budget is per hour and a window restarts after an hour; a negative budget is no limit.
func TestWriteAdmit(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	limiter := securitylimit.NewMemory(func() time.Time { return now }, 0)
	h := Handler(&fakeSrc{valid: map[string]access.SubView{}}, decoyHandler, Config{Limiter: limiter, MaxWritesPerHour: 3, Now: func() time.Time { return now }}).(*handler)
	for i := 0; i < 3; i++ {
		if retry := h.writeAdmit(context.Background(), tokA); retry != 0 {
			t.Fatalf("write %d refused", i)
		}
	}
	now = now.Add(10 * time.Minute)
	if retry := h.writeWait(context.Background(), tokA); retry <= 0 || retry > 51*time.Minute {
		t.Errorf("retry = %v", retry)
	}
	now = now.Add(51 * time.Minute)
	if retry := h.writeAdmit(context.Background(), tokA); retry != 0 {
		t.Error("a new hour must restart the budget")
	}
	free := Handler(&fakeSrc{valid: map[string]access.SubView{}}, decoyHandler, Config{Limiter: securitylimit.NewMemory(nil, 0), MaxWritesPerHour: -1}).(*handler)
	for i := 0; i < 1000; i++ {
		if retry := free.writeAdmit(context.Background(), tokA); retry != 0 {
			t.Fatal("unlimited refused")
		}
	}
}
