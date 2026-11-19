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
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	panelvless "github.com/mistgate/mistgate/internal/panel/protocols/vless"
	"github.com/mistgate/mistgate/internal/plugin"
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

// A node that shows a site of its own is probed as a node that shows another site (a fetch by the neighbour's name), even where the
// profile's target is the node's own site: the spec of the inbound says it, not the profile's settings.
func TestProbeTargetsOfATargetSetOnTheNode(t *testing.T) {
	c := newCountryEnv(t)
	c.node("fi1", "hoster-fi1", true)
	c.names["fi1.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.12")}
	self := c.vlessInbound("fi1", 443)
	kinds := func() []string {
		got, err := c.s.ProbeTargets(c.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, tg := range got {
			if tg.Kind != "tcp" {
				out = append(out, tg.Kind+" "+tg.SNI)
			}
		}
		return out
	}
	if got := kinds(); !reflect.DeepEqual(got, []string{"tls fi1.example.com"}) {
		t.Fatalf("the node's own site: %v", got)
	}
	c.exec(`UPDATE inbound SET reality_target_override = 'shop.example.com:443' WHERE id = ?`, self)
	c.s.invalidateSnapshot()
	if got := kinds(); !reflect.DeepEqual(got, []string{"fetch shop.example.com"}) {
		t.Fatalf("with a target of the node: %v", got)
	}
}

// Two services that share 443 by SNI are probed by the name each answers to, one at a time: the node has room for one TLS target.
func TestProbeTargetsTakeTurnsOnASharedPort(t *testing.T) {
	c := newCountryEnv(t)
	c.node("de1", "hoster-de1", true)
	c.names["de1.example.com"] = []netip.Addr{netip.MustParseAddr("203.0.113.10")}
	c.vlessInbound("de1", 443)    // REALITY of the node's own site: the node's name
	c.vlessCDNInbound("de1", 443) // TLS behind a CDN: its own name
	now := time.Unix(1_800_000_000, 0)
	c.s.now = func() time.Time { return now }
	tlsName := func() string {
		t.Helper()
		got, err := c.s.ProbeTargets(c.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("targets = %q", probeLines(got))
		}
		if got[1].Kind == "tcp" || got[1].Port != 443 {
			t.Fatalf("the second target is %q", probeLines(got))
		}
		return got[1].SNI
	}
	first := tlsName()
	if again := tlsName(); again != first {
		t.Fatalf("the name changed within a turn: %q, %q", first, again)
	}
	seen := map[string]bool{first: true}
	for i := 0; i < 3; i++ {
		now = now.Add(probeTurn)
		seen[tlsName()] = true
	}
	if !seen["de1.example.com"] || !seen["origin.example.org"] || len(seen) != 2 {
		t.Fatalf("the names probed over four turns: %v, want both services", seen)
	}
}

// Every service of a shared port is probed with the name of its own route (plugin.TCPSharePlan), whatever kind it is: REALITY of the
// node's own site, TLS behind a CDN, REALITY of another site (a name its target is asked for, not the TLS block of its spec).
func TestVLESSProbesSendTheNameOfTheirRoute(t *testing.T) {
	c := newCountryEnv(t)
	c.node("de1", "hoster-de1", true)
	c.vlessInbound("de1", 443)
	c.vlessCDNInbound("de1", 443)
	raw, err := panelvless.New().DefaultSettings()
	if err != nil {
		t.Fatal(err)
	}
	var other panelvless.Settings
	if err := json.Unmarshal(raw, &other); err != nil {
		t.Fatal(err)
	}
	other.Reality.TargetKind, other.Reality.Target = "site", "www.example.org:443"
	other.Reality.ServerNames = []string{"www.example.org", "www2.example.org"}
	raw, _ = json.Marshal(other)
	prof := must(c.acc.CreateProfile(c.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "vless", Name: "other site", SettingsJson: string(raw)})))
	in := must(c.acc.CreateInbound(c.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: prof.Msg.Profile.Id, NodeId: "de1", PortOverride: 443})))
	c.exec(`UPDATE inbound SET state = 'active' WHERE id = ?`, in.Msg.Inbound.Id)
	c.s.invalidateSnapshot()
	sn, err := c.s.snapshot(c.ctx)
	if err != nil {
		t.Fatal(err)
	}
	var specs []plugin.InboundSpec
	for _, tg := range sn.byNode["de1"] {
		if tg.err != nil {
			t.Fatal(tg.err)
		}
		specs = append(specs, tg.spec)
	}
	share := plugin.TCPSharePlan(specs)[443]
	if !share.Front() || len(share.Routes) != 3 {
		t.Fatalf("the plan of 443 = %+v", share)
	}
	for _, r := range share.Routes {
		tg := sn.targets[r.ID]
		params, ok := vlessProbeParams(Target{Node: tg.node, Inbound: tg.in, Spec: tg.spec, Settings: tg.settings, CredID: "probe", Secret: "66ad4540-b58c-4ad2-9926-ea63445a9b57"})
		if !ok {
			t.Fatalf("route %s (%v): no client parameters", r.ID, r.Names)
		}
		if !slices.Contains(r.Names, strings.ToLower(params.SNI)) {
			t.Errorf("route %s holds %v, the probe sends %q", r.ID, r.Names, params.SNI)
		}
	}
}
