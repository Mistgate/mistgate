package dns

import "fmt"

// The provider catalog: well-known public resolvers with their plain, DoT and DoH
// endpoints. It ships with the panel and changes with releases; a preset server may point at a variant
// ("cloudflare/family") instead of carrying bare addresses, so a corrected address reaches every preset on the
// next upgrade.
//
// CatalogVersion is the date every endpoint below was last checked against the provider's own documentation.
const CatalogVersion = "2026-09-30"

// Category tells what a variant is for. The admin UI groups the catalog and filters the presets by it.
type Category string

const (
	CategoryRussia   Category = "russia"   // built for resolving Russian sites from inside Russia
	CategoryRegular  Category = "regular"  // no filtering
	CategoryNoAds    Category = "no_ads"   // blocks ads and trackers
	CategoryFamily   Category = "family"   // blocks adult content (and malware)
	CategorySecurity Category = "security" // blocks malware and phishing
)

// Variant is one way to use a provider. The IP addresses answer plain DNS (primary first) and, being anycast
// addresses of the same service, also serve as the bootstrap addresses of the DoT and DoH host names.
type Variant struct {
	ID             string // "provider/variant"
	NameRU, NameEN string
	Category       Category
	IPv4, IPv6     []string
	NoPlain        bool   // the addresses do not answer plain DNS (they only bootstrap DoT/DoH)
	DoTHost        string // empty: no DoT endpoint
	DoTPort        int
	DoHURL         string // empty: no DoH endpoint
	NoteRU, NoteEN string
}

// Provider groups the variants of one operator.
type Provider struct {
	ID, Name string
	Variants []Variant
}

// Sources, all read 2026-09-30 (VERIFIED = on the provider's page, endpoint behaviour confirmed where said):
//
//	Cloudflare   https://developers.cloudflare.com/1.1.1.1/ip-addresses/  (plain, v4 and v6, all three variants)
//	             https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-tls/  (one.one.one.one:853)
//	             https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-https/make-api-requests/  (cloudflare-dns.com/dns-query)
//	             https://developers.cloudflare.com/1.1.1.1/setup/  (security./family. DoH URLs and DoT host names)
//	Google       https://developers.google.com/speed/public-dns/docs/using, .../dns-over-tls (dns.google:853), .../doh
//	Quad9        https://docs.quad9.net/services/  (9.9.9.9 "Secure": plain, DoT dns.quad9.net:853, DoH dns.quad9.net/dns-query)
//	AdGuard      https://adguard-dns.io/en/public-dns.html  (default, non-filtering, family: plain, DoH, DoT)
//	Yandex       https://dns.yandex.ru/ (plain v4/v6 and the three modes), https://yandex.com/support/dns/en/settings-windows.md
//	             (DoH https://<mode>.dot.dns.yandex.net/dns-query; DoT is the same host name on 853)
//	Mullvad      https://mullvad.net/en/help/dns-over-https-and-dns-over-tls  (hosts, DoH, DoT; the port-53 resolver only
//	             bootstraps the service, so NoPlain). The page says the public encrypted DNS service is discontinued on
//	             2026-11-02: the notes say so; drop the provider in the release after that date.
//	OpenDNS      https://www.opendns.com/setupguide/ and the Cisco FamilyShield note (208.67.222.123 / 208.67.220.123). DoH
//	             https://familyshield.opendns.com/dns-query is not on an official page I could open; it answered a DoH query
//	             (HTTP 200, application/dns-message) from this machine on 2026-09-30. UNVERIFIED: a DoT host name (third-party
//	             sources disagree) and IPv6 addresses, so neither is listed.
//	DNS.SB       https://dns.sb/guide/, https://dns.sb/dot/ (dot.sb:853), https://dns.sb/doh/ (https://doh.dns.sb/dns-query)
//
// The Cloudflare DoH note below (blocked from Russia since 08.2026) is the owner's report, not something I could check.
var catalog = []Provider{
	{ID: "cloudflare", Name: "Cloudflare", Variants: []Variant{
		{ID: "cloudflare/standard", NameRU: "Обычный", NameEN: "Standard", Category: CategoryRegular,
			IPv4: []string{"1.1.1.1", "1.0.0.1"}, IPv6: []string{"2606:4700:4700::1111", "2606:4700:4700::1001"},
			DoTHost: "one.one.one.one", DoTPort: 853, DoHURL: "https://cloudflare-dns.com/dns-query",
			NoteRU: "Быстрый, без фильтров. DoH к Cloudflare из России блокируется с 08.2026 — внутри туннеля работает.",
			NoteEN: "Fast, no filtering. DoH to Cloudflare is blocked in Russia since 08.2026 — fine inside the tunnel."},
		{ID: "cloudflare/security", NameRU: "Без вредоносных", NameEN: "Security", Category: CategorySecurity,
			IPv4: []string{"1.1.1.2", "1.0.0.2"}, IPv6: []string{"2606:4700:4700::1112", "2606:4700:4700::1002"},
			DoTHost: "security.cloudflare-dns.com", DoTPort: 853, DoHURL: "https://security.cloudflare-dns.com/dns-query",
			NoteRU: "Блокирует вредоносные и фишинговые сайты. DoH из России блокируется с 08.2026.",
			NoteEN: "Blocks malware and phishing. DoH is blocked in Russia since 08.2026."},
		{ID: "cloudflare/family", NameRU: "Семейный", NameEN: "Family", Category: CategoryFamily,
			IPv4: []string{"1.1.1.3", "1.0.0.3"}, IPv6: []string{"2606:4700:4700::1113", "2606:4700:4700::1003"},
			DoTHost: "family.cloudflare-dns.com", DoTPort: 853, DoHURL: "https://family.cloudflare-dns.com/dns-query",
			NoteRU: "Блокирует вредоносные сайты и контент для взрослых. DoH из России блокируется с 08.2026.",
			NoteEN: "Blocks malware and adult content. DoH is blocked in Russia since 08.2026."},
	}},
	{ID: "google", Name: "Google", Variants: []Variant{
		{ID: "google/standard", NameRU: "Обычный", NameEN: "Standard", Category: CategoryRegular,
			IPv4: []string{"8.8.8.8", "8.8.4.4"}, IPv6: []string{"2001:4860:4860::8888", "2001:4860:4860::8844"},
			DoTHost: "dns.google", DoTPort: 853, DoHURL: "https://dns.google/dns-query",
			NoteRU: "Без фильтров.", NoteEN: "No filtering."},
	}},
	{ID: "quad9", Name: "Quad9", Variants: []Variant{
		{ID: "quad9/standard", NameRU: "Защита от вредоносных", NameEN: "Secure", Category: CategorySecurity,
			IPv4: []string{"9.9.9.9", "149.112.112.112"}, IPv6: []string{"2620:fe::fe", "2620:fe::9"},
			DoTHost: "dns.quad9.net", DoTPort: 853, DoHURL: "https://dns.quad9.net/dns-query",
			NoteRU: "Блокирует известные вредоносные и фишинговые домены.", NoteEN: "Blocks known malware and phishing domains."},
	}},
	{ID: "adguard", Name: "AdGuard", Variants: []Variant{
		{ID: "adguard/default", NameRU: "Без рекламы", NameEN: "Default", Category: CategoryNoAds,
			IPv4: []string{"94.140.14.14", "94.140.15.15"}, IPv6: []string{"2a10:50c0::ad1:ff", "2a10:50c0::ad2:ff"},
			DoTHost: "dns.adguard-dns.com", DoTPort: 853, DoHURL: "https://dns.adguard-dns.com/dns-query",
			NoteRU: "Блокирует рекламу и трекеры.", NoteEN: "Blocks ads and trackers."},
		{ID: "adguard/family", NameRU: "Семейный", NameEN: "Family", Category: CategoryFamily,
			IPv4: []string{"94.140.14.15", "94.140.15.16"}, IPv6: []string{"2a10:50c0::bad1:ff", "2a10:50c0::bad2:ff"},
			DoTHost: "family.adguard-dns.com", DoTPort: 853, DoHURL: "https://family.adguard-dns.com/dns-query",
			NoteRU: "Блокирует рекламу, трекеры и контент для взрослых.", NoteEN: "Blocks ads, trackers and adult content."},
		{ID: "adguard/unfiltered", NameRU: "Без фильтров", NameEN: "Unfiltered", Category: CategoryRegular,
			IPv4: []string{"94.140.14.140", "94.140.14.141"}, IPv6: []string{"2a10:50c0::1:ff", "2a10:50c0::2:ff"},
			DoTHost: "unfiltered.adguard-dns.com", DoTPort: 853, DoHURL: "https://unfiltered.adguard-dns.com/dns-query",
			NoteRU: "Ничего не блокирует.", NoteEN: "Blocks nothing."},
	}},
	{ID: "yandex", Name: "Яндекс", Variants: []Variant{
		{ID: "yandex/basic", NameRU: "Базовый", NameEN: "Basic", Category: CategoryRussia,
			IPv4: []string{"77.88.8.8", "77.88.8.1"}, IPv6: []string{"2a02:6b8::feed:ff", "2a02:6b8:0:1::feed:ff"},
			DoTHost: "common.dot.dns.yandex.net", DoTPort: 853, DoHURL: "https://common.dot.dns.yandex.net/dns-query",
			NoteRU: "Российские сайты открываются надёжнее. DoH и DoT из России работают.",
			NoteEN: "Russian sites resolve more reliably. DoH and DoT work from Russia."},
		{ID: "yandex/safe", NameRU: "Безопасный", NameEN: "Safe", Category: CategorySecurity,
			IPv4: []string{"77.88.8.88", "77.88.8.2"}, IPv6: []string{"2a02:6b8::feed:bad", "2a02:6b8:0:1::feed:bad"},
			DoTHost: "safe.dot.dns.yandex.net", DoTPort: 853, DoHURL: "https://safe.dot.dns.yandex.net/dns-query",
			NoteRU: "Блокирует вредоносные и мошеннические сайты. DoH и DoT из России работают.",
			NoteEN: "Blocks malware and fraud sites. DoH and DoT work from Russia."},
		{ID: "yandex/family", NameRU: "Семейный", NameEN: "Family", Category: CategoryFamily,
			IPv4: []string{"77.88.8.7", "77.88.8.3"}, IPv6: []string{"2a02:6b8::feed:a11", "2a02:6b8:0:1::feed:a11"},
			DoTHost: "family.dot.dns.yandex.net", DoTPort: 853, DoHURL: "https://family.dot.dns.yandex.net/dns-query",
			NoteRU: "Блокирует контент для взрослых и вредоносные сайты. DoH и DoT из России работают.",
			NoteEN: "Blocks adult content and malware. DoH and DoT work from Russia."},
	}},
	{ID: "mullvad", Name: "Mullvad", Variants: []Variant{
		{ID: "mullvad/standard", NameRU: "Обычный", NameEN: "Standard", Category: CategoryRegular, NoPlain: true,
			IPv4: []string{"194.242.2.2"}, IPv6: []string{"2a07:e340::2"},
			DoTHost: "dns.mullvad.net", DoTPort: 853, DoHURL: "https://dns.mullvad.net/dns-query",
			NoteRU: "Только DoH/DoT, обычного DNS нет. Сервис закрывается 02.11.2026.",
			NoteEN: "DoH/DoT only, no plain DNS. The service shuts down on 2026-11-02."},
		{ID: "mullvad/adblock", NameRU: "Без рекламы", NameEN: "Adblock", Category: CategoryNoAds, NoPlain: true,
			IPv4: []string{"194.242.2.3"}, IPv6: []string{"2a07:e340::3"},
			DoTHost: "adblock.dns.mullvad.net", DoTPort: 853, DoHURL: "https://adblock.dns.mullvad.net/dns-query",
			NoteRU: "Блокирует рекламу и трекеры. Только DoH/DoT. Сервис закрывается 02.11.2026.",
			NoteEN: "Blocks ads and trackers. DoH/DoT only. The service shuts down on 2026-11-02."},
		{ID: "mullvad/base", NameRU: "Реклама и вредоносные", NameEN: "Base", Category: CategoryNoAds, NoPlain: true,
			IPv4: []string{"194.242.2.4"}, IPv6: []string{"2a07:e340::4"},
			DoTHost: "base.dns.mullvad.net", DoTPort: 853, DoHURL: "https://base.dns.mullvad.net/dns-query",
			NoteRU: "Блокирует рекламу, трекеры и вредоносные сайты. Только DoH/DoT. Сервис закрывается 02.11.2026.",
			NoteEN: "Blocks ads, trackers and malware. DoH/DoT only. The service shuts down on 2026-11-02."},
	}},
	{ID: "opendns", Name: "OpenDNS", Variants: []Variant{
		{ID: "opendns/familyshield", NameRU: "FamilyShield", NameEN: "FamilyShield", Category: CategoryFamily,
			IPv4:   []string{"208.67.222.123", "208.67.220.123"},
			DoHURL: "https://familyshield.opendns.com/dns-query",
			NoteRU: "Блокирует контент для взрослых. DoT не указан: адрес не подтверждён.",
			NoteEN: "Blocks adult content. No DoT: the host name is not confirmed."},
	}},
	{ID: "dnssb", Name: "DNS.SB", Variants: []Variant{
		{ID: "dnssb/standard", NameRU: "Обычный", NameEN: "Standard", Category: CategoryRegular,
			IPv4: []string{"185.222.222.222", "45.11.45.11"}, IPv6: []string{"2a09::", "2a11::"},
			DoTHost: "dot.sb", DoTPort: 853, DoHURL: "https://doh.dns.sb/dns-query",
			NoteRU: "Без логов, DNSSEC, без фильтров.", NoteEN: "No logging, DNSSEC, no filtering."},
	}},
}

// variants indexes the catalog by variant id; parents maps a variant id to its provider.
var (
	variants = map[string]Variant{}
	parents  = map[string]Provider{}
)

func init() {
	for _, p := range catalog {
		for _, v := range p.Variants {
			variants[v.ID] = v
			parents[v.ID] = p
		}
	}
}

// Catalog returns the providers in display order. Callers must not modify it.
func Catalog() []Provider { return catalog }

// LookupVariant returns a catalog variant and its provider.
func LookupVariant(id string) (Variant, Provider, bool) {
	v, ok := variants[id]
	return v, parents[id], ok
}

// addresses returns the IPs of a variant for a client: IPv4 first, then IPv6 when the client takes it.
func (v Variant) addresses(ipv6 bool) []string {
	out := append([]string(nil), v.IPv4...)
	if ipv6 {
		out = append(out, v.IPv6...)
	}
	return out
}

// dotAddress is the DoT address of a variant ("host", or "host:port" off the standard port).
func (v Variant) dotAddress() string {
	if v.DoTPort == 0 || v.DoTPort == 853 {
		return v.DoTHost
	}
	return fmt.Sprintf("%s:%d", v.DoTHost, v.DoTPort)
}

// legacy is what a client that predates the catalog is shown for a variant: the primary plain address, else
// the DoH URL (Mullvad).
func (v Variant) legacy() (Kind, string) {
	if !v.NoPlain && len(v.IPv4) > 0 {
		return KindPlain, v.IPv4[0]
	}
	return KindDoH, v.DoHURL
}
