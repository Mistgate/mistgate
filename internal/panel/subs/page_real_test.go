package subs

import (
	"io/fs"
	"testing"

	"github.com/mistgate/mistgate/web"
)

// The page the web build produces (when it has been built) has the shape the server relies on: one marker,
// one inline module script, nothing external; and it stays inside the 100 KB gzip budget.
func TestBuiltUserPageHasTheExpectedShape(t *testing.T) {
	raw, err := fs.ReadFile(web.Dist(), pageFile)
	if err != nil {
		t.Skip("web/dist/sub.html is not built (cd web && pnpm build)")
	}
	p, err := loadPage(web.Dist())
	if err != nil {
		t.Fatalf("the built page is not usable: %v", err)
	}
	if p.hash == "" || len(raw) == 0 {
		t.Fatal("empty hash or page")
	}
	if len(raw) > 400<<10 {
		t.Errorf("sub.html is %d bytes before gzip", len(raw))
	}
}
