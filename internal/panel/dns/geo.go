package dns

import (
	"slices"
	"strings"
)

// Geo lists: a split rule may name whole countries ("cn", "ru") instead of, or next to, domain suffixes. The names of the lists
// and the places clients download them from are written here and nowhere else.
//
// What a client does with a country code, per format (docs/en/guide/dns.md):
//
//   - Mihomo: two rule providers (mrs format, downloaded through the tunnel group), a nameserver-policy entry for the
//     geosite one and two RULE-SET rules, the geoip one with no-resolve.
//   - Xray JSON: geosite:<name> in the split server's domains and in the direct domain rule, geoip:<name> in a direct ip
//     rule. The client needs geosite.dat and geoip.dat that hold the lists.
//   - sing-box: two remote binary rule sets, a DNS rule and a route rule. No download_detour (deprecated in 1.14): the
//     implicit client downloads through the default outbound, which is the selector.
//   - Happ: geosite:<name> in DirectSites and geoip:<name> in DirectIp; the app ships the geo files.
//   - AmneziaWG and the base64 list have no routing: a geo rule changes nothing there.
//
// A rule may also name one geosite category alone, "geosite:<name>" (geosite:telegram, geosite:category-media-ru): the site half of the
// lists only, no address list, so no geoip rule is written in any format. The name is checked by its form, not against a list of
// names: the panel carries none, and a name the lists do not hold is shown by the app that tries to load it.
//
// The geoip lists decide only connections made to an address (Mihomo no-resolve, Xray domainStrategy AsIs, sing-box
// without a resolve action): a domain is never looked up just to be checked against them.
type geoList struct {
	Site string // geosite list name (v2fly/domain-list-community data file)
	IP   string // geoip list name (country code); empty for a category alone
}

// geoLists maps a code of a split rule to its lists.
var geoLists = map[string]geoList{
	"cn": {Site: "cn", IP: "cn"},
	"ir": {Site: "category-ir", IP: "ir"},
	"ru": {Site: "category-ru", IP: "ru"},
}

// geositePrefix starts the code of a category alone in Geo; maxGeositeName bounds the name after it, MaxGeoPerRule the codes of one rule.
const (
	geositePrefix  = "geosite:"
	maxGeositeName = 64
	MaxGeoPerRule  = 32
)

// geoListOf is the lists of a code of a split rule: a known country, or a well-formed "geosite:<name>" (site list only).
func geoListOf(code string) (geoList, bool) {
	if name, ok := strings.CutPrefix(code, geositePrefix); ok {
		return geoList{Site: name}, validGeositeName(name)
	}
	l, ok := geoLists[code]
	return l, ok
}

// validGeositeName is the form of a v2fly list name: lower-case letters, digits, "-", and "!" or "@" after the first character
// (geolocation-!cn, a name with an attribute).
func validGeositeName(s string) bool {
	if s == "" || len(s) > maxGeositeName {
		return false
	}
	for i, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || i > 0 && (r == '-' || r == '!' || r == '@')) {
			return false
		}
	}
	return true
}

// adsSite is the geosite list of ad and tracker domains that a preset with BlockAds makes the apps drop (v2fly/domain-list-community
// data file, so the same URLs below serve it). The one place its name is written; adsTag is its rule set's name in Mihomo and sing-box.
const (
	adsSite = "category-ads-all"
	adsTag  = "geosite-" + adsSite
)

// Where the rule-set files live. %s is the list name.
const (
	mihomoGeositeURL  = "https://github.com/MetaCubeX/meta-rules-dat/raw/meta/geo/geosite/%s.mrs"
	mihomoGeoipURL    = "https://github.com/MetaCubeX/meta-rules-dat/raw/meta/geo/geoip/%s.mrs"
	singboxGeositeURL = "https://raw.githubusercontent.com/SagerNet/sing-geosite/rule-set/geosite-%s.srs"
	singboxGeoipURL   = "https://raw.githubusercontent.com/SagerNet/sing-geoip/rule-set/geoip-%s.srs"
)

// GeoCodes lists the codes a split rule may carry, sorted.
func GeoCodes() []string {
	out := make([]string, 0, len(geoLists))
	for c := range geoLists {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// normGeo validates the codes of one split rule: lower case, no duplicates, every one a known country or a well-formed category.
func normGeo(in []string) ([]string, error) {
	var out []string
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		if _, ok := geoListOf(c); !ok {
			if strings.HasPrefix(c, geositePrefix) {
				return nil, bad("invalid site category %q (geosite:<name>: lower-case letters, digits, - ! @, at most %d characters)", c, maxGeositeName)
			}
			return nil, bad("unknown country list %q (known: %s, or a category geosite:<name>)", c, strings.Join(GeoCodes(), ", "))
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) > MaxGeoPerRule {
		return nil, bad("at most %d country lists and site categories in a rule", MaxGeoPerRule)
	}
	return out, nil
}

// geositeTag and geoipTag name a list's rule set in the formats that keep rule sets by name (Mihomo, sing-box).
func geositeTag(code string) string { l, _ := geoListOf(code); return "geosite-" + l.Site }
func geoipTag(code string) string   { l, _ := geoListOf(code); return "geoip-" + l.IP }
