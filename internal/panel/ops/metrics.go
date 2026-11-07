//go:build !js

package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/buildinfo"
)

// The metrics endpoint: fleet gauges for a scraper, in the OpenMetrics text format. It is off until the owner turns it on
// (Settings -> System), answers 404 while off, and asks for a bearer secret of its own, made by the owner's request and stored
// only as a hash. It never names a person and never carries an address: gauges are counts and per-node numbers keyed by the
// node's name.

const (
	metricsEnabledKey   = "metrics.enabled"
	metricsTokenHashKey = "metrics.token_hash"
	metricsTokenPrefix  = "mgm_"
	metricsCacheFor     = 5 * time.Second // a scrape never reads the database more often than this
	contentType         = "application/openmetrics-text; version=1.0.0; charset=utf-8"
)

type metricsState struct {
	mu      sync.Mutex
	at      time.Time
	body    []byte
	hashHex string // last token hash seen, to drop the cache when the token changes
}

func (r rpc) GetMetricsSettings(ctx context.Context, _ *connect.Request[opsv1.GetMetricsSettingsRequest]) (*connect.Response[opsv1.GetMetricsSettingsResponse], error) {
	enabled, hash, err := r.s.metricsConfig(ctx)
	if err != nil {
		return nil, internalErr(r.s.cfg.Log, "read metrics settings", err)
	}
	return connect.NewResponse(&opsv1.GetMetricsSettingsResponse{Enabled: enabled, HasToken: hash != ""}), nil
}

func (r rpc) SetMetricsSettings(ctx context.Context, req *connect.Request[opsv1.SetMetricsSettingsRequest]) (*connect.Response[opsv1.SetMetricsSettingsResponse], error) {
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	enabled, hash, err := s.metricsConfig(ctx)
	if err != nil {
		return nil, internalErr(s.cfg.Log, "read metrics settings", err)
	}
	if req.Msg.Enabled != nil {
		enabled = *req.Msg.Enabled
	}
	token := ""
	if req.Msg.RotateToken || (enabled && hash == "") {
		token, hash = newMetricsToken()
	}
	kv := map[string]string{metricsEnabledKey: "0"}
	if enabled {
		kv[metricsEnabledKey] = "1"
	}
	if hash != "" {
		kv[metricsTokenHashKey] = hash
	}
	if err := s.cfg.Store.SetSettings(ctx, kv); err != nil {
		return nil, internalErr(s.cfg.Log, "save metrics settings", err)
	}
	s.m.mu.Lock()
	s.m.body = nil
	s.m.mu.Unlock()
	state := "off"
	if enabled {
		state = "on"
	}
	s.audit(ctx, "metrics_set", map[string]string{"state": state, "token_rotated": fmt.Sprint(token != "")})
	return connect.NewResponse(&opsv1.SetMetricsSettingsResponse{Enabled: enabled, HasToken: hash != "", Token: token}), nil
}

// newMetricsToken makes a secret (160 random bits) and its hash.
func newMetricsToken() (token, hash string) {
	var b [20]byte
	rand.Read(b[:])
	token = metricsTokenPrefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
	return token, hashToken(token)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Service) metricsConfig(ctx context.Context) (enabled bool, hash string, err error) {
	kv, err := s.cfg.Store.SettingValues(ctx, []string{metricsEnabledKey, metricsTokenHashKey})
	if err != nil {
		return false, "", err
	}
	return kv[metricsEnabledKey] == "1", kv[metricsTokenHashKey], nil
}

// MetricsHandler serves /metrics and reports whether it answered. Off, or with no token made yet, it writes nothing and returns
// false, so the admin answers as for any unknown path. A missing or wrong token: 401.
func (s *Service) MetricsHandler() func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		enabled, hash, err := s.metricsConfig(r.Context())
		if err != nil {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return true
		}
		if !enabled || hash == "" {
			return false
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := hashToken(strings.TrimSpace(got))
		if !ok || subtle.ConstantTimeCompare([]byte(sum), []byte(hash)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return true
		}
		body := s.metricsBody(r.Context(), hash)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
		return true
	}
}

func (s *Service) metricsBody(ctx context.Context, hash string) []byte {
	s.m.mu.Lock()
	defer s.m.mu.Unlock()
	now := s.cfg.Now()
	if s.m.body != nil && s.m.hashHex == hash && now.Sub(s.m.at) < metricsCacheFor {
		return s.m.body
	}
	s.m.body, s.m.at, s.m.hashHex = s.gather(ctx), now, hash
	return s.m.body
}

// inproc calls one admin service handler directly, with no socket and no session: the handlers are the raw services.
type inproc struct{ h http.Handler }

func (t inproc) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	r := req.Clone(req.Context())
	r.Host, r.RemoteAddr = "panel.invalid", "127.0.0.1:0"
	t.h.ServeHTTP(rec, r)
	resp := rec.Result()
	resp.Request = req
	return resp, nil
}

func client(h http.Handler) *http.Client { return &http.Client{Transport: inproc{h}} }

var (
	nodeStatuses = []string{"pending", "online", "down", "blip", "no_traffic", "updating"}
	checkKinds   = []string{"ok", "degraded", "failed", "unknown"}
)

// gather reads the fleet and renders the exposition. A source that cannot be read leaves its gauges out; the rest is served.
func (s *Service) gather(ctx context.Context) []byte {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var m expo
	m.gauge("mistgate_build_info", "Build of this panel; the value is always 1.", sample{labels: lbl("version", buildinfo.Version), v: 1})
	if s.cfg.Fleet != nil {
		fc := adminv1connect.NewFleetServiceClient(client(s.cfg.Fleet), "http://panel.invalid")
		if r, err := fc.Overview(ctx, connect.NewRequest(&adminv1.OverviewRequest{EventLimit: 1})); err == nil {
			byStatus := map[string]float64{}
			for _, n := range nodeStatuses {
				byStatus[n] = 0
			}
			var down, up, cpu []sample
			for _, n := range r.Msg.GetNodes() {
				st := enumName("NODE_STATUS_", n.GetStatus().String())
				byStatus[st]++
				l := lbl("node", n.GetName())
				down = append(down, sample{labels: l, v: float64(n.GetDownBps())})
				up = append(up, sample{labels: l, v: float64(n.GetUpBps())})
				if n.GetHasMetrics() {
					cpu = append(cpu, sample{labels: l, v: float64(n.GetCpuPct())})
				}
			}
			m.gauge("mistgate_nodes", "Nodes by status.", sorted(byStatus, "status")...)
			m.gauge("mistgate_users_online", "People online across the fleet.", sample{v: float64(r.Msg.GetUsersOnline())})
			m.gauge("mistgate_node_down_bps", "Current download speed of a node's people, bits per second.", down...)
			m.gauge("mistgate_node_up_bps", "Current upload speed of a node's people, bits per second.", up...)
			m.gauge("mistgate_node_cpu_percent", "CPU use of a node, percent.", cpu...)
		}
	}
	if s.cfg.Health != nil {
		hc := adminv1connect.NewHealthServiceClient(client(s.cfg.Health), "http://panel.invalid")
		if r, err := hc.ListAlerts(ctx, connect.NewRequest(&adminv1.ListAlertsRequest{HistoryWindowS: 1})); err == nil {
			bySev := map[string]float64{"info": 0, "warning": 0, "critical": 0}
			for _, a := range r.Msg.GetActive() {
				bySev[enumName("ALERT_SEVERITY_", a.GetSeverity().String())]++
			}
			m.gauge("mistgate_alerts_active", "Active alerts by severity.", sorted(bySev, "severity")...)
		}
		if r, err := hc.GetChecks(ctx, connect.NewRequest(&adminv1.GetChecksRequest{})); err == nil {
			by := map[string]float64{}
			for _, k := range checkKinds {
				by[k] = 0
			}
			for _, row := range r.Msg.GetRows() {
				for _, cell := range row.GetCells() {
					if !cell.GetDeployed() {
						continue
					}
					switch cell.GetLast().GetStatus() {
					case adminv1.CheckStatus_CHECK_STATUS_OK:
						by["ok"]++
					case adminv1.CheckStatus_CHECK_STATUS_DEGRADED:
						by["degraded"]++
					case adminv1.CheckStatus_CHECK_STATUS_FAILED:
						by["failed"]++
					default:
						by["unknown"]++
					}
				}
			}
			m.gauge("mistgate_checks", "Deployed synthetic checks by their latest result.", sorted(by, "result")...)
		}
	}
	if s.cfg.Relay != nil {
		rc := adminv1connect.NewRelayServiceClient(client(s.cfg.Relay), "http://panel.invalid")
		if r, err := rc.ListRelays(ctx, connect.NewRequest(&adminv1.ListRelaysRequest{})); err == nil {
			by := map[string]float64{"pending": 0, "active": 0, "failed": 0, "disabled": 0}
			for _, rel := range r.Msg.GetRelays() {
				by[enumName("INBOUND_STATE_", rel.GetState().String())]++
			}
			m.gauge("mistgate_relays", "Relays by state.", sorted(by, "state")...)
		}
	}
	return m.bytes()
}

func enumName(prefix, s string) string { return strings.ToLower(strings.TrimPrefix(s, prefix)) }

// A sample is one value of a gauge with its labels already rendered.
type sample struct {
	labels string
	v      float64
}

func lbl(k, v string) string { return k + `="` + escape(v) + `"` }

func sorted(m map[string]float64, label string) []sample {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]sample, len(keys))
	for i, k := range keys {
		out[i] = sample{labels: lbl(label, k), v: m[k]}
	}
	return out
}

// escape is the OpenMetrics label value escaping.
func escape(v string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(v)
}

type expo struct{ b strings.Builder }

func (e *expo) gauge(name, help string, samples ...sample) {
	if len(samples) == 0 {
		return
	}
	fmt.Fprintf(&e.b, "# TYPE %s gauge\n# HELP %s %s\n", name, name, help)
	for _, s := range samples {
		if s.labels == "" {
			fmt.Fprintf(&e.b, "%s %g\n", name, s.v)
		} else {
			fmt.Fprintf(&e.b, "%s{%s} %g\n", name, s.labels, s.v)
		}
	}
}

func (e *expo) bytes() []byte { e.b.WriteString("# EOF\n"); return []byte(e.b.String()) }
