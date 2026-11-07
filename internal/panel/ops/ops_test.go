//go:build !js

package ops

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/gen/mistgate/ops/v1/opsv1connect"
	"github.com/mistgate/mistgate/internal/logring"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/reqid"
)

// Canaries: a secret SNI and path prefix, an address, a share link and a key-shaped value, a user name. None may leave the panel.
const (
	canarySNI    = "q7x9k2m4.canary-sni.example"
	canaryPrefix = "zz9plural9alpha9canary"
	canaryAddr   = "203.0.113.77"
	canaryLink   = "vless://11111111-2222-3333-4444-555555555555@node.example:443?security=reality#x"
	canaryKey    = "Zm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyZm9vYmFyMTI"
	canaryUser   = "canary-person-name"
)

type env struct {
	t     *testing.T
	st    *store.Store
	svc   *Service
	ring  *logring.Ring
	level *slog.LevelVar
	log   *slog.Logger
	cl    opsv1connect.OpsServiceClient
}

func newEnv(t *testing.T, mut ...func(*Config)) *env {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, ring, lv := logring.New(io.Discard, "text", slog.LevelInfo, 200)
	cfg := Config{Store: st, Ring: ring, Level: lv, DefaultLevel: slog.LevelInfo, Format: "text", Log: log,
		Secrets: []string{canarySNI, "q7x9k2m4", "/" + canaryPrefix + "/"}, Now: time.Now}
	for _, m := range mut {
		m(&cfg)
	}
	svc := New(cfg)
	path, h := svc.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &env{t: t, st: st, svc: svc, ring: ring, level: lv, log: log, cl: opsv1connect.NewOpsServiceClient(srv.Client(), srv.URL)}
}

func (e *env) logs(req *opsv1.GetPanelLogsRequest) *opsv1.GetPanelLogsResponse {
	e.t.Helper()
	r, err := e.cl.GetPanelLogs(context.Background(), connect.NewRequest(req))
	if err != nil {
		e.t.Fatal(err)
	}
	return r.Msg
}

func TestPanelLogsAreScrubbedAndFiltered(t *testing.T) {
	e := newEnv(t)
	e.log.Warn("fleet: dial failed to "+canaryAddr+":443 via "+canaryLink, "code", "dial_failed", "err", "lookup "+canarySNI+" at "+canaryAddr,
		"node", "de1", "user", canaryUser, "token", canaryKey, "path", "/"+canaryPrefix+"/sub", "ip", canaryAddr)
	e.log.Info("health: sweep done", "count", 3)
	e.log.Error("access: secret in text "+canaryKey+" and Bearer sk-ant-abcdefghij1234567890", "code", "access_failed", "reason", "q7x9k2m4 is the name")
	e.log.Debug("hidden at info")

	all := e.logs(&opsv1.GetPanelLogsRequest{})
	if len(all.Entries) != 3 {
		t.Fatalf("entries: %d, want 3 (debug is below the level)", len(all.Entries))
	}
	raw, _ := protoJSON(all)
	for _, canary := range []string{canarySNI, "q7x9k2m4", canaryPrefix, canaryAddr, "11111111-2222", canaryKey, canaryUser, "sk-ant-abcdefghij"} {
		if strings.Contains(raw, canary) {
			t.Errorf("panel_logs leaked %q:\n%s", canary, raw)
		}
	}
	// the allow-list: node and err stay, user, token and ip never appear
	var dial *opsv1.PanelLogEntry
	for _, en := range all.Entries {
		if en.Code == "dial_failed" {
			dial = en
		}
	}
	if dial == nil || dial.Attrs["node"] != "de1" || dial.Attrs["err"] == "" || dial.Component != "fleet" {
		t.Fatalf("dial entry: %+v", dial)
	}
	for _, k := range []string{"user", "token", "ip"} {
		if _, ok := dial.Attrs[k]; ok {
			t.Errorf("attribute %q is not on the allow-list but was returned", k)
		}
	}
	if !strings.Contains(dial.Attrs["err"], "<secret>") || !strings.Contains(dial.Attrs["err"], "<ip>") {
		t.Errorf("err attribute was not cleaned in place: %q", dial.Attrs["err"])
	}

	// a substring query is matched against the cleaned text: it cannot be used to find out whether a secret or address is in a record
	for _, probe := range []string{canarySNI, canaryAddr, canaryPrefix, "11111111"} {
		if got := e.logs(&opsv1.GetPanelLogsRequest{Query: probe}); len(got.Entries) != 0 {
			t.Errorf("query %q matched %d records through the raw text", probe, len(got.Entries))
		}
	}
	if got := e.logs(&opsv1.GetPanelLogsRequest{Query: "<secret>"}); len(got.Entries) != 2 {
		t.Errorf("query on the cleaned text: %d", len(got.Entries))
	}

	// filters: code, component, level, request id, limit and cursor
	if got := e.logs(&opsv1.GetPanelLogsRequest{Code: "ACCESS_FAILED"}); len(got.Entries) != 1 || got.Entries[0].Component != "access" {
		t.Errorf("by code: %+v", got.Entries)
	}
	if got := e.logs(&opsv1.GetPanelLogsRequest{Component: "health"}); len(got.Entries) != 1 {
		t.Errorf("by component: %+v", got.Entries)
	}
	if got := e.logs(&opsv1.GetPanelLogsRequest{MinLevel: "warn"}); len(got.Entries) != 2 {
		t.Errorf("level warn: %d", len(got.Entries))
	}
	if _, err := e.cl.GetPanelLogs(context.Background(), connect.NewRequest(&opsv1.GetPanelLogsRequest{MinLevel: "loud"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad level: %v", err)
	}
	e.log.InfoContext(reqid.With(context.Background(), "abc12345"), "http: request", "status", 200)
	if got := e.logs(&opsv1.GetPanelLogsRequest{RequestId: "abc12345"}); len(got.Entries) != 1 || got.Entries[0].RequestId != "abc12345" {
		t.Errorf("by request id: %+v", got.Entries)
	}
	first := e.logs(&opsv1.GetPanelLogsRequest{Limit: 2})
	if len(first.Entries) != 2 || first.NextCursor == 0 {
		t.Fatalf("first page: %+v", first)
	}
	second := e.logs(&opsv1.GetPanelLogsRequest{Limit: 2, BeforeSeq: first.NextCursor})
	if len(second.Entries) != 2 || second.Entries[0].Seq >= first.Entries[1].Seq || second.NextCursor != 0 {
		t.Fatalf("second page: %+v after %+v", second, first)
	}
}

func TestLogLevelIsAdjustableAndAudited(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s, err := e.cl.GetLogSettings(ctx, connect.NewRequest(&opsv1.GetLogSettingsRequest{}))
	if err != nil || s.Msg.Level != "info" || s.Msg.DefaultLevel != "info" || s.Msg.Capacity != 200 {
		t.Fatalf("settings: %+v %v", s, err)
	}
	if _, err := e.cl.SetLogLevel(ctx, connect.NewRequest(&opsv1.SetLogLevelRequest{Level: "verbose"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad level: %v", err)
	}
	e.log.Debug("before")
	if _, err := e.cl.SetLogLevel(ctx, connect.NewRequest(&opsv1.SetLogLevelRequest{Level: "debug"})); err != nil {
		t.Fatal(err)
	}
	e.log.Debug("after")
	got := e.logs(&opsv1.GetPanelLogsRequest{MinLevel: "debug"})
	var seen []string
	for _, en := range got.Entries {
		seen = append(seen, en.Message)
	}
	if got.Level != "debug" || !contains(seen, "after") || contains(seen, "before") {
		t.Fatalf("level %s, messages %v", got.Level, seen)
	}
	var n int
	if err := e.st.R.QueryRow(`SELECT count(*) FROM audit WHERE action = 'log_level_set'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows for the level change: %d %v", n, err)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestRetentionSettingsAreValidatedAndStored(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	get := func() map[string]uint32 {
		r, err := e.cl.GetRetention(ctx, connect.NewRequest(&opsv1.GetRetentionRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]uint32{}
		for _, i := range r.Msg.Items {
			out[i.Key] = i.Value
		}
		return out
	}
	d := get()
	if d["journal_days"] != 7 || d["events_info_days"] != 90 || d["events_warn_days"] != 400 || d["audit_days"] != 400 || d["alerts_days"] != 90 ||
		d["check_samples_hours"] != 25 || d["host_history_days"] != 90 || d["traffic_bucket_days"] != 400 || d["node_traffic_hour_days"] != 400 || len(d) != 10 {
		t.Fatalf("defaults: %v", d)
	}
	for name, vals := range map[string]map[string]uint32{
		"unknown":      {"nonsense": 5},
		"too short":    {"audit_days": 3},
		"too long":     {"journal_days": 30},
		"empty":        {},
		"one bad of 2": {"events_info_days": 30, "check_samples_hours": 1},
	} {
		if _, err := e.cl.SetRetention(ctx, connect.NewRequest(&opsv1.SetRetentionRequest{Values: vals})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := get(); got["events_info_days"] != 90 {
		t.Fatalf("a refused change was partly applied: %v", got)
	}
	if _, err := e.cl.SetRetention(ctx, connect.NewRequest(&opsv1.SetRetentionRequest{Values: map[string]uint32{"events_info_days": 30, "journal_days": 3}})); err != nil {
		t.Fatal(err)
	}
	if got := get(); got["events_info_days"] != 30 || got["journal_days"] != 3 || got["audit_days"] != 400 {
		t.Fatalf("after change: %v", got)
	}
	// back to the default removes the row
	if _, err := e.cl.SetRetention(ctx, connect.NewRequest(&opsv1.SetRetentionRequest{Values: map[string]uint32{"events_info_days": 90}})); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Setting(ctx, "retention.events_info_days"); err == nil {
		t.Fatal("the default is still stored as a row")
	}
}

func TestExportIsScrubbedNDJSON(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	now := time.Now()
	e.log.Warn("fleet: failed for "+canaryAddr, "code", "x_failed", "err", canarySNI, "user", canaryUser)
	if err := e.st.InsertEvent(ctx, store.EventRow{Time: now.Add(-time.Minute), Severity: 2, Code: "node_down", Source: "panel",
		Params: map[string]string{"error": "dial " + canaryAddr + ":22 " + canaryLink, "sni": canarySNI}}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.Audit(ctx, now.Add(-time.Minute), store.AuditEntry{Actor: "adm_x", Action: "settings.changed", Params: `{"note":"https://x.example/` + canaryPrefix + `/page?token=abc"}`, Result: "ok", IP: canaryAddr}); err != nil {
		t.Fatal(err)
	}
	stream, err := e.cl.ExportData(ctx, connect.NewRequest(&opsv1.ExportDataRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for stream.Receive() {
		out.Write(stream.Msg().Data)
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, canary := range []string{canarySNI, canaryPrefix, canaryAddr, "11111111-2222", canaryUser, "token=abc"} {
		if strings.Contains(body, canary) {
			t.Errorf("the export leaked %q:\n%s", canary, body)
		}
	}
	lines := strings.Split(strings.TrimSpace(body), "\n")
	sources := map[string]int{}
	for _, l := range lines {
		if !strings.HasPrefix(l, "{") || !strings.HasSuffix(l, "}") {
			t.Fatalf("not an NDJSON line: %q", l)
		}
		for _, s := range []string{"events", "audit", "logs", "export"} {
			if strings.Contains(l, `"source":"`+s+`"`) {
				sources[s]++
			}
		}
	}
	if sources["events"] != 1 || sources["audit"] < 1 || sources["logs"] != 1 || sources["export"] != 1 {
		t.Fatalf("rows per source: %v\n%s", sources, body)
	}
	// a too long window, an unknown source
	if s, err := e.cl.ExportData(ctx, connect.NewRequest(&opsv1.ExportDataRequest{SinceUnix: now.Add(-40 * 24 * time.Hour).Unix(), UntilUnix: now.Unix()})); err == nil {
		for s.Receive() {
		}
		if connect.CodeOf(s.Err()) != connect.CodeInvalidArgument {
			t.Errorf("40 days: %v", s.Err())
		}
	}
	if s, err := e.cl.ExportData(ctx, connect.NewRequest(&opsv1.ExportDataRequest{Sources: []string{"users"}})); err == nil {
		for s.Receive() {
		}
		if connect.CodeOf(s.Err()) != connect.CodeInvalidArgument {
			t.Errorf("unknown source: %v", s.Err())
		}
	}
}

// fakeFleet answers Overview with a node, a person's name and an address in fields the metrics must not copy.
type fakeFleet struct {
	adminv1connect.UnimplementedFleetServiceHandler
}

func (fakeFleet) Overview(context.Context, *connect.Request[adminv1.OverviewRequest]) (*connect.Response[adminv1.OverviewResponse], error) {
	return connect.NewResponse(&adminv1.OverviewResponse{
		NodesTotal: 2, UsersOnline: 7,
		Nodes: []*adminv1.NodeCard{
			{Id: "nod_1", Name: "de1", Status: adminv1.NodeStatus_NODE_STATUS_ONLINE, Location: canaryAddr, DownBps: 1000, UpBps: 500, HasMetrics: true, CpuPct: 12.5},
			{Id: "nod_2", Name: "nl1", Status: adminv1.NodeStatus_NODE_STATUS_DOWN},
		},
		TopConsumers: []*adminv1.TopConsumer{{UserId: "usr_1", UserName: canaryUser, NodeName: "de1", DownBps: 99}},
		Events:       []*adminv1.Event{{Code: "x", UserName: canaryUser}},
	}), nil
}

func TestMetricsAuthAndContent(t *testing.T) {
	var fleetH http.Handler
	e := newEnv(t, func(c *Config) {
		_, fleetH = adminv1connect.NewFleetServiceHandler(fakeFleet{})
		c.Fleet = fleetH
	})
	ctx := context.Background()
	handler := e.svc.MetricsHandler()
	serve := func(auth string, method string) (int, string, bool) {
		r := httptest.NewRequest(method, "/metrics", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		handled := handler(w, r)
		return w.Code, w.Body.String(), handled
	}

	// off by default: not handled at all, so the admin answers as for any unknown path
	if _, _, handled := serve("", http.MethodGet); handled {
		t.Fatal("metrics answered while off")
	}
	if _, err := e.cl.GetMetricsSettings(ctx, connect.NewRequest(&opsv1.GetMetricsSettingsRequest{})); err != nil {
		t.Fatal(err)
	}
	on := true
	r, err := e.cl.SetMetricsSettings(ctx, connect.NewRequest(&opsv1.SetMetricsSettingsRequest{Enabled: &on}))
	if err != nil || !r.Msg.Enabled || !strings.HasPrefix(r.Msg.Token, "mgm_") {
		t.Fatalf("enable: %+v %v", r, err)
	}
	token := r.Msg.Token
	// the secret is stored only as a hash
	if h, err := e.st.Setting(ctx, "metrics.token_hash"); err != nil || h == token || strings.Contains(h, token) {
		t.Fatalf("stored token hash: %q %v", h, err)
	}
	if g, _ := e.cl.GetMetricsSettings(ctx, connect.NewRequest(&opsv1.GetMetricsSettingsRequest{})); g == nil || !g.Msg.HasToken || !g.Msg.Enabled {
		t.Fatalf("settings: %+v", g)
	}

	for name, auth := range map[string]string{"no token": "", "wrong token": "Bearer mgm_wrong", "not bearer": "Basic " + token, "session-like": "Cookie " + token} {
		if code, body, handled := serve(auth, http.MethodGet); !handled || code != http.StatusUnauthorized || strings.Contains(body, "mistgate_") {
			t.Errorf("%s: %d %q handled=%v", name, code, body, handled)
		}
	}
	code, body, _ := serve("Bearer "+token, http.MethodGet)
	if code != http.StatusOK {
		t.Fatalf("good token: %d %s", code, body)
	}
	for _, want := range []string{
		"# TYPE mistgate_nodes gauge", `mistgate_nodes{status="online"} 1`, `mistgate_nodes{status="down"} 1`, `mistgate_nodes{status="pending"} 0`,
		"mistgate_users_online 7", `mistgate_node_down_bps{node="de1"} 1000`, `mistgate_node_up_bps{node="de1"} 500`, `mistgate_node_cpu_percent{node="de1"} 12.5`,
		"# EOF",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `node_cpu_percent{node="nl1"}`) {
		t.Error("a node without metrics got a cpu gauge")
	}
	// no person and no address
	for _, canary := range []string{canaryUser, canaryAddr, "usr_1", "nod_1", token} {
		if strings.Contains(body, canary) {
			t.Errorf("metrics expose %q:\n%s", canary, body)
		}
	}
	if code, _, _ := serve("Bearer "+token, http.MethodHead); code != http.StatusOK {
		t.Errorf("HEAD: %d", code)
	}
	if code, _, _ := serve("Bearer "+token, http.MethodPost); code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", code)
	}

	// rotating the token invalidates the old one; turning off stops the endpoint
	r2, err := e.cl.SetMetricsSettings(ctx, connect.NewRequest(&opsv1.SetMetricsSettingsRequest{RotateToken: true}))
	if err != nil || r2.Msg.Token == "" || r2.Msg.Token == token {
		t.Fatalf("rotate: %+v %v", r2, err)
	}
	if code, _, _ := serve("Bearer "+token, http.MethodGet); code != http.StatusUnauthorized {
		t.Errorf("the old token: %d", code)
	}
	if code, _, _ := serve("Bearer "+r2.Msg.Token, http.MethodGet); code != http.StatusOK {
		t.Errorf("the new token: %d", code)
	}
	off := false
	if _, err := e.cl.SetMetricsSettings(ctx, connect.NewRequest(&opsv1.SetMetricsSettingsRequest{Enabled: &off})); err != nil {
		t.Fatal(err)
	}
	if _, _, handled := serve("Bearer "+r2.Msg.Token, http.MethodGet); handled {
		t.Error("metrics answered after being turned off")
	}
	// the audit rows never hold a token
	rows, _ := e.st.ListAudit(ctx, "", 0, 50)
	for _, a := range rows {
		if strings.Contains(a.Params, "mgm_") {
			t.Errorf("audit row holds a token: %+v", a)
		}
	}
}

func protoJSON(m *opsv1.GetPanelLogsResponse) (string, error) {
	var b strings.Builder
	for _, e := range m.Entries {
		b.WriteString(e.String())
		b.WriteByte('\n')
	}
	return b.String(), nil
}
