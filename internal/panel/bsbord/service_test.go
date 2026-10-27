package bsbord

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const testUnit = "example|krym|off"
const testKey = "bsk_live_example_test_key"

type fakeRequest struct {
	Method, Path, Body, Auth, IdempotencyKey string
	At                                       time.Time
}

type fakeReply struct {
	Status int
	Body   string
}

type fakeBsbord struct {
	mu             sync.Mutex
	srv            *httptest.Server
	requests       []fakeRequest
	probeReplies   []fakeReply
	previewReplies []fakeReply
	previewCost    int64
	operators      string
}

func newFakeBsbord(t *testing.T) *fakeBsbord {
	t.Helper()
	f := &fakeBsbord{previewCost: 10}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeBsbord) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, fakeRequest{Method: r.Method, Path: r.URL.Path, Body: string(body),
		Auth: r.Header.Get("Authorization"), IdempotencyKey: r.Header.Get("Idempotency-Key"), At: time.Now()})
	var reply fakeReply
	switch r.URL.Path {
	case "/v1/account":
		reply = fakeReply{Status: http.StatusOK, Body: `{"balance_credits":900,"bonus_credits":40,"balance_total":940,"tier":"pro","min_interval_sec":60}`}
	case "/v1/operators":
		body := f.operators
		if body == "" {
			body = `{"units":[{"op_key":"example|krym|off","operator":"Example Mobile","name":"Example","region":"Krym","region_code":"krym","dpi":"off","channel_state":"ready","probeable":true}],"n_units":1,"n_probeable":1}`
		}
		reply = fakeReply{Status: http.StatusOK, Body: body}
	case "/v1/probe/preview":
		if len(f.previewReplies) > 0 {
			reply = f.previewReplies[0]
			f.previewReplies = f.previewReplies[1:]
		} else {
			reply = fakeReply{Status: http.StatusOK, Body: fmt.Sprintf(`{"cost_credits":%d}`, f.previewCost)}
		}
	case "/v1/probe":
		if len(f.probeReplies) > 0 {
			reply = f.probeReplies[0]
			f.probeReplies = f.probeReplies[1:]
			if reply.Status == http.StatusOK && reply.Body == "" {
				reply.Body = successfulProbe(string(body), f.previewCost)
			}
		} else {
			reply = fakeReply{Status: http.StatusOK, Body: successfulProbe(string(body), f.previewCost)}
		}
	case "/v1/probe/cancel":
		reply = fakeReply{Status: http.StatusOK, Body: `{}`}
	default:
		reply = fakeReply{Status: http.StatusNotFound, Body: `{}`}
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-API-Version", "test-v1")
	w.WriteHeader(reply.Status)
	_, _ = io.WriteString(w, reply.Body)
}

func (f *fakeBsbord) snapshot() []fakeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.requests)
}

func (f *fakeBsbord) enqueueProbe(replies ...fakeReply) {
	f.mu.Lock()
	f.probeReplies = append(f.probeReplies, replies...)
	f.mu.Unlock()
}

func (f *fakeBsbord) enqueuePreview(replies ...fakeReply) {
	f.mu.Lock()
	f.previewReplies = append(f.previewReplies, replies...)
	f.mu.Unlock()
}

func (f *fakeBsbord) setPreviewCost(n int64) {
	f.mu.Lock()
	f.previewCost = n
	f.mu.Unlock()
}

func (f *fakeBsbord) setOperators(body string) {
	f.mu.Lock()
	f.operators = body
	f.mu.Unlock()
}

type gatedBsbord struct {
	srv     *httptest.Server
	entered chan struct{}
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	keys    []string
	cancels int
}

func newGatedBsbord(t *testing.T) *gatedBsbord {
	t.Helper()
	g := &gatedBsbord{entered: make(chan struct{}), release: make(chan struct{})}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/v1/probe/preview":
			_, _ = io.WriteString(w, `{"cost_credits":10}`)
		case "/v1/probe/cancel":
			g.mu.Lock()
			g.cancels++
			g.mu.Unlock()
			_, _ = io.WriteString(w, `{}`)
		case "/v1/probe":
			g.mu.Lock()
			g.keys = append(g.keys, r.Header.Get("Idempotency-Key"))
			g.mu.Unlock()
			g.once.Do(func() { close(g.entered) })
			<-g.release
			_, _ = io.WriteString(w, successfulProbe(string(body), 10))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func successfulProbe(body string, cost int64) string {
	var req probeRequest
	_ = json.Unmarshal([]byte(body), &req)
	byTarget := make(map[string]probeTargetResult, len(req.Targets))
	for _, target := range req.Targets {
		byOp := make(map[string]probeLeg, len(req.Operators))
		for _, unit := range req.Operators {
			byOp[unit] = probeLeg{OK: true, Operator: "Example Mobile", Region: "Krym", DPI: "off",
				ICMP: &probeCell{OK: true}, TCP: &probeCell{OK: true}}
		}
		byTarget[target] = probeTargetResult{ByOperator: byOp}
	}
	out, _ := json.Marshal(probeResponse{Outcome: "done", CostCredits: cost, ByTarget: byTarget})
	return string(out)
}

type testEnv struct {
	t       *testing.T
	ctx     context.Context
	st      *store.Store
	vlt     *vault.Vault
	fake    *fakeBsbord
	svc     *Service
	now     time.Time
	targets []health.CountryTarget
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{t: t, ctx: ctx, st: st, vlt: vlt, fake: newFakeBsbord(t),
		now:     time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		targets: []health.CountryTarget{{Key: "de1", IP: "203.0.113.10", Ports: []uint16{80, 443}}},
	}
	e.newService()
	return e
}

func (e *testEnv) newService() {
	e.t.Helper()
	e.svc = e.serviceWith(e.fake.srv.Client(), e.fake.srv.URL)
}

func (e *testEnv) serviceWith(client *http.Client, baseURL string) *Service {
	e.t.Helper()
	s, err := New(Config{Store: e.st, Vault: e.vlt, StepUp: func(context.Context) error { return nil },
		Targets:    func(context.Context) ([]health.CountryTarget, error) { return slices.Clone(e.targets), nil },
		HTTPClient: client, BaseURL: baseURL, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return e.now }})
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *testEnv) serviceOn(g *gatedBsbord) *Service {
	return e.serviceWith(g.srv.Client(), g.srv.URL)
}

func (e *testEnv) configure(enabled bool, units []string, cap int64) {
	e.t.Helper()
	sealed := e.vlt.Seal([]byte(testKey), keyAAD)
	st := settings{Enabled: enabled, Sealed: sealed, Units: slices.Clone(units), IntervalMin: 60, CapCredits: cap}
	if err := e.svc.saveConfigAndState(e.ctx, st); err != nil {
		e.t.Fatal(err)
	}
}

func ownerContext() context.Context {
	return auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
}

func TestOffDoesNothingAndTestRPCOnlyReadsAccountAndUnits(t *testing.T) {
	e := newTestEnv(t)
	e.svc.Tick(e.ctx)
	if got := e.fake.snapshot(); len(got) != 0 {
		t.Fatalf("disabled integration sent requests: %+v", got)
	}
	e.configure(false, []string{testUnit}, DefaultCapCredits)
	res, err := (rpc{e.svc}).TestBsbord(ownerContext(), connect.NewRequest(&adminv1.TestBsbordRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Msg.Reachable || res.Msg.Tier != "pro" || res.Msg.BalanceTotal != 940 || res.Msg.NProbeable != 1 || res.Msg.ApiVersion != "test-v1" {
		t.Fatalf("TestBsbord response = %+v", res.Msg)
	}
	got := e.fake.snapshot()
	if len(got) != 2 || got[0].Path != "/v1/account" || got[1].Path != "/v1/operators" {
		t.Fatalf("test requests = %+v", got)
	}
	for _, r := range got {
		if r.Method != http.MethodGet || strings.Contains(r.Body, "203.0.113.") {
			t.Fatalf("test sent node data: %+v", r)
		}
	}
}

func TestListUnitsMarksDefaultPresetAndPreviewShowsMonthlyEstimate(t *testing.T) {
	e := newTestEnv(t)
	e.configure(false, nil, DefaultCapCredits)
	listed, err := (rpc{e.svc}).ListBsbordUnits(ownerContext(), connect.NewRequest(&adminv1.ListBsbordUnitsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Msg.Units) != 1 || !listed.Msg.Units[0].Selected || listed.Msg.Units[0].OpKey != testUnit {
		t.Fatalf("default operator preset = %+v", listed.Msg.Units)
	}
	e.configure(true, []string{testUnit}, DefaultCapCredits)
	st, err := e.svc.load(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.IntervalMin = DefaultIntervalMin
	if err := e.svc.saveConfig(e.ctx, st); err != nil {
		t.Fatal(err)
	}
	preview, err := (rpc{e.svc}).PreviewBsbord(ownerContext(), connect.NewRequest(&adminv1.PreviewBsbordRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if preview.Msg.CostCredits != 10 || preview.Msg.MonthlyCredits != 300 || preview.Msg.TargetCount != 1 || preview.Msg.UnitCount != 1 {
		t.Fatalf("preview estimate = %+v", preview.Msg)
	}
	for _, r := range e.fake.snapshot() {
		if r.Path == "/v1/probe" {
			t.Fatal("PreviewBsbord sent a paid request")
		}
	}
}

func TestUpdateValidatesAndSealsWriteOnlyKey(t *testing.T) {
	e := newTestEnv(t)
	for _, key := range []string{"wrong_prefix", "bsk_live_has space", "bsk_live_" + strings.Repeat("x", maxKey)} {
		key := key
		_, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{Key: &key}))
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("key %q: UpdateBsbord error = %v, want invalid argument", key, err)
		}
	}
	key, enabled := testKey, true
	units := &adminv1.BsbordUnitSelection{OpKeys: []string{testUnit}}
	res, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{Key: &key, Enabled: &enabled, Units: units}))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Msg.Settings.HasKey || !res.Msg.Settings.Enabled {
		t.Fatalf("write-only key settings = %+v", res.Msg.Settings)
	}
	st, err := e.svc.load(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.svc.secret(st)
	if err != nil || got.Reveal() != testKey {
		t.Fatalf("sealed key round trip = %q, %v", got.Reveal(), err)
	}
}

func TestPreviewAndPaidProbeUseSameBodyAndPersistRowsAndSpend(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 100)
	e.svc.Tick(e.ctx)
	var preview, paid []fakeRequest
	for _, r := range e.fake.snapshot() {
		switch r.Path {
		case "/v1/probe/preview":
			preview = append(preview, r)
		case "/v1/probe":
			paid = append(paid, r)
		}
	}
	if len(preview) != 1 || len(paid) != 1 || preview[0].Body != paid[0].Body {
		t.Fatalf("preview/probe requests differ: preview=%+v paid=%+v", preview, paid)
	}
	if paid[0].IdempotencyKey == "" || !strings.Contains(preview[0].Body, `"dpi":"any"`) || !strings.Contains(preview[0].Body, `"203.0.113.10:443"`) {
		t.Fatalf("paid request body/key = %q / %q", paid[0].Body, paid[0].IdempotencyKey)
	}
	rows, err := e.st.RecentExtChecks(e.ctx, 10, Source)
	if err != nil || len(rows) != 1 || rows[0].NodeID != "de1" || rows[0].Verdict != countryreach.VerdictOK {
		t.Fatalf("persisted rows = %+v, %v", rows, err)
	}
	st, err := e.svc.load(e.ctx)
	if err != nil || st.Spend.Credits != 10 || st.Pending != nil || st.LastStatus != "ok" {
		t.Fatalf("round state = %+v, %v", st, err)
	}
}

func TestPendingIdempotencySurvivesTimeoutConflictAndServiceRestart(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 100)
	e.fake.enqueueProbe(
		fakeReply{Status: 524, Body: `{"error":{"code":"gateway_timeout","details":{"retryable":true}}}`},
		fakeReply{Status: http.StatusConflict, Body: `{"error":{"code":"request_in_progress","details":{"retryable":true,"retry_after":1}}}`},
		fakeReply{Status: http.StatusOK},
	)
	e.svc.Tick(e.ctx)
	first, err := e.svc.load(e.ctx)
	if err != nil || first.Pending == nil || !first.Pending.Sent {
		t.Fatalf("pending after 524 = %+v, %v", first.Pending, err)
	}
	firstKey, firstBody := first.Pending.Key, first.Pending.Body
	e.now = e.now.Add(time.Minute)
	e.newService() // the in-memory service is gone; only the stored pending key/body can resume the request.
	e.svc.Tick(e.ctx)
	middle, err := e.svc.load(e.ctx)
	if err != nil || middle.Pending == nil || middle.Pending.Key != firstKey || middle.Pending.Body != firstBody {
		t.Fatalf("pending after restart/409 = %+v, %v", middle.Pending, err)
	}
	e.now = e.now.Add(time.Minute)
	e.svc.Tick(e.ctx)
	posts := make([]fakeRequest, 0, 3)
	for _, r := range e.fake.snapshot() {
		if r.Path == "/v1/probe" {
			posts = append(posts, r)
		}
	}
	if len(posts) != 3 {
		t.Fatalf("paid POSTs = %+v", posts)
	}
	for _, r := range posts {
		if r.IdempotencyKey != firstKey || r.Body != firstBody {
			t.Fatalf("retry changed key/body: want %q %q, got %+v", firstKey, firstBody, r)
		}
	}
	st, err := e.svc.load(e.ctx)
	if err != nil || st.Pending != nil || st.Spend.Credits != 10 {
		t.Fatalf("final spend = %+v, pending=%+v, err=%v", st.Spend, st.Pending, err)
	}
}

func TestCapStopsBeforePaidPost(t *testing.T) {
	e := newTestEnv(t)
	e.fake.setPreviewCost(51)
	e.configure(true, []string{testUnit}, 50)
	e.svc.Tick(e.ctx)
	var paid int
	for _, r := range e.fake.snapshot() {
		if r.Path == "/v1/probe" {
			paid++
		}
	}
	st, err := e.svc.load(e.ctx)
	if err != nil || paid != 0 || st.LastStatus != "cap_reached" || st.Pending != nil || st.Spend.Credits != 0 {
		t.Fatalf("cap check: paid=%d state=%+v err=%v", paid, st, err)
	}
}

func TestProbeErrorsApplyRetryAndHoldPolicies(t *testing.T) {
	tests := []struct {
		name    string
		reply   fakeReply
		status  string
		pending bool
		hold    time.Duration
		farHold bool
		spent   int64
	}{
		{"429 retry-after", fakeReply{Status: http.StatusTooManyRequests, Body: `{"error":{"code":"rate_limited","details":{"retryable":true,"retry_after":17}}}`}, "rate_limited", false, 17 * time.Second, false, 0},
		{"401 unauthorized", fakeReply{Status: http.StatusUnauthorized}, "unauthorized", false, 0, true, 10},
		{"402 no balance", fakeReply{Status: http.StatusPaymentRequired, Body: `{"error":{"code":"no_balance","details":{}}}`}, "no_balance", false, 6 * time.Hour, false, 10},
		{"403 tier", fakeReply{Status: http.StatusForbidden, Body: `{"error":{"code":"tier_too_low","details":{}}}`}, "tier_too_low", false, 0, true, 10},
		{"400 no dpi", fakeReply{Status: http.StatusBadRequest, Body: `{"error":{"code":"no_dpi_on","details":{}}}`}, "no_dpi_on", false, time.Hour, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.configure(true, []string{testUnit}, 100)
			e.fake.enqueueProbe(tt.reply)
			e.svc.Tick(e.ctx)
			st, err := e.svc.load(e.ctx)
			if err != nil || st.LastStatus != tt.status || (st.Pending != nil) != tt.pending || st.Spend.Credits != tt.spent {
				t.Fatalf("state = %+v, %v", st, err)
			}
			if tt.hold > 0 {
				if got := st.HoldUntil.Sub(e.now); got < tt.hold-time.Second || got > tt.hold+time.Second {
					t.Fatalf("hold = %v, want about %v", got, tt.hold)
				}
			}
			if tt.farHold && !st.HoldUntil.After(e.now.Add(100*365*24*time.Hour)) {
				t.Fatalf("tier hold is not deferred until settings change: %v", st.HoldUntil)
			}
		})
	}
}

func TestSkippedUnavailableIsRecordedInSourceDetail(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 100)
	response := `{"outcome":"done","cost_credits":10,"refunded":0,"by_target":{"203.0.113.10:443":{"by_operator":{"example|krym|off":{"ok":true,"operator":"Example Mobile","region":"Krym","dpi":"off","icmp":{"ok":true},"tcp":{"ok":true}}}}},"skipped_unavailable":[{"op_key":"example|krym|off"}],"skipped_dpi_off":[]}`
	e.fake.enqueueProbe(fakeReply{Status: http.StatusOK, Body: response})
	e.svc.Tick(e.ctx)
	rows, err := e.st.RecentExtChecks(e.ctx, 10, Source)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].DetailJSON, `"unavailable:example|krym|off"`) {
		t.Fatalf("skipped unit detail = %+v, %v", rows, err)
	}
}

func TestLegMappingKeepsUncertainWhitelistAndTCPMetadata(t *testing.T) {
	message := "timeout"
	request, _ := json.Marshal(probeRequest{Targets: []string{"203.0.113.10:443"}, Operators: []string{"missing", "dns", "error", "dpi"}, DPI: "any"})
	p := &pending{Body: string(request), RoundAt: time.Now().Unix(), Targets: []targetRef{{Target: "203.0.113.10:443", NodeIDs: []string{"de1"}}}}
	result := probeResponse{Outcome: "done", ByTarget: map[string]probeTargetResult{"203.0.113.10:443": {ByOperator: map[string]probeLeg{
		"dns":   {OK: false, DNSFail: true, DPI: "off", ICMP: &probeCell{}, TCP: &probeCell{}},
		"error": {OK: false, Error: &message, DPI: "off", ICMP: &probeCell{}, TCP: &probeCell{}},
		"dpi":   {OK: true, DPI: "on", TCPIsTLS: true, ICMP: &probeCell{}, TCP: &probeCell{OK: true}},
	}}}}
	rows := rowsForResult(result, p, time.Now())
	if len(rows) != 1 {
		t.Fatalf("mapped rows = %+v", rows)
	}
	var detail countryreach.ExtDetail
	if err := json.Unmarshal([]byte(rows[0].DetailJSON), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Vantages) != 3 || detail.Vantages[0].Key != "dns" || detail.Vantages[0].State != countryreach.VantageStateUncertain ||
		detail.Vantages[1].Key != "dpi" || detail.Vantages[1].State != countryreach.VantageStateReach || !detail.Vantages[1].TCPOK || !detail.Vantages[1].TLS ||
		detail.Vantages[2].Key != "error" || detail.Vantages[2].State != countryreach.VantageStateUncertain {
		t.Fatalf("mapped vantages = %+v", detail.Vantages)
	}
}

func TestElevenTargetsAreChunkedAndPaidRequestsAreSpaced(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 1000)
	e.targets = e.targets[:0]
	ids := []string{"de1", "nl1", "fi1", "node4", "node5", "node6", "node7", "node8", "node9", "node10", "node11"}
	for i, id := range ids {
		e.targets = append(e.targets, health.CountryTarget{Key: id, IP: "203.0.113." + strconv.Itoa(i+1), Ports: []uint16{443}})
	}
	e.svc.Tick(e.ctx)
	e.svc.Tick(e.ctx)
	var posts []fakeRequest
	for _, r := range e.fake.snapshot() {
		if r.Path == "/v1/probe" {
			posts = append(posts, r)
		}
	}
	if len(posts) != 2 {
		t.Fatalf("paid calls = %+v", posts)
	}
	var first, second probeRequest
	if json.Unmarshal([]byte(posts[0].Body), &first) != nil || json.Unmarshal([]byte(posts[1].Body), &second) != nil ||
		len(first.Targets) != maxTargets || len(second.Targets) != 1 || posts[1].At.Sub(posts[0].At) < requestGap {
		t.Fatalf("chunks or spacing: first=%+v second=%+v elapsed=%v", first, second, posts[1].At.Sub(posts[0].At))
	}
	rows, err := e.st.RecentExtChecks(e.ctx, 10, Source)
	if err != nil || len(rows) != 11 {
		t.Fatalf("stored checks = %d, %v", len(rows), err)
	}
}

func TestSwitchingOffCancelsSentPendingRequestWithoutStoringRows(t *testing.T) {
	e := newTestEnv(t)
	st := settings{Enabled: true, Sealed: e.vlt.Seal([]byte(testKey), keyAAD), Units: []string{testUnit}, IntervalMin: 60,
		CapCredits: 100, Pending: &pending{Key: "pending-key", Body: `{"targets":["203.0.113.10:443"],"operators":["example|krym|off"],"dpi":"any"}`,
			Targets: []targetRef{{Target: "203.0.113.10:443", NodeIDs: []string{"de1"}}}, At: e.now.Unix(), RoundAt: e.now.Unix(), Reserve: 10, Sent: true}}
	if err := e.svc.saveConfigAndState(e.ctx, st); err != nil {
		t.Fatal(err)
	}
	enabled := false
	if _, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{Enabled: &enabled})); err != nil {
		t.Fatal(err)
	}
	requests := e.fake.snapshot()
	if len(requests) != 1 || requests[0].Path != "/v1/probe/cancel" || requests[0].IdempotencyKey != "pending-key" {
		t.Fatalf("cancel request = %+v", requests)
	}
	after, err := e.svc.load(e.ctx)
	rows, rowsErr := e.st.RecentExtChecks(e.ctx, 10, Source)
	if err != nil || rowsErr != nil || after.Enabled || after.Pending != nil || after.Spend.Credits != 10 || len(rows) != 0 {
		t.Fatalf("after off = %+v rows=%+v errors=%v/%v", after, rows, err, rowsErr)
	}
}

func TestReviewCapLoweredDuringPostIsNotReverted(t *testing.T) {
	e := newTestEnv(t)
	g := newGatedBsbord(t)
	s := e.serviceOn(g)
	e.svc = s
	e.configure(true, []string{testUnit}, 1000)
	done := make(chan struct{})
	go func() { s.Tick(e.ctx); close(done) }()
	<-g.entered
	lower := uint64(20)
	if _, err := (rpc{s}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{CapCredits: &lower})); err != nil {
		t.Fatal(err)
	}
	close(g.release)
	<-done
	st, err := s.load(e.ctx)
	rows, rowsErr := e.st.RecentExtChecks(e.ctx, 10, Source)
	if err != nil || rowsErr != nil || st.CapCredits != 20 || len(rows) != 1 {
		t.Fatalf("after cap update during POST: cap=%d rows=%d errors=%v/%v", st.CapCredits, len(rows), err, rowsErr)
	}
}

func TestReviewEdgeDisconnectDuringPostDoesNotRestoreTheIntegration(t *testing.T) {
	e := newTestEnv(t)
	g := newGatedBsbord(t)
	cron, admin := e.serviceOn(g), e.serviceOn(g)
	e.svc = cron
	e.configure(true, []string{testUnit}, 1000)
	done := make(chan struct{})
	go func() { cron.Tick(e.ctx); close(done) }()
	<-g.entered
	if _, err := (rpc{admin}).DisconnectBsbord(ownerContext(), connect.NewRequest(&adminv1.DisconnectBsbordRequest{})); err != nil {
		t.Fatal(err)
	}
	mid, err := admin.load(e.ctx)
	if err != nil || mid.Enabled || len(mid.Sealed) != 0 || len(mid.Units) != 0 {
		t.Fatalf("disconnect state = %+v, %v", mid, err)
	}
	close(g.release)
	<-done
	st, err := admin.load(e.ctx)
	rows, rowsErr := e.st.RecentExtChecks(e.ctx, 10, Source)
	if err != nil || rowsErr != nil || st.Enabled || len(st.Sealed) > 0 || len(st.Units) > 0 || len(rows) > 0 ||
		st.Spend.Month != mid.Spend.Month || st.Spend.Credits != mid.Spend.Credits {
		t.Fatalf("late POST answer restored state or spend: state=%+v rows=%d errors=%v/%v", st, len(rows), err, rowsErr)
	}
}

func TestReviewVPSPendingEraseDuringPostLeavesNoRows(t *testing.T) {
	e := newTestEnv(t)
	g := newGatedBsbord(t)
	cron, admin := e.serviceOn(g), e.serviceOn(g)
	e.svc = cron
	e.configure(true, []string{testUnit}, 1000)
	done := make(chan struct{})
	go func() { cron.Tick(e.ctx); close(done) }()
	<-g.entered
	newKey := "bsk_live_example_replaced_key"
	if _, err := (rpc{admin}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{Key: &newKey})); err != nil {
		t.Fatal(err)
	}
	mid, err := admin.load(e.ctx)
	if err != nil || !mid.Enabled || mid.Pending != nil || mid.Spend.Credits != 10 {
		t.Fatalf("pending erase state = %+v, %v", mid, err)
	}
	close(g.release)
	<-done
	st, err := admin.load(e.ctx)
	rows, rowsErr := e.st.RecentExtChecks(e.ctx, 10, Source)
	if err != nil || rowsErr != nil || !st.Enabled || st.Pending != nil || len(rows) != 0 ||
		st.Spend.Month != mid.Spend.Month || st.Spend.Credits != mid.Spend.Credits {
		t.Fatalf("late POST answer restored the erased pending call: state=%+v rows=%d errors=%v/%v", st, len(rows), err, rowsErr)
	}
}

func TestReviewCapLoweredWhilePendingStillReadsPaidResult(t *testing.T) {
	e := newTestEnv(t)
	e.now = time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)
	e.configure(true, []string{testUnit}, 100)
	e.fake.enqueueProbe(fakeReply{Status: 524, Body: `{"error":{"code":"gateway_timeout","details":{"retryable":true}}}`})
	e.svc.Tick(e.ctx)
	first, err := e.svc.load(e.ctx)
	if err != nil || first.Pending == nil || !first.Pending.Running {
		t.Fatalf("pending after timeout = %+v, %v", first.Pending, err)
	}
	lower := uint64(5)
	if _, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{CapCredits: &lower})); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(2 * time.Minute)
	e.svc.Tick(e.ctx)
	rows, err := e.st.RecentExtChecks(e.ctx, 10, Source)
	posts := 0
	for _, r := range e.fake.snapshot() {
		if r.Path == "/v1/probe" {
			posts++
		}
	}
	st, loadErr := e.svc.load(e.ctx)
	if err != nil || loadErr != nil || len(rows) != 1 || posts != 2 || st.Spend.Credits != 10 || st.Pending != nil {
		t.Fatalf("sent result was lost on resume: posts=%d rows=%d state=%+v errors=%v/%v", posts, len(rows), st, err, loadErr)
	}
}

func TestReviewThrottleOnResumeKeepsTheBilledIdempotencyKey(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 100)
	e.fake.enqueueProbe(
		fakeReply{Status: 524, Body: `{"error":{"code":"gateway_timeout","details":{"retryable":true}}}`},
		fakeReply{Status: http.StatusTooManyRequests, Body: `{"error":{"code":"rate_limited","details":{"retryable":true,"retry_after":1}}}`},
		fakeReply{Status: http.StatusOK},
	)
	for i := 0; i < 3; i++ {
		e.svc.Tick(e.ctx)
		e.now = e.now.Add(2 * time.Minute)
	}
	var keys []string
	for _, r := range e.fake.snapshot() {
		if r.Path == "/v1/probe" {
			keys = append(keys, r.IdempotencyKey)
		}
	}
	st, err := e.svc.load(e.ctx)
	rows, rowsErr := e.st.RecentExtChecks(e.ctx, 10, Source)
	if err != nil || rowsErr != nil || len(keys) != 3 || keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] || len(rows) != 1 || st.Spend.Credits != 10 {
		t.Fatalf("resume throttle changed its billed call: keys=%v state=%+v rows=%d errors=%v/%v", keys, st, len(rows), err, rowsErr)
	}
}

func TestFirstSendTransientAndBusyErrorsDoNotReserveCredits(t *testing.T) {
	tests := []struct {
		name    string
		reply   fakeReply
		pending bool
		sent    bool
		lastRun bool
		status  string
	}{
		{name: "503 first send", reply: fakeReply{Status: http.StatusServiceUnavailable, Body: `{"error":{"code":"unavailable","details":{"retryable":true,"retry_after":17}}}`}, status: "unavailable", lastRun: true},
		{name: "409 busy", reply: fakeReply{Status: http.StatusConflict, Body: `{"error":{"code":"busy","details":{"retryable":true,"retry_after":17}}}`}, status: "pending", pending: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.configure(true, []string{testUnit}, 100)
			e.fake.enqueueProbe(tt.reply)
			e.svc.Tick(e.ctx)
			st, err := e.svc.load(e.ctx)
			if err != nil || (st.Pending != nil) != tt.pending || st.Spend.Credits != 0 || st.LastStatus != tt.status ||
				(st.Pending != nil && st.Pending.Sent != tt.sent) || st.LastRun.IsZero() != !tt.lastRun {
				t.Fatalf("first-send failure state = %+v, %v", st, err)
			}
			if st.Pending != nil && (st.Pending.Reserve != 0 || st.Pending.Running || st.HoldUntil.Sub(e.now) < 16*time.Second) {
				t.Fatalf("busy response should keep an unreserved, unsent call with a retry hold: %+v", st.Pending)
			}
		})
	}
}

func TestSelectorsAreExactListedOperatorDistrictModeKeys(t *testing.T) {
	for _, bad := range []string{"*|krym|*", "|krym|off", "mts||off", "mts|krym", "mts|krym|*", "mts|krym|off|extra", "mts"} {
		if _, ok := cleanUnitKey(bad); ok {
			t.Errorf("cleanUnitKey(%q) accepted an invalid selector", bad)
		}
	}
	latin, okLatin := cleanUnitKey("mts|krym|off")
	cyrillic, okCyrillic := cleanUnitKey("mts|крым|off")
	if !okLatin || !okCyrillic || latin == cyrillic {
		t.Fatalf("valid listed district spellings were not preserved exactly: %q/%v %q/%v", latin, okLatin, cyrillic, okCyrillic)
	}

	e := newTestEnv(t)
	e.fake.setOperators(`{"units":[{"op_key":"mts|krym|off"},{"op_key":"mts|крым|off"}],"n_units":2,"n_probeable":2}`)
	key := testKey
	units := &adminv1.BsbordUnitSelection{OpKeys: []string{"mts|крым|off", "mts|krym|off"}}
	if _, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{Key: &key, Units: units})); err != nil {
		t.Fatalf("listed Latin and Cyrillic op_keys rejected: %v", err)
	}
	unknown := "mts|crimea|off"
	if _, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{Units: &adminv1.BsbordUnitSelection{OpKeys: []string{unknown}}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unlisted district spelling error = %v, want invalid argument", err)
	}
}

func TestBsbordStateAndGetAreUnavailableOutsideRussia(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 100)
	if err := e.st.SetSettings(e.ctx, map[string]string{settingCountry: "BY"}); err != nil {
		t.Fatal(err)
	}
	e.svc.cfg.Country = func(context.Context) string { return "BY" }
	state := e.svc.State(e.ctx)
	if state.On || state.MaxAge != 0 {
		t.Fatalf("Bsbord state outside RU = %+v", state)
	}
	if _, err := (rpc{e.svc}).GetBsbord(ownerContext(), connect.NewRequest(&adminv1.GetBsbordRequest{})); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("GetBsbord outside RU error = %v, want unavailable", err)
	}
	e.svc.Tick(e.ctx)
	if got := e.fake.snapshot(); len(got) != 0 {
		t.Fatalf("tick outside RU made requests: %+v", got)
	}
}

func TestBsbordStateIsSettingsOnlyAndExpiresAfterThreeIntervals(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 100)
	state := e.svc.State(e.ctx)
	if !state.On || state.MaxAge != 3*time.Hour {
		t.Fatalf("Bsbord source state = %+v, want on for three configured intervals", state)
	}
	requests := e.fake.snapshot()
	if len(requests) != 0 {
		t.Fatalf("State made upstream requests: %+v", requests)
	}
	if err := e.st.SetSettings(e.ctx, map[string]string{settingInterval: "120"}); err != nil {
		t.Fatal(err)
	}
	if state = e.svc.State(e.ctx); state.MaxAge != 6*time.Hour {
		t.Fatalf("Bsbord MaxAge after interval update = %v, want 6h", state.MaxAge)
	}
}

func TestPreviewWorksWhileOffAfterStepUp(t *testing.T) {
	e := newTestEnv(t)
	e.configure(false, []string{testUnit}, 100)
	steppedUp := false
	e.svc.cfg.StepUp = func(context.Context) error { steppedUp = true; return nil }
	result, err := (rpc{e.svc}).PreviewBsbord(ownerContext(), connect.NewRequest(&adminv1.PreviewBsbordRequest{}))
	if err != nil || !steppedUp || result.Msg.CostCredits != 10 {
		t.Fatalf("preview while off = %+v, step-up=%v, err=%v", result, steppedUp, err)
	}
	for _, r := range e.fake.snapshot() {
		if r.Path == "/v1/probe" {
			t.Fatal("preview while off sent a paid request")
		}
	}
}

func TestPreviewInvalidRequestDropsPendingAndWaitsOneInterval(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 100)
	e.fake.enqueuePreview(fakeReply{Status: http.StatusBadRequest, Body: `{"error":{"code":"invalid_request","details":{"retryable":false}}}`})
	e.svc.Tick(e.ctx)
	st, err := e.svc.load(e.ctx)
	if err != nil || st.Pending != nil || !st.LastRun.Equal(e.now) || st.LastStatus != "rejected" || st.HoldUntil.Sub(e.now) != time.Hour {
		t.Fatalf("invalid preview state = %+v, %v", st, err)
	}
	e.now = e.now.Add(time.Minute)
	e.svc.Tick(e.ctx)
	var previews, posts int
	for _, r := range e.fake.snapshot() {
		if r.Path == "/v1/probe/preview" {
			previews++
		}
		if r.Path == "/v1/probe" {
			posts++
		}
	}
	if previews != 1 || posts != 0 {
		t.Fatalf("invalid preview retried in a one-minute loop: previews=%d posts=%d", previews, posts)
	}
}

func TestDropPendingAlwaysSetsLastRun(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 100)
	st, err := e.svc.load(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	st.Pending = &pending{Key: "request-key", Reserve: 0}
	if err := e.svc.saveState(e.ctx, st); err != nil {
		t.Fatal(err)
	}
	e.svc.dropPending(e.ctx, st, e.now, "rejected", false, "request-key")
	after, err := e.svc.load(e.ctx)
	if err != nil || after.Pending != nil || !after.LastRun.Equal(e.now) {
		t.Fatalf("dropPending state = %+v, %v", after, err)
	}
}

func TestPreferredPortUsesExplicitTLSThen443ThenLowest(t *testing.T) {
	for _, tt := range []struct {
		name      string
		ports     []uint16
		tls, want uint16
	}{
		{"configured reality port", []uint16{80, 443, 2053}, 2053, 2053},
		{"fallback tls port", []uint16{80, 8443, 443}, 9443, 443},
		{"lowest tcp port", []uint16{8443, 80, 2053}, 0, 80},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := preferredPort(tt.ports, tt.tls); got != tt.want {
				t.Fatalf("preferredPort = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRowsUseEverySortedResponseUnitAndClassifyVantages(t *testing.T) {
	message := "network failure"
	tests := []struct {
		name string
		leg  probeLeg
		want string
	}{
		{"failed leg", probeLeg{OK: false, DPI: "off", ICMP: &probeCell{OK: true}, TCP: &probeCell{OK: true}}, countryreach.VantageStateUncertain},
		{"both dark", probeLeg{OK: true, DPI: "off", ICMP: &probeCell{}, TCP: &probeCell{}}, countryreach.VantageStateDark},
		{"icmp only", probeLeg{OK: true, DPI: "off", ICMP: &probeCell{OK: true}, TCP: &probeCell{}}, countryreach.VantageStateReach},
		{"tcp only", probeLeg{OK: true, DPI: "off", ICMP: &probeCell{}, TCP: &probeCell{OK: true}}, countryreach.VantageStateReach},
		{"dns failure", probeLeg{OK: true, DNSFail: true, DPI: "off", ICMP: &probeCell{}, TCP: &probeCell{}}, countryreach.VantageStateUncertain},
		{"error", probeLeg{OK: true, Error: &message, DPI: "off", ICMP: &probeCell{}, TCP: &probeCell{}}, countryreach.VantageStateUncertain},
		{"whitelist reaches", probeLeg{OK: true, DPI: "on", ICMP: &probeCell{}, TCP: &probeCell{OK: true}}, countryreach.VantageStateReach},
		{"whitelist dark is not counted dark", probeLeg{OK: true, DPI: "on", ICMP: &probeCell{}, TCP: &probeCell{}}, countryreach.VantageStateWhitelist},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mapLeg("unit", tt.leg, false).State; got != tt.want {
				t.Fatalf("mapLeg state = %q, want %q", got, tt.want)
			}
		})
	}

	at := time.Now().UTC()
	p := &pending{RoundAt: at.Unix(), Targets: []targetRef{{Target: "203.0.113.10:443", NodeIDs: []string{"de1"}}}}
	for _, tt := range []struct {
		name string
		legs map[string]probeLeg
		want string
	}{
		{"blocked", map[string]probeLeg{"mts|krym|off": tests[1].leg, "volna|krym|off": tests[1].leg}, countryreach.VerdictBlocked},
		{"partial", map[string]probeLeg{"mts|krym|off": tests[1].leg, "volna|krym|off": tests[2].leg}, countryreach.VerdictPartial},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rows := rowsForResult(probeResponse{Outcome: "done", ByTarget: map[string]probeTargetResult{p.Targets[0].Target: {ByOperator: tt.legs}}}, p, at)
			if len(rows) != 1 || rows[0].Verdict != tt.want {
				t.Fatalf("rows = %+v, want %s", rows, tt.want)
			}
		})
	}
}

// A TLS-checked TCP leg fails on any REALITY port; it is dark only when other units get pings from the node and it does not.
func TestTLSCheckedTCPFailureIsNoBlock(t *testing.T) {
	tlsDark := probeLeg{OK: true, DPI: "off", TCPIsTLS: true, ICMP: &probeCell{}, TCP: &probeCell{}}
	tlsPing := probeLeg{OK: true, DPI: "off", TCPIsTLS: true, ICMP: &probeCell{OK: true}, TCP: &probeCell{}}
	plainDark := probeLeg{OK: true, DPI: "off", ICMP: &probeCell{}, TCP: &probeCell{}}
	plainTCP := probeLeg{OK: true, DPI: "off", ICMP: &probeCell{}, TCP: &probeCell{OK: true}}
	at := time.Now().UTC()
	p := &pending{RoundAt: at.Unix(), Targets: []targetRef{{Target: "203.0.113.10:443", NodeIDs: []string{"de1"}}}}
	for _, tt := range []struct {
		name  string
		legs  map[string]probeLeg
		state string
		want  string
	}{
		{"pings elsewhere", map[string]probeLeg{"mts|крым|off": tlsDark, "volna|крым|off": tlsPing}, countryreach.VantageStateDark, countryreach.VerdictPartial},
		{"no pings anywhere", map[string]probeLeg{"mts|крым|off": tlsDark, "volna|крым|off": plainTCP}, countryreach.VantageStateUncertain, countryreach.VerdictOK},
		{"plain TCP fails too", map[string]probeLeg{"mts|крым|off": plainDark, "volna|крым|off": plainTCP}, countryreach.VantageStateDark, countryreach.VerdictPartial},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rows := rowsForResult(probeResponse{Outcome: "done", ByTarget: map[string]probeTargetResult{p.Targets[0].Target: {ByOperator: tt.legs}}}, p, at)
			var detail countryreach.ExtDetail
			if len(rows) != 1 || json.Unmarshal([]byte(rows[0].DetailJSON), &detail) != nil || len(detail.Vantages) != 2 {
				t.Fatalf("rows = %+v", rows)
			}
			if got := detail.Vantages[0].State; got != tt.state || rows[0].Verdict != tt.want {
				t.Fatalf("mts state = %q verdict = %q, want %q %q", got, rows[0].Verdict, tt.state, tt.want)
			}
		})
	}
}

func TestUnitNamesAreSpelledOut(t *testing.T) {
	v := mapLeg("winmobile|крым|off", probeLeg{OK: true, Operator: "winmobile", Region: "КРЫМ", DPI: "off", ICMP: &probeCell{OK: true}, TCP: &probeCell{}}, true)
	if v.Network != "Win Mobile" || v.Region != "Крым" {
		t.Fatalf("names = %q %q", v.Network, v.Region)
	}
	if v := mapLeg("x", probeLeg{Operator: "newop", Region: "ЦФО"}, false); v.Network != "newop" || v.Region != "ЦФО" {
		t.Fatalf("unknown names = %q %q", v.Network, v.Region)
	}
}

func TestCapIncludesThisMonthsPriorSpendAndMonthRolloverResetsIt(t *testing.T) {
	t.Run("prior spend reaches cap", func(t *testing.T) {
		e := newTestEnv(t)
		e.configure(true, []string{testUnit}, 100)
		st, _ := e.svc.load(e.ctx)
		st.Spend = spend{Month: e.now.UTC().Format("2006-01"), Credits: 95}
		if err := e.svc.saveState(e.ctx, st); err != nil {
			t.Fatal(err)
		}
		e.svc.Tick(e.ctx)
		after, _ := e.svc.load(e.ctx)
		for _, r := range e.fake.snapshot() {
			if r.Path == "/v1/probe" {
				t.Fatal("cap allowed paid request over monthly spend")
			}
		}
		if after.Spend.Credits != 95 || after.LastStatus != "cap_reached" {
			t.Fatalf("cap state = %+v", after)
		}
	})
	t.Run("new month resets spend", func(t *testing.T) {
		e := newTestEnv(t)
		e.now = time.Date(2026, 10, 31, 23, 59, 0, 0, time.UTC)
		e.configure(true, []string{testUnit}, 10)
		st, _ := e.svc.load(e.ctx)
		st.Spend = spend{Month: "2026-10", Credits: 10}
		if err := e.svc.saveState(e.ctx, st); err != nil {
			t.Fatal(err)
		}
		e.now = time.Date(2026, 11, 1, 0, 1, 0, 0, time.UTC)
		e.svc.Tick(e.ctx)
		after, _ := e.svc.load(e.ctx)
		if after.Spend.Month != "2026-11" || after.Spend.Credits != 10 || after.Pending != nil {
			t.Fatalf("rollover state = %+v", after)
		}
	})
}

func TestWriteOnlyKeyNeverAppearsInResponsesAuditOrLogs(t *testing.T) {
	e := newTestEnv(t)
	var logs bytes.Buffer
	e.svc.log = slog.New(slog.NewTextHandler(&logs, nil))
	key, enabled := testKey, true
	units := &adminv1.BsbordUnitSelection{OpKeys: []string{testUnit}}
	updated, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{Key: &key, Enabled: &enabled, Units: units}))
	if err != nil {
		t.Fatal(err)
	}
	got, err := (rpc{e.svc}).GetBsbord(ownerContext(), connect.NewRequest(&adminv1.GetBsbordRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	updateJSON, _ := json.Marshal(updated)
	getJSON, _ := json.Marshal(got)
	var params string
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT params FROM audit WHERE action = 'bsbord_update' ORDER BY id DESC LIMIT 1`).Scan(&params); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updateJSON), key) || strings.Contains(string(getJSON), key) || strings.Contains(params, key) || strings.Contains(logs.String(), key) {
		t.Fatalf("write-only key escaped: update=%s get=%s params=%s logs=%s", updateJSON, getJSON, params, logs.String())
	}
}

func TestSkippedResponseTargetsAndExecutorDPIOffItemsAreRecorded(t *testing.T) {
	e := newTestEnv(t)
	e.configure(true, []string{testUnit}, 100)
	response := `{"outcome":"done","cost_credits":10,"by_target":{"203.0.113.10:443":{"by_operator":{"example|krym|off":{"ok":true,"dpi":"off","icmp":{"ok":true},"tcp":{"ok":true}}}}},"skipped":["203.0.113.11:443"],"skipped_dpi_off":[{"target":"203.0.113.10:443","operator":"Example Mobile","region":"Krym","reason":"dpi off"}]}`
	e.fake.enqueueProbe(fakeReply{Status: http.StatusOK, Body: response})
	e.svc.Tick(e.ctx)
	rows, err := e.st.RecentExtChecks(e.ctx, 10, Source)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].DetailJSON, "target:203.0.113.11:443") ||
		!strings.Contains(rows[0].DetailJSON, "dpi_off:203.0.113.10:443:Example Mobile:Krym:dpi off") {
		t.Fatalf("skipped response details = %+v, %v", rows, err)
	}
}

// Any save on the card lifts a hold the owner can fix (cap, key, plan, balance): a save that fixes nothing gets the hold back on
// the next tick, after a free preview. A server's rate-limit hold is never lifted by a setting.
func TestOwnerChangeLiftsAFixableHold(t *testing.T) {
	e := newTestEnv(t)
	e.fake.setPreviewCost(51)
	e.configure(true, []string{testUnit}, 50)
	e.svc.Tick(e.ctx)
	if st, _ := e.svc.load(e.ctx); st.LastStatus != "cap_reached" || !st.HoldUntil.After(e.now) {
		t.Fatalf("setup: %+v", st)
	}
	interval := uint32(120)
	if _, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{IntervalMin: &interval})); err != nil {
		t.Fatal(err)
	}
	e.svc.Tick(e.ctx)
	if st, _ := e.svc.load(e.ctx); st.LastStatus != "cap_reached" || !st.HoldUntil.After(e.now) {
		t.Fatalf("a save that fixes nothing must hold again on the next tick: %+v", st)
	}
	capCredits := uint64(100)
	if _, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{CapCredits: &capCredits})); err != nil {
		t.Fatal(err)
	}
	st, _ := e.svc.load(e.ctx)
	if st.LastStatus != "idle" || !st.HoldUntil.IsZero() {
		t.Fatalf("raising the cap kept the hold: %+v", st)
	}
	e.svc.Tick(e.ctx)
	var paid int
	for _, r := range e.fake.snapshot() {
		if r.Path == "/v1/probe" {
			paid++
		}
	}
	if paid != 1 {
		t.Fatalf("after raising the cap the next tick paid %d rounds, want 1", paid)
	}

	if err := e.svc.saveState(e.ctx, settings{LastStatus: "rate_limited", HoldUntil: e.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	capCredits = 200
	if _, err := (rpc{e.svc}).UpdateBsbord(ownerContext(), connect.NewRequest(&adminv1.UpdateBsbordRequest{CapCredits: &capCredits})); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.svc.load(e.ctx); st.LastStatus != "rate_limited" || !st.HoldUntil.After(e.now) {
		t.Fatalf("a setting lifted the server's rate-limit hold: %+v", st)
	}
}
