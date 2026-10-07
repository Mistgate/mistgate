package subs

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

var (
	tokA = strings.Repeat("a", 43)
	tokB = strings.Repeat("b", 43)
)

const decoyText = "decoy 404"

var decoyHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(decoyText))
})

// fakeSrc knows the tokens in valid and counts the lookups.
type fakeSrc struct {
	mu    sync.Mutex
	valid map[string]access.SubView
	calls int
}

func (f *fakeSrc) Subscription(_ context.Context, token string) (access.SubView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	v, ok := f.valid[token]
	if !ok {
		return access.SubView{}, access.ErrUnknownToken
	}
	return v, nil
}

func (f *fakeSrc) CheckSubscriptionToken(_ context.Context, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.valid[token]; !ok {
		return access.ErrUnknownToken
	}
	return nil
}

func (f *fakeSrc) lookups() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeSrc) set(token string, v access.SubView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.valid[token] = v
}

type fakeEvents struct{ ch chan store.EventRow }

func (f fakeEvents) InsertEvent(_ context.Context, e store.EventRow) error {
	f.ch <- e
	return nil
}

// rig drives a handler with a fake clock and clients chosen by the X-Test-IP header.
type rig struct {
	t     *testing.T
	src   *fakeSrc
	h     http.Handler
	now   time.Time
	ev    fakeEvents
	extra func(*Config)
}

func newRig(t *testing.T, mut func(*Config)) *rig {
	t.Helper()
	r := &rig{t: t, now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), ev: fakeEvents{make(chan store.EventRow, 16)}}
	r.src = &fakeSrc{valid: map[string]access.SubView{
		tokA: {UserName: "alice", Status: access.StatusActive, Lines: []string{"hysteria2://x@de1.example.com:443/"}, Total: 100},
		tokB: {UserName: "bob", Status: access.StatusActive, Lines: []string{"hysteria2://y@de1.example.com:443/"}},
	}}
	cfg := Config{
		Events: r.ev,
		ClientIP: func(req *http.Request) netip.Addr {
			a, _ := netip.ParseAddr(req.Header.Get("X-Test-IP"))
			return a
		},
		Now: func() time.Time { return r.now },
	}
	if mut != nil {
		mut(&cfg)
	}
	r.h = Handler(r.src, decoyHandler, cfg)
	return r
}

func (r *rig) get(ip, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/"+token, nil)
	req.Header.Set("X-Test-IP", ip)
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

func isDecoy(rec *httptest.ResponseRecorder) bool {
	return rec.Code == 404 && rec.Body.String() == decoyText
}

func unknownToken(i int) string { return fmt.Sprintf("%043d", i) }

// Token guessing: after MissLimit unknown tokens within the window the client network gets only the
// decoy, for any token, for BlockFor. The block is per IPv4 address and per IPv6 /64, expires, does
// not cost a lookup and does not reach other clients.
func TestGuessingBlocksTheClientNetwork(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MissLimit, c.MissWindow, c.BlockFor = 5, time.Minute, 15*time.Minute })
	bad := "2001:db8:aa:bb::1"
	for i := 0; i < 4; i++ {
		if rec := r.get(bad, unknownToken(i)); !isDecoy(rec) {
			t.Fatalf("miss %d: %d", i, rec.Code)
		}
	}
	if rec := r.get(bad, tokA); rec.Code != 200 { // four misses: not blocked yet
		t.Fatalf("valid token before the block: %d", rec.Code)
	}
	r.get(bad, unknownToken(4)) // the fifth
	lookups := r.src.lookups()
	for _, ip := range []string{bad, "2001:db8:aa:bb:ffff::7"} { // the same /64, another address
		if rec := r.get(ip, tokA); !isDecoy(rec) || rec.Header().Get("Subscription-Userinfo") != "" {
			t.Fatalf("%s: a valid token while blocked: %d %q", ip, rec.Code, rec.Body.String())
		}
	}
	if r.src.lookups() != lookups {
		t.Error("a blocked client still cost lookups")
	}
	for _, ip := range []string{"2001:db8:aa:cc::1", "203.0.113.7"} { // other clients are unaffected
		if rec := r.get(ip, tokB); rec.Code != 200 {
			t.Errorf("%s: %d", ip, rec.Code)
		}
	}
	r.now = r.now.Add(14 * time.Minute)
	if rec := r.get(bad, tokB); !isDecoy(rec) {
		t.Errorf("block ended early: %d", rec.Code)
	}
	r.now = r.now.Add(2 * time.Minute)
	if rec := r.get(bad, tokB); rec.Code != 200 {
		t.Errorf("still blocked after the period: %d", rec.Code)
	}
}

// Misses that are spread out do not add up, and a request under the prefix that is no token counts as a miss.
func TestGuessingWindowAndMalformedTokens(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MissLimit, c.MissWindow = 5, time.Minute })
	for i := 0; i < 4; i++ {
		r.get("198.51.100.1", unknownToken(i))
	}
	r.now = r.now.Add(61 * time.Second)
	for i := 0; i < 4; i++ {
		r.get("198.51.100.1", unknownToken(10+i))
	}
	if rec := r.get("198.51.100.1", tokA); rec.Code != 200 {
		t.Fatalf("8 misses in two windows blocked the client: %d", rec.Code)
	}
	for i := 0; i < 5; i++ {
		r.get("198.51.100.2", "short")
	}
	if rec := r.get("198.51.100.2", tokA); !isDecoy(rec) {
		t.Errorf("malformed tokens do not count: %d", rec.Code)
	}
}

func TestGuessingLimitIsSharedByHandlers(t *testing.T) {
	limiter := securitylimit.NewMemory(nil, 0)
	r := newRig(t, func(c *Config) { c.MissLimit, c.Limiter = 2, limiter })
	first := r.h
	second := Handler(r.src, decoyHandler, Config{
		Limiter: limiter, MissLimit: 2, Now: func() time.Time { return r.now },
		ClientIP: func(req *http.Request) netip.Addr {
			a, _ := netip.ParseAddr(req.Header.Get("X-Test-IP"))
			return a
		},
	})
	r.get("198.51.100.19", unknownToken(20))
	r.h = second
	r.get("198.51.100.19", unknownToken(21))
	if rec := r.get("198.51.100.19", tokA); !isDecoy(rec) {
		t.Fatalf("a second handler did not see the shared network block: %d", rec.Code)
	}
	r.h = first
}

// A request from an unknown address is never limited or counted: the panel cannot tell clients apart then.
func TestUnknownClientIsNeverBlocked(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MissLimit = 3 })
	for i := 0; i < 20; i++ {
		r.get("", unknownToken(i))
	}
	if rec := r.get("", tokA); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
}

// Per token: a fetch within MinInterval of the last render is answered from memory, with the same bytes
// and headers; after it the subscription is rendered again and shows the change.
func TestFetchesAreServedFromACache(t *testing.T) {
	r := newRig(t, nil)
	first := r.get("203.0.113.1", tokA)
	if first.Code != 200 || r.src.lookups() != 1 {
		t.Fatalf("first: %d, %d lookups", first.Code, r.src.lookups())
	}
	r.src.set(tokA, access.SubView{UserName: "alice", Status: access.StatusActive, Lines: []string{"hysteria2://new@de1.example.com:443/"}, Total: 200})
	r.now = r.now.Add(9 * time.Second)
	again := r.get("198.51.100.9", tokA) // another client: the cache is per token
	if r.src.lookups() != 1 || again.Body.String() != first.Body.String() || again.Header().Get("Subscription-Userinfo") != first.Header().Get("Subscription-Userinfo") {
		t.Fatalf("not served from the cache: %d lookups, %q", r.src.lookups(), again.Body.String())
	}
	for _, k := range []string{"Content-Type", "Cache-Control", "Profile-Title", "Profile-Update-Interval"} {
		if again.Header().Get(k) != first.Header().Get(k) {
			t.Errorf("cached header %s differs", k)
		}
	}
	r.now = r.now.Add(2 * time.Second)
	fresh := r.get("203.0.113.1", tokA)
	if r.src.lookups() != 2 || fresh.Body.String() == first.Body.String() || !strings.Contains(fresh.Header().Get("Subscription-Userinfo"), "total=200") {
		t.Fatalf("after the interval: %d lookups, %q", r.src.lookups(), fresh.Header().Get("Subscription-Userinfo"))
	}
	// The cache is per token.
	if r.get("203.0.113.1", tokB); r.src.lookups() != 3 {
		t.Errorf("another token was served from tokA's cache (%d lookups)", r.src.lookups())
	}
	// A rotated link dies with the first lookup after the cache, and is forgotten.
	r.src.mu.Lock()
	delete(r.src.valid, tokA)
	r.src.mu.Unlock()
	r.now = r.now.Add(11 * time.Second)
	if rec := r.get("203.0.113.1", tokA); !isDecoy(rec) {
		t.Errorf("rotated link: %d", rec.Code)
	}
}

// A hard cap per token per hour: cached answers count, the next one is a 429 with Retry-After, and the
// hour after the first fetch of the window opens it again. Other tokens are not affected.
func TestHourlyCapPerToken(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MaxPerHour = 5 })
	for i := 0; i < 5; i++ {
		if rec := r.get("203.0.113.1", tokA); rec.Code != 200 {
			t.Fatalf("fetch %d: %d", i, rec.Code)
		}
	}
	rec := r.get("198.51.100.9", tokA) // any client: the cap is on the link
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "3600" || strings.Contains(rec.Body.String(), "hysteria2") {
		t.Fatalf("over the cap: %d %v", rec.Code, rec.Header())
	}
	if rec := r.get("203.0.113.1", tokB); rec.Code != 200 {
		t.Errorf("another token: %d", rec.Code)
	}
	r.now = r.now.Add(30 * time.Minute)
	if rec := r.get("203.0.113.1", tokA); rec.Code != 429 || rec.Header().Get("Retry-After") != "1800" {
		t.Errorf("half an hour later: %d %v", rec.Code, rec.Header())
	}
	r.now = r.now.Add(31 * time.Minute)
	if rec := r.get("203.0.113.1", tokA); rec.Code != 200 {
		t.Errorf("the next hour: %d", rec.Code)
	}
}

// The event is written by the after-response runner: nothing is written until the saved work runs.
func TestSharedSuspectEventWaitsForTheAfterResponseRunner(t *testing.T) {
	var saved []func()
	r := newRig(t, func(c *Config) {
		c.SharedNets = 1
		c.MaxPerHour = -1
		c.AfterResponse = func(work func()) { saved = append(saved, work) }
	})
	r.get("203.0.113.1", tokA)
	r.get("198.51.100.1", tokA) // the second network: over
	if len(saved) != 1 {
		t.Fatalf("%d works handed to the runner, want 1", len(saved))
	}
	select {
	case e := <-r.ev.ch:
		t.Fatalf("event written before the runner ran the work: %+v", e)
	default:
	}
	saved[0]()
	select {
	case e := <-r.ev.ch:
		if e.Code != EventSharedSuspect {
			t.Errorf("event: %+v", e)
		}
	default:
		t.Fatal("no event after the saved work ran")
	}
}

// "Link shared": distinct client networks per token per day; above the threshold exactly one event per day,
// carrying counts only.
func TestSharedLinkSignal(t *testing.T) {
	r := newRig(t, func(c *Config) { c.SharedNets = 3; c.MaxPerHour = -1 })
	wantNone := func(why string) {
		t.Helper()
		select {
		case e := <-r.ev.ch:
			t.Fatalf("%s: unexpected event %+v", why, e)
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Many hosts of one /24 and of one /48 are two networks, not many.
	for i := 1; i <= 30; i++ {
		r.get(fmt.Sprintf("203.0.113.%d", i), tokA)
		r.get(fmt.Sprintf("2001:db8:1:%x::%d", i, i), tokA)
	}
	wantNone("two networks")
	r.get("198.51.100.1", tokA) // the third
	wantNone("three networks, threshold is above three")
	ips := []string{"192.0.2.77", "100.64.9.9", "10.9.8.7", "172.20.1.1"}
	r.get(ips[0], tokA) // the fourth: over
	var e store.EventRow
	select {
	case e = <-r.ev.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("no event above the threshold")
	}
	if e.Code != EventSharedSuspect || e.Severity != 2 || e.Source != "panel" || e.Params["user"] != "alice" || e.Params["networks"] != "4" {
		t.Errorf("event: %+v", e)
	}
	// No address anywhere in it.
	all := fmt.Sprint(e.Params, e.Code)
	for _, frag := range []string{"203.0.113", "2001:db8", "198.51.100", "192.0.2", "100.64"} {
		if strings.Contains(all, frag) {
			t.Errorf("event carries address material %q: %s", frag, all)
		}
	}
	for _, ip := range ips[1:] {
		r.get(ip, tokA)
	}
	wantNone("second event on the same day")
	r.get("203.0.113.1", tokB)
	wantNone("another token with one network")
	// The next day counts afresh and reports again.
	r.now = r.now.Add(24 * time.Hour)
	for _, ip := range []string{"203.0.113.1", "198.51.100.1", "192.0.2.1", "100.64.9.9"} {
		r.get(ip, tokA)
	}
	select {
	case e := <-r.ev.ch:
		if e.Params["user"] != "alice" {
			t.Errorf("next day's event: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event on the next day")
	}
}

// Without an event sink the signal is silently off; the rest works.
func TestSharedLinkSignalWithoutSink(t *testing.T) {
	r := newRig(t, func(c *Config) { c.SharedNets = 1; c.Events = nil })
	for _, ip := range []string{"203.0.113.1", "198.51.100.1", "192.0.2.1"} {
		if rec := r.get(ip, tokA); rec.Code != 200 {
			t.Fatalf("%d", rec.Code)
		}
	}
}

// The tables are bounded: a scan from a million networks, or a flood of valid tokens, does not grow them.
func TestTablesAreBounded(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MaxKeys, c.MissLimit = 64, 10000 })
	h := r.h.(*handler)
	for i := 0; i < 2000; i++ {
		r.get(fmt.Sprintf("2001:db8:%x::1", i), unknownToken(i))
	}
	if n := h.cfg.Limiter.(*securitylimit.Memory).Size("subscription-miss"); n != 64 {
		t.Errorf("%d client miss records, want the cap of 64", n)
	}
	for i := 0; i < 2000; i++ {
		tok := unknownToken(1_000_000 + i)
		r.src.set(tok, access.SubView{Status: access.StatusActive})
		r.get("203.0.113.1", tok)
	}
	if n := h.tokens.Len(); n != 64 {
		t.Errorf("%d token records, want the cap of 64", n)
	}
}
