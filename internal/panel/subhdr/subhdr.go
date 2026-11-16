// Package subhdr names the response headers of the subscription endpoint and says which names the owner's own response
// headers (SubscriptionSettings.response_headers) may not take.
//
// The constants are what the code of internal/panel/subs sets, and that code names a header only through them: a test of
// that package reads its source and fails for a header it sets by a literal or by a name that is not in Panel. So the list
// of names the owner may not use is the list of what the panel sets, kept in one place, not a second list beside it. What
// is not the panel's own, the transport and security headers of every HTTP answer, is listed below it.
package subhdr

import (
	"slices"
	"strings"
)

// The headers the subscription endpoint sets on its answers (app answers, the user page, the devices API, the client API
// and the refusals). Add a header the code sets here and to Panel; the tests fail until both are done.
const (
	Announce                      = "Announce"
	CacheControl                  = "Cache-Control"
	ContentDisposition            = "Content-Disposition"
	ContentEncoding               = "Content-Encoding"
	ContentSecurityPolicy         = "Content-Security-Policy"
	ContentType                   = "Content-Type"
	ETag                          = "ETag"
	MistgateClientAPI             = "Mistgate-Client-Api"
	PingResult                    = "Ping-Result"
	PingType                      = "Ping-Type"
	ProbeReport                   = "Probe-Report"
	ProfileTitle                  = "Profile-Title"
	ProfileUpdateInterval         = "Profile-Update-Interval"
	ProfileWebPageURL             = "Profile-Web-Page-Url"
	ReferrerPolicy                = "Referrer-Policy"
	RetryAfter                    = "Retry-After"
	Routing                       = "Routing"
	SubscriptionPingOnopenEnabled = "Subscription-Ping-Onopen-Enabled"
	SubscriptionUserinfo          = "Subscription-Userinfo"
	SupportURL                    = "Support-Url"
	Vary                          = "Vary"
	XContentTypeOptions           = "X-Content-Type-Options"
	XFrameOptions                 = "X-Frame-Options"
	XHwidActive                   = "X-Hwid-Active"
	XHwidLimit                    = "X-Hwid-Limit"
	XHwidMaxDevicesReached        = "X-Hwid-Max-Devices-Reached"
	XHwidNotSupported             = "X-Hwid-Not-Supported"
	XRobotsTag                    = "X-Robots-Tag"
)

// Panel lists every constant above.
var Panel = []string{
	Announce, CacheControl, ContentDisposition, ContentEncoding, ContentSecurityPolicy, ContentType, ETag, MistgateClientAPI,
	PingResult, PingType, ProbeReport, ProfileTitle, ProfileUpdateInterval, ProfileWebPageURL, ReferrerPolicy, RetryAfter, Routing,
	SubscriptionPingOnopenEnabled, SubscriptionUserinfo, SupportURL, Vary, XContentTypeOptions, XFrameOptions, XHwidActive,
	XHwidLimit, XHwidMaxDevicesReached, XHwidNotSupported, XRobotsTag,
}

// Families the panel and the apps that read it own: any name that starts with one is reserved, so that a header the panel
// adds tomorrow in a family (another X-Hwid-*, a Profile-*) does not meet an owner's header of the same name.
var panelPrefixes = []string{"X-Hwid-", "Profile-", "Subscription-", "Mistgate-", "Ping-"}

// transport are the names the answer of any HTTP server carries or that a proxy, a browser or the platform acts on: the
// message framing and hop-by-hop headers (RFC 9110 section 7.6.1, RFC 9112), caching, cookies, redirects, content
// negotiation, range and conditional requests, authentication, and the security and cross-origin policy headers. The
// panel's own and the owner's headers must not meet them.
var transport = []string{
	"Accept-Ranges", "Age", "Allow", "Alt-Svc", "Alt-Used", "Authorization", "Cache-Status", "Clear-Site-Data", "Connection",
	"Cookie", "Cookie2", "Date", "Early-Data", "ETag", "Expect", "Expires", "Forwarded", "From", "Host", "Keep-Alive",
	"Last-Modified", "Link", "Location", "Max-Forwards", "NEL", "Origin", "Origin-Agent-Cluster", "Permissions-Policy",
	"Pragma", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Range", "Refresh", "Report-To", "Reporting-Endpoints",
	"Server", "Server-Timing", "Service-Worker-Allowed", "Set-Cookie", "Set-Cookie2", "Strict-Transport-Security", "TE", "Timing-Allow-Origin",
	"Trailer", "Transfer-Encoding", "Upgrade", "Upgrade-Insecure-Requests", "User-Agent", "Via", "Warning", "WWW-Authenticate",
	"X-Accel-Buffering", "X-Accel-Redirect", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Powered-By",
	"X-Real-Ip", "X-Request-Id", "X-Xss-Protection",
}

// transportPrefixes are families of the transport: Content-* (type, length, encoding, language, location, range, disposition,
// security policy...), Accept-*, Access-Control-*, If-*, Cross-Origin-*, Sec-*, Proxy-*, X-Forwarded-*, and the platform's
// own cf-* (Cloudflare) and x-envoy-* names.
var transportPrefixes = []string{"Content-", "Accept-", "Access-Control-", "If-", "Cross-Origin-", "Sec-", "Proxy-", "X-Forwarded-", "CF-", "X-Envoy-"}

// Reserved reports whether name (any case) is a header the owner may not set: one of the panel's own, a member of one of
// its families, or a transport header.
func Reserved(name string) bool {
	if slices.ContainsFunc(Panel, func(n string) bool { return strings.EqualFold(n, name) }) || slices.ContainsFunc(transport, func(n string) bool { return strings.EqualFold(n, name) }) {
		return true
	}
	return hasPrefix(name, panelPrefixes) || hasPrefix(name, transportPrefixes)
}

func hasPrefix(name string, prefixes []string) bool {
	return slices.ContainsFunc(prefixes, func(p string) bool { return len(name) >= len(p) && strings.EqualFold(name[:len(p)], p) })
}

// ReservedNames is the same rule as a list for the admin: the prefixes written with a trailing "*" ("X-Hwid-*") and the exact
// names that no prefix covers, sorted.
func ReservedNames() []string {
	var out []string
	for _, p := range slices.Concat(panelPrefixes, transportPrefixes) {
		out = append(out, p+"*")
	}
	for _, n := range slices.Concat(Panel, transport) {
		if !hasPrefix(n, panelPrefixes) && !hasPrefix(n, transportPrefixes) {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return slices.CompactFunc(out, strings.EqualFold)
}
