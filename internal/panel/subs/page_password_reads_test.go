package subs_test

import (
	"context"
	"net/http"
	"net/netip"
	"sync"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// handlerOver is rig.handler over another Source (one that wraps the access module).
func (r *rig) handlerOver(src subs.Source, mut func(*subs.Config)) (http.Handler, *subsettings.Cache) {
	cache := subsettings.NewCache(r.st, nil)
	cfg := subs.Config{
		Title: "Fallback", BaseURL: "https://sub.example.com/k3xq8", Settings: cache, BrandTTL: -1,
		Brand:    func(ctx context.Context) (instance.Settings, error) { return instance.Load(ctx, r.st) },
		Dist:     pageDist,
		ClientIP: func(*http.Request) netip.Addr { return netip.MustParseAddr("198.51.100.7") },
	}
	if mut != nil {
		mut(&cfg)
	}
	return subs.Handler(src, decoy, cfg), cache
}

// readCounter counts what the public handler asks the access module for: a view of the person (SubscriptionWith, the many-statement
// read of the user's row and everything the page shows) and the one-row check that confirms a link and now also carries the state of
// the page password. A D1 request costs by the statements behind these calls, and the statements of each call are the ones they were
// before the password state existed: the state is two more columns of the row the call already reads.
type readCounter struct {
	*access.Service
	mu            sync.Mutex
	views, checks int
}

func (c *readCounter) Subscription(ctx context.Context, token string) (access.SubView, error) {
	c.mu.Lock()
	c.views++
	c.mu.Unlock()
	return c.Service.Subscription(ctx, token)
}

func (c *readCounter) SubscriptionWith(ctx context.Context, token string, opt access.SubOptions) (access.SubView, error) {
	c.mu.Lock()
	c.views++
	c.mu.Unlock()
	return c.Service.SubscriptionWith(ctx, token, opt)
}

func (c *readCounter) CheckSubscriptionToken(ctx context.Context, token string) (access.PageState, error) {
	c.mu.Lock()
	c.checks++
	c.mu.Unlock()
	return c.Service.CheckSubscriptionToken(ctx, token)
}

func (c *readCounter) take() (views, checks int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	views, checks = c.views, c.checks
	c.views, c.checks = 0, 0
	return
}

func newCountedGate(t *testing.T) (*gate, *readCounter) {
	t.Helper()
	g := &gate{m3rig: newM3Rig(t), now: time.Unix(1_800_000_000, 0), ip: "198.51.100.7"}
	cnt := &readCounter{Service: g.svc}
	g.h, g.cache = g.rig.handlerOver(cnt, func(c *subs.Config) {
		c.PageKey = pageKey(t)
		c.Events = g.st
		c.Now = func() time.Time { return g.now }
		c.ClientIP = func(*http.Request) netip.Addr { a, _ := netip.ParseAddr(g.ip); return a }
	})
	return g, cnt
}

// The calls to the access module behind one request, for each kind of request. The "before" column is what the handler made before the
// state of the password existed (the same calls: this change adds none to a page view, an app's refresh or an unlock).
//
//	request                                                    views  checks
//	page, first (fresh view)                                   1      0
//	page, again within MinInterval (cached view)               0      1
//	app refresh, first / again                                 1 / 0  0 / 1
//	unlock                                                     1      0
//	device call, genuine cookie of the current generation      1      0
//	device call, no cookie, password asked (locked)            0      1   (was 0: a known token was answered from memory)
//	device call, no cookie, password removed (goes on)         1      1   (was: locked, no way in)
func TestRequestsMakeTheSameReadsAsBeforeThePasswordState(t *testing.T) {
	g, cnt := newCountedGate(t)
	uid, tok := g.newUser("alice", nil)
	cnt.take()
	call := call{g.h, t, tok}
	expect := func(what string, views, checks int) {
		t.Helper()
		if v, c := cnt.take(); v != views || c != checks {
			t.Errorf("%s: %d views and %d checks, want %d and %d", what, v, c, views, checks)
		}
	}

	g.page(tok)
	expect("page, first", 1, 0)
	g.page(tok)
	expect("page, again within MinInterval", 0, 1)
	g.now = g.now.Add(time.Minute)
	fetch(g.h, "/"+tok, happUA)
	expect("app refresh, first", 1, 0)
	fetch(g.h, "/"+tok, happUA)
	expect("app refresh, again", 0, 1)

	c := cookieOf(t, g.unlock(t, tok, g.password(uid)))
	cnt.take()
	g.unlock(t, tok, "aaaa-bbbb")
	expect("unlock", 1, 0)
	call.post("/devices", g.addBody("phone"), "Cookie", ck(c))
	if v, ch := cnt.take(); v != 1 || ch != 0 {
		t.Errorf("device call with the cookie: %d views and %d checks, want 1 and 0", v, ch)
	}
	call.post("/devices", g.addBody("phone"))
	expect("device call without the cookie, password asked", 0, 1)

	g.action(uid, adminv1.PagePasswordAction_PAGE_PASSWORD_ACTION_REMOVE)
	cnt.take()
	call.post("/devices", g.addBody("tablet"))
	if v, ch := cnt.take(); v != 1 || ch != 1 {
		t.Errorf("device call of a person with no password: %d views and %d checks, want 1 and 1", v, ch)
	}
}
