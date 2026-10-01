package hysteria2

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A pin or address a hostile node managed to store must not add parameters, extra lines or another host to
// the subscription line of every user of the inbound.
func TestRenderRefusesHostilePinAndHost(t *testing.T) {
	p := New()
	in := func(pin, host string) protocols.RenderInput {
		iv := inbound(plugin.TLSSelfSigned, "de2.example.com", pin)
		iv.Node.Address = host
		return protocols.RenderInput{Format: plugin.FormatURIList, Settings: settings(t, nil), Inbound: iv, Secret: "tok"}
	}

	for _, pin := range []string{
		"aa&sni=evil.example\nhysteria2://x@evil.example:1/",
		strings.Repeat("a", 63),          // too short
		strings.Repeat("a", 65),          // too long
		strings.Repeat("g", 64),          // not hex
		strings.Repeat("ab", 31) + "a%&", // right length, hostile characters
		"\n" + strings.Repeat("ab", 32),  // a line break in front of a valid pin
	} {
		f, ok := p.Render(in(pin, "de2.example.com"))
		if !ok {
			t.Fatalf("pin %q: Render refused, want a line without a pin", pin)
		}
		got := string(f.Data)
		if strings.ContainsAny(got, "\r\n") || strings.Contains(got, "pinSHA256") || strings.Contains(got, "insecure") || strings.Contains(got, "evil") {
			t.Errorf("pin %q leaked into %q", pin, got)
		}
	}

	ok64 := strings.Repeat("Ab", 32) // upper case is normalised, the stored form is lower case
	f, ok := p.Render(in(ok64, "de2.example.com"))
	if !ok || !strings.Contains(string(f.Data), "pinSHA256="+strings.ToLower(ok64)) {
		t.Errorf("valid pin: %q, %v", f.Data, ok)
	}

	for _, host := range []string{"evil.example/?x=", "a b.example", "h@evil.example", "h.example#frag", "h.example\nx"} {
		if _, ok := p.Render(in(ok64, host)); ok {
			t.Errorf("host %q must be refused", host)
		}
	}

	// Every component survives a round trip through a URL parser unchanged.
	f, _ = p.Render(in(ok64, "de2.example.com"))
	u, err := url.Parse(string(f.Data))
	if err != nil || u.Hostname() != "de2.example.com" || u.Query().Get("pinSHA256") != strings.ToLower(ok64) {
		t.Errorf("parsed %+v, %v", u, err)
	}
}
