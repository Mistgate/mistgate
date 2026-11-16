package ownprobe

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/ownprobe/v1/ownprobev1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	settingsEnabled    = "probes.enabled"
	settingsInterval   = "probes.interval_min"
	settingsFallback   = "probes.fallback_url"
	probeSyncBucket    = "own-probe-sync"
	defaultSyncBurst   = 3
	probeSyncRefill    = 5 * time.Minute
	defaultInterval    = 60
	defaultPollSeconds = 900
	probeTokenTTL      = 24 * time.Hour
)

// Target is a panel-selected destination. The public response never accepts or echoes a target from the probe.
type Target struct {
	NodeID string `json:"node_id"`
	IP     string `json:"ip"`
	Port   uint16 `json:"port"`
	Kind   string `json:"kind"`
	SNI    string `json:"sni,omitempty"`
	Path   string `json:"path,omitempty"`
}

// References are country-specific control addresses supplied by the existing reference source.
type References struct {
	ControlIP  string
	ControlSNI string
	BlockedIP  string
}

type Settings struct {
	Enabled     bool   `json:"enabled"`
	IntervalMin uint32 `json:"intervalMin"`
	FallbackURL string `json:"fallbackUrl"`
}

type Config struct {
	Store       *store.Store
	Limiter     securitylimit.Limiter
	StepUp      func(context.Context) error
	PublicURL   string
	ProbePrefix string
	Now         func() time.Time
	Targets     func(context.Context) ([]Target, error)
	Country     func(context.Context) string
	ASN         func(netip.Addr) uint32
	Operator    func(country string, asn uint32) string
	References  func(country string) (References, bool)
	OnChange    func(context.Context)
	SyncBurst   int // reports in a row one probe may send before the 5-minute refill; 0 = 3
	Log         *slog.Logger
}

// Service owns the admin and public surfaces. mu serializes report folds and revocation refolds in this process.
type Service struct {
	cfg       Config
	mu        sync.Mutex
	jobCursor int
}

func New(cfg Config) *Service {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Limiter == nil {
		cfg.Limiter = securitylimit.NewMemory(cfg.Now, 0)
	}
	if cfg.Country == nil {
		cfg.Country = func(context.Context) string { return countryreach.DefaultCountry }
	}
	if cfg.SyncBurst <= 0 {
		cfg.SyncBurst = defaultSyncBurst
	}
	return &Service{cfg: cfg}
}

func (s *Service) settings(ctx context.Context) (Settings, error) {
	out := Settings{IntervalMin: defaultInterval}
	kv, err := s.cfg.Store.SettingValues(ctx, []string{settingsEnabled, settingsInterval, settingsFallback})
	if err != nil {
		return out, err
	}
	out.Enabled = kv[settingsEnabled] == "1" || strings.EqualFold(kv[settingsEnabled], "true")
	if n, err := strconv.Atoi(kv[settingsInterval]); err == nil && n >= 30 && n <= 240 {
		out.IntervalMin = uint32(n)
	}
	out.FallbackURL = kv[settingsFallback]
	return out, nil
}

// SourceState tells the health module whether the "probes" source is on, and how old a point may be before it stops counting:
// three intervals.
func (s *Service) SourceState(ctx context.Context) (bool, time.Duration, error) {
	settings, err := s.settings(ctx)
	return settings.Enabled, time.Duration(settings.IntervalMin) * 3 * time.Minute, err
}

func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return ownprobev1connect.NewOwnProbeServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(16 << 10)}, opts...)...)
}

func (s *Service) stepUp(ctx context.Context) error {
	if s.cfg.StepUp == nil {
		return nil
	}
	return s.cfg.StepUp(ctx)
}

func (s *Service) changed(ctx context.Context) {
	if s.cfg.OnChange != nil {
		s.cfg.OnChange(ctx)
	}
}

func (s *Service) internal(message, code string, err error) error {
	s.cfg.Log.Error(message, "code", code, "err", err)
	return fail(connect.CodeInternal, "internal error")
}

// audit writes one audit row for an owner action. Params carry ids and labels only: never the token, the secret or the
// install command.
func (s *Service) audit(ctx context.Context, admin store.Admin, action string, params map[string]any) {
	raw := []byte("{}")
	if params != nil {
		raw, _ = json.Marshal(params)
	}
	if err := s.cfg.Store.Audit(ctx, s.cfg.Now(), store.AuditEntry{Actor: admin.ID, Action: action, Params: string(raw), Result: "ok"}); err != nil {
		s.cfg.Log.Warn("own probe audit", "code", "own_probe_audit_failed", "action", action, "err", err)
	}
}

func owner(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token || a.Role != store.RoleOwner {
		return a, fail(connect.CodePermissionDenied, "owner access required")
	}
	return a, nil
}

func fail(code connect.Code, message string) error {
	return connect.NewError(code, errors.New(message))
}

func mapStoreError(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fail(connect.CodeNotFound, "probe not found")
	}
	return fail(connect.CodeInternal, "internal error")
}

func validateFallbackURL(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return "", errors.New("invalid fallback URL")
	}
	return u.String(), nil
}

func safeLabel(value string, maxLen int) bool {
	if value == "" || len(value) > maxLen || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// BuildInstallCommand returns the one-time router command. The random token alphabet is restricted, and URLs are validated
// before shell quoting, so settings cannot turn the line into additional commands.
func BuildInstallCommand(publicURL, prefix, token, scriptHash string) (string, error) {
	u, err := url.Parse(publicURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		!validProbePrefix(prefix) || !validProbeToken(token) || !lowerHex(scriptHash, 64) {
		return "", errors.New("invalid install command input")
	}
	base := strings.TrimRight(u.String(), "/") + strings.TrimSuffix(prefix, "/") // the script adds /probe.sh, /enroll, /sync
	quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\\''") + "'" }
	return "opkg update && opkg install curl ca-bundle coreutils-sha256sum && cd /tmp && curl -fsSo mgp.sh " + quote(base+"/probe.sh") +
		" && echo " + quote(scriptHash+"  mgp.sh") + " | sha256sum -c - && sh mgp.sh install " + quote(base) + " " + quote(token), nil
}

func validProbePrefix(prefix string) bool {
	if len(prefix) != 26 || prefix[0] != '/' || prefix[len(prefix)-1] != '/' {
		return false
	}
	for _, c := range prefix[1 : len(prefix)-1] {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

func validProbeToken(token string) bool {
	if len(token) != 56 || !strings.HasPrefix(token, "mgp_") {
		return false
	}
	for _, c := range token[4:] {
		if !(c >= 'a' && c <= 'z' || c >= '2' && c <= '7') {
			return false
		}
	}
	return true
}

func randomID(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func tokenSecret() (string, []byte, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	secret := "mgps_" + base64.RawURLEncoding.EncodeToString(raw)
	h := sha256.Sum256([]byte(secret))
	return secret, h[:], nil
}

func hashCredential(secret string) []byte {
	h := sha256.Sum256([]byte(secret))
	return h[:]
}

func ipFor(r *http.Request) netip.Addr {
	ip := auth.ClientIPFrom(r.Context())
	if !ip.IsValid() {
		ip = auth.PeerIP(r)
	}
	return ip.Unmap()
}

func encodeJob(targets []Target) string {
	b, _ := json.Marshal(savedJob{Targets: targets})
	return string(b)
}

func parseJob(raw string) []Target {
	var job savedJob
	if json.Unmarshal([]byte(raw), &job) != nil {
		return nil
	}
	if len(job.Targets) > 32 {
		return nil
	}
	return job.Targets
}

// chooseTargets are the targets of a probe's next job: what the health module offers, in the shape the line protocol takes,
// at most maxTargets. A fleet bigger than one job is covered in turns, the job after the last one starting where it stopped.
func (s *Service) chooseTargets(ctx context.Context, version string) ([]Target, error) {
	if s.cfg.Targets == nil {
		return nil, nil
	}
	all, err := s.cfg.Targets(ctx)
	if err != nil {
		return nil, err
	}
	all = filterTargets(all, version)
	if len(all) <= maxTargets {
		return all, nil
	}
	start := s.jobCursor % len(all)
	s.jobCursor = (start + maxTargets) % len(all)
	out := make([]Target, 0, maxTargets)
	for i := 0; i < maxTargets; i++ {
		out = append(out, all[(start+i)%len(all)])
	}
	return out, nil
}

func slicesClone[T any](in []T) []T { return append([]T(nil), in...) }

func formatTargetLine(index int, t Target) string {
	sni, path := t.SNI, t.Path
	if sni == "" {
		sni = "-"
	}
	if path == "" {
		path = "-"
	}
	return fmt.Sprintf("t %d %s %d %s %s %s", index, t.IP, t.Port, t.Kind, sni, path)
}
