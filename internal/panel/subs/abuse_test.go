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
	mu     sync.Mutex
	valid  map[string]access.SubView
	calls  int
	checks int
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
	f.checks++
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

func (f *fakeSrc) checkLookups() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks
}

func (f *fakeSrc) set(token string, v access.SubView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.valid[token] = v
}

func (f *fakeSrc) rotate(old, next string, v access.SubView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.valid, old)
	f.valid[next] = v
}

type fakeEvents struct{ ch chan store.EventRow }

func (f fakeEvents) InsertEvent(_ context.Context, e store.EventRow) error {
	f.ch <- e
	return nil
}

type countingMissLimiter struct {
	securitylimit.Limiter
	records int
	byName  map[string]int
}

func (l *countingMissLimiter) Record(ctx context.Context, w securitylimit.Window, key string) (securitylimit.Decision, error) {
	l.records++
	if l.byName == nil {
		l.byName = make(map[string]int)
	}
	l.byName[w.Name]++
	return l.Limiter.Record(ctx, w, key)
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

// Token guessing limits unknown links while allowing cold valid links a bounded check during lockout.
// Unknown tokens stay on the decoy path without a subscription lookup or another miss record.
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
	missLimiter := &countingMissLimiter{Limiter: r.h.(*handler).cfg.Limiter}
	r.h.(*handler).cfg.Limiter = missLimiter
	for _, ip := range []string{bad, "2001:db8:aa:bb:ffff::7"} { // the same /64, another address
		if rec := r.get(ip, tokA); rec.Code != http.StatusOK || rec.Header().Get("Subscription-Userinfo") == "" {
			t.Fatalf("%s: a known valid token while blocked: %d %q", ip, rec.Code, rec.Body.String())
		}
	}
	unknownLookups := r.src.lookups()
	if rec := r.get(bad, unknownToken(5)); !isDecoy(rec) {
		t.Fatalf("blocked unknown token: %d %q", rec.Code, rec.Body.String())
	}
	if unknownLookups != lookups || r.src.lookups() != unknownLookups {
		t.Errorf("blocked traffic cost source lookups: before=%d after-valid=%d after-unknown=%d", lookups, unknownLookups, r.src.lookups())
	}
	if missLimiter.byName["subscription-miss"] != 0 || missLimiter.byName["subscription-cold-miss"] != 1 {
		t.Errorf("blocked unknown token records: %+v", missLimiter.byName)
	}
	for _, ip := range []string{"2001:db8:aa:cc::1", "203.0.113.7"} { // other clients are unaffected
		if rec := r.get(ip, tokB); rec.Code != 200 {
			t.Errorf("%s: %d", ip, rec.Code)
		}
	}
	r.now = r.now.Add(14 * time.Minute)
	if rec := r.get(bad, tokB); rec.Code != http.StatusOK {
		t.Errorf("known token while still blocked: %d", rec.Code)
	}
	if rec := r.get(bad, unknownToken(90)); !isDecoy(rec) {
		t.Errorf("blocked unknown token: %d", rec.Code)
	}
	if missLimiter.byName["subscription-miss"] != 0 || missLimiter.byName["subscription-cold-miss"] != 2 {
		t.Errorf("blocked unknown token records before expiry: %+v", missLimiter.byName)
	}
	r.now = r.now.Add(2 * time.Minute)
	if rec := r.get(bad, unknownToken(91)); !isDecoy(rec) {
		t.Errorf("unknown token after block expiry: %d", rec.Code)
	}
	if missLimiter.byName["subscription-miss"] != 1 {
		t.Errorf("expired block recorded %d normal misses, want one", missLimiter.byName["subscription-miss"])
	}
}

// Misses that are spread out do not add up, and malformed requests count toward the guessing limit.
func TestGuessingWindowAndMalformedTokens(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MissLimit, c.MissWindow = 5, time.Minute })
	misses := &countingMissLimiter{Limiter: r.h.(*handler).cfg.Limiter}
	r.h.(*handler).cfg.Limiter = misses
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
	if r.h.(*handler).blocked(context.Background(), "198.51.100.1") {
		t.Fatal("misses from separate windows kept the client blocked")
	}
	beforeMalformed := misses.records
	beforeChecks := r.src.checkLookups()
	for i := 0; i < 6; i++ {
		r.get("198.51.100.2", "short")
	}
	if counted := misses.records - beforeMalformed; counted != 5 {
		t.Fatalf("malformed paths counted %d misses, want the five allowed before lockout", counted)
	}
	if !r.h.(*handler).blocked(context.Background(), "198.51.100.2") {
		t.Fatal("malformed paths did not block the client after five misses")
	}
	if got := r.src.checkLookups(); got != beforeChecks {
		t.Fatalf("malformed paths performed %d cold-token checks", got-beforeChecks)
	}
	if rec := r.get("198.51.100.2", tokA); rec.Code != http.StatusOK {
		t.Errorf("cached valid token behind a blocked network: %d", rec.Code)
	}
}

func TestColdValidTokenWorksBehindBlockedAddress(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MissLimit, c.BlockFor = 1, 15*time.Minute })
	misses := &countingMissLimiter{Limiter: r.h.(*handler).cfg.Limiter}
	r.h.(*handler).cfg.Limiter = misses
	const client = "198.51.100.20"
	if rec := r.get(client, unknownToken(700)); !isDecoy(rec) {
		t.Fatalf("first guessing miss: %d %q", rec.Code, rec.Body.String())
	}
	if misses.records != 1 || r.src.lookups() != 1 {
		t.Fatalf("before cold valid token: misses=%d source lookups=%d", misses.records, r.src.lookups())
	}
	if _, known := r.h.(*handler).tokens.Get(tokA); known {
		t.Fatal("the valid token was already warm in this handler")
	}
	for i := 0; i < 4; i++ {
		rec := r.get(client, tokA)
		if rec.Code != http.StatusOK || rec.Header().Get("Subscription-Userinfo") == "" {
			t.Fatalf("cold valid token request %d behind blocked client: %d %q", i, rec.Code, rec.Body.String())
		}
	}
	if r.src.checkLookups() != 4 || r.src.lookups() != 2 {
		t.Fatalf("cold valid token checks=%d subscriptions=%d, want four checks after the initial miss", r.src.checkLookups(), r.src.lookups())
	}
	if misses.byName["subscription-miss"] != 1 || misses.byName["subscription-cold-miss"] != 0 {
		t.Fatalf("successful cold token changed miss windows: %+v", misses.byName)
	}
}

func TestColdTokenMissWindowsArePerClientNetwork(t *testing.T) {
	r := newRig(t, func(c *Config) { c.MissLimit, c.BlockFor = 1, 15*time.Minute })
	const client1 = "2001:db8:aa:bb::1"
	const client2 = "2001:db8:aa:bc::2"
	if rec := r.get(client1, unknownToken(800)); !isDecoy(rec) {
		t.Fatalf("first guessing miss: %d %q", rec.Code, rec.Body.String())
	}
	if rec := r.get(client2, unknownToken(801)); !isDecoy(rec) {
		t.Fatalf("second network guessing miss: %d %q", rec.Code, rec.Body.String())
	}
	for i, client := range []string{client1, client2} {
		if rec := r.get(client, unknownToken(1000+i)); !isDecoy(rec) {
			t.Fatalf("cold unknown token for network %s: %d %q", client, rec.Code, rec.Body.String())
		}
	}
	if got := r.src.checkLookups(); got != 2 {
		t.Fatalf("cold checks across two client networks: %d, want one per network", got)
	}
	if got := r.src.lookups(); got != 2 {
		t.Fatalf("blocked unknown tokens reached subscription fetch: %d lookups after the two initial misses", got)
	}
	for i, client := range []string{client1, client2} {
		if rec := r.get(client, unknownToken(1200+i)); !isDecoy(rec) {
			t.Fatalf("cold unknown token beyond network limit %s: %d %q", client, rec.Code, rec.Body.String())
		}
	}
	if got := r.src.checkLookups(); got != 2 {
		t.Fatalf("cold checks after each network reached its limit: %d, want 2", got)
	}
}

func TestBlockedClientColdLookupsStayWithinWindowDuringSixteenMinutes(t *testing.T) {
	r := newRig(t, nil)
	const client = "198.51.100.40"
	h := r.h.(*handler)
	blockedWindows := make([]int, 0, 2)
	blocked, blockedChecks := false, 0
	for sec := 0; sec < 16*60; sec++ {
		blockedNow := h.blocked(context.Background(), client)
		if blocked && !blockedNow {
			blockedWindows = append(blockedWindows, blockedChecks)
			blockedChecks = 0
		}
		if !blocked && blockedNow {
			blockedChecks = 0
		}
		beforeChecks := r.src.checkLookups()
		if rec := r.get(client, unknownToken(10_000+sec)); !isDecoy(rec) {
			t.Fatalf("unknown token at second %d: %d %q", sec, rec.Code, rec.Body.String())
		}
		if blockedNow {
			blockedChecks += r.src.checkLookups() - beforeChecks
		}
		blocked = blockedNow
		r.now = r.now.Add(time.Second)
	}
	if blocked {
		blockedWindows = append(blockedWindows, blockedChecks)
	}
	if len(blockedWindows) != 2 {
		t.Fatalf("sixteen-minute run had %d blocked intervals, want two", len(blockedWindows))
	}
	for i, checks := range blockedWindows {
		if checks > h.cfg.MissLimit {
			t.Fatalf("blocked interval %d caused %d cold token checks, want at most %d", i+1, checks, h.cfg.MissLimit)
		}
	}
	total := r.src.lookups() + r.src.checkLookups()
	if total > 4*h.cfg.MissLimit {
		t.Fatalf("sixteen-minute run caused %d database lookups, want at most two normal and two blocked-cold windows (%d)", total, 4*h.cfg.MissLimit)
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
	if rec := r.get("198.51.100.19", tokA); rec.Code != http.StatusOK {
		t.Fatalf("a cold valid token behind the shared network block: %d %q", rec.Code, rec.Body.String())
	}
	if rec := r.get("198.51.100.19", unknownToken(22)); !isDecoy(rec) {
		t.Fatalf("unknown token behind the shared network block: %d %q", rec.Code, rec.Body.String())
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
	if r.src.lookups() != 1 || r.src.checkLookups() != 1 || again.Body.String() != first.Body.String() || again.Header().Get("Subscription-Userinfo") != first.Header().Get("Subscription-Userinfo") {
		t.Fatalf("not served from the cache after checking the token: %d lookups, %d checks, %q", r.src.lookups(), r.src.checkLookups(), again.Body.String())
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

func TestRotatedTokenIsRejectedDuringTheCacheWindow(t *testing.T) {
	r := newRig(t, nil)
	if rec := r.get("203.0.113.1", tokA); rec.Code != http.StatusOK {
		t.Fatalf("first fetch: %d", rec.Code)
	}
	r.src.rotate(tokA, unknownToken(100), access.SubView{UserName: "alice", Status: access.StatusActive, Lines: []string{"hysteria2://new@de1.example.com:443/"}})
	r.now = r.now.Add(time.Second)
	if rec := r.get("203.0.113.1", tokA); !isDecoy(rec) {
		t.Fatalf("rotated token returned cached data: %d", rec.Code)
	}
	if r.src.lookups() != 1 || r.src.checkLookups() != 1 {
		t.Fatalf("old token was rendered instead of rejected: %d view lookups, %d token checks", r.src.lookups(), r.src.checkLookups())
	}
	if _, ok := r.h.(*handler).tokens.Get(tokA); ok {
		t.Fatal("rotated token stayed in the cache map")
	}
}

// The cached data of a token goes MinInterval after its last fetch, with no further request: a token whose app
// refreshes hourly must not hold its view for as long as the panel runs.
func TestCachedViewIsDroppedAfterTheInterval(t *testing.T) {
	r := newRig(t, func(c *Config) { c.Now, c.MinInterval = time.Now, 50*time.Millisecond })
	if rec := r.get("203.0.113.1", tokA); rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	st, _ := r.h.(*handler).tokens.Get(tokA)
	views := func() int {
		st.mu.Lock()
		defer st.mu.Unlock()
		return len(st.cached)
	}
	if views() != 1 {
		t.Fatalf("%d views right after the fetch", views())
	}
	for deadline := time.Now().Add(5 * time.Second); views() != 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the view is still cached 5 s after the interval")
		}
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
