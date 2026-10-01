package httpserver

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
)

// probe is a client whose X-Forwarded-For can be set per request.
type probe struct {
	*browser
	xff string
}

func newProbe(t *testing.T, e *testEnv) *probe {
	p := &probe{browser: newBrowser(t, e.admin.URL+"/", "")}
	p.jar.xff = func() string { return p.xff }
	return p
}

// hit makes one rate-limited call that costs the server next to nothing (a login name
// that cannot exist is refused before any hashing) and reports whether it was limited.
func (p *probe) hit(xff string) (limited bool) {
	p.xff = xff
	_, err := p.passwordLogin("no such login!", "x", "000000")
	switch code(err) {
	case connect.CodeResourceExhausted:
		return true
	case connect.CodeUnauthenticated:
		return false
	}
	p.t.Fatalf("unexpected answer for %q: %v", xff, err)
	return false
}

func (p *probe) burst(xff string, n int) (limited int) {
	for range n {
		if p.hit(xff) {
			limited++
		}
	}
	return
}

func TestRateLimitKeysOnTheRealClient(t *testing.T) {
	proxy := func(c *auth.Config) { c.TrustedProxies = loopback() }

	t.Run("trusted proxy: each forwarded client has its own bucket", func(t *testing.T) {
		p := newProbe(t, newTestEnvAuth(t, proxy))
		if l := p.burst("203.0.113.1", 15); l < 4 || l > 5 {
			t.Fatalf("client A: %d of 15 limited, want the burst of 10 to pass", l)
		}
		if p.burst("203.0.113.2", 5) != 0 {
			t.Error("client B was limited by client A")
		}
		// Values the client put at the left of the header do not change who it is.
		if !p.hit("9.9.9.9, 203.0.113.1") || !p.hit("198.51.100.9,203.0.113.1") {
			t.Error("a forged left-hand entry bought a fresh bucket")
		}
		// The proxy's own address is not a client: a chain of only trusted hops is the leftmost.
		if p.burst("127.0.0.1", 12) == 0 {
			t.Error("requests naming the proxy itself were never limited")
		}
	})

	t.Run("IPv6 is keyed by /64", func(t *testing.T) {
		p := newProbe(t, newTestEnvAuth(t, proxy))
		if l := p.burst("2001:db8:1:2::1", 15); l < 4 {
			t.Fatalf("first address: %d limited", l)
		}
		for _, sibling := range []string{"2001:db8:1:2::2", "2001:db8:1:2:ffff:ffff:ffff:ffff", "[2001:db8:1:2::77]:4711"} {
			if !p.hit(sibling) {
				t.Errorf("%s has its own bucket inside one /64", sibling)
			}
		}
		if p.burst("2001:db8:1:3::1", 5) != 0 {
			t.Error("the next /64 shares a bucket")
		}
	})

	t.Run("without trusted proxies the header is ignored", func(t *testing.T) {
		p := newProbe(t, newTestEnv(t))
		limited := 0
		for i := range 15 { // a new "client" every request
			if p.hit("203.0.113." + string(rune('1'+i%9))) {
				limited++
			}
		}
		if limited < 4 {
			t.Fatalf("rotating X-Forwarded-For evaded the limit (%d limited)", limited)
		}
	})

	t.Run("a peer outside the trusted networks cannot forge", func(t *testing.T) {
		p := newProbe(t, newTestEnvAuth(t, func(c *auth.Config) { c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")} }))
		limited := 0
		for i := range 15 {
			if p.hit("203.0.113." + string(rune('1'+i%9))) {
				limited++
			}
		}
		if limited < 4 {
			t.Fatalf("an untrusted peer chose its own bucket (%d limited)", limited)
		}
	})
}

func TestCeremoniesArePerSourceCapped(t *testing.T) {
	e := newTestEnvAuth(t, func(c *auth.Config) { c.TrustedProxies = loopback() })
	p := newProbe(t, e)
	begin := func(xff string) error {
		p.xff = xff
		_, _, err := p.beginLogin()
		return err
	}
	// 8 pending ceremonies per source; the 9th is refused (the burst of 10 is not the limit here).
	for i := range 8 {
		if err := begin("203.0.113.1"); err != nil {
			t.Fatalf("ceremony %d: %v", i, err)
		}
	}
	err := begin("203.0.113.1")
	if code(err) != connect.CodeResourceExhausted || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("9th ceremony from one source: %v", err)
	}
	// Other sources are unaffected, and an IPv6 /64 is one source.
	if err := begin("203.0.113.2"); err != nil {
		t.Fatalf("another source: %v", err)
	}
	for i := range 8 {
		if err := begin("2001:db8:5:5::" + string(rune('1'+i))); err != nil {
			t.Fatalf("v6 ceremony %d: %v", i, err)
		}
	}
	if err := begin("2001:db8:5:5::ff"); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("9th ceremony from one /64: %v", err)
	}
	// Setup ceremonies count against the same source.
	tok := e.setupToken(t, time.Now())
	p.xff = "203.0.113.9"
	for range 8 {
		if _, err := p.api.BeginSetup(context.Background(), connect.NewRequest(&adminv1.BeginSetupRequest{SetupToken: tok})); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := p.api.BeginSetup(context.Background(), connect.NewRequest(&adminv1.BeginSetupRequest{SetupToken: tok})); code(err) != connect.CodeResourceExhausted {
		t.Errorf("9th setup ceremony: %v", err)
	}
}

func TestAuditAndSessionsRecordTheForwardedClient(t *testing.T) {
	e := newTestEnvAuth(t, func(c *auth.Config) { c.TrustedProxies = loopback() })
	p := newProbe(t, e)
	p.xff = "203.0.113.77"
	if err := p.register(e.setupToken(t, time.Now()), "Ada"); err != nil {
		t.Fatal(err)
	}
	p.xff = "9.9.9.9, 2001:db8:a:b:c:d:e:f"
	p.logout()
	if err := p.login(); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.st.ListAudit(context.Background(), "", 0, 10)
	got := map[string]string{}
	for _, r := range rows {
		got[r.Action+"/"+r.Result] = r.IP
	}
	// The audit log keeps the full address (only rate limiting masks IPv6).
	if got["setup/ok"] != "203.0.113.77" || got["logout/ok"] != "2001:db8:a:b:c:d:e:f" || got["login/ok"] != "2001:db8:a:b:c:d:e:f" {
		t.Errorf("audit IPs: %v", got)
	}
	r, err := p.api.ListSessions(context.Background(), connect.NewRequest(&adminv1.ListSessionsRequest{}))
	if err != nil || len(r.Msg.Sessions) != 1 || r.Msg.Sessions[0].Ip != "2001:db8:a:b:c:d:e:f" {
		t.Errorf("session ip: %+v %v", r, err)
	}
}
