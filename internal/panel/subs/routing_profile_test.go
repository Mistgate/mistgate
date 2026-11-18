package subs_test

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The owner's own routing profiles for Happ and INCY (SubscriptionSettings.routing_profiles): what each app gets, by which group,
// and that nothing but the profile the panel rewrote reaches the answer.

const incyUA = "INCY/1.4.0/ios"

const ownProfile = `{"Name":"family","GlobalProxy":"true","RemoteDNSType":"DoU","RemoteDNSIP":"192.0.2.1","DomesticDNSType":"DoU","DomesticDNSIP":"192.0.2.2",` +
	`"DirectSites":["domain:example.ru","geosite:category-ru"],"DirectIp":["geoip:ru"],"ProxySites":["domain:example.org"],"BlockSites":["geosite:category-ads"],` +
	`"DomainStrategy":"IPIfNonMatch"}`

func routingEntry(profile string, onlyAdd bool, groups ...string) *adminv1.RoutingProfile {
	return &adminv1.RoutingProfile{Profile: profile, OnlyAdd: onlyAdd, OnlyGroups: len(groups) > 0, GroupIds: groups}
}

// routingLink splits the routing header into the app scheme, the verb and the profile.
func routingLink(t *testing.T, link string) (scheme, verb string, prof map[string]any) {
	t.Helper()
	scheme, rest, ok := strings.Cut(link, "://routing/")
	verb, payload, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 {
		t.Fatalf("not a routing link: %q", link)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(raw, &prof) != nil {
		t.Fatalf("the payload of %q is not base64 JSON: %v", link, err)
	}
	return scheme, verb, prof
}

func strList(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func saveRouting(t *testing.T, m *m3rig, cache *subsettings.Cache, list ...*adminv1.RoutingProfile) {
	t.Helper()
	set := subsettings.Defaults()
	set.RoutingProfiles = list
	if _, err := cache.Update(m.ctx, set); err != nil {
		t.Fatal(err)
	}
}

func TestOwnRoutingProfileGoesToHappAndIncyWithThePersonsDNS(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1 })
	uid, tok := m.newUser("alice", nil) // group g2
	_, other := m.user("bob", nil)      // the other group
	saveRouting(t, m, cache, routingEntry(ownProfile, false, m.group2))

	pre, err := dns.New(m.st).LinkEffective(m.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	want, err := dns.ProfileDNSOf(pre)
	if err != nil {
		t.Fatal(err)
	}
	for ua, scheme := range map[string]string{happUA: "happ", incyUA: "incy"} {
		rec := fetch(h, "/"+tok, ua)
		got, verb, prof := routingLink(t, rec.Header().Get("Routing"))
		if got != scheme || verb != "onadd" {
			t.Errorf("%s: scheme %q verb %q", ua, got, verb)
		}
		// the rules are the owner's, the DNS fields are the person's preset's
		if prof["Name"] != "family" || strings.Join(strList(prof["DirectSites"]), ",") != "domain:example.ru,geosite:category-ru" ||
			strings.Join(strList(prof["BlockSites"]), ",") != "geosite:category-ads" || strings.Join(strList(prof["ProxySites"]), ",") != "domain:example.org" ||
			prof["DomainStrategy"] != "IPIfNonMatch" {
			t.Errorf("%s: rules %v", ua, prof)
		}
		if prof["RemoteDNSType"] != want.RemoteType || prof["RemoteDNSIP"] != want.RemoteIP || prof["DomesticDNSType"] != want.DomesticType || prof["DomesticDNSIP"] != want.DomesticIP {
			t.Errorf("%s: dns fields %v, the preset gives %+v", ua, prof, want)
		}
		if prof["RemoteDNSIP"] == "192.0.2.1" {
			t.Errorf("%s: the profile's own DNS was kept over the person's", ua)
		}
	}
	// a person of the other group: this profile is not theirs, so Happ gets what it got before (the DNS profile), INCY the same.
	for ua, scheme := range map[string]string{happUA: "happ", incyUA: "incy"} {
		got, _, prof := routingLink(t, fetch(h, "/"+other, ua).Header().Get("Routing"))
		if got != scheme || prof["Name"] != dns.HappProfileName {
			t.Errorf("%s of the other group: %s %v", ua, got, prof["Name"])
		}
	}
}

func TestOwnRoutingProfileModeAndOtherClients(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1 })
	_, tok := m.newUser("alice", nil)
	saveRouting(t, m, cache, routingEntry(ownProfile, true)) // for every group, add only
	if _, verb, _ := routingLink(t, fetch(h, "/"+tok, happUA).Header().Get("Routing")); verb != "add" {
		t.Errorf("verb %q, want add", verb)
	}
	before := map[string]string{}
	for name, c := range map[string]struct{ path, ua string }{
		"mihomo": {"/" + tok, mihomoUA}, "xray": {"/" + tok + "?format=xray-json", v2rayNGUA}, "singbox": {"/" + tok, sfaUA}, "curl": {"/" + tok, curlUA},
	} {
		rec := fetch(h, c.path, c.ua)
		if rec.Header().Get("Routing") != "" {
			t.Errorf("%s got a routing header", name)
		}
		before[name] = rec.Body.String()
	}
	saveRouting(t, m, cache) // none: the other apps' answers are the same bytes with or without a profile
	for name, c := range map[string]struct{ path, ua string }{
		"mihomo": {"/" + tok, mihomoUA}, "xray": {"/" + tok + "?format=xray-json", v2rayNGUA}, "singbox": {"/" + tok, sfaUA}, "curl": {"/" + tok, curlUA},
	} {
		if got := fetch(h, c.path, c.ua).Body.String(); got != before[name] {
			t.Errorf("%s: the answer changed with a routing profile saved", name)
		}
	}
}

// Without a profile of the owner Happ gets exactly the header it got before, byte for byte; INCY gets the same profile under its scheme.
func TestRoutingWithoutAnOwnerProfile(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1 })
	uid, tok := m.newUser("alice", nil)
	pre, err := dns.New(m.st).LinkEffective(m.ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	wantHapp, err := dns.HappRouting(pre)
	if err != nil {
		t.Fatal(err)
	}
	for _, list := range [][]*adminv1.RoutingProfile{nil, {routingEntry(ownProfile, false, "grp_nobody")}} {
		saveRouting(t, m, cache, list...)
		if got := fetch(h, "/"+tok, happUA).Header().Get("Routing"); got != wantHapp {
			t.Errorf("Happ:\n got %s\nwant %s", got, wantHapp)
		}
		if got, want := fetch(h, "/"+tok, incyUA).Header().Get("Routing"), "incy"+strings.TrimPrefix(wantHapp, "happ"); got != want {
			t.Errorf("INCY:\n got %s\nwant %s", got, want)
		}
	}
	// no DNS module wired: Happ keeps what the Routing says, INCY has no profile to give and gets none
	rt := &fakeRouting{}
	h2, _ := m.rig.handler(func(c *subs.Config) { c.Routing = rt; c.MinInterval = -1; c.MaxPerHour = -1 })
	if fetch(h2, "/"+tok, happUA).Header().Get("Routing") == "" || fetch(h2, "/"+tok, incyUA).Header().Get("Routing") != "" {
		t.Error("a Routing that is no DNS module: Happ gets its value, INCY none")
	}
}

func TestRoutingProfileTooLongForTheHeaderIsALineOfTheBody(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1 })
	_, tok := m.newUser("alice", nil)
	var sites []string
	for i := 0; i < 160; i++ {
		sites = append(sites, "domain:site"+strings.Repeat("x", 8)+string(rune('a'+i%26))+string(rune('a'+i/26))+".example.com")
	}
	big, _ := json.Marshal(map[string]any{"Name": "big", "DirectSites": sites})
	small := ownProfile
	saveRouting(t, m, cache, routingEntry(string(big), false, m.group2))

	plain := decode(t, fetch(h, "/"+tok, curlUA).Body.String())
	for ua, scheme := range map[string]string{happUA: "happ", incyUA: "incy"} {
		rec := fetch(h, "/"+tok, ua)
		if rec.Header().Get("Routing") != "" {
			t.Errorf("%s: the long link went in the header (%d bytes)", ua, len(rec.Header().Get("Routing")))
		}
		lines := strings.Split(decode(t, rec.Body.String()), "\n")
		last := lines[len(lines)-1]
		if len(last) <= subsettings.MaxRoutingHeaderValue || !strings.HasPrefix(last, scheme+"://routing/onadd/") {
			t.Fatalf("%s: last line %.60q (%d bytes)", ua, last, len(last))
		}
		_, _, prof := routingLink(t, last)
		if len(strList(prof["DirectSites"])) != 160 {
			t.Errorf("%s: the line lost rules", ua)
		}
		// the servers are the ones every other app gets (Happ's names carry a load figure, so compare the count and the schemes)
		if strings.Join(lines[:len(lines)-1], "\n") == "" || len(lines)-1 != len(strings.Split(plain, "\n")) {
			t.Errorf("%s: the servers changed: %d lines, others get %d", ua, len(lines)-1, len(strings.Split(plain, "\n")))
		}
	}
	if strings.Contains(decode(t, fetch(h, "/"+tok, curlUA).Body.String()), "routing/") {
		t.Error("another app got the line")
	}
	// a short profile stays in the header and the body has no line
	saveRouting(t, m, cache, routingEntry(small, false, m.group2))
	rec := fetch(h, "/"+tok, happUA)
	if rec.Header().Get("Routing") == "" || strings.Contains(decode(t, rec.Body.String()), "routing/") {
		t.Error("a short profile must go in the header only")
	}
}

// A person in several groups: the main group decides, the extra groups name nothing.
func TestRoutingProfileOfTheMainGroup(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1 })
	aliceID, alice := m.newUser("alice", nil) // main group g2
	bobID, bob := m.user("bob", nil)          // main group g
	profile := func(name string) string {
		return `{"Name":"` + name + `","DirectSites":["domain:` + name + `.example"]}`
	}
	saveRouting(t, m, cache, routingEntry(profile("ing2"), false, m.group2), routingEntry(profile("ing"), false, m.group))
	for _, c := range []struct{ id, main, extra string }{{aliceID, m.group2, m.group}, {bobID, m.group, m.group2}} {
		if _, err := m.svc.UpdateUser(m.ctx, connect.NewRequest(&adminv1.UpdateUserRequest{UserId: c.id, AddGroupIds: []string{c.extra}})); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ token, want string }{{alice, "ing2"}, {bob, "ing"}} {
		_, _, prof := routingLink(t, fetch(h, "/"+c.token, happUA).Header().Get("Routing"))
		if prof["Name"] != c.want {
			t.Errorf("a person whose main group is %q got %v", c.want, prof["Name"])
		}
	}
	// the extra group has no profile of its own and none is for all groups: nothing from it, the person gets the default DNS profile
	saveRouting(t, m, cache, routingEntry(profile("ing2"), false, m.group2))
	_, _, prof := routingLink(t, fetch(h, "/"+bob, happUA).Header().Get("Routing"))
	if prof["Name"] != dns.HappProfileName {
		t.Errorf("bob's extra group g2 has a profile, which must not apply: %v", prof["Name"])
	}
}

// Only what the panel wrote goes out: a field it does not know is gone, and the header is the prefix with base64 whatever the profile says.
func TestRoutingProfileNothingButTheRewrittenJSONReachesTheAnswer(t *testing.T) {
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1 })
	_, tok := m.newUser("alice", nil)
	// a line break in a value is refused at the save
	set := subsettings.Defaults()
	set.RoutingProfiles = []*adminv1.RoutingProfile{routingEntry(`{"Name":"x
X-Injected: 1","DirectSites":["domain:example.ru"]}`, false)}
	if _, err := cache.Update(m.ctx, set); err == nil {
		t.Fatal("a name with a line break was saved")
	}
	// a field the panel does not know is gone, whatever it holds
	saveRouting(t, m, cache, routingEntry("incy://routing/onadd/"+base64.StdEncoding.EncodeToString([]byte(
		`{"Name":"x","DirectSites":["domain:example.ru"],"X-Injected":"1","Evil":{"a":["<script>"]}}`)), false))
	for _, ua := range []string{happUA, incyUA} {
		rec := fetch(h, "/"+tok, ua)
		if rec.Header().Get("Set-Cookie") != "" || rec.Header().Get("X-Injected") != "" {
			t.Fatalf("a header was injected: %v", rec.Header())
		}
		val := rec.Header().Get("Routing")
		scheme := map[string]string{happUA: "happ", incyUA: "incy"}[ua]
		if !strings.HasPrefix(val, scheme+"://routing/onadd/") || strings.Trim(strings.TrimPrefix(val, scheme+"://routing/onadd/"), "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=") != "" {
			t.Errorf("the header is the prefix and base64 only: %q", val)
		}
		_, _, prof := routingLink(t, val)
		if _, ok := prof["Evil"]; ok || prof["X-Injected"] != nil || prof["Name"] != "x" {
			t.Errorf("profile %v", prof)
		}
	}
}

func TestPreviewShowsTheRoutingHeader(t *testing.T) {
	m := newM3Rig(t)
	alice, _ := m.newUser("alice", nil)
	var sites []string
	for i := 0; i < 160; i++ {
		sites = append(sites, "domain:s"+strings.Repeat("y", 10)+string(rune('a'+i%26))+string(rune('a'+i/26))+".example.com")
	}
	big, _ := json.Marshal(map[string]any{"Name": "big", "DirectSites": sites})
	preview := func(profile *adminv1.RoutingProfile, ua string) *adminv1.PreviewResponseHeadersResponse {
		t.Helper()
		saveRouting(t, m, subsettings.NewCache(m.st, nil), profile)
		svc := headerService(m)
		svc.RoutingWith(subs.HappRouting(dns.New(m.st)))
		r, err := svc.PreviewResponseHeaders(m.ctx, connect.NewRequest(&adminv1.PreviewResponseHeadersRequest{UserId: alice, UserAgent: ua}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	r := preview(routingEntry(ownProfile, true, m.group2), incyUA)
	if len(r.Headers) != 1 || r.Headers[0].Name != "routing" || !strings.HasPrefix(r.Headers[0].Value, "incy://routing/add/") || r.RoutingInBody {
		t.Errorf("incy: %+v", r)
	}
	if r = preview(routingEntry(ownProfile, false, m.group2), happUA); len(r.Headers) != 1 || !strings.HasPrefix(r.Headers[0].Value, "happ://routing/onadd/") {
		t.Errorf("happ: %+v", r)
	}
	if r = preview(routingEntry(string(big), false, m.group2), happUA); len(r.Headers) != 0 || !r.RoutingInBody {
		t.Errorf("a long profile is a line of the body: %+v", r)
	}
	if r = preview(routingEntry(ownProfile, false, m.group2), curlUA); len(r.Headers) != 0 || r.RoutingInBody {
		t.Errorf("curl: %+v", r)
	}
	if r = preview(routingEntry(ownProfile, false, m.group2), mihomoUA); len(r.Headers) != 0 {
		t.Errorf("mihomo: %+v", r)
	}
}

func TestPreviewRoutingProfile(t *testing.T) {
	m := newM3Rig(t)
	svc := headerService(m)
	ask := func(in string) (*adminv1.RoutingProfile, error) {
		r, err := svc.PreviewRoutingProfile(m.ctx, connect.NewRequest(&adminv1.PreviewRoutingProfileRequest{Input: in}))
		if err != nil {
			return nil, err
		}
		return r.Msg.Profile, nil
	}
	e, err := ask("happ://routing/add/" + base64.StdEncoding.EncodeToString([]byte(`{"Name":"a","DirectSites":["domain:example.ru"],"Odd":1}`)))
	if err != nil || !e.OnlyAdd || e.Info.Name != "a" || e.Info.DirectSites != 1 || len(e.Info.DroppedFields) != 1 || strings.Contains(e.Profile, "Odd") {
		t.Fatalf("%+v, %v", e, err)
	}
	for _, bad := range []string{"", "nonsense!", `{"foo":1}`, "happ://routing/off"} {
		if _, err := ask(bad); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

// A device the limit turns away gets the note and the DNS profile it always got, never the owner's rules.
func TestOwnRoutingProfileIsNotSentOnARefusal(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, cache := r.handler(func(c *subs.Config) {
		c.Routing = subs.HappRouting(dns.New(r.st))
		c.MinInterval, c.MaxPerHour, c.AfterResponse = -1, -1, func(f func()) { f() }
	})
	_, tok := r.user("alice", func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 })
	r.setLinkMode(store.LinkModeEnforce)
	set := subsettings.Defaults()
	set.RoutingProfiles = []*adminv1.RoutingProfile{routingEntry(ownProfile, false)}
	set.LinkDeviceLimit = adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_ENFORCE.Enum()
	if _, err := cache.Update(r.ctx, set); err != nil {
		t.Fatal(err)
	}
	_, _, prof := routingLink(t, fetch(h, "/"+tok, happUA, "X-HWID", hwidA).Header().Get("Routing"))
	if prof["Name"] != "family" {
		t.Fatalf("the first device gets the owner's profile: %v", prof["Name"])
	}
	rec := fetch(h, "/"+tok, happUA, "X-HWID", hwidB, "X-Device-OS", "Android")
	if rec.Header().Get("X-Hwid-Max-Devices-Reached") != "true" || rec.Header().Get("Routing") != "" {
		t.Errorf("a refused device: max-devices %q, routing %q", rec.Header().Get("X-Hwid-Max-Devices-Reached"), rec.Header().Get("Routing"))
	}
}

// The routing profiles of the settings through the admin API: the save stores the JSON the panel wrote (never the pasted text), the
// answer and the read carry the info, a group that was deleted since drops out, and the audit row counts the profiles.
func TestRoutingProfilesThroughTheSettingsAPI(t *testing.T) {
	m := newM3Rig(t)
	svc := headerService(m)
	ctx := auth.WithAdmin(m.ctx, store.Admin{ID: "adm_routing", Role: store.RoleOwner})
	temp := must(m.svc.CreateGroup(m.ctx, connect.NewRequest(&adminv1.CreateGroupRequest{Name: "temp", ProfileIds: []string{m.profile}}))).Msg.Group.Id
	save := func(list ...*adminv1.RoutingProfile) (*adminv1.SubscriptionSettings, error) {
		set := subsettings.Defaults()
		set.RoutingProfiles = list
		r, err := svc.UpdateSubscriptionSettings(ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: set}))
		if err != nil {
			return nil, err
		}
		return r.Msg.Settings, nil
	}
	invalid := func(what string, list ...*adminv1.RoutingProfile) {
		t.Helper()
		if _, err := save(list...); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("%s: %v", what, err)
		}
	}
	invalid("a group that never existed", routingEntry(ownProfile, false, "grp_nosuchgroup"))
	invalid("text that is no profile", routingEntry("hello", false))
	invalid("a geo file over http", routingEntry(`{"Name":"x","Geoipurl":"http://geo.example.com/g.dat"}`, false))

	// the owner saves them, the pasted link is stored as the profile it holds, the info is in the answer
	pasted := "happ://routing/add/" + base64.StdEncoding.EncodeToString([]byte(`{"Name":" family ","Evil":1,"DirectSites":["domain:example.ru"]}`))
	got, err := save(routingEntry(pasted, false, temp, m.group2), routingEntry(`{"Name":"rest","DirectSites":[]}`, true, m.group))
	if err != nil || len(got.RoutingProfiles) != 2 {
		t.Fatalf("two profiles: %v, %v", got, err)
	}
	first := got.RoutingProfiles[0]
	if strings.Contains(first.Profile, "Evil") || strings.Contains(first.Profile, "happ://") || !strings.HasPrefix(first.Profile, `{"Name":"family"`) || first.Info == nil || first.Info.DirectSites != 1 || len(first.GroupIds) != 2 {
		t.Fatalf("the entry as stored: %+v", first)
	}
	read := must(svc.GetSubscriptionSettings(ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))).Msg.Settings
	if len(read.RoutingProfiles) != 2 || read.RoutingProfiles[0].Profile != first.Profile || read.RoutingProfiles[0].Info.Name != "family" || read.RoutingProfiles[1].Info.Name != "rest" {
		t.Fatalf("read back: %v", read.RoutingProfiles)
	}

	// the group of the profile is deleted; another tab of the admin still sends the old list: the group drops out, the save passes
	must(m.svc.DeleteGroup(m.ctx, connect.NewRequest(&adminv1.DeleteGroupRequest{GroupId: temp})))
	got, err = save(routingEntry(first.Profile, false, temp, m.group2), routingEntry(`{"Name":"rest","DirectSites":[]}`, true, m.group))
	if err != nil || len(got.RoutingProfiles[0].GroupIds) != 1 || got.RoutingProfiles[0].GroupIds[0] != m.group2 {
		t.Fatalf("a deleted group: %v, %v", got, err)
	}
	invalid("a group that never existed, still", routingEntry(ownProfile, false, "grp_nosuchgroup"))

	for _, row := range must(m.st.ListAudit(m.ctx, "", 0, 10)) {
		if row.Action != "subscription_settings_update" {
			continue
		}
		var params map[string]any
		if err := json.Unmarshal([]byte(row.Params), &params); err != nil {
			t.Fatal(err)
		}
		if params["routing_profiles"] != float64(2) || len(row.Params) > 600 || strings.Contains(row.Params, "example.ru") {
			t.Fatalf("audit: %v", row.Params)
		}
		return
	}
	t.Fatal("no audit row")
}
