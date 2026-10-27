package cheburcheck

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/httpheaders"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const testToken = "own_probe_secret_0123456789"

type testEnv struct {
	t     *testing.T
	st    *store.Store
	vault *vault.Vault
	fake  *fakeSite
	svc   *Service
	logs  *bytes.Buffer
	mu    sync.Mutex
	now   time.Time
	steps int
}

func newEnv(t *testing.T, probeTimeout time.Duration) *testEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "cheburcheck.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	v, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeSite()
	t.Cleanup(fake.close)
	e := &testEnv{t: t, st: st, vault: v, fake: fake, logs: &bytes.Buffer{},
		now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
	e.svc, err = New(Config{Store: st, Vault: v, StepUp: func(context.Context) error { e.steps++; return nil },
		Address: func(_ context.Context, n store.NodeRow) (netip.Addr, bool) {
			ip, err := netip.ParseAddr(n.Address)
			return ip, err == nil
		}, HTTPClient: fake.srv.Client(), BaseURL: fake.srv.URL,
		ProbeTimeout: probeTimeout, Log: quietLogger(e.logs), Now: func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now }})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func quietLogger(w *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func (e *testEnv) advance(d time.Duration) {
	e.mu.Lock()
	e.now = e.now.Add(d)
	e.mu.Unlock()
}

func (e *testEnv) setCountry(country string) {
	e.t.Helper()
	if err := e.st.SetSettings(context.Background(), map[string]string{settingCountry: country}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) time() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func testOwner() context.Context {
	return auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
}

func (e *testEnv) update(req *adminv1.UpdateCheburcheckRequest) (*adminv1.CheburcheckSettings, error) {
	e.t.Helper()
	resp, err := (rpc{e.svc}).UpdateCheburcheck(testOwner(), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg.Settings, nil
}

func (e *testEnv) enablePublic() {
	e.t.Helper()
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{Enabled: ptr(true), PublicEnabled: ptr(true)}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) addNode(name, address string) string {
	e.t.Helper()
	ctx := context.Background()
	now := e.time()
	id := store.NewID("nod_")
	if _, err := e.st.CreateEnrollment(ctx, &store.NodeRow{ID: id, Name: name, Address: address, CountryCode: "RU"}, "",
		[]byte("enrollment-"+name), "adm_test", now, now.Add(time.Hour)); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.st.W.ExecContext(ctx, `UPDATE node SET state = 'active', last_seen_at = ? WHERE id = ?`, now.Unix(), id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func ptr[T any](v T) *T { return &v }

func TestDefaultBaseURLAndTestOnlyStatusRequest(t *testing.T) {
	e := newEnv(t, 0)
	if got := e.svc.client().base; got != e.fake.srv.URL {
		t.Fatalf("configured BaseURL = %q, want httptest URL", got)
	}

	svc, err := New(Config{Store: e.st, Vault: e.vault, StepUp: func(context.Context) error { return nil }, BaseURL: "",
		Address: func(context.Context, store.NodeRow) (netip.Addr, bool) { return netip.Addr{}, false }})
	if err != nil {
		t.Fatal(err)
	}
	if got := svc.client().base; got != DefaultBaseURL {
		t.Fatalf("empty BaseURL resolved to %q, want %q", got, DefaultBaseURL)
	}

	resp, err := (rpc{e.svc}).TestCheburcheck(testOwner(), connect.NewRequest(&adminv1.TestCheburcheckRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Msg.Reachable || resp.Msg.Version != "1.5.4" || resp.Msg.V4Count != 9 || resp.Msg.DomainCount != 7 {
		t.Fatalf("test status = %+v", resp.Msg)
	}
	reqs := e.fake.snapshot()
	if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != "/api/v1/status" || reqs[0].query != "" ||
		reqs[0].accept != "application/json" || reqs[0].authorization != "" || reqs[0].userAgent != httpheaders.MistgateUserAgent {
		t.Fatalf("status request = %+v", reqs)
	}
}

func TestTickOffMakesNoRequestAndPublicIntervalCannotReplayCache(t *testing.T) {
	e := newEnv(t, 0)
	e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.fake.snapshot()) != 0 {
		t.Fatalf("disabled Tick sent requests: %+v", e.fake.snapshot())
	}
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{IntervalMin: ptr(uint32(179))}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("public interval below the cache floor = %v", err)
	}
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{OwnIntervalMin: ptr(uint32(29))}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("own interval below 30 minutes = %v", err)
	}
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{IntervalMin: ptr(uint32(180)), OwnIntervalMin: ptr(uint32(30))}); err != nil {
		t.Fatalf("minimum intervals were refused: %v", err)
	}
}

func TestRunStoresJudgedVantagesAndUsesMistgateUserAgent(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.probeResponses = []fakeReply{{body: "event: started\ndata: {\"online_probes\":3}\n\n" +
		"event: result\ndata: {\"probe_id\":17,\"region\":\"Example Region\",\"provider\":\"Example ISP\",\"asn\":\"AS64496\",\"verdicts\":[\"ok\"]}\n\n" +
		"event: result\ndata: {\"probe_id\":18,\"region\":\"Sample Region\",\"provider\":\"Sample Network\",\"asn\":64497,\"verdicts\":[\"tspu_block\"]}\n\n" +
		"event: result\ndata: {\"probe_id\":19,\"region\":\"Other Region\",\"provider\":\"Other Network\",\"verdicts\":[\"uncertain\"]}\n\n" +
		"event: done\ndata: {\"status\":\"done\",\"online_probes\":3}\n\n"}}
	e.enablePublic()
	id := e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 1 {
		t.Fatalf("checks = %+v, %v", rows, err)
	}
	if rows[0].NodeID != id || rows[0].Verdict != countryreach.VerdictPartial {
		t.Fatalf("row = %+v", rows[0])
	}
	var detail countryreach.ExtDetail
	if err := json.Unmarshal([]byte(rows[0].DetailJSON), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Vantages) != 3 || detail.Vantages[0].Key != "probe:17" || detail.Vantages[0].State != countryreach.VantageStateReach ||
		detail.Vantages[1].State != countryreach.VantageStateDark || detail.Vantages[2].State != countryreach.VantageStateUncertain || detail.Vantages[0].ASN != "64496" {
		t.Fatalf("vantages = %+v", detail.Vantages)
	}
	for _, req := range e.fake.snapshot() {
		if req.userAgent != httpheaders.MistgateUserAgent {
			t.Errorf("request User-Agent = %q", req.userAgent)
		}
		if req.path == "/api/v1/check" {
			query, err := url.ParseQuery(req.query)
			if err != nil || len(query) != 1 || query.Get("target") != "203.0.113.10" {
				t.Errorf("check query = %q, %v", req.query, err)
			}
		}
	}
}

func TestStateIsRussiaOnlyAndExposesIndependentFreshness(t *testing.T) {
	e := newEnv(t, 0)
	e.setCountry("ZZ")
	state := e.svc.State(context.Background())
	if state.Unavailable != "country" || state.On {
		t.Fatalf("disabled non-Russia state = %+v", state)
	}
	e.setCountry("RU")
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{Enabled: ptr(true), Token: ptr(testToken)}); err != nil {
		t.Fatal(err)
	}
	state = e.svc.State(context.Background())
	if !state.PublicOn || !state.OwnOn || state.PublicMaxAge != 9*time.Hour || state.OwnMaxAge != 3*time.Hour {
		t.Fatalf("source state = %+v", state)
	}
	if sources := state.Sources(); len(sources) != 2 || sources[0].Source != SourcePublic || sources[1].Source != SourceOwn || !sources[0].On || !sources[1].On {
		t.Fatalf("source entries = %+v", sources)
	}
	e.setCountry("ZZ")
	state = e.svc.State(context.Background())
	if state.Unavailable != "country" || state.On || state.PublicOn || state.OwnOn {
		t.Fatalf("non-Russia state = %+v", state)
	}
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.fake.snapshot()) != 0 {
		t.Fatalf("unavailable country made requests: %+v", e.fake.snapshot())
	}
}

func TestEdgeDueCallUsesSharedLease(t *testing.T) {
	e := newEnv(t, 0)
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	e.svc.cfg.Edge = true
	var leaseName string
	var leaseTTL time.Duration
	e.svc.cfg.RoundLease = func(_ context.Context, name string, ttl time.Duration) bool {
		leaseName, leaseTTL = name, ttl
		return false
	}
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantTTL := max(callSpacing, e.svc.probeTimeout()+5*time.Second)
	if leaseName != "cheburcheck-call" || leaseTTL != wantTTL || leaseTTL <= e.svc.probeTimeout() || len(e.fake.snapshot()) != 0 {
		t.Fatalf("edge lease = %q/%s, requests %v", leaseName, leaseTTL, e.fake.snapshot())
	}
}

func TestOwnProbeTokenIsSealedWriteOnlyAndOnlyQueryParameter(t *testing.T) {
	e := newEnv(t, 0)
	_, err := e.update(&adminv1.UpdateCheburcheckRequest{Enabled: ptr(true), PublicEnabled: ptr(false), Token: ptr(testToken)})
	if err != nil {
		t.Fatal(err)
	}
	e.addNode("nl1", "203.0.113.11")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var probe recordedRequest
	for _, req := range e.fake.snapshot() {
		if strings.HasPrefix(req.path, "/api/v1/probe/") {
			probe = req
		}
	}
	if probe.path == "" || probe.query != "token="+testToken || probe.authorization != "" || probe.accept != "text/event-stream" {
		t.Fatalf("own probe request = %+v", probe)
	}
	settings, err := e.st.SettingValues(context.Background(), []string{settingToken})
	if err != nil || settings[settingToken] == "" || strings.Contains(settings[settingToken], testToken) {
		t.Fatalf("stored token was not sealed: %q, %v", settings[settingToken], err)
	}
	response, err := (rpc{e.svc}).GetCheburcheck(testOwner(), connect.NewRequest(&adminv1.GetCheburcheckRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(response.Msg.Settings)
	if err != nil || strings.Contains(string(public), testToken) {
		t.Fatalf("token appeared in API response %s: %v", public, err)
	}
	rows, err := e.st.R.QueryContext(context.Background(), `SELECT params FROM audit WHERE action LIKE 'cheburcheck_%'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var params string
		if err := rows.Scan(&params); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(params, testToken) {
			t.Fatalf("token appeared in audit params %q", params)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.logs.String(), testToken) {
		t.Fatalf("token appeared in logs %q", e.logs.String())
	}
}

func TestToken404StopsOwnChecksUntilTokenChanges(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.probeResponses = []fakeReply{{status: http.StatusNotFound}}
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{Enabled: ptr(true), PublicEnabled: ptr(false), Token: ptr(testToken)}); err != nil {
		t.Fatal(err)
	}
	e.addNode("fi1", "203.0.113.12")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	kv, err := e.st.SettingValues(context.Background(), []string{settingStatus, settingTokenUnknown})
	if err != nil || kv[settingStatus] != "token_unknown" || kv[settingTokenUnknown] != "1" {
		t.Fatalf("token failure state = %v, %v", kv, err)
	}
	before := len(e.fake.snapshot())
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(e.fake.snapshot()); got != before {
		t.Fatalf("unknown token caused a retry: requests %d -> %d", before, got)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourceOwn)
	if err != nil || len(rows) != 0 {
		t.Fatalf("token rejection stored rows %+v: %v", rows, err)
	}
	if strings.Contains(e.logs.String(), testToken) {
		t.Fatalf("rejected token appeared in logs %q", e.logs.String())
	}
}

func Test429BackoffDoublesAndStoresNoRow(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.probeResponses = []fakeReply{{status: http.StatusTooManyRequests}, {status: http.StatusTooManyRequests}}
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	kv, err := e.st.SettingValues(context.Background(), []string{settingStatus, settingHoldUntil, settingBackoff})
	if err != nil || kv[settingStatus] != "rate_limited" || kv[settingBackoff] != "4" {
		t.Fatalf("first 429 state = %v, %v", kv, err)
	}
	hold, _ := strconv.ParseInt(kv[settingHoldUntil], 10, 64)
	if hold != e.time().Add(2*time.Minute).Unix() {
		t.Fatalf("first hold until = %d, want two minutes", hold)
	}
	before := len(e.fake.snapshot())
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.fake.snapshot()) != before {
		t.Fatal("request was sent during the 429 hold")
	}
	e.advance(2 * time.Minute)
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	kv, err = e.st.SettingValues(context.Background(), []string{settingBackoff})
	if err != nil || kv[settingBackoff] != "8" {
		t.Fatalf("second 429 backoff = %v, %v", kv, err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 0 {
		t.Fatalf("429 stored rows %+v: %v", rows, err)
	}
}

func Test400IsIPProbesOffAndHeld(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.probeResponses = []fakeReply{{status: http.StatusBadRequest}}
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	kv, err := e.st.SettingValues(context.Background(), []string{settingStatus, settingHoldUntil})
	if err != nil || kv[settingStatus] != "ip_probes_off" {
		t.Fatalf("400 state = %v, %v", kv, err)
	}
	hold, _ := strconv.ParseInt(kv[settingHoldUntil], 10, 64)
	if hold != e.time().Add(probesOffHold).Unix() {
		t.Fatalf("400 hold until = %d", hold)
	}
	before := len(e.fake.snapshot())
	e.advance(time.Minute)
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(e.fake.snapshot()) != before {
		t.Fatal("400 caused a retry loop before the hold expired")
	}
}

func Test401GetsOneFreshCheck(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.probeResponses = []fakeReply{{status: http.StatusUnauthorized}, {body: completeFrames()}}
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	checks, probes := e.fake.counts()
	if checks != 2 || probes != 2 {
		t.Fatalf("401 refresh made %d checks and %d probes, want 2 each", checks, probes)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 1 || rows[0].Verdict != countryreach.VerdictPartial {
		t.Fatalf("fresh check result = %+v, %v", rows, err)
	}
}

func TestIncompleteStreamKeepsReceivedResults(t *testing.T) {
	e := newEnv(t, 150*time.Millisecond)
	e.fake.probeResponses = []fakeReply{{body: "event: started\ndata: {\"online_probes\":3}\n\n" +
		"event: result\ndata: {\"probe_id\":21,\"provider\":\"Example ISP\",\"verdicts\":[\"ok\"]}\n\n" +
		"event: result\ndata: {\"probe_id\":22,\"provider\":\"Sample ISP\",\"verdicts\":[\"tspu_block\"]}\n\n", hold: true}}
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 1 || rows[0].Verdict != countryreach.VerdictPartial {
		t.Fatalf("incomplete stream rows = %+v, %v", rows, err)
	}
	var detail countryreach.ExtDetail
	if err := json.Unmarshal([]byte(rows[0].DetailJSON), &detail); err != nil || len(detail.Vantages) != 2 {
		t.Fatalf("incomplete stream detail = %+v, %v", detail, err)
	}
}

func TestEmptyIncompleteStreamStoresUnknownTimeout(t *testing.T) {
	e := newEnv(t, 150*time.Millisecond)
	e.fake.probeResponses = []fakeReply{{body: "event: started\ndata: {\"online_probes\":2}\n\n", hold: true}}
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 1 || rows[0].Verdict != countryreach.VerdictUnknown {
		t.Fatalf("empty incomplete stream rows = %+v, %v", rows, err)
	}
	var detail countryreach.ExtDetail
	if err := json.Unmarshal([]byte(rows[0].DetailJSON), &detail); err != nil || detail.Reason != "timeout" || len(detail.Vantages) != 0 {
		t.Fatalf("empty incomplete stream detail = %+v, %v", detail, err)
	}
}

func TestSiteCallsAreSpacedThirtySecondsAcrossNodes(t *testing.T) {
	e := newEnv(t, 0)
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	e.addNode("nl1", "203.0.113.11")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := len(e.fake.snapshot())
	if first != 2 {
		t.Fatalf("first Tick sent %d requests, want check + probe", first)
	}
	e.advance(29 * time.Second)
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(e.fake.snapshot()); got != first {
		t.Fatalf("second site call was only 29 seconds later: %d -> %d", first, got)
	}
	e.advance(time.Second)
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(e.fake.snapshot()); got != first+2 {
		t.Fatalf("call at 30 seconds added %d requests, want check + probe", got-first)
	}
}

func TestSwitchOffMidStreamStoresNothing(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.probeResponses = []fakeReply{{body: "event: started\ndata: {\"online_probes\":2}\n\n" +
		"event: result\ndata: {\"probe_id\":31,\"provider\":\"Example ISP\",\"verdicts\":[\"ok\"]}\n\n", hold: true}}
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	done := make(chan error, 1)
	go func() { done <- e.svc.Tick(context.Background()) }()
	select {
	case <-e.fake.probeStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("probe stream did not start")
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Tick after switching off = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight probe did not stop")
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 0 {
		t.Fatalf("switch-off stored rows %+v: %v", rows, err)
	}
}

func TestPermanentProbeFailureIsHeldAndDoesNotStarveOtherNodes(t *testing.T) {
	e := newEnv(t, 0)
	var mu sync.Mutex
	var checked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/check":
			target := r.URL.Query().Get("target")
			mu.Lock()
			checked = append(checked, target)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"id":"`+target+`"}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/probe/"):
			if strings.TrimPrefix(r.URL.Path, "/api/v1/probe/") == "203.0.113.10" {
				http.Error(w, `{"code":403}`, http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(w, completeFrames())
		}
	}))
	defer srv.Close()
	e.svc.cfg.BaseURL, e.svc.cfg.HTTPClient = srv.URL, srv.Client()
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	e.addNode("nl1", "203.0.113.11")
	for i := 0; i < 10; i++ {
		if err := e.svc.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
		e.advance(30 * time.Second)
	}
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, target := range checked {
		found = found || target == "203.0.113.11"
	}
	if !found {
		t.Fatalf("nl1 was never checked; targets: %v", checked)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Verdict == countryreach.VerdictUnknown {
			assertReason(t, row, "http_403")
			return
		}
	}
	t.Fatal("403 did not store an unknown row")
}

func TestPublicIntervalStartsAfterProbeCompletes(t *testing.T) {
	e := newEnv(t, 0)
	var mu sync.Mutex
	cache := map[string]time.Time{}
	replays := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/check":
			_, _ = io.WriteString(w, `{"id":"`+r.URL.Query().Get("target")+`"}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/probe/"):
			target := strings.TrimPrefix(r.URL.Path, "/api/v1/probe/")
			mu.Lock()
			if at, ok := cache[target]; ok && e.time().Sub(at) < 3*time.Hour {
				replays++
			} else {
				e.advance(10 * time.Second)
				cache[target] = e.time()
			}
			mu.Unlock()
			_, _ = io.WriteString(w, completeFrames())
		}
	}))
	defer srv.Close()
	e.svc.cfg.BaseURL, e.svc.cfg.HTTPClient = srv.URL, srv.Client()
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	start := e.time()
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 361; i++ {
		e.mu.Lock()
		e.now = start.Add(time.Duration(i) * 30 * time.Second)
		e.mu.Unlock()
		if err := e.svc.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 5, SourcePublic)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rows: %d, cache replays: %d", len(rows), replays)
	if replays != 0 {
		t.Fatalf("the second public run replayed the three-hour cache %d times", replays)
	}
}

func TestUnreadableOwnTokenLeavesPublicLaneOn(t *testing.T) {
	e := newEnv(t, 0)
	e.enablePublic()
	other, err := vault.New(append(make([]byte, vault.KeySize-1), 1))
	if err != nil {
		t.Fatal(err)
	}
	sealed := base64.StdEncoding.EncodeToString(other.Seal([]byte(testToken), tokenAAD))
	if err := e.st.SetSettings(context.Background(), map[string]string{settingToken: sealed}); err != nil {
		t.Fatal(err)
	}
	e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	requests := e.fake.snapshot()
	if len(requests) == 0 {
		t.Fatalf("public lane made no request; logs: %s", e.logs.String())
	}
	for _, request := range requests {
		if strings.Contains(request.query, testToken) {
			t.Fatalf("unopenable token was sent: %+v", request)
		}
	}
}

func TestCheck404DoesNotMarkOwnTokenUnknown(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.checkResponses = []fakeReply{{status: http.StatusNotFound}}
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{Enabled: ptr(true), PublicEnabled: ptr(false), Token: ptr(testToken)}); err != nil {
		t.Fatal(err)
	}
	e.addNode("fi1", "203.0.113.12")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	kv, err := e.st.SettingValues(context.Background(), []string{settingTokenUnknown, settingStatus})
	if err != nil || kv[settingTokenUnknown] == "1" || kv[settingStatus] != "unreachable" {
		t.Fatalf("check 404 state = %v, %v", kv, err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourceOwn)
	if err != nil || len(rows) != 1 {
		t.Fatalf("check 404 rows = %+v, %v", rows, err)
	}
	assertReason(t, rows[0], "http_404")
}

func TestMissingCheckIDStoresUnknownAndAdvances(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.checkResponses = []fakeReply{{body: `{"id":null}`}}
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	e.addNode("nl1", "203.0.113.11")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	kv, err := e.st.SettingValues(context.Background(), []string{settingStatus})
	if err != nil || kv[settingStatus] != "unreachable" {
		t.Fatalf("missing id status = %v, %v", kv, err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 1 {
		t.Fatalf("missing id rows = %+v, %v", rows, err)
	}
	assertReason(t, rows[0], "no_id")
	e.advance(30 * time.Second)
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if checks, _ := e.fake.counts(); checks != 2 {
		t.Fatalf("the next node did not get a check after no_id; total checks = %d, want 2", checks)
	}
}

func TestRepeated401AndServerErrorStoreUnknown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		probes   []fakeReply
		want     string
		checks   int
		probeNum int
	}{
		{name: "second 401", probes: []fakeReply{{status: http.StatusUnauthorized}, {status: http.StatusUnauthorized}}, want: "http_401", checks: 2, probeNum: 2},
		{name: "server error", probes: []fakeReply{{status: http.StatusServiceUnavailable}}, want: "http_5xx", checks: 1, probeNum: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, 0)
			e.fake.probeResponses = tc.probes
			e.enablePublic()
			e.addNode("de1", "203.0.113.10")
			if err := e.svc.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
			if err != nil || len(rows) != 1 || rows[0].Verdict != countryreach.VerdictUnknown {
				t.Fatalf("failure rows = %+v, %v", rows, err)
			}
			assertReason(t, rows[0], tc.want)
			kv, err := e.st.SettingValues(context.Background(), []string{settingStatus})
			if err != nil || kv[settingStatus] != "unreachable" {
				t.Fatalf("failure status = %v, %v", kv, err)
			}
			checks, probes := e.fake.counts()
			if checks != tc.checks || probes != tc.probeNum {
				t.Fatalf("failure made %d checks and %d probes, want %d and %d", checks, probes, tc.checks, tc.probeNum)
			}
		})
	}
}

func TestNetworkFailureStoresUnknown(t *testing.T) {
	e := newEnv(t, 0)
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	e.svc.cfg.HTTPClient = &http.Client{Transport: cheburRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("network down")
	})}
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 1 || rows[0].Verdict != countryreach.VerdictUnknown {
		t.Fatalf("network failure rows = %+v, %v", rows, err)
	}
	assertReason(t, rows[0], "error")
	kv, err := e.st.SettingValues(context.Background(), []string{settingStatus})
	if err != nil || kv[settingStatus] != "unreachable" {
		t.Fatalf("network failure status = %v, %v", kv, err)
	}
}

func TestZeroOnlineProbesStoresNoProbeReason(t *testing.T) {
	e := newEnv(t, 0)
	e.fake.probeResponses = []fakeReply{{body: "event: started\ndata: {\"online_probes\":0}\n\n" +
		"event: done\ndata: {\"status\":\"done\",\"online_probes\":0}\n\n"}}
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 1 || rows[0].Verdict != countryreach.VerdictUnknown {
		t.Fatalf("zero online probes rows = %+v, %v", rows, err)
	}
	assertReason(t, rows[0], "no_probe")
}

func TestUpdateAndDisconnectRequireStepUp(t *testing.T) {
	e := newEnv(t, 0)
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{Enabled: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	if e.steps != 1 {
		t.Fatalf("UpdateCheburcheck StepUp calls = %d, want 1", e.steps)
	}
	if _, err := (rpc{e.svc}).DisconnectCheburcheck(testOwner(), connect.NewRequest(&adminv1.DisconnectCheburcheckRequest{})); err != nil {
		t.Fatal(err)
	}
	if e.steps != 2 {
		t.Fatalf("Update and Disconnect StepUp calls = %d, want 2", e.steps)
	}
}

func TestNonOwnerCannotChangeCheburcheck(t *testing.T) {
	e := newEnv(t, 0)
	admin := auth.WithAdmin(context.Background(), store.Admin{ID: "adm_helper", Role: store.RoleHelper})
	if _, err := (rpc{e.svc}).UpdateCheburcheck(admin, connect.NewRequest(&adminv1.UpdateCheburcheckRequest{Enabled: ptr(true)})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-owner UpdateCheburcheck error = %v", err)
	}
	if _, err := (rpc{e.svc}).DisconnectCheburcheck(admin, connect.NewRequest(&adminv1.DisconnectCheburcheckRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-owner DisconnectCheburcheck error = %v", err)
	}
	if e.steps != 0 {
		t.Fatalf("non-owner reached StepUp %d times", e.steps)
	}
}

func TestDisconnectDeletesRowsAndForgetsToken(t *testing.T) {
	e := newEnv(t, 0)
	if _, err := e.update(&adminv1.UpdateCheburcheckRequest{Enabled: ptr(true), PublicEnabled: ptr(true), Token: ptr(testToken)}); err != nil {
		t.Fatal(err)
	}
	e.addNode("de1", "203.0.113.10")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.advance(callSpacing)
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic, SourceOwn)
	if err != nil || len(rows) != 2 {
		t.Fatalf("checks before disconnect = %+v, %v", rows, err)
	}
	resp, err := (rpc{e.svc}).DisconnectCheburcheck(testOwner(), connect.NewRequest(&adminv1.DisconnectCheburcheckRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Settings.Enabled || resp.Msg.Settings.HasToken {
		t.Fatalf("disconnect settings = %+v", resp.Msg.Settings)
	}
	rows, err = e.st.RecentExtChecks(context.Background(), 1, SourcePublic, SourceOwn)
	if err != nil || len(rows) != 0 {
		t.Fatalf("checks after disconnect = %+v, %v", rows, err)
	}
	kv, err := e.st.SettingValues(context.Background(), []string{settingToken})
	if err != nil || kv[settingToken] != "" {
		t.Fatalf("stored token after disconnect = %q, %v", kv[settingToken], err)
	}
}

func TestStateSourcesDoNotReadRows(t *testing.T) {
	e := newEnv(t, 0)
	e.enablePublic()
	id := e.addNode("de1", "203.0.113.10")
	if err := e.st.InsertExtChecks(context.Background(), []store.ExtCheck{{NodeID: id, Source: SourcePublic, At: e.time(), Verdict: "unknown", DetailJSON: `{}`}}); err != nil {
		t.Fatal(err)
	}
	state := e.svc.State(context.Background())
	if !state.LastRun.IsZero() || state.LastVerdict != "" {
		t.Fatalf("settings-only State contains result metadata: %+v", state)
	}
	resp, err := (rpc{e.svc}).GetCheburcheck(testOwner(), connect.NewRequest(&adminv1.GetCheburcheckRequest{}))
	if err != nil || resp.Msg.Settings.LastRunUnix != e.time().Unix() || resp.Msg.Settings.LastVerdict != "unknown" {
		t.Fatalf("admin latest result = %+v, %v", resp, err)
	}
}

func TestTickResolvesOnlyTheChosenNode(t *testing.T) {
	e := newEnv(t, 0)
	e.enablePublic()
	e.addNode("de1", "203.0.113.10")
	e.addNode("nl1", "203.0.113.11")
	resolve := e.svc.cfg.Address
	resolved := 0
	e.svc.cfg.Address = func(ctx context.Context, n store.NodeRow) (netip.Addr, bool) {
		resolved++
		return resolve(ctx, n)
	}
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resolved != 1 {
		t.Fatalf("Tick resolved %d node addresses, want only the chosen node", resolved)
	}
}

func TestStoredRowsTriggerOnChangeAndMissingRowsDoNot(t *testing.T) {
	e := newEnv(t, 0)
	e.enablePublic()
	changes := 0
	e.svc.cfg.OnChange = func(context.Context) { changes++ }
	e.addNode("de1", "10.0.0.1")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if changes != 0 {
		t.Fatalf("OnChange ran without a stored row: %d", changes)
	}
	e.addNode("nl1", "203.0.113.11")
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if changes != 1 {
		t.Fatalf("OnChange calls after storing a row = %d, want 1", changes)
	}
}

func TestStoreResultRejectsStaleGeneration(t *testing.T) {
	e := newEnv(t, 0)
	id := e.addNode("de1", "203.0.113.10")
	changes := 0
	e.svc.cfg.OnChange = func(context.Context) { changes++ }
	gen := e.svc.generation()
	e.svc.stop()
	stored, err := e.svc.storeResult(context.Background(), gen, settings{}, store.ExtCheck{
		NodeID: id, Source: SourcePublic, At: e.time(), Verdict: countryreach.VerdictUnknown, DetailJSON: `{"reason":"error"}`,
	}, false)
	if err != nil || stored {
		t.Fatalf("stale storeResult = %v, %v; want no stored row", stored, err)
	}
	rows, err := e.st.RecentExtChecks(context.Background(), 1, SourcePublic)
	if err != nil || len(rows) != 0 || changes != 0 {
		t.Fatalf("stale result rows = %+v, %v; OnChange calls = %d", rows, err, changes)
	}
}

func assertReason(t *testing.T, row store.ExtCheck, want string) {
	t.Helper()
	var detail countryreach.ExtDetail
	if err := json.Unmarshal([]byte(row.DetailJSON), &detail); err != nil || detail.Reason != want {
		t.Fatalf("row reason = %q, want %q (row %+v, err %v)", detail.Reason, want, row, err)
	}
}

type cheburRoundTripFunc func(*http.Request) (*http.Response, error)

func (f cheburRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
