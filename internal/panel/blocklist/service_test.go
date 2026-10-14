package blocklist

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

type testEnv struct {
	t       *testing.T
	st      *store.Store
	fake    *fakeLists
	svc     *Service
	logs    *bytes.Buffer
	steps   int
	country string
	mu      sync.Mutex
	now     time.Time
	hosts   map[string]string // node id -> IPv4 ("" = does not resolve)
	timeout time.Duration
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &testEnv{t: t, st: st, fake: newFakeLists(t), logs: &bytes.Buffer{}, country: "RU", now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		hosts: map[string]string{}}
	e.fake.standard()
	e.svc, err = New(Config{Store: st, StepUp: func(context.Context) error { e.steps++; return nil }, URLs: e.fake.urls(), HTTPClient: e.fake.srv.Client(),
		Log:     slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:     func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		Country: func(context.Context) string { return e.country },
		Address: func(_ context.Context, n store.NodeRow) (netip.Addr, bool) {
			a, err := netip.ParseAddr(e.hosts[n.ID])
			return a, err == nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *testEnv) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

// node adds an active node whose address is ip.
func (e *testEnv) node(id, ip string) {
	e.t.Helper()
	if _, err := e.st.W.ExecContext(context.Background(), `INSERT INTO node (id, name, address, provider, state, created_at) VALUES (?, ?, ?, 'hoster', 'active', 1)`,
		id, id, ip); err != nil {
		e.t.Fatal(err)
	}
	e.hosts[id] = ip
}

func ownerCtx() context.Context {
	return auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
}

func ptr[T any](v T) *T { return &v }

func (e *testEnv) enable() {
	e.t.Helper()
	if _, err := (rpc{e.svc}).UpdateBlocklist(ownerCtx(), connect.NewRequest(&adminv1.UpdateBlocklistRequest{Enabled: ptr(true)})); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) get() *adminv1.BlocklistSettings {
	e.t.Helper()
	r, err := (rpc{e.svc}).GetBlocklist(ownerCtx(), connect.NewRequest(&adminv1.GetBlocklistRequest{}))
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Msg.Settings
}

func (e *testEnv) test() (*adminv1.TestBlocklistResponse, error) {
	r, err := (rpc{e.svc}).TestBlocklist(ownerCtx(), connect.NewRequest(&adminv1.TestBlocklistRequest{}))
	if err != nil {
		return nil, err
	}
	return r.Msg, nil
}

func (e *testEnv) rows() map[string]store.BlocklistCheck {
	e.t.Helper()
	rows, err := e.st.RecentBlocklistChecks(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]store.BlocklistCheck{}
	for _, r := range rows {
		out[r.NodeID] = r
	}
	return out
}

func connectCode(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return 0
}

func TestOffByDefaultNothingHappens(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	if s := e.get(); s.Enabled || !s.Available || s.CheckedUnix != 0 {
		t.Fatalf("defaults = %+v", s)
	}
	if e.svc.Active(context.Background()) {
		t.Fatal("Active while off")
	}
	for range 3 {
		if e.svc.Tick(context.Background()) {
			t.Fatal("a check ran while off")
		}
	}
	if _, err := e.test(); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "blocklist_off") {
		t.Fatalf("Test while off = %v", err)
	}
	if e.fake.total() != 0 || len(e.rows()) != 0 {
		t.Fatalf("while off: %d requests, %d rows", e.fake.total(), len(e.rows()))
	}
}

func TestTickChecksNodesAndStoresTheResult(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")  // in ipsum and in the CDN list
	e.node("nl1", "192.0.2.77")    // in community, and in a CDN range
	e.node("fi1", "198.51.100.44") // in ipsum only
	e.node("se1", "10.99.0.1")     // nowhere
	e.node("v6", "")               // does not resolve to an IPv4 address
	e.enable()
	if !e.svc.Active(context.Background()) {
		t.Fatal("not active after enabling")
	}
	if !e.svc.Tick(context.Background()) {
		t.Fatal("the first tick did not check")
	}
	rows := e.rows()
	if len(rows) != 5 {
		t.Fatalf("rows = %+v", rows)
	}
	de := rows["de1"]
	if !de.InRKN || de.ListedBy != "antifilter_ipsum" || de.RKNSubnet != "203.0.113.0/24" || de.CDNProvider != "ExampleHost" || de.CDNRegion != "eu" ||
		!de.At.Equal(e.now) || !de.ListsAt.Equal(time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("de1 = %+v", de)
	}
	if nl := rows["nl1"]; !nl.InRKN || nl.ListedBy != "antifilter_community" || nl.RKNSubnet != "192.0.2.77/32" || nl.CDNProvider != "ExampleCloud" || nl.CDNRegion != "" {
		t.Fatalf("nl1 = %+v", nl)
	}
	if fi := rows["fi1"]; !fi.InRKN || fi.CDNProvider != "" {
		t.Fatalf("fi1 = %+v", fi)
	}
	for _, id := range []string{"se1", "v6"} {
		if r := rows[id]; r.InRKN || r.ListedBy != "" || r.RKNSubnet != "" || r.CDNProvider != "" {
			t.Fatalf("%s = %+v", id, r)
		}
	}
	s := e.get()
	if !s.Enabled || s.CheckedUnix != e.now.Unix() || s.ListsUnix != time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("settings after a check = %+v", s)
	}
	// four lists, one request each
	for _, p := range []string{"/ipsum", "/community", "/cdn"} {
		if e.fake.count(p) != 1 {
			t.Errorf("%s asked %d times", p, e.fake.count(p))
		}
	}
	if e.fake.count("/mirror") != 0 {
		t.Error("the mirror was asked although the main address answered")
	}
}

func TestTickIsLazyUntilDue(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.enable()
	if !e.svc.Tick(context.Background()) {
		t.Fatal("no first check")
	}
	n := e.fake.total()
	// the minutes after: nothing is due, nothing is asked
	for range 5 {
		e.advance(time.Minute)
		if e.svc.Tick(context.Background()) {
			t.Fatal("a check ran before it was due")
		}
	}
	if e.fake.total() != n {
		t.Fatalf("%d requests while nothing was due", e.fake.total()-n)
	}

	// a node added a minute ago is looked up at once, from the lists in memory: no new request
	e.node("nl1", "192.0.2.77")
	if !e.svc.Tick(context.Background()) {
		t.Fatal("a new node did not make a check due")
	}
	if e.fake.total() != n || !e.rows()["nl1"].InRKN {
		t.Fatalf("new node: %d requests, rows %+v", e.fake.total()-n, e.rows())
	}

	// six hours after, the results are old: the lists are asked again with validators, and the unchanged ones answer 304
	e.advance(6*time.Hour + time.Minute)
	if !e.svc.Tick(context.Background()) {
		t.Fatal("no check after six hours")
	}
	if e.fake.count("/ipsum") != 2 || e.fake.conds["/ipsum"] != 1 || e.fake.conds["/community"] != 1 {
		t.Fatalf("refresh: ipsum hits=%d conditional=%d community conditional=%d", e.fake.count("/ipsum"), e.fake.conds["/ipsum"], e.fake.conds["/community"])
	}
	if !e.rows()["de1"].At.Equal(e.now) {
		t.Fatal("the result was not renewed")
	}
}

func TestAfterARestartTheListsAreDownloadedOnlyWhenDue(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.enable()
	e.svc.Tick(context.Background())
	n := e.fake.total()
	e.svc.reset() // what a restart does to the memory
	e.advance(time.Hour)
	if e.svc.Tick(context.Background()) || e.fake.total() != n {
		t.Fatal("a restart downloaded the lists although the results are fresh")
	}
	e.advance(6 * time.Hour)
	if !e.svc.Tick(context.Background()) || e.fake.count("/ipsum") != 2 || e.fake.conds["/ipsum"] != 0 {
		t.Fatalf("after a restart, when due: ipsum hits=%d conditional=%d", e.fake.count("/ipsum"), e.fake.conds["/ipsum"])
	}
}

func TestTestRPCReportsAndIsRateLimited(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.node("se1", "10.99.0.1")
	e.enable()
	r, err := e.test()
	if err != nil {
		t.Fatal(err)
	}
	if r.Prefixes != 3 || r.CdnRanges != 2 || r.NodesChecked != 2 || r.NodesListed != 1 || r.MirrorUsed || r.ListsUnix != time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("test = %+v", r)
	}
	if len(e.rows()) != 2 {
		t.Fatalf("the test stores its result: %+v", e.rows())
	}
	// again at once: refused; later: the lists are asked with validators
	if _, err := e.test(); connectCode(err) != connect.CodeResourceExhausted {
		t.Fatalf("second test = %v", err)
	}
	e.advance(time.Minute)
	if _, err := e.test(); err != nil {
		t.Fatal(err)
	}
	if e.fake.conds["/ipsum"] != 1 || e.fake.conds["/cdn"] != 0 { // the CDN fake sends no validators: a full download
		t.Fatalf("conditional: ipsum=%d cdn=%d", e.fake.conds["/ipsum"], e.fake.conds["/cdn"])
	}
}

func TestTestRPCUsesTheMirrorAndFailsWhenNothingAnswers(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.enable()
	e.fake.force("/ipsum", http.StatusBadGateway)
	r, err := e.test()
	if err != nil || !r.MirrorUsed || r.NodesListed != 1 {
		t.Fatalf("mirror test = %+v, %v", r, err)
	}

	e2 := newEnv(t)
	e2.node("de1", "203.0.113.10")
	e2.enable()
	e2.fake.force("/ipsum", http.StatusBadGateway)
	e2.fake.force("/mirror", http.StatusServiceUnavailable)
	if _, err := e2.test(); connectCode(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "blocklist_unreachable") {
		t.Fatalf("nothing answers: %v", err)
	}
	if len(e2.rows()) != 0 {
		t.Fatal("a failed test stored rows")
	}
}

// The optional lists can fail: the registry list alone is enough to check.
func TestAnOptionalListMayFail(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.fake.force("/community", http.StatusInternalServerError)
	e.fake.force("/cdn", http.StatusInternalServerError)
	e.enable()
	if !e.svc.Tick(context.Background()) {
		t.Fatal("no check")
	}
	if r := e.rows()["de1"]; !r.InRKN || r.CDNProvider != "" {
		t.Fatalf("de1 = %+v", r)
	}
}

func TestFailedDownloadBacksOff(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.fake.force("/ipsum", http.StatusBadGateway)
	e.fake.force("/mirror", http.StatusBadGateway)
	e.enable()
	if e.svc.Tick(context.Background()) {
		t.Fatal("a check ran without lists")
	}
	n := e.fake.total()
	e.advance(5 * time.Minute)
	if e.svc.Tick(context.Background()) || e.fake.total() != n {
		t.Fatal("it asked again before the retry time")
	}
	e.fake.force("/ipsum", 0)
	e.advance(retryAfter)
	if !e.svc.Tick(context.Background()) || !e.rows()["de1"].InRKN {
		t.Fatal("no check after the retry time")
	}
	if st, _ := e.svc.load(context.Background()); !st.retryAt.IsZero() {
		t.Fatalf("the retry time stayed: %v", st.retryAt)
	}
}

func TestAnotherCountryDoesNothing(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.enable()
	e.country = "KZ"
	if s := e.get(); !s.Enabled || s.Available {
		t.Fatalf("settings for KZ = %+v", s)
	}
	if e.svc.Active(context.Background()) {
		t.Fatal("Active for another country")
	}
	if e.svc.Tick(context.Background()) || e.fake.total() != 0 || len(e.rows()) != 0 {
		t.Fatalf("it did something for KZ: %d requests", e.fake.total())
	}
	if _, err := e.test(); connectCode(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "blocklist_unavailable_for_country") {
		t.Fatalf("Test for KZ = %v", err)
	}
	e.country = "ru" // case does not matter
	if !e.svc.Active(context.Background()) || !e.svc.Tick(context.Background()) {
		t.Fatal("not back for RU")
	}
}

func TestSwitchingOffStopsEverythingAndHidesTheResults(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.enable()
	e.svc.Tick(context.Background())
	if len(e.rows()) != 1 || !e.svc.ipsum.loaded {
		t.Fatal("setup")
	}
	if _, err := (rpc{e.svc}).UpdateBlocklist(ownerCtx(), connect.NewRequest(&adminv1.UpdateBlocklistRequest{Enabled: ptr(false)})); err != nil {
		t.Fatal(err)
	}
	if e.svc.Active(context.Background()) {
		t.Fatal("still active")
	}
	if s := e.get(); s.Enabled || s.CheckedUnix != 0 || s.ListsUnix != 0 {
		t.Fatalf("settings off = %+v", s)
	}
	n := e.fake.total()
	e.advance(24 * time.Hour)
	if e.svc.Tick(context.Background()) || e.fake.total() != n {
		t.Fatal("it downloaded while off")
	}
	if e.svc.ipsum.loaded {
		t.Fatal("the lists stayed in memory while off")
	}
	if len(e.rows()) != 1 {
		t.Fatal("switching off deleted the stored rows: they stay until retention, hidden by Active")
	}
	if e.steps != 2 {
		t.Fatalf("step-ups = %d, want one per Update", e.steps)
	}
	// on again: it checks at once (the results are old)
	e.enable()
	if !e.svc.Tick(context.Background()) {
		t.Fatal("no check after switching it on again")
	}
}

func TestDisconnectForgetsListsAndResults(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.enable()
	e.svc.Tick(context.Background())
	steps := e.steps
	r, err := (rpc{e.svc}).DisconnectBlocklist(ownerCtx(), connect.NewRequest(&adminv1.DisconnectBlocklistRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if r.Msg.Settings.Enabled || e.steps != steps+1 {
		t.Fatalf("disconnect: %+v, step-ups %d", r.Msg.Settings, e.steps-steps)
	}
	if len(e.rows()) != 0 || e.svc.ipsum.loaded || e.svc.community.loaded || e.svc.cdn.loaded {
		t.Fatalf("not forgotten: %d rows, lists loaded %v", len(e.rows()), e.svc.ipsum.loaded)
	}
	n := e.fake.total()
	if e.svc.Tick(context.Background()) || e.fake.total() != n {
		t.Fatal("it ran after Disconnect")
	}
	// idempotent
	if _, err := (rpc{e.svc}).DisconnectBlocklist(ownerCtx(), connect.NewRequest(&adminv1.DisconnectBlocklistRequest{})); err != nil {
		t.Fatal(err)
	}
}

// Switching off in the middle of a check cancels it and stores nothing.
func TestDisconnectInTheMiddleOfACheckStoresNothing(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.enable()
	e.fake.mu.Lock()
	e.fake.delay["/cdn"] = 5 * time.Second
	e.fake.mu.Unlock()
	done := make(chan bool, 1)
	go func() { done <- e.svc.Tick(context.Background()) }()
	for i := 0; e.fake.count("/cdn") == 0; i++ {
		if i > 500 {
			t.Fatal("the check never reached the last list")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := (rpc{e.svc}).DisconnectBlocklist(ownerCtx(), connect.NewRequest(&adminv1.DisconnectBlocklistRequest{})); err != nil {
		t.Fatal(err)
	}
	if ran := <-done; ran {
		t.Fatal("the cancelled check reported success")
	}
	if len(e.rows()) != 0 {
		t.Fatalf("rows after a disconnect in the middle: %+v", e.rows())
	}
}

func TestOnlyTheOwnerMayCall(t *testing.T) {
	e := newEnv(t)
	r := rpc{e.svc}
	for name, ctx := range map[string]context.Context{
		"anonymous": context.Background(),
		"admin":     auth.WithAdmin(context.Background(), store.Admin{ID: "adm_x", Role: store.RoleHelper}),
		"token":     auth.WithAdmin(context.Background(), store.Admin{ID: "token:abc", Role: store.RoleOwner}),
		"mcp":       auth.WithAdmin(context.Background(), store.Admin{ID: "mcp:abc", Role: store.RoleOwner}),
	} {
		if _, err := r.GetBlocklist(ctx, connect.NewRequest(&adminv1.GetBlocklistRequest{})); connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Get = %v", name, err)
		}
		if _, err := r.UpdateBlocklist(ctx, connect.NewRequest(&adminv1.UpdateBlocklistRequest{Enabled: ptr(true)})); connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Update = %v", name, err)
		}
		if _, err := r.TestBlocklist(ctx, connect.NewRequest(&adminv1.TestBlocklistRequest{})); connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Test = %v", name, err)
		}
		if _, err := r.DisconnectBlocklist(ctx, connect.NewRequest(&adminv1.DisconnectBlocklistRequest{})); connectCode(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Disconnect = %v", name, err)
		}
	}
	if st, _ := e.svc.load(context.Background()); st.enabled || e.steps != 0 {
		t.Fatal("a refused call changed something")
	}
}

func TestUpdateAsksForAStepUpFirst(t *testing.T) {
	e := newEnv(t)
	denied := errors.New("step-up required")
	e.svc.cfg.StepUp = func(context.Context) error { return denied }
	if _, err := (rpc{e.svc}).UpdateBlocklist(ownerCtx(), connect.NewRequest(&adminv1.UpdateBlocklistRequest{Enabled: ptr(true)})); !errors.Is(err, denied) {
		t.Fatalf("Update = %v", err)
	}
	if _, err := (rpc{e.svc}).DisconnectBlocklist(ownerCtx(), connect.NewRequest(&adminv1.DisconnectBlocklistRequest{})); !errors.Is(err, denied) {
		t.Fatalf("Disconnect = %v", err)
	}
	if e.get().Enabled {
		t.Fatal("it changed without the step-up")
	}
}

// No node address, name or id is in any request the panel makes: the lists are fetched by fixed address, with no query,
// body, cookie or referrer.
func TestNoNodeAddressLeavesThePanel(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "203.0.113.10")
	e.node("nl1", "192.0.2.77")
	e.enable()
	e.svc.Tick(context.Background())
	if _, err := e.test(); err != nil {
		t.Fatal(err)
	}
	e.fake.mu.Lock()
	defer e.fake.mu.Unlock()
	if len(e.fake.requests) == 0 {
		t.Fatal("no request recorded")
	}
	for _, r := range e.fake.requests {
		if strings.Contains(r, "203.0.113.10") || strings.Contains(r, "192.0.2.77") || strings.Contains(r, "de1") || strings.Contains(r, "?") || !strings.HasPrefix(r, "GET /") {
			t.Errorf("request %q", r)
		}
	}
}
