package subs_test

import (
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

// With the real DNS module behind it, a Happ client gets the routing profile of the user's effective preset
// and every other client gets none.
func TestHappRoutingFromTheDNSModule(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.Routing = subs.HappRouting(dns.New(r.st)) })
	_, tok := r.user("alice", nil)

	got := fetch(h, "/"+tok, happUA).Header().Get("Routing")
	if !strings.HasPrefix(got, "happ://routing/onadd/") || len(got) < 40 {
		t.Errorf("routing header for Happ: %q", got)
	}
	if fetch(h, "/"+tok, curlUA).Header().Get("Routing") != "" {
		t.Error("curl got a routing header")
	}
}
