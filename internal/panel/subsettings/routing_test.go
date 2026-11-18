package subsettings

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

const familyProfile = `{"Name":"family","GlobalProxy":"true","LastUpdated":"1760000000","RemoteDNSType":"DoH","RemoteDNSDomain":"https://dns.example.com/dns-query","RemoteDNSIP":"192.0.2.53",` +
	`"DomesticDNSType":"DoU","DomesticDNSIP":"192.0.2.54","DnsHosts":{"dns.example.com":"192.0.2.53"},"DirectSites":["geosite:category-ru","domain:example.ru"],` +
	`"DirectIp":["geoip:ru","192.0.2.0/24"],"ProxySites":["domain:example.org"],"ProxyIp":[],"BlockSites":["geosite:category-ads"],"BlockIp":[],` +
	`"Geoipurl":"https://example.com/geoip.dat","Geositeurl":"https://example.com/geosite.dat","DomainStrategy":"IPIfNonMatch","FakeDNS":"false","useChunkFiles":true}`

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestParseRoutingInputForms(t *testing.T) {
	for name, tc := range map[string]struct{ in, mode string }{
		"json":            {familyProfile, ""},
		"happ onadd":      {"happ://routing/onadd/" + b64(familyProfile), "onadd"},
		"incy add":        {"incy://routing/add/" + b64(familyProfile), "add"},
		"any scheme":      {"sbx://routing/onadd/" + b64(familyProfile), "onadd"},
		"no scheme":       {"://routing/onadd/" + b64(familyProfile), "onadd"},
		"upper verb":      {"happ://routing/ONADD/" + b64(familyProfile), "onadd"},
		"bare base64":     {b64(familyProfile), ""},
		"unpadded":        {strings.TrimRight(b64(familyProfile), "="), ""},
		"url alphabet":    {strings.NewReplacer("+", "-", "/", "_").Replace(b64(familyProfile)), ""},
		"line breaks":     {b64(familyProfile)[:40] + "\n" + b64(familyProfile)[40:], ""},
		"percent encoded": {"happ://routing/onadd/" + strings.ReplaceAll(b64(familyProfile), "=", "%3D"), "onadd"},
		"spaces around":   {"  \n" + familyProfile + "\n ", ""},
	} {
		p, err := ParseRoutingInput(tc.in)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if p.Mode != tc.mode || p.Doc.Name != "family" || len(p.Doc.DirectSites) != 2 || len(p.Dropped) != 0 {
			t.Errorf("%s: mode %q doc %+v dropped %v", name, p.Mode, p.Doc, p.Dropped)
		}
	}
}

func TestParseRoutingInputCanonical(t *testing.T) {
	p, err := ParseRoutingInput(familyProfile)
	if err != nil {
		t.Fatal(err)
	}
	c := p.Doc.Canonical()
	again, err := ParseRoutingInput(c)
	if err != nil || again.Doc.Canonical() != c {
		t.Fatalf("canonical form is not stable: %v\n%s\n%s", err, c, again.Doc.Canonical())
	}
	if !strings.HasPrefix(c, `{"Name":"family","GlobalProxy":"true","LastUpdated":"1760000000","RemoteDNSType":"DoH"`) {
		t.Errorf("field order: %s", c)
	}
}

func TestParseRoutingInputRefusals(t *testing.T) {
	huge := `{"DirectSites":["` + strings.Repeat("a", 250) + `"` + strings.Repeat(`,"`+strings.Repeat("b", 250)+`"`, 140) + `]}`
	for name, tc := range map[string]struct{ in, want string }{
		"empty":           {"   ", "empty"},
		"words":           {"hello world", "not a JSON object"},
		"punctuation":     {"hello, world!", "not a link"},
		"not an object":   {b64(`[1,2]`), "not a JSON object"},
		"empty object":    {`{}`, "no routing field"},
		"only unknown":    {`{"foo":1}`, "no routing field"},
		"broken json":     {`{"Name":`, "not valid JSON"},
		"trailing":        {`{"Name":"a"} {"Name":"b"}`, "text after"},
		"off link":        {"happ://routing/off", "turns routing off"},
		"autorouting":     {"incy://autorouting/onadd/https%3A%2F%2Fexample.com%2Fr", "autorouting"},
		"other link":      {"happ://add/https://example.com/sub", "not a routing link"},
		"bad scheme":      {"he llo://routing/add/" + b64(familyProfile), "not a scheme"},
		"list as text":    {`{"DirectSites":"geosite:ru"}`, "DirectSites: must be a list"},
		"number in list":  {`{"DirectSites":[1]}`, "DirectSites: must be a list"},
		"space in entry":  {`{"DirectSites":["a b"]}`, "space"},
		"empty entry":     {`{"BlockIp":[""]}`, "empty"},
		"name is object":  {`{"Name":{"a":1}}`, "Name: must be text"},
		"bad global":      {`{"GlobalProxy":"yes"}`, `GlobalProxy: "true" or "false"`},
		"bad dns type":    {`{"RemoteDNSType":"DoT"}`, `"DoH" or "DoU"`},
		"http doh":        {`{"RemoteDNSType":"DoH","RemoteDNSDomain":"http://dns.example.com/q"}`, "RemoteDNSDomain: an https address"},
		"http geo":        {`{"Geoipurl":"http://example.com/geoip.dat"}`, "Geoipurl: an https address"},
		"geo creds":       {`{"Geositeurl":"https://user:pw@example.com/g.dat"}`, "Geositeurl: an https address"},
		"geo file scheme": {`{"Geositeurl":"file:///etc/passwd"}`, "Geositeurl: an https address"},
		"dns ip not ip":   {`{"RemoteDNSIP":"dns.example.com"}`, "an IP address"},
		"bad strategy":    {`{"DomainStrategy":"Fast"}`, "DomainStrategy"},
		"long name":       {`{"Name":"` + strings.Repeat("n", 61) + `"}`, "Name: at most 60"},
		"bad hosts":       {`{"DnsHosts":{"a":1}}`, "DnsHosts"},
		"bad updated":     {`{"LastUpdated":"yesterday","DirectSites":[]}`, "LastUpdated"},
		"too big":         {huge, "at most"},
		"control char":    {"{\"Name\":\"a\\u0007b\"}", "control characters"},
	} {
		_, err := ParseRoutingInput(tc.in)
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func TestParseRoutingUnknownFieldsAreDroppedAndNamed(t *testing.T) {
	p, err := ParseRoutingInput(`{"Name":"a","DirectSites":["domain:example.ru"],"Evil":"<script>","Nested":{"x":[1]},"` + "‮" + `bidi":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Dropped) != 3 || p.Dropped[0] != "Evil" || p.Dropped[1] != "Nested" {
		t.Errorf("dropped = %q", p.Dropped)
	}
	for _, bad := range []string{"script", "Nested", "Evil", "bidi", "‮"} {
		if strings.Contains(p.Doc.Canonical(), bad) {
			t.Errorf("canonical JSON carries %q: %s", bad, p.Doc.Canonical())
		}
	}
	for _, d := range p.Dropped {
		if strings.Contains(d, "‮") {
			t.Errorf("dropped name is not cleaned: %q", d)
		}
	}
}

func TestParseRoutingLenientTypes(t *testing.T) {
	p, err := ParseRoutingInput(`{"GlobalProxy":true,"FakeDNS":false,"LastUpdated":1760000000,"useChunkFiles":"true","DirectSites":null}`)
	if err != nil {
		t.Fatal(err)
	}
	d := p.Doc
	if d.GlobalProxy != "true" || d.FakeDNS != "false" || d.LastUpdated != "1760000000" || d.UseChunkFiles == nil || !*d.UseChunkFiles || d.Name != "Routing" || d.DirectSites == nil {
		t.Errorf("doc = %+v", d)
	}
	// the six lists are always written, so the app sees `[]`, never a missing field
	if !strings.Contains(d.Canonical(), `"DirectSites":[],"DirectIp":[],"ProxySites":[],"ProxyIp":[],"BlockSites":[],"BlockIp":[]`) {
		t.Errorf("canonical = %s", d.Canonical())
	}
}

func TestRoutingWithDNS(t *testing.T) {
	p, _ := ParseRoutingInput(`{"Name":"a","RemoteDns":"192.0.2.1","DomesticDns":"192.0.2.2","DnsHosts":{"own.example.com":"192.0.2.9","dns.example.com":"192.0.2.99"},"DirectSites":["domain:example.ru"],"BlockSites":["geosite:ads"]}`)
	got := p.Doc.WithDNS(RoutingDNS{
		RemoteType: "DoH", RemoteDomain: "https://dns.example.com/dns-query", RemoteIP: "192.0.2.53",
		DomesticType: "DoU", DomesticIP: "192.0.2.54", Hosts: map[string]string{"dns.example.com": "192.0.2.53"},
	})
	var m map[string]any
	if err := json.Unmarshal([]byte(got.Canonical()), &m); err != nil {
		t.Fatal(err)
	}
	if m["RemoteDNSType"] != "DoH" || m["RemoteDNSIP"] != "192.0.2.53" || m["DomesticDNSIP"] != "192.0.2.54" || m["RemoteDns"] != nil || m["DomesticDns"] != nil {
		t.Errorf("dns fields: %v", m)
	}
	hosts := m["DnsHosts"].(map[string]any)
	if hosts["dns.example.com"] != "192.0.2.53" || hosts["own.example.com"] != "192.0.2.9" {
		t.Errorf("hosts: %v", hosts)
	}
	if got.DirectSites[0] != "domain:example.ru" || got.BlockSites[0] != "geosite:ads" {
		t.Error("the rules are the owner's")
	}
	if p.Doc.RemoteDns != "192.0.2.1" || p.Doc.DnsHosts["dns.example.com"] != "192.0.2.99" {
		t.Error("WithDNS changed the profile it was called on")
	}
	if same := p.Doc.WithDNS(RoutingDNS{}); same.Canonical() != p.Doc.Canonical() {
		t.Error("no preset: the profile must stay as it is")
	}
}

func TestRoutingLink(t *testing.T) {
	p, _ := ParseRoutingInput(familyProfile)
	for _, c := range []struct {
		scheme  string
		onlyAdd bool
		prefix  string
	}{{"happ", false, "happ://routing/onadd/"}, {"incy", true, "incy://routing/add/"}} {
		l := p.Doc.Link(c.scheme, c.onlyAdd)
		if !strings.HasPrefix(l, c.prefix) {
			t.Fatalf("link %q", l)
		}
		back, err := ParseRoutingInput(l)
		if err != nil || back.Doc.Canonical() != p.Doc.Canonical() {
			t.Errorf("link does not read back: %v", err)
		}
		if strings.ContainsFunc(l, func(r rune) bool { return r > 0x7e || r < 0x20 }) {
			t.Errorf("link is not printable ASCII: %q", l)
		}
	}
}

func rp(profile string, groups ...string) *adminv1.RoutingProfile {
	return &adminv1.RoutingProfile{Profile: profile, OnlyGroups: len(groups) > 0, GroupIds: groups}
}

func TestValidateRoutingProfiles(t *testing.T) {
	a, b := `{"Name":"a","DirectSites":["domain:a.example"]}`, `{"Name":"B","DirectSites":["domain:b.example"]}`
	check := func(l ...*adminv1.RoutingProfile) error {
		return validateRoutingProfiles(&adminv1.SubscriptionSettings{RoutingProfiles: l})
	}
	if err := check(); err != nil {
		t.Error(err)
	}
	if err := check(rp(a, "grp_1"), rp(b)); err != nil {
		t.Error(err)
	}
	for name, l := range map[string][]*adminv1.RoutingProfile{
		"same name":       {rp(a, "g1"), rp(strings.ToUpper(a), "g2")},
		"group twice":     {rp(a, "g1"), rp(b, "g1")},
		"two for all":     {rp(a), rp(b)},
		"groups w/o flag": {{Profile: a, GroupIds: []string{"g1"}}},
		"bad group id":    {rp(a, "g 1")},
		"repeated id":     {rp(a, "g1", "g1")},
		"garbage":         {rp("hello")},
		"nil":             {nil},
	} {
		if err := check(l...); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var many []*adminv1.RoutingProfile
	for i := 0; i <= MaxRoutingProfiles; i++ {
		many = append(many, rp(`{"Name":"p`+strings.Repeat("x", i)+`","DirectSites":[]}`, "g"+strings.Repeat("a", i)))
	}
	if err := check(many...); err == nil || !strings.Contains(err.Error(), "at most 20") {
		t.Errorf("21 profiles: %v", err)
	}
}

func TestNormalizeRoutingStoresTheCanonicalJSON(t *testing.T) {
	s := &adminv1.SubscriptionSettings{RoutingProfiles: []*adminv1.RoutingProfile{
		{Profile: "happ://routing/onadd/" + b64(`{"Name":" x ","Evil":1,"DirectSites":[" domain:a.example "]}`), Info: &adminv1.RoutingInfo{Name: "forged"}},
	}}
	normalizeRoutingProfiles(s.RoutingProfiles)
	e := s.RoutingProfiles[0]
	if e.Info != nil || e.Profile != `{"Name":"x","DirectSites":["domain:a.example"],"DirectIp":[],"ProxySites":[],"ProxyIp":[],"BlockSites":[],"BlockIp":[]}` {
		t.Errorf("entry = %+v", e)
	}
}

func TestRoutingProfileForTheMainGroupDecides(t *testing.T) {
	s := &adminv1.SubscriptionSettings{RoutingProfiles: []*adminv1.RoutingProfile{
		rp(`{"Name":"all","DirectSites":[]}`), rp(`{"Name":"family","DirectSites":[]}`, "g_family"), rp(`{"Name":"home","DirectSites":[]}`, "g_home"),
	}}
	name := func(group string) string {
		if e := RoutingProfileFor(s, group); e != nil {
			d, _ := RoutingDocOf(e)
			return d.Name
		}
		return ""
	}
	// the person's extra groups are never asked: only the main group is passed in
	for group, want := range map[string]string{"g_family": "family", "g_home": "home", "g_other": "all", "": "all"} {
		if got := name(group); got != want {
			t.Errorf("group %q: %q, want %q", group, got, want)
		}
	}
	s.RoutingProfiles = s.RoutingProfiles[1:]
	if name("g_other") != "" || name("g_family") != "family" {
		t.Error("without a profile for all groups, an unnamed group gets none")
	}
	if RoutingProfileFor(&adminv1.SubscriptionSettings{}, "g") != nil {
		t.Error("no profiles")
	}
}

func TestRoutingInfo(t *testing.T) {
	e, err := RoutingProfileOf("incy://routing/add/" + b64(familyProfile))
	if err != nil {
		t.Fatal(err)
	}
	i := e.Info
	if !e.OnlyAdd || i.Name != "family" || i.DirectSites != 2 || i.DirectIp != 2 || i.ProxySites != 1 || i.BlockSites != 1 || i.Hosts != 1 ||
		i.RemoteDnsType != "DoH" || i.RemoteDns != "https://dns.example.com/dns-query" || i.DomesticDns != "192.0.2.54" || i.GeoipAddress != "https://example.com/geoip.dat" || !i.UseChunkFiles || i.ProfileBytes == 0 {
		t.Errorf("info = %+v", i)
	}
}

func TestStoredRoutingProfilesThatNoLongerPassAreNotServed(t *testing.T) {
	good := `{"Name":"a","DirectSites":["domain:a.example"]}`
	enc := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	s, err := Parse(`{"user_page":{},"routing_profiles":[{"profile":"hello"},{"profile":` + enc(good) + `,"only_groups":true,"group_ids":["g1"]},{"profile":` + enc(good) + `}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.RoutingProfiles) != 1 || s.RoutingProfiles[0].GroupIds[0] != "g1" {
		t.Errorf("kept %v", s.RoutingProfiles)
	}
}
