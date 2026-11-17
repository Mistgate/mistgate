//go:build !js

package health

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
)

// vlessInboundWith deploys a VLESS profile whose settings the test edits after the plugin has made its defaults. The edit goes
// into the database as it is, the way a profile that predates a validation rule, or a hand-made row, would be there.
func (c *countryEnv) vlessInboundWith(nodeID string, port uint32, edit func(*panelvless.Settings)) {
	c.t.Helper()
	id := c.vlessInbound(nodeID, port)
	var profile string
	if err := c.st.R.QueryRowContext(c.ctx, `SELECT profile_id FROM inbound WHERE id = ?`, id).Scan(&profile); err != nil {
		c.t.Fatal(err)
	}
	var raw string
	if err := c.st.R.QueryRowContext(c.ctx, `SELECT settings_json FROM profile WHERE id = ?`, profile).Scan(&raw); err != nil {
		c.t.Fatal(err)
	}
	var settings panelvless.Settings
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		c.t.Fatal(err)
	}
	edit(&settings)
	edited, err := json.Marshal(settings)
	if err != nil {
		c.t.Fatal(err)
	}
	c.exec(`UPDATE profile SET settings_json = ? WHERE id = ?`, string(edited), profile)
	c.s.invalidateSnapshot()
}

func probeLines(targets []ProbeTarget) []string {
	var out []string
	for _, t := range targets {
		out = append(out, fmt.Sprintf("%s %s:%d %s %q %q", t.NodeID, t.IP, t.Port, t.Kind, t.SNI, t.Path))
	}
	return out
}

// What goes into a line of the probe's job comes from the owner's profiles; a name or a path outside the strict shapes is not
// repaired and not sent: the target is left out, and the node keeps the ones that are clean.
func TestProbeTargetsSkipAnythingTheLineCannotCarry(t *testing.T) {
	c := newCountryEnv(t)
	c.node("de1", "hoster-de1", true)
	c.vlessInboundWith("de1", 443, func(s *panelvless.Settings) {
		s.Reality.ServerNames = []string{"www.example.org"}
		s.Reality.TargetKind, s.Reality.Target = "site", "www.example.org:443"
		s.Transport, s.XHTTP.Path = "xhttp", "/xh-path_1"
	})
	c.names["de1.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.10")}
	c.node("nl1", "hoster-nl1", true)
	c.vlessInboundWith("nl1", 443, func(s *panelvless.Settings) {
		s.Reality.ServerNames = []string{"$(touch /tmp/x).example.org"} // REALITY of the node's own site: not the name it answers to
		s.Transport, s.XHTTP.Path = "xhttp", "/xh-path_2"
	})
	c.names["nl1.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.20")}
	c.node("fi1", "hoster-fi1", true)
	c.vlessInboundWith("fi1", 443, func(s *panelvless.Settings) {
		s.Reality.ServerNames = []string{"ok.example.org"}
		s.Transport, s.XHTTP.Path = "xhttp", "/é"
	})
	c.names["fi1.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.30")}
	c.node("msk1", "hoster-msk1", true)
	c.vlessInboundWith("msk1", 443, func(s *panelvless.Settings) {
		s.Reality.TargetKind, s.Reality.Target = "site", "www.example.org:443"
		s.Reality.ServerNames = []string{"a.example.org\nt 1 203.0.113.9 1 tcp - -"}
	})
	c.names["msk1.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.40")}
	c.node("ber1", "hoster-ber1", true)
	c.vlessInboundWith("ber1", 443, func(s *panelvless.Settings) { s.Reality.ServerNames = []string{"ok.example.org"} })
	c.names["ber1.example.com"] = []netip.Addr{netip.MustParseAddr("10.0.0.5")} // not a public address: no target at all

	got, err := c.s.ProbeTargets(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	lines := probeLines(got)
	for _, line := range lines {
		for _, bad := range []string{"$(", "touch", "\n", "10.0.0.5", "a b", "é"} {
			if strings.Contains(line, bad) {
				t.Errorf("target %s carries %q", line, bad)
			}
		}
	}
	per := map[string]int{}
	for _, tg := range got {
		per[tg.NodeID]++
		if per[tg.NodeID] > 2 {
			t.Errorf("more than two targets for %s", tg.NodeID)
		}
	}
	// fi1's profile does not pass the plugin's own path check, nor msk1's the name check, so those nodes have no inbound to
	// measure; nl1's garbage list is not what its own-site REALITY answers to, so it is probed by the name it does answer to;
	// ber1 is not public.
	want := []string{
		`de1 203.0.113.10:443 fetch "www.example.org" "/xh-path_1"`,
		`de1 203.0.113.10:443 tcp "" ""`,
		`nl1 203.0.113.20:443 tcp "" ""`,
		`nl1 203.0.113.20:443 tls "nl1.example.com" "/xh-path_2"`,
	}
	if !slices.Equal(lines, want) {
		t.Fatalf("targets:\n got %q\nwant %q", lines, want)
	}
}

// The probe names what the inbound answers to: the node's own "Domain (SNI)" of the inbound, not the default of its profile.
func TestProbeTargetsUseTheNameOfTheInbound(t *testing.T) {
	c := newCountryEnv(t)
	for i, id := range []string{"de1", "nl1", "fi1"} {
		c.node(id, "hoster-"+id, true)
		c.names[id+".example.com"] = []netip.Addr{netip.MustParseAddr(fmt.Sprintf("203.0.113.%d", 10+i))}
	}
	hy2 := c.inbound("de1", 443)
	tls := c.vlessCDNInbound("nl1", 443)
	self := c.vlessInbound("fi1", 443)
	sni := func() map[string]string {
		got, err := c.s.ProbeTargets(c.ctx)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, tg := range got {
			if tg.Kind != "tcp" {
				out[tg.NodeID] = tg.SNI
			}
		}
		return out
	}
	want := map[string]string{"de1": "de1.example.com", "nl1": "origin.example.org", "fi1": "fi1.example.com"}
	if got := sni(); !reflect.DeepEqual(got, want) {
		t.Fatalf("before the override: %v, want %v", got, want)
	}
	for _, id := range []string{hy2, tls, self} {
		c.exec(`UPDATE inbound SET tls_server_name_override = 'alt.example.org' WHERE id = ?`, id)
	}
	c.s.invalidateSnapshot()
	want = map[string]string{"de1": "alt.example.org", "nl1": "alt.example.org", "fi1": "alt.example.org"}
	if got := sni(); !reflect.DeepEqual(got, want) {
		t.Fatalf("with the override: %v, want %v", got, want)
	}
}
