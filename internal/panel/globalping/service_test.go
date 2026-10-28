package globalping

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const secretToken = "gp_secret_token_0123456789abcdef"

type testEnv struct {
	t       *testing.T
	st      *store.Store
	v       *vault.Vault
	fake    *fakeGlobalping
	svc     *Service
	logs    *bytes.Buffer
	steps   int
	country string
	mu      sync.Mutex
	now     time.Time
}

func newEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	v, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	e := &testEnv{t: t, st: st, v: v, fake: newFakeGlobalping(t), logs: &bytes.Buffer{}, now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), country: "RU"}
	e.svc, err = New(Config{Store: st, Vault: v, StepUp: func(context.Context) error { e.steps++; return nil }, BaseURL: e.fake.srv.URL, HTTPClient: e.fake.client(),
		PollDelay: time.Millisecond, Log: slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:     func() time.Time { e.mu.Lock(); defer e.mu.Unlock(); return e.now },
		Country: func(context.Context) string { return e.country }})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *testEnv) advance(d time.Duration) { e.mu.Lock(); e.now = e.now.Add(d); e.mu.Unlock() }

func ownerCtx() context.Context {
	return auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
}

func (e *testEnv) update(req *adminv1.UpdateGlobalpingRequest) (*adminv1.GlobalpingSettings, error) {
	e.t.Helper()
	r, err := rpc{e.svc}.UpdateGlobalping(ownerCtx(), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return r.Msg.Settings, nil
}

func ptr[T any](v T) *T { return &v }

func (e *testEnv) enable(token string) {
	e.t.Helper()
	req := &adminv1.UpdateGlobalpingRequest{Enabled: ptr(true)}
	if token != "" {
		req.Token = ptr(token)
	}
	if _, err := e.update(req); err != nil {
		e.t.Fatal(err)
	}
}

func (e *testEnv) source() health.CountrySource {
	e.t.Helper()
	cc, src := e.svc.Country(context.Background())
	if !cc.Enabled || src == nil {
		e.t.Fatalf("the integration is off: %+v", cc)
	}
	return src
}

func round(targets ...health.CountryTarget) health.CountryRound {
	return health.CountryRound{Country: "RU", Probes: 3, ControlHost: "ya.ru", ControlPort: 443, Targets: targets}
}

func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%q: %v", s, err)
	}
	return m
}

func TestFakeGlobalpingProbeListAndLocationFilters(t *testing.T) {
	f := newFakeGlobalping(t)
	f.probes = []fakeProbe{
		{Country: "RU", Network: "Example ISP", City: "Tomsk", ASN: 64496, Tags: []string{"eyeball-network"}, ControlOK: true},
		{Country: "RU", Network: "Vimpelcom", City: "Ufa", ASN: 3216, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Vimpelcom", City: "Moscow", ASN: 3216, Eyeball: true, ControlOK: true},
		{Country: "DE", Network: "Vimpelcom", City: "Ufa", ASN: 3216, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Datacenter", City: "Ufa", ASN: 3216, Tags: []string{"datacenter-network"}, ControlOK: true},
	}
	c := &client{hc: f.client(), base: f.srv.URL}
	probes, err := c.probes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != len(f.probes) || probes[0].Location.Country != "RU" || probes[0].Location.ASN != 64496 || probes[0].Location.City != "Tomsk" ||
		!slices.Contains(probes[0].Tags, "eyeball-network") {
		t.Fatalf("probe list = %+v", probes)
	}

	created, err := c.create(context.Background(), measurementRequest{Type: "ping", Target: "example.invalid", Locations: []location{
		{Country: "RU", ASN: 3216, City: "Ufa", Tags: []string{"eyeball-network"}, Limit: 1},
		{Country: "RU", ASN: 3216, Tags: []string{"eyeball-network"}, Limit: 2},
		{Country: "DE", ASN: 65000, Limit: 1}, // no match is skipped
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.defs[created.ID].probes; !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("picked probes = %v, want location matches [1 2] with Set dedupe", got)
	}
	_, err = c.create(context.Background(), measurementRequest{Type: "ping", Target: "example.invalid", Locations: []location{
		{Country: "RU", ASN: 3216, City: "Missing city", Tags: []string{"eyeball-network"}, Limit: 1},
		{Country: "DE", ASN: 65000, Limit: 1},
	}})
	var apiErr *apiError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("all-empty locations error = %v, want 422", err)
	}
}

func TestControlLocationLimitsStayWithinBudgetWithFourOnlineOperators(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	e.fake.probes = []fakeProbe{
		{Country: "RU", Network: "MTS", City: "Exampleville", ASN: 8359, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Beeline", City: "Sampletown", ASN: 3216, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Rostelecom", City: "Teston", ASN: 12389, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "ER-Telecom", City: "Example City", ASN: 51570, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Example ISP", City: "Example Borough", ASN: 64496, Eyeball: true, ControlOK: true},
	}
	r := round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"})
	r.Want = []health.CountryWant{
		{Name: "MTS", ASNs: []uint32{8359}},
		{Name: "Beeline", ASNs: []uint32{3216}},
		{Name: "MegaFon", ASNs: []uint32{31133}},
		{Name: "Rostelecom", ASNs: []uint32{12389}},
		{Name: "T2", ASNs: []uint32{15378}},
		{Name: "ER-Telecom", ASNs: []uint32{51570}},
	}
	if _, err := e.source().Measure(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	locations := jsonOf(t, e.fake.posts()[0].Body)["locations"].([]any)
	total := 0
	for _, raw := range locations {
		loc := raw.(map[string]any)
		limit := 1
		if n, ok := loc["limit"].(float64); ok {
			limit = int(n)
		}
		total += limit
	}
	if total != r.Probes || len(locations) != 3 {
		t.Fatalf("control location limits sum to %d across %d locations; want budget %d across two pins and one generic location", total, len(locations), r.Probes)
	}
}

func TestControlPlanSkipsDataCenterAndForeignOperatorProbes(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	e.fake.probes = []fakeProbe{
		{Country: "RU", Network: "MTS", City: "Exampleville", ASN: 8359, Tags: []string{"datacenter-network"}, ControlOK: true},
		{Country: "KZ", Network: "MTS", City: "Sampletown", ASN: 8359, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Example ISP", City: "Example Borough", ASN: 64496, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Example ISP Two", City: "Teston", ASN: 64497, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Example ISP Three", City: "Example City", ASN: 64498, Eyeball: true, ControlOK: true},
	}
	r := round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"})
	r.Want = []health.CountryWant{{Name: "MTS", ASNs: []uint32{8359}}}
	obs, err := e.source().Measure(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	locations := jsonOf(t, e.fake.posts()[0].Body)["locations"].([]any)
	if len(locations) != 1 || len(obs.NoProbe) != 1 || obs.NoProbe[0] != "MTS" {
		t.Fatalf("locations = %v, no_probe = %v; want only the generic location and MTS in no_probe", locations, obs.NoProbe)
	}
}

func TestNamedControlLocationsAndNoProbe(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	f := e.fake
	f.probes = []fakeProbe{
		{Country: "RU", Network: "Example ISP", City: "Tomsk", ASN: 64496, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Beeline", City: "Ufa", ASN: 3216, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Rostelecom", City: "Kostroma", ASN: 12389, Eyeball: true, ControlOK: true},
		{Country: "RU", Network: "Example DC", City: "Moscow", ASN: 49505, ControlOK: true},
	}
	r := round(health.CountryTarget{Key: "de1", IP: "203.0.113.10", Ports: []uint16{443}})
	r.Want = []health.CountryWant{
		{Name: "MTS", ASNs: []uint32{8359}},
		{Name: "Beeline", ASNs: []uint32{3216}},
		{Name: "MegaFon", ASNs: []uint32{31133}},
		{Name: "Rostelecom", ASNs: []uint32{12389}},
		{Name: "T2", ASNs: []uint32{15378}},
		{Name: "ER-Telecom", ASNs: []uint32{51570}},
	}
	obs, err := e.source().Measure(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"MTS", "MegaFon", "T2", "ER-Telecom"}; !slices.Equal(obs.NoProbe, want) {
		t.Fatalf("no_probe = %v, want %v", obs.NoProbe, want)
	}
	posts := f.posts()
	if len(posts) < 2 {
		t.Fatalf("measurements = %d, want a control and node measurement", len(posts))
	}
	locations := jsonOf(t, posts[0].Body)["locations"].([]any)
	if len(locations) != 3 {
		t.Fatalf("control locations = %v, want two named picks and one generic", locations)
	}
	var total int
	for _, raw := range locations {
		loc := raw.(map[string]any)
		total += int(loc["limit"].(float64))
	}
	if total != r.Probes {
		t.Fatalf("sum of control location limits = %d, want round budget %d", total, r.Probes)
	}
	first, second, generic := locations[0].(map[string]any), locations[1].(map[string]any), locations[2].(map[string]any)
	if first["asn"] != float64(3216) || first["city"] != "Ufa" || first["limit"] != float64(1) ||
		second["asn"] != float64(12389) || second["city"] != "Kostroma" || second["limit"] != float64(1) ||
		generic["country"] != "RU" || generic["limit"] != float64(1) || !slices.Contains(generic["tags"].([]any), "eyeball-network") {
		t.Fatalf("control locations = %v", locations)
	}
	if len(obs.Probes) != 3 {
		t.Fatalf("control selected %d probes, want named probes plus the generic probe", len(obs.Probes))
	}
}

func TestProbeListFailureFallsBackToGenericCountryLocation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testEnv, *source)
	}{
		{name: "500", setup: func(e *testEnv, _ *source) { e.fake.probesCode = http.StatusInternalServerError }},
		{name: "timeout", setup: func(e *testEnv, s *source) {
			e.fake.probesDelay = 2 * time.Second
			s.probeTimeout = time.Second
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.enable(secretToken)
			src := e.source().(*source)
			tc.setup(e, src)
			r := round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"})
			r.Want = []health.CountryWant{{Name: "MTS", ASNs: []uint32{8359}}}
			obs, err := src.Measure(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if len(obs.NoProbe) != 0 {
				t.Fatalf("no_probe after listing failure = %v, want empty", obs.NoProbe)
			}
			var probesRequests int
			for _, req := range e.fake.requests() {
				if req.Method == http.MethodGet && req.Path == "/v1/probes" {
					probesRequests++
				}
			}
			if probesRequests != 1 {
				t.Fatalf("GET /v1/probes count = %d, want 1", probesRequests)
			}
			locations := jsonOf(t, e.fake.posts()[0].Body)["locations"].([]any)
			if len(locations) != 1 {
				t.Fatalf("fallback control locations = %v, want one generic location", locations)
			}
			loc := locations[0].(map[string]any)
			if loc["country"] != "RU" || loc["limit"] != float64(r.Probes) || !slices.Contains(loc["tags"].([]any), "eyeball-network") {
				t.Fatalf("fallback control location = %v", loc)
			}
			log := e.logs.String()
			if count := strings.Count(log, `msg="globalping: probe list"`); count != 1 {
				t.Fatalf("probe-list warning count = %d, want 1:\n%s", count, log)
			}
			if strings.Contains(log, e.fake.srv.URL) || strings.Contains(log, secretToken) {
				t.Fatalf("probe-list warning contains a URL or token:\n%s", log)
			}
		})
	}
}

func TestListedOperatorCountsAsAskedWhenProbeIsNotSelectedAtK2(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	f := e.fake
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/probes" {
			list := []map[string]any{{"location": map[string]any{"country": "RU", "asn": 8359, "city": "Exampleville", "network": "MTS"}, "tags": []string{"eyeball-network"}}}
			for _, p := range f.probes {
				list = append(list, map[string]any{"location": map[string]any{"country": p.Country, "asn": p.ASN, "city": p.City, "network": p.Network}, "tags": p.tags()})
			}
			_ = json.NewEncoder(w).Encode(list)
			return
		}
		f.serve(w, r)
	}))
	defer srv.Close()
	src := e.source().(*source)
	src.c = &client{hc: srv.Client(), base: srv.URL}
	src.poll = time.Millisecond
	r := round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"})
	r.Probes = 2
	r.Want = []health.CountryWant{{Name: "MTS", ASNs: []uint32{8359}}}
	obs, err := src.Measure(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Probes) >= r.Probes {
		t.Fatalf("selected probes = %d, want the listed but unavailable MTS pin to leave the round below K=2", len(obs.Probes))
	}
	if len(obs.Asked) != 1 || obs.Asked[0] != "MTS" {
		t.Fatalf("asked = %v, want MTS even though its probe was not selected", obs.Asked)
	}
}

func TestEmptyWantDoesNotRequestProbeList(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	if _, err := e.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"})); err != nil {
		t.Fatal(err)
	}
	for _, req := range e.fake.requests() {
		if req.Method == http.MethodGet && req.Path == "/v1/probes" {
			t.Fatal("GET /v1/probes ran with an empty Want")
		}
	}
	posts := e.fake.posts()
	if len(posts) < 2 {
		t.Fatalf("measurements = %d, want control plus node", len(posts))
	}
	locations := jsonOf(t, posts[0].Body)["locations"].([]any)
	if len(locations) != 1 {
		t.Fatalf("control locations = %v, want one generic country location", locations)
	}
	loc := locations[0].(map[string]any)
	if loc["country"] != "RU" || loc["limit"] != float64(3) || !slices.Contains(loc["tags"].([]any), "eyeball-network") ||
		loc["asn"] != nil || loc["city"] != nil {
		t.Fatalf("empty-Want control location = %v", loc)
	}
}

func TestProbeEyeballTagsAndCompleteMeasurements(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	f := e.fake
	f.probes[0].Eyeball = false
	f.probes[0].Tags = []string{"eyeball-network"}
	f.icmp["203.0.113.10"], f.tcp["203.0.113.10:443"] = true, true
	r := round(health.CountryTarget{Key: "de1", IP: "203.0.113.10", Ports: []uint16{443}})
	obs, err := e.source().Measure(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs.Probes) == 0 || !obs.Probes[0].Eyeball || !obs.Probes[0].Nodes["de1"].Complete {
		t.Fatalf("probe tags or completed tests = %+v", obs.Probes)
	}

	e = newEnv(t)
	e.enable("")
	e.fake.failTCP = map[int]bool{0: true}
	obs, err = e.source().Measure(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Probes[0].Nodes["de1"].Answered || obs.Probes[0].Nodes["de1"].Complete {
		t.Fatalf("a TCP failure inside the probe was counted complete: %+v", obs.Probes[0].Nodes["de1"])
	}

	e = newEnv(t)
	e.enable("")
	e.fake.keepNodeTCP = true
	src := e.source().(*source)
	src.poll = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	obs, err = src.Measure(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Probes[0].Nodes["de1"].Answered || obs.Probes[0].Nodes["de1"].Complete {
		t.Fatalf("a missing TCP measurement was counted complete: %+v", obs.Probes[0].Nodes["de1"])
	}
}

func TestOffByDefault(t *testing.T) {
	e := newEnv(t)
	r, err := rpc{e.svc}.GetGlobalping(ownerCtx(), connect.NewRequest(&adminv1.GetGlobalpingRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	s := r.Msg.Settings
	if s.Enabled || s.IntervalMin != 15 || s.HasToken {
		t.Fatalf("defaults = %+v", s)
	}
	if cc, src := e.svc.Country(context.Background()); cc.Enabled || src != nil {
		t.Fatalf("Country() = %+v, %v; want off and no source", cc, src)
	}
	if e.fake.count() != 0 {
		t.Fatal("a request was made")
	}
}

func TestMeasureSharesTheProbesAndPolls(t *testing.T) {
	e := newEnv(t)
	e.enable(secretToken)
	f := e.fake
	f.icmp["203.0.113.10"], f.icmp["203.0.113.30"], f.icmp["198.51.100.7"] = true, true, true
	f.tcp["203.0.113.10:443"], f.tcp["198.51.100.7:443"] = true, true // 8443 is closed

	obs, err := e.source().Measure(context.Background(), round(
		health.CountryTarget{Key: "de1", IP: "203.0.113.10", Ports: []uint16{443, 8443}},
		health.CountryTarget{Key: "plain", IP: "203.0.113.30"},
		health.CountryTarget{Key: "named", IP: "198.51.100.7", Ports: []uint16{443}}))
	if err != nil {
		t.Fatal(err)
	}
	posts := f.posts()
	// the control, then ICMP + 2 TCP for de1, ICMP for plain, ICMP + TCP for named
	if len(posts) != 1+3+1+2 {
		t.Fatalf("%d measurements created: %+v", len(posts), posts)
	}
	ctl := jsonOf(t, posts[0].Body)
	loc := ctl["locations"].([]any)[0].(map[string]any)
	opts := ctl["measurementOptions"].(map[string]any)
	if ctl["type"] != "ping" || ctl["target"] != "ya.ru" || loc["country"] != "RU" || loc["limit"] != 3.0 || loc["tags"].([]any)[0] != "eyeball-network" ||
		opts["protocol"] != "TCP" || opts["port"] != 443.0 {
		t.Fatalf("control request = %s", posts[0].Body)
	}
	for _, p := range posts[1:] {
		b := jsonOf(t, p.Body)
		if b["locations"] != "m1" { // the id of the control: the same probes for every node
			t.Fatalf("a node measurement does not reuse the control's probes: %s", p.Body)
		}
		if tg := b["target"]; tg == "ya.ru" {
			t.Fatalf("the control was measured twice: %s", p.Body)
		}
	}
	for _, p := range f.requests() {
		if p.Auth != "Bearer "+secretToken || p.UA == "" || !strings.HasPrefix(p.UA, "mistgate-panel") {
			t.Fatalf("headers of %s %s: auth set %v ua %q", p.Method, p.Path, p.Auth != "", p.UA)
		}
	}
	gets := 0
	for _, r := range f.requests() {
		if r.Method == "GET" {
			gets++
		}
	}
	if gets < 2*7 { // every measurement was read until it finished: at least once while running, once finished
		t.Fatalf("only %d reads for 7 measurements", gets)
	}

	if !obs.Eyeball || len(obs.Probes) != 3 {
		t.Fatalf("observed = %+v", obs)
	}
	first := obs.Probes[0]
	if first.Network != "Rostelecom" || first.ASN != 12389 || first.City != "Moscow" || !first.Online || !first.ControlOK {
		t.Fatalf("probe 0 = %+v", first)
	}
	de := first.Nodes["de1"]
	if !de.Answered || !de.ICMPOK || de.ICMPMs != 23 || len(de.TCP) != 2 || !de.TCP[0].OK || de.TCP[0].Port != 443 || de.TCP[1].OK || de.TCP[1].Port != 8443 {
		t.Fatalf("de1 = %+v", de)
	}
	if n := first.Nodes["named"]; !n.ICMPOK || len(n.TCP) != 1 || !n.TCP[0].OK {
		t.Fatalf("named = %+v", n)
	}
	if n := first.Nodes["plain"]; len(n.TCP) != 0 || !n.ICMPOK {
		t.Fatalf("plain = %+v", n)
	}
	// nothing but the control site and the targets was ever named in a request
	for _, p := range posts {
		b := jsonOf(t, p.Body)
		switch b["target"] {
		case "ya.ru", "203.0.113.10", "203.0.113.30", "198.51.100.7":
		default:
			t.Fatalf("unexpected target %v", b["target"])
		}
	}
}

func TestFallsBackToAnyNetworkWhenTooFewHomeProbes(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	f := e.fake
	f.probes[1].Eyeball, f.probes[2].Eyeball = false, false // one home probe only
	f.icmp["203.0.113.10"] = true
	obs, err := e.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"}))
	if err != nil {
		t.Fatal(err)
	}
	posts := f.posts()
	if len(posts) != 3 { // the home-network control, the any-network control, one ICMP
		t.Fatalf("%d measurements: %+v", len(posts), posts)
	}
	if jsonOf(t, posts[0].Body)["locations"].([]any)[0].(map[string]any)["tags"] == nil || jsonOf(t, posts[1].Body)["locations"].([]any)[0].(map[string]any)["tags"] != nil {
		t.Fatalf("controls = %s / %s", posts[0].Body, posts[1].Body)
	}
	if jsonOf(t, posts[2].Body)["locations"] != "m2" || obs.Eyeball || len(obs.Probes) != 3 {
		t.Fatalf("the nodes should use the second control's probes: %s, eyeball %v, %d probes", posts[2].Body, obs.Eyeball, len(obs.Probes))
	}
	for _, p := range f.requests() {
		if p.Auth != "" {
			t.Fatal("an Authorization header without a token")
		}
	}
}

func TestProbesThatFailTheControlAreReportedAndTooFewSkipTheNodes(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	f := e.fake
	f.icmp["203.0.113.10"] = true
	f.probes[2].ControlOK = false
	obs, err := e.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"}))
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Probes[0].ControlOK || !obs.Probes[1].ControlOK || obs.Probes[2].ControlOK || len(obs.Probes) != 3 {
		t.Fatalf("control flags = %+v", obs.Probes)
	}

	// two of the three fail the control: no node measurement is spent, the caller gets the probes and judges "unknown"
	e = newEnv(t)
	e.enable("")
	e.fake.probes[1].ControlOK, e.fake.probes[2].ControlOK = false, false
	obs, err = e.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range e.fake.posts() {
		if jsonOf(t, p.Body)["target"] != "ya.ru" {
			t.Fatalf("a node was measured with too few probes: %s", p.Body)
		}
	}
	if len(obs.Probes) == 0 || len(obs.Probes[0].Nodes) != 0 {
		t.Fatalf("observed = %+v", obs)
	}
}

func TestPartialProbeFailuresLeaveThoseProbesUnanswered(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	f := e.fake
	f.icmp["203.0.113.10"] = true
	f.failNode[1], f.failNode[2] = "offline", "failed"
	obs, err := e.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"}))
	if err != nil {
		t.Fatal(err)
	}
	if !obs.Probes[0].Nodes["de1"].Answered || obs.Probes[1].Nodes["de1"].Answered || obs.Probes[2].Nodes["de1"].Answered {
		t.Fatalf("answered = %v %v %v, want true false false", obs.Probes[0].Nodes["de1"].Answered, obs.Probes[1].Nodes["de1"].Answered, obs.Probes[2].Nodes["de1"].Answered)
	}
}

func TestRateLimitOnCreateAsksTheCallerToWait(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	e.fake.rateLimited = true
	_, err := e.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"}))
	var limit *health.CountryLimitError
	if !errors.As(err, &limit) || limit.Reset != 30*time.Minute {
		t.Fatalf("err = %v, want a limit error with the 1800 s of X-RateLimit-Reset", err)
	}
	if e.fake.count() != 1 {
		t.Fatalf("%d requests after a 429, want the one", e.fake.count())
	}
}

func TestHourlyLimitFollowsTheToken(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	if l := e.source().HourlyLimit(); l != 250 {
		t.Errorf("limit without a token = %d", l)
	}
	if _, err := e.update(&adminv1.UpdateGlobalpingRequest{Token: ptr(secretToken)}); err != nil {
		t.Fatal(err)
	}
	if l := e.source().HourlyLimit(); l != 500 {
		t.Errorf("limit with a token = %d", l)
	}
}

func TestTheTokenIsWriteOnlyAndNeverLogged(t *testing.T) {
	e := newEnv(t)
	s, err := e.update(&adminv1.UpdateGlobalpingRequest{Enabled: ptr(true), Token: ptr("  " + secretToken + "  ")})
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasToken {
		t.Fatal("has_token is false")
	}
	got, _ := rpc{e.svc}.GetGlobalping(ownerCtx(), connect.NewRequest(&adminv1.GetGlobalpingRequest{}))
	b, _ := json.Marshal(got.Msg)
	if strings.Contains(string(b), secretToken) {
		t.Fatalf("the token came back: %s", b)
	}
	// stored sealed
	for _, key := range []string{settingToken, settingEnabled, settingInterval} {
		if v, _ := e.st.Setting(context.Background(), key); strings.Contains(v, secretToken) {
			t.Fatalf("%s holds the token in clear: %q", key, v)
		}
	}
	// audit rows say the token changed, never what it is
	rows, err := e.st.ListAudit(context.Background(), "", 0, 20)
	if err == nil {
		for _, r := range rows {
			if strings.Contains(r.Params, secretToken) {
				t.Fatalf("audit holds the token: %+v", r)
			}
		}
	}
	// failing calls: a server error, a refused test
	e.fake.limitsCode = 401
	if _, err := (rpc{e.svc}).TestGlobalping(ownerCtx(), connect.NewRequest(&adminv1.TestGlobalpingRequest{})); err == nil {
		t.Fatal("a refused token passed the test")
	} else if strings.Contains(err.Error(), secretToken) {
		t.Fatalf("the error holds the token: %v", err)
	}
	e.fake.srv.Close()
	_, err = e.source().Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"}))
	if err == nil || strings.Contains(err.Error(), secretToken) {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(e.logs.String(), secretToken) {
		t.Fatalf("the log holds the token:\n%s", e.logs.String())
	}
	// a bad shape is refused without being echoed
	_, err = e.update(&adminv1.UpdateGlobalpingRequest{Token: ptr("has a space")})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || strings.Contains(err.Error(), "has a space") {
		t.Fatalf("err = %v", err)
	}
}

func TestUpdateValidatesAndNeedsStepUp(t *testing.T) {
	e := newEnv(t)
	for name, req := range map[string]*adminv1.UpdateGlobalpingRequest{
		"interval low": {IntervalMin: ptr(uint32(9))},
		"interval big": {IntervalMin: ptr(uint32(1441))},
	} {
		if _, err := e.update(req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if e.steps != 0 {
		t.Errorf("a refused request asked for a step-up (%d)", e.steps)
	}
	s, err := e.update(&adminv1.UpdateGlobalpingRequest{IntervalMin: ptr(uint32(30))})
	if err != nil || s.IntervalMin != 30 || s.Enabled || e.steps != 1 {
		t.Fatalf("update = %+v, %v, steps %d", s, err, e.steps)
	}
	// the country is not this integration's: it comes from the shared setting, whatever it is when the round starts
	e.enable("")
	e.country = "KZ"
	if cc, _ := e.svc.Country(context.Background()); cc.Country != "KZ" || cc.Interval != 30*time.Minute || !cc.Enabled {
		t.Fatalf("Country() = %+v", cc)
	}
}

func TestOnlyTheOwnerMayCallIt(t *testing.T) {
	e := newEnv(t)
	for name, ctx := range map[string]context.Context{
		"helper":    auth.WithAdmin(context.Background(), store.Admin{ID: "adm_h", Role: store.RoleHelper}),
		"token":     auth.WithAdmin(context.Background(), store.Admin{ID: "token:tok_1", Role: store.RoleOwner}),
		"mcp":       auth.WithAdmin(context.Background(), store.Admin{ID: "mcp:tok_1", Role: store.RoleOwner}),
		"anonymous": context.Background(),
	} {
		if _, err := (rpc{e.svc}).GetGlobalping(ctx, connect.NewRequest(&adminv1.GetGlobalpingRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Get err = %v", name, err)
		}
		if _, err := (rpc{e.svc}).UpdateGlobalping(ctx, connect.NewRequest(&adminv1.UpdateGlobalpingRequest{Enabled: ptr(true)})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Update err = %v", name, err)
		}
		if _, err := (rpc{e.svc}).TestGlobalping(ctx, connect.NewRequest(&adminv1.TestGlobalpingRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Test err = %v", name, err)
		}
		if _, err := (rpc{e.svc}).DisconnectGlobalping(ctx, connect.NewRequest(&adminv1.DisconnectGlobalpingRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Disconnect err = %v", name, err)
		}
	}
	if e.fake.count() != 0 {
		t.Fatal("a refused call reached Globalping")
	}
}

// The test call is GET /v1/limits and nothing else: no measurement, no address, no body.
func TestTestCallOnlyReadsTheLimits(t *testing.T) {
	e := newEnv(t)
	r, err := (rpc{e.svc}).TestGlobalping(ownerCtx(), connect.NewRequest(&adminv1.TestGlobalpingRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if m := r.Msg; !m.Reachable || m.TokenUsed || m.Limit != 250 || m.Remaining != 245 || m.ResetS != 3000 || m.Credits != 7 {
		t.Fatalf("test = %+v", m)
	}
	reqs := e.fake.requests()
	if len(reqs) != 1 || reqs[0].Method != "GET" || reqs[0].Path != "/v1/limits" || reqs[0].Body != "" || reqs[0].Auth != "" {
		t.Fatalf("requests = %+v", reqs)
	}
	if _, err := (rpc{e.svc}).TestGlobalping(ownerCtx(), connect.NewRequest(&adminv1.TestGlobalpingRequest{})); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("a second test at once: %v", err)
	}
	e.advance(11 * time.Second)
	e.enable(secretToken)
	r, err = (rpc{e.svc}).TestGlobalping(ownerCtx(), connect.NewRequest(&adminv1.TestGlobalpingRequest{}))
	if err != nil || !r.Msg.TokenUsed || r.Msg.Limit != 500 {
		t.Fatalf("with a token: %+v, %v", r, err)
	}
	e.advance(11 * time.Second)
	e.fake.limitsCode = 401
	if _, err := (rpc{e.svc}).TestGlobalping(ownerCtx(), connect.NewRequest(&adminv1.TestGlobalpingRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition ||
		!strings.Contains(err.Error(), "globalping_unauthorized") {
		t.Fatalf("a refused token: %v", err)
	}
	e.advance(11 * time.Second)
	e.fake.limitsCode = 500
	if _, err := (rpc{e.svc}).TestGlobalping(ownerCtx(), connect.NewRequest(&adminv1.TestGlobalpingRequest{})); connect.CodeOf(err) != connect.CodeUnavailable ||
		!strings.Contains(err.Error(), "globalping_unreachable") {
		t.Fatalf("a server error: %v", err)
	}
	for _, r := range e.fake.requests() {
		if r.Method == "POST" {
			t.Fatal("the test created a measurement")
		}
	}
}

func TestDisconnectSwitchesOffAndForgetsTheToken(t *testing.T) {
	e := newEnv(t)
	e.enable(secretToken)
	if _, err := e.update(&adminv1.UpdateGlobalpingRequest{IntervalMin: ptr(uint32(20))}); err != nil {
		t.Fatal(err)
	}
	r, err := (rpc{e.svc}).DisconnectGlobalping(ownerCtx(), connect.NewRequest(&adminv1.DisconnectGlobalpingRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if s := r.Msg.Settings; s.Enabled || s.HasToken || s.IntervalMin != 20 {
		t.Fatalf("after disconnect = %+v", s)
	}
	if cc, src := e.svc.Country(context.Background()); cc.Enabled || src != nil {
		t.Fatalf("Country() = %+v, %v", cc, src)
	}
	if _, err := (rpc{e.svc}).DisconnectGlobalping(ownerCtx(), connect.NewRequest(&adminv1.DisconnectGlobalpingRequest{})); err != nil {
		t.Fatalf("a second disconnect: %v", err) // idempotent
	}
	e.enable("")
	if r, _ := (rpc{e.svc}).GetGlobalping(ownerCtx(), connect.NewRequest(&adminv1.GetGlobalpingRequest{})); r.Msg.Settings.HasToken {
		t.Fatal("the token came back after a reconnect")
	}
}

// A round in flight ends at once when the integration is switched off, and nothing reaches the server afterwards.
func TestSwitchingOffCancelsARoundInFlight(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	e.fake.keepRunning = true
	src := e.source()
	done := make(chan error, 1)
	go func() {
		_, err := src.Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"}))
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for e.fake.count() < 3 { // created, and read a few times while it never finishes
		if time.Now().After(deadline) {
			t.Fatal("the round never started")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := e.update(&adminv1.UpdateGlobalpingRequest{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, health.ErrCountryStopped) {
			t.Fatalf("err = %v, want ErrCountryStopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the round did not end")
	}
	// counted as the panel sends them: a GET written just before the switch-off may reach the server's log a moment after
	// the round has ended, and it was not sent afterwards
	n := e.fake.sent.Load()
	time.Sleep(50 * time.Millisecond)
	if e.fake.sent.Load() != n {
		t.Fatal("requests went on after the round ended")
	}
	// a source made before the switch-off does not start either
	if _, err := src.Measure(context.Background(), round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"})); !errors.Is(err, health.ErrCountryStopped) {
		t.Fatalf("an old source started a round: %v", err)
	}
	if e.fake.sent.Load() != n {
		t.Fatal("an old source made a request")
	}
}

func TestADeadlineEndsTheRoundWithTheContextError(t *testing.T) {
	e := newEnv(t)
	e.enable("")
	e.fake.keepRunning = true
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := e.source().Measure(ctx, round(health.CountryTarget{Key: "de1", IP: "203.0.113.10"}))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
}

// ---------------------------------------------------------------------------------------------------
// The scheduler of the health module, with this integration behind it.

type liveFleet struct{}

func (liveFleet) Live(context.Context) ([]store.NodeLiveRow, error) {
	return []store.NodeLiveRow{{NodeID: "de1", State: "active", Exists: true, Connected: true}}, nil
}
func (liveFleet) NodeLive(context.Context, string) (store.NodeLiveRow, error) {
	return store.NodeLiveRow{NodeID: "de1", State: "active", Exists: true, Connected: true}, nil
}
func (liveFleet) NodeStatusWithLive(context.Context, store.NodeRow, store.NodeLiveRow, map[string]store.NodeHealthSummary) adminv1.NodeStatus {
	return adminv1.NodeStatus_NODE_STATUS_ONLINE
}
func (liveFleet) RunDoctor(context.Context, string, []string, time.Duration) (*agentv1.DoctorReport, error) {
	return nil, errors.New("not used")
}
func (liveFleet) ApplyFix(context.Context, string, string, bool, map[string]string) (*agentv1.CommandResult, error) {
	return nil, errors.New("not used")
}
func (liveFleet) OnlineByInbound(context.Context) map[string]int { return nil }

func TestSwitchingItOffStopsTheScheduler(t *testing.T) {
	e := newEnv(t)
	now := e.now
	if _, err := e.st.W.ExecContext(context.Background(), `INSERT INTO node (id, name, address, provider, state, created_at, last_seen_at, last_connected_at)
		VALUES ('de1', 'de1', '203.0.113.10', 'hoster', 'active', 1, ?, ?)`, now.Unix(), now.Unix()); err != nil {
		t.Fatal(err)
	}
	e.fake.icmp["203.0.113.10"] = true
	clock := now
	hs := health.New(e.st, e.v, builtin.Registry(), liveFleet{}, health.Config{
		Country: e.svc.Country, Now: func() time.Time { return clock }, Log: slog.New(slog.NewTextHandler(e.logs, nil)),
		SnapshotTTL: time.Nanosecond, StartGrace: time.Nanosecond,
	})
	ctx := context.Background()

	// off by default: the scheduler does nothing and no request is made
	if hs.CountryTick(ctx) || e.fake.count() != 0 {
		t.Fatalf("a round ran while it was off (%d requests)", e.fake.count())
	}

	e.enable("")
	if !hs.CountryTick(ctx) {
		t.Fatal("no round after switching it on")
	}
	started := e.fake.count()
	if started == 0 {
		t.Fatal("the round made no request")
	}
	if rows, _ := e.st.RecentCountryChecks(ctx, 4, 5, "de1"); len(rows) != 1 || rows[0].Verdict != "ok" {
		t.Fatalf("rows = %+v", rows)
	}

	// switched off: the interval passes again and again, nothing starts and nothing reaches the server
	if _, err := e.update(&adminv1.UpdateGlobalpingRequest{Enabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		clock = clock.Add(time.Hour)
		if hs.CountryTick(ctx) {
			t.Fatal("a round ran after it was switched off")
		}
		hs.ReconcileCountry(ctx)
	}
	if e.fake.count() != started {
		t.Fatalf("%d requests reached the server after it was switched off", e.fake.count()-started)
	}
	// what was stored stays for the retention, but the API reports the check as off
	if rows, _ := e.st.RecentCountryChecks(ctx, 4, 5, "de1"); len(rows) != 1 {
		t.Fatalf("stored rounds = %+v, want them kept", rows)
	}

	// and on again it resumes
	e.enable("")
	if !hs.CountryTick(ctx) {
		t.Fatal("no round after switching it on again")
	}
}
