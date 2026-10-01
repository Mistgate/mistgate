package dns

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type env struct {
	t   *testing.T
	st  *store.Store
	s   *Service
	ctx context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &env{t: t, st: st, s: New(st), ctx: context.Background()}
}

func (e *env) sql(q string, args ...any) {
	e.t.Helper()
	if _, err := e.st.W.ExecContext(e.ctx, q, args...); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
}

func (e *env) group(id, preset string) {
	var p any
	if preset != "" {
		p = preset
	}
	e.sql(`INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES (?, ?, 1, ?)`, id, id, p)
}

func (e *env) user(id, group, preset string) {
	var p any
	if preset != "" {
		p = preset
	}
	e.sql(`INSERT INTO user (id, name, group_id, period_start, sub_token_hash, sub_token_enc, created_at, dns_preset_id)
	       VALUES (?, ?, ?, 1, ?, x'02', 1, ?)`, id, id, group, []byte(id), p)
}

func plain(a string) *adminv1.DnsServer {
	return &adminv1.DnsServer{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: a}
}

func (e *env) list() map[string]*adminv1.DnsPreset {
	e.t.Helper()
	r, err := e.s.ListDnsPresets(e.ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
	if err != nil {
		e.t.Fatal(err)
	}
	m := map[string]*adminv1.DnsPreset{}
	for _, p := range r.Msg.Presets {
		m[p.Id] = p
	}
	return m
}

func (e *env) create(name string, servers ...*adminv1.DnsServer) (*adminv1.DnsPreset, error) {
	r, err := e.s.CreateDnsPreset(e.ctx, connect.NewRequest(&adminv1.CreateDnsPresetRequest{Name: name, Servers: servers}))
	if err != nil {
		return nil, err
	}
	return r.Msg.Preset, nil
}

func wantCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("want %v, got success", code)
	}
	if got := connect.CodeOf(err); got != code {
		t.Fatalf("want %v, got %v (%v)", code, got, err)
	}
}

// wantMsg checks a refusal the admin UI words by its code (web/src/lib/errors.ts): code and message as Connect prints them.
func wantMsg(t *testing.T, err error, msg string) {
	t.Helper()
	if err == nil || err.Error() != msg {
		t.Fatalf("want %q, got %v", msg, err)
	}
}

func TestSeed(t *testing.T) {
	e := newEnv(t)
	r, err := e.s.ListDnsPresets(e.ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range r.Msg.Presets {
		ids = append(ids, p.Id)
		if !p.Builtin || len(p.Servers) == 0 || p.Name == "" || !strings.Contains(p.Description, "\n") {
			t.Errorf("preset %s: builtin=%v servers=%d name=%q description=%q", p.Id, p.Builtin, len(p.Servers), p.Name, p.Description)
		}
		if p.IsDefault != (p.Id == "dns_builtin_ru_split") {
			t.Errorf("preset %s is_default=%v", p.Id, p.IsDefault)
		}
		// every seeded server and suffix passes the validation that custom presets go through
		if _, _, _, _, err := Clean(p.Name, p.Description, serversFromProto(p.Servers), splitFromProto(p.Split)); err != nil {
			t.Errorf("preset %s does not validate: %v", p.Id, err)
		}
	}
	// grouped by category: Russia, regular, no ads, family, security
	want := []string{
		"dns_builtin_ru_split", "dns_builtin_ru_proxied", "dns_builtin_yandex",
		"dns_builtin_standard", "dns_builtin_cloudflare", "dns_builtin_google", "dns_builtin_dnssb",
		"dns_builtin_adblock",
		"dns_builtin_family", "dns_builtin_cloudflare_family", "dns_builtin_yandex_family", "dns_builtin_opendns_family",
		"dns_builtin_quad9", "dns_builtin_cloudflare_security", "dns_builtin_yandex_safe",
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	wantCat := map[string]adminv1.DnsCategory{
		"dns_builtin_ru_split": adminv1.DnsCategory_DNS_CATEGORY_RUSSIA, "dns_builtin_ru_proxied": adminv1.DnsCategory_DNS_CATEGORY_RUSSIA,
		"dns_builtin_yandex": adminv1.DnsCategory_DNS_CATEGORY_RUSSIA, "dns_builtin_standard": adminv1.DnsCategory_DNS_CATEGORY_REGULAR,
		"dns_builtin_cloudflare": adminv1.DnsCategory_DNS_CATEGORY_REGULAR, "dns_builtin_google": adminv1.DnsCategory_DNS_CATEGORY_REGULAR,
		"dns_builtin_dnssb": adminv1.DnsCategory_DNS_CATEGORY_REGULAR, "dns_builtin_adblock": adminv1.DnsCategory_DNS_CATEGORY_NO_ADS,
		"dns_builtin_family": adminv1.DnsCategory_DNS_CATEGORY_FAMILY, "dns_builtin_cloudflare_family": adminv1.DnsCategory_DNS_CATEGORY_FAMILY,
		"dns_builtin_yandex_family": adminv1.DnsCategory_DNS_CATEGORY_FAMILY, "dns_builtin_opendns_family": adminv1.DnsCategory_DNS_CATEGORY_FAMILY,
		"dns_builtin_quad9": adminv1.DnsCategory_DNS_CATEGORY_SECURITY, "dns_builtin_cloudflare_security": adminv1.DnsCategory_DNS_CATEGORY_SECURITY,
		"dns_builtin_yandex_safe": adminv1.DnsCategory_DNS_CATEGORY_SECURITY,
	}
	for _, p := range r.Msg.Presets {
		if p.Category != wantCat[p.Id] {
			t.Errorf("preset %s category = %v, want %v", p.Id, p.Category, wantCat[p.Id])
		}
		if p.PreferredTransport != adminv1.DnsTransport_DNS_TRANSPORT_PLAIN {
			t.Errorf("preset %s preferred transport = %v, want plain", p.Id, p.PreferredTransport)
		}
		for _, s := range p.Servers {
			if _, _, ok := LookupVariant(s.ProviderVariant); !ok {
				t.Errorf("preset %s: built-in server is not a catalog variant: %v", p.Id, s)
			}
		}
	}
	// the built-ins reference the catalog, and old clients still see a plain address in kind/address
	ru := r.Msg.Presets[0]
	if ru.Name != "Россия: .ru напрямую" || !ru.SplitDirect || len(ru.Split) != 1 || ru.Split[0].Servers[0].ProviderVariant != "yandex/basic" ||
		ru.Servers[0].ProviderVariant != "cloudflare/standard" || ru.Servers[1].ProviderVariant != "google/standard" ||
		ru.Servers[0].Kind != adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN || ru.Servers[0].Address != "1.1.1.1" || ru.Servers[1].Address != "8.8.8.8" || ru.Split[0].Servers[0].Address != "77.88.8.8" {
		t.Errorf("ru split = %v", ru)
	}
	for _, s := range []string{".ru", ".su", ".xn--p1ai"} {
		if !slices.Contains(ru.Split[0].Suffixes, s) {
			t.Errorf("ru split has no suffix %s: %v", s, ru.Split[0].Suffixes)
		}
	}
	// only the Russia split goes direct; the proxied Russia preset has no split at all
	for id, p := range e.list() {
		if p.SplitDirect != (id == "dns_builtin_ru_split") {
			t.Errorf("preset %s split_direct=%v", id, p.SplitDirect)
		}
	}
	if px := e.list()["dns_builtin_ru_proxied"]; px.Name != "Россия: всё через VPN" || len(px.Split) != 0 || px.Servers[0].Address != "1.1.1.1" || px.Servers[1].Address != "8.8.8.8" {
		t.Errorf("ru proxied = %v", px)
	}
	ad := e.list()["dns_builtin_adblock"]
	if len(ad.Servers) != 1 || ad.Servers[0].ProviderVariant != "adguard/default" || ad.Servers[0].Address != "94.140.14.14" {
		t.Errorf("adblock = %v", ad)
	}
}

func TestCRUD(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	p, err := e.create("  Мой  ", plain("9.9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Builtin || p.Name != "Мой" || !strings.HasPrefix(p.Id, "dns_") || p.UserCount != 0 || p.IsDefault {
		t.Fatalf("created = %v", p)
	}
	_, err = e.create("мой", plain("1.1.1.1")) // names are unique case-insensitively
	wantMsg(t, err, "already_exists: name_taken")
	_, err = e.create("Cloudflare + Google", plain("1.1.1.1")) // also against the built-ins
	wantCode(t, err, connect.CodeAlreadyExists)

	u, err := e.s.UpdateDnsPreset(ctx, connect.NewRequest(&adminv1.UpdateDnsPresetRequest{
		Id: p.Id, Name: "Мой 2", Description: "d", Ipv4Only: true, SplitDirect: true,
		Servers: []*adminv1.DnsServer{plain("1.0.0.1:5353")},
		Split:   []*adminv1.DnsSplitRule{{Suffixes: []string{".РФ", "Example.COM", "example.com"}, Servers: []*adminv1.DnsServer{plain("77.88.8.8")}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := u.Msg.Preset
	if got.Name != "Мой 2" || !got.Ipv4Only || !got.SplitDirect || got.Servers[0].Address != "1.0.0.1:5353" ||
		!slices.Equal(got.Split[0].Suffixes, []string{".xn--p1ai", "example.com"}) {
		t.Fatalf("updated = %v", got)
	}
	// the list order: built-ins first, then custom by name
	r, _ := e.s.ListDnsPresets(ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
	if n := len(r.Msg.Presets); n != 16 || r.Msg.Presets[15].Id != p.Id {
		t.Fatalf("list = %d presets", n)
	}
	_, err = e.s.UpdateDnsPreset(ctx, connect.NewRequest(&adminv1.UpdateDnsPresetRequest{Id: "dns_nope", Name: "x", Servers: []*adminv1.DnsServer{plain("1.1.1.1")}}))
	wantCode(t, err, connect.CodeNotFound)
	_, err = e.s.UpdateDnsPreset(ctx, connect.NewRequest(&adminv1.UpdateDnsPresetRequest{Id: p.Id, Name: "Яндекс DNS", Servers: []*adminv1.DnsServer{plain("1.1.1.1")}}))
	wantCode(t, err, connect.CodeAlreadyExists)

	// a built-in can be edited, keeps its id and flag
	b, err := e.s.UpdateDnsPreset(ctx, connect.NewRequest(&adminv1.UpdateDnsPresetRequest{
		Id: "dns_builtin_quad9", Name: "Quad9", Servers: []*adminv1.DnsServer{plain("9.9.9.10")}}))
	if err != nil || !b.Msg.Preset.Builtin || b.Msg.Preset.Servers[0].Address != "9.9.9.10" {
		t.Fatalf("edit built-in: %v %v", b, err)
	}

	del := func(id string) error {
		_, err := e.s.DeleteDnsPreset(ctx, connect.NewRequest(&adminv1.DeleteDnsPresetRequest{Id: id}))
		return err
	}
	wantMsg(t, del("dns_builtin_yandex"), "failed_precondition: preset_builtin")
	wantCode(t, del("dns_nope"), connect.CodeNotFound)
	// the instance default cannot be deleted
	if err := e.s.SetDefaultPresetID(ctx, p.Id); err != nil {
		t.Fatal(err)
	}
	wantMsg(t, del(p.Id), "failed_precondition: preset_is_default")
	if !e.list()[p.Id].IsDefault || e.list()["dns_builtin_ru_split"].IsDefault {
		t.Error("is_default did not follow the instance default")
	}
	if err := e.s.SetDefaultPresetID(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := del(p.Id); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.list()[p.Id]; ok {
		t.Error("preset still listed after delete")
	}
}

func TestValidation(t *testing.T) {
	ok := map[Server]string{
		{Kind: KindPlain, Address: "77.88.8.8"}:                                "77.88.8.8",
		{Kind: KindPlain, Address: " 77.88.8.8:53 "}:                           "77.88.8.8:53",
		{Kind: KindPlain, Address: "2606:4700:4700::1111"}:                     "2606:4700:4700::1111",
		{Kind: KindPlain, Address: "[2606:4700:4700::1111]:53"}:                "[2606:4700:4700::1111]:53",
		{Kind: KindDoH, Address: "https://dns.adguard-dns.com/dns-query"}:      "https://dns.adguard-dns.com/dns-query",
		{Kind: KindDoH, Address: "https://DNS.Example.com:8443/dns-query?x=1"}: "https://dns.example.com:8443/dns-query?x=1",
		{Kind: KindDoH, Address: "https://днс.рф/dns-query"}:                   "https://xn--d1asm.xn--p1ai/dns-query",
		{Kind: KindDoT, Address: "dns.adguard-dns.com"}:                        "dns.adguard-dns.com",
		{Kind: KindDoT, Address: "94.140.14.14:853"}:                           "94.140.14.14:853",
		{Kind: KindDoT, Address: "dns.example.com:853"}:                        "dns.example.com:853",
		{Kind: KindDoT, Address: "Днс.рф"}:                                     "xn--d1asm.xn--p1ai",
	}
	for in, want := range ok {
		got, err := NormalizeServer(in)
		if err != nil || got.Address != want || got.Kind != in.Kind {
			t.Errorf("%v: got %v, %v; want %q", in, got, err, want)
		}
	}
	bad := []Server{
		{Kind: KindPlain, Address: "dns.example.com"}, {Kind: KindPlain, Address: "1.1.1.1:0"}, {Kind: KindPlain, Address: "1.1.1.1:99999"}, {Kind: KindPlain, Address: "0.0.0.0"},
		{Kind: KindPlain, Address: "127.0.0.1"}, {Kind: KindPlain, Address: "https://1.1.1.1"}, {Kind: KindPlain, Address: ""}, {Kind: KindPlain, Address: "fe80::1%eth0"},
		{Kind: KindDoH, Address: "http://dns.example.com/dns-query"}, {Kind: KindDoH, Address: "dns.example.com"}, {Kind: KindDoH, Address: "https://"},
		{Kind: KindDoH, Address: "https://user:pw@dns.example.com/"}, {Kind: KindDoH, Address: "https://dns.example.com/#frag"}, {Kind: KindDoH, Address: "https://bad_host.example.com/"},
		{Kind: KindDoH, Address: "https://dns.example.com:0/"},
		{Kind: KindDoT, Address: "https://dns.example.com"}, {Kind: KindDoT, Address: "dns.example.com/path"}, {Kind: KindDoT, Address: "dns.example.com:0"}, {Kind: KindDoT, Address: ""},
		{Kind: KindDoT, Address: "-bad.example.com"}, {Kind: KindDoT, Address: "a..b"},
		{Kind: "", Address: "1.1.1.1"}, {Kind: "udp", Address: "1.1.1.1"},
	}
	for _, in := range bad {
		if got, err := NormalizeServer(in); err == nil {
			t.Errorf("%v accepted as %v", in, got)
		}
	}

	sfx := map[string]string{
		".ru": ".ru", "ru": "ru", "gosuslugi.ru": "gosuslugi.ru", ".рф": ".xn--p1ai", "*.Example.COM": ".example.com",
		" .SU ": ".su", "münchen.de": "xn--mnchen-3ya.de", "a-b.c1.example": "a-b.c1.example",
	}
	for in, want := range sfx {
		if got, err := NormalizeSuffix(in); err != nil || got != want {
			t.Errorf("suffix %q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", ".", "..ru", "a..ru", "-a.ru", "a-.ru", "exa mple.ru", "a_b.ru", "http://x.ru", "x.ru/", strings.Repeat("a", 64) + ".ru", strings.Repeat("a.", 130) + "ru"} {
		if got, err := NormalizeSuffix(in); err == nil {
			t.Errorf("suffix %q accepted as %q", in, got)
		}
	}

	good := []Server{{Kind: KindPlain, Address: "1.1.1.1"}}
	// limits: 16 servers, 64 split rules; duplicates collapse first
	many := func(n int) []Server {
		var s []Server
		for i := range n {
			s = append(s, Server{Kind: KindPlain, Address: fmt.Sprintf("10.0.0.%d", i+1)})
		}
		return s
	}
	if _, _, _, _, err := Clean("n", "", many(16), nil); err != nil {
		t.Errorf("16 servers: %v", err)
	}
	if _, _, _, _, err := Clean("n", "", many(17), nil); err == nil {
		t.Error("17 servers accepted")
	}
	if _, _, s, _, err := Clean("n", "", append(many(3), many(3)...), nil); err != nil || len(s) != 3 {
		t.Errorf("duplicates: %v %v", s, err)
	}
	rules := func(n int) []SplitRule {
		var r []SplitRule
		for i := range n {
			r = append(r, SplitRule{Suffixes: []string{fmt.Sprintf("d%d.example", i)}, Servers: good})
		}
		return r
	}
	if _, _, _, _, err := Clean("n", "", good, rules(64)); err != nil {
		t.Errorf("64 rules: %v", err)
	}
	if _, _, _, _, err := Clean("n", "", good, rules(65)); err == nil {
		t.Error("65 rules accepted")
	}
	if _, _, _, _, err := Clean("n", "", good, []SplitRule{{Suffixes: nil, Servers: good}}); err == nil {
		t.Error("rule without suffixes accepted")
	}
	if _, _, _, _, err := Clean("n", "", good, []SplitRule{{Suffixes: []string{".ru"}}}); err == nil {
		t.Error("rule without servers accepted")
	}
	if _, _, _, _, err := Clean("n", "", nil, nil); err == nil {
		t.Error("no servers accepted")
	}
	if _, _, _, _, err := Clean(" ", "", good, nil); err == nil {
		t.Error("empty name accepted")
	}
	if _, _, _, _, err := Clean(strings.Repeat("я", 65), "", good, nil); err == nil {
		t.Error("65-character name accepted")
	}
	if _, _, _, _, err := Clean(strings.Repeat("я", 64), "", good, nil); err != nil {
		t.Errorf("64-character name: %v", err)
	}

	// the same through the API: InvalidArgument
	e := newEnv(t)
	_, err := e.create("x", &adminv1.DnsServer{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_UNSPECIFIED, Address: "1.1.1.1"})
	wantCode(t, err, connect.CodeInvalidArgument)
	_, err = e.create("x", plain("not an ip"))
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestEffectiveInheritance(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx
	custom, err := e.create("Custom", plain("9.9.9.9"))
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.create("Other", plain("8.8.4.4"))
	if err != nil {
		t.Fatal(err)
	}
	e.group("g_plain", "")
	e.group("g_dns", custom.Id)
	e.user("u_default", "g_plain", "")            // nothing set anywhere
	e.user("u_group", "g_dns", "")                // inherits the group
	e.user("u_own", "g_dns", other.Id)            // overrides the group
	e.user("u_own_nogroup", "g_plain", custom.Id) // overrides nothing

	eff := func(user string) (string, Source) {
		t.Helper()
		p, src, err := e.s.Effective(ctx, user)
		if err != nil {
			t.Fatal(err)
		}
		return p.ID, src
	}
	check := func(user, id string, src Source) {
		t.Helper()
		if gid, gsrc := eff(user); gid != id || gsrc != src {
			t.Errorf("%s: effective = %s/%s, want %s/%s", user, gid, gsrc, id, src)
		}
	}
	check("u_default", "dns_builtin_ru_split", SourceDefault)
	check("u_group", custom.Id, SourceGroup)
	check("u_own", other.Id, SourceUser)
	check("u_own_nogroup", custom.Id, SourceUser)
	if _, _, err := e.s.Effective(ctx, "usr_missing"); err == nil {
		t.Error("unknown user resolved")
	}

	// user_count: direct, via group, via the default
	counts := func() map[string]uint32 {
		m := map[string]uint32{}
		for id, p := range e.list() {
			m[id] = p.UserCount
		}
		return m
	}
	c := counts()
	if c["dns_builtin_ru_split"] != 1 || c[custom.Id] != 2 || c[other.Id] != 1 {
		t.Errorf("counts = %v", c)
	}

	// a new instance default applies to users with nothing of their own
	if err := e.s.SetDefaultPresetID(ctx, "dns_builtin_standard"); err != nil {
		t.Fatal(err)
	}
	check("u_default", "dns_builtin_standard", SourceDefault)
	check("u_group", custom.Id, SourceGroup)
	if id, err := e.s.DefaultPresetID(ctx); err != nil || id != "dns_builtin_standard" {
		t.Errorf("DefaultPresetID = %q %v", id, err)
	}
	if err := e.s.SetDefaultPresetID(ctx, "dns_nope"); err != ErrUnknownPreset {
		t.Errorf("unknown default: %v", err)
	}

	// deleting a preset in use: users and groups fall back (user -> group -> default)
	if _, err := e.s.DeleteDnsPreset(ctx, connect.NewRequest(&adminv1.DeleteDnsPresetRequest{Id: other.Id})); err != nil {
		t.Fatal(err)
	}
	check("u_own", custom.Id, SourceGroup) // own preset gone: back to the group's
	if _, err := e.s.DeleteDnsPreset(ctx, connect.NewRequest(&adminv1.DeleteDnsPresetRequest{Id: custom.Id})); err != nil {
		t.Fatal(err)
	}
	check("u_group", "dns_builtin_standard", SourceDefault)
	check("u_own", "dns_builtin_standard", SourceDefault)
	check("u_own_nogroup", "dns_builtin_standard", SourceDefault)
	var n int
	e.st.R.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM user WHERE dns_preset_id IS NOT NULL) + (SELECT count(*) FROM user_group WHERE dns_preset_id IS NOT NULL)`).Scan(&n)
	if n != 0 {
		t.Errorf("%d dangling references left", n)
	}
	if c := counts(); c["dns_builtin_standard"] != 4 {
		t.Errorf("counts after delete = %v", c)
	}

	// the stored default pointing at a preset that vanished behind our back falls back to the built-in one
	e.sql(`UPDATE setting SET v = 'dns_gone' WHERE k = 'dns.default_preset'`)
	check("u_default", "dns_builtin_ru_split", SourceDefault)
	if id, _ := e.s.DefaultPresetID(ctx); id != "" {
		t.Errorf("DefaultPresetID of a stale setting = %q", id)
	}
}
