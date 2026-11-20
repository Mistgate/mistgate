package mtproto

import (
	"strings"
	"testing"
)

func TestBootstrapPageContainsOnlyTheMetaToken(t *testing.T) {
	page := string(bootstrapPage(`token"&<>`))
	want := `<!doctype html><html><head><meta name="tproxy-bootstrap" content="token&#34;&amp;&lt;&gt;"></head><body></body></html>`
	if page != want || strings.Contains(page, "<script") {
		t.Fatalf("bootstrap page = %q", page)
	}
}
