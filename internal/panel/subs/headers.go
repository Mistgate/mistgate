package subs

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
