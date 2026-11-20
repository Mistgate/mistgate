package mtproto

import "html"

// bootstrapPage is a placeholder until the bridge package supplies its page. Keep the one-use token in a meta attribute,
// without a script or other bridge behavior in this engine package.
func bootstrapPage(token string) []byte {
	return []byte(`<!doctype html><html><head><meta name="tproxy-bootstrap" content="` + html.EscapeString(token) + `"></head><body></body></html>`)
}
