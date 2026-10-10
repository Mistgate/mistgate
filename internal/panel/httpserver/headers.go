package httpserver

var (
	headerContentTypeHTML      = []string{"text/html; charset=utf-8"}
	headerContentTypePlainText = []string{"text/plain; charset=utf-8"}
	headerContentTypeSVG       = []string{"image/svg+xml"}
	headerLogoCSP              = []string{"default-src 'none'; style-src 'unsafe-inline'; sandbox"}
	headerCacheNoStore         = []string{"no-store"}
	headerCacheNoCache         = []string{"no-cache"}
	headerCacheImmutable       = []string{"public, max-age=31536000, immutable"}
	headerNoSniff              = []string{"nosniff"}
	headerFrameDeny            = []string{"DENY"}
	headerNoReferrer           = []string{"no-referrer"}
	headerCOOPSameOrigin       = []string{"same-origin"}
	headerRobotsNoIndex        = []string{"noindex, nofollow"}
	headerHSTS                 = []string{"max-age=31536000"}
)
