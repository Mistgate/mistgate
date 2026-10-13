package subs

import (
	"net/http"

	"github.com/mistgate/mistgate/internal/panel/access"
)

var (
	headerContentTypeHTML      = []string{"text/html; charset=utf-8"}
	headerContentTypeJSON      = []string{"application/json; charset=utf-8"}
	headerContentTypeYAML      = []string{"text/yaml; charset=utf-8"}
	headerContentTypePlainText = []string{"text/plain; charset=utf-8"}
	headerCacheNoStore         = []string{"no-store"}
	headerNoSniff              = []string{"nosniff"}
	headerNoReferrer           = []string{"no-referrer"}
	headerRobotsNoIndex        = []string{"noindex, nofollow"}
	headerFrameDeny            = []string{"DENY"}
	headerFrameSameOrigin      = []string{"SAMEORIGIN"}
	headerVaryAcceptEncoding   = []string{"Accept-Encoding"}
	headerContentEncodingGzip  = []string{"gzip"}
)

// firstHeader is the first value of a header whose name is already canonical (what net/http stores): no canonicalization,
// no allocation, on the path of every subscription fetch.
func firstHeader(r *http.Request, canonical string) string {
	if v := r.Header[canonical]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// deviceHint reads what an app says about its device: X-HWID (the standard of Happ, Remnawave and 3x-ui), X-Device-OS,
// X-Ver-OS, X-Device-Model and the User-Agent.
func deviceHint(r *http.Request) access.DeviceHint {
	return access.NewDeviceHint(firstHeader(r, "X-Hwid"), firstHeader(r, "X-Device-Os"), firstHeader(r, "X-Ver-Os"),
		firstHeader(r, "X-Device-Model"), r.UserAgent())
}
