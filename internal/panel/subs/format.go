package subs

import (
	"strings"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// Choose picks what a client gets: the first rule whose ua_contains is a case-insensitive
// substring of the User-Agent, else the user page for a browser-like User-Agent, else the base64 URI list.
// rule is the index into settings.Rules (-1 = fallback); browser tells the fallback took the browser branch.
func Choose(set *adminv1.SubscriptionSettings, ua string) (rule int, format adminv1.SubFormat, browser bool) {
	l := strings.ToLower(ua)
	for i, r := range set.GetRules() {
		if u := strings.ToLower(r.GetUaContains()); u != "" && strings.Contains(l, u) {
			return i, r.Format, false
		}
	}
	if IsBrowser(ua) {
		return -1, adminv1.SubFormat_SUB_FORMAT_USER_PAGE, true
	}
	return -1, adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, false
}

// nonBrowser lists client names that put a browser-shaped prefix in their User-Agent but are VPN apps.
var nonBrowser = []string{"happ", "v2ray", "clash", "mihomo", "hiddify", "sing-box", "streisand", "shadowrocket", "karing", "flclash", "nekobox", "okhttp", "cfnetwork"}

// IsBrowser reports a browser-like User-Agent: "Mozilla/" plus a rendering engine token, and none of the
// well-known VPN clients. Heuristic; an unusual client is steered with a rule in the settings.
func IsBrowser(ua string) bool {
	if !strings.HasPrefix(ua, "Mozilla/") {
		return false
	}
	l := strings.ToLower(ua)
	for _, n := range nonBrowser {
		if strings.Contains(l, n) {
			return false
		}
	}
	return strings.Contains(ua, "AppleWebKit") || strings.Contains(ua, "Gecko/") || strings.Contains(ua, "Firefox/") || strings.Contains(ua, "Trident/")
}

// isHapp reports a Happ-like client, the ones that read the `routing` header.
func isHapp(ua string) bool { return strings.Contains(strings.ToLower(ua), "happ") }
